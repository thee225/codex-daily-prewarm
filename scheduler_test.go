package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHourlySlotJitterPersistsAcrossRestart(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	r := newRuntime()
	r.cfg = cfg
	s := newRuntimeScheduler(r)
	r.scheduler = s
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, cfg.Location)
	key := slotKey(cfg.Jobs[0].Name, base)
	s.planSlot(cfg.Jobs[0], base, base.Add(-time.Minute))
	first := r.state.Slots[key]
	if first.JitterSeconds < 10 || first.JitterSeconds > 60 || !first.PlannedAt.Equal(base.Add(time.Duration(first.JitterSeconds)*time.Second)) {
		t.Fatalf("invalid jittered slot: %#v", first)
	}
	state, err := readState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newRuntime()
	restarted.cfg = cfg
	restarted.state = state
	s2 := newRuntimeScheduler(restarted)
	restarted.scheduler = s2
	s2.planSlot(cfg.Jobs[0], base, base.Add(30*time.Second))
	if got := restarted.state.Slots[key]; got != first {
		t.Fatalf("restart changed slot: got %#v, want %#v", got, first)
	}
	if next := cfg.Jobs[0].CronSchedule.Next(base); next.Hour() != 11 || next.Minute() != 0 {
		t.Fatalf("next slot drifted: %s", next)
	}
}

func TestFollowupPlansUseReportedResetAndNeverVirtualIdleReset(t *testing.T) {
	cfg := defaultPluginConfig()
	now := time.Date(2026, 9, 24, 6, 0, 0, 0, cfg.Location)
	fiveReset := time.Date(2026, 9, 24, 10, 10, 0, 0, cfg.Location)
	weekReset := time.Date(2026, 9, 24, 12, 37, 0, 0, cfg.Location)
	window := accountWindow{}
	observation := quotaObservation{
		CheckedAt: now,
		FiveHour:  quotaWindow{WindowMinutes: fiveHourMinutes, UsedPercent: 100, ResetAt: fiveReset},
		Weekly:    quotaWindow{WindowMinutes: weeklyMinutes, UsedPercent: 96, ResetAt: weekReset},
	}
	updateFollowupPlan(&window, observation)
	if !window.FiveHourFollowupAt.Equal(fiveReset.Add(90*time.Second)) || !window.WeeklyFollowupAt.Equal(weekReset.Add(90*time.Second)) {
		t.Fatalf("incorrect reset plans: %#v", window)
	}
	window.LastFiveHourR = fiveReset
	observation.FiveHour.UsedPercent = 0
	observation.FiveHour.ResetAt = now.Add(5 * time.Hour)
	updateFollowupPlan(&window, observation)
	if !window.FiveHourFollowupAt.IsZero() {
		t.Fatalf("virtual idle reset was scheduled: %#v", window)
	}
	window.LastWeeklyR = weekReset
	updateFollowupPlan(&window, observation)
	if !window.WeeklyFollowupAt.IsZero() {
		t.Fatalf("completed weekly reset was rescheduled: %#v", window)
	}
}

func TestNightFollowupsWaitForDaytime(t *testing.T) {
	location := defaultPluginConfig().Location
	for hour, want := range map[int]bool{4: false, 5: true, 22: true, 23: false} {
		at := time.Date(2026, 9, 24, hour, 10, 0, 0, location)
		if got := inFollowupHours(at, location); got != want {
			t.Fatalf("hour %d active=%v, want %v", hour, got, want)
		}
	}
}

func TestQuotaRetryIsBoundedAndRespectsRateLimit(t *testing.T) {
	r := newRuntime()
	cfg := defaultPluginConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 9, 24, 10, 12, 0, 0, cfg.Location)
	r.scheduleQuotaRetry(cfg, "acct-test", now, "usage_http_429")
	if !r.state.Accounts["acct-test"].QuotaRetryAt.IsZero() {
		t.Fatal("quota 429 must not get an immediate retry")
	}
	r.scheduleQuotaRetry(cfg, "acct-test", now, "usage_transport_failed")
	first := r.state.Accounts["acct-test"]
	if first.QuotaRetryStep != 1 || !first.QuotaRetryAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("first retry: %#v", first)
	}
	first.QuotaRetryAt = time.Time{}
	r.state.Accounts["acct-test"] = first
	r.scheduleQuotaRetry(cfg, "acct-test", now.Add(2*time.Minute), "usage_transport_failed")
	second := r.state.Accounts["acct-test"]
	if second.QuotaRetryStep != 2 || !second.QuotaRetryAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("second retry: %#v", second)
	}
	second.QuotaRetryAt = time.Time{}
	r.state.Accounts["acct-test"] = second
	r.scheduleQuotaRetry(cfg, "acct-test", now.Add(10*time.Minute), "usage_transport_failed")
	if !r.state.Accounts["acct-test"].QuotaRetryAt.IsZero() {
		t.Fatal("third retry was scheduled")
	}
}

func TestResetPlanSurvivesEarlyHourlyRead(t *testing.T) {
	cfg := defaultPluginConfig()
	now := time.Date(2026, 9, 24, 14, 0, 0, 0, cfg.Location)
	reset := now.Add(time.Hour)
	window := accountWindow{}
	updateFollowupPlan(&window, quotaObservation{CheckedAt: now, FiveHour: quotaWindow{WindowMinutes: fiveHourMinutes, UsedPercent: 80, ResetAt: reset}})
	if window.FiveHourFollowupAt.IsZero() {
		t.Fatal("missing reset followup")
	}
	// The hourly check overlaps R but fails to prove idle. Retain R+90s.
	updateFollowupPlan(&window, quotaObservation{CheckedAt: reset, FiveHour: quotaWindow{WindowMinutes: fiveHourMinutes, UsedPercent: 0, ResetAt: reset.Add(5 * time.Hour)}})
	if !window.FiveHourFollowupAt.Equal(reset.Add(90 * time.Second)) {
		t.Fatalf("early hourly read removed followup: %#v", window)
	}
}

func TestSchedulerStateWriteFailureDoesNotConsumeSlot(t *testing.T) {
	cfg := defaultPluginConfig()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.StatePath = filepath.Join(blocker, "state.json")
	r := newRuntime()
	r.cfg = cfg
	s := newRuntimeScheduler(r)
	r.scheduler = s
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, cfg.Location)
	s.planSlot(cfg.Jobs[0], base, base)
	if len(r.state.Slots) != 0 {
		t.Fatal("unpersisted slot remained in memory")
	}
	key := slotKey(cfg.Jobs[0].Name, base)
	r.state.Slots[key] = scheduledSlot{Job: cfg.Jobs[0].Name, BaseAt: base, PlannedAt: base.Add(10 * time.Second)}
	s.processDue(base.Add(11 * time.Second))
	if !r.state.Slots[key].StartedAt.IsZero() || r.running {
		t.Fatal("unpersisted slot was consumed or dispatched")
	}
}
