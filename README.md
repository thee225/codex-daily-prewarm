# CPA Codex 额度巡检与预热插件

每小时巡检与重置补查的[设计方案和验收标准](docs/每小时巡检与重置补查方案.md)已实现。生产补查先以 `observe` 模式记录真实情况；后续根据真实日志决定何时启用 `active`。

插件逐账号读取 Codex 上游 `GET /backend-api/wham/usage`，按额度证据决定是否向指定账号发送一次短模型请求。它不参与 CPA 的正常轮询、权重或会话亲和。默认北京时间 05:00–23:00 每小时一个巡检槽位，整轮在整点后随机 10–60 秒启动；时间可用 `schedule` 或 `jobs` 修改，但自动巡检目前只接受 05:00–23:58 的时间。凌晨配置会在加载时明确报错，不会静默跳过。

## 决策规则

1. 通过 CPA 宿主的 `host.auth.list` 枚举账号，`host.auth.get` 在进程内取得当前凭据，`host.http.do` 发起只读上游额度查询。无需配置 CPA 管理密钥。凭据与完整响应不写入状态、日志或管理页。
2. 周额度剩余必须 **大于 5%**，上游 `allowed` 必须为真。五小时已用百分比非零时直接跳过。
3. 对五小时为 0% 的候选账号，间隔三秒再查询。只有两次查询都显示 0%、五小时重置时间随墙钟前移、周额度持续满足门槛，才判定为尚未使用。查询失败或证据不完整时跳过。
4. 每个账号至少间隔五小时才允许再预热；每个滚动 24 小时最多五次**实际模型调用**。请求发出前先持久化次数；重试、备用模型、失败及结果不确定的调用都计数。状态写入失败时停止调用。
5. `dry_run` 默认开启。它执行真实的只读额度查询并记录 `would_warm`，不发模型请求。启用真实预热前应查看运行记录。

普通业务请求和业务 `429` 不触发插件查询或预热。已使用五小时窗口与周剩余不大于 5% 的账号，若上游给出可信重置点，白天在重置后约 90 秒只读补查该账号；夜间交给次日定时巡检。`reset_followup_mode: observe` 仅记录补查时本应预热的账号，`active` 才允许补查发模型请求。

## 配置

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    codex-daily-prewarm:
      enabled: true
      automatic_enabled: true
      dry_run: true
      warm_allowlist: []
      reset_followup_mode: observe
      schedule: "0 5-23 * * *"
      timezone: "Asia/Shanghai"
      model: "gpt-6-luna"
      fallback_model: "gpt-5.6-luna"
      prompt: "hi"
      expected_account_count: 0
      unknown_quota_policy: "skip"
      account_spacing: "30s"
      retry_count: 1
      state_path: "/CLIProxyAPI/plugins/state/codex-daily-prewarm.json"
      # bark_url: "https://你的 Bark 服务地址/设备密钥"
```

`bark_url` 只能放在权限受限的生产 CPA 配置中，不能提交到 Git。插件从现有 `https://Bark服务/设备密钥` 配置中提取密钥，向同一服务的 `/push` 发送 JSON，正文中的 `%` 保持原样。管理状态只显示是否配置。定时巡检结束后汇总发一条 Bark；北京时间 23:00 至次日 08:00 只记日志。业务事件不推送。`jobs` 可代替 `schedule` 配置多个检查点，每项可包含 `name`、`schedule`、`model`、`prompt`。若前置 Worker 对已解析的 JSON 正文再次执行 URL 解码，仍需修复 Worker 才能正确处理 `%`。

灰度时将 `dry_run` 设为 `false`，并把一个状态页中的匿名账号指纹填入 `warm_allowlist`。插件仍查询全部账号，仅允许列表内的账号发模型请求。空列表表示允许所有符合条件的账号。

## 查看运行情况

- `GET /v0/resource/plugins/codex-daily-prewarm/status`：公开入口只提供中文页面壳；逐账号额度、补查与最近运行结果通过需认证的管理状态接口加载。页面提供“一键预热（先查额度）”按钮。点击后立刻查询全部账号，符合条件的账号马上发起轻量模型请求；其余账号记录跳过原因，不等待下一个定时点。手动与定时运行使用同一任务配置和 Bark 汇总规则，仅触发来源不同。按钮调用需 CPA 管理认证，面板记住管理密钥时可直接使用；未记住时页面会在点击时询问一次，且不保存输入。
- `GET /v0/management/plugins/codex-daily-prewarm/status`：状态 JSON，包含匿名账号的额度和滚动调用记录。
- `GET /v0/management/plugins/codex-daily-prewarm/history`：最近 30 次巡检。
- `POST /v0/management/plugins/codex-daily-prewarm/run-now`：手动发起一轮，仍受额度证据、dry-run 与次数保护；传入 `{"notify":true}` 可验证 Bark 汇总。

兼容旧调用中的 `force` 参数，但它不越过额度证据、五小时冷却、滚动次数和 dry-run 保护。

持久状态为 `state_path`；同目录的 `codex-daily-prewarm.events.jsonl` 记录 `run_started` 与 `run_finished`，后者包含逐账号查询结果、跳过原因、预热调用数和 Bark 接收状态。文件权限均为 `0600`。生产 audit 文件由 infra 的 logrotate 管理；写失败会留下脱敏宿主日志。

## 构建

与 CPA 宿主一致使用 Go 1.26：

```bash
make test
make build GOOS=linux GOARCH=amd64 VERSION=0.6.3
```
