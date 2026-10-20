# CPA Codex 额度巡检与预热插件

每小时巡检与重置补查的[设计方案和验收标准](docs/每小时巡检与重置补查方案.md)已实现。生产补查目前使用 `active`：可信重置点后的补查只有在额度和冷却条件全部通过时才发送预热请求。

插件逐账号读取 Codex 上游 `GET /backend-api/wham/usage`，按额度证据决定是否向指定账号发送一次短模型请求。它不参与 CPA 的正常轮询、权重或会话亲和。默认北京时间 05:00–23:00 每小时一个巡检槽位，整轮在整点后随机 10–60 秒启动；时间可用 `schedule` 或 `jobs` 修改，但自动巡检目前只接受 05:00–23:58 的时间。凌晨配置会在加载时明确报错，不会静默跳过。

## 决策规则

1. 通过 CPA 宿主的 `host.auth.list` 枚举账号，`host.auth.get` 在进程内取得当前凭据，`host.http.do` 发起只读上游额度查询。无需配置 CPA 管理密钥。凭据与完整响应不写入状态、日志或管理页。
2. 周额度剩余必须 **大于 5%**，上游 `allowed` 必须为真。五小时已用百分比非零时直接跳过。
3. 对五小时为 0% 的候选账号，先完成账号间隔，再使用新鲜额度证据间隔三秒确认；轮次首查超过十秒时重新查询。只有两次查询都显示 0%、五小时重置时间随墙钟前移、周额度持续满足门槛，才判定为尚未使用。查询失败或证据不完整时跳过。
4. 每个账号至少间隔五小时才允许再预热；每个滚动 24 小时最多五次**实际模型调用**。请求发出前先持久化次数；重试、备用模型、失败及结果不确定的调用都计数。关闭信号若在持久化后、发出请求前到达，保留这次保守预留。状态写入失败时停止调用。
5. `dry_run` 默认开启。它执行真实的只读额度查询并记录 `would_warm`，不发模型请求。启用真实预热前应查看运行记录。

普通业务请求和业务 `429` 不触发插件查询或预热。已使用五小时窗口与周剩余不大于 5% 的账号，若上游给出可信重置点，白天在重置后约 90 秒只读补查该账号；夜间交给次日定时巡检。`reset_followup_mode: observe` 仅记录补查时本应预热的账号，`active` 才允许补查发模型请求。

重载或关闭时停止接纳新任务，并在账号间和等待期间尽快退出；已发出的同步宿主调用会等到完成。被中断的定时槽位或单账号补查会持久化为待处理；仍在有效时段的任务恢复后重新核对额度，过期任务交给下一轮巡检兜底。已持久化的调用次数和冷却仍防止重复预热。若正在使用的状态文件损坏，插件保持关闭，避免在调用次数未知时继续发模型请求。

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
      digest_time: "22:30"
      quiet_start: "23:00"
      quiet_end: "08:00"
      unknown_failure_streak: 3
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

`bark_url` 只能放在权限受限的生产 CPA 配置中，不能提交到 Git。插件从现有 `https://Bark服务/设备密钥` 配置中提取密钥，向同一服务的 `/push` 发送 JSON，正文中的 `%` 保持原样。管理状态只显示是否配置。从 v0.7.0 起，成功不再即时推送，通知规则如下：

- 每天 `digest_time`（默认 22:30，使用 `timezone`）发送一条 `passive` 静默日报；统计前一天同一时刻至本次时刻的半开窗口 `[起点, 终点)`。按本轮结束入账时间归属，22:30 整及之后、23 点和凌晨运行计入下一份日报。统计全部触发来源的轮数、有效回复次数、失败、逐账号最后一次确认的周剩余额度、失败恢复情况和按原因码汇总的跳过次数；零运行也发存活日报。
- 已实际尝试预热但最终没有有效回复时，以 `timeSensitive` 即时告警；同一账号连续 `unknown_failure_streak` 轮（默认 3）额度未知或查询失败，也归为 `quota_unknown` 告警。一次 unknown、周额度不足、滚动 24h 上限、闲置未确认等只进日报。unknown 达到门槛后的每轮继续累计失败，额度恢复为已知后重置连续计数；某账号之后又成功预热时，日报标记已恢复。
- 每个日报窗口内，同一匿名账号、同一错误类别最多尝试即时告警一次（请求失败统一为 `request_failed`，具体错误码只作正文详情；额度未知为 `quota_unknown`），失败投递也不反复推送。`quiet_start`–`quiet_end`（默认 23:00–08:00）的自动失败告警持久化暂存，在结束免打扰后合并一条 `timeSensitive`；跨重启仍保留队列和去重。手动 `notify: true` 始终即时发一条 `active` 结果反馈，包括失败、跳过和零成功，不受免打扰限制，也计入日报；手动反馈中的失败占用相同窗口的告警去重额度。
- 日报计数、窗口游标、发送结果、unknown 连续次数和夜间队列写入原 state 文件。插件启动后补发已错过的日报和已到期夜间队列，成功后不重复；日报投递失败最多尝试 3 次、间隔 5 分钟，耗尽后记录放弃并推进窗口。发送前先持久化投递占用；若崩溃留下无法确认的投递结果，标记不确定并放弃该次，避免重复。旧 state 缺字段时按空初始化，不追算旧历史；首次启用从下一个日报边界开始，升级首份日报可能不足整窗；多日离线只补发当前到期窗口一次，跳过中间已过期窗口并清理其夜间队列，随后等待下一个日报边界。手动运行也会清理结束时间早于当前 7 天的窗口及其夜间队列。自动预热关闭但已配置 Bark 时，通知时钟仍运行。

三个时间配置均使用 `HH:MM`，`quiet_start` 与 `quiet_end` 不能相同；`unknown_failure_streak` 接受 1–100。日报的失败数采用上述告警口径（含去重掉的失败），与运行历史里包含单次查询失败的 `failed_accounts` 口径不同。`bark_status` 表示本轮结果：常见值为 `deferred_to_digest`、`failure_accepted`、`failure_deduplicated`、`failure_queued_quiet_hours` 和 `manual_accepted`。认证状态接口及状态页展示最近日报发送时间、结果和下一日报边界，通知审计写入同目录 events.jsonl；公开页面壳不包含逐账号详情。

`jobs` 可代替 `schedule` 配置多个检查点，每项可包含 `name`、`schedule`、`model`、`prompt`。若前置 Worker 对已解析的 JSON 正文再次执行 URL 解码，仍需修复 Worker 才能正确处理 `%`。

灰度时将 `dry_run` 设为 `false`，并把一个状态页中的匿名账号指纹填入 `warm_allowlist`。插件仍查询全部账号，仅允许列表内的账号发模型请求。空列表表示允许所有符合条件的账号。

## 查看运行情况

- `GET /v0/resource/plugins/codex-daily-prewarm/status`：公开入口只提供中文页面壳和全局模型名；逐账号额度、补查与最近运行结果通过需认证的管理状态接口加载。面板选择“记住密钥”后，插件页从同源的 `cli-proxy-auth` 登录存储中读取密钥并自动显示详情；未选择时浏览器不持久化密钥，页面无法自动取得面板内存中的登录态，可点击“验证并查看详情”只读加载，输入仅用于本页、不保存。页面另有“一键预热”按钮，立刻查询全部账号额度并按现有条件尝试预热；完成后即时发一条结果 Bark。按钮调用始终需要 CPA 管理认证。
- `GET /v0/management/plugins/codex-daily-prewarm/status`：状态 JSON，包含匿名账号的额度和滚动调用记录。
- `GET /v0/management/plugins/codex-daily-prewarm/history`：最近 30 次巡检。
- `POST /v0/management/plugins/codex-daily-prewarm/run-now`：手动发起一轮，仍受额度证据、dry-run 与次数保护；传入 `{"notify":true}` 即时发送结果 Bark（包括无成功、失败或跳过）。

兼容旧调用中的 `force` 参数，但它不越过额度证据、五小时冷却、滚动次数和 dry-run 保护。

从 v0.6.10 起，`would_warm_accounts` 表示通过额度、灰度及冷却门槛的账号数，真实、dry-run 和 observe 模式使用同一口径。`skipped_accounts` 只统计策略跳过，`failed_accounts` 统计查询或执行失败；查询失败、状态写入失败会使整轮 `success=false`，页面显示错误及“额度未知”。历史旧记录保留原值，不追溯重算。模型调用记录保留 `quota_before` 作为调用前证据，响应提供的新额度、重置时间及实际备用模型写入调用结果；响应没有额度时继续保留最近可信观测。

持久状态为 `state_path`；同目录的 `codex-daily-prewarm.events.jsonl` 记录 `run_started` 与 `run_finished`，后者包含逐账号查询结果、跳过原因、预热调用数和 Bark 接收状态。文件权限均为 `0600`。生产 audit 文件由 infra 的 logrotate 管理；写失败会留下脱敏宿主日志。

## 构建

与 CPA 宿主一致使用 Go 1.26：

```bash
make test
make build GOOS=linux GOARCH=amd64 VERSION=0.7.0
```

页面状态回归检查（需要 Node.js）：`node tests/status-page.mjs`。Go 整轮执行测试使用模拟宿主，不访问生产服务、不消耗模型额度。
