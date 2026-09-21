package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

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
