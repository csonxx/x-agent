# xxx-code Roadmap

更新时间：2026-04-09

## 目标

把 `xxx-code` 从“已经很完整的 Go 版 agent runtime”继续收成一个可长期运行、可部署、可观测、可扩展的产品。

当前已经完成的主干包括：

- 本地 CLI、REPL、TUI、单次执行
- Anthropic provider 与主循环
- 本地工具与权限策略
- multi-agent、workflow、resume
- MCP `stdio / http / sse / ws`
- daemon、remote bridge、remote TUI
- streaming turn
- bearer auth

接下来不再以“补一个大能力缺口”为主，而是进入稳定性、发布能力、治理能力和生态能力的持续收尾阶段。

## P0 稳定性

- [x] 端到端集成测试
  - 已覆盖 daemon / remote bridge / auth / streaming / workflow / restart 的完整回归链路
- [x] 并发与恢复压测
  - 已补多 session 并发、daemon restart 后 transcript 恢复、`go test -race ./...`
- [x] daemon 生命周期收紧
  - 已补 active turn cancel、关闭时 agent 收敛、订阅关闭、事件背压保护
- [x] 错误模型统一
  - 已补结构化 `error/code/retryable`、remote 端统一解析、timeout/cancel/not found/conflict 判别

## P1 发布能力

- [x] CI 基础
  - 已补 `gofmt`、`go test ./...`、`go test -race ./...`、`--version` 回归
- [x] 版本化与发布
  - 已补 `xxx-code version` / `--version`
  - 已补 GoReleaser 配置、release workflow、checksums
- [x] 配置体系完善
  - 已补 `.xxx-code/config.json` 自动发现、`--config`、示例模板
  - 已明确 flags > env > config file > defaults
- [x] 日志与诊断
  - 已补 `--log-level`、`--debug`、`--log-file`
  - 已补 daemon trace id 与请求日志

## P1 Agent / Workflow 强化

- [x] workflow 查询与可视化增强
  - 已补 workflow summary 扩展统计、`workflow_tasks`、状态/名称过滤
- [x] 更细粒度恢复
  - 已补 `workflow_resume.only_failed`
  - 已补 `workflow_resume.task_names`
  - 会自动把 downstream dependents 一起纳入恢复
- [x] remote / local 命令面对齐
  - 已对齐本地/远程 REPL 的 workflow 查询、选择性恢复与 MCP 查询命令
- [x] workflow artifact 约定
  - 已补 `.xxx-code/artifacts/workflows/<workflow-id>/manifest.json`
  - 已补 task 级 artifact/result 索引，便于排障与上层编排消费

## P2 安全与治理

- [x] daemon 审计日志
  - 已补 request / auth failure / ACL deny / rate limit / tool / policy block / agent 事件记录
- [x] daemon ACL
  - 已补 API mode 与 session prefix 级访问控制
- [x] token 轮换与部署建议
  - 已补 `daemon_token_file` / `remote_token_file`
  - 已补热更新 token 轮换、TLS 反代、最小暴露面说明
- [x] 速率限制与资源上限
  - 已补 per-client request rate limit / burst

## P2 生态与扩展

- [x] MCP 管理增强
  - 已补 server health、reload、配置校验
  - 已补本地 REPL、remote REPL、daemon API 的统一入口
- [ ] provider 扩展
  - OpenAI / Azure / 本地模型
- [ ] hooks 向事件总线演进
  - 不只是 shell hook
- [ ] tool / runtime 插件化
  - 降低后续扩展成本

## 执行顺序

1. 补端到端集成测试与基础 CI
2. 做并发 / 恢复压测与 daemon 生命周期收敛
3. 完善版本化、发布和配置体系
4. 做审计、ACL、速率限制
5. 扩 MCP 管理与 provider 生态

## 当前阶段

当前默认推进顺序：

1. 进入 P2 生态与扩展
2. 继续做 provider 扩展
3. 再继续做 hooks / plugin 化

## 完成标准

阶段性“可发布”标准：

- `go test ./...` 稳定通过
- 关键路径有端到端集成测试
- daemon / remote / auth / streaming / workflow 有回归覆盖
- 有基本 CI
- 有明确版本、安装和运维说明
