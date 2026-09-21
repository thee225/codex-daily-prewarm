package main

import (
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
