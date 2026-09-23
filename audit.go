package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// auditEntry contains only anonymized account IDs and bounded execution metadata.
// It must never include request bodies, response bodies, API keys, or OAuth data.
type auditEntry struct {
	At            time.Time  `json:"at"`
	Event         string     `json:"event"`
	RunID         string     `json:"run_id"`
	Trigger       string     `json:"trigger"`
	Job           string     `json:"job"`
	SourceAccount string     `json:"source_account,omitempty"`
	Run           *runRecord `json:"run,omitempty"`
}

func auditPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "codex-daily-prewarm.events.jsonl")
}

func appendAuditEntry(statePath string, entry auditEntry) error {
	path := auditPath(statePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if info, err := file.Stat(); err == nil && info.Mode().Perm()&0o077 != 0 {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return err
		}
	}
	_, writeErr := file.WriteString(strings.TrimSpace(string(raw)) + "\n")
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
