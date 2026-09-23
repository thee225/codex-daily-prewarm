package main

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	fiveHourMinutes = 300
	weeklyMinutes   = 10080
	windowDrift     = 150 * time.Minute
)

// quotaWindow is derived only from bounded upstream quota headers. The window
// length, not the primary/secondary label, identifies the five-hour and week limits.
type quotaWindow struct {
	WindowMinutes int       `json:"window_minutes,omitempty"`
	UsedPercent   float64   `json:"used_percent,omitempty"`
	ResetAt       time.Time `json:"reset_at,omitempty"`
	ObservedAt    time.Time `json:"observed_at,omitempty"`
}

func parseQuotaWindows(headers http.Header, now time.Time) (fiveHour, weekly quotaWindow) {
	for _, name := range []string{"primary", "secondary"} {
		window, ok := parseQuotaWindow(headers, name, now)
		if !ok {
			continue
		}
		switch window.WindowMinutes {
		case fiveHourMinutes:
			fiveHour = window
		case weeklyMinutes:
			weekly = window
		}
	}
	return fiveHour, weekly
}

func parseQuotaWindow(headers http.Header, name string, now time.Time) (quotaWindow, bool) {
	prefix := "x-codex-" + name + "-"
	minutes, err := strconv.Atoi(boundedHeader(headers, prefix+"window-minutes"))
	if err != nil || (minutes != fiveHourMinutes && minutes != weeklyMinutes) {
		return quotaWindow{}, false
	}
	used, err := strconv.ParseFloat(boundedHeader(headers, prefix+"used-percent"), 64)
	if err != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
		return quotaWindow{}, false
	}
	resetAt := quotaResetAt(headers, prefix, now)
	if resetAt.IsZero() {
		return quotaWindow{}, false
	}
	return quotaWindow{WindowMinutes: minutes, UsedPercent: used, ResetAt: resetAt, ObservedAt: now.UTC()}, true
}

func quotaResetAt(headers http.Header, prefix string, now time.Time) time.Time {
	if raw := boundedHeader(headers, prefix+"reset-at"); raw != "" {
		if unix, err := strconv.ParseInt(raw, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0).UTC()
		}
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			return parsed.UTC()
		}
	}
	if raw := boundedHeader(headers, prefix+"reset-after-seconds"); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 && seconds <= 8*24*60*60 {
			return now.Add(time.Duration(seconds) * time.Second).UTC()
		}
	}
	return time.Time{}
}

func sameFiveHourWindow(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	delta := a.Sub(b)
	return math.Abs(delta.Seconds()) < windowDrift.Seconds()
}

func newlyOpenedFiveHour(window quotaWindow, now time.Time) bool {
	if window.WindowMinutes != fiveHourMinutes {
		return false
	}
	remaining := window.ResetAt.Sub(now)
	return remaining >= 4*time.Hour+30*time.Minute && remaining <= 5*time.Hour+10*time.Minute
}

func weeklyRemaining(window quotaWindow, now time.Time) (remaining bool, known bool) {
	if window.WindowMinutes != weeklyMinutes || window.ResetAt.IsZero() {
		return false, false
	}
	if !window.ResetAt.After(now) {
		return true, true
	}
	return window.UsedPercent < 100, true
}

func normalizedUnknownQuotaPolicy(value string) string {
	if strings.TrimSpace(value) == "probe_once" {
		return "probe_once"
	}
	return "skip"
}
