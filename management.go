package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const managementRoutePrefix = "/plugins/" + pluginName

func managementRegistration() pluginapi.ManagementRegistrationResponse {
	return pluginapi.ManagementRegistrationResponse{
		Resources: []pluginapi.ResourceRoute{{
			Path: "/status", Menu: "Codex 每日预热", Description: "查看定时、模型和最近执行结果。",
		}},
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: managementRoutePrefix + "/status", Description: "读取插件状态与最近一次执行结果。"},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/history", Description: "读取最近 30 次匿名化执行历史。"},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/run-now", Description: "立即执行一次；传 {\"notify\":true} 时仅在实际预热成功后发送 Bark；安全门槛始终生效。"},
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var request pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fmt.Errorf("decode management request: %w", err)
	}
	return okEnvelope(dispatchManagement(request))
}

func dispatchManagement(request pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == "" {
		method = http.MethodGet
	}
	switch {
	case method == http.MethodGet && isResourceStatusPath(request.Path):
		return htmlManagementResponse(renderStatusShell(app.status().Config.Model))
	case method == http.MethodGet && isManagementPath(request.Path, "/status"):
		return jsonManagementResponse(http.StatusOK, app.status())
	case method == http.MethodGet && isManagementPath(request.Path, "/history"):
		return jsonManagementResponse(http.StatusOK, map[string]any{"history": app.history()})
	case method == http.MethodPost && isManagementPath(request.Path, "/run-now"):
		var input struct {
			Force  bool   `json:"force"`
			Notify bool   `json:"notify"`
			Job    string `json:"job"`
		}
		if len(request.Body) > 0 {
			if err := json.Unmarshal(request.Body, &input); err != nil {
				return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "invalid_json"})
			}
		}
		if err := app.startNamedRunWithNotify("manual", input.Job, input.Force, input.Notify); err != nil {
			if err == errJobNotFound {
				return jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": "job_not_found"})
			}
			if err == errAlreadyRunning {
				return jsonManagementResponse(http.StatusConflict, map[string]any{"error": "already_running"})
			}
			return jsonManagementResponse(http.StatusServiceUnavailable, map[string]any{"error": safeErrorCode(err)})
		}
		return jsonManagementResponse(http.StatusAccepted, map[string]any{"accepted": true, "force": input.Force, "notify": input.Notify, "job": input.Job})
	default:
		return jsonManagementResponse(http.StatusNotFound, map[string]any{"error": "not_found"})
	}
}

func isManagementPath(path, suffix string) bool {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	return path == managementRoutePrefix+suffix || path == "/v0/management"+managementRoutePrefix+suffix
}

func isResourceStatusPath(path string) bool {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	return path == "/v0/resource"+managementRoutePrefix+"/status" || path == "/resource"+managementRoutePrefix+"/status"
}

func jsonManagementResponse(status int, value any) pluginapi.ManagementResponse {
	raw, err := json.Marshal(value)
	if err != nil {
		raw = []byte(`{"error":"json_encoding_failed"}`)
		status = http.StatusInternalServerError
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}, "Cache-Control": []string{"no-store"}},
		Body:       raw,
	}
}

func htmlManagementResponse(body string) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}, "Cache-Control": []string{"no-store"}},
		Body:       []byte(body),
	}
}

// Resource routes are public on some CPA installations. Load account details
// only through the already authenticated management status route.
// This shell reads a same-origin management key; escape every dynamic value.
func renderStatusShell(model string) string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Codex 每日预热</title><style>body{font-family:system-ui,sans-serif;max-width:900px;margin:40px auto;padding:0 20px;color:#17202a}section{border:1px solid #dde3ea;border-radius:14px;padding:20px;margin-bottom:18px}button{background:#175cd3;color:#fff;border:0;border-radius:8px;cursor:pointer}.hint{color:#667085;font-size:14px}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style></head><body><h1>Codex 每日预热</h1><p class="hint">巡检模型：` + html.EscapeString(model) + `</p><section><h2>立即预热</h2><p>先查询全部账号额度；符合条件的账号发起一次轻量请求，其余跳过。灰度名单、dry-run、五小时冷却和 24 小时次数限制仍然生效。</p><button id="run-now" type="button">一键预热（先查额度）</button><p id="run-result" role="status" aria-live="polite" class="hint"></p></section><section><h2>运行状态</h2><button id="unlock-status" type="button" hidden>验证并查看详情</button><pre id="status-details" class="hint">正在通过 CPA 管理认证加载状态…</pre></section>` + statusPageActionScript + `</body></html>`
}

func renderStatusPage(status runtimeStatus) string {
	location, err := time.LoadLocation(status.Config.Timezone)
	if err != nil {
		location = time.UTC
	}
	next := "未计划"
	if !status.NextRunAt.IsZero() {
		next = status.NextRunAt.In(location).Format("2006-01-02 15:04:05 MST")
	}
	last := "尚未运行"
	if status.LastRun != nil {
		lastError := status.LastRun.ErrorCode
		if lastError == "" {
			lastError = "无"
		}
		last = fmt.Sprintf("%s（%s），查询账号 %d/%d，符合预热 %d，预热账号 %d，有效模型回复 %d，跳过 %d，错误 %s",
			status.LastRun.FinishedAt.In(location).Format("2006-01-02 15:04:05 MST"), status.LastRun.Job,
			status.LastRun.QuotaQueried, status.LastRun.Discovered, status.LastRun.WouldWarm,
			status.LastRun.Attempted, status.LastRun.Succeeded, status.LastRun.Skipped, lastError)
	}
	var plans strings.Builder
	followupMode := map[string]string{"off": "关闭", "observe": "只读观察", "active": "符合条件时预热"}[status.Config.ResetFollowupMode]
	for _, job := range status.NextRuns {
		plans.WriteString("<dd><code>" + html.EscapeString(job.Name) + "</code> · " + html.EscapeString(job.Schedule) + " · " + html.EscapeString(job.Model) + " · " + html.EscapeString(job.NextRunAt.In(location).Format("2006-01-02 15:04 MST")) + "</dd>")
	}
	var accounts strings.Builder
	var followups strings.Builder
	accountIDs := make([]string, 0, len(status.Accounts))
	for account := range status.Accounts {
		accountIDs = append(accountIDs, account)
	}
	sort.Strings(accountIDs)
	for _, account := range accountIDs {
		window := status.Accounts[account]
		for _, plan := range []struct {
			kind string
			at   time.Time
		}{{"五小时", window.FiveHourFollowupAt}, {"周", window.WeeklyFollowupAt}, {"查询重试", window.QuotaRetryAt}} {
			if !plan.at.IsZero() {
				followups.WriteString("<tr><td>" + html.EscapeString(account) + "</td><td>" + plan.kind + "</td><td>" + html.EscapeString(plan.at.In(location).Format("2006-01-02 15:04:05 MST")) + "</td></tr>")
			}
		}
	}
	if status.LastRun != nil {
		for _, item := range status.LastRun.Accounts {
			quota := "未知"
			if !item.QuotaCheckedAt.IsZero() {
				quota = fmt.Sprintf("%.0f%% / %.0f%%", 100-item.FiveHour.UsedPercent, 100-item.Weekly.UsedPercent)
			}
			reason := item.SkipReason
			if reason == "followup_observe" {
				reason = "观察模式：符合预热条件，未发模型请求"
			}
			accounts.WriteString("<tr><td>" + html.EscapeString(item.Account) + "</td><td>" + html.EscapeString(item.QuotaStatus) + "</td><td>" + quota + "</td><td>" + html.EscapeString(reason) + "</td><td>" + fmt.Sprintf("%d", item.Attempts24h) + "</td></tr>")
		}
	}
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Codex 每日预热</title><style>body{font-family:system-ui,sans-serif;max-width:900px;margin:40px auto;padding:0 20px;color:#17202a}section{border:1px solid #dde3ea;border-radius:14px;padding:20px;margin-bottom:18px}dt{color:#667085}dd{margin:4px 0 14px;font-weight:600}code{background:#f4f6f8;padding:2px 6px;border-radius:5px}table{border-collapse:collapse;width:100%}td,th{text-align:left;padding:8px;border-bottom:1px solid #dde3ea}button{background:#175cd3;color:#fff;border:0;border-radius:8px;cursor:pointer}button:disabled{opacity:.5;cursor:wait}.hint{color:#667085;font-size:14px}</style></head><body><h1>Codex 每日预热</h1><section><h2>立即预热</h2><p>立即查询全部账号额度；符合现有条件的账号马上发起一次轻量请求，其余账号跳过。仍受灰度名单、dry-run、五小时冷却和滚动 24 小时次数限制。</p><button id="run-now" type="button">一键预热（先查额度）</button><p id="run-result" role="status" aria-live="polite" class="hint"></p></section><section><dl><dt>状态</dt><dd>` + html.EscapeString(map[bool]string{true: "运行中", false: "空闲"}[status.Running]) + `</dd><dt>自动任务</dt><dd>` + html.EscapeString(map[bool]string{true: "已启用", false: "已停用"}[status.Config.AutomaticEnabled]) + `</dd><dt>dry-run</dt><dd>` + html.EscapeString(map[bool]string{true: "开启", false: "关闭"}[status.Config.DryRun]) + `</dd><dt>重置补查</dt><dd>` + html.EscapeString(followupMode) + `</dd><dt>定时任务</dt>` + plans.String() + `<dt>下次运行</dt><dd>` + html.EscapeString(next) + `</dd><dt>上次运行</dt><dd>` + html.EscapeString(last) + `</dd></dl></section><section><h2>待执行的账号补查</h2><table><tr><th>匿名账号</th><th>类型</th><th>计划时间</th></tr>` + followups.String() + `</table></section><section><h2>最近逐账号检查</h2><table><tr><th>匿名账号</th><th>查询</th><th>剩余 5h / 周</th><th>结果</th><th>24h 调用</th></tr>` + accounts.String() + `</table></section><p>完整匿名化历史位于 CPA 管理 API。</p>` + statusPageActionScript + `</body></html>`
}

// The resource page is public. Only the authenticated management route may start a run.
// The CPA panel stores its remembered management key in the persisted auth store.
// The public page only loads account details from the authenticated management route.
const statusPageActionScript = `<script>
(() => {
  const button = document.getElementById('run-now');
  const message = document.getElementById('run-result');
  const details = document.getElementById('status-details');
  const unlock = document.getElementById('unlock-status');
  const route = '/v0/management/plugins/codex-daily-prewarm';
  let sessionKey = '';
  let ignoreRememberedKey = false;

  function rememberedKey() {
    try {
      let value = localStorage.getItem('cli-proxy-auth');
      if (!value) return '';
      if (value.startsWith('enc::v1::')) {
        const secret = new TextEncoder().encode('cli-proxy-api-webui::secure-storage|' + location.host + '|' + navigator.userAgent);
        const encoded = atob(value.slice(9));
        const bytes = new Uint8Array(encoded.length);
        for (let i = 0; i < encoded.length; i++) bytes[i] = encoded.charCodeAt(i) ^ secret[i % secret.length];
        value = new TextDecoder().decode(bytes);
      }
      const auth = JSON.parse(value);
      if (!auth || !auth.state || auth.state.rememberPassword !== true) return '';
      return typeof auth.state.managementKey === 'string' ? auth.state.managementKey : '';
    } catch (_) {
      return '';
    }
  }

  async function managementRequest(path, options, key) {
    const response = await fetch(route + path, {
      ...options,
      cache: 'no-store',
      credentials: 'same-origin',
      headers: {'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'}
    });
    if (response.status === 401 || response.status === 403) throw new Error('管理认证失败，请在 CPA 面板重新登录');
    const body = await response.json();
    if (!response.ok) {
      if (response.status === 409) throw new Error('已有巡检正在运行，请稍后再试');
      throw new Error('巡检未启动：' + (body.error || response.status));
    }
    return body;
  }

  function formatTime(value, timezone) {
    if (!value || value.startsWith('0001-01-01')) return '';
    const at = new Date(value);
    return Number.isNaN(at.getTime()) ? '' : at.toLocaleString('zh-CN', {timeZone: timezone, hour12: false});
  }

  async function loadStatus() {
    if (!details) return;
    const key = sessionKey || (!ignoreRememberedKey && rememberedKey());
    if (!key) {
      unlock.hidden = false;
      details.textContent = 'CPA 面板未保存管理密钥；可验证一次后查看详情，或在面板登录时选择“记住密钥”。';
      return;
    }
    try {
      const state = await managementRequest('/status', {method: 'GET'}, key);
      unlock.hidden = true;
      const last = state.last_run;
      const lines = [
        '自动任务：' + (state.config.automatic_enabled ? '已启用' : '已停用'),
        'dry-run：' + (state.config.dry_run ? '开启' : '关闭'),
        '重置补查：' + state.config.reset_followup_mode,
        '当前：' + (state.running ? '运行中' : '空闲'),
        '下次运行：' + (formatTime(state.next_run_at, state.config.timezone) || '未计划'),
        '最近运行：' + (last ? [formatTime(last.finished_at, state.config.timezone), '查询 ' + last.quota_queried_accounts + '/' + last.discovered_accounts, '符合预热 ' + last.would_warm_accounts, '预热请求 ' + last.attempted_accounts, '跳过 ' + last.skipped_accounts, 'Bark ' + (last.bark_status || '无')].join(' · ') : '暂无'),
        '', '待执行账号补查：'
      ];
      for (const [account, window] of Object.entries(state.accounts || {}).sort()) {
        for (const [label, field] of [['5h', 'five_hour_followup_at'], ['周', 'weekly_followup_at'], ['查询重试', 'quota_retry_at']]) {
          const at = formatTime(window[field], state.config.timezone);
          if (at) lines.push(account + ' · ' + label + ' · ' + at);
        }
      }
      lines.push('', '最近逐账号检查：');
      for (const item of (last?.accounts || [])) {
        const quota = item.quota_checked_at ? '5h ' + (100 - (item.five_hour?.used_percent ?? 0)).toFixed(0) + '% / 周 ' + (100 - (item.weekly?.used_percent ?? 0)).toFixed(0) + '%' : '额度未知';
        lines.push([item.account, quota, item.skip_reason || item.error_code || (item.response_received ? '预热成功' : '无结果'), '24h 调用 ' + item.attempts_24h].join(' · '));
      }
      details.textContent = lines.join('\n');
    } catch (error) {
      unlock.hidden = false;
      details.textContent = error instanceof Error ? error.message : '状态加载失败';
    }
  }

  loadStatus();

  unlock.addEventListener('click', () => {
    const key = window.prompt('请输入 CPA 管理密钥（仅用于本页，不保存）') || '';
    if (!key) return;
    sessionKey = key;
    loadStatus();
  });

  button.addEventListener('click', async () => {
    button.disabled = true;
    try {
      let key = sessionKey || (!ignoreRememberedKey && rememberedKey());
      if (!key) key = window.prompt('请输入 CPA 管理密钥（仅用于本次页面请求，不保存）') || '';
      if (!key) { message.textContent = '已取消。'; return; }
      sessionKey = key;
      loadStatus();
      message.textContent = '正在启动巡检…';
      const before = await managementRequest('/status', {method: 'GET'}, key);
      const previousID = before.last_run && before.last_run.id;
      await managementRequest('/run-now', {method: 'POST', body: '{"notify":true}'}, key);
      message.textContent = '已启动，正在逐账号查询并按规则预热…';
      for (let i = 0; i < 90; i++) {
        await new Promise(resolve => setTimeout(resolve, 2000));
        const state = await managementRequest('/status', {method: 'GET'}, key);
        const result = state.last_run;
        if (result && result.id !== previousID && result.trigger === 'manual') {
          message.textContent = '已完成：查询 ' + result.quota_queried_accounts + ' 个，预热请求 ' + result.attempted_accounts + ' 个，跳过 ' + result.skipped_accounts + ' 个。页面即将刷新。';
          setTimeout(() => location.reload(), 1800);
          return;
        }
      }
      message.textContent = '巡检已启动但仍未完成，请稍后刷新页面查看结果。';
    } catch (error) {
      message.textContent = error instanceof Error ? error.message : '巡检失败，请查看 CPA 日志';
      if (message.textContent.includes('认证失败')) { sessionKey = ''; ignoreRememberedKey = true; }
    } finally {
      button.disabled = false;
    }
  });
})();
</script>`
