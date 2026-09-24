package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

const (
	defaultSchedule      = "0 5-23 * * *"
	defaultTimezone      = "Asia/Shanghai"
	defaultModel         = "gpt-6-luna"
	defaultFallbackModel = "gpt-5.6-luna"
	defaultPrompt        = "hi"
	defaultStatePath     = "/CLIProxyAPI/plugins/state/codex-daily-prewarm.json"
)

type pluginConfig struct {
	AutomaticEnabled     bool           `json:"automatic_enabled"`
	DryRun               bool           `json:"dry_run"`
	BarkURL              string         `json:"-"`
	WarmAllowlist        []string       `json:"warm_allowlist,omitempty"`
	ResetFollowupMode    string         `json:"reset_followup_mode"`
	Schedule             string         `json:"schedule"`
	Timezone             string         `json:"timezone"`
	Model                string         `json:"model"`
	FallbackModel        string         `json:"fallback_model"`
	Prompt               string         `json:"-"`
	ExpectedAccountCount int            `json:"expected_account_count"`
	UnknownQuotaPolicy   string         `json:"unknown_quota_policy"`
	AccountSpacing       time.Duration  `json:"-"`
	RetryCount           int            `json:"retry_count"`
	StatePath            string         `json:"state_path"`
	Location             *time.Location `json:"-"`
	CronSchedule         cron.Schedule  `json:"-"`
	Jobs                 []prewarmJob   `json:"-"`
}

type prewarmJob struct {
	Name         string
	Schedule     string
	Model        string
	Prompt       string
	CronSchedule cron.Schedule
}

type yamlPrewarmJob struct {
	Name     string `yaml:"name"`
	Schedule string `yaml:"schedule"`
	Model    string `yaml:"model"`
	Prompt   string `yaml:"prompt"`
}

type yamlPluginConfig struct {
	Enabled              *bool            `yaml:"enabled"`  // CPA controls loading; the plugin does not use it.
	Priority             *int             `yaml:"priority"` // CPA host ordering; not a prewarm policy.
	AutomaticEnabled     *bool            `yaml:"automatic_enabled"`
	DryRun               *bool            `yaml:"dry_run"`
	BarkURL              string           `yaml:"bark_url"`
	WarmAllowlist        []string         `yaml:"warm_allowlist"`
	SyncOnFirstUse       *bool            `yaml:"sync_on_first_use"` // Accepted for old CPA configs; intentionally ignored.
	ResetFollowupMode    string           `yaml:"reset_followup_mode"`
	Schedule             string           `yaml:"schedule"`
	Timezone             string           `yaml:"timezone"`
	Model                string           `yaml:"model"`
	FallbackModel        string           `yaml:"fallback_model"`
	Prompt               string           `yaml:"prompt"`
	ExpectedAccountCount *int             `yaml:"expected_account_count"`
	UnknownQuotaPolicy   string           `yaml:"unknown_quota_policy"`
	AccountSpacing       string           `yaml:"account_spacing"`
	RetryCount           *int             `yaml:"retry_count"`
	StatePath            string           `yaml:"state_path"`
	Jobs                 []yamlPrewarmJob `yaml:"jobs"`
}

func defaultPluginConfig() pluginConfig {
	location, _ := time.LoadLocation(defaultTimezone)
	schedule, _ := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(defaultSchedule)
	return pluginConfig{
		AutomaticEnabled:     false,
		DryRun:               true,
		ResetFollowupMode:    "observe",
		Schedule:             defaultSchedule,
		Timezone:             defaultTimezone,
		Model:                defaultModel,
		FallbackModel:        defaultFallbackModel,
		Prompt:               defaultPrompt,
		ExpectedAccountCount: 0,
		UnknownQuotaPolicy:   "skip",
		AccountSpacing:       30 * time.Second,
		RetryCount:           1,
		StatePath:            defaultStatePath,
		Location:             location,
		CronSchedule:         schedule,
		Jobs:                 []prewarmJob{{Name: "default", Schedule: defaultSchedule, Model: defaultModel, Prompt: defaultPrompt, CronSchedule: schedule}},
	}
}

func parsePluginConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	var input yamlPluginConfig
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&input); err != nil {
		return cfg, fmt.Errorf("decode plugin config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cfg, fmt.Errorf("decode plugin config: expected exactly one YAML document")
	}
	if input.AutomaticEnabled != nil {
		cfg.AutomaticEnabled = *input.AutomaticEnabled
	}
	if input.DryRun != nil {
		cfg.DryRun = *input.DryRun
	}
	if input.BarkURL != "" {
		if _, _, err := barkPushTarget(input.BarkURL); err != nil {
			return cfg, err
		}
		cfg.BarkURL = strings.TrimSpace(input.BarkURL)
	}
	if len(input.WarmAllowlist) > 0 {
		if len(input.WarmAllowlist) > 100 {
			return cfg, fmt.Errorf("warm_allowlist must contain at most 100 accounts")
		}
		seen := make(map[string]bool, len(input.WarmAllowlist))
		for _, fingerprint := range input.WarmAllowlist {
			if len(fingerprint) != 17 || !strings.HasPrefix(fingerprint, "acct-") || seen[fingerprint] {
				return cfg, fmt.Errorf("warm_allowlist contains an invalid or duplicate fingerprint")
			}
			for _, ch := range fingerprint[5:] {
				if !strings.ContainsRune("0123456789abcdef", ch) {
					return cfg, fmt.Errorf("warm_allowlist contains an invalid fingerprint")
				}
			}
			seen[fingerprint] = true
		}
		cfg.WarmAllowlist = append([]string(nil), input.WarmAllowlist...)
	}
	if value := strings.TrimSpace(input.ResetFollowupMode); value != "" {
		cfg.ResetFollowupMode = value
	}
	if value := strings.TrimSpace(input.Schedule); value != "" {
		cfg.Schedule = value
	}
	if value := strings.TrimSpace(input.Timezone); value != "" {
		cfg.Timezone = value
	}
	if value := strings.TrimSpace(input.Model); value != "" {
		cfg.Model = value
	}
	if value := strings.TrimSpace(input.FallbackModel); value != "" {
		cfg.FallbackModel = value
	}
	if input.Prompt != "" {
		cfg.Prompt = input.Prompt
	}
	if input.ExpectedAccountCount != nil {
		cfg.ExpectedAccountCount = *input.ExpectedAccountCount
	}
	if value := strings.TrimSpace(input.UnknownQuotaPolicy); value != "" {
		cfg.UnknownQuotaPolicy = value
	}
	if value := strings.TrimSpace(input.AccountSpacing); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return cfg, fmt.Errorf("account_spacing: %w", err)
		}
		cfg.AccountSpacing = duration
	}
	if input.RetryCount != nil {
		cfg.RetryCount = *input.RetryCount
	}
	if value := strings.TrimSpace(input.StatePath); value != "" {
		cfg.StatePath = filepath.Clean(value)
	}

	if cfg.ExpectedAccountCount < 0 || cfg.ExpectedAccountCount > 100 {
		return cfg, fmt.Errorf("expected_account_count must be between 0 and 100")
	}
	if cfg.UnknownQuotaPolicy != "skip" {
		return cfg, fmt.Errorf("unknown_quota_policy must be skip")
	}
	if cfg.ResetFollowupMode != "off" && cfg.ResetFollowupMode != "observe" && cfg.ResetFollowupMode != "active" {
		return cfg, fmt.Errorf("reset_followup_mode must be off, observe or active")
	}
	if cfg.AccountSpacing < 0 || cfg.AccountSpacing > time.Hour {
		return cfg, fmt.Errorf("account_spacing must be between 0 and 1h")
	}
	if cfg.RetryCount < 0 || cfg.RetryCount > 3 {
		return cfg, fmt.Errorf("retry_count must be between 0 and 3")
	}
	if err := validateModel(cfg.Model); err != nil {
		return cfg, err
	}
	if err := validateModel(cfg.FallbackModel); err != nil {
		return cfg, fmt.Errorf("fallback_model: %w", err)
	}
	if !utf8.ValidString(cfg.Prompt) || strings.TrimSpace(cfg.Prompt) == "" || len([]byte(cfg.Prompt)) > 1024 {
		return cfg, fmt.Errorf("prompt must be valid UTF-8 between 1 and 1024 bytes")
	}
	if !filepath.IsAbs(cfg.StatePath) {
		return cfg, fmt.Errorf("state_path must be absolute")
	}
	location, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return cfg, fmt.Errorf("timezone: %w", err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if len(input.Jobs) > 0 && strings.TrimSpace(input.Schedule) != "" {
		return cfg, fmt.Errorf("use either schedule or jobs, not both")
	}
	if len(input.Jobs) > 16 {
		return cfg, fmt.Errorf("jobs must contain at most 16 entries")
	}
	if len(input.Jobs) == 0 {
		schedule, err := parser.Parse(cfg.Schedule)
		if err != nil {
			return cfg, fmt.Errorf("schedule must be a standard five-field cron expression: %w", err)
		}
		if err := validateDaytimeSchedule(schedule); err != nil {
			return cfg, err
		}
		cfg.CronSchedule = schedule
		cfg.Jobs = []prewarmJob{{Name: "default", Schedule: cfg.Schedule, Model: cfg.Model, Prompt: cfg.Prompt, CronSchedule: schedule}}
	} else {
		cfg.Jobs = make([]prewarmJob, 0, len(input.Jobs))
		seen := make(map[string]bool)
		for _, item := range input.Jobs {
			name := strings.TrimSpace(item.Name)
			if !validJobName(name) || seen[name] {
				return cfg, fmt.Errorf("job names must be unique and use 1-64 letters, digits, underscores or hyphens")
			}
			seen[name] = true
			scheduleText := strings.TrimSpace(item.Schedule)
			schedule, err := parser.Parse(scheduleText)
			if err != nil {
				return cfg, fmt.Errorf("job %s schedule must be a standard five-field cron expression: %w", name, err)
			}
			if err := validateDaytimeSchedule(schedule); err != nil {
				return cfg, fmt.Errorf("job %s: %w", name, err)
			}
			model := cfg.Model
			if strings.TrimSpace(item.Model) != "" {
				model = strings.TrimSpace(item.Model)
			}
			if err := validateModel(model); err != nil {
				return cfg, fmt.Errorf("job %s: %w", name, err)
			}
			prompt := cfg.Prompt
			if item.Prompt != "" {
				prompt = item.Prompt
			}
			if !utf8.ValidString(prompt) || strings.TrimSpace(prompt) == "" || len([]byte(prompt)) > 1024 {
				return cfg, fmt.Errorf("job %s prompt must be valid UTF-8 between 1 and 1024 bytes", name)
			}
			cfg.Jobs = append(cfg.Jobs, prewarmJob{Name: name, Schedule: scheduleText, Model: model, Prompt: prompt, CronSchedule: schedule})
		}
		cfg.Schedule = ""
		cfg.CronSchedule = nil
	}
	cfg.Location = location
	return cfg, nil
}

func validateDaytimeSchedule(schedule cron.Schedule) error {
	spec, ok := schedule.(*cron.SpecSchedule)
	if !ok {
		return fmt.Errorf("schedule must use standard five-field cron syntax")
	}
	for hour := 0; hour < 24; hour++ {
		if spec.Hour&(uint64(1)<<hour) != 0 && (hour < 5 || hour > 23) {
			return fmt.Errorf("schedule hours must be within 05:00–23:59; nighttime automatic checks are disabled")
		}
	}
	if spec.Hour&(uint64(1)<<23) != 0 && spec.Minute&(uint64(1)<<59) != 0 {
		return fmt.Errorf("schedule at 23:59 would cross midnight after jitter")
	}
	return nil
}

func validJobName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func validateModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 256 {
		return fmt.Errorf("model must be between 1 and 256 characters")
	}
	for _, r := range model {
		if !(r == '-' || r == '_' || r == '.' || r == ':' || r == '/' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return fmt.Errorf("model contains unsupported characters")
		}
	}
	return nil
}

type publicConfig struct {
	AutomaticEnabled     bool        `json:"automatic_enabled"`
	DryRun               bool        `json:"dry_run"`
	BarkConfigured       bool        `json:"bark_configured"`
	WarmAllowlist        []string    `json:"warm_allowlist,omitempty"`
	ResetFollowupMode    string      `json:"reset_followup_mode"`
	Schedule             string      `json:"schedule"`
	Timezone             string      `json:"timezone"`
	Model                string      `json:"model"`
	FallbackModel        string      `json:"fallback_model"`
	PromptSummary        string      `json:"prompt_summary"`
	ExpectedAccountCount int         `json:"expected_account_count"`
	UnknownQuotaPolicy   string      `json:"unknown_quota_policy"`
	AccountSpacing       string      `json:"account_spacing"`
	RetryCount           int         `json:"retry_count"`
	StatePath            string      `json:"state_path"`
	Jobs                 []publicJob `json:"jobs"`
}

type publicJob struct {
	Name          string `json:"name"`
	Schedule      string `json:"schedule"`
	Model         string `json:"model"`
	PromptSummary string `json:"prompt_summary"`
}

func (cfg pluginConfig) public() publicConfig {
	jobs := make([]publicJob, 0, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		jobs = append(jobs, publicJob{Name: job.Name, Schedule: job.Schedule, Model: job.Model, PromptSummary: fmt.Sprintf("configured (%d bytes)", len([]byte(job.Prompt)))})
	}
	return publicConfig{
		AutomaticEnabled:     cfg.AutomaticEnabled,
		DryRun:               cfg.DryRun,
		BarkConfigured:       cfg.BarkURL != "",
		WarmAllowlist:        append([]string(nil), cfg.WarmAllowlist...),
		ResetFollowupMode:    cfg.ResetFollowupMode,
		Schedule:             cfg.Schedule,
		Timezone:             cfg.Timezone,
		Model:                cfg.Model,
		FallbackModel:        cfg.FallbackModel,
		PromptSummary:        fmt.Sprintf("configured (%d bytes)", len([]byte(cfg.Prompt))),
		ExpectedAccountCount: cfg.ExpectedAccountCount,
		UnknownQuotaPolicy:   cfg.UnknownQuotaPolicy,
		AccountSpacing:       cfg.AccountSpacing.String(),
		RetryCount:           cfg.RetryCount,
		StatePath:            cfg.StatePath,
		Jobs:                 jobs,
	}
}
