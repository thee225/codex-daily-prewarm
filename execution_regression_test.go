package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Exercise the real quota parser, confirmation, model execution and persistence
// together. Only the native host transport is replaced; no production requests.
func TestWholeRunResults(t *testing.T) {
	for _, mode := range []string{"success", "fallback", "no_headers", "quota_429", "dry_run", "observe", "all_query_failed", "partial_query_failed", "state_failed", "final_state_failed"} {
		t.Run(mode, func(t *testing.T) {
			r := newRuntime()
			cfg := r.cfg
			cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
			cfg.DryRun = mode == "dry_run"
			cfg.AccountSpacing = 0
			cfg.RetryCount = 0
			r.cfg = cfg
			var mu sync.Mutex
			calls := 0
			old := callHost
			t.Cleanup(func() { callHost = old })
			encode := func(v any) (json.RawMessage, error) { return json.Marshal(v) }
			callHost = func(method string, payload any) (json.RawMessage, error) {
				mu.Lock()
				defer mu.Unlock()
				switch method {
				case pluginabi.MethodHostAuthList:
					if mode == "state_failed" {
						if err := os.Mkdir(cfg.StatePath, 0700); err != nil {
							t.Fatal(err)
						}
					}
					auths := []pluginapi.HostAuthFileEntry{{ID: "a", AuthIndex: "a", Provider: "codex"}}
					if mode == "partial_query_failed" {
						auths = append(auths, pluginapi.HostAuthFileEntry{ID: "b", AuthIndex: "b", Provider: "codex"})
					}
					return encode(authListResponse{Files: auths})
				case pluginabi.MethodHostAuthGet:
					req := payload.(pluginapi.HostAuthGetRequest)
					if mode == "all_query_failed" || mode == "partial_query_failed" && req.AuthIndex == "a" {
						return nil, errors.New("synthetic failure")
					}
					return encode(pluginapi.HostAuthGetResponse{JSON: []byte(`{"access_token":"test-only","account_id":"fixture"}`)})
				case pluginabi.MethodHostHTTPDo:
					now := time.Now().Unix()
					body, _ := json.Marshal(map[string]any{"rate_limit": map[string]any{"allowed": true, "primary_window": map[string]any{"used_percent": 0, "reset_after_seconds": 18000, "reset_at": now + 18000}, "secondary_window": map[string]any{"used_percent": 20, "reset_after_seconds": 86400, "reset_at": now + 86400}}})
					return encode(pluginapi.HTTPResponse{StatusCode: 200, Body: body})
				case pluginabi.MethodHostModelExecute:
					calls++
					if r.attemptCount(accountFingerprint("a"), time.Now()) == 0 && mode != "partial_query_failed" {
						t.Error("model call preceded durable reservation")
					}
					if mode == "fallback" && calls == 1 {
						return encode(pluginapi.HostModelExecutionResponse{StatusCode: 404, Body: []byte(`{"error":{"code":"model_not_found"}}`)})
					}
					if mode == "quota_429" {
						return nil, &hostCallbackError{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`}
					}
					if mode == "final_state_failed" {
						if err := os.Remove(cfg.StatePath); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(cfg.StatePath, 0700); err != nil {
							t.Fatal(err)
						}
					}
					h := http.Header{}
					if mode != "no_headers" {
						h.Set("X-Codex-Primary-Window-Minutes", "300")
						h.Set("X-Codex-Primary-Used-Percent", "1")
						h.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(time.Now().Add(4*time.Hour).Unix(), 10))
						h.Set("X-Codex-Secondary-Window-Minutes", "10080")
						h.Set("X-Codex-Secondary-Used-Percent", "21")
						h.Set("X-Codex-Secondary-Reset-After-Seconds", "86400")
					}
					return encode(pluginapi.HostModelExecutionResponse{StatusCode: 200, Headers: h, Body: []byte(`{"choices":[{"message":{"content":"hi"}}]}`)})
				case pluginabi.MethodHostLog:
					return encode(map[string]any{})
				default:
					t.Errorf("unexpected host method %s", method)
					return nil, errors.New("unexpected method")
				}
			}
			got := r.executeRun(cfg, runRequest{Trigger: "manual", Job: cfg.Jobs[0], ObserveOnly: mode == "observe"})
			if got.FinishedAt.IsZero() {
				t.Fatal("returned record was not finalized")
			}
			switch mode {
			case "all_query_failed", "state_failed":
				if got.Success || got.Failed != 1 || got.Skipped != 0 || calls != 0 || got.ErrorCode == "" {
					t.Fatalf("failure masked: %+v calls=%d", got, calls)
				}
			case "partial_query_failed":
				if got.Success || got.Failed != 1 || got.Succeeded != 1 || got.Skipped != 0 || got.WouldWarm != 1 {
					t.Fatalf("partial failure masked: %+v", got)
				}
			case "final_state_failed":
				if got.Success || got.ErrorCode != "state_write_failed" || r.history()[0].Success {
					t.Fatalf("write failure masked: %+v", got)
				}
			case "quota_429":
				if got.Success || got.Failed != 1 || !r.accountWindow(accountFingerprint("a")).RetryAfter.After(time.Now()) {
					t.Fatalf("429 reset lost: %+v", got)
				}
			case "dry_run", "observe":
				if !got.Success || got.WouldWarm != 1 || got.Skipped != 1 || calls != 0 {
					t.Fatalf("simulation counts: %+v", got)
				}
			default:
				if !got.Success || got.WouldWarm != 1 || got.Succeeded != 1 || got.Failed != 0 {
					t.Fatalf("success counts: %+v", got)
				}
				a := got.Accounts[0]
				if a.QuotaBefore == nil || a.QuotaBefore.FiveHour.UsedPercent != 0 || a.QuotaBefore.Weekly.UsedPercent != 20 {
					t.Fatalf("admission evidence lost: %+v", a)
				}
				w := r.accountWindow(a.Account)
				if mode == "fallback" && (a.Model != cfg.FallbackModel || !a.FallbackUsed || calls != 2) {
					t.Fatalf("fallback lost: %+v", a)
				}
				if mode == "no_headers" {
					if w.Weekly.UsedPercent != 20 || w.FiveHour.WindowMinutes != 300 {
						t.Fatalf("trusted observation lost: %+v", w)
					}
				} else if w.FiveHour.UsedPercent != 1 || w.Weekly.UsedPercent != 21 || w.FiveHourFollowupAt.IsZero() || a.PrimaryResetAt.IsZero() || !w.LastPrewarmResetAt.Equal(w.FiveHour.ResetAt) {
					t.Fatalf("response quota lost: %+v %+v", a, w)
				}
			}
			if mode != "state_failed" && mode != "final_state_failed" {
				disk, err := readState(cfg.StatePath)
				if err != nil || len(disk.History) != 1 || disk.History[0].Success != got.Success || disk.History[0].Failed != got.Failed {
					t.Fatalf("persisted result mismatch: %v %+v", err, disk)
				}
			}
		})
	}
}
