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
	Accounts   map[string]accountWindow   `json:"accounts,omitempty"`
	Slots      map[string]scheduledSlot   `json:"slots,omitempty"`
	History    []runRecord                `json:"history,omitempty"`
	LastSyncAt time.Time                  `json:"last_sync_at,omitempty"`
}

// The old daily map remains readable for rollback, but decisions use the
// observed, per-account reset time. A missing reset is never called confirmed.
type accountWindow struct {
	LastQuotaCheckedAt time.Time   `json:"last_quota_checked_at,omitempty"`
	QuotaStatus        string      `json:"quota_status,omitempty"`
	AttemptTimes       []time.Time `json:"attempt_times,omitempty"`
	ResetAt            time.Time   `json:"reset_at,omitempty"`
	LastProbeAt        time.Time   `json:"last_probe_at,omitempty"`
	RetryAfter         time.Time   `json:"retry_after,omitempty"`
	LastSyncAt         time.Time   `json:"last_sync_at,omitempty"`
	LastSyncResetAt    time.Time   `json:"last_sync_reset_at,omitempty"`
	LastPrewarmAt      time.Time   `json:"last_prewarm_at,omitempty"`
	LastPrewarmResetAt time.Time   `json:"last_prewarm_reset_at,omitempty"`
	FiveHour           quotaWindow `json:"five_hour,omitempty"`
	Weekly             quotaWindow `json:"weekly,omitempty"`
	FiveHourFollowupAt time.Time   `json:"five_hour_followup_at,omitempty"`
	FiveHourFollowupR  time.Time   `json:"five_hour_followup_reset_at,omitempty"`
	LastFiveHourR      time.Time   `json:"last_five_hour_followup_reset_at,omitempty"`
	WeeklyFollowupAt   time.Time   `json:"weekly_followup_at,omitempty"`
	WeeklyFollowupR    time.Time   `json:"weekly_followup_reset_at,omitempty"`
	LastWeeklyR        time.Time   `json:"last_weekly_followup_reset_at,omitempty"`
	QuotaRetryAt       time.Time   `json:"quota_retry_at,omitempty"`
	QuotaRetryStep     int         `json:"quota_retry_step,omitempty"`
}

type runRecord struct {
	ID                 string          `json:"id"`
	Job                string          `json:"job,omitempty"`
	Trigger            string          `json:"trigger"`
	Slot               string          `json:"slot,omitempty"`
	PlannedAt          time.Time       `json:"planned_at,omitempty"`
	JitterSeconds      int             `json:"jitter_seconds,omitempty"`
	TargetAccount      string          `json:"target_account,omitempty"`
	FollowupKind       string          `json:"followup_kind,omitempty"`
	SourceAccount      string          `json:"source_account,omitempty"`
	Force              bool            `json:"force"`
	Date               string          `json:"date"`
	Model              string          `json:"model"`
	StartedAt          time.Time       `json:"started_at"`
	FinishedAt         time.Time       `json:"finished_at,omitempty"`
	Expected           int             `json:"expected_accounts"`
	Discovered         int             `json:"discovered_accounts"`
	Attempted          int             `json:"attempted_accounts"`
	QuotaQueried       int             `json:"quota_queried_accounts"`
	WouldWarm          int             `json:"would_warm_accounts"`
	BarkStatus         string          `json:"bark_status,omitempty"`
	Succeeded          int             `json:"succeeded_accounts"`
	Skipped            int             `json:"skipped_accounts"`
	ObservedResetCount int             `json:"observed_reset_accounts"`
	ResetSpreadSeconds *int64          `json:"reset_spread_seconds,omitempty"`
	Success            bool            `json:"success"`
	ErrorCode          string          `json:"error_code,omitempty"`
	Accounts           []accountResult `json:"accounts,omitempty"`
}

type accountResult struct {
	Account          string      `json:"account"`
	AccountType      string      `json:"account_type,omitempty"`
	Model            string      `json:"model"`
	StartedAt        time.Time   `json:"started_at"`
	FinishedAt       time.Time   `json:"finished_at"`
	Attempts         int         `json:"attempts"`
	QuotaCheckedAt   time.Time   `json:"quota_checked_at,omitempty"`
	QuotaStatus      string      `json:"quota_status,omitempty"`
	Attempts24h      int         `json:"attempts_24h"`
	WouldWarm        bool        `json:"would_warm,omitempty"`
	StatusCode       int         `json:"status_code,omitempty"`
	ResponseReceived bool        `json:"response_received"`
	FallbackUsed     bool        `json:"fallback_used,omitempty"`
	SkipReason       string      `json:"skip_reason,omitempty"`
	PrimaryResetAt   time.Time   `json:"primary_reset_at,omitempty"`
	PrimaryUsed      string      `json:"primary_used_percent,omitempty"`
	FiveHour         quotaWindow `json:"five_hour,omitempty"`
	Weekly           quotaWindow `json:"weekly,omitempty"`
	ErrorCode        string      `json:"error_code,omitempty"`
}

type runtimeStatus struct {
	Plugin      string                   `json:"plugin"`
	Version     string                   `json:"version"`
	Config      publicConfig             `json:"config"`
	Running     bool                     `json:"running"`
	NextRunAt   time.Time                `json:"next_run_at,omitempty"`
	LastError   string                   `json:"last_error,omitempty"`
	LastRun     *runRecord               `json:"last_run,omitempty"`
	HistorySize int                      `json:"history_size"`
	NextRuns    []jobNextRun             `json:"next_runs,omitempty"`
	LastSyncAt  time.Time                `json:"last_sync_at,omitempty"`
	Accounts    map[string]accountWindow `json:"accounts,omitempty"`
}

type jobNextRun struct {
	Name      string    `json:"name"`
	Schedule  string    `json:"schedule"`
	Model     string    `json:"model"`
	NextRunAt time.Time `json:"next_run_at"`
}

type runRequest struct {
	Generation    uint64
	Trigger       string
	Job           prewarmJob
	Force         bool
	TargetAccount string
	FollowupKind  string
	DueAt         time.Time
	Slot          string
	SlotBase      time.Time
	PlannedAt     time.Time
	JitterSeconds int
	ReadOnly      bool
	ObserveOnly   bool
	Notify        bool
}

type runtime struct {
	lifecycleMu    sync.Mutex
	terminal       bool // guarded by lifecycleMu; native shutdown cannot be reopened
	mu             sync.RWMutex
	runWG          sync.WaitGroup
	generation     uint64
	persistMu      sync.Mutex
	cfg            pluginConfig
	cron           *cron.Cron
	scheduler      *runtimeScheduler
	currentRequest runRequest
	stopCh         chan struct{}
	state          runtimeState
	running        bool
	pending        []runRequest
	closed         bool
	lastError      string
	runTask        func(pluginConfig, runRequest) runRecord // test seam; nil uses executeRun
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
	return &runtime{cfg: cfg, state: newState(), generation: 1, stopCh: make(chan struct{})}
}

func newState() runtimeState {
	return runtimeState{Version: stateVersion, Daily: make(map[string]map[string]bool), Accounts: make(map[string]accountWindow), Slots: make(map[string]scheduledSlot)}
}

func (r *runtime) configure(raw []byte) error {
	cfg, err := parsePluginConfig(raw)
	if err != nil {
		return err
	}
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.terminal {
		return errors.New("plugin has been shut down")
	}
	r.mu.RLock()
	oldPath := r.cfg.StatePath
	hadState := len(r.state.Accounts) > 0 || len(r.state.Slots) > 0 || len(r.state.History) > 0 || len(r.state.Daily) > 0
	r.mu.RUnlock()
	// Validate a different target before stopping the healthy old scheduler.
	// The authoritative snapshot is still read after quiesce drains old runs.
	if cfg.StatePath != oldPath {
		if _, err := readState(cfg.StatePath); err != nil && (!errors.Is(err, os.ErrNotExist) || hadState) {
			if _, oldErr := readState(oldPath); oldErr == nil || errors.Is(oldErr, os.ErrNotExist) {
				return fmt.Errorf("load state: %w", err)
			}
			r.quiesce() // The old state is also unreadable: fail closed.
			return fmt.Errorf("load state: %w", err)
		}
	}
	r.quiesce()
	r.mu.RLock()
	hadState = len(r.state.Accounts) > 0 || len(r.state.Slots) > 0 || len(r.state.History) > 0 || len(r.state.Daily) > 0
	r.mu.RUnlock()
	state, stateErr := readState(cfg.StatePath)
	if errors.Is(stateErr, os.ErrNotExist) && hadState {
		return fmt.Errorf("load state: missing state file would discard existing account history")
	}
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return fmt.Errorf("load state: %w", stateErr)
	}
	r.mu.Lock()
	r.cfg = cfg
	r.state = state
	r.closed = false
	r.stopCh = make(chan struct{})
	r.lastError = ""
	r.mu.Unlock()
	if cfg.AutomaticEnabled {
		scheduler := newRuntimeScheduler(r)
		c := cron.New(cron.WithLocation(cfg.Location))
		for _, job := range cfg.Jobs {
			job := job
			if _, err := c.AddFunc(job.Schedule, func() {
				scheduler.planSlot(job, latestCronSlot(job.CronSchedule, time.Now().In(cfg.Location)), time.Now())
			}); err != nil {
				return fmt.Errorf("register job %s: %w", job.Name, err)
			}
		}
		r.mu.Lock()
		r.cron = c
		r.scheduler = scheduler
		r.mu.Unlock()
		scheduler.bootstrap(cfg, time.Now())
		c.Start()
	}
	return nil
}

func (r *runtime) shutdown() {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	r.terminal = true
	r.quiesce()
}

func (r *runtime) quiesceOnly() {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	r.quiesce()
}

// quiesce closes the admission gate before stopping producers. It waits for
// every accepted run before state can be reloaded or the host API unloaded.
func (r *runtime) quiesce() {
	r.mu.Lock()
	if !r.closed && r.stopCh != nil {
		close(r.stopCh)
	}
	r.closed = true
	r.generation++
	pending := append([]runRequest(nil), r.pending...)
	r.pending = nil
	c := r.cron
	scheduler := r.scheduler
	r.cron = nil
	r.scheduler = nil
	r.mu.Unlock()
	if c != nil {
		<-c.Stop().Done()
	}
	if scheduler != nil {
		scheduler.stop()
	}
	r.runWG.Wait()
	for _, request := range pending {
		r.requeueInterrupted(request)
	}
}

func runStopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func waitForRun(stop <-chan struct{}, duration time.Duration) bool {
	if runStopped(stop) {
		return false
	}
	if duration <= 0 {
		return true
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return !runStopped(stop)
	}
}

// A stopped run may have claimed a durable slot or follow-up before it began.
// Put it back; reservations already persisted for completed model calls still
// prevent a duplicate prewarm when the new scheduler picks it up.
func (r *runtime) requeueInterrupted(request runRequest) {
	r.mu.Lock()
	changed := false
	switch request.Trigger {
	case "schedule":
		if slot, exists := r.state.Slots[request.Slot]; exists && !slot.StartedAt.IsZero() {
			slot.StartedAt = time.Time{}
			r.state.Slots[request.Slot] = slot
			changed = true
		}
	case "reset_followup":
		if request.TargetAccount != "" && !request.DueAt.IsZero() {
			window := r.state.Accounts[request.TargetAccount]
			for _, kind := range strings.Split(request.FollowupKind, "+") {
				switch kind {
				case "five_hour":
					window.FiveHourFollowupAt = request.DueAt
				case "weekly":
					window.WeeklyFollowupAt = request.DueAt
				case "retry":
					window.QuotaRetryAt = request.DueAt
				}
			}
			r.state.Accounts[request.TargetAccount] = window
			changed = true
		}
	}
	path := r.cfg.StatePath
	r.mu.Unlock()
	if changed {
		if err := r.persistState(path); err != nil {
			r.setLastError("interrupted_requeue_write_failed")
			logHost("error", "codex prewarm interrupted run requeue failed", map[string]any{"error_code": "interrupted_requeue_write_failed"})
		}
	}
}

var errAlreadyRunning = errors.New("prewarm is already running")
var errJobNotFound = errors.New("prewarm job not found")

func (r *runtime) startRun(trigger string, force bool) error {
	return r.startNamedRun(trigger, "", force)
}

func (r *runtime) startNamedRun(trigger, name string, force bool) error {
	return r.startNamedRunWithNotify(trigger, name, force, false)
}

func (r *runtime) startNamedRunWithNotify(trigger, name string, force, notify bool) error {
	r.mu.RLock()
	jobs := r.cfg.Jobs
	generation := r.generation
	r.mu.RUnlock()
	if name == "" && len(jobs) > 0 {
		name = jobs[0].Name
	}
	for _, job := range jobs {
		if job.Name == name {
			return r.startRunJob(runRequest{Generation: generation, Trigger: trigger, Job: job, Force: force, Notify: notify})
		}
	}
	return errJobNotFound
}

func (r *runtime) startRunJob(request runRequest) error {
	r.mu.Lock()
	if r.closed || (request.Generation != 0 && request.Generation != r.generation) {
		r.mu.Unlock()
		return errors.New("plugin is shutting down")
	}
	if r.running {
		if request.Trigger != "manual" {
			if r.currentRequest.Trigger == request.Trigger && r.currentRequest.Job.Name == request.Job.Name && r.currentRequest.TargetAccount == request.TargetAccount && r.currentRequest.Slot == request.Slot {
				r.mu.Unlock()
				return nil
			}
			for _, queued := range r.pending {
				if queued.Trigger == request.Trigger && queued.Job.Name == request.Job.Name && queued.TargetAccount == request.TargetAccount && queued.Slot == request.Slot {
					r.mu.Unlock()
					return nil
				}
			}
			r.pending = append(r.pending, request)
			r.mu.Unlock()
			return nil
		}
		r.mu.Unlock()
		return errAlreadyRunning
	}
	r.running = true
	r.runWG.Add(1)
	r.currentRequest = request
	r.lastError = ""
	r.mu.Unlock()

	go r.runQueue(request)
	return nil
}

func (r *runtime) runQueue(request runRequest) {
	defer r.runWG.Done()
	for {
		r.mu.RLock()
		cfg := r.cfg
		r.mu.RUnlock()
		execute := r.runTask
		if execute == nil {
			execute = r.executeRun
		}
		record := execute(cfg, request)
		r.mu.Lock()
		if record.ErrorCode != "" {
			r.lastError = record.ErrorCode
		}
		if len(r.pending) == 0 || r.closed {
			r.pending = nil
			r.running = false
			r.currentRequest = runRequest{}
			r.mu.Unlock()
			return
		}
		request = r.pending[0]
		r.pending = r.pending[1:]
		r.currentRequest = request
		r.mu.Unlock()
	}
}

func (r *runtime) executeRun(cfg pluginConfig, request runRequest) runRecord {
	r.mu.RLock()
	stop := r.stopCh
	r.mu.RUnlock()
	now := time.Now().In(cfg.Location)
	record := runRecord{
		ID:            fmt.Sprintf("%d", now.UnixNano()),
		Job:           request.Job.Name,
		Trigger:       request.Trigger,
		Slot:          request.Slot,
		PlannedAt:     request.PlannedAt,
		JitterSeconds: request.JitterSeconds,
		TargetAccount: request.TargetAccount,
		FollowupKind:  request.FollowupKind,
		Force:         request.Force,
		Date:          now.Format("2006-01-02"),
		Model:         request.Job.Model,
		StartedAt:     now,
		Expected:      cfg.ExpectedAccountCount,
	}
	if err := appendAuditEntry(cfg.StatePath, auditEntry{At: now, Event: "run_started", RunID: record.ID, Trigger: record.Trigger, Job: record.Job, SourceAccount: record.SourceAccount}); err != nil {
		r.setLastError("audit_write_failed")
		logHost("error", "codex daily prewarm audit write failed", map[string]any{"error_code": "audit_write_failed"})
	}
	defer func() {
		record.FinishedAt = time.Now().In(cfg.Location)
		record.Success = record.ErrorCode == "" && record.Succeeded+record.Skipped == record.Discovered && record.Discovered > 0
		if record.ErrorCode == "interrupted" {
			r.requeueInterrupted(request)
		}
		if request.Trigger == "schedule" || request.Trigger == "reset_followup" || request.Notify {
			record.BarkStatus = sendPrewarmSuccessBark(cfg, record)
		}
		r.appendRun(cfg, record)
	}()
	if runStopped(stop) {
		record.ErrorCode = "interrupted"
		return record
	}

	auths, err := listCodexAuths()
	if err != nil {
		record.ErrorCode = "auth_inventory_unavailable"
		logHost("error", "codex daily prewarm could not list auths", map[string]any{"error_code": record.ErrorCode})
		return record
	}
	if abortForAccountCount(cfg.ExpectedAccountCount, len(auths), request.Trigger) {
		record.ErrorCode = "unexpected_account_count"
		logHost("error", "codex daily prewarm account count mismatch", map[string]any{"expected": cfg.ExpectedAccountCount, "discovered": len(auths)})
		return record
	}
	if len(auths) == 0 {
		record.ErrorCode = "no_eligible_codex_accounts"
		return record
	}
	if request.TargetAccount != "" {
		selected := auths[:0]
		for _, auth := range auths {
			if accountFingerprint(auth.ID) == request.TargetAccount {
				selected = append(selected, auth)
			}
		}
		auths = selected
		if len(auths) == 0 {
			record.ErrorCode = "target_account_unavailable"
			return record
		}
	}
	record.Discovered = len(auths)
	firstResults := fetchInitialUsagesUntil(auths, stop, fetchUsage)
	if runStopped(stop) {
		record.ErrorCode = "interrupted"
		return record
	}

	var lastModelFinishedAt time.Time
	for index, auth := range auths {
		if runStopped(stop) {
			record.ErrorCode = "interrupted"
			return record
		}
		fingerprint := accountFingerprint(auth.ID)
		result := accountResult{Account: fingerprint, AccountType: safeAccountType(auth.AccountType), Model: request.Job.Model, StartedAt: time.Now().In(cfg.Location)}
		first, reason := firstResults[index].observation, firstResults[index].reason
		record.QuotaQueried++
		if reason != "" {
			result.QuotaStatus = "query_failed"
			r.saveQuotaFailure(cfg, fingerprint, reason)
			if request.TargetAccount != "" {
				r.scheduleQuotaRetry(cfg, fingerprint, time.Now(), reason)
			}
		}
		if reason == "" {
			result.QuotaCheckedAt, result.FiveHour, result.Weekly = first.CheckedAt, first.FiveHour, first.Weekly
			result.QuotaStatus = "confirmed"
			if !r.saveQuota(cfg, fingerprint, first) {
				reason = "state_write_failed"
			}
			if reason == "" && request.TargetAccount == "" {
				r.clearQuotaRetry(cfg, fingerprint)
			}
			if reason == "" {
				reason = quotaCandidateReason(first)
				if reason == "" {
					if limitReason := r.warmLimitReason(fingerprint, time.Now()); limitReason != "" {
						reason = limitReason
					}
					if reason == "" && len(cfg.WarmAllowlist) > 0 {
						selected := false
						for _, allowed := range cfg.WarmAllowlist {
							if allowed == fingerprint {
								selected = true
								break
							}
						}
						if !selected {
							reason = "gray_not_selected"
						}
					}
					if reason == "" {
						var spacing time.Duration
						if !lastModelFinishedAt.IsZero() {
							spacing = cfg.AccountSpacing - time.Since(lastModelFinishedAt)
						}
						confirmed, confirmReason := confirmIdleAfterSpacing(first, spacing, stop, waitForRun,
							func() (quotaObservation, string) { return fetchUsage(auth) },
							func(observation quotaObservation) bool { return r.saveQuota(cfg, fingerprint, observation) },
						)
						result.QuotaCheckedAt, result.FiveHour, result.Weekly = confirmed.CheckedAt, confirmed.FiveHour, confirmed.Weekly
						reason = confirmReason
						if strings.HasPrefix(reason, "idle_confirmation_") {
							result.QuotaStatus = "query_failed"
							r.saveQuotaFailure(cfg, fingerprint, reason)
							if request.TargetAccount != "" {
								r.scheduleQuotaRetry(cfg, fingerprint, time.Now(), strings.TrimPrefix(reason, "idle_confirmation_"))
							}
						}
						if reason == "" {
							reason = r.warmLimitReason(fingerprint, time.Now())
						}
					}
				}
			}
		}
		if request.TargetAccount != "" && result.QuotaStatus != "query_failed" {
			r.clearQuotaRetry(cfg, fingerprint)
		}
		if reason == "interrupted" {
			record.ErrorCode = "interrupted"
			return record
		}
		if reason == "" && request.ObserveOnly {
			result.WouldWarm = true
			record.WouldWarm++
			reason = "followup_observe"
		}
		if reason == "" && cfg.DryRun {
			result.WouldWarm = true
			result.SkipReason = "dry_run"
			record.WouldWarm++
			reason = "dry_run"
		}
		if reason != "" {
			result.SkipReason = reason
			result.FinishedAt = time.Now().In(cfg.Location)
			result.Attempts24h = r.attemptCount(fingerprint, time.Now())
			record.Skipped++
			record.Accounts = append(record.Accounts, result)
			window := r.accountWindow(fingerprint)
			logHost("info", "codex prewarm account skipped", map[string]any{
				"account": fingerprint, "trigger": request.Trigger, "reason": reason,
				"five_hour_used_percent": result.FiveHour.UsedPercent, "weekly_used_percent": result.Weekly.UsedPercent,
				"next_five_hour_check_at": window.FiveHourFollowupAt, "next_weekly_check_at": window.WeeklyFollowupAt,
				"quota_retry_at": window.QuotaRetryAt,
			})
			continue
		}
		if runStopped(stop) {
			record.ErrorCode = "interrupted"
			return record
		}
		modelResult := executeAccountWithGuard(cfg, request.Job, auth, stop, func() bool {
			return r.reserveAttempt(cfg, fingerprint, time.Now())
		})
		result.Attempts = modelResult.Attempts
		result.StatusCode = modelResult.StatusCode
		result.ResponseReceived = modelResult.ResponseReceived
		result.FallbackUsed = modelResult.FallbackUsed
		result.ErrorCode = modelResult.ErrorCode
		result.FinishedAt = modelResult.FinishedAt
		result.Attempts24h = r.attemptCount(fingerprint, time.Now())
		if result.Attempts > 0 {
			record.Attempted++
			lastModelFinishedAt = modelResult.FinishedAt
			r.recordAccountResult(cfg, fingerprint, result)
		}
		record.Accounts = append(record.Accounts, result)
		if result.ResponseReceived {
			record.Succeeded++
		}
		logHost("info", "codex daily prewarm account finished", map[string]any{
			"account": fingerprint, "status": result.StatusCode, "response_received": result.ResponseReceived,
			"error_code": result.ErrorCode, "position": index + 1, "total": len(auths),
		})
		if modelResult.ErrorCode == "interrupted" {
			record.ErrorCode = "interrupted"
			return record
		}
	}
	if record.Succeeded+record.Skipped != record.Discovered {
		record.ErrorCode = "one_or_more_accounts_failed"
	}
	record.ObservedResetCount, record.ResetSpreadSeconds = r.resetSpread(auths, time.Now())
	return record
}

func (r *runtime) resetSpread(auths []pluginapi.HostAuthFileEntry, now time.Time) (int, *int64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var earliest, latest time.Time
	count := 0
	for _, auth := range auths {
		reset := r.state.Accounts[accountFingerprint(auth.ID)].FiveHour.ResetAt
		if !reset.After(now) {
			continue
		}
		count++
		if earliest.IsZero() || reset.Before(earliest) {
			earliest = reset
		}
		if latest.IsZero() || reset.After(latest) {
			latest = reset
		}
	}
	if count != len(auths) || count < 2 {
		return count, nil
	}
	seconds := int64(latest.Sub(earliest).Seconds())
	return count, &seconds
}

func abortForAccountCount(expected, discovered int, _ string) bool {
	if expected <= 0 || discovered == expected {
		return false
	}
	return true
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
		if auth.Disabled || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		eligible = append(eligible, auth)
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	return eligible
}

func executeAccount(cfg pluginConfig, job prewarmJob, auth pluginapi.HostAuthFileEntry) accountResult {
	return executeAccountWithGuard(cfg, job, auth, nil, func() bool { return true })
}

func executeAccountWithGuard(cfg pluginConfig, job prewarmJob, auth pluginapi.HostAuthFileEntry, stop <-chan struct{}, reserve func() bool) accountResult {
	result := accountResult{
		Account:     accountFingerprint(auth.ID),
		AccountType: safeAccountType(auth.AccountType),
		Model:       job.Model,
		StartedAt:   time.Now().In(cfg.Location),
	}
	models := []string{job.Model}
	if cfg.FallbackModel != job.Model {
		models = append(models, cfg.FallbackModel)
	}
	for modelIndex, model := range models {
		if runStopped(stop) {
			result.ErrorCode = "interrupted"
			break
		}
		result.Model = model
		result.FallbackUsed = modelIndex > 0
		body, err := json.Marshal(chatCompletionRequest{
			Model: model, Messages: []chatMessage{{Role: "user", Content: job.Prompt}},
		})
		if err != nil {
			result.ErrorCode = "request_encoding_failed"
			break
		}
		unsupported := false
		for attempt := 0; attempt <= cfg.RetryCount; attempt++ {
			if runStopped(stop) {
				result.ErrorCode = "interrupted"
				break
			}
			if !reserve() {
				result.ErrorCode = "attempt_limit_or_state_write_failed"
				break
			}
			result.Attempts++
			if runStopped(stop) {
				// A persisted reservation is kept even if shutdown wins this race.
				result.ErrorCode = "interrupted"
				break
			}
			raw, callErr := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
				HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
					EntryProtocol: "openai", ExitProtocol: "openai", Model: model,
					Stream: false, Body: body, Headers: http.Header{"Content-Type": []string{"application/json"}},
					ForcedProvider: "codex", AuthID: auth.ID,
				},
			})
			if callErr != nil {
				var hostErr *hostCallbackError
				if errors.As(callErr, &hostErr) && hostErr.HTTPStatus > 0 {
					result.StatusCode = hostErr.HTTPStatus
					result.PrimaryResetAt = upstreamResetAt(hostErr.Message, time.Now())
					result.ErrorCode = upstreamErrorCode(hostErr.Message, hostErr.HTTPStatus)
					unsupported = explicitUnsupportedModel(hostErr.HTTPStatus, []byte(hostErr.Message))
					if unsupported || attempt >= cfg.RetryCount || !safeToRetryStatus(hostErr.HTTPStatus) {
						break
					}
					if !waitForRun(stop, time.Duration(attempt+1)*2*time.Second) {
						result.ErrorCode = "interrupted"
						break
					}
					continue
				}
				// An uncertain callback may have consumed quota: never retry or fall back.
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
			result.FiveHour, result.Weekly = parseQuotaWindows(response.Headers, time.Now())
			if result.PrimaryResetAt.IsZero() && response.StatusCode == http.StatusTooManyRequests {
				result.PrimaryResetAt = upstreamResetAt(string(response.Body), time.Now())
			}
			result.PrimaryUsed = boundedHeader(response.Headers, "x-codex-primary-used-percent")
			if response.StatusCode >= 200 && response.StatusCode < 300 && validModelResponse(response.Body) {
				result.ResponseReceived = true
				result.ErrorCode = ""
				break
			}
			result.ErrorCode = upstreamErrorCode(string(response.Body), response.StatusCode)
			unsupported = explicitUnsupportedModel(response.StatusCode, response.Body)
			if unsupported || attempt >= cfg.RetryCount || !safeToRetryStatus(response.StatusCode) {
				break
			}
			if !waitForRun(stop, time.Duration(attempt+1)*2*time.Second) {
				result.ErrorCode = "interrupted"
				break
			}
		}
		if result.ResponseReceived || !unsupported || result.ErrorCode == "interrupted" {
			break
		}
	}
	result.FinishedAt = time.Now().In(cfg.Location)
	return result
}

func explicitUnsupportedModel(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusNotFound && status != http.StatusUnprocessableEntity {
		return false
	}
	if len(body) == 0 || len(body) > 16<<10 || !json.Valid(body) {
		return false
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	for _, code := range []string{payload.Error.Code, payload.Error.Type} {
		switch strings.ToLower(strings.TrimSpace(code)) {
		case "model_not_found", "unsupported_model", "model_not_supported":
			return true
		}
	}
	return false
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
			Type  string `json:"type"`
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(message), &payload) == nil && (payload.Error.Type == "usage_limit_reached" || payload.Type == "usage_limit_reached") {
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

func (r *runtime) skipReason(account string, now time.Time) string {
	r.mu.RLock()
	window := r.state.Accounts[account]
	policy := normalizedUnknownQuotaPolicy(r.cfg.UnknownQuotaPolicy)
	r.mu.RUnlock()
	if now.Before(window.RetryAfter) {
		return "quota_retry_pending"
	}
	fiveHour := window.FiveHour
	if fiveHour.WindowMinutes == 0 && !window.ResetAt.IsZero() && !window.LastProbeAt.IsZero() {
		// Older state did not record window length. Accept only a reset that was
		// within five hours of the observed request; a weekly reset is excluded.
		if duration := window.ResetAt.Sub(window.LastProbeAt); duration >= 0 && duration <= 5*time.Hour+10*time.Minute {
			fiveHour = quotaWindow{WindowMinutes: fiveHourMinutes, ResetAt: window.ResetAt, ObservedAt: window.LastProbeAt}
		}
	}
	if fiveHour.WindowMinutes == fiveHourMinutes {
		if now.Before(fiveHour.ResetAt) {
			return "active_five_hour_window"
		}
		if sameFiveHourWindow(fiveHour.ResetAt, window.LastPrewarmResetAt) {
			return "already_prewarmed_for_window"
		}
	} else if policy == "skip" {
		return "five_hour_unknown"
	} else if now.Before(window.LastProbeAt.Add(5 * time.Hour)) {
		return "unverified_window_cooldown"
	}
	if remaining, known := weeklyRemaining(window.Weekly, now); known {
		if !remaining {
			return "weekly_exhausted"
		}
	} else if policy == "skip" {
		return "weekly_unknown"
	} else if now.Before(window.LastProbeAt.Add(5 * time.Hour)) {
		return "unverified_weekly_cooldown"
	}
	return ""
}

func (r *runtime) accountWindow(account string) accountWindow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.Accounts[account]
}

func recentAttempts(times []time.Time, now time.Time) []time.Time {
	kept := make([]time.Time, 0, len(times))
	for _, at := range times {
		if !at.IsZero() && at.After(now.Add(-24*time.Hour)) && !at.After(now.Add(time.Minute)) {
			kept = append(kept, at)
		}
	}
	return kept
}

func (r *runtime) attemptCount(account string, now time.Time) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(recentAttempts(r.state.Accounts[account].AttemptTimes, now))
}

func (r *runtime) warmLimitReason(account string, now time.Time) string {
	r.mu.RLock()
	window := r.state.Accounts[account]
	r.mu.RUnlock()
	if len(recentAttempts(window.AttemptTimes, now)) >= 5 {
		return "rolling_24h_limit"
	}
	if !window.LastPrewarmAt.IsZero() && now.Before(window.LastPrewarmAt.Add(5*time.Hour)) {
		return "recent_prewarm_attempt"
	}
	return ""
}

// Persist the reservation before sending a model request. A crash or an
// uncertain callback still consumes one of the five rolling 24-hour slots.
func (r *runtime) reserveAttempt(cfg pluginConfig, account string, now time.Time) bool {
	r.mu.Lock()
	window := r.state.Accounts[account]
	window.AttemptTimes = recentAttempts(window.AttemptTimes, now)
	if len(window.AttemptTimes) >= 5 {
		r.mu.Unlock()
		return false
	}
	window.AttemptTimes = append(window.AttemptTimes, now.UTC())
	window.LastPrewarmAt = now.UTC()
	r.state.Accounts[account] = window
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("state_write_failed")
		return false
	}
	return true
}

func (r *runtime) saveQuota(cfg pluginConfig, account string, observation quotaObservation) bool {
	r.mu.Lock()
	window := r.state.Accounts[account]
	window.LastQuotaCheckedAt = observation.CheckedAt
	window.QuotaStatus = observation.Status
	window.FiveHour = observation.FiveHour
	window.Weekly = observation.Weekly
	if cfg.ResetFollowupMode != "off" {
		updateFollowupPlan(&window, observation)
	}
	r.state.Accounts[account] = window
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("state_write_failed")
		return false
	}
	return true
}

func (r *runtime) saveQuotaFailure(cfg pluginConfig, account, reason string) {
	r.mu.Lock()
	window := r.state.Accounts[account]
	window.LastQuotaCheckedAt = time.Now().UTC()
	window.QuotaStatus = reason
	r.state.Accounts[account] = window
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("state_write_failed")
	}
}

func (r *runtime) recordAccountResult(cfg pluginConfig, account string, result accountResult) {
	r.mu.Lock()
	window := r.state.Accounts[account]
	window.LastPrewarmAt = result.FinishedAt.UTC()
	if window.FiveHour.WindowMinutes == fiveHourMinutes {
		window.LastPrewarmResetAt = window.FiveHour.ResetAt
	} else if !window.ResetAt.IsZero() && !window.LastProbeAt.IsZero() {
		if duration := window.ResetAt.Sub(window.LastProbeAt); duration >= 0 && duration <= 5*time.Hour+10*time.Minute {
			window.LastPrewarmResetAt = window.ResetAt
		}
	}
	if result.ResponseReceived {
		window.LastProbeAt = result.FinishedAt.UTC()
		window.RetryAfter = time.Time{}
		if result.FiveHour.WindowMinutes == fiveHourMinutes {
			window.FiveHour = result.FiveHour
		}
		if result.Weekly.WindowMinutes == weeklyMinutes {
			window.Weekly = result.Weekly
		}
		if result.FiveHour.WindowMinutes == fiveHourMinutes {
			window.ResetAt = result.FiveHour.ResetAt
		} else if !result.PrimaryResetAt.IsZero() {
			window.ResetAt = result.PrimaryResetAt
		} else if !window.ResetAt.After(result.FinishedAt) {
			window.ResetAt = time.Time{}
		}
		if cfg.ResetFollowupMode != "off" && result.FiveHour.WindowMinutes == fiveHourMinutes && result.Weekly.WindowMinutes == weeklyMinutes {
			updateFollowupPlan(&window, quotaObservation{CheckedAt: result.FinishedAt, FiveHour: result.FiveHour, Weekly: result.Weekly})
		}
	} else if result.ErrorCode == "usage_limit_reached" && !result.PrimaryResetAt.IsZero() {
		window.RetryAfter = result.PrimaryResetAt
	}
	r.state.Accounts[account] = window
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("state_write_failed")
	}
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
	if err := appendAuditEntry(cfg.StatePath, auditEntry{At: record.FinishedAt, Event: "run_finished", RunID: record.ID, Trigger: record.Trigger, Job: record.Job, SourceAccount: record.SourceAccount, Run: &record}); err != nil {
		r.setLastError("audit_write_failed")
		logHost("error", "codex daily prewarm audit write failed", map[string]any{"error_code": "audit_write_failed"})
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
		Accounts: make(map[string]accountWindow, len(r.state.Accounts)),
	}
	for account, window := range r.state.Accounts {
		window.AttemptTimes = recentAttempts(window.AttemptTimes, time.Now())
		status.Accounts[account] = window
	}
	if r.cfg.AutomaticEnabled && r.cfg.Location != nil {
		for _, job := range r.cfg.Jobs {
			next := job.CronSchedule.Next(time.Now().In(r.cfg.Location))
			for _, slot := range r.state.Slots {
				if slot.Job == job.Name && slot.StartedAt.IsZero() && slot.PlannedAt.After(time.Now()) && (next.IsZero() || slot.PlannedAt.Before(next.Add(time.Minute))) {
					next = slot.PlannedAt
				}
			}
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
	if state.Accounts == nil {
		state.Accounts = make(map[string]accountWindow)
	}
	if state.Slots == nil {
		state.Slots = make(map[string]scheduledSlot)
	}
	// Import recent v0.4 model attempts once so the new rolling limit also
	// covers calls made shortly before the upgrade.
	knownAttempts := make(map[string]bool, len(state.Accounts))
	for account, window := range state.Accounts {
		knownAttempts[account] = len(window.AttemptTimes) != 0
	}
	for _, run := range state.History {
		for _, result := range run.Accounts {
			if result.Account == "" || result.Attempts <= 0 || result.FinishedAt.IsZero() {
				continue
			}
			window := state.Accounts[result.Account]
			if knownAttempts[result.Account] || !result.FinishedAt.After(time.Now().Add(-24*time.Hour)) {
				continue
			}
			for i := 0; i < result.Attempts && i < 5; i++ {
				window.AttemptTimes = append(window.AttemptTimes, result.FinishedAt)
			}
			state.Accounts[result.Account] = window
		}
	}
	// Seed the new per-account view from the previous plugin's bounded history.
	// This avoids an unnecessary probe immediately after an upgrade.
	if len(state.Accounts) == 0 {
		for index := len(state.History) - 1; index >= 0; index-- {
			for _, result := range state.History[index].Accounts {
				if result.Account == "" || result.FinishedAt.IsZero() {
					continue
				}
				window := state.Accounts[result.Account]
				if result.ResponseReceived {
					window.LastProbeAt = result.FinishedAt
					window.ResetAt = result.PrimaryResetAt
					window.RetryAfter = time.Time{}
				} else if result.ErrorCode == "usage_limit_reached" {
					window.RetryAfter = result.PrimaryResetAt
				}
				state.Accounts[result.Account] = window
			}
		}
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
	clone := runtimeState{Version: state.Version, Daily: make(map[string]map[string]bool), Accounts: make(map[string]accountWindow), Slots: make(map[string]scheduledSlot), History: append([]runRecord(nil), state.History...), LastSyncAt: state.LastSyncAt}
	for date, accounts := range state.Daily {
		clone.Daily[date] = make(map[string]bool, len(accounts))
		for account, done := range accounts {
			clone.Daily[date][account] = done
		}
	}
	for account, window := range state.Accounts {
		window.AttemptTimes = append([]time.Time(nil), window.AttemptTimes...)
		clone.Accounts[account] = window
	}
	for key, slot := range state.Slots {
		clone.Slots[key] = slot
	}
	return clone
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
