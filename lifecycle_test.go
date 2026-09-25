package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitForClosed(t *testing.T, r *runtime) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		r.mu.RLock()
		closed := r.closed
		r.mu.RUnlock()
		if closed {
			return
		}
		select {
		case <-deadline:
			t.Fatal("runtime did not stop accepting runs")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestReconfigureWaitsBeforeReadingReservedAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	r := newRuntime()
	r.cfg.StatePath = path
	if err := writeState(path, newState()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	r.runTask = func(cfg pluginConfig, _ runRequest) runRecord {
		close(entered)
		<-release
		if !r.reserveAttempt(cfg, "acct-012345abcdef", time.Now()) {
			t.Error("reservation failed")
		}
		return runRecord{}
	}
	if err := r.startRunJob(runRequest{Trigger: "manual", Job: r.cfg.Jobs[0]}); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan error, 1)
	go func() { done <- r.configure([]byte("state_path: " + path + "\n")) }()
	waitForClosed(t, r)
	select {
	case err := <-done:
		t.Fatalf("reconfigure completed before accepted run: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	window := r.state.Accounts["acct-012345abcdef"]
	if len(window.AttemptTimes) != 1 || window.LastPrewarmAt.IsZero() {
		t.Fatalf("reconfigure lost reservation: %#v", window)
	}
	loaded, err := readState(path)
	if err != nil || len(loaded.Accounts["acct-012345abcdef"].AttemptTimes) != 1 {
		t.Fatalf("persisted attempt lost: %v %#v", err, loaded.Accounts)
	}
}

func TestShutdownDrainsAcceptedHostWorkAndRejectsNewRuns(t *testing.T) {
	r := newRuntime()
	entered := make(chan struct{})
	release := make(chan struct{})
	hostWorkFinished := make(chan struct{})
	r.runTask = func(_ pluginConfig, _ runRequest) runRecord {
		close(entered)
		<-release
		// Represents the final in-flight host callback after shutdown begins.
		close(hostWorkFinished)
		return runRecord{}
	}
	if err := r.startRunJob(runRequest{Trigger: "manual", Job: r.cfg.Jobs[0]}); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan struct{})
	go func() { r.shutdown(); close(done) }()
	waitForClosed(t, r)
	if err := r.startRunJob(runRequest{Trigger: "manual", Job: r.cfg.Jobs[0]}); err == nil {
		t.Fatal("accepted run after shutdown gate closed")
	}
	select {
	case <-done:
		t.Fatal("shutdown returned while accepted host work was pending")
	default:
	}
	close(release)
	<-done
	select {
	case <-hostWorkFinished:
	default:
		t.Fatal("shutdown returned before host work completed")
	}
	if err := r.configure(nil); err == nil {
		t.Fatal("final shutdown allowed runtime to reopen after host API unload")
	}
}

func TestQuiesceAllowsLaterReconfigure(t *testing.T) {
	r := newRuntime()
	r.quiesceOnly()
	if err := r.configure([]byte("automatic_enabled: false\n")); err != nil {
		t.Fatalf("reconfigure after temporary quiesce: %v", err)
	}
	if r.closed {
		t.Fatal("runtime remained closed after reconfigure")
	}
}

func TestQuiesceInterruptsWaitWithoutStartingMoreWork(t *testing.T) {
	r := newRuntime()
	stop := r.stopCh
	entered := make(chan struct{})
	r.runTask = func(_ pluginConfig, _ runRequest) runRecord {
		close(entered)
		if waitForRun(stop, time.Hour) {
			t.Error("wait did not stop")
		}
		return runRecord{ErrorCode: "interrupted"}
	}
	if err := r.startRunJob(runRequest{Trigger: "manual", Job: r.cfg.Jobs[0]}); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan struct{})
	go func() { r.quiesceOnly(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("quiesce waited for the full idle delay")
	}
	if !r.closed || r.lastError != "interrupted" {
		t.Fatalf("quiesce state: closed=%v error=%s", r.closed, r.lastError)
	}
}

func TestBadNewStateDoesNotStopHealthyConfiguration(t *testing.T) {
	oldPath := filepath.Join(t.TempDir(), "old.json")
	badPath := filepath.Join(t.TempDir(), "bad.json")
	if err := writeState(oldPath, newState()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRuntime()
	if err := r.configure([]byte("automatic_enabled: false\nstate_path: " + oldPath)); err != nil {
		t.Fatal(err)
	}
	if err := r.configure([]byte("automatic_enabled: false\nstate_path: " + badPath)); err == nil {
		t.Fatal("invalid new state was accepted")
	}
	if r.closed || r.cfg.StatePath != oldPath {
		t.Fatal("healthy old configuration was stopped")
	}
	r.runTask = func(pluginConfig, runRequest) runRecord { return runRecord{} }
	if err := r.startRun("manual", false); err != nil {
		t.Fatalf("old scheduler rejected a run: %v", err)
	}
	r.runWG.Wait()
}

func TestCorruptCurrentStateFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeState(path, newState()); err != nil {
		t.Fatal(err)
	}
	r := newRuntime()
	config := []byte("automatic_enabled: false\nstate_path: " + path)
	if err := r.configure(config); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.configure(config); err == nil || !r.closed {
		t.Fatalf("corrupt active state did not fail closed: err=%v closed=%v", err, r.closed)
	}
}

func TestMissingStateCannotEraseReservedAttempts(t *testing.T) {
	oldPath := filepath.Join(t.TempDir(), "state.json")
	r := newRuntime()
	r.cfg.StatePath = oldPath
	r.state.Accounts["acct-test"] = accountWindow{AttemptTimes: []time.Time{time.Now().UTC()}}
	if err := writeState(oldPath, r.state); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(t.TempDir(), "missing.json")
	if err := r.configure([]byte("automatic_enabled: false\nstate_path: " + missingPath)); err == nil || r.closed {
		t.Fatalf("new missing state should leave old runtime open: err=%v closed=%v", err, r.closed)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	if err := r.configure([]byte("automatic_enabled: false\nstate_path: " + oldPath)); err == nil || !r.closed {
		t.Fatalf("missing active state did not fail closed: err=%v closed=%v", err, r.closed)
	}
}

func TestInterruptedClaimsArePersistentlyRequeued(t *testing.T) {
	r := newRuntime()
	r.cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	due := time.Now().UTC()
	slot := scheduledSlot{Job: "default", BaseAt: due, PlannedAt: due, StartedAt: due}
	r.state.Slots["slot"] = slot
	r.requeueInterrupted(runRequest{Trigger: "schedule", Slot: "slot"})
	r.requeueInterrupted(runRequest{Trigger: "reset_followup", TargetAccount: "acct-test", FollowupKind: "five_hour+weekly", DueAt: due})
	state, err := readState(r.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Slots["slot"].StartedAt.IsZero() {
		t.Fatal("interrupted slot remained consumed")
	}
	window := state.Accounts["acct-test"]
	if !window.FiveHourFollowupAt.Equal(due) || !window.WeeklyFollowupAt.Equal(due) {
		t.Fatalf("interrupted follow-up was lost: %#v", window)
	}
}

func TestQuiesceRequeuesAnAcceptedPendingSlot(t *testing.T) {
	r := newRuntime()
	r.cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	base := time.Now().UTC()
	r.state.Slots["pending"] = scheduledSlot{Job: "default", BaseAt: base, PlannedAt: base, StartedAt: base}
	stop := r.stopCh
	entered := make(chan struct{})
	r.runTask = func(pluginConfig, runRequest) runRecord {
		close(entered)
		waitForRun(stop, time.Hour)
		return runRecord{ErrorCode: "interrupted"}
	}
	job := r.cfg.Jobs[0]
	if err := r.startRunJob(runRequest{Trigger: "manual", Job: job}); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := r.startRunJob(runRequest{Trigger: "schedule", Job: job, Slot: "pending"}); err != nil {
		t.Fatal(err)
	}
	r.quiesceOnly()
	state, err := readState(r.cfg.StatePath)
	if err != nil || !state.Slots["pending"].StartedAt.IsZero() {
		t.Fatalf("pending slot was lost: err=%v slot=%#v", err, state.Slots["pending"])
	}
}
