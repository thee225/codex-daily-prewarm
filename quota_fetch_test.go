package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func usageFixture(now time.Time, fiveUsed, weekUsed float64, fiveReset int64) []byte {
	return []byte(fmt.Sprintf(`{"rate_limit":{"allowed":true,"primary_window":{"used_percent":%v,"reset_after_seconds":%d,"reset_at":%d},"secondary_window":{"used_percent":%v,"reset_after_seconds":360000,"reset_at":%d}}}`, fiveUsed, fiveReset-now.Unix(), fiveReset, weekUsed, now.Unix()+360000))
}

func TestConfirmedIdleNeedsMovingVirtualResetAndWeeklyHeadroom(t *testing.T) {
	now := time.Unix(1790213000, 0)
	first, err := parseUsageBody(usageFixture(now, 0, 50, now.Unix()+18000), now)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(3 * time.Second)
	second, err := parseUsageBody(usageFixture(later, 0, 50, later.Unix()+18000), later)
	if err != nil || !confirmedIdle(first, second) {
		t.Fatalf("moving idle reset rejected: %v", err)
	}
	active, err := parseUsageBody(usageFixture(later, 0, 50, now.Unix()+18000), later)
	if err != nil || confirmedIdle(first, active) {
		t.Fatalf("fixed active reset accepted: %v", err)
	}
	noWeek, err := parseUsageBody(usageFixture(later, 0, 100, later.Unix()+18000), later)
	if err != nil || confirmedIdle(first, noWeek) {
		t.Fatalf("exhausted weekly quota accepted: %v", err)
	}
}

func TestRecentAttemptsRollingWindow(t *testing.T) {
	now := time.Now()
	times := []time.Time{now.Add(-25 * time.Hour), now.Add(-23 * time.Hour), now.Add(-time.Minute)}
	if got := len(recentAttempts(times, now)); got != 2 {
		t.Fatalf("recent attempts=%d, want 2", got)
	}
}

func TestReservationSurvivesRestartAndLimitsCalls(t *testing.T) {
	r := newRuntime()
	cfg := defaultPluginConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	now := time.Now()
	for i := 0; i < 5; i++ {
		if !r.reserveAttempt(cfg, "acct-test", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("reservation %d rejected", i)
		}
	}
	if r.reserveAttempt(cfg, "acct-test", now.Add(6*time.Second)) {
		t.Fatal("sixth model call was allowed")
	}
	state, err := readState(cfg.StatePath)
	if err != nil || len(state.Accounts["acct-test"].AttemptTimes) != 5 {
		t.Fatalf("persisted attempt count=%d, error=%v", len(state.Accounts["acct-test"].AttemptTimes), err)
	}
}
