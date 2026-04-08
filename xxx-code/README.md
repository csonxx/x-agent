# xxx-code

`xxx-code` 是一个用 Go 实现的终端 coding agent，目标不是机械复刻 TypeScript 版 Claude Code 的全部产品表层，而是先把最核心的 agent runtime、多轮工具循环和 multi-agent 基础设施做成一个可运行、可继续演化的内核。

当前版本已经包含：

- Anthropic Messages API 适配
- 多轮 agent loop
- 自动上下文压缩与 context budget 保护
- 文件/命令权限策略
- lifecycle hooks 扩展点
- 本地工具调用
- 本地/远程 MCP 客户端与动态工具桥接（stdio / http / sse / ws）
- 主会话流式文本输出
- REPL、TUI 与单次执行模式
- HTTP daemon、远程 bridge 与 session API
- daemon 审计日志、ACL 与 per-client 速率限制
- in-process multi-agent 基础设施
- 子 agent 的 `spawn / send / cancel / wait / list`
- workflow 的 `list / get / tasks / resume`
- agent 并发上限、优先级与排队调度
- transcript、workflow 状态持久化与 `resume`
- workflow artifact manifest 与 task result artifact
- `version` / build metadata
- GoReleaser 发布配置与 GitHub release workflow
- config file、env、flags 的优先级配置体系
- trace id、request logging 与 `--log-file` 诊断输出

## 目录结构

```text
xxx-code/
  cmd/xxx-code/              CLI 入口
  internal/cli/              REPL、事件输出、自动保存
  internal/config/           配置与参数
  internal/daemon/           常驻 HTTP daemon、远程 session API
  internal/remote/           daemon bridge client、远程 REPL
  internal/engine/           核心运行时、消息模型、主循环、agent 管理
  internal/mcp/              MCP 配置加载、stdio/http/sse client、动态 tool bridge
  internal/persist/          session、agent 与 workflow 状态持久化
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
- `agent_fanout`
- `agent_send`
- `agent_cancel`
- `agent_wait`
- `agent_list`
- `workflow_list`
- `workflow_get`
- `workflow_tasks`
- `workflow_resume`
- `mcp__<server>__<tool>` 动态 MCP tools
- `list_mcp_resources`
- `list_mcp_resource_templates`
- `read_mcp_resource`
- `list_mcp_prompts`
- `get_mcp_prompt`

## 运行前准备

设置 Anthropic API Key：

```bash
export ANTHROPIC_API_KEY=...
```

## 版本信息

查看当前二进制的构建信息：

```bash
go run ./cmd/xxx-code --version
```

或者：

```bash
go run ./cmd/xxx-code version
```

发布产物会通过 ldflags 注入：

- version
- commit
- build date
- built by
- go version / platform

## 配置优先级

`xxx-code` 现在支持 4 层配置来源，优先级从低到高是：

1. 内建默认值
2. config file
3. environment variables
4. CLI flags

默认会自动发现：

```text
.xxx-code/config.json
```

也可以显式指定：

```bash
go run ./cmd/xxx-code --config /path/to/config.json
```

仓库里放了一份可直接改的模板：

- [examples/config.json](/Users/tt/goworkspace/src/x-agent/xxx-code/examples/config.json)

比较常用的环境变量有：

- `ANTHROPIC_API_KEY`
- `XXX_CODE_MODEL`
- `XXX_CODE_REMOTE_URL`
- `XXX_CODE_REMOTE_TOKEN`
- `XXX_CODE_DAEMON_TOKEN`
- `XXX_CODE_DAEMON_AUDIT_FILE`
- `XXX_CODE_DAEMON_ALLOW_MODES`
- `XXX_CODE_DAEMON_DENY_MODES`
- `XXX_CODE_DAEMON_ALLOW_SESSION_PREFIX`
- `XXX_CODE_DAEMON_DENY_SESSION_PREFIX`
- `XXX_CODE_DAEMON_RATE_LIMIT_PER_MINUTE`
- `XXX_CODE_DAEMON_RATE_LIMIT_BURST`
- `XXX_CODE_LOG_LEVEL`
- `XXX_CODE_LOG_FILE`
- `XXX_CODE_CONFIG`

比较常用的诊断 flag 有：

- `--log-level info|debug|error`
- `--debug`
- `--log-file .xxx-code/xxx-code.log`
- `--config .xxx-code/config.json`

## 交互模式

```bash
cd xxx-code
go run ./cmd/xxx-code
```

REPL 内支持：

- `:help`
- `:agents`
- `:workflows`
- `:workflow <workflow-id>`
- `:workflow-tasks <workflow-id> [status|name=<task>]`
- `:workflow-resume <workflow-id> [failed|task...]`
- `:mcp`
- `:mcp-resources [server]`
- `:mcp-resource-templates [server]`
- `:mcp-prompts [server]`
- `:mcp-read <server> <uri>`
- `:mcp-prompt <server> <name> [key=value ...]`
- `:wait <agent-id>`
- `:wait-all [agent-id ...]`
- `:send <agent-id> <prompt>`
- `:cancel <agent-id>`
- `:history [n]`
- `:audit [n]`
- `:compact`
- `:policy`
- `:hooks`
- `:save`
- `:session`
- `:quit`

如果你更想用一个更像“终端应用”的界面，而不是逐行 REPL，也可以直接开 TUI：

```bash
go run ./cmd/xxx-code --tui
```

当前 TUI 提供：

- 滚动 transcript 视图
- 流式 assistant 输出
- 底部输入框
- 侧边栏 session / agent / workflow / MCP 状态
- `Ctrl+S` 保存、`Ctrl+L` 清屏、`Ctrl+O` 开关侧边栏、`Ctrl+C` 退出

## 单次执行

```bash
go run ./cmd/xxx-code --print "分析当前目录的 Go 项目结构并给出修改建议"
```

如果你想关闭主会话的增量输出，也可以显式关掉：

```bash
go run ./cmd/xxx-code --stream=false
```

## Daemon 模式

如果你想把 `xxx-code` 当成一个常驻的远程 agent runtime，而不是只在本地 REPL 里用，可以直接启动 daemon：

```bash
go run ./cmd/xxx-code \
  --daemon \
  --listen 127.0.0.1:7331
```

如果你要把 daemon 暴露给别的机器、别的服务，建议至少打开 bearer token：

```bash
go run ./cmd/xxx-code \
  --daemon \
  --listen 127.0.0.1:7331 \
  --daemon-token dev-secret
```

如果你还希望 daemon 自带基础治理能力，可以再加上审计、ACL 和限流：

```bash
go run ./cmd/xxx-code \
  --daemon \
  --listen 127.0.0.1:7331 \
  --daemon-token dev-secret \
  --daemon-audit-file .xxx-code/daemon/audit.jsonl \
  --daemon-allow-modes sessions_read,sessions_write,turns,agents,workflows,audit \
  --daemon-allow-session-prefix team- \
  --daemon-rate-limit-per-minute 120 \
  --daemon-rate-limit-burst 20
```

这些参数的含义分别是：

- `daemon_audit_file`: 以 JSONL 方式落审计记录
- `daemon_allow_modes / daemon_deny_modes`: 控制哪些 API 面可以调
- `daemon_allow_session_prefix / daemon_deny_session_prefix`: 控制哪些 session ID 前缀可访问
- `daemon_rate_limit_per_minute / daemon_rate_limit_burst`: 控制每个 client address 的请求速率

daemon 会把远程 session 存到：

```text
.xxx-code/daemon/sessions/
```

也可以显式改目录：

```bash
go run ./cmd/xxx-code \
  --daemon \
  --daemon-dir /tmp/xxx-code-daemon
```

目前内置的是一套简单 JSON API，比较常用的入口有：

- `GET /healthz`
- `GET /v1/audit?limit=50`
- `GET /v1/sessions`
- `POST /v1/sessions`
- `GET /v1/sessions/{id}`
- `GET /v1/sessions/{id}/messages?limit=20`
- `GET /v1/sessions/{id}/audit?limit=50`
- `POST /v1/sessions/{id}/turns`
- `POST /v1/sessions/{id}/turns/stream`
- `GET /v1/sessions/{id}/policy`
- `GET /v1/sessions/{id}/hooks`
- `GET /v1/sessions/{id}/mcp`
- `GET /v1/sessions/{id}/mcp/resources?server=name`
- `GET /v1/sessions/{id}/mcp/resource-templates?server=name`
- `GET /v1/sessions/{id}/mcp/prompts?server=name`
- `POST /v1/sessions/{id}/mcp/read-resource`
- `POST /v1/sessions/{id}/mcp/get-prompt`
- `GET /v1/sessions/{id}/agents`
- `POST /v1/sessions/{id}/agents/{agent_id}/send`
- `POST /v1/sessions/{id}/agents/{agent_id}/cancel`
- `POST /v1/sessions/{id}/agents/{agent_id}/wait`
- `GET /v1/sessions/{id}/workflows`
- `GET /v1/sessions/{id}/workflows/{workflow_id}`
- `GET /v1/sessions/{id}/workflows/{workflow_id}/tasks?status=failed`
- `POST /v1/sessions/{id}/workflows/{workflow_id}/resume`

例如新建一个远程 session：

```bash
curl -s http://127.0.0.1:7331/v1/sessions -X POST
```

如果 daemon 开了 token，就把 `Authorization` 一起带上：

```bash
curl -s http://127.0.0.1:7331/v1/sessions \
  -X POST \
  -H 'Authorization: Bearer dev-secret'
```

然后驱动它跑一轮：

```bash
curl -s http://127.0.0.1:7331/v1/sessions/<id>/turns \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"分析当前目录代码结构"}'
```

这样别的服务、脚本或上层 orchestrator 就可以把 `xxx-code` 当作一个远程 agent backend 去调。

如果你想让远程 turn 也边生成边输出，可以直接调用 SSE 版本：

```bash
curl -N http://127.0.0.1:7331/v1/sessions/<id>/turns/stream \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"分析当前目录代码结构"}'
```

daemon 现在会为每个请求生成或透传：

- `X-Trace-ID`

打开 `--log-level=debug` 后，请求日志里会带上：

- trace id
- method / path
- status
- duration
- remote addr

如果开启了 `daemon_audit_file`，daemon 还会额外把这些事件追加到 JSONL audit log：

- request 完成记录
- auth failure
- ACL deny
- rate limit deny
- tool call / tool result
- policy block
- agent 生命周期事件

## Remote Bridge 模式

如果 daemon 已经跑起来，本地这个 CLI 也可以直接把它当成“远程后端”来用，而不是自己再起一个本地 provider/runtime：

```bash
go run ./cmd/xxx-code \
  --remote-url http://127.0.0.1:7331 \
  --remote-list-sessions
```

直接连到一个已有或自动创建的远程 session：

```bash
go run ./cmd/xxx-code \
  --remote-url http://127.0.0.1:7331 \
  --remote-session repo-main
```

远程 daemon 也可以直接进 TUI：

```bash
go run ./cmd/xxx-code \
  --remote-url http://127.0.0.1:7331 \
  --remote-session repo-main \
  --tui
```

如果远端 daemon 开了 token，就再加上：

```bash
go run ./cmd/xxx-code \
  --remote-url http://127.0.0.1:7331 \
  --remote-token dev-secret \
  --remote-session repo-main
```

或者单次远程执行一轮：

```bash
go run ./cmd/xxx-code \
  --remote-url http://127.0.0.1:7331 \
  --remote-session repo-main \
  --print "分析当前目录代码结构"
```

## 诊断日志

如果你想把 stderr 和调试日志一起持久化到文件：

```bash
go run ./cmd/xxx-code \
  --log-level debug \
  --log-file .xxx-code/xxx-code.log
```

daemon 模式也一样：

```bash
go run ./cmd/xxx-code \
  --daemon \
  --listen 127.0.0.1:7331 \
  --log-level debug \
  --log-file .xxx-code/daemon.log
```

## 发布

本仓库已经补了：

- [/.goreleaser.yml](/Users/tt/goworkspace/src/x-agent/.goreleaser.yml)
- [xxx-code-release.yml](/Users/tt/goworkspace/src/x-agent/.github/workflows/xxx-code-release.yml)

CI 会跑：

- `gofmt`
- `go test ./...`
- `go test -race ./...`
- `go run ./cmd/xxx-code --version`

release workflow 会在推送 `v*` tag 时生成多平台二进制、archive 和 `checksums.txt`。

远程 REPL 当前支持这些命令：

- `:help`
- `:session`
- `:history [n]`
- `:audit [n]`
- `:mcp`
- `:mcp-resources [server]`
- `:mcp-resource-templates [server]`
- `:mcp-prompts [server]`
- `:mcp-read <server> <uri>`
- `:mcp-prompt <server> <name> [key=value ...]`
- `:policy`
- `:hooks`
- `:agents`
- `:wait <agent-id>`
- `:send <agent-id> <prompt>`
- `:cancel <agent-id>`
- `:workflows`
- `:workflow <id>`
- `:workflow-tasks <id> [status|name=<task>]`
- `:workflow-resume <id> [failed|task...]`
- `:save`
- `:quit`

这一路径下，本地 CLI 不需要直接配置 `ANTHROPIC_API_KEY`，模型调用和 session 持久化都由 daemon 负责。
如果 daemon 开了 `--daemon-token`，remote bridge 会用 `--remote-token` 或环境变量 `XXX_CODE_REMOTE_TOKEN` 自动发 `Authorization: Bearer ...`。

默认情况下，`--remote-url` 会沿用 `--stream=true`，所以远程单次执行和远程 REPL 也会边收到文本边打印；如果你更想等整轮结束后再输出，可以显式关掉：

```bash
go run ./cmd/xxx-code \
  --remote-url http://127.0.0.1:7331 \
  --stream=false \
  --print "分析当前目录代码结构"
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

恢复上一次主会话、已知子 agent 和已保存 workflow：

```bash
go run ./cmd/xxx-code --resume
```

如果某个子 agent 在 session 保存时仍处于运行中，恢复后会被标记为失败，需要重新发送任务。这是当前实现为了保持状态一致性做的显式处理。

如果某个 `agent_fanout` workflow 在保存时还没跑完，恢复后会被标记成 `interrupted`，unfinished task 会回到可恢复状态。你可以用：

```text
:workflows
:workflow <workflow-id>
:workflow-tasks <workflow-id> failed
:workflow-resume <workflow-id> failed
:workflow-resume <workflow-id> planner writer
```

或者对应的工具：

- `workflow_list`
- `workflow_get`
- `workflow_tasks`
- `workflow_resume`

如果配置了默认 artifact 目录，workflow 每次状态变化还会额外写出：

```text
.xxx-code/artifacts/workflows/<workflow-id>/
  manifest.json
  01_<task-name>.json
  02_<task-name>.json
```

`manifest.json` 里会包含 workflow summary 和完整 task 列表；每个 task artifact 会记录当次 workflow 状态、task snapshot、错误信息和生成时间，方便后续排障或作为更高层 orchestrator 的结果索引。

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

## 权限策略

`xxx-code` 现在把工具权限也收进了 runtime，而不是完全放任工具自由访问本机。

默认行为：

- `read_file` / `glob` / `grep` 只允许读取工作目录及显式允许的 read roots
- `write_file` / `edit_file` 只允许写入工作目录及显式允许的 write roots
- `bash` 可以整体关闭
- `--read-only` 会直接禁止写文件类工具
- 可以按 tool 名做 allowlist / denylist
- `bash` 还可以继续细化到命令前缀 allow / deny

常见用法：

```bash
go run ./cmd/xxx-code \
  --read-only \
  --bash=false \
  --allow-read ../shared-docs,/tmp/project-cache
```

或者：

```bash
go run ./cmd/xxx-code \
  --allow-write ./generated,./reports
```

或者把权限直接收得更细：

```bash
go run ./cmd/xxx-code \
  --allow-tools read_file,glob,grep,bash \
  --deny-tools mcp__playwright__navigate \
  --allow-bash-prefix "git status,go test,go list" \
  --deny-bash-prefix "rm ,sudo "
```

这里的 `--allow-tools` / `--deny-tools` 既可以控内建 tool，也可以控动态 MCP tool；`bash` 的前缀策略则适合把“允许哪些命令族”收得更死。

REPL 里可以用 `:policy` 查看当前生效策略。

## Hooks

可以为 runtime 接 shell hooks，把 `xxx-code` 接进你自己的审计、日志、编排或外部 agent 系统里。

可用 hook：

- `--hook-before-tool`
- `--hook-after-tool`
- `--hook-after-turn`
- `--hook-agent-event`

hook 会把 JSON payload 写到命令的 stdin，同时注入这些环境变量：

- `XXX_CODE_HOOK_KIND`
- `XXX_CODE_AGENT_ID`
- `XXX_CODE_AGENT_NAME`
- `XXX_CODE_TOOL_NAME`
- `XXX_CODE_STATUS`

其中 `before_tool` hook 的命令如果非零退出，会阻止这次工具调用。

示例：

```bash
go run ./cmd/xxx-code \
  --hook-before-tool 'cat > /tmp/xxx-before-tool.json' \
  --hook-after-turn 'cat > /tmp/xxx-after-turn.json'
```

REPL 里可以用 `:hooks` 查看当前配置。

## MCP

`xxx-code` 现在会自动读取工作目录下的 `.mcp.json`，并把其中 `mcpServers` 里配置的 MCP server 动态注册成工具。目前支持四种 transport：`stdio`、`http`（streamable HTTP）、`sse` 和 `ws`。

兼容的配置形态：

```json
{
  "mcpServers": {
    "playwright": {
      "command": "npx",
      "args": ["-y", "@playwright/mcp@latest"]
    },
    "remote_docs": {
      "transport": "http",
      "url": "https://example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${TOKEN}"
      }
    },
    "legacy_sse": {
      "type": "sse",
      "url": "https://example.com/sse"
    },
    "legacy_ws": {
      "transport": "ws",
      "url": "wss://example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${TOKEN}"
      }
    }
  }
}
```

启动时会把这些远端工具映射成 `mcp__playwright__<tool>` 这种名字，所以它们会和内建 tools 一起出现在同一个 tool 集合里。

除了远端 tools，这一版还把 MCP 的资源和 prompt 能力接进来了：

- `list_mcp_resources`
- `list_mcp_resource_templates`
- `read_mcp_resource`
- `list_mcp_prompts`
- `get_mcp_prompt`

也就是说，模型现在不仅能调 MCP server 的动作，还能枚举资源、读取资源内容，以及取回 prompt 模板消息。

也可以显式指定配置文件：

```bash
go run ./cmd/xxx-code \
  --mcp-config /path/to/.mcp.json
```

REPL 里可以用 `:mcp` 查看每个 server 的连接状态、transport、URL、已注册工具和 warning。远程 server 上配置的 `headers` 会透传到每个 HTTP 请求里，方便接鉴权代理或自定义网关。

## Agent 调度

`xxx-code` 现在支持子 agent 并发上限控制。超过上限的新 agent 不会直接并发运行，而是进入 `queued` 状态，等已有 agent 释放槽位后再继续执行。

默认值：

```text
max-parallel-agents = 4
```

可以调整：

```bash
go run ./cmd/xxx-code \
  --max-parallel-agents 2
```

`agent_list` 和 `:agents` 都会显示 `queued / running / idle / failed / cancelled` 这些状态。

排队 agent 现在还支持 `priority`。当并发槽位满了以后，优先级更高的任务会先启动；同优先级下保持先进先出。`agent_spawn` 和 `agent_fanout.tasks[]` 都支持传这个字段。

示例：

```json
{
  "name": "reviewer",
  "prompt": "优先检查回归风险",
  "priority": 10,
  "background": true
}
```

## 批量编排

现在还补了两组更适合 multi-agent orchestration 的原语：

- `agent_fanout`: 一次起一批子 agent，可选 `wait=true` 直接回收整批结果
- `agent_wait`: 除了单个 `agent_id`，现在也支持 `agent_ids` 数组和 `all=true`

示例：

```json
{
  "max_parallel": 2,
  "resource_limits": {"browser": 1},
  "fail_fast": true,
  "preempt_lower_priority": true,
  "tasks": [
    {"name": "reader", "prompt": "分析 README 并提炼风险", "priority": 4, "retries": 1},
    {"name": "tester", "prompt": "检查最近改动的测试缺口", "priority": 8, "resource": "browser", "timeout_seconds": 30},
    {
      "name": "writer",
      "prompt": "基于 {{tasks.reader.result}} 和 {{tasks.tester.result}} 输出结论",
      "depends_on": ["reader", "tester"]
    }
  ],
  "wait": true
}
```

`depends_on` 会按任务名建立依赖图。前置任务成功后，下游任务才会启动；如果前置任务失败或取消，下游任务会被标记成 `skipped`，不会继续消耗 agent 槽位。为了保证这个编排过程可控，带依赖的 fanout 目前要求 `wait=true`。

下游 prompt 里还可以显式引用上游任务字段：

- `{{tasks.<name>.result}}`
- `{{tasks.<name>.status}}`
- `{{tasks.<name>.error}}`
- `{{tasks.<name>.agent_id}}`

这些引用必须同时满足两点：目标任务有 `name`，并且当前任务在 `depends_on` 里显式声明了这个依赖。执行结果里也会返回 `tasks[].resolved_prompt`，方便你调试真实下发给子 agent 的 prompt。

如果你希望单个 workflow 不要把全局 agent 槽位全吃满，可以在 `agent_fanout` 里加 `max_parallel` 做局部并发上限。再往上，如果你希望任一任务失败后尽快止损，可以加 `fail_fast=true`：

- 已经启动的 sibling task 会被取消，状态变成 `cancelled`
- 还没启动的 sibling task 会被标记成 `skipped`

每个 task 还支持两组更偏执行层的控制：

- `retries`: 失败、取消或超时后自动重试的次数
- `timeout_seconds`: 单任务超时，超时后对应 agent 会被取消，task 状态记成 `timed_out`

`fail_fast` 会等某个任务把自己的重试次数耗尽后再真正触发，所以它和 `retries` 可以组合使用。带这些执行控制的 workflow 同样要求 `wait=true`，因为需要由编排器负责调度和回收。

如果 workflow 里有某类任务不适合并发跑，还可以给 task 打上 `resource`，再用 `resource_limits` 做资源池限流。例如上面的 `"browser": 1` 就表示同一时刻最多只跑一个 `resource="browser"` 的任务；其它不在这个资源池里的任务不受影响。

如果你还希望高优先级任务能“插队”，可以加 `preempt_lower_priority=true`。这时高优先级 task 在被 `max_parallel` 或 `resource_limits` 挡住时，会尝试取消已经运行中的更低优先级 task，先让高优先级任务跑完；被抢占的低优先级 task 后面会重新排回去继续执行。执行结果里的 `tasks[].preemptions` 会记录它被抢占了多少次。

带编排控制的 `agent_fanout` 现在还会返回一个 `workflow.id`。这个 workflow 会跟着 session 一起保存，所以如果中途退出，你可以在 `--resume` 之后继续查看或恢复，而不用手工重新拼整张 DAG。

如果你只想重跑失败节点，可以在 `workflow_resume` 里传 `only_failed=true`；如果你已经知道要从哪几个节点开始，也可以传 `task_names`。runtime 会自动把这些节点的 downstream dependents 一起重置成可运行状态：

```json
{
  "workflow_id": "workflow_123",
  "only_failed": true
}
```

或者：

```json
{
  "workflow_id": "workflow_123",
  "task_names": ["planner", "writer"]
}
```

配合 `workflow_tasks` 的状态过滤：

```json
{
  "workflow_id": "workflow_123",
  "status": "failed"
}
```

上层 orchestrator 就可以先查出失败节点，再选择性地恢复，而不需要整张 DAG 从头重跑。

这意味着上层 agent 不用手工循环很多次 `agent_spawn -> agent_wait`，而是可以直接表达一轮 fan-out / join，或者一张简单的 DAG。

## Daemon 治理

daemon 审计日志默认会写到：

```text
.xxx-code/daemon/audit.jsonl
```

每一行都是一条独立 JSON 事件，适合直接配合 `jq`、`rg`、日志采集器或后续 SIEM 管道消费。现在支持两种查询入口：

- `GET /v1/audit?limit=50`
- `GET /v1/sessions/{id}/audit?limit=50`

其中 session 级 audit 更适合排一个具体任务或 workflow；全局 audit 更适合查 auth failure、ACL deny 和速率限制。

## 常用参数

```bash
go run ./cmd/xxx-code \
  --model claude-sonnet-4-5 \
  --max-turns 12 \
  --max-parallel-agents 4 \
  --tool-timeout 2m \
  --hook-timeout 30s \
  --context-budget 120000 \
  --compact-keep 12 \
  --stream=false \
  --cwd /path/to/project \
  --mcp-config /path/to/project/.mcp.json \
  --allow-read ../shared-docs \
  --allow-write ./generated \
  --allow-tools read_file,glob,grep,bash \
  --allow-bash-prefix "git status,go test" \
  --daemon-audit-file .xxx-code/daemon/audit.jsonl \
  --daemon-allow-modes sessions_read,sessions_write,turns,agents,workflows,audit \
  --daemon-rate-limit-per-minute 120 \
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

主会话在成功完成一轮后自动保存，子 agent 在 spawn / 完成时也会自动保存；workflow 状态变化时也会一起落盘。这样即便是 REPL 模式，也更接近真正可持续协作的 agent，而不是临时命令行包装器。

### 4. 长上下文管理不是完全交给外部模型

除了 session 持久化，runtime 自己也会做 context budget 管理。这样 agent 和子 agent 都能在更长时间尺度上持续工作，而不会因为 transcript 线性膨胀就很快失控。

### 5. MCP 也走统一 Tool 抽象

MCP server 并不是旁路插件系统，而是启动时桥接进同一个 registry。对模型来说，它看到的只是额外多了一批 `mcp__server__tool`，所以主循环、hooks、multi-agent 协作都不需要分叉实现。

### 6. 依赖仍然尽量轻

除 Anthropic HTTP 适配外，新增的 MCP 能力也只引入了官方 Go SDK，没有把 runtime 绑到更重的框架里，后面继续做嵌入式 multi-agent runtime 还比较顺手。

## 测试

```bash
go test ./...
```

现在它已经不只是一个“会调几个工具的 Go CLI”，而是一个具备 session、agent 生命周期、远程 API 和可恢复状态的 Go agent runtime。后面你要拿它继续做 multi-agent 编排，会顺很多。
