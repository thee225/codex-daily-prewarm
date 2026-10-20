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

type barkSender func(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)

func sendBarkHTTP(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	raw, err := callHost(pluginabi.MethodHostHTTPDo, request)
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	var response pluginapi.HTTPResponse
	if json.Unmarshal(raw, &response) != nil {
		return pluginapi.HTTPResponse{}, errors.New("invalid host HTTP response")
	}
	return response, nil
}

func sendBarkWith(cfg pluginConfig, title, bodyText, level string, send barkSender) string {
	if cfg.BarkURL == "" {
		return "not_configured"
	}
	pushURL, deviceKey, err := barkPushTarget(cfg.BarkURL)
	if err != nil {
		return "invalid_bark_url"
	}
	body, err := json.Marshal(map[string]string{
		"device_key": deviceKey,
		"title":      title,
		"body":       bodyText,
		"level":      level,
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
