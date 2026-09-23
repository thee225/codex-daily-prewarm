# 项目协作规则

- 本项目的用户私有 Gitea 仓库是协作基线；`https://github.com/thee225/codex-daily-prewarm` 是公开审计镜像。本机维护副本目前以 `origin` 指向 Gitea、`github` 指向 GitHub；其他副本须核对实际 remote、upstream 与分支，不能仅凭远端名称推断目标。
- 修改前遵守全局 Git 同步规则，检查工作区并联网获取 Gitea 的当前分支。提交前检查差异和凭据，公开镜像不得包含 Token、私钥、认证文件或本机运行状态。
- 完成源码变更后，先将当前分支提交并推送到已核对的 Gitea 对应分支，再显式推送同一提交到 GitHub 对应分支；分别 fetch 并核对提交一致。不使用强制推送，不自动改写当前分支的 Gitea upstream。任一远端未同步时分别报告，不把单侧成功称为双端完成。
- 生产部署配置与状态由 `/Users/tuyo/infra` 管理；源码已同步不等于生产插件已更新。
