package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

const (
	defaultSchedule  = "0 6 * * *"
	defaultTimezone  = "Asia/Shanghai"
	defaultModel     = "gpt-5.6-luna"
	defaultPrompt    = "hi"
	defaultStatePath = "/CLIProxyAPI/plugins/state/codex-daily-prewarm.json"
)

type pluginConfig struct {
	AutomaticEnabled     bool           `json:"automatic_enabled"`
	Schedule             string         `json:"schedule"`
	Timezone             string         `json:"timezone"`
	Model                string         `json:"model"`
	Prompt               string         `json:"-"`
	ExpectedAccountCount int            `json:"expected_account_count"`
	AccountSpacing       time.Duration  `json:"-"`
	RetryCount           int            `json:"retry_count"`
	StatePath            string         `json:"state_path"`
	Location             *time.Location `json:"-"`
	CronSchedule         cron.Schedule  `json:"-"`
}

type yamlPluginConfig struct {
	AutomaticEnabled     *bool  `yaml:"automatic_enabled"`
	Schedule             string `yaml:"schedule"`
	Timezone             string `yaml:"timezone"`
	Model                string `yaml:"model"`
	Prompt               string `yaml:"prompt"`
	ExpectedAccountCount *int   `yaml:"expected_account_count"`
	AccountSpacing       string `yaml:"account_spacing"`
	RetryCount           *int   `yaml:"retry_count"`
	StatePath            string `yaml:"state_path"`
}

func defaultPluginConfig() pluginConfig {
	location, _ := time.LoadLocation(defaultTimezone)
	schedule, _ := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(defaultSchedule)
	return pluginConfig{
		AutomaticEnabled:     false,
		Schedule:             defaultSchedule,
		Timezone:             defaultTimezone,
		Model:                defaultModel,
		Prompt:               defaultPrompt,
		ExpectedAccountCount: 3,
		AccountSpacing:       30 * time.Second,
		RetryCount:           1,
		StatePath:            defaultStatePath,
		Location:             location,
		CronSchedule:         schedule,
	}
}

func parsePluginConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	var input yamlPluginConfig
	if err := yaml.Unmarshal(raw, &input); err != nil {
		return cfg, fmt.Errorf("decode plugin config: %w", err)
	}
	if input.AutomaticEnabled != nil {
		cfg.AutomaticEnabled = *input.AutomaticEnabled
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
	if input.Prompt != "" {
		cfg.Prompt = input.Prompt
	}
	if input.ExpectedAccountCount != nil {
		cfg.ExpectedAccountCount = *input.ExpectedAccountCount
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
	if cfg.AccountSpacing < 0 || cfg.AccountSpacing > time.Hour {
		return cfg, fmt.Errorf("account_spacing must be between 0 and 1h")
	}
	if cfg.RetryCount < 0 || cfg.RetryCount > 3 {
		return cfg, fmt.Errorf("retry_count must be between 0 and 3")
	}
	if err := validateModel(cfg.Model); err != nil {
		return cfg, err
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
	schedule, err := parser.Parse(cfg.Schedule)
	if err != nil {
		return cfg, fmt.Errorf("schedule must be a standard five-field cron expression: %w", err)
	}
	cfg.Location = location
	cfg.CronSchedule = schedule
	return cfg, nil
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
	AutomaticEnabled     bool   `json:"automatic_enabled"`
	Schedule             string `json:"schedule"`
	Timezone             string `json:"timezone"`
	Model                string `json:"model"`
	PromptSummary        string `json:"prompt_summary"`
	ExpectedAccountCount int    `json:"expected_account_count"`
	AccountSpacing       string `json:"account_spacing"`
	RetryCount           int    `json:"retry_count"`
	StatePath            string `json:"state_path"`
}

func (cfg pluginConfig) public() publicConfig {
	return publicConfig{
		AutomaticEnabled:     cfg.AutomaticEnabled,
		Schedule:             cfg.Schedule,
		Timezone:             cfg.Timezone,
		Model:                cfg.Model,
		PromptSummary:        fmt.Sprintf("configured (%d bytes)", len([]byte(cfg.Prompt))),
		ExpectedAccountCount: cfg.ExpectedAccountCount,
		AccountSpacing:       cfg.AccountSpacing.String(),
		RetryCount:           cfg.RetryCount,
		StatePath:            cfg.StatePath,
	}
}
