package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestEligibleCodexAuths(t *testing.T) {
	files := []pluginapi.HostAuthFileEntry{
		{ID: "b", Provider: "codex", Status: "active", AccountType: "team"},
		{ID: "a", Type: "codex", Status: "ready", AccountType: "plus"},
		{ID: "disabled", Provider: "codex", Disabled: true},
		{ID: "unavailable", Provider: "codex", Unavailable: true},
		{ID: "other", Provider: "gemini"},
		{Provider: "codex"},
	}
	got := eligibleCodexAuths(files)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("eligible = %#v", got)
	}
}

func TestFirstUseContinuesWithFewerAvailableAccounts(t *testing.T) {
	if abortForAccountCount(3, 2, "first_use") {
		t.Fatal("first-use should still test other available accounts")
	}
	if !abortForAccountCount(3, 2, "schedule") {
		t.Fatal("scheduled run must retain the three-account guard")
	}
	if !abortForAccountCount(3, 4, "first_use") {
		t.Fatal("unexpected extra accounts must not receive requests")
	}
}

func TestFirstUseRetriesAfterNoAccountWasTested(t *testing.T) {
	now := time.Date(2026, 9, 23, 11, 30, 0, 0, time.UTC)
	state := newState()
	state.LastSyncAt = now.Add(-6 * time.Minute)
	state.History = []runRecord{{Trigger: "first_use", Attempted: 0, ErrorCode: "unexpected_account_count"}}
	if firstUseCoolingDown(state, now) {
		t.Fatal("a run that tested no account should retry after five minutes")
	}
	if !firstUseCoolingDown(state, now.Add(-5*time.Minute)) {
		t.Fatal("a failed run must not retry on every client request")
	}
	state.History[0].Attempted = 1
	if !firstUseCoolingDown(state, now) {
		t.Fatal("a run that tested an account must retain the five-hour cooldown")
	}
}

func TestValidModelResponse(t *testing.T) {
	valid := [][]byte{
		[]byte(`{"choices":[{"message":{"content":"hi"}}]}`),
		[]byte(`{"status":"completed","output":[]}`),
		[]byte(`{"output":[{"content":[{"text":"OK"}]}]}`),
	}
	for _, body := range valid {
		if !validModelResponse(body) {
			t.Fatalf("expected valid: %s", body)
		}
	}
	for _, body := range [][]byte{nil, []byte(`{}`), []byte(`{"choices":[]}`), []byte(`not-json`)} {
		if validModelResponse(body) {
			t.Fatalf("expected invalid: %s", body)
		}
	}
}

func TestPrimaryResetAt(t *testing.T) {
	now := time.Date(2026, 9, 21, 6, 0, 0, 0, time.UTC)
	headers := http.Header{"X-Codex-Primary-Reset-After-Seconds": []string{"18000"}}
	if got := primaryResetAt(headers, now); !got.Equal(now.Add(5 * time.Hour)) {
		t.Fatalf("reset = %v", got)
	}
	headers.Set("X-Codex-Primary-Reset-At", "1790000000")
	if got := primaryResetAt(headers, now); got.Unix() != 1790000000 {
		t.Fatalf("reset = %v", got)
	}
}

func TestUpstreamUsageLimitDetails(t *testing.T) {
	now := time.Date(2026, 9, 21, 6, 0, 0, 0, time.UTC)
	message := `{"error":{"type":"usage_limit_reached","resets_at":1790129498,"resets_in_seconds":156515}}`
	if got := upstreamResetAt(message, now); got.Unix() != 1790129498 {
		t.Fatalf("reset = %v", got)
	}
	if got := upstreamErrorCode(message, http.StatusTooManyRequests); got != "usage_limit_reached" {
		t.Fatalf("error code = %q", got)
	}
	if safeToRetryStatus(http.StatusTooManyRequests) {
		t.Fatal("usage-limit 429 must not be retried")
	}
}

func TestHostCallbackErrorClassification(t *testing.T) {
	err := &hostCallbackError{Code: "host_call_failed", Message: `{}`, HTTPStatus: http.StatusTooManyRequests}
	var got *hostCallbackError
	if !errors.As(err, &got) || got.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("host callback error = %#v", got)
	}
}

func TestStateRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	state := newState()
	state.Daily["2026-09-21"] = map[string]bool{"acct-123": true}
	state.History = []runRecord{{ID: "1", Date: "2026-09-21", Success: true}}
	if err := writeState(path, state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	loaded, err := readState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Daily["2026-09-21"]["acct-123"] || len(loaded.History) != 1 {
		t.Fatalf("loaded = %#v", loaded)
	}
}

func TestAccountFingerprintDoesNotExposeAuthID(t *testing.T) {
	fingerprint := accountFingerprint("sensitive-auth-id")
	if fingerprint == "sensitive-auth-id" || len(fingerprint) != len("acct-")+12 {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
}

func TestJobDailyKeysAreIndependentAndLegacyCompatible(t *testing.T) {
	account := "acct-123"
	if dailyKey("default", account) != account {
		t.Fatal("legacy daily state must remain readable")
	}
	if dailyKey("evening", account) == dailyKey("default", account) {
		t.Fatal("different jobs must not share daily completion")
	}
}

func TestFirstUseOnlyRespondsToSuccessfulExternalCodexUsage(t *testing.T) {
	valid := pluginapi.UsageRecord{Provider: "codex", AuthID: "auth-1", APIKey: "client-key", Generate: true}
	if !eligibleFirstUse(valid) {
		t.Fatal("successful external Codex request should trigger")
	}
	for name, mutate := range map[string]func(*pluginapi.UsageRecord){
		"own callback":   func(r *pluginapi.UsageRecord) { r.APIKey = "" },
		"failed":         func(r *pluginapi.UsageRecord) { r.Failed = true },
		"other provider": func(r *pluginapi.UsageRecord) { r.Provider = "gemini" },
		"no auth":        func(r *pluginapi.UsageRecord) { r.AuthID = "" },
		"no generation":  func(r *pluginapi.UsageRecord) { r.Generate = false },
	} {
		t.Run(name, func(t *testing.T) {
			record := valid
			mutate(&record)
			if eligibleFirstUse(record) {
				t.Fatal("unexpected first-use trigger")
			}
		})
	}
}

func TestFirstUseStartsOneRoundAndPersistsCooldown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	r := newRuntime()
	if err := r.configure([]byte("sync_on_first_use: true\nstate_path: " + path + "\n")); err != nil {
		t.Fatal(err)
	}
	record := pluginapi.UsageRecord{Provider: "codex", AuthID: "auth-1", APIKey: "client-key", Generate: true}
	r.handleUsage(record)
	r.handleUsage(record)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(r.history()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	history := r.history()
	if len(history) != 1 || history[0].Trigger != "first_use" || history[0].Job != "first-use" {
		t.Fatalf("unexpected history = %#v", history)
	}
	state, err := readState(path)
	if err != nil || state.LastSyncAt.IsZero() {
		t.Fatalf("sync state was not persisted: %#v, %v", state, err)
	}
}
