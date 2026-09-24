package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var errInvalidBarkURL = errors.New("bark_url must be a keyed HTTPS URL without query parameters")

// barkPushTarget moves only the configured device key into the JSON request.
// Notification text never participates in URL parsing or escaping.
func barkPushTarget(raw string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" {
		return "", "", errInvalidBarkURL
	}
	escapedKey := strings.Trim(u.EscapedPath(), "/")
	if escapedKey == "" || strings.Contains(escapedKey, "/") {
		return "", "", errInvalidBarkURL
	}
	key, err := url.PathUnescape(escapedKey)
	if err != nil || key == "" || strings.ContainsAny(key, "/?#") {
		return "", "", errInvalidBarkURL
	}
	u.Path = "/push"
	u.RawPath = ""
	return u.String(), key, nil
}

func sendScheduleBark(cfg pluginConfig, record runRecord) string {
	return sendScheduleBarkWith(cfg, record, func(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		raw, err := callHost(pluginabi.MethodHostHTTPDo, request)
		if err != nil {
			return pluginapi.HTTPResponse{}, err
		}
		var response pluginapi.HTTPResponse
		if json.Unmarshal(raw, &response) != nil {
			return pluginapi.HTTPResponse{}, errors.New("invalid host HTTP response")
		}
		return response, nil
	})
}

func sendScheduleBarkWith(cfg pluginConfig, record runRecord, send func(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)) string {
	if cfg.BarkURL == "" {
		return "not_configured"
	}
	hour := record.FinishedAt.In(cfg.Location).Hour()
	if hour >= 23 || hour < 8 {
		return "quiet_hours"
	}
	pushURL, deviceKey, err := barkPushTarget(cfg.BarkURL)
	if err != nil {
		return "invalid_bark_url"
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
	body, err := json.Marshal(map[string]string{
		"device_key": deviceKey,
		"title":      "Codex 额度巡检",
		"body":       strings.Join(lines, "\n"),
		"group":      "codex-daily-prewarm",
	})
	if err != nil {
		return "request_encoding_failed"
	}
	response, err := send(pluginapi.HTTPRequest{
		Method: http.MethodPost, URL: pushURL,
		Headers: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		}, Body: body,
	})
	if err != nil {
		return "transport_failed"
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Sprintf("http_%d", response.StatusCode)
	}
	var acknowledgement struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(response.Body, &acknowledgement) != nil || acknowledgement.Code != 200 {
		return "not_accepted"
	}
	return "accepted"
}
