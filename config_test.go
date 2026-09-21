package main

import (
	"testing"
	"time"
)

func TestParsePluginConfigDefaults(t *testing.T) {
	cfg, err := parsePluginConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutomaticEnabled || cfg.Schedule != "0 6 * * *" || cfg.Timezone != "Asia/Shanghai" || cfg.Model != "gpt-5.6-luna" || cfg.Prompt != "hi" {
		t.Fatalf("unexpected defaults: %#v", cfg.public())
	}
	if got := cfg.CronSchedule.Next(time.Date(2026, 9, 21, 5, 0, 0, 0, cfg.Location)); got.Hour() != 6 || got.Minute() != 0 {
		t.Fatalf("next run = %v", got)
	}
}

func TestParsePluginConfigCustom(t *testing.T) {
	raw := []byte("enabled: true\nautomatic_enabled: true\nschedule: '10 4 * * *'\ntimezone: Asia/Shanghai\nmodel: gpt-5.4\nprompt: hello\nexpected_account_count: 4\naccount_spacing: 5s\nretry_count: 0\nstate_path: /tmp/prewarm.json\n")
	cfg, err := parsePluginConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutomaticEnabled || cfg.Model != "gpt-5.4" || cfg.Prompt != "hello" || cfg.ExpectedAccountCount != 4 || cfg.AccountSpacing != 5*time.Second || cfg.RetryCount != 0 {
		t.Fatalf("unexpected config: %#v", cfg.public())
	}
	if got := cfg.CronSchedule.Next(time.Date(2026, 9, 21, 4, 9, 59, 0, cfg.Location)); got.Hour() != 4 || got.Minute() != 10 {
		t.Fatalf("next run = %v", got)
	}
}

func TestHostEnabledDoesNotEnableAutomaticSchedule(t *testing.T) {
	cfg, err := parsePluginConfig([]byte("enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutomaticEnabled {
		t.Fatal("CPA host field enabled must not start the automatic prewarm schedule")
	}
}

func TestParsePluginConfigRejectsUnsafeValues(t *testing.T) {
	for name, raw := range map[string]string{
		"cron":     "schedule: '* * *'",
		"timezone": "timezone: Mars/Olympus",
		"model":    "model: 'gpt 5'",
		"state":    "state_path: relative.json",
		"count":    "expected_account_count: -1",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePluginConfig([]byte(raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
