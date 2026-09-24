package main

import (
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	followupDelay = 90 * time.Second
	maxSlotAge    = 48 * time.Hour
)

type scheduledSlot struct {
	Job           string    `json:"job"`
	BaseAt        time.Time `json:"base_at"`
	PlannedAt     time.Time `json:"planned_at"`
	JitterSeconds int       `json:"jitter_seconds"`
	StartedAt     time.Time `json:"started_at,omitempty"`
}

type runtimeScheduler struct {
	runtime *runtime
	stopCh  chan struct{}
	doneCh  chan struct{}
	once    sync.Once
}

func newRuntimeScheduler(r *runtime) *runtimeScheduler {
	return &runtimeScheduler{runtime: r, stopCh: make(chan struct{}), doneCh: make(chan struct{})}
}

func (s *runtimeScheduler) stop() {
	s.once.Do(func() { close(s.stopCh) })
	select {
	case <-s.doneCh:
	case <-time.After(2 * time.Second):
	}
}

func slotKey(job string, base time.Time) string {
	return job + "|" + base.UTC().Format("2006-01-02T15:04Z")
}

func inScheduleHour(at time.Time, location *time.Location) bool {
	hour := at.In(location).Hour()
	return hour >= 5 && hour <= 23
}

func inFollowupHours(at time.Time, location *time.Location) bool {
	hour := at.In(location).Hour()
	return hour >= 5 && hour < 23
}

func sameLocalDate(a, b time.Time, location *time.Location) bool {
	return a.In(location).Format("2006-01-02") == b.In(location).Format("2006-01-02")
}

func latestCronSlot(schedule cron.Schedule, now time.Time) time.Time {
	var latest time.Time
	for cursor := now.Add(-25 * time.Hour); ; {
		next := schedule.Next(cursor)
		if next.IsZero() || next.After(now) {
			return latest
		}
		latest = next
		cursor = next
	}
}

func (s *runtimeScheduler) bootstrap(cfg pluginConfig, now time.Time) {
	for _, job := range cfg.Jobs {
		latest := latestCronSlot(job.CronSchedule, now.In(cfg.Location))
		if !latest.IsZero() && sameLocalDate(latest, now, cfg.Location) && now.Sub(latest) <= 70*time.Minute {
			s.planSlot(job, latest, now)
		}
		s.planSlot(job, job.CronSchedule.Next(now.In(cfg.Location)), now)
	}
	go s.loop()
}

func (s *runtimeScheduler) planSlot(job prewarmJob, base, now time.Time) {
	r := s.runtime
	r.mu.RLock()
	location := r.cfg.Location
	r.mu.RUnlock()
	if base.IsZero() || !inScheduleHour(base, location) {
		return
	}
	select {
	case <-s.stopCh:
		return
	default:
	}
	key := slotKey(job.Name, base)
	r.mu.Lock()
	if r.closed || r.scheduler != s {
		r.mu.Unlock()
		return
	}
	if _, exists := r.state.Slots[key]; exists {
		r.mu.Unlock()
		return
	}
	jitter := 10 + rand.IntN(51)
	r.state.Slots[key] = scheduledSlot{Job: job.Name, BaseAt: base.UTC(), PlannedAt: base.Add(time.Duration(jitter) * time.Second).UTC(), JitterSeconds: jitter}
	for oldKey, slot := range r.state.Slots {
		if slot.BaseAt.Before(now.Add(-maxSlotAge)) {
			delete(r.state.Slots, oldKey)
		}
	}
	r.mu.Unlock()
	if err := r.persistState(r.cfg.StatePath); err != nil {
		r.mu.Lock()
		if slot, exists := r.state.Slots[key]; exists && slot.StartedAt.IsZero() && slot.JitterSeconds == jitter {
			delete(r.state.Slots, key)
		}
		r.mu.Unlock()
		r.setLastError("slot_state_write_failed")
		return
	}
	logHost("info", "codex prewarm hourly slot planned", map[string]any{"job": job.Name, "slot": key, "jitter_seconds": jitter, "planned_at": base.Add(time.Duration(jitter) * time.Second)})
}

func (s *runtimeScheduler) loop() {
	defer close(s.doneCh)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	s.processDue(time.Now())
	for {
		select {
		case <-s.stopCh:
			return
		case now := <-ticker.C:
			s.processDue(now)
		}
	}
}

func (s *runtimeScheduler) processDue(now time.Time) {
	r := s.runtime
	var requests []runRequest
	previousSlots := make(map[string]scheduledSlot)
	previousAccounts := make(map[string]accountWindow)
	r.mu.Lock()
	if r.closed || r.scheduler != s {
		r.mu.Unlock()
		return
	}
	cfg := r.cfg
	jobs := make(map[string]prewarmJob, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		jobs[job.Name] = job
	}
	for key, slot := range r.state.Slots {
		if !slot.StartedAt.IsZero() || slot.PlannedAt.After(now) || !sameLocalDate(slot.BaseAt, now, cfg.Location) || now.Sub(slot.BaseAt) > 70*time.Minute {
			continue
		}
		if slot.BaseAt.In(cfg.Location).Hour() == 23 && now.After(slot.BaseAt.Add(2*time.Minute)) {
			continue
		}
		job, exists := jobs[slot.Job]
		if !exists {
			continue
		}
		slot.StartedAt = now.UTC()
		previousSlots[key] = r.state.Slots[key]
		r.state.Slots[key] = slot
		requests = append(requests, runRequest{Trigger: "schedule", Job: job, Slot: key, SlotBase: slot.BaseAt, PlannedAt: slot.PlannedAt, JitterSeconds: slot.JitterSeconds})
	}
	if cfg.ResetFollowupMode != "off" && inFollowupHours(now, cfg.Location) {
		for account, window := range r.state.Accounts {
			var due time.Time
			var kinds []string
			if !window.FiveHourFollowupAt.IsZero() && !window.FiveHourFollowupAt.After(now) && sameLocalDate(window.FiveHourFollowupAt, now, cfg.Location) {
				due = window.FiveHourFollowupAt
				window.LastFiveHourR = window.FiveHourFollowupR
				window.FiveHourFollowupAt = time.Time{}
				kinds = append(kinds, "five_hour")
			}
			if !window.WeeklyFollowupAt.IsZero() && !window.WeeklyFollowupAt.After(now) && sameLocalDate(window.WeeklyFollowupAt, now, cfg.Location) {
				if due.IsZero() || window.WeeklyFollowupAt.Before(due) {
					due = window.WeeklyFollowupAt
				}
				window.LastWeeklyR = window.WeeklyFollowupR
				window.WeeklyFollowupAt = time.Time{}
				kinds = append(kinds, "weekly")
			}
			if !window.QuotaRetryAt.IsZero() && !window.QuotaRetryAt.After(now) && sameLocalDate(window.QuotaRetryAt, now, cfg.Location) {
				if due.IsZero() || window.QuotaRetryAt.Before(due) {
					due = window.QuotaRetryAt
				}
				window.QuotaRetryAt = time.Time{}
				kinds = append(kinds, "retry")
			}
			if len(kinds) == 0 {
				continue
			}
			previousAccounts[account] = r.state.Accounts[account]
			r.state.Accounts[account] = window
			requests = append(requests, runRequest{Trigger: "reset_followup", Job: cfg.Jobs[0], TargetAccount: account, FollowupKind: strings.Join(kinds, "+"), DueAt: due, ObserveOnly: cfg.ResetFollowupMode == "observe"})
		}
	}
	r.mu.Unlock()
	if len(requests) == 0 {
		return
	}
	if err := r.persistState(cfg.StatePath); err != nil {
		r.mu.Lock()
		for key, previous := range previousSlots {
			if current, exists := r.state.Slots[key]; exists && current.StartedAt.Equal(now.UTC()) {
				r.state.Slots[key] = previous
			}
		}
		for account, previous := range previousAccounts {
			current := r.state.Accounts[account]
			// Restore only scheduling fields; a concurrent quota read may have
			// updated the observed limits while the state write was in flight.
			if current.LastFiveHourR.Equal(previous.FiveHourFollowupR) {
				current.FiveHourFollowupAt = previous.FiveHourFollowupAt
				current.LastFiveHourR = previous.LastFiveHourR
			}
			if current.LastWeeklyR.Equal(previous.WeeklyFollowupR) {
				current.WeeklyFollowupAt = previous.WeeklyFollowupAt
				current.LastWeeklyR = previous.LastWeeklyR
			}
			if current.QuotaRetryStep == previous.QuotaRetryStep {
				current.QuotaRetryAt = previous.QuotaRetryAt
			}
			r.state.Accounts[account] = current
		}
		r.mu.Unlock()
		r.setLastError("scheduler_state_write_failed")
		return
	}
	for _, request := range requests {
		if request.TargetAccount != "" {
			logHost("info", "codex prewarm followup due", map[string]any{"account": request.TargetAccount, "kind": request.FollowupKind, "due_at": request.DueAt})
		}
		if err := r.startRunJob(request); err != nil {
			r.setLastError(safeErrorCode(err))
			logHost("error", "codex prewarm scheduled run rejected", map[string]any{"trigger": request.Trigger, "error_code": safeErrorCode(err)})
		}
	}
	for _, request := range requests {
		if request.Trigger != "schedule" {
			continue
		}
		next := request.Job.CronSchedule.Next(request.SlotBase.In(cfg.Location))
		s.planSlot(request.Job, next, now)
	}
}

func updateFollowupPlan(window *accountWindow, observation quotaObservation) {
	fiveRemaining := observation.FiveHour.ResetAt.Sub(observation.CheckedAt)
	if observation.FiveHour.WindowMinutes == fiveHourMinutes && observation.FiveHour.UsedPercent > 0 && fiveRemaining > 0 && fiveRemaining <= 5*time.Hour+10*time.Minute && !sameResetPoint(observation.FiveHour.ResetAt, window.LastFiveHourR) {
		window.FiveHourFollowupR = observation.FiveHour.ResetAt
		window.FiveHourFollowupAt = observation.FiveHour.ResetAt.Add(followupDelay)
	} else if sameResetPoint(window.FiveHourFollowupR, window.LastFiveHourR) || !window.FiveHourFollowupAt.After(observation.CheckedAt) {
		window.FiveHourFollowupAt = time.Time{}
	}
	weeklyRemaining := observation.Weekly.ResetAt.Sub(observation.CheckedAt)
	if observation.Weekly.WindowMinutes == weeklyMinutes && observation.Weekly.UsedPercent >= 95 && weeklyRemaining > 0 && weeklyRemaining <= 7*24*time.Hour+10*time.Minute && !sameResetPoint(observation.Weekly.ResetAt, window.LastWeeklyR) {
		window.WeeklyFollowupR = observation.Weekly.ResetAt
		window.WeeklyFollowupAt = observation.Weekly.ResetAt.Add(followupDelay)
	} else if sameResetPoint(window.WeeklyFollowupR, window.LastWeeklyR) || !window.WeeklyFollowupAt.After(observation.CheckedAt) {
		window.WeeklyFollowupAt = time.Time{}
	}
}

func sameResetPoint(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	d := a.Sub(b)
	return d >= -time.Minute && d <= time.Minute
}

func (r *runtime) scheduleQuotaRetry(cfg pluginConfig, account string, now time.Time, reason string) {
	if reason == "usage_http_429" || !inFollowupHours(now, cfg.Location) {
		return
	}
	r.mu.Lock()
	window := r.state.Accounts[account]
	if window.QuotaRetryStep >= 2 || !window.QuotaRetryAt.IsZero() {
		r.mu.Unlock()
		return
	}
	delay := 2 * time.Minute
	if window.QuotaRetryStep == 1 {
		delay = 8 * time.Minute
	}
	retryAt := now.Add(delay)
	if !inFollowupHours(retryAt, cfg.Location) {
		r.mu.Unlock()
		return
	}
	window.QuotaRetryAt = retryAt.UTC()
	window.QuotaRetryStep++
	r.state.Accounts[account] = window
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("retry_state_write_failed")
	}
	logHost("info", "codex prewarm quota retry planned", map[string]any{"account": account, "reason": reason, "retry_at": retryAt})
}

func (r *runtime) clearQuotaRetry(cfg pluginConfig, account string) {
	r.mu.Lock()
	window := r.state.Accounts[account]
	if window.QuotaRetryAt.IsZero() && window.QuotaRetryStep == 0 {
		r.mu.Unlock()
		return
	}
	window.QuotaRetryAt = time.Time{}
	window.QuotaRetryStep = 0
	r.state.Accounts[account] = window
	r.mu.Unlock()
	if err := r.persistState(cfg.StatePath); err != nil {
		r.setLastError("state_write_failed")
	}
}
