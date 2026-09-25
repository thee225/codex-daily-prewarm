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

func sendPrewarmSuccessBark(cfg pluginConfig, record runRecord) string {
	return sendPrewarmSuccessBarkWith(cfg, record, func(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
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

func sendPrewarmSuccessBarkWith(cfg pluginConfig, record runRecord, send func(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)) string {
	var successful []accountResult
	for _, item := range record.Accounts {
		if item.ResponseReceived {
			successful = append(successful, item)
		}
	}
	if len(successful) == 0 {
		return "no_successful_prewarm"
	}
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
	lines := []string{fmt.Sprintf("预热成功 %d 个账号", len(successful))}
	for _, item := range successful {
		line := item.Account
		if !item.QuotaCheckedAt.IsZero() {
			line += fmt.Sprintf(" 5h %.0f%% 周 %.0f%%", 100-item.FiveHour.UsedPercent, 100-item.Weekly.UsedPercent)
		}
		lines = append(lines, line)
	}
	body, err := json.Marshal(map[string]string{
		"device_key": deviceKey,
		"title":      "Codex 预热成功",
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
