# CPA Codex 额度窗口检查插件

`codex-daily-prewarm` 是 CLIProxyAPI（CPA）动态插件。它在指定时间点检查每个
Codex 账号的已观察到的五小时重置时间，或在某账号首次进入新窗口后，向尚未进入
窗口的其他可用账号精确发送一次短模型请求。默认检查点为北京时间 05:00、10:00、
15:00、20:00；检查点不是无条件发请求。

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
      schedule: "0 5,10,15,20 * * *"
      timezone: "Asia/Shanghai"
      model: "gpt-6-luna"
      fallback_model: "gpt-5.6-luna"
      prompt: "hi"
      expected_account_count: 0
      account_spacing: "30s"
      retry_count: 1
      state_path: "/CLIProxyAPI/plugins/state/codex-daily-prewarm.json"
```

`expected_account_count: 0` 动态发现可用账号，不把当前的三个账号写死；明确设为
非零值时才启用数量保护。标准五段 cron `0 5,10,15,20 * * *` 表示每天四次检查。

`sync_on_first_use` 监听正常业务请求的成功用量事件。它按来源账号和实际响应头中的
重置时间去重，跳过刚才正式使用的账号；其他账号若已有未到期的窗口，也不会被
重复测试。若响应头没有重置时间，插件只作五小时保守冷却，不称其为已确认窗口。
插件自己的请求不会再次触发同步。`429 usage_limit_reached` 不会因切换模型而重试；
若上游提供重置时间，插件会在到期前跳过该账号。周额度耗尽账号可能被 CPA 标为
不可用而不进入可用名册；它恢复后会自动参与后续检查点。

主模型默认 `gpt-6-luna`。只有上游明确返回不支持模型的错误，才尝试
`gpt-5.6-luna`；普通错误、额度不足或结果不确定时不会回退。成功收到回复并不
证明这次请求“重置”了一个已开启的窗口；应查看历史记录中的实际
`primary_reset_at`，以观察账号之间的时间差。

如果需要多个独立时间段，可以用 `jobs` 取代 `schedule`。每个任务独立按天去重，
可以覆写模型和提示词。原来的单条 `schedule` 仍受支持，且旧状态文件无需迁移：

```yaml
      jobs:
        - name: default
          schedule: "0 5 * * *"
        - name: evening
          schedule: "0 20 * * *"
          model: "gpt-6-luna"
          prompt: "hi"
```

多个任务也共用逐账号窗口状态，不会因为任务名称不同而重复调用已进入窗口的账号。
任务同时到点时会排队执行，不会并行争抢账号。`sync_on_first_use` 可以与定时任务同时启用。

`enabled` 是 CPA 的宿主保留字段，决定插件是否加载；`automatic_enabled` 只控制
每日自动预热。首次部署时保持 `enabled: true`、`automatic_enabled: false`，可在不
启动定时任务的情况下检查状态并完成手工灰度，灰度成功后再启用自动任务。

## 管理接口

- `GET /v0/resource/plugins/codex-daily-prewarm/status`
- `GET /v0/management/plugins/codex-daily-prewarm/status`
- `GET /v0/management/plugins/codex-daily-prewarm/history`
- `POST /v0/management/plugins/codex-daily-prewarm/run-now`

`run-now` 默认运行第一个任务并遵守逐账号窗口判断；可以传入
`{"job":"evening"}` 选择任务，或在明确授权的手工灰度中传入 `{"force":true}`。

状态与历史只保存账号匿名指纹、任务名称、模型、返回状态、跳过原因、是否收到有效回复和额度窗口响应头，
不保存提示词回复正文、Token、Cookie 或认证文件内容。

## 构建

CPA `v7.3.9` 使用 Go `1.26`。Linux amd64 构建：

```bash
make test
make build GOOS=linux GOARCH=amd64 VERSION=0.3.0
```

输出为 `dist/codex-daily-prewarm.so`。
