package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"

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
			{Method: http.MethodPost, Path: managementRoutePrefix + "/run-now", Description: "立即执行一次；可传 {\"notify\":true} 验证 Bark；安全门槛始终生效。"},
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
	case method == http.MethodGet && pathEndsWith(request.Path, "/status") && strings.Contains(request.Path, "/resource/"):
		return htmlManagementResponse(renderStatusPage(app.status()))
	case method == http.MethodGet && pathEndsWith(request.Path, "/status"):
		return jsonManagementResponse(http.StatusOK, app.status())
	case method == http.MethodGet && pathEndsWith(request.Path, "/history"):
		return jsonManagementResponse(http.StatusOK, map[string]any{"history": app.history()})
	case method == http.MethodPost && pathEndsWith(request.Path, "/run-now"):
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

func pathEndsWith(path, suffix string) bool {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	return strings.HasSuffix(path, strings.TrimRight(suffix, "/"))
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

func renderStatusPage(status runtimeStatus) string {
	next := "未计划"
	if !status.NextRunAt.IsZero() {
		next = status.NextRunAt.Format("2006-01-02 15:04:05 MST")
	}
	last := "尚未运行"
	if status.LastRun != nil {
		last = fmt.Sprintf("%s（%s），成功 %d/%d，错误 %s", status.LastRun.FinishedAt.Format("2006-01-02 15:04:05 MST"), status.LastRun.Job, status.LastRun.Succeeded, status.LastRun.Discovered, status.LastRun.ErrorCode)
	}
	var plans strings.Builder
	lastSync := "尚未触发"
	if !status.LastSyncAt.IsZero() {
		lastSync = status.LastSyncAt.Format("2006-01-02 15:04:05 MST")
	}
	for _, job := range status.NextRuns {
		plans.WriteString("<dd><code>" + html.EscapeString(job.Name) + "</code> · " + html.EscapeString(job.Schedule) + " · " + html.EscapeString(job.Model) + " · " + html.EscapeString(job.NextRunAt.Format("2006-01-02 15:04 MST")) + "</dd>")
	}
	var accounts strings.Builder
	if status.LastRun != nil {
		for _, item := range status.LastRun.Accounts {
			quota := "未知"
			if !item.QuotaCheckedAt.IsZero() {
				quota = fmt.Sprintf("%.0f%% / %.0f%%", 100-item.FiveHour.UsedPercent, 100-item.Weekly.UsedPercent)
			}
			accounts.WriteString("<tr><td>" + html.EscapeString(item.Account) + "</td><td>" + html.EscapeString(item.QuotaStatus) + "</td><td>" + quota + "</td><td>" + html.EscapeString(item.SkipReason) + "</td><td>" + fmt.Sprintf("%d", item.Attempts24h) + "</td></tr>")
		}
	}
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Codex 每日预热</title><style>body{font-family:system-ui,sans-serif;max-width:900px;margin:40px auto;padding:0 20px;color:#17202a}section{border:1px solid #dde3ea;border-radius:14px;padding:20px;margin-bottom:18px}dt{color:#667085}dd{margin:4px 0 14px;font-weight:600}code{background:#f4f6f8;padding:2px 6px;border-radius:5px}table{border-collapse:collapse;width:100%}td,th{text-align:left;padding:8px;border-bottom:1px solid #dde3ea}</style></head><body><h1>Codex 每日预热</h1><section><dl><dt>状态</dt><dd>` + html.EscapeString(map[bool]string{true: "运行中", false: "空闲"}[status.Running]) + `</dd><dt>自动任务</dt><dd>` + html.EscapeString(map[bool]string{true: "已启用", false: "已停用"}[status.Config.AutomaticEnabled]) + `</dd><dt>dry-run</dt><dd>` + html.EscapeString(map[bool]string{true: "开启", false: "关闭"}[status.Config.DryRun]) + `</dd><dt>首次使用同步</dt><dd>` + html.EscapeString(map[bool]string{true: "已启用", false: "已停用"}[status.Config.SyncOnFirstUse]) + `</dd><dt>上次首次使用同步</dt><dd>` + html.EscapeString(lastSync) + `</dd><dt>定时任务</dt>` + plans.String() + `<dt>下次运行</dt><dd>` + html.EscapeString(next) + `</dd><dt>上次运行</dt><dd>` + html.EscapeString(last) + `</dd></dl></section><section><h2>最近逐账号检查</h2><table><tr><th>匿名账号</th><th>查询</th><th>剩余 5h / 周</th><th>结果</th><th>24h 调用</th></tr>` + accounts.String() + `</table></section><p>完整匿名化历史位于 CPA 管理 API。</p></body></html>`
}
