package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestManagementRegistration(t *testing.T) {
	registration := managementRegistration()
	if len(registration.Resources) != 1 || len(registration.Routes) != 3 {
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
	page := dispatchManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/codex-daily-prewarm/status"})
	if page.StatusCode != http.StatusOK || !strings.Contains(string(page.Body), "一键预热（先查额度）") {
		t.Fatalf("resource page status = %d", page.StatusCode)
	}
	if !strings.Contains(string(page.Body), "/v0/management/plugins/codex-daily-prewarm") {
		t.Fatal("button does not use the authenticated management route")
	}
	response := dispatchManagement(pluginapi.ManagementRequest{Method: http.MethodPost, Path: "/v0/resource/plugins/codex-daily-prewarm/run-now"})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unauthenticated resource POST status = %d", response.StatusCode)
	}
}

func TestStatusPageDisplaysConfiguredTimezone(t *testing.T) {
	cfg := defaultPluginConfig().public()
	page := renderStatusPage(runtimeStatus{
		Config:    cfg,
		NextRunAt: time.Date(2026, 9, 24, 10, 0, 38, 0, time.UTC),
		Accounts:  map[string]accountWindow{"acct-test": {FiveHourFollowupAt: time.Date(2026, 9, 24, 11, 9, 18, 0, time.UTC)}},
	})
	if !strings.Contains(page, "2026-09-24 18:00:38 CST") || !strings.Contains(page, "2026-09-24 19:09:18 CST") {
		t.Fatal("planned times were not shown in Asia/Shanghai")
	}
}
