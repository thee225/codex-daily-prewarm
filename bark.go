package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func sendScheduleBark(cfg pluginConfig, record runRecord) string {
	if cfg.BarkURL == "" {
		return "not_configured"
	}
	hour := record.FinishedAt.In(cfg.Location).Hour()
	if hour >= 23 || hour < 8 {
		return "quiet_hours"
	}
	lines := []string{fmt.Sprintf("查询 %d/%d；预热 %d；待预热 %d；跳过 %d", record.QuotaQueried, record.Discovered, record.Attempted, record.WouldWarm, record.Skipped)}
	for _, item := range record.Accounts {
		line := item.Account
		if !item.QuotaCheckedAt.IsZero() {
			line += fmt.Sprintf(" 5h %.0f%% 周 %.0f%%", 100-item.FiveHour.UsedPercent, 100-item.Weekly.UsedPercent)
		}
		if item.SkipReason != "" {
			line += " " + item.SkipReason
		} else if item.ResponseReceived {
			line += " warm_ok"
		} else if item.ErrorCode != "" {
			line += " " + item.ErrorCode
		}
		lines = append(lines, line)
	}
	body, _ := json.Marshal(map[string]string{
		"title": "Codex 额度巡检",
		"body":  strings.Join(lines, "\n"),
		"group": "codex-daily-prewarm",
	})
	raw, err := callHost(pluginabi.MethodHostHTTPDo, pluginapi.HTTPRequest{
		Method: http.MethodPost, URL: cfg.BarkURL,
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: body,
	})
	if err != nil {
		return "transport_failed"
	}
	var response pluginapi.HTTPResponse
	if json.Unmarshal(raw, &response) != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return "http_failed"
	}
	var acknowledgement struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(response.Body, &acknowledgement) != nil || acknowledgement.Code != 200 {
		return "not_accepted"
	}
	return "accepted"
}
