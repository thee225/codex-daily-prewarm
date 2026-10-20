package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestBarkPushKeepsNotificationTextInJSON(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.BarkURL = "https://bark.example/device-secret"
	body := "acct-012345abcdef 中文\nemoji 😀 % %25 周剩余 14%"
	got := sendBarkWith(cfg, "Codex 预热日报", body, "passive", func(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if request.Method != http.MethodPost || request.URL != "https://bark.example/push" || strings.Contains(request.URL, "device-secret") {
			t.Fatalf("unsafe Bark target: %q", request.URL)
		}
		if request.Headers.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatal("wrong content type")
		}
		var payload map[string]string
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["device_key"] != "device-secret" || payload["title"] != "Codex 预热日报" || payload["group"] != "codex-daily-prewarm" || payload["level"] != "passive" || payload["body"] != body {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200}`)}, nil
	})
	if got != "accepted" {
		t.Fatalf("status=%s", got)
	}
}

func TestBarkPushURLRejectsUnsafeInputWithoutLeakingKey(t *testing.T) {
	for _, raw := range []string{"http://bark.example/secret", "https://bark.example/secret?body=100%", "https://bark.example/a/b", "https://bark.example/%2fsecret", "https://secret@bark.example/key"} {
		_, _, err := barkPushTarget(raw)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe URL accepted or key leaked: %q", raw)
		}
	}
}
