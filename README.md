# x-agent

这个仓库当前主要包含两部分内容：

- `claude-code/`
  - Claude Code 源码快照，用来做架构分析、对照和设计参考
- `xxx-code/`
  - 一个用 Go 实现的 coding agent runtime，已经具备本地 CLI、TUI、multi-agent、workflow、MCP、daemon、remote bridge、streaming、auth 等核心能力

另外还有：

- `docs/`
  - 对 `claude-code/src` 的分析文档和 AGENT 设计提炼

## 仓库结构

```text
x-agent/
  claude-code/   Claude Code 源码快照
  docs/          Claude Code 分析与设计文档
  xxx-code/      Go 版 coding agent 实现
```

## 当前重点

当前真正持续演进的主线是 `xxx-code/`。

它现在已经完成：

- 本地 REPL / TUI / 单次执行
- 工具调用循环与上下文压缩
- multi-agent 与 workflow 编排
- MCP `stdio / http / sse / ws`
- daemon、remote bridge、remote TUI
- streaming turn
- bearer auth
- session / workflow 持久化与恢复
- 端到端集成测试基础
- 基础 CI
- P0 稳定性收口
  - daemon 生命周期、结构化错误、并发/恢复回归、`go test -race ./...`
- P1 发布能力收口
  - `version`、GoReleaser、release workflow、config file、日志诊断
- P1 Agent / Workflow 强化收口
  - `workflow_tasks`、失败节点/指定节点恢复、workflow artifact/result 索引、local/remote REPL 对齐
- P2 安全与治理收口
  - daemon audit、API mode ACL、session prefix ACL、per-client rate limiting、token rotation、部署治理说明
- P2 生态扩展第一阶段收口
  - MCP health、reload、validate，以及 local/remote/daemon 三端统一 MCP 管理接口
- P2 生态扩展第二阶段收口
  - provider 从 Anthropic-only 扩到 `anthropic / openai / azure-openai`

更完整的能力说明见：

- [xxx-code/README.md](/Users/tt/goworkspace/src/x-agent/xxx-code/README.md)
- [xxx-code/ROADMAP.md](/Users/tt/goworkspace/src/x-agent/xxx-code/ROADMAP.md)

## 剩余优化点

当前剩下的工作，已经不再是“缺一个主干功能”，而是继续把 `xxx-code` 从可运行内核收成更稳定、可发布、可治理的产品。

### P0 稳定性

- 已完成
  - 端到端集成测试矩阵
  - 并发 / 恢复压测
  - daemon 生命周期和长连接收尾
  - 错误模型和 API status 语义统一

### P1 发布能力

- 已完成
  - 版本号、发布流程、二进制分发配置
  - 配置文件与环境变量优先级
  - 日志与诊断能力

### P2 安全与治理

- 已完成
  - daemon audit、API mode ACL、session prefix ACL、per-client rate limiting
  - token rotation、TLS/反代、最小暴露面部署建议

### P2 生态扩展

- hooks 向事件总线演进
- tool / runtime 插件化

## 推荐执行顺序

1. 继续做 hooks 事件总线
2. 再做 tool/runtime 插件化

## 快速开始

如果要直接运行 Go 版 agent：

```bash
cd /Users/tt/goworkspace/src/x-agent/xxx-code
go test ./...
go run ./cmd/xxx-code
```

如果要看 Claude Code 的分析资料：

- [claude-code-src-analysis.md](/Users/tt/goworkspace/src/x-agent/docs/claude-code-src-analysis.md)
- [claude-code-agent-design-essence.md](/Users/tt/goworkspace/src/x-agent/docs/claude-code-agent-design-essence.md)
