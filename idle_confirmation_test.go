package main

import (
	"testing"
	"time"
)

func idleObservation(at time.Time, weeklyUsed float64) quotaObservation {
	return quotaObservation{
		CheckedAt: at,
		Allowed:   true,
		FiveHour:  quotaWindow{WindowMinutes: fiveHourMinutes, ResetAt: at.Add(5 * time.Hour)},
		Weekly:    quotaWindow{WindowMinutes: weeklyMinutes, UsedPercent: weeklyUsed, ResetAt: at.Add(48 * time.Hour)},
	}
}

func TestFiveIdleAccountsRefreshAfterSpacingAndAllConfirm(t *testing.T) {
	for account := 0; account < 5; account++ {
		first := idleObservation(time.Now().Add(-time.Minute), 20)
		var order []string
		var refreshed quotaObservation
		fetches, saves := 0, 0
		latest, reason := confirmIdleAfterSpacing(first, 30*time.Second, nil,
			func(_ <-chan struct{}, duration time.Duration) bool {
				if duration == 30*time.Second {
					order = append(order, "spacing")
				} else if duration == 3*time.Second {
					order = append(order, "confirmation_delay")
				} else {
					t.Fatalf("unexpected delay: %s", duration)
				}
				return true
			},
			func() (quotaObservation, string) {
				fetches++
				if fetches == 1 {
					refreshed = idleObservation(time.Now(), 20)
					order = append(order, "fresh_first")
					return refreshed, ""
				}
				order = append(order, "second")
				return idleObservation(refreshed.CheckedAt.Add(3*time.Second), 20), ""
			},
			func(quotaObservation) bool { saves++; return true },
		)
		if reason != "" || fetches != 2 || saves != 2 || latest.CheckedAt.Sub(refreshed.CheckedAt) != 3*time.Second {
			t.Fatalf("account %d: reason=%s fetches=%d saves=%d", account, reason, fetches, saves)
		}
		want := []string{"spacing", "fresh_first", "confirmation_delay", "second"}
		for i, step := range want {
			if order[i] != step {
				t.Fatalf("account %d: order=%v", account, order)
			}
		}
	}
}

func TestRefreshedQuotaCanVetoPrewarm(t *testing.T) {
	fetches := 0
	_, reason := confirmIdleAfterSpacing(idleObservation(time.Now().Add(-time.Minute), 20), 0, nil,
		func(<-chan struct{}, time.Duration) bool { return true },
		func() (quotaObservation, string) { fetches++; return idleObservation(time.Now(), 100), "" },
		func(quotaObservation) bool { return true },
	)
	if reason != "weekly_below_threshold" || fetches != 1 {
		t.Fatalf("stale weekly evidence was accepted: reason=%s fetches=%d", reason, fetches)
	}
}

func TestStopDuringSpacingPreventsFurtherQuotaCalls(t *testing.T) {
	stop := make(chan struct{})
	fetches := 0
	_, reason := confirmIdleAfterSpacing(idleObservation(time.Now().Add(-time.Minute), 20), time.Hour, stop,
		func(ch <-chan struct{}, _ time.Duration) bool {
			close(stop)
			return waitForRun(ch, time.Hour)
		},
		func() (quotaObservation, string) { fetches++; return quotaObservation{}, "" },
		func(quotaObservation) bool { return true },
	)
	if reason != "interrupted" || fetches != 0 {
		t.Fatalf("continued after stop: reason=%s fetches=%d", reason, fetches)
	}
}
