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

更完整的能力说明见：

- [xxx-code/README.md](/Users/tt/goworkspace/src/x-agent/xxx-code/README.md)
- [xxx-code/ROADMAP.md](/Users/tt/goworkspace/src/x-agent/xxx-code/ROADMAP.md)

## 剩余优化点

当前剩下的工作，已经不再是“缺一个主干功能”，而是继续把 `xxx-code` 从可运行内核收成更稳定、可发布、可治理的产品。

### P0 稳定性

- 补更厚的端到端集成测试矩阵
- 做并发 / 恢复压测
- 收紧 daemon 生命周期和长连接收尾
- 统一错误模型和 API status 语义

### P1 发布能力

- 完善 CI
- 增加版本号、发布流程、二进制分发
- 完善配置文件与环境变量体系
- 增加日志和诊断能力

### P1 Agent / Workflow 强化

- 更强的 workflow 查询和失败诊断
- 单 task 重跑、从失败节点继续
- 继续收敛 remote / local 命令面对齐
- 建立 workflow artifact/result 约定

### P2 安全与治理

- daemon 审计日志
- daemon ACL
- token 轮换与部署建议
- 速率限制和资源上限

### P2 生态扩展

- MCP 管理增强
- provider 扩展
- hooks 向事件总线演进
- tool / runtime 插件化

## 推荐执行顺序

1. 继续补测试、压测和 daemon 稳定性
2. 完善版本化、发布和配置体系
3. 增强 workflow 恢复、结果管理和查询能力
4. 做审计、ACL、速率限制
5. 扩 MCP 管理和 provider 生态

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
