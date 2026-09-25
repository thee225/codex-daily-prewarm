package main

import "time"

const maxInitialQuotaAge = 10 * time.Second

func quotaCandidateReason(observation quotaObservation) string {
	switch {
	case observation.Weekly.UsedPercent >= 95:
		return "weekly_below_threshold"
	case !observation.Allowed:
		return "upstream_disallowed"
	case observation.FiveHour.UsedPercent != 0:
		return "active_five_hour_window"
	default:
		return ""
	}
}

// The spacing belongs before the final evidence pair. Refresh an old initial
// observation, then confirm the moving idle reset immediately before a call.
func confirmIdleAfterSpacing(
	first quotaObservation,
	spacing time.Duration,
	stop <-chan struct{},
	wait func(<-chan struct{}, time.Duration) bool,
	fetch func() (quotaObservation, string),
	save func(quotaObservation) bool,
) (quotaObservation, string) {
	if !wait(stop, spacing) {
		return first, "interrupted"
	}
	if time.Since(first.CheckedAt) > maxInitialQuotaAge {
		refreshed, reason := fetch()
		if reason != "" {
			return first, "idle_confirmation_" + reason
		}
		first = refreshed
		if !save(first) {
			return first, "state_write_failed"
		}
		if reason := quotaCandidateReason(first); reason != "" {
			return first, reason
		}
	}
	if !wait(stop, 3*time.Second) {
		return first, "interrupted"
	}
	second, reason := fetch()
	if reason != "" {
		return first, "idle_confirmation_" + reason
	}
	if !save(second) {
		return second, "state_write_failed"
	}
	if !confirmedIdle(first, second) {
		return second, "five_hour_not_confirmed_idle"
	}
	return second, ""
}
