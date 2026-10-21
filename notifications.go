package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const digestRetryInterval = 5 * time.Minute

type notificationState struct {
	NextDigestAt       time.Time                `json:"next_digest_at,omitempty"`
	LastDigestAt       time.Time                `json:"last_digest_at,omitempty"`
	LastDigestEnd      time.Time                `json:"last_digest_end,omitempty"`
	LastDigestResult   string                   `json:"last_digest_result,omitempty"`
	LastDigestAttempts int                      `json:"last_digest_attempts,omitempty"`
	LastQuietAt        time.Time                `json:"last_quiet_at,omitempty"`
	LastQuietResult    string                   `json:"last_quiet_result,omitempty"`
	Windows            map[string]*digestWindow `json:"windows,omitempty"`
	UnknownStreaks     map[string]int           `json:"unknown_streaks,omitempty"`
	Pending            []failureNotice          `json:"pending,omitempty"`
}

type digestWindow struct {
	Start         time.Time                 `json:"start"`
	End           time.Time                 `json:"end"`
	Runs          int                       `json:"runs"`
	Succeeded     int                       `json:"succeeded"`
	Failed        int                       `json:"failed"`
	Accounts      map[string]*digestAccount `json:"accounts"`
	Skips         map[string]int            `json:"skips"`
	Failures      map[string]string         `json:"failures"`
	Attempts      int                       `json:"attempts"`
	NextAttemptAt time.Time                 `json:"next_attempt_at,omitempty"`
	Result        string                    `json:"result,omitempty"`
}

type digestAccount struct {
	Succeeded       int       `json:"succeeded"`
	Failed          int       `json:"failed"`
	LastFailureAt   time.Time `json:"last_failure_at,omitempty"`
	LastSuccessAt   time.Time `json:"last_success_at,omitempty"`
	QuotaCheckedAt  time.Time `json:"quota_checked_at,omitempty"`
	WeeklyRemaining float64   `json:"weekly_remaining"`
}

type failureNotice struct {
	Account   string    `json:"account"`
	Category  string    `json:"category"`
	Detail    string    `json:"detail,omitempty"`
	At        time.Time `json:"at"`
	DueAt     time.Time `json:"due_at"`
	WindowEnd time.Time `json:"window_end"`
}

// notificationStatus contains no account-level data, even when a queue is pending.
type notificationStatus struct {
	LastDigestAt       time.Time `json:"last_digest_at,omitempty"`
	LastDigestEnd      time.Time `json:"last_digest_end,omitempty"`
	LastDigestResult   string    `json:"last_digest_result,omitempty"`
	LastDigestAttempts int       `json:"last_digest_attempts"`
	NextDigestAt       time.Time `json:"next_digest_at,omitempty"`
	LastQuietAt        time.Time `json:"last_quiet_at,omitempty"`
	LastQuietResult    string    `json:"last_quiet_result,omitempty"`
	PendingFailures    int       `json:"pending_failures"`
}

func (n *notificationState) init() {
	if n.Windows == nil {
		n.Windows = make(map[string]*digestWindow)
	}
	if n.UnknownStreaks == nil {
		n.UnknownStreaks = make(map[string]int)
	}
	for _, w := range n.Windows {
		if w.Accounts == nil {
			w.Accounts = make(map[string]*digestAccount)
		}
		if w.Skips == nil {
			w.Skips = make(map[string]int)
		}
		if w.Failures == nil {
			w.Failures = make(map[string]string)
		}
	}
}

func localTimeOn(at time.Time, clock string, location *time.Location) time.Time {
	at = at.In(location)
	parsed, _ := time.Parse("15:04", clock)
	return time.Date(at.Year(), at.Month(), at.Day(), parsed.Hour(), parsed.Minute(), 0, 0, location)
}

func digestEnd(cfg pluginConfig, at time.Time) time.Time {
	end := localTimeOn(at, cfg.DigestTime, cfg.Location)
	if !at.Before(end) {
		end = end.AddDate(0, 0, 1)
	}
	return end
}

func quietUntil(cfg pluginConfig, at time.Time) time.Time {
	start := localTimeOn(at, cfg.QuietStart, cfg.Location)
	end := localTimeOn(at, cfg.QuietEnd, cfg.Location)
	if end.Before(start) {
		if at.Before(end) {
			return end
		}
		if !at.Before(start) {
			return end.AddDate(0, 0, 1)
		}
	} else if !at.Before(start) && at.Before(end) {
		return end
	}
	return time.Time{}
}

func (n *notificationState) window(cfg pluginConfig, end time.Time) *digestWindow {
	key := end.UTC().Format(time.RFC3339)
	if n.Windows[key] == nil {
		n.Windows[key] = &digestWindow{Start: end.In(cfg.Location).AddDate(0, 0, -1), End: end,
			Accounts: make(map[string]*digestAccount), Skips: make(map[string]int), Failures: make(map[string]string)}
	}
	return n.Windows[key]
}

func (n notificationState) status(cfg pluginConfig, now time.Time) notificationStatus {
	next := n.NextDigestAt
	if next.IsZero() {
		next = digestEnd(cfg, now)
	}
	return notificationStatus{LastDigestAt: n.LastDigestAt, LastDigestEnd: n.LastDigestEnd,
		LastDigestResult: n.LastDigestResult, LastDigestAttempts: n.LastDigestAttempts, NextDigestAt: next,
		LastQuietAt: n.LastQuietAt, LastQuietResult: n.LastQuietResult, PendingFailures: len(n.Pending)}
}

func unknownQuotaResult(item accountResult) bool {
	// Fetch failures include expired credentials and missing quota fields.
	return item.Attempts == 0 && (strings.Contains(item.SkipReason, "unknown") || strings.Contains(item.ErrorCode, "unknown") || item.QuotaStatus == "unknown" || item.QuotaStatus == "query_failed")
}

func failureCategory(item accountResult, streak, threshold int) string {
	if item.Attempts > 0 && !item.ResponseReceived && (item.ErrorCode != "interrupted" || item.RequestSent) {
		return "request_failed"
	}
	if streak >= threshold {
		return "quota_unknown"
	}
	return ""
}

func failureBody(notices []failureNotice) string {
	lines := []string{fmt.Sprintf("预热失败告警 %d 项", len(notices))}
	for _, notice := range notices {
		detail := notice.Detail
		switch notice.Category {
		case "request_failed":
			if detail == "" {
				detail = "no_valid_response"
			}
			detail = "请求失败（" + detail + "）"
		case "quota_unknown":
			if detail == "" {
				detail = "连续多轮额度未知"
			}
		default:
			detail = "请求失败（" + notice.Category + "）"
		}
		lines = append(lines, notice.Account+" · "+detail)
	}
	return strings.Join(lines, "\n")
}

// Serialize accounting with digest closure. A run belongs to the window in which
// it is finalized; no account IDs or error messages from the host enter a URL.
func (r *runtime) recordNotifications(cfg pluginConfig, record *runRecord, notify bool) string {
	r.notificationMu.Lock()
	defer r.notificationMu.Unlock()
	record.FinishedAt = r.currentTime().In(cfg.Location)
	r.mu.Lock()
	n := &r.state.Notifications
	n.init()
	if n.NextDigestAt.IsZero() {
		n.NextDigestAt = digestEnd(cfg, record.FinishedAt)
	}
	n.pruneWindowsBefore(record.FinishedAt.AddDate(0, 0, -7))
	w := n.window(cfg, digestEnd(cfg, record.FinishedAt))
	if record.Trigger != "quota_refresh" {
		w.Runs++
	}
	var immediate []failureNotice
	deduplicated, queued := false, false
	for _, item := range record.Accounts {
		a := w.Accounts[item.Account]
		if a == nil {
			a = &digestAccount{}
			w.Accounts[item.Account] = a
		}
		if item.ResponseReceived {
			w.Succeeded++
			a.Succeeded++
			a.LastSuccessAt = record.FinishedAt
		}
		if item.QuotaStatus == "confirmed" && !item.QuotaCheckedAt.IsZero() && item.Weekly.WindowMinutes == weeklyMinutes && !item.QuotaCheckedAt.Before(a.QuotaCheckedAt) && !item.QuotaCheckedAt.Before(w.Start) && item.QuotaCheckedAt.Before(w.End) {
			a.QuotaCheckedAt = item.QuotaCheckedAt
			a.WeeklyRemaining = 100 - item.Weekly.UsedPercent
		}
		if item.SkipReason == "quota_refresh_only" {
			// Successful read-only refreshes are not skipped prewarms.
		} else if item.SkipReason != "" {
			w.Skips[item.SkipReason]++
		} else if unknownQuotaResult(item) {
			reason := item.ErrorCode
			if reason == "" {
				reason = "quota_unknown"
			}
			w.Skips[reason]++
		}
		if unknownQuotaResult(item) {
			n.UnknownStreaks[item.Account]++
		} else {
			delete(n.UnknownStreaks, item.Account)
		}
		category := failureCategory(item, n.UnknownStreaks[item.Account], cfg.UnknownFailureStreak)
		if category == "" {
			continue
		}
		w.Failed++
		a.Failed++
		a.LastFailureAt = record.FinishedAt
		key := item.Account + "|" + category
		if _, exists := w.Failures[key]; exists {
			deduplicated = true
			continue
		}
		// Manual feedback also consumes this window's alert allowance.
		if notify {
			w.Failures[key] = "manual"
			continue
		}
		detail := item.ErrorCode
		if category == "quota_unknown" {
			detail = fmt.Sprintf("连续 %d 轮额度未知", n.UnknownStreaks[item.Account])
		} else if detail == "" {
			detail = "no_valid_response"
		}
		notice := failureNotice{Account: item.Account, Category: category, Detail: detail, At: record.FinishedAt, WindowEnd: w.End}
		if until := quietUntil(cfg, record.FinishedAt); !until.IsZero() {
			notice.DueAt = until
			n.Pending = append(n.Pending, notice)
			w.Failures[key] = "queued"
			queued = true
		} else {
			immediate = append(immediate, notice)
			w.Failures[key] = "sending"
		}
	}
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("notification_state_write_failed")
		return "notification_state_write_failed"
	}
	if notify {
		lines := []string{fmt.Sprintf("巡检 %d 个账号，成功预热 %d，失败 %d，跳过 %d", record.Discovered, record.Succeeded, record.Failed, record.Skipped)}
		for _, item := range record.Accounts {
			result := item.SkipReason
			if item.ResponseReceived {
				result = "预热成功"
			} else if item.ErrorCode != "" {
				result = item.ErrorCode
			}
			lines = append(lines, item.Account+" · "+result)
		}
		if record.ErrorCode != "" {
			lines = append(lines, "错误："+record.ErrorCode)
		}
		return "manual_" + r.sendNotification(cfg, "Codex 预热结果", strings.Join(lines, "\n"), "active")
	}
	if len(immediate) > 0 {
		result := r.sendNotification(cfg, "Codex 预热失败", failureBody(immediate), "timeSensitive")
		r.mu.Lock()
		for _, notice := range immediate {
			w.Failures[notice.Account+"|"+notice.Category] = result
		}
		r.mu.Unlock()
		r.persistNotificationResult(cfg)
		r.auditNotification(cfg, record.FinishedAt, "bark_failure", result, w.End)
		return "failure_" + result
	}
	if queued {
		return "failure_queued_quiet_hours"
	}
	if deduplicated {
		return "failure_deduplicated"
	}
	return "deferred_to_digest"
}

func (r *runtime) sendNotification(cfg pluginConfig, title, body, level string) string {
	send := r.barkSend
	if send == nil {
		send = sendBarkHTTP
	}
	return sendBarkWith(cfg, title, r.displayNotification(body), level, send)
}

func (r *runtime) persistNotificationResult(cfg pluginConfig) {
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("notification_state_write_failed")
	}
}

func (r *runtime) auditNotification(cfg pluginConfig, at time.Time, event, result string, end time.Time) {
	if err := appendAuditEntry(cfg.StatePath, auditEntry{At: at, Event: event, BarkStatus: result, DigestEnd: end}); err != nil {
		r.setLastError("audit_write_failed")
	}
}

func digestBody(cfg pluginConfig, w *digestWindow) string {
	lines := []string{fmt.Sprintf("统计 %s – %s", w.Start.In(cfg.Location).Format("01-02 15:04"), w.End.In(cfg.Location).Format("01-02 15:04")),
		fmt.Sprintf("巡检 %d 轮，成功预热 %d 次，失败 %d", w.Runs, w.Succeeded, w.Failed)}
	accounts := make([]string, 0, len(w.Accounts))
	for id := range w.Accounts {
		accounts = append(accounts, id)
	}
	sort.Strings(accounts)
	var failures []string
	for _, id := range accounts {
		a := w.Accounts[id]
		quota := "周剩余未知"
		if !a.QuotaCheckedAt.IsZero() {
			quota = fmt.Sprintf("周剩余 %.0f%%", a.WeeklyRemaining)
		}
		lines = append(lines, fmt.Sprintf("%s 预热 %d 次 · %s", id, a.Succeeded, quota))
		if a.Failed > 0 {
			recovery := "未恢复"
			if a.LastSuccessAt.After(a.LastFailureAt) {
				recovery = "已恢复"
			}
			failures = append(failures, fmt.Sprintf("%s %d 次（%s）", id, a.Failed, recovery))
		}
	}
	if len(failures) > 0 {
		lines = append(lines, "失败："+strings.Join(failures, "；"))
	}
	var skips []string
	for reason, count := range w.Skips {
		skips = append(skips, fmt.Sprintf("%s %d", reason, count))
	}
	sort.Strings(skips)
	if len(skips) > 0 {
		lines = append(lines, "跳过："+strings.Join(skips, " · "))
	}
	return strings.Join(lines, "\n")
}

// processNotifications is driven by the scheduler's injected time, including
// its first tick after restart. Reservations are durable before any HTTP call.
func (r *runtime) processNotifications(now time.Time) {
	r.notificationMu.Lock()
	defer r.notificationMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	cfg := r.cfg
	n := &r.state.Notifications
	n.init()
	if n.LastQuietResult == "sending" {
		n.LastQuietResult = "abandoned_uncertain_delivery"
		r.mu.Unlock()
		r.persistNotificationResult(cfg)
		r.mu.Lock()
	}
	if n.NextDigestAt.IsZero() {
		n.NextDigestAt = digestEnd(cfg, now)
		r.mu.Unlock()
		r.persistNotificationResult(cfg)
		r.mu.Lock()
	}
	var notices, pending []failureNotice
	for _, notice := range n.Pending {
		if !notice.DueAt.After(now) && quietUntil(cfg, now).IsZero() {
			notices = append(notices, notice)
		} else {
			pending = append(pending, notice)
		}
	}
	if len(notices) > 0 {
		n.Pending = pending
		n.LastQuietAt = now
		n.LastQuietResult = "sending"
		r.mu.Unlock()
		if err := r.persistState(cfg.StatePath); err != nil {
			r.mu.Lock()
			n.Pending = append(n.Pending, notices...)
			n.LastQuietResult = "state_write_failed"
			r.mu.Unlock()
			r.setLastError("notification_state_write_failed")
			return
		}
		result := r.sendNotification(cfg, "Codex 夜间预热失败汇总", failureBody(notices), "timeSensitive")
		r.mu.Lock()
		n.LastQuietResult = result
		for _, notice := range notices {
			if w := n.Windows[notice.WindowEnd.UTC().Format(time.RFC3339)]; w != nil {
				w.Failures[notice.Account+"|"+notice.Category] = result
			}
		}
		r.mu.Unlock()
		r.persistNotificationResult(cfg)
		r.auditNotification(cfg, now, "bark_quiet_failures", result, time.Time{})
		r.mu.Lock()
	}
	if now.Before(n.NextDigestAt) {
		r.mu.Unlock()
		return
	}
	w := n.window(cfg, n.NextDigestAt)
	// A crash after dispatch has an uncertain outcome. Do not send it twice.
	if w.Result == "sending" {
		w.Result = "abandoned_uncertain_delivery"
		r.finishDigestLocked(cfg, w, now)
		r.mu.Unlock()
		r.persistNotificationResult(cfg)
		r.auditNotification(cfg, now, "bark_digest", w.Result, w.End)
		return
	}
	if w.NextAttemptAt.After(now) {
		r.mu.Unlock()
		return
	}
	if w.Attempts >= 3 {
		r.finishDigestLocked(cfg, w, now)
		r.mu.Unlock()
		r.persistNotificationResult(cfg)
		return
	}
	w.Attempts++
	w.Result = "sending"
	w.NextAttemptAt = now.Add(digestRetryInterval)
	title, body := "Codex 预热日报 "+w.End.In(cfg.Location).Format("01-02"), digestBody(cfg, w)
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.mu.Lock()
		w.Result = "state_write_failed"
		r.mu.Unlock()
		r.setLastError("notification_state_write_failed")
		return
	}
	result := r.sendNotification(cfg, title, body, "passive")
	r.mu.Lock()
	w.Result = result
	n.LastDigestResult = result
	n.LastDigestEnd = w.End
	n.LastDigestAttempts = w.Attempts
	if result == "accepted" {
		n.LastDigestAt = now
	}
	if result == "accepted" || result == "not_configured" || w.Attempts >= 3 {
		r.finishDigestLocked(cfg, w, now)
	}
	r.mu.Unlock()
	r.persistNotificationResult(cfg)
	r.auditNotification(cfg, now, "bark_digest", result, w.End)
}

func (r *runtime) finishDigestLocked(cfg pluginConfig, w *digestWindow, now time.Time) {
	n := &r.state.Notifications
	n.LastDigestEnd = w.End
	n.LastDigestResult = w.Result
	n.LastDigestAttempts = w.Attempts
	if w.Result != "accepted" && w.Result != "not_configured" {
		n.LastDigestResult = "abandoned_" + strings.TrimPrefix(w.Result, "abandoned_")
	}
	n.NextDigestAt = localTimeOn(w.End.In(cfg.Location).AddDate(0, 0, 1), cfg.DigestTime, cfg.Location)
	if !n.NextDigestAt.After(now) {
		n.NextDigestAt = digestEnd(cfg, now)
	}
	n.pruneWindowsBefore(n.NextDigestAt)
}

func (n *notificationState) pruneWindowsBefore(cutoff time.Time) {
	for key, w := range n.Windows {
		if w.End.Before(cutoff) {
			delete(n.Windows, key)
		}
	}
	var pending []failureNotice
	for _, notice := range n.Pending {
		if _, exists := n.Windows[notice.WindowEnd.UTC().Format(time.RFC3339)]; exists {
			pending = append(pending, notice)
		}
	}
	n.Pending = pending
}

func cloneNotifications(n notificationState) notificationState {
	clone := n
	clone.Pending = append([]failureNotice(nil), n.Pending...)
	clone.UnknownStreaks = make(map[string]int, len(n.UnknownStreaks))
	for account, count := range n.UnknownStreaks {
		clone.UnknownStreaks[account] = count
	}
	clone.Windows = make(map[string]*digestWindow, len(n.Windows))
	for key, w := range n.Windows {
		copy := *w
		copy.Accounts = make(map[string]*digestAccount, len(w.Accounts))
		for id, a := range w.Accounts {
			value := *a
			copy.Accounts[id] = &value
		}
		copy.Skips = make(map[string]int, len(w.Skips))
		for reason, count := range w.Skips {
			copy.Skips[reason] = count
		}
		copy.Failures = make(map[string]string, len(w.Failures))
		for key, result := range w.Failures {
			copy.Failures[key] = result
		}
		clone.Windows[key] = &copy
	}
	return clone
}
