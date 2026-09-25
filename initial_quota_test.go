package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestInitialQuotaQueriesRunAtMostThreeConcurrently(t *testing.T) {
	auths := []pluginapi.HostAuthFileEntry{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"}}
	started := make(chan struct{}, len(auths))
	release := make(chan struct{})
	finished := make(chan []firstQuota, 1)
	var active, peak atomic.Int32
	go func() {
		finished <- fetchInitialUsages(auths, func(auth pluginapi.HostAuthFileEntry) (quotaObservation, string) {
			current := active.Add(1)
			for {
				old := peak.Load()
				if current <= old || peak.CompareAndSwap(old, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return quotaObservation{Status: auth.ID}, ""
		})
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("three concurrent quota queries did not start")
		}
	}
	if got := peak.Load(); got != 3 {
		t.Fatalf("peak concurrency=%d, want 3", got)
	}
	close(release)
	select {
	case results := <-finished:
		if len(results) != len(auths) || peak.Load() > 3 {
			t.Fatalf("results=%d, peak=%d", len(results), peak.Load())
		}
		for i, result := range results {
			if result.observation.Status != auths[i].ID {
				t.Fatalf("result order changed at %d: %#v", i, result)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quota query round did not finish")
	}
}

func TestStoppingInitialQuotaQueriesDoesNotStartQueuedAccounts(t *testing.T) {
	auths := []pluginapi.HostAuthFileEntry{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"}}
	stop := make(chan struct{})
	started := make(chan struct{}, len(auths))
	release := make(chan struct{})
	finished := make(chan []firstQuota, 1)
	go func() {
		finished <- fetchInitialUsagesUntil(auths, stop, func(pluginapi.HostAuthFileEntry) (quotaObservation, string) {
			started <- struct{}{}
			<-release
			return quotaObservation{}, ""
		})
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("initial quota workers did not start")
		}
	}
	close(stop)
	close(release)
	select {
	case results := <-finished:
		interrupted := 0
		for _, result := range results {
			if result.reason == "interrupted" {
				interrupted++
			}
		}
		if interrupted != 2 || len(started) != 0 {
			t.Fatalf("stop started queued work: interrupted=%d extra_started=%d", interrupted, len(started))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initial quota workers did not stop")
	}
}
