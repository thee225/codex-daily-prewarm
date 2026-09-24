package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// quotaObservation deliberately contains only quota data. Credential JSON and
// the upstream response body must never be persisted or sent to the UI.
type quotaObservation struct {
	CheckedAt time.Time   `json:"checked_at"`
	FiveHour  quotaWindow `json:"five_hour"`
	Weekly    quotaWindow `json:"weekly"`
	Allowed   bool        `json:"allowed"`
	Status    string      `json:"status"`
}

type usageWindow struct {
	UsedPercent       *float64 `json:"used_percent"`
	ResetAfterSeconds *int64   `json:"reset_after_seconds"`
	ResetAt           *int64   `json:"reset_at"`
}

type usageRateLimit struct {
	Allowed         *bool        `json:"allowed"`
	PrimaryWindow   *usageWindow `json:"primary_window"`
	SecondaryWindow *usageWindow `json:"secondary_window"`
}

func parseUsageBody(body []byte, now time.Time) (quotaObservation, error) {
	if len(body) == 0 || len(body) > 1<<20 {
		return quotaObservation{}, errors.New("invalid_usage_size")
	}
	var payload struct {
		RateLimit *usageRateLimit `json:"rate_limit"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.RateLimit == nil || payload.RateLimit.Allowed == nil {
		return quotaObservation{}, errors.New("invalid_usage_payload")
	}
	five, okFive := usageQuotaWindow(payload.RateLimit.PrimaryWindow, fiveHourMinutes, now)
	week, okWeek := usageQuotaWindow(payload.RateLimit.SecondaryWindow, weeklyMinutes, now)
	if !okFive || !okWeek {
		return quotaObservation{}, errors.New("missing_quota_windows")
	}
	return quotaObservation{CheckedAt: now.UTC(), FiveHour: five, Weekly: week, Allowed: *payload.RateLimit.Allowed, Status: "confirmed"}, nil
}

func usageQuotaWindow(input *usageWindow, minutes int, now time.Time) (quotaWindow, bool) {
	if input == nil || input.UsedPercent == nil || input.ResetAfterSeconds == nil || input.ResetAt == nil {
		return quotaWindow{}, false
	}
	used, seconds, unix := *input.UsedPercent, *input.ResetAfterSeconds, *input.ResetAt
	limit := int64(minutes * 60)
	if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 || seconds < 0 || seconds > limit+60 || unix < now.Unix()-60 || unix > now.Unix()+limit+60 {
		return quotaWindow{}, false
	}
	if math.Abs(float64(unix-now.Unix()-seconds)) > 60 {
		return quotaWindow{}, false
	}
	return quotaWindow{WindowMinutes: minutes, UsedPercent: used, ResetAt: time.Unix(unix, 0).UTC(), ObservedAt: now.UTC()}, true
}

// An idle account's virtual five-hour reset moves forward with wall time.
// A real active window has a fixed reset timestamp. Two observations protect
// against an active window that still rounds to zero percent used.
func confirmedIdle(first, second quotaObservation) bool {
	if first.FiveHour.UsedPercent != 0 || second.FiveHour.UsedPercent != 0 || !first.Allowed || !second.Allowed {
		return false
	}
	if first.Weekly.UsedPercent >= 95 || second.Weekly.UsedPercent >= 95 {
		return false
	}
	delta := second.CheckedAt.Sub(first.CheckedAt)
	if delta < 2*time.Second || delta > 30*time.Second {
		return false
	}
	for _, item := range []quotaObservation{first, second} {
		remaining := item.FiveHour.ResetAt.Sub(item.CheckedAt)
		if remaining < 4*time.Hour+59*time.Minute || remaining > 5*time.Hour+time.Minute {
			return false
		}
	}
	return second.FiveHour.ResetAt.Sub(first.FiveHour.ResetAt) >= delta-2*time.Second
}

func fetchUsage(auth pluginapi.HostAuthFileEntry) (quotaObservation, string) {
	if strings.TrimSpace(auth.AuthIndex) == "" {
		return quotaObservation{}, "auth_index_missing"
	}
	raw, err := callHost(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: auth.AuthIndex})
	if err != nil {
		return quotaObservation{}, "credential_unavailable"
	}
	var credential pluginapi.HostAuthGetResponse
	if json.Unmarshal(raw, &credential) != nil {
		return quotaObservation{}, "credential_invalid"
	}
	var secret struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	}
	if json.Unmarshal(credential.JSON, &secret) != nil || secret.AccessToken == "" || secret.AccountID == "" {
		return quotaObservation{}, "credential_incomplete"
	}
	request := pluginapi.HTTPRequest{Method: http.MethodGet, URL: codexUsageURL, Headers: http.Header{
		"Authorization":      []string{"Bearer " + secret.AccessToken},
		"Chatgpt-Account-Id": []string{secret.AccountID},
		"Accept":             []string{"application/json"},
	}}
	responseRaw, err := callHost(pluginabi.MethodHostHTTPDo, request)
	if err != nil {
		return quotaObservation{}, "usage_transport_failed"
	}
	var response pluginapi.HTTPResponse
	if json.Unmarshal(responseRaw, &response) != nil {
		return quotaObservation{}, "usage_response_invalid"
	}
	if response.StatusCode != http.StatusOK {
		return quotaObservation{}, fmt.Sprintf("usage_http_%d", response.StatusCode)
	}
	result, err := parseUsageBody(response.Body, time.Now())
	if err != nil {
		return quotaObservation{}, err.Error()
	}
	return result, ""
}
