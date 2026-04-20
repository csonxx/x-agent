# xxx-code Roadmap

更新时间：2026-04-20

## 目标

把 `xxx-code` 从“功能已经比较完整的 Go 版 agent runtime”继续收成一个真正适合长期使用的产品底座：

- 可持续回归
- 可稳定发布
- 可观测、可运维
- 可被插件、MCP、multi-agent 工作流持续扩展

这意味着后续重点不再是补一个全新的大功能，而是把已经完成的主干能力进一步产品化。

## 当前状态

当前主线已经具备的能力包括：

- 本地 CLI / REPL / TUI / 单次执行
- provider 抽象与多 provider 支持
- tool calling 主循环
- multi-agent 与 workflow 编排
- MCP `stdio / http / sse / ws`
- 插件目录、manifest、动态工具桥接
- daemon / remote bridge / remote TUI
- 权限、审计、ACL、速率限制、token file 热轮换
- 端到端测试、`go test -race ./...`
- 独立的 `xxx-code-stability` 长稳/soak 程序

结论上，`xxx-code` 现在已经不是 demo，而是进入“发布能力、运维能力、扩展生态和质量治理”的收尾阶段。

## 已完成基线

### 稳定性

- [x] 端到端集成测试
  - 已覆盖 daemon / remote bridge / auth / streaming / workflow / restart 主链路
- [x] 并发与恢复压测
  - 已覆盖多 session 并发、daemon restart 后 transcript 恢复、`go test -race ./...`
- [x] daemon 生命周期收紧
  - 已覆盖 active turn cancel、关闭时 agent 收敛、订阅关闭、事件背压保护
- [x] 错误模型统一
  - 已补结构化 `error/code/retryable`，remote 端统一解析 timeout / cancel / conflict / not found
- [x] 独立长稳工具
  - 已补 `cmd/xxx-code-stability`
  - 已补 restart、plugin、MCP、agent、workflow、session save、timeout 场景

### 发布能力

- [x] 基础 CI
  - 已补 `gofmt`、`go test ./...`、`go test -race ./...`、`--version`
- [x] 版本化与发布
  - 已补 `--version`
  - 已补 GoReleaser、checksums、release workflow
- [x] 配置体系
  - 已补自动发现配置、`--config`、YAML 示例模板
  - 已明确 flags > env > config file > defaults
- [x] 日志与诊断
  - 已补 `--log-level`、`--debug`、`--log-file`
  - 已补 daemon trace id 与请求日志

### Agent / Workflow

- [x] workflow 查询增强
  - 已补 workflow summary 扩展统计、`workflow_tasks`、状态/名称过滤
- [x] 更细粒度恢复
  - 已补 `workflow_resume.only_failed`
  - 已补 `workflow_resume.task_names`
  - 已支持自动把 downstream dependents 一起纳入恢复
- [x] remote / local 命面对齐
  - 已对齐本地/远程 REPL 的 workflow 查询、恢复、MCP 查询命令
- [x] workflow artifact 约定
  - 已补 `.xxx-code/artifacts/workflows/<workflow-id>/manifest.json`
  - 已补 task 级 artifact/result 索引

### 安全与治理

- [x] daemon 审计日志
  - 已记录 request / auth failure / ACL deny / rate limit / tool / policy block / agent 事件
- [x] daemon ACL
  - 已支持 API mode 与 session prefix 级访问控制
- [x] token 轮换
  - 已支持 `daemon_token_file` / `remote_token_file`
  - 已补热更新轮换与部署建议
- [x] 速率限制
  - 已补 per-client request rate limit / burst

### 生态与扩展

- [x] MCP 管理增强
  - 已补 health、reload、配置校验以及本地/远程/daemon 统一入口
- [x] provider 扩展
  - 已支持 `anthropic / openai / gpt / azure-openai / gemini / minimax / glm`
- [x] hooks 事件总线
  - 已支持 shell hook 与 JSONL event sink 同时分发
- [x] runtime 插件化
  - 已支持插件 validate / install / remove / reload、本地/远程查看与管理

## 现阶段待办

下面这些是当前最值得继续推进的事项。优先级越靠前，越偏“产品化闭环”。

## P0 发布前门禁补齐

- [x] 把 `xxx-code-stability` smoke 纳入 CI
  - 目标：每次改动都自动验证独立长稳程序至少能完整跑一轮
  - 已补 GitHub Actions 里的 `go run ./cmd/xxx-code-stability --iterations 1`
  - 已补 summary artifact 上传，便于失败后回看运行结果
- [x] 给 release 产物补 artifact smoke
  - 目标：确认 GoReleaser 打出来的二进制本身可执行
  - 已补 snapshot release 预构建与 archive 解包 smoke
  - 已在发布前执行 `xxx-code --version`、`xxx-code-stability --version`
- [x] 补安装与分发说明
  - 目标：让用户不看源码也能安装
  - 已补 README 里的 release 下载、checksum 校验、解压、PATH 安装说明

## P1 运维与交付

- [x] 补 daemon 服务化模板
  - 目标：让 daemon 更容易正式部署
  - 已补 `systemd`、`launchd`、Docker 模板与部署文档
- [x] 补 nightly soak / 长稳流水线
  - 目标：把当前的 soak 能力升级成定时回归，而不是只靠手工触发
  - 已补定时 workflow 与 summary artifact 上传
- [x] 补 provider 实网 smoke
  - 目标：在有密钥时做最小真实 API 回归
  - 已补带环境变量门控的 provider smoke workflow 与统一 smoke 脚本

## P1 可观测性

- [x] 补基础 runtime metrics
  - 目标：让 daemon 除了日志和 audit 之外，还能暴露稳定的运行指标
  - 已补 `/metrics`
  - 已补请求数、错误数、turn latency、tool latency、agent/workflow 状态计数与 Go runtime 指标
- [x] 补性能分析入口
  - 目标：让长稳和性能问题更容易定位
  - 已补 `/debug/pprof/*`
  - 已补基于 daemon token + introspection ACL 的保护
  - 已补性能调试与部署文档
- [x] 建 benchmark 基线
  - 目标：关键路径性能退化可量化
  - 已补 provider loop、workflow orchestration、daemon API benchmark

## P2 开发者生态

- [x] 补插件开发指南
  - 目标：让外部开发者清楚如何写 command plugin
  - 已补 `docs/plugin-development.md`
  - 已补 `examples/plugins/echoer`
- [x] 补 MCP 集成指南
  - 目标：让使用者更容易接自己的 MCP server
  - 已补 `docs/mcp-integration.md`
  - 已补 `examples/mcp/*.json`
- [x] 补扩展设计文档
  - 目标：为未来更通用的 multi-agent 平台化演进留下稳定边界
  - 已补 `docs/extension-architecture.md`
  - 已明确 tool/plugin/MCP/workflow 的分层约定
- [x] 补可运行 demo workspace
  - 目标：把零散示例升级成“拿下来就能走通”的最小完整工程
  - 已补 `examples/demo-workspace/`
  - 已串起 config、plugin、stdio MCP server、任务说明和可直接运行的 prompts
- [x] 补 demo workspace 脚本化 smoke
  - 目标：让示例工程不仅能看，还能被一条命令和 CI 稳定回归
  - 已补 `scripts/demo-workspace-smoke.sh`
  - 已补用户故事级 smoke 回归与 CI artifact 上传

## 推荐推进顺序

1. 先补齐 CI / release 门禁，让已有能力真正进入自动质量闭环
2. 再补 daemon 交付模板和 nightly soak，让系统更适合长期运行
3. 然后补 metrics / profiling / benchmark，让稳定性和性能都可观测
4. 最后补插件与 MCP 的开发者文档，把扩展生态收成熟

## 当前默认主线

当前默认推进顺序：

1. P2 开发者生态基线已完成
2. 继续往更多生态模板、场景化工作区和更高层 multi-agent 示例推进

## 阶段完成标准

下一阶段可以视为“更接近正式发布”的标准：

- CI 同时覆盖 `go test`、`-race` 与 `xxx-code-stability` smoke
- release 产物有最小可执行验证
- daemon 有正式部署模板
- soak 能定时跑并保留 summary
- 关键 runtime 指标可以被观测
- README 能说明安装、运行、部署、验证的完整闭环
