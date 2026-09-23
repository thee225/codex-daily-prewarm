package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	stateVersion    = 1
	maxHistoryItems = 30
)

type runtimeState struct {
	Version    int                        `json:"version"`
	Daily      map[string]map[string]bool `json:"daily,omitempty"`
	History    []runRecord                `json:"history,omitempty"`
	LastSyncAt time.Time                  `json:"last_sync_at,omitempty"`
}

type runRecord struct {
	ID         string          `json:"id"`
	Job        string          `json:"job,omitempty"`
	Trigger    string          `json:"trigger"`
	Force      bool            `json:"force"`
	Date       string          `json:"date"`
	Model      string          `json:"model"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
	Expected   int             `json:"expected_accounts"`
	Discovered int             `json:"discovered_accounts"`
	Attempted  int             `json:"attempted_accounts"`
	Succeeded  int             `json:"succeeded_accounts"`
	Skipped    int             `json:"skipped_accounts"`
	Success    bool            `json:"success"`
	ErrorCode  string          `json:"error_code,omitempty"`
	Accounts   []accountResult `json:"accounts,omitempty"`
}

type accountResult struct {
	Account          string    `json:"account"`
	AccountType      string    `json:"account_type,omitempty"`
	Model            string    `json:"model"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	Attempts         int       `json:"attempts"`
	StatusCode       int       `json:"status_code,omitempty"`
	ResponseReceived bool      `json:"response_received"`
	PrimaryResetAt   time.Time `json:"primary_reset_at,omitempty"`
	PrimaryUsed      string    `json:"primary_used_percent,omitempty"`
	ErrorCode        string    `json:"error_code,omitempty"`
}

type runtimeStatus struct {
	Plugin      string       `json:"plugin"`
	Version     string       `json:"version"`
	Config      publicConfig `json:"config"`
	Running     bool         `json:"running"`
	NextRunAt   time.Time    `json:"next_run_at,omitempty"`
	LastError   string       `json:"last_error,omitempty"`
	LastRun     *runRecord   `json:"last_run,omitempty"`
	HistorySize int          `json:"history_size"`
	NextRuns    []jobNextRun `json:"next_runs,omitempty"`
	LastSyncAt  time.Time    `json:"last_sync_at,omitempty"`
}

type jobNextRun struct {
	Name      string    `json:"name"`
	Schedule  string    `json:"schedule"`
	Model     string    `json:"model"`
	NextRunAt time.Time `json:"next_run_at"`
}

type runRequest struct {
	Trigger      string
	Job          prewarmJob
	Force        bool
	SourceAuthID string
}

type runtime struct {
	mu        sync.RWMutex
	persistMu sync.Mutex
	cfg       pluginConfig
	cron      *cron.Cron
	state     runtimeState
	running   bool
	pending   []runRequest
	closed    bool
	lastError string
}

type authListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type hostLogRequest struct {
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

func newRuntime() *runtime {
	cfg := defaultPluginConfig()
	return &runtime{cfg: cfg, state: newState()}
}

func newState() runtimeState {
	return runtimeState{Version: stateVersion, Daily: make(map[string]map[string]bool)}
}

func (r *runtime) configure(raw []byte) error {
	cfg, err := parsePluginConfig(raw)
	if err != nil {
		return err
	}
	state, stateErr := readState(cfg.StatePath)
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return fmt.Errorf("load state: %w", stateErr)
	}

	r.mu.Lock()
	previous := r.cron
	r.cron = nil
	r.cfg = cfg
	r.state = state
	r.closed = false
	r.lastError = ""
	if cfg.AutomaticEnabled {
		c := cron.New(cron.WithLocation(cfg.Location))
		for _, job := range cfg.Jobs {
			job := job
			if _, err := c.AddFunc(job.Schedule, func() {
				if err := r.startRunJob(runRequest{Trigger: "schedule", Job: job}); err != nil {
					r.setLastError(safeErrorCode(err))
				}
			}); err != nil {
				r.mu.Unlock()
				return fmt.Errorf("register job %s: %w", job.Name, err)
			}
		}
		r.cron = c
		c.Start()
	}
	r.mu.Unlock()
	if previous != nil {
		previous.Stop()
	}
	return nil
}

func (r *runtime) shutdown() {
	r.mu.Lock()
	r.closed = true
	c := r.cron
	r.cron = nil
	r.mu.Unlock()
	if c != nil {
		c.Stop()
	}
}

var errAlreadyRunning = errors.New("prewarm is already running")
var errJobNotFound = errors.New("prewarm job not found")

func (r *runtime) startRun(trigger string, force bool) error {
	return r.startNamedRun(trigger, "", force)
}

func (r *runtime) startNamedRun(trigger, name string, force bool) error {
	r.mu.RLock()
	jobs := r.cfg.Jobs
	r.mu.RUnlock()
	if name == "" && len(jobs) > 0 {
		name = jobs[0].Name
	}
	for _, job := range jobs {
		if job.Name == name {
			return r.startRunJob(runRequest{Trigger: trigger, Job: job, Force: force})
		}
	}
	return errJobNotFound
}

func (r *runtime) startRunJob(request runRequest) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("plugin is shutting down")
	}
	if r.running {
		if request.Trigger == "schedule" || request.Trigger == "first_use" {
			r.pending = append(r.pending, request)
			r.mu.Unlock()
			return nil
		}
		r.mu.Unlock()
		return errAlreadyRunning
	}
	r.running = true
	r.lastError = ""
	r.mu.Unlock()

	go r.runQueue(request)
	return nil
}

func (r *runtime) runQueue(request runRequest) {
	for {
		r.mu.RLock()
		cfg := r.cfg
		r.mu.RUnlock()
		record := r.executeRun(cfg, request)
		r.mu.Lock()
		if record.ErrorCode != "" {
			r.lastError = record.ErrorCode
		}
		if len(r.pending) == 0 || r.closed {
			r.pending = nil
			r.running = false
			r.mu.Unlock()
			return
		}
		request = r.pending[0]
		r.pending = r.pending[1:]
		r.mu.Unlock()
	}
}

func (r *runtime) executeRun(cfg pluginConfig, request runRequest) runRecord {
	now := time.Now().In(cfg.Location)
	record := runRecord{
		ID:        fmt.Sprintf("%d", now.UnixNano()),
		Job:       request.Job.Name,
		Trigger:   request.Trigger,
		Force:     request.Force,
		Date:      now.Format("2006-01-02"),
		Model:     request.Job.Model,
		StartedAt: now,
		Expected:  cfg.ExpectedAccountCount,
	}
	defer func() {
		record.FinishedAt = time.Now().In(cfg.Location)
		record.Success = record.ErrorCode == "" && record.Succeeded+record.Skipped == record.Discovered && record.Discovered > 0
		r.appendRun(cfg, record)
	}()

	auths, err := listCodexAuths()
	if err != nil {
		record.ErrorCode = "auth_inventory_unavailable"
		logHost("error", "codex daily prewarm could not list auths", map[string]any{"error_code": record.ErrorCode})
		return record
	}
	record.Discovered = len(auths)
	inventoryMismatch := cfg.ExpectedAccountCount > 0 && len(auths) != cfg.ExpectedAccountCount
	if abortForAccountCount(cfg.ExpectedAccountCount, len(auths), request.Trigger) {
		record.ErrorCode = "unexpected_account_count"
		logHost("error", "codex daily prewarm account count mismatch", map[string]any{"expected": cfg.ExpectedAccountCount, "discovered": len(auths)})
		return record
	}
	if inventoryMismatch {
		logHost("warn", "codex first-use sync has fewer available accounts than expected", map[string]any{"expected": cfg.ExpectedAccountCount, "discovered": len(auths)})
	}
	if len(auths) == 0 {
		record.ErrorCode = "no_eligible_codex_accounts"
		return record
	}

	for index, auth := range auths {
		fingerprint := accountFingerprint(auth.ID)
		if auth.ID == request.SourceAuthID || (!request.Force && r.completedToday(record.Date, request.Job.Name, fingerprint)) {
			record.Skipped++
			continue
		}
		if record.Attempted > 0 && cfg.AccountSpacing > 0 {
			time.Sleep(cfg.AccountSpacing)
		}
		result := executeAccount(cfg, request.Job, auth)
		record.Attempted++
		record.Accounts = append(record.Accounts, result)
		if result.ResponseReceived {
			record.Succeeded++
			r.markCompleted(cfg, record.Date, request.Job.Name, fingerprint)
		}
		logHost("info", "codex daily prewarm account finished", map[string]any{
			"account": fingerprint, "status": result.StatusCode, "response_received": result.ResponseReceived,
			"error_code": result.ErrorCode, "position": index + 1, "total": len(auths),
		})
	}
	if inventoryMismatch {
		record.ErrorCode = "unexpected_account_count"
	} else if record.Succeeded+record.Skipped != record.Discovered {
		record.ErrorCode = "one_or_more_accounts_failed"
	}
	return record
}

func abortForAccountCount(expected, discovered int, trigger string) bool {
	if expected <= 0 || discovered == expected {
		return false
	}
	return trigger != "first_use" || discovered > expected
}

func listCodexAuths() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if err != nil {
		return nil, err
	}
	var response authListResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode auth list: %w", err)
	}
	return eligibleCodexAuths(response.Files), nil
}

func eligibleCodexAuths(files []pluginapi.HostAuthFileEntry) []pluginapi.HostAuthFileEntry {
	eligible := make([]pluginapi.HostAuthFileEntry, 0, len(files))
	for _, auth := range files {
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		authType := strings.ToLower(strings.TrimSpace(auth.Type))
		if provider != "codex" && authType != "codex" {
			continue
		}
		if auth.Disabled || auth.Unavailable || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(auth.Status))
		if status != "" && status != "active" && status != "ready" {
			continue
		}
		eligible = append(eligible, auth)
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	return eligible
}

func executeAccount(cfg pluginConfig, job prewarmJob, auth pluginapi.HostAuthFileEntry) accountResult {
	result := accountResult{
		Account:     accountFingerprint(auth.ID),
		AccountType: safeAccountType(auth.AccountType),
		Model:       job.Model,
		StartedAt:   time.Now().In(cfg.Location),
	}
	body, err := json.Marshal(chatCompletionRequest{
		Model:    job.Model,
		Messages: []chatMessage{{Role: "user", Content: job.Prompt}},
	})
	if err != nil {
		result.ErrorCode = "request_encoding_failed"
		result.FinishedAt = time.Now().In(cfg.Location)
		return result
	}

	for attempt := 0; attempt <= cfg.RetryCount; attempt++ {
		result.Attempts++
		raw, callErr := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
			HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
				EntryProtocol:  "openai",
				ExitProtocol:   "openai",
				Model:          job.Model,
				Stream:         false,
				Body:           body,
				Headers:        http.Header{"Content-Type": []string{"application/json"}},
				ForcedProvider: "codex",
				AuthID:         auth.ID,
			},
		})
		if callErr != nil {
			var hostErr *hostCallbackError
			if errors.As(callErr, &hostErr) && hostErr.HTTPStatus > 0 {
				result.StatusCode = hostErr.HTTPStatus
				result.PrimaryResetAt = upstreamResetAt(hostErr.Message, time.Now())
				result.ErrorCode = upstreamErrorCode(hostErr.Message, hostErr.HTTPStatus)
				if attempt >= cfg.RetryCount || !safeToRetryStatus(hostErr.HTTPStatus) {
					break
				}
				time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
				continue
			}
			// The host callback may have reached the provider before returning an
			// error, so repeating it could consume twice. Treat it as uncertain.
			result.ErrorCode = "model_execution_uncertain"
			break
		}
		var response pluginapi.HostModelExecutionResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			result.ErrorCode = "model_response_decode_failed"
			break
		}
		result.StatusCode = response.StatusCode
		result.PrimaryResetAt = primaryResetAt(response.Headers, time.Now())
		result.PrimaryUsed = boundedHeader(response.Headers, "x-codex-primary-used-percent")
		if response.StatusCode >= 200 && response.StatusCode < 300 && validModelResponse(response.Body) {
			result.ResponseReceived = true
			result.ErrorCode = ""
			break
		}
		result.ErrorCode = statusErrorCode(response.StatusCode)
		if attempt >= cfg.RetryCount || !safeToRetryStatus(response.StatusCode) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	result.FinishedAt = time.Now().In(cfg.Location)
	return result
}

func validModelResponse(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 4<<20 || !json.Valid(raw) {
		return false
	}
	var payload struct {
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	for _, choice := range payload.Choices {
		switch value := choice.Message.Content.(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return true
			}
		case []any:
			if len(value) > 0 {
				return true
			}
		}
	}
	for _, output := range payload.Output {
		for _, content := range output.Content {
			if strings.TrimSpace(content.Text) != "" {
				return true
			}
		}
	}
	return strings.EqualFold(strings.TrimSpace(payload.Status), "completed")
}

func primaryResetAt(headers http.Header, now time.Time) time.Time {
	if raw := boundedHeader(headers, "x-codex-primary-reset-at"); raw != "" {
		if unix, err := strconv.ParseInt(raw, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0).UTC()
		}
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			return parsed.UTC()
		}
	}
	if raw := boundedHeader(headers, "x-codex-primary-reset-after-seconds"); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 && seconds <= 7*24*60*60 {
			return now.Add(time.Duration(seconds) * time.Second).UTC()
		}
	}
	return time.Time{}
}

func upstreamResetAt(message string, now time.Time) time.Time {
	if len(message) == 0 || len(message) > 16<<10 || !json.Valid([]byte(message)) {
		return time.Time{}
	}
	var payload struct {
		Error struct {
			ResetsAt        int64 `json:"resets_at"`
			ResetsInSeconds int64 `json:"resets_in_seconds"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(message), &payload); err != nil {
		return time.Time{}
	}
	if payload.Error.ResetsAt > 0 {
		return time.Unix(payload.Error.ResetsAt, 0).UTC()
	}
	if seconds := payload.Error.ResetsInSeconds; seconds > 0 && seconds <= 45*24*60*60 {
		return now.Add(time.Duration(seconds) * time.Second).UTC()
	}
	return time.Time{}
}

func upstreamErrorCode(message string, status int) string {
	if status == http.StatusTooManyRequests && len(message) <= 16<<10 && json.Valid([]byte(message)) {
		var payload struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(message), &payload) == nil && payload.Error.Type == "usage_limit_reached" {
			return "usage_limit_reached"
		}
	}
	return statusErrorCode(status)
}

func boundedHeader(headers http.Header, name string) string {
	value := strings.TrimSpace(headers.Get(name))
	if len(value) > 128 {
		return ""
	}
	return value
}

func statusErrorCode(status int) string {
	if status <= 0 {
		return "model_response_invalid"
	}
	return fmt.Sprintf("http_%d", status)
}

func safeToRetryStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func safeAccountType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "plus", "team", "business", "enterprise", "edu", "pro", "free":
		return value
	default:
		return ""
	}
}

func accountFingerprint(authID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(authID)))
	return "acct-" + hex.EncodeToString(sum[:6])
}

func safeErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errAlreadyRunning) {
		return "already_running"
	}
	return "runtime_error"
}

func dailyKey(job, account string) string {
	if job == "default" {
		return account // Preserve the v0.1 state for the original daily job.
	}
	return job + "/" + account
}

func (r *runtime) completedToday(date, job, account string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.Daily[date] != nil && r.state.Daily[date][dailyKey(job, account)]
}

func (r *runtime) markCompleted(cfg pluginConfig, date, job, account string) {
	r.mu.Lock()
	if r.state.Daily == nil {
		r.state.Daily = make(map[string]map[string]bool)
	}
	if r.state.Daily[date] == nil {
		r.state.Daily[date] = make(map[string]bool)
	}
	r.state.Daily[date][dailyKey(job, account)] = true
	pruneDaily(r.state.Daily, time.Now().In(cfg.Location))
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("state_write_failed")
	}
}

func (r *runtime) appendRun(cfg pluginConfig, record runRecord) {
	r.mu.Lock()
	r.state.History = append([]runRecord{record}, r.state.History...)
	if len(r.state.History) > maxHistoryItems {
		r.state.History = r.state.History[:maxHistoryItems]
	}
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("state_write_failed")
		logHost("error", "codex daily prewarm state write failed", map[string]any{"error_code": "state_write_failed"})
	}
}

func (r *runtime) persistState(path string) error {
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	r.mu.RLock()
	state := cloneState(r.state)
	r.mu.RUnlock()
	return writeState(path, state)
}

func (r *runtime) setLastError(code string) {
	r.mu.Lock()
	r.lastError = code
	r.mu.Unlock()
}

func (r *runtime) status() runtimeStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	status := runtimeStatus{
		Plugin: pluginName, Version: pluginVersion, Config: r.cfg.public(), Running: r.running,
		LastError: r.lastError, HistorySize: len(r.state.History), LastSyncAt: r.state.LastSyncAt,
	}
	if r.cfg.AutomaticEnabled && r.cfg.Location != nil {
		for _, job := range r.cfg.Jobs {
			next := job.CronSchedule.Next(time.Now().In(r.cfg.Location))
			status.NextRuns = append(status.NextRuns, jobNextRun{Name: job.Name, Schedule: job.Schedule, Model: job.Model, NextRunAt: next})
			if status.NextRunAt.IsZero() || next.Before(status.NextRunAt) {
				status.NextRunAt = next
			}
		}
	}
	if len(r.state.History) > 0 {
		latest := r.state.History[0]
		status.LastRun = &latest
	}
	return status
}

func (r *runtime) history() []runRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]runRecord(nil), r.state.History...)
}

func readState(path string) (runtimeState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newState(), err
		}
		return runtimeState{}, err
	}
	if len(raw) > 2<<20 {
		return runtimeState{}, fmt.Errorf("state exceeds 2 MiB")
	}
	var state runtimeState
	if err := json.Unmarshal(raw, &state); err != nil {
		return runtimeState{}, err
	}
	if state.Version != stateVersion {
		return runtimeState{}, fmt.Errorf("unsupported state version %d", state.Version)
	}
	if state.Daily == nil {
		state.Daily = make(map[string]map[string]bool)
	}
	if len(state.History) > maxHistoryItems {
		state.History = state.History[:maxHistoryItems]
	}
	return state, nil
}

func writeState(path string, state runtimeState) error {
	state.Version = stateVersion
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".codex-daily-prewarm-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func cloneState(state runtimeState) runtimeState {
	clone := runtimeState{Version: state.Version, Daily: make(map[string]map[string]bool), History: append([]runRecord(nil), state.History...), LastSyncAt: state.LastSyncAt}
	for date, accounts := range state.Daily {
		clone.Daily[date] = make(map[string]bool, len(accounts))
		for account, done := range accounts {
			clone.Daily[date][account] = done
		}
	}
	return clone
}

// handleUsage starts one synchronization round after the first successful
// authenticated client request in a five-hour window. Host model callbacks
// have no frontend API key, so this plugin cannot trigger itself recursively.
func (r *runtime) handleUsage(record pluginapi.UsageRecord) {
	if !eligibleFirstUse(record) {
		return
	}
	r.mu.Lock()
	cfg := r.cfg
	if r.closed || !cfg.SyncOnFirstUse || firstUseCoolingDown(r.state, time.Now()) {
		r.mu.Unlock()
		return
	}
	job := prewarmJob{Name: "first-use", Model: cfg.Model, Prompt: cfg.Prompt}
	r.state.LastSyncAt = time.Now().In(cfg.Location)
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("state_write_failed")
		logHost("error", "codex daily prewarm state write failed", map[string]any{"error_code": "state_write_failed"})
	}
	if err := r.startRunJob(runRequest{Trigger: "first_use", Job: job, Force: true, SourceAuthID: record.AuthID}); err != nil {
		r.setLastError(safeErrorCode(err))
	}
}

func firstUseCoolingDown(state runtimeState, now time.Time) bool {
	if state.LastSyncAt.IsZero() {
		return false
	}
	elapsed := now.Sub(state.LastSyncAt)
	if elapsed >= 5*time.Hour {
		return false
	}
	if len(state.History) > 0 {
		latest := state.History[0]
		if latest.Trigger == "first_use" && latest.Attempted == 0 && latest.ErrorCode == "unexpected_account_count" {
			return elapsed < 5*time.Minute
		}
	}
	return true
}

func eligibleFirstUse(record pluginapi.UsageRecord) bool {
	return strings.TrimSpace(record.APIKey) != "" && strings.EqualFold(record.Provider, "codex") && !record.Failed && record.Generate && strings.TrimSpace(record.AuthID) != ""
}

func pruneDaily(daily map[string]map[string]bool, now time.Time) {
	cutoff := now.AddDate(0, 0, -14).Format("2006-01-02")
	for date := range daily {
		if date < cutoff {
			delete(daily, date)
		}
	}
}

func logHost(level, message string, fields map[string]any) {
	_, _ = callHost(pluginabi.MethodHostLog, hostLogRequest{Level: level, Message: message, Fields: fields})
}
