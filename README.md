# CPA Codex 每日预热插件

`codex-daily-prewarm` 是 CLIProxyAPI（CPA）动态插件。它可以在指定时区和 cron
时间点，或在普通 Codex 模型请求首次成功后，枚举 CPA 已加载且可用的 Codex 账号，
通过 CPA v7.3 的宿主回调向其他账号精确发送一次短模型请求。

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
      sync_on_first_use: true
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

`sync_on_first_use` 监听正常业务请求的成功用量事件。每五小时最多触发一轮，
跳过刚才已经正式使用的账号，并向其他启用且可用的 Codex 账号发送短请求。
插件自己的请求不会再次触发同步。它不会绕过 `429 usage_limit_reached`，
已经开始的五小时窗口也不会因为新的短请求重新计时。

如果需要多个独立时间段，可以用 `jobs` 取代 `schedule`。每个任务独立按天去重，
可以覆写模型和提示词。原来的单条 `schedule` 仍受支持，且旧状态文件无需迁移：

```yaml
      jobs:
        - name: default
          schedule: "0 6 * * *"
        - name: evening
          schedule: "0 18 * * *"
          model: "gpt-5.6-luna"
          prompt: "hi"
```

同一个时间段内，如果一个账号已经成功，重试该时间段时会跳过它；另一个时间段可以再次调用。
任务同时到点时会排队执行，不会并行争抢账号。`sync_on_first_use` 可以与定时任务同时启用。

`enabled` 是 CPA 的宿主保留字段，决定插件是否加载；`automatic_enabled` 只控制
每日自动预热。首次部署时保持 `enabled: true`、`automatic_enabled: false`，可在不
启动定时任务的情况下检查状态并完成手工灰度，灰度成功后再启用自动任务。

## 管理接口

- `GET /v0/resource/plugins/codex-daily-prewarm/status`
- `GET /v0/management/plugins/codex-daily-prewarm/status`
- `GET /v0/management/plugins/codex-daily-prewarm/history`
- `POST /v0/management/plugins/codex-daily-prewarm/run-now`

`run-now` 默认运行第一个任务并遵守该任务的当天去重；可以传入
`{"job":"evening"}` 选择任务，或在明确授权的手工灰度中传入 `{"force":true}`。

状态与历史只保存账号匿名指纹、任务名称、模型、返回状态、是否收到有效回复和额度窗口响应头，
不保存提示词回复正文、Token、Cookie 或认证文件内容。

## 构建

CPA `v7.3.9` 使用 Go `1.26`。Linux amd64 构建：

```bash
make test
make build GOOS=linux GOARCH=amd64 VERSION=0.2.0
```

输出为 `dist/codex-daily-prewarm.so`。
