package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditIsPrivateJSONLWithAnonymizedAccounts(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state", "state.json")
	account := accountFingerprint("sensitive-auth-id")
	entry := auditEntry{
		At: time.Now(), Event: "run_finished", RunID: "1", Trigger: "first_use", Job: "first-use",
		SourceAccount: account,
		Run:           &runRecord{ID: "1", Trigger: "first_use", SourceAccount: account, Accounts: []accountResult{{Account: account, SkipReason: "active_five_hour_window"}}},
	}
	if err := appendAuditEntry(statePath, entry); err != nil {
		t.Fatal(err)
	}
	path := auditPath(statePath)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("audit file mode = %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sensitive-auth-id") || !strings.Contains(string(raw), "active_five_hour_window") || len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 1 {
		t.Fatalf("unexpected audit content: %s", raw)
	}
}
