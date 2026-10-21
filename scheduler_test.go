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
	cfg.AutomaticEnabled = true
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

func TestQuotaRefreshScheduleIsIndependentAndCoalesces(t *testing.T) {
	cfg, err := parsePluginConfig([]byte("automatic_enabled: true\nquota_refresh_schedule: '*/30 * * * *'\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	r := newRuntime()
	r.cfg = cfg
	s := newRuntimeScheduler(r)
	r.scheduler = s
	jobs := scheduledJobs(cfg)
	if len(jobs) != 2 {
		t.Fatalf("jobs: %+v", jobs)
	}
	quota := jobs[1]
	for _, hour := range []int{0, 4, 5, 12, 23} {
		base := time.Date(2026, 10, 21, hour, 0, 0, 0, cfg.Location)
		s.planSlot(quota, base, base)
		_, exists := r.state.Slots[slotKey(quota.Name, base)]
		if exists == (hour >= 5) {
			t.Fatalf("unexpected quota slot at %v: %v", base, exists)
		}
		if hour >= 5 {
			if _, ok := r.state.Slots[slotKey(quota.Name, base.Add(30*time.Minute))]; !ok {
				t.Fatal("coalescing lost next half-hour slot")
			}
		}
	}
	cfg.AutomaticEnabled = false
	r.cfg = cfg
	jobs = scheduledJobs(cfg)
	if len(jobs) != 1 || !jobs[0].QuotaOnly {
		t.Fatal("quota refresh must survive prewarm disabled")
	}
	noon := time.Date(2026, 10, 21, 12, 0, 0, 0, cfg.Location)
	s.planSlot(jobs[0], noon, noon)
	if _, ok := r.state.Slots[slotKey(quota.Name, noon)]; !ok {
		t.Fatal("disabled prewarm incorrectly suppresses refresh")
	}
}

func TestQuotaRefreshDispatchAtNightWithoutPrewarming(t *testing.T) {
	cfg, err := parsePluginConfig([]byte("quota_refresh_schedule: '*/30 * * * *'\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	r := newRuntime()
	r.cfg = cfg
	s := newRuntimeScheduler(r)
	r.scheduler = s
	received := make(chan runRequest, 1)
	r.runTask = func(_ pluginConfig, req runRequest) runRecord { received <- req; return runRecord{} }
	base := time.Date(2026, 10, 21, 0, 30, 0, 0, cfg.Location)
	job := scheduledJobs(cfg)[0]
	s.planSlot(job, base, base)
	s.processDue(base.Add(time.Minute))
	select {
	case req := <-received:
		if !req.ReadOnly || req.Trigger != "quota_refresh" {
			t.Fatalf("unsafe request: %+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("night quota refresh did not dispatch")
	}
	r.runWG.Wait()
	state, err := readState(cfg.StatePath)
	if err != nil || state.Slots[slotKey(job.Name, base)].StartedAt.IsZero() {
		t.Fatalf("slot not durable: %v", err)
	}
	restarted := newRuntime()
	restarted.cfg = cfg
	restarted.state = state
	s2 := newRuntimeScheduler(restarted)
	restarted.scheduler = s2
	s2.planSlot(job, base, base)
	if restarted.state.Slots[slotKey(job.Name, base)].StartedAt.IsZero() {
		t.Fatal("restart consumed completed slot twice")
	}
}

func TestQuotaRefreshConfigValidation(t *testing.T) {
	if cfg, err := parsePluginConfig(nil); err != nil || cfg.QuotaCronSchedule != nil {
		t.Fatal("legacy config changed")
	}
	for _, value := range []string{"invalid", "* * * * * *", "59 23 * * *"} {
		if _, err := parsePluginConfig([]byte("quota_refresh_schedule: '" + value + "'")); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}

func TestQuotaRefreshCronRegistrationAndDayCoverage(t *testing.T) {
	r := newRuntime()
	path := filepath.Join(t.TempDir(), "state.json")
	// A future wall clock avoids dispatching a real task while checking registration.
	r.now = func() time.Time { return time.Date(2026, 10, 21, 2, 10, 0, 0, defaultPluginConfig().Location) }
	r.runTask = func(pluginConfig, runRequest) runRecord { return runRecord{} }
	if err := r.configure([]byte("automatic_enabled: false\nquota_refresh_schedule: '*/30 * * * *'\nstate_path: '" + path + "'\n")); err != nil {
		t.Fatal(err)
	}
	defer r.shutdown()
	if r.cron == nil || len(r.cron.Entries()) != 1 || r.scheduler == nil {
		t.Fatal("independent quota cron not registered")
	}
	cfg, err := parsePluginConfig([]byte("automatic_enabled: true\nquota_refresh_schedule: '*/30 * * * *'\n"))
	if err != nil {
		t.Fatal(err)
	}
	total, quota, warm := 0, 0, 0
	start := time.Date(2026, 10, 21, 0, 0, 0, 0, cfg.Location)
	for at := start; at.Before(start.Add(24 * time.Hour)); at = at.Add(30 * time.Minute) {
		total++
		if quotaCoveredByPrewarm(cfg, at) {
			warm++
		} else {
			quota++
		}
	}
	if total != 48 || warm != 19 || quota != 29 {
		t.Fatalf("daily query coverage: total=%d warm=%d quota=%d", total, warm, quota)
	}
}
