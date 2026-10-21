package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestManagementRegistration(t *testing.T) {
	registration := managementRegistration()
	if len(registration.Resources) != 1 || len(registration.Routes) != 5 {
		t.Fatalf("registration = %#v", registration)
	}
}

func TestStatusManagementResponseIsJSON(t *testing.T) {
	response := dispatchManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/plugins/codex-daily-prewarm/status"})
	if response.StatusCode != http.StatusOK || !json.Valid(response.Body) {
		t.Fatalf("response = %#v body=%s", response, response.Body)
	}
	if strings.Contains(string(response.Body), `"prompt":`) {
		t.Fatal("status leaked prompt text")
	}
}

func TestRunNowRejectsInvalidJSON(t *testing.T) {
	response := dispatchManagement(pluginapi.ManagementRequest{Method: http.MethodPost, Path: "/plugins/codex-daily-prewarm/run-now", Body: []byte(`{`)})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestResourcePageCannotStartUnauthenticatedRun(t *testing.T) {
	previousApp := app
	app = newRuntime()
	defer func() { app = previousApp }()
	app.state.Accounts["acct-012345abcdef"] = accountWindow{LastPrewarmAt: time.Now()}
	app.state.History = []runRecord{{ID: "private-run-id", Accounts: []accountResult{{Account: "acct-012345abcdef"}}}}
	page := dispatchManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/codex-daily-prewarm/status"})
	if page.StatusCode != http.StatusOK || !strings.Contains(string(page.Body), "一键预热（先查额度）") {
		t.Fatalf("resource page status = %d", page.StatusCode)
	}
	if !strings.Contains(string(page.Body), defaultModel) {
		t.Fatal("public aggregate model missing; deployment health check would fail")
	}
	if !strings.Contains(string(page.Body), "/v0/management/plugins/codex-daily-prewarm") {
		t.Fatal("button does not use the authenticated management route")
	}
	if strings.Contains(string(page.Body), "acct-012345abcdef") || strings.Contains(string(page.Body), "private-run-id") {
		t.Fatal("public resource leaked account or run details")
	}
	for _, required := range []string{"localStorage.getItem('cli-proxy-auth')", "auth.state.rememberPassword !== true", "auth.state.managementKey", "id=\"unlock-status\"", "验证并查看详情"} {
		if !strings.Contains(string(page.Body), required) {
			t.Fatalf("resource page missing CPA auth integration: %q", required)
		}
	}
	if strings.Contains(string(page.Body), "localStorage.getItem('managementKey')") {
		t.Fatal("resource page still reads the CPA legacy storage key")
	}
	managed := dispatchManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/codex-daily-prewarm/status"})
	if !strings.Contains(string(managed.Body), "acct-012345abcdef") {
		t.Fatal("management status lost account details")
	}
	response := dispatchManagement(pluginapi.ManagementRequest{Method: http.MethodPost, Path: "/v0/resource/plugins/codex-daily-prewarm/run-now"})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unauthenticated resource POST status = %d", response.StatusCode)
	}
}

func TestStatusPageDisplaysConfiguredTimezone(t *testing.T) {
	cfg := defaultPluginConfig().public()
	page := renderStatusPage(runtimeStatus{
		Config:        cfg,
		Notifications: notificationStatus{LastDigestAt: time.Date(2026, 9, 24, 14, 30, 0, 0, time.UTC), LastDigestResult: "accepted", NextDigestAt: time.Date(2026, 9, 25, 14, 30, 0, 0, time.UTC)},
		NextRunAt:     time.Date(2026, 9, 24, 10, 0, 38, 0, time.UTC),
		LastRun: &runRecord{
			Job: "default", FinishedAt: time.Date(2026, 9, 24, 11, 9, 22, 0, time.UTC),
			Discovered: 1, QuotaQueried: 1, WouldWarm: 1, Skipped: 1,
			Accounts: []accountResult{{Account: "acct-test", SkipReason: "followup_observe"}},
		},
		Accounts: map[string]accountWindow{"acct-test": {FiveHourFollowupAt: time.Date(2026, 9, 24, 11, 9, 18, 0, time.UTC)}},
	})
	if !strings.Contains(page, "2026-09-24 18:00:38 CST") || !strings.Contains(page, "2026-09-24 19:09:18 CST") {
		t.Fatal("planned times were not shown in Asia/Shanghai")
	}
	if !strings.Contains(page, "最近发送 2026-09-24 22:30:00 CST · 结果 accepted · 下次日报 2026-09-25 22:30:00 CST") {
		t.Fatal("digest status missing from management page")
	}
	if !strings.Contains(page, "查询账号 1/1，符合预热 1，预热账号 0，有效模型回复 0，跳过 1，错误 无") || !strings.Contains(page, "观察模式：符合预热条件，未发模型请求") || strings.Contains(page, "成功 0/1") {
		t.Fatal("observation was presented as a failed model request")
	}
}

func TestAccountDetailsRemainSeparateFromState(t *testing.T) {
	oldApp := app
	app = newRuntime()
	defer func() { app = oldApp }()
	app.cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	old := callHost
	defer func() { callHost = old }()
	calls := 0
	callHost = func(method string, payload any) (json.RawMessage, error) {
		calls++
		if method == pluginabi.MethodHostAuthGet {
			if payload.(pluginapi.HostAuthGetRequest).AuthIndex != "plus-index" {
				t.Fatal("wrong credential selected")
			}
			return json.Marshal(pluginapi.HostAuthGetResponse{JSON: []byte(`{"plan_type":"plus","access_token":"never-expose-token"}`)})
		}
		if method != pluginabi.MethodHostAuthList {
			t.Fatalf("unexpected host method: %s", method)
		}
		return json.Marshal(authListResponse{Files: []pluginapi.HostAuthFileEntry{
			{ID: "plus-id", AuthIndex: "plus-index", Provider: "codex", Email: "same@example.com", AccountType: "oauth"},
			{ID: "team-id", Provider: "codex", Email: "same@example.com", AccountType: "team"},
			{ID: "label-id", Provider: "codex", Label: "自定义账号", Email: "other@example.com", AccountType: "pro"},
			{ID: "file-id", Type: "codex", Name: "/private/auths/fallback.json", Disabled: true},
			{ID: "other-id", Provider: "gemini", Email: "hidden@example.com"},
		}})
	}
	for _, path := range []string{"/v0/resource/plugins/codex-daily-prewarm/account-details", "/v0/resource/plugins/codex-daily-prewarm/status", "/v0/management/plugins/codex-daily-prewarm/status", "/v0/management/plugins/codex-daily-prewarm/history"} {
		response := dispatchManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: path})
		if strings.Contains(string(response.Body), "same@example.com") || calls != 0 {
			t.Fatal("names loaded outside explicit authenticated details route")
		}
	}
	response := dispatchManagement(pluginapi.ManagementRequest{Method: http.MethodPut, Path: "/v0/management/plugins/codex-daily-prewarm/account-details", Body: []byte(`{"enabled":true}`)})
	var body struct {
		Accounts map[string]accountDisplayDetail `json:"accounts"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(response.Body, &body) != nil {
		t.Fatalf("details failed: %s", response.Body)
	}
	if calls != 2 || len(body.Accounts) != 4 {
		t.Fatalf("calls=%d accounts=%d", calls, len(body.Accounts))
	}
	plus, team := body.Accounts[accountFingerprint("plus-id")], body.Accounts[accountFingerprint("team-id")]
	if plus.Name != "same@example.com" || team.Name != plus.Name || plus.AccountType != "plus" || team.AccountType != "team" {
		t.Fatal("same-email plans lost")
	}
	if body.Accounts[accountFingerprint("label-id")].Name != "自定义账号" || body.Accounts[accountFingerprint("file-id")].Name != "fallback.json" {
		t.Fatal("name fallback incorrect")
	}
	for _, forbidden := range []string{"plus-id", "team-id", "/private/", "hidden@example.com", "other@example.com", "never-expose-token"} {
		if strings.Contains(string(response.Body), forbidden) {
			t.Fatalf("details leaked %s", forbidden)
		}
	}
	if response.Headers.Get("Cache-Control") != "no-store" {
		t.Fatal("identity response may be cached")
	}
}

func TestAccountDisplayPlanFailureIsUnknown(t *testing.T) {
	old := callHost
	defer func() { callHost = old }()
	for _, body := range []string{`{`, `{"json":"e30="}`, `{"json":"eyJwbGFuX3R5cGUiOiJ1bnN1cHBvcnRlZCJ9"}`} {
		callHost = func(string, any) (json.RawMessage, error) { return json.RawMessage(body), nil }
		if got := accountDisplayPlan(pluginapi.HostAuthFileEntry{AuthIndex: "fixture", AccountType: "oauth"}); got != "" {
			t.Fatalf("invalid metadata guessed plan: %s", got)
		}
	}
}

func TestGlobalAccountDisplayPersistsAndControlsAllNotificationBodies(t *testing.T) {
	r := newRuntime()
	r.cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	id := "acct-111111111111"
	on := accountDisplayState{Enabled: true, Accounts: map[string]accountDisplayDetail{id: {Name: "same@example.com", AccountType: "team"}}}
	if err := r.saveAccountDisplay(on); err != nil {
		t.Fatal(err)
	}
	disk, err := readState(r.cfg.StatePath)
	if err != nil || !disk.AccountDisplay.Enabled {
		t.Fatal("preference not persisted")
	}
	for _, body := range []string{id + " · active_five_hour_window", id + " 预热 2 次", id + " · 连续 3 轮额度未知"} {
		if got := r.displayNotification(body); !strings.Contains(got, "same@example.com · Team") || !strings.Contains(got, id) {
			t.Fatalf("name missing: %s", got)
		}
	}

	cfg := r.cfg
	cfg.BarkURL = "https://example.com/test-only"
	r.barkSend = func(req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		var body map[string]string
		if json.Unmarshal(req.Body, &body) != nil || !strings.Contains(body["body"], "same@example.com · Team") {
			t.Fatal("serialized Bark lost display name")
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200}`)}, nil
	}
	for _, title := range []string{"Codex 预热结果", "Codex 预热失败", "Codex 预热日报", "Codex 夜间预热失败汇总"} {
		if got := r.sendNotification(cfg, title, id, "active"); got != "accepted" {
			t.Fatal(got)
		}
	}
	if err := r.setAccountDisplay(false); err != nil {
		t.Fatal(err)
	}
	disk, err = readState(r.cfg.StatePath)
	if err != nil || disk.AccountDisplay.Enabled || len(disk.AccountDisplay.Accounts) > 0 {
		t.Fatal("disabled cache retained names")
	}
	if got := r.displayNotification(id); got != id {
		t.Fatal("disabled notifications retained names")
	}
	r.cfg.StatePath = filepath.Join(t.TempDir(), "not-a-file")
	if err := os.Mkdir(r.cfg.StatePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.saveAccountDisplay(on); err == nil || r.accountDisplay().Enabled {
		t.Fatal("failed write changed preference")
	}
}
