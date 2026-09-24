package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestBarkPushKeepsNotificationTextInJSON(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.BarkURL = "https://bark.example/device-secret"
	finished := time.Date(2026, 9, 24, 10, 0, 0, 0, cfg.Location)
	record := runRecord{FinishedAt: finished, Accounts: []accountResult{{
		Account: "acct-012345abcdef", QuotaCheckedAt: finished,
		FiveHour: quotaWindow{UsedPercent: 0}, Weekly: quotaWindow{UsedPercent: 86},
		SkipReason: "中文\nemoji 😀 % %25",
	}}}
	called := false
	got := sendScheduleBarkWith(cfg, record, func(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		called = true
		if request.Method != http.MethodPost || request.URL != "https://bark.example/push" || strings.Contains(request.URL, "device-secret") {
			t.Fatalf("unsafe Bark target: %q", request.URL)
		}
		if request.Headers.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("content type = %q", request.Headers.Get("Content-Type"))
		}
		var payload map[string]string
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["device_key"] != "device-secret" || payload["title"] != "Codex 额度巡检" || payload["group"] != "codex-daily-prewarm" {
			t.Fatalf("unexpected Bark payload fields: %#v", payload)
		}
		for _, want := range []string{"5h 100% 周 14%", "中文", "\n", "😀", " % %25"} {
			if !strings.Contains(payload["body"], want) {
				t.Fatalf("body lost %q: %q", want, payload["body"])
			}
		}
		if strings.Contains(payload["body"], "%2525") || strings.Contains(payload["body"], "百分之") {
			t.Fatalf("body was URL encoded: %q", payload["body"])
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200}`)}, nil
	})
	if !called || got != "accepted" {
		t.Fatalf("called=%v status=%s", called, got)
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

func TestBarkQuietHoursDoNotSend(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.BarkURL = "https://bark.example/secret"
	for _, hour := range []int{23, 0, 1, 7} {
		record := runRecord{FinishedAt: time.Date(2026, 9, 24, hour, 0, 0, 0, cfg.Location)}
		if got := sendScheduleBarkWith(cfg, record, func(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
			t.Fatal("sent during quiet hours")
			return pluginapi.HTTPResponse{}, nil
		}); got != "quiet_hours" {
			t.Fatalf("hour %d: %s", hour, got)
		}
	}
}
