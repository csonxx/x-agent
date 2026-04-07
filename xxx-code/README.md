# xxx-code

`xxx-code` 是一个用 Go 实现的终端 coding agent，目标不是机械复刻 TypeScript 版 Claude Code 的全部产品表层，而是先把最核心的 agent runtime、多轮工具循环和 multi-agent 基础设施做成一个可运行、可继续演化的内核。

当前版本已经包含：

- Anthropic Messages API 适配
- 多轮 agent loop
- 自动上下文压缩与 context budget 保护
- 本地工具调用
- REPL 与单次执行模式
- in-process multi-agent 基础设施
- 子 agent 的 `spawn / send / cancel / wait / list`
- transcript 持久化与 `resume`

## 目录结构

```text
xxx-code/
  cmd/xxx-code/              CLI 入口
  internal/cli/              REPL、事件输出、自动保存
  internal/config/           配置与参数
  internal/engine/           核心运行时、消息模型、主循环、agent 管理
  internal/persist/          session 与 agent 状态持久化
  internal/provider/         模型提供方适配
  internal/tools/            内建工具
```

## 已实现的工具

- `bash`
- `read_file`
- `write_file`
- `edit_file`
- `glob`
- `grep`
- `agent_spawn`
- `agent_send`
- `agent_cancel`
- `agent_wait`
- `agent_list`

## 运行前准备

设置 Anthropic API Key：

```bash
export ANTHROPIC_API_KEY=...
```

## 交互模式

```bash
cd xxx-code
go run ./cmd/xxx-code
```

REPL 内支持：

- `:help`
- `:agents`
- `:wait <agent-id>`
- `:send <agent-id> <prompt>`
- `:cancel <agent-id>`
- `:history [n]`
- `:compact`
- `:save`
- `:session`
- `:quit`

## 单次执行

```bash
go run ./cmd/xxx-code --print "分析当前目录的 Go 项目结构并给出修改建议"
```

## Session 持久化与恢复

默认 session 文件会写到当前工作目录下：

```text
.xxx-code/session.json
```

也可以显式指定：

```bash
go run ./cmd/xxx-code --session-file /path/to/session.json
```

恢复上一次主会话和已知子 agent：

```bash
go run ./cmd/xxx-code --resume
```

如果某个子 agent 在 session 保存时仍处于运行中，恢复后会被标记为失败，需要重新发送任务。这是当前实现为了保持状态一致性做的显式处理。

## 自动上下文压缩

`xxx-code` 会在会话上下文接近预算时自动压缩较早的消息，把旧消息折叠成一条 summary，同时保留最近若干条消息原样传给模型。

默认参数：

```text
context-budget = 120000
compact-keep   = 12
```

可以调整：

```bash
go run ./cmd/xxx-code \
  --context-budget 80000 \
  --compact-keep 10
```

也可以在 REPL 里手动执行一次：

```text
:compact
```

当前的 budget 是近似 token 估算，不是 provider 返回的精确上下文计数，但已经足够拿来做稳定的长会话保护。

## 常用参数

```bash
go run ./cmd/xxx-code \
  --model claude-sonnet-4-5 \
  --max-turns 12 \
  --tool-timeout 2m \
  --context-budget 120000 \
  --compact-keep 12 \
  --cwd /path/to/project \
  --resume \
  --session-file /path/to/project/.xxx-code/session.json \
  --print "实现一个功能"
```

## 设计重点

### 1. 统一执行内核

主线程和子 agent 复用同一个 `Runner` 主循环：

- 发送 messages 给模型
- 解析 `text` / `tool_use`
- 执行工具
- 回写 `tool_result`
- 继续下一轮直到没有工具调用

### 2. Multi-agent 是真正的私有会话

`agent_spawn` 创建的是一个带独立 session 的 agent，而不是随便拼一个 prompt 分支：

- 独立消息历史
- 可选继承父会话历史
- 可以后台执行
- 可以被 `agent_send` 继续驱动
- 可以被 `agent_wait` / `agent_list` 管理
- 可以被持久化并在之后恢复

这让 `xxx-code` 更适合作为后续 Go 版 multi-agent runtime 的基础。

### 3. 自动保存优先于“一次性脚本感”

主会话在成功完成一轮后自动保存，子 agent 在 spawn / 完成时也会自动保存。这样即便是 REPL 模式，也更接近真正可持续协作的 agent，而不是临时命令行包装器。

### 4. 长上下文管理不是完全交给外部模型

除了 session 持久化，runtime 自己也会做 context budget 管理。这样 agent 和子 agent 都能在更长时间尺度上持续工作，而不会因为 transcript 线性膨胀就很快失控。

### 5. 依赖尽量轻

当前实现只使用 Go 标准库，方便你后续继续扩展、嵌入、裁剪。

## 测试

```bash
go test ./...
```

## 现在还没做的

这一版仍然刻意没有覆盖 TypeScript 版里特别重的产品层：

- 流式 UI / 增量 token 输出
- MCP 客户端
- hook 系统
- 更细粒度的权限系统
- remote agent / bridge / daemon
- 更完整的任务调度、优先级与取消传播

但现在它已经不只是一个“会调几个工具的 Go CLI”，而是一个具备 session、agent 生命周期和可恢复状态的 Go agent runtime。后面你要拿它继续做 multi-agent 编排，会顺很多。
