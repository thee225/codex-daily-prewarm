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
	if cfg.AutomaticEnabled || cfg.Schedule != "0 5,10,15,20 * * *" || cfg.Timezone != "Asia/Shanghai" || cfg.Model != "gpt-6-luna" || cfg.FallbackModel != "gpt-5.6-luna" || cfg.Prompt != "hi" || cfg.ExpectedAccountCount != 0 {
		t.Fatalf("unexpected defaults: %#v", cfg.public())
	}
	if got := cfg.CronSchedule.Next(time.Date(2026, 9, 21, 5, 0, 0, 0, cfg.Location)); got.Hour() != 10 || got.Minute() != 0 {
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
		"cron":            "schedule: '* * *'",
		"timezone":        "timezone: Mars/Olympus",
		"model":           "model: 'gpt 5'",
		"state":           "state_path: relative.json",
		"count":           "expected_account_count: -1",
		"mixed schedules": "schedule: '0 6 * * *'\njobs:\n  - name: noon\n    schedule: '0 12 * * *'",
		"duplicate jobs":  "jobs:\n  - name: morning\n    schedule: '0 6 * * *'\n  - name: morning\n    schedule: '0 12 * * *'",
		"invalid job":     "jobs:\n  - name: bad/job\n    schedule: '0 6 * * *'",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePluginConfig([]byte(raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseMultipleJobs(t *testing.T) {
	raw := []byte("automatic_enabled: true\nsync_on_first_use: true\ntimezone: Asia/Shanghai\nmodel: gpt-5.6-luna\nprompt: hi\njobs:\n  - name: default\n    schedule: '0 6 * * *'\n  - name: evening\n    schedule: '0 18 * * *'\n    model: gpt-5.4\n    prompt: hello\n")
	cfg, err := parsePluginConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SyncOnFirstUse || len(cfg.Jobs) != 2 || cfg.Jobs[0].Model != "gpt-5.6-luna" || cfg.Jobs[1].Model != "gpt-5.4" || cfg.Jobs[1].Prompt != "hello" {
		t.Fatalf("jobs = %#v", cfg.Jobs)
	}
	if got := cfg.Jobs[1].CronSchedule.Next(time.Date(2026, 9, 23, 17, 0, 0, 0, cfg.Location)); got.Hour() != 18 {
		t.Fatalf("evening next = %s", got)
	}
}
