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
	if cfg.AutomaticEnabled || cfg.Schedule != "0 5-23 * * *" || cfg.ResetFollowupMode != "observe" || cfg.Timezone != "Asia/Shanghai" || cfg.Model != "gpt-6-luna" || cfg.FallbackModel != "gpt-5.6-luna" || cfg.Prompt != "hi" || cfg.ExpectedAccountCount != 0 || cfg.UnknownQuotaPolicy != "skip" {
		t.Fatalf("unexpected defaults: %#v", cfg.public())
	}
	if got := cfg.CronSchedule.Next(time.Date(2026, 9, 21, 5, 0, 0, 0, cfg.Location)); got.Hour() != 6 || got.Minute() != 0 {
		t.Fatalf("next run = %v", got)
	}
}

func TestParsePluginConfigCustom(t *testing.T) {
	raw := []byte("enabled: true\nautomatic_enabled: true\nschedule: '10 5 * * *'\ntimezone: Asia/Shanghai\nmodel: gpt-5.4\nprompt: hello\nexpected_account_count: 4\naccount_spacing: 5s\nretry_count: 0\nstate_path: /tmp/prewarm.json\n")
	cfg, err := parsePluginConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutomaticEnabled || cfg.Model != "gpt-5.4" || cfg.Prompt != "hello" || cfg.ExpectedAccountCount != 4 || cfg.AccountSpacing != 5*time.Second || cfg.RetryCount != 0 {
		t.Fatalf("unexpected config: %#v", cfg.public())
	}
	if got := cfg.CronSchedule.Next(time.Date(2026, 9, 21, 5, 9, 59, 0, cfg.Location)); got.Hour() != 5 || got.Minute() != 10 {
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
		"unknown policy":  "unknown_quota_policy: always",
		"followup mode":   "reset_followup_mode: always",
		"mixed schedules": "schedule: '0 6 * * *'\njobs:\n  - name: noon\n    schedule: '0 12 * * *'",
		"duplicate jobs":  "jobs:\n  - name: morning\n    schedule: '0 6 * * *'\n  - name: morning\n    schedule: '0 12 * * *'",
		"invalid job":     "jobs:\n  - name: bad/job\n    schedule: '0 6 * * *'",
		"night schedule":  "schedule: '10 4 * * *'",
		"01 schedule":     "schedule: '0 1 * * *'",
		"mixed night":     "schedule: '0 1,8 * * *'",
		"night job":       "jobs:\n  - name: night\n    schedule: '0 1 * * *'",
		"midnight jitter": "schedule: '59 23 * * *'",
		"allowlist typo":  "warm_allow_list:\n  - acct-012345abcdef",
		"job typo":        "jobs:\n  - name: day\n    schedule: '0 8 * * *'\n    modle: gpt-5.4",
		"extra document":  "enabled: true\n---\nenabled: false",
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
	if len(cfg.Jobs) != 2 || cfg.Jobs[0].Model != "gpt-5.6-luna" || cfg.Jobs[1].Model != "gpt-5.4" || cfg.Jobs[1].Prompt != "hello" {
		t.Fatalf("jobs = %#v", cfg.Jobs)
	}
	if got := cfg.Jobs[1].CronSchedule.Next(time.Date(2026, 9, 23, 17, 0, 0, 0, cfg.Location)); got.Hour() != 18 {
		t.Fatalf("evening next = %s", got)
	}
}

func TestWarmAllowlistValidation(t *testing.T) {
	cfg, err := parsePluginConfig([]byte("warm_allowlist:\n  - acct-012345abcdef\n"))
	if err != nil || len(cfg.WarmAllowlist) != 1 {
		t.Fatalf("valid allowlist rejected: %v", err)
	}
	for _, raw := range []string{
		"warm_allowlist: [acct-012345abcdef, acct-012345abcdef]",
		"warm_allowlist: [acct-invalid]",
	} {
		if _, err := parsePluginConfig([]byte(raw)); err == nil {
			t.Fatalf("invalid allowlist accepted: %s", raw)
		}
	}
}
