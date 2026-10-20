package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func notificationTime(clock string) time.Time {
	at, err := time.ParseInLocation("2006-01-02 15:04", "2026-"+clock, defaultPluginConfig().Location)
	if err != nil {
		panic(err)
	}
	return at
}

func notificationRuntime(t *testing.T) (*runtime, *[]map[string]string) {
	t.Helper()
	r := newRuntime()
	r.cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	r.cfg.BarkURL = "https://bark.example/test-key"
	var payloads []map[string]string
	r.barkSend = func(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		var payload map[string]string
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200}`)}, nil
	}
	return r, &payloads
}

func notificationRun(r *runtime, clock string, notify bool, accounts ...accountResult) string {
	at := notificationTime(clock)
	r.now = func() time.Time {
		return at
	}
	record := runRecord{Trigger: "schedule", Discovered: len(accounts), Accounts: accounts}
	for _, item := range accounts {
		if item.ResponseReceived {
			record.Succeeded++
		} else if item.Attempts > 0 {
			record.Failed++
		} else {
			record.Skipped++
		}
	}
	return r.recordNotifications(r.cfg, &record, notify)
}

func TestSuccessDeferredAndDigestWindowBoundaries(t *testing.T) {
	r, sent := notificationRuntime(t)
	ok := accountResult{Account: "acct-3831abcd1234", ResponseReceived: true, QuotaStatus: "confirmed", QuotaCheckedAt: notificationTime("10-19 22:29"), Weekly: quotaWindow{WindowMinutes: weeklyMinutes, UsedPercent: 7}}
	if got := notificationRun(r, "10-19 22:29", false, ok); got != "deferred_to_digest" || len(*sent) != 0 {
		t.Fatalf("success notification: %s %#v", got, *sent)
	}
	r.processNotifications(notificationTime("10-19 22:29"))
	if len(*sent) != 0 {
		t.Fatal("early digest")
	}
	r.processNotifications(notificationTime("10-19 22:30"))
	if len(*sent) != 1 {
		t.Fatalf("digest count: %d", len(*sent))
	}
	payload := (*sent)[0]
	if payload["level"] != "passive" || payload["title"] != "Codex 预热日报 10-19" {
		t.Fatalf("digest fields: %#v", payload)
	}
	for _, want := range []string{"统计 10-18 22:30 – 10-19 22:30", "巡检 1 轮，成功预热 1 次，失败 0", "acct-3831abcd1234 预热 1 次 · 周剩余 93%"} {
		if !strings.Contains(payload["body"], want) {
			t.Fatalf("missing %s: %s", want, payload["body"])
		}
	}
	for _, clock := range []string{"10-19 22:30", "10-19 22:31", "10-19 23:00", "10-20 01:00"} {
		notificationRun(r, clock, false, ok)
	}
	if len(*sent) != 1 {
		t.Fatal("success sent immediately after digest or at night")
	}
	r.processNotifications(notificationTime("10-20 22:30"))
	if len(*sent) != 2 || !strings.Contains((*sent)[1]["body"], "巡检 4 轮，成功预热 4 次") {
		t.Fatalf("next window: %#v", *sent)
	}
	r.processNotifications(notificationTime("10-21 22:30"))
	if len(*sent) != 3 || !strings.Contains((*sent)[2]["body"], "巡检 0 轮") {
		t.Fatalf("missing heartbeat: %#v", *sent)
	}
}

func TestFailureDeduplicationRecoveryAndSkipSummary(t *testing.T) {
	r, sent := notificationRuntime(t)
	fail := accountResult{Account: "acct-3831abcd1234", Attempts: 2, ErrorCode: "transport_failed"}
	if got := notificationRun(r, "10-19 09:00", false, fail); got != "failure_accepted" {
		t.Fatal(got)
	}
	if got := notificationRun(r, "10-19 10:00", false, fail); got != "failure_deduplicated" || len(*sent) != 1 {
		t.Fatalf("dedupe %s %#v", got, *sent)
	}
	other := fail
	other.ErrorCode = "usage_limit_reached"
	if got := notificationRun(r, "10-19 11:00", false, other); got != "failure_deduplicated" {
		t.Fatal(got)
	}
	if len(*sent) != 1 || (*sent)[0]["level"] != "timeSensitive" {
		t.Fatalf("failure payloads: %#v", *sent)
	}
	ok := accountResult{Account: fail.Account, ResponseReceived: true, QuotaStatus: "confirmed", QuotaCheckedAt: notificationTime("10-19 12:00"), Weekly: quotaWindow{WindowMinutes: weeklyMinutes, UsedPercent: 10}}
	notificationRun(r, "10-19 12:00", false, ok)
	ok.ResponseReceived = false
	ok.Weekly.UsedPercent = 20
	ok.QuotaCheckedAt = notificationTime("10-19 13:00")
	ok.SkipReason = "weekly_below_threshold"
	notificationRun(r, "10-19 13:00", false, ok)
	notificationRun(r, "10-19 14:00", false, accountResult{Account: "acct-other", SkipReason: "rolling_24h_limit"})
	r.processNotifications(notificationTime("10-19 22:30"))
	body := (*sent)[1]["body"]
	for _, want := range []string{"失败 3", "3 次（已恢复）", "周剩余 80%", "weekly_below_threshold 1", "rolling_24h_limit 1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s: %s", want, body)
		}
	}
	notificationRun(r, "10-19 22:31", false, fail)
	if len(*sent) != 3 {
		t.Fatal("dedupe did not reset at digest boundary")
	}
}

func TestQuietFailuresMergeAtEightAcrossRestart(t *testing.T) {
	r, sent := notificationRuntime(t)
	r.processNotifications(notificationTime("10-19 22:29"))
	r.processNotifications(notificationTime("10-19 22:30"))
	fail := accountResult{Account: "acct-3831abcd1234", Attempts: 1, ErrorCode: "no_valid_response"}
	if got := notificationRun(r, "10-19 23:00", false, fail); got != "failure_queued_quiet_hours" {
		t.Fatal(got)
	}
	if got := notificationRun(r, "10-20 01:00", false, fail); got != "failure_deduplicated" {
		t.Fatal(got)
	}
	fail.Account = "acct-other"
	notificationRun(r, "10-20 07:59", false, fail)
	if len(*sent) != 1 {
		t.Fatal("failure escaped quiet hours")
	}
	state, err := readState(r.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newRuntime()
	restarted.cfg = r.cfg
	restarted.state = state
	restarted.barkSend = r.barkSend
	restarted.processNotifications(notificationTime("10-20 07:59"))
	if len(*sent) != 1 {
		t.Fatal("queued failures sent early")
	}
	restarted.processNotifications(notificationTime("10-20 08:00"))
	restarted.processNotifications(notificationTime("10-20 08:01"))
	if len(*sent) != 2 || (*sent)[1]["level"] != "timeSensitive" || !strings.Contains((*sent)[1]["body"], "告警 2 项") || len(restarted.state.Notifications.Pending) != 0 {
		t.Fatalf("morning merge: %#v", *sent)
	}
	if got := notificationRun(restarted, "10-20 08:02", false, fail); got != "failure_deduplicated" {
		t.Fatal(got)
	}
	state, err = readState(r.cfg.StatePath)
	if err != nil || state.Notifications.LastQuietResult != "accepted" {
		t.Fatalf("quiet result not persisted: %v %#v", err, state.Notifications)
	}
}

func TestUnknownStreakPersistsAndResets(t *testing.T) {
	r, sent := notificationRuntime(t)
	unknown := accountResult{Account: "acct-3831abcd1234", SkipReason: "five_hour_unknown"}
	for _, clock := range []string{"10-19 09:00", "10-19 10:00"} {
		notificationRun(r, clock, false, unknown)
	}
	if len(*sent) != 0 {
		t.Fatal("single unknown alerted")
	}
	state, err := readState(r.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	r.state = state
	unknown.SkipReason = "weekly_unknown"
	if got := notificationRun(r, "10-19 11:00", false, unknown); got != "failure_accepted" || len(*sent) != 1 {
		t.Fatalf("third unknown: %s %#v", got, *sent)
	}
	if !strings.Contains((*sent)[0]["body"], unknown.Account+" · 连续 3 轮额度未知") {
		t.Fatalf("unknown streak detail missing: %#v", *sent)
	}
	if got := notificationRun(r, "10-19 12:00", false, unknown); got != "failure_deduplicated" {
		t.Fatal(got)
	}
	known := unknown
	known.SkipReason = "active_five_hour_window"
	notificationRun(r, "10-19 13:00", false, known)
	if r.state.Notifications.UnknownStreaks[known.Account] != 0 {
		t.Fatal("known quota did not reset streak")
	}
	unknown.Account = "acct-expired"
	unknown.SkipReason = ""
	unknown.QuotaStatus = "query_failed"
	unknown.ErrorCode = "usage_http_401"
	for _, clock := range []string{"10-19 14:00", "10-19 15:00", "10-19 16:00"} {
		notificationRun(r, clock, false, unknown)
	}
	if len(*sent) != 2 {
		t.Fatal("persistent fetch failures did not alert")
	}
}

func TestRestartBackfillsDigestOnceAndLegacyStateLoads(t *testing.T) {
	r, sent := notificationRuntime(t)
	legacy := []byte(`{"version":1,"accounts":{"acct-legacy":{}},"history":[]}`)
	if err := os.WriteFile(r.cfg.StatePath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := readState(r.cfg.StatePath)
	if err != nil || state.Notifications.Windows == nil || state.Notifications.UnknownStreaks == nil || len(state.Accounts) != 1 {
		t.Fatalf("legacy state: %v %#v", err, state)
	}
	r.state = state
	notificationRun(r, "10-19 10:00", false, accountResult{Account: "acct-ok", ResponseReceived: true})
	state, err = readState(r.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	r.state = state
	r.processNotifications(notificationTime("10-20 09:00"))
	r.processNotifications(notificationTime("10-20 09:01"))
	state, err = readState(r.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	r.state = state
	r.processNotifications(notificationTime("10-20 09:02"))
	if len(*sent) != 1 || !strings.Contains((*sent)[0]["body"], "成功预热 1 次") || !state.Notifications.LastDigestAt.Equal(notificationTime("10-20 09:00")) {
		t.Fatalf("backfill: %#v %#v", *sent, state.Notifications)
	}
}

func TestManualFeedbackActiveEvenAtNightAndWithoutSuccess(t *testing.T) {
	for _, accounts := range [][]accountResult{nil, {{Account: "acct-ok", ResponseReceived: true}}, {{Account: "acct-fail", Attempts: 1, ErrorCode: "no_valid_response"}}, {{Account: "acct-skip", SkipReason: "dry_run"}}} {
		r, sent := notificationRuntime(t)
		if got := notificationRun(r, "10-19 23:15", true, accounts...); got != "manual_accepted" || len(*sent) != 1 || (*sent)[0]["level"] != "active" || (*sent)[0]["title"] != "Codex 预热结果" {
			t.Fatalf("manual result: %s %#v", got, *sent)
		}
		if len(r.state.Notifications.Pending) != 0 {
			t.Fatal("manual feedback also queued an alert")
		}
	}
}

func TestDigestRetriesAreBoundedAndPersisted(t *testing.T) {
	for _, failure := range []string{"transport_failed", "not_accepted"} {
		t.Run(failure, func(t *testing.T) {
			r, _ := notificationRuntime(t)
			calls := 0
			r.barkSend = func(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
				calls++
				if failure == "transport_failed" {
					return pluginapi.HTTPResponse{}, errors.New("offline test")
				}
				return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":500}`)}, nil
			}
			r.processNotifications(notificationTime("10-19 22:29"))
			for _, clock := range []string{"10-19 22:30", "10-19 22:31", "10-19 22:34", "10-19 22:35", "10-19 22:36", "10-19 22:40", "10-19 22:45"} {
				r.processNotifications(notificationTime(clock))
				state, err := readState(r.cfg.StatePath)
				if err != nil {
					t.Fatal(err)
				}
				r.state = state
			}
			if calls != 3 || r.state.Notifications.LastDigestResult != "abandoned_"+failure || r.state.Notifications.LastDigestAttempts != 3 {
				t.Fatalf("retry state calls=%d %#v", calls, r.state.Notifications)
			}
		})
	}
}

func TestUncertainDigestDeliveryDoesNotDuplicate(t *testing.T) {
	r, sent := notificationRuntime(t)
	n := &r.state.Notifications
	n.init()
	n.NextDigestAt = notificationTime("10-19 22:30")
	w := n.window(r.cfg, n.NextDigestAt)
	w.Attempts = 1
	w.Result = "sending"
	if err := writeState(r.cfg.StatePath, r.state); err != nil {
		t.Fatal(err)
	}
	state, err := readState(r.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	r.state = state
	r.processNotifications(notificationTime("10-20 09:00"))
	if len(*sent) != 0 || r.state.Notifications.LastDigestResult != "abandoned_uncertain_delivery" {
		t.Fatalf("uncertain dispatch repeated: %#v", r.state.Notifications)
	}
}

func TestNotificationClockUsesConfiguredTimezoneAndQuietBoundaries(t *testing.T) {
	cfg := defaultPluginConfig()
	for clock, quiet := range map[string]bool{"10-19 22:59": false, "10-19 23:00": true, "10-20 07:59": true, "10-20 08:00": false} {
		at := notificationTime(clock).UTC()
		if got := !quietUntil(cfg, at).IsZero(); got != quiet {
			t.Fatalf("%s quiet=%v", clock, got)
		}
	}
	cfg.QuietStart = "12:00"
	cfg.QuietEnd = "13:00"
	if quietUntil(cfg, notificationTime("10-19 12:30")).IsZero() || !quietUntil(cfg, notificationTime("10-19 13:00")).IsZero() {
		t.Fatal("same-day quiet hours")
	}
}

func TestNotificationSchedulerBackfillsOnStartupWithAutomaticRunsDisabled(t *testing.T) {
	r := newRuntime()
	path := filepath.Join(t.TempDir(), "state.json")
	now := notificationTime("10-20 09:00")
	r.now = func() time.Time { return now }
	state := newState()
	state.Notifications.init()
	state.Notifications.NextDigestAt = notificationTime("10-19 22:30")
	state.Notifications.window(r.cfg, state.Notifications.NextDigestAt).Runs = 2
	if err := writeState(path, state); err != nil {
		t.Fatal(err)
	}
	delivered := make(chan string, 4)
	r.barkSend = func(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		var payload map[string]string
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			return pluginapi.HTTPResponse{}, err
		}
		delivered <- payload["body"]
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200}`)}, nil
	}
	config := []byte("automatic_enabled: false\nbark_url: https://bark.example/test-key\nstate_path: " + path)
	if err := r.configure(config); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-delivered:
		if !strings.Contains(body, "巡检 2 轮") {
			t.Error(body)
		}
	case <-time.After(time.Second):
		r.shutdown()
		t.Fatal("startup did not backfill the overdue digest")
	}
	r.shutdown()
	restarted := newRuntime()
	restarted.now = r.now
	restarted.barkSend = r.barkSend
	if err := restarted.configure(config); err != nil {
		t.Fatal(err)
	}
	restarted.scheduler.processDue(now)
	restarted.shutdown()
	select {
	case body := <-delivered:
		t.Fatalf("duplicate after second restart: %s", body)
	default:
	}
}

func TestFailedImmediateAndMorningPushesAreNotRetried(t *testing.T) {
	r, _ := notificationRuntime(t)
	calls := 0
	r.barkSend = func(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		calls++
		return pluginapi.HTTPResponse{StatusCode: 503}, nil
	}
	fail := accountResult{Account: "acct-3831abcd1234", Attempts: 1, ErrorCode: "no_valid_response"}
	if got := notificationRun(r, "10-19 10:00", false, fail); got != "failure_http_503" {
		t.Fatal(got)
	}
	notificationRun(r, "10-19 11:00", false, fail)
	if calls != 1 {
		t.Fatal("failed immediate push retried")
	}
	// Settle the old digest before the night window begins.
	r.processNotifications(notificationTime("10-19 22:30"))
	r.processNotifications(notificationTime("10-19 22:35"))
	r.processNotifications(notificationTime("10-19 22:40"))
	notificationRun(r, "10-19 23:00", false, fail)
	r.processNotifications(notificationTime("10-20 08:00"))
	callsAfterMorning := calls
	r.processNotifications(notificationTime("10-20 08:01"))
	if calls != callsAfterMorning || r.state.Notifications.LastQuietResult != "http_503" {
		t.Fatalf("morning push repeated: calls=%d result=%s", calls, r.state.Notifications.LastQuietResult)
	}
}

func TestInterruptedReservationWithoutRequestDoesNotAlert(t *testing.T) {
	r, sent := notificationRuntime(t)
	item := accountResult{Account: "acct-3831abcd1234", Attempts: 1, ErrorCode: "interrupted"}
	if got := notificationRun(r, "10-19 10:00", false, item); got != "deferred_to_digest" || len(*sent) != 0 {
		t.Fatalf("unsent reservation alerted: %s %#v", got, *sent)
	}
	item.RequestSent = true
	if got := notificationRun(r, "10-19 10:01", false, item); got != "failure_accepted" {
		t.Fatalf("sent request failure did not alert: %s", got)
	}
}

func TestFirstNotificationInitializationAfterDigestWaitsUntilTomorrow(t *testing.T) {
	for _, source := range []string{"status", "scheduler", "run"} {
		t.Run(source, func(t *testing.T) {
			r, sent := notificationRuntime(t)
			now := notificationTime("10-19 23:00")
			want := notificationTime("10-20 22:30")
			switch source {
			case "status":
				if got := r.state.Notifications.status(r.cfg, now).NextDigestAt; !got.Equal(want) {
					t.Fatalf("first status next digest = %s, want %s", got, want)
				}
			case "scheduler":
				r.processNotifications(now)
			case "run":
				notificationRun(r, "10-19 23:00", false, accountResult{Account: "acct-ok", ResponseReceived: true})
			}
			r.processNotifications(now)
			if len(*sent) != 0 || !r.state.Notifications.NextDigestAt.Equal(want) {
				t.Fatalf("first initialization sent an old digest: %#v %#v", *sent, r.state.Notifications)
			}
			r.processNotifications(notificationTime("10-20 22:29"))
			if len(*sent) != 0 {
				t.Fatal("first digest sent early")
			}
			r.processNotifications(want)
			r.processNotifications(want.Add(time.Second))
			if len(*sent) != 1 || (*sent)[0]["title"] != "Codex 预热日报 10-20" {
				t.Fatalf("first digest missing or duplicated: %#v", *sent)
			}
		})
	}
}

func TestMultiDayOfflineBackfillsOnlyOneDigest(t *testing.T) {
	r, sent := notificationRuntime(t)
	n := &r.state.Notifications
	n.init()
	n.NextDigestAt = notificationTime("10-16 22:30")
	n.window(r.cfg, n.NextDigestAt).Runs = 2
	for _, clock := range []string{"10-17 22:30", "10-18 22:30", "10-19 22:30"} {
		n.window(r.cfg, notificationTime(clock))
	}
	futureEnd := notificationTime("10-20 22:30")
	future := n.window(r.cfg, futureEnd)
	future.Runs = 1
	future.Failures["acct-kept|request_failed"] = "queued"
	due := notificationTime("10-20 08:00")
	n.Pending = []failureNotice{
		{Account: "acct-old", Category: "request_failed", WindowEnd: notificationTime("10-17 22:30"), DueAt: due},
		{Account: "acct-kept", Category: "request_failed", WindowEnd: futureEnd, DueAt: due},
	}
	if err := writeState(r.cfg.StatePath, r.state); err != nil {
		t.Fatal(err)
	}
	state, err := readState(r.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	r.state = state
	now := notificationTime("10-19 23:00")
	for tick := 0; tick < 5; tick++ {
		r.processNotifications(now.Add(time.Duration(tick) * time.Second))
	}
	n = &r.state.Notifications
	if len(*sent) != 1 || !strings.Contains((*sent)[0]["body"], "巡检 2 轮") || !n.NextDigestAt.Equal(futureEnd) {
		t.Fatalf("offline catchup repeated: %#v %#v", *sent, n)
	}
	if len(n.Windows) != 1 || n.Windows[futureEnd.UTC().Format(time.RFC3339)].Failures["acct-kept|request_failed"] != "queued" || len(n.Pending) != 1 || n.Pending[0].Account != "acct-kept" {
		t.Fatalf("stale windows or pending failures retained: %#v", n)
	}
	state, err = readState(r.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	r.state = state
	// Keep the retained night queue separate from the digest count.
	r.processNotifications(notificationTime("10-20 08:00"))
	if len(*sent) != 2 || (*sent)[1]["level"] != "timeSensitive" {
		t.Fatalf("retained night queue was lost: %#v", *sent)
	}
	r.processNotifications(notificationTime("10-20 22:29"))
	if len(*sent) != 2 {
		t.Fatal("digest sent before the next boundary")
	}
	r.processNotifications(futureEnd)
	if len(*sent) != 3 || (*sent)[2]["level"] != "passive" || (*sent)[2]["title"] != "Codex 预热日报 10-20" {
		t.Fatalf("next digest missing: %#v", *sent)
	}
}

func TestManualNotificationAccountingPrunesWindowsOlderThanSevenDays(t *testing.T) {
	r, sent := notificationRuntime(t)
	r.cfg.BarkURL = ""
	n := &r.state.Notifications
	n.init()
	oldEnd := notificationTime("10-11 22:30")
	keptEnd := notificationTime("10-12 10:00")
	n.window(r.cfg, oldEnd)
	n.window(r.cfg, keptEnd).Failures["acct-kept|request_failed"] = "accepted"
	n.Pending = []failureNotice{
		{Account: "acct-old", WindowEnd: oldEnd},
		{Account: "acct-kept", WindowEnd: keptEnd},
	}
	notificationRun(r, "10-19 10:00", false, accountResult{Account: "acct-ok", ResponseReceived: true})
	state, err := readState(r.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	n = &state.Notifications
	if len(*sent) != 0 || len(n.Windows) != 2 || n.Windows[oldEnd.UTC().Format(time.RFC3339)] != nil {
		t.Fatalf("old windows retained without scheduler: %#v", n)
	}
	if n.Windows[keptEnd.UTC().Format(time.RFC3339)].Failures["acct-kept|request_failed"] != "accepted" || len(n.Pending) != 1 || n.Pending[0].Account != "acct-kept" {
		t.Fatalf("pruning changed a retained window's dedupe: %#v", n)
	}
}

func TestRequestFailureCodesShareWindowDeduplication(t *testing.T) {
	r, sent := notificationRuntime(t)
	item := accountResult{Account: "acct-3831abcd1234", Attempts: 1, ErrorCode: "http_502"}
	if got := notificationRun(r, "10-19 10:00", false, item); got != "failure_accepted" {
		t.Fatal(got)
	}
	state, err := readState(r.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	r.state = state
	item.ErrorCode = "http_503"
	if got := notificationRun(r, "10-19 11:00", false, item); got != "failure_deduplicated" || len(*sent) != 1 {
		t.Fatalf("HTTP codes bypassed request dedupe: %s %#v", got, *sent)
	}
	if !strings.Contains((*sent)[0]["body"], item.Account+" · 请求失败（http_502）") {
		t.Fatalf("request detail missing: %#v", *sent)
	}
	w := r.state.Notifications.window(r.cfg, notificationTime("10-19 22:30"))
	if w.Failed != 2 || w.Accounts[item.Account].Failed != 2 || len(w.Failures) != 1 || w.Failures[item.Account+"|request_failed"] != "accepted" {
		t.Fatalf("deduplicated failure was not counted: %#v", w)
	}
	item.Account = "acct-no-code"
	item.ErrorCode = ""
	notificationRun(r, "10-19 12:00", false, item)
	if len(*sent) != 2 || !strings.Contains((*sent)[1]["body"], item.Account+" · 请求失败（no_valid_response）") {
		t.Fatalf("missing fallback request detail: %#v", *sent)
	}
}
