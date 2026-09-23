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

func TestDynamicAccountCount(t *testing.T) {
	for _, count := range []int{1, 2, 3, 5, 20} {
		if abortForAccountCount(0, count, "schedule") {
			t.Fatalf("dynamic account inventory %d was rejected", count)
		}
	}
	if !abortForAccountCount(3, 2, "schedule") {
		t.Fatal("an explicitly configured inventory guard must still work")
	}
}

func TestPerAccountWindowDecision(t *testing.T) {
	now := time.Date(2026, 9, 23, 11, 30, 0, 0, time.UTC)
	r := newRuntime()
	r.state.Accounts["active"] = accountWindow{ResetAt: now.Add(time.Hour)}
	r.state.Accounts["quota"] = accountWindow{RetryAfter: now.Add(48 * time.Hour)}
	r.state.Accounts["unknown"] = accountWindow{LastProbeAt: now.Add(-time.Hour)}
	r.state.Accounts["expired"] = accountWindow{ResetAt: now.Add(-time.Minute)}
	if got := r.skipReason("active", now); got != "active_window" {
		t.Fatalf("active skip = %q", got)
	}
	if got := r.skipReason("quota", now); got != "quota_retry_pending" {
		t.Fatalf("quota skip = %q", got)
	}
	if got := r.skipReason("unknown", now); got != "unverified_window_cooldown" {
		t.Fatalf("unknown skip = %q", got)
	}
	for _, account := range []string{"expired", "new"} {
		if got := r.skipReason(account, now); got != "" {
			t.Fatalf("%s unexpectedly skipped: %q", account, got)
		}
	}
}

func TestFallbackOnlyOnExplicitUnsupportedModel(t *testing.T) {
	if !explicitUnsupportedModel(http.StatusBadRequest, []byte(`{"error":{"code":"model_not_found"}}`)) {
		t.Fatal("known model error should allow fallback")
	}
	for _, test := range []struct {
		status int
		body   string
	}{
		{http.StatusTooManyRequests, `{"error":{"code":"model_not_found"}}`},
		{http.StatusBadRequest, `{"error":{"code":"invalid_request"}}`},
		{http.StatusBadRequest, `bad response`},
	} {
		if explicitUnsupportedModel(test.status, []byte(test.body)) {
			t.Fatalf("unsafe fallback: %d %s", test.status, test.body)
		}
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
	state.Accounts["acct-123"] = accountWindow{ResetAt: time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)}
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
	if !loaded.Daily["2026-09-21"]["acct-123"] || len(loaded.History) != 1 || loaded.Accounts["acct-123"].ResetAt.IsZero() {
		t.Fatalf("loaded = %#v", loaded)
	}
}

func TestLegacyStateSeedsObservedWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := newState()
	state.Accounts = nil
	reset := time.Date(2026, 9, 23, 20, 0, 0, 0, time.UTC)
	state.History = []runRecord{{Accounts: []accountResult{{Account: "acct-legacy", ResponseReceived: true, FinishedAt: reset.Add(-5 * time.Hour), PrimaryResetAt: reset}}}}
	if err := writeState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := readState(path)
	if err != nil || !loaded.Accounts["acct-legacy"].ResetAt.Equal(reset) {
		t.Fatalf("legacy migration: %#v, %v", loaded.Accounts, err)
	}
}

func TestResetSpreadRequiresEveryAvailableAccount(t *testing.T) {
	r := newRuntime()
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	auths := []pluginapi.HostAuthFileEntry{{ID: "a"}, {ID: "b"}}
	r.state.Accounts[accountFingerprint("a")] = accountWindow{ResetAt: now.Add(5 * time.Hour)}
	if count, spread := r.resetSpread(auths, now); count != 1 || spread != nil {
		t.Fatalf("partial reset observation: count=%d spread=%v", count, spread)
	}
	r.state.Accounts[accountFingerprint("b")] = accountWindow{ResetAt: now.Add(5*time.Hour + 30*time.Second)}
	if count, spread := r.resetSpread(auths, now); count != 2 || spread == nil || *spread != 30 {
		t.Fatalf("complete reset observation: count=%d spread=%v", count, spread)
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
