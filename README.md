# x-agent

`x-agent` 是一个围绕“终端原生 coding agent”展开的研究与实现仓库。

这个仓库里同时放了两类东西：

- 一份 `Claude Code` 源码快照与分析资料，用来做架构参考和设计提炼
- 一个正在持续演进的 Go 版 agent runtime：`xxx-code`

如果你把它理解成“源码研究 + Go 实现 + 设计沉淀”三部分合在一起的工作区，会比较准确。

## 仓库里有什么

### `claude-code/`

`Claude Code` 的源码快照。

它在这个仓库里的角色不是“直接继续开发的主线”，而是：

- 架构对照样本
- 交互与 agent runtime 设计参考
- 多 agent、工具、上下文管理、权限等能力的灵感来源

### `docs/`

对 `claude-code/src` 的分析与提炼文档：

- [claude-code-src-analysis.md](./docs/claude-code-src-analysis.md)
- [claude-code-agent-design-essence.md](./docs/claude-code-agent-design-essence.md)

这部分适合你先建立认知，再回头看源码，而不是一上来就陷进完整快照里。

### `xxx-code/`

一个用 Go 实现的终端原生 coding agent runtime，也是当前仓库里真正持续演进的主线。

它已经不是简单的“模型 API + 几个工具”，而是一整套可运行系统，包含：

- 本地 CLI / REPL / TUI
- 单次执行模式
- daemon / remote bridge / remote TUI
- 多 provider
- tool-calling 主循环
- multi-agent 与 workflow
- MCP 与插件扩展
- 权限、审计、恢复、配置治理

完整说明见：

- [xxx-code/README.md](./xxx-code/README.md)
- [xxx-code/ROADMAP.md](./xxx-code/ROADMAP.md)

## 三部分之间的关系

可以把这个仓库理解成下面这条链路：

```text
Claude Code 源码快照
        ↓
源码分析与设计提炼
        ↓
Go 版 agent runtime 实现（xxx-code）
```

也就是说：

- `claude-code/` 提供“参考对象”
- `docs/` 提供“抽象与总结”
- `xxx-code/` 提供“工程化落地”

## 当前主线：xxx-code

目前仓库的核心产出是 `xxx-code`。

它现在已经完成的主干能力包括：

- 本地 REPL、TUI、单次执行
- 流式输出与自动上下文压缩
- 文件工具、bash、统一 tool registry
- sub-agent、fanout workflow、resume
- MCP `stdio / http / sse / ws`
- 插件目录、manifest 与动态工具桥接
- daemon、remote bridge、remote TUI
- bearer auth、ACL、audit、rate limit
- YAML 配置、环境变量、flags 优先级体系
- provider 扩展：
  - `anthropic`
  - `openai`
  - `gpt`
  - `azure-openai`
  - `gemini`
  - `minimax`
  - `glm`

按现在的状态看，`xxx-code` 已经不是“功能还不全的 demo”，而是一套可本地使用、可远程部署、可继续扩展成更大 multi-agent 系统的底座。

## 仓库结构

```text
x-agent/
  README.md                  仓库入口说明
  claude-code/               Claude Code 源码快照
  docs/                      Claude Code 源码分析与设计提炼
  xxx-code/                  Go 版 coding agent runtime
    README.md                xxx-code 完整功能与架构说明
    ROADMAP.md               xxx-code 路线图
    docs/                    补充部署文档
    deploy/                  systemd / launchd / Docker 部署模板
    examples/                YAML 配置模板与 .env 示例
    cmd/xxx-code/            程序入口
    cmd/xxx-code-stability/  长稳/soak 测试入口
    internal/                运行时核心实现
```

## 你应该从哪里开始看

### 如果你想理解 Claude Code 的设计

建议顺序：

1. 先读 [claude-code-agent-design-essence.md](./docs/claude-code-agent-design-essence.md)
2. 再读 [claude-code-src-analysis.md](./docs/claude-code-src-analysis.md)
3. 最后按文档里的主链路去看 `claude-code/` 源码

这样不会一开始就被完整源码淹没。

### 如果你想直接使用 Go 版 agent

建议顺序：

1. 先看 [xxx-code/README.md](./xxx-code/README.md)
2. 用 `examples/` 里的 YAML 模板配一个最小配置
3. 先跑本地 REPL 或 `--print`
4. 再看 daemon / remote / MCP / plugin 这些增强能力

### 如果你想继续开发这个仓库

建议顺序：

1. 看 [xxx-code/README.md](./xxx-code/README.md) 了解当前能力边界
2. 看 [xxx-code/ROADMAP.md](./xxx-code/ROADMAP.md) 理解项目阶段与演进方向
3. 从 `xxx-code/cmd/xxx-code/main.go` 开始，顺着入口往 `internal/cli`、`internal/daemon`、`internal/engine` 看

## 快速开始

### 安装发布版

如果你不想从源码运行，可以直接从发布页下载二进制：

- [x-agent Releases](https://github.com/csonxx/x-agent/releases)

完整安装说明见：

- [xxx-code/README.md](./xxx-code/README.md)

### 运行 xxx-code

进入项目目录：

```bash
cd /Users/tt/goworkspace/src/x-agent/xxx-code
```

设置一个 provider key，例如默认 Anthropic：

```bash
export ANTHROPIC_API_KEY=your-key
```

启动本地 REPL：

```bash
go run ./cmd/xxx-code
```

或者直接做一次性执行：

```bash
go run ./cmd/xxx-code --print "分析当前目录项目结构"
```

查看版本：

```bash
go run ./cmd/xxx-code --version
```

### 跑测试

```bash
cd /Users/tt/goworkspace/src/x-agent/xxx-code
go test ./...
go test -race ./...
```

如果想跑一轮不依赖真实模型 key 的稳定性回归：

```bash
cd /Users/tt/goworkspace/src/x-agent/xxx-code
go run ./cmd/xxx-code-stability --iterations 1
```

## 推荐阅读入口

最适合从根目录开始点开的几份文档是：

- [xxx-code/README.md](./xxx-code/README.md)
- [xxx-code/ROADMAP.md](./xxx-code/ROADMAP.md)
- [docs/claude-code-agent-design-essence.md](./docs/claude-code-agent-design-essence.md)
- [docs/claude-code-src-analysis.md](./docs/claude-code-src-analysis.md)

## 当前状态

当前仓库的状态可以概括成三句话：

- `claude-code/` 是研究样本，不是主开发线
- `docs/` 已经沉淀出相对清晰的设计理解
- `xxx-code/` 已经完成主干能力，接下来更偏向生态、体验和更高层的 agent 编排演进

如果你只打算先看一个部分，优先看 `xxx-code/`。  
如果你更关心“这些设计从哪里来”，再回到 `docs/` 和 `claude-code/`。
