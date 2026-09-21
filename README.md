# CPA Codex 每日预热插件

`codex-daily-prewarm` 是 CLIProxyAPI（CPA）动态插件。它在指定时区和 cron
时间点，枚举 CPA 已加载且可用的全部 Codex 账号，并通过 CPA v7.3 的宿主回调
为每个账号精确发送一次短模型请求。

插件不会读取、复制或记录 OAuth Token；请求通过 CPA 的 `host.auth.list` 与
`host.model.execute` 完成，并使用精确的 `auth_id`，不会依赖普通轮询。

## 配置示例

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    codex-daily-prewarm:
      enabled: true
      automatic_enabled: true
      schedule: "0 6 * * *"
      timezone: "Asia/Shanghai"
      model: "gpt-5.6-luna"
      prompt: "hi"
      expected_account_count: 3
      account_spacing: "30s"
      retry_count: 1
      state_path: "/CLIProxyAPI/plugins/state/codex-daily-prewarm.json"
```

标准五段 cron `10 4 * * *` 表示每天 04:10，`0 6 * * *` 表示每天 06:00。

`enabled` 是 CPA 的宿主保留字段，决定插件是否加载；`automatic_enabled` 只控制
每日自动预热。首次部署时保持 `enabled: true`、`automatic_enabled: false`，可在不
启动定时任务的情况下检查状态并完成手工灰度，灰度成功后再启用自动任务。

## 管理接口

- `GET /v0/resource/plugins/codex-daily-prewarm/status`
- `GET /v0/management/plugins/codex-daily-prewarm/status`
- `GET /v0/management/plugins/codex-daily-prewarm/history`
- `POST /v0/management/plugins/codex-daily-prewarm/run-now`

`run-now` 默认遵守当天去重；传入 `{"force":true}` 可用于明确授权的手工灰度。

状态与历史只保存账号匿名指纹、模型、返回状态、是否收到有效回复和额度窗口响应头，
不保存提示词回复正文、Token、Cookie 或认证文件内容。

## 构建

CPA `v7.3.9` 使用 Go `1.26`。Linux amd64 构建：

```bash
make test
make build GOOS=linux GOARCH=amd64 VERSION=0.1.0
```

输出为 `dist/codex-daily-prewarm.so`。
