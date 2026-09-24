package main

import (
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
