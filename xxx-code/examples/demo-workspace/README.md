# xxx-code Demo Workspace

这个目录是一个“能直接拿来跑”的 `xxx-code` 示例工作区。

它把几类能力放在同一个最小案例里：

- `config.yaml`
- `.mcp.json`
- 本地 plugin
- 本地 stdio MCP server
- workflow / multi-agent 示例 prompt

如果你想快速理解 `xxx-code` 的扩展能力怎么在一个真实 workspace 里组合，这个目录就是最短路径。

## 目录结构

```text
demo-workspace/
  .env.example
  .mcp.json
  config.yaml
  brief.md
  demo-prompts.md
  mcp-demo-server/
    main.go
  .xxx-code/
    plugins/
      demo_helpers/
        plugin.json
        emit_markdown_note.go
```

## 启动方式

前提：

- 本机已安装 Go
- 已设置任意一个可用 provider 的 API key

在 `xxx-code/` 仓库根目录执行：

```bash
export ANTHROPIC_API_KEY=your-key
go run ./cmd/xxx-code --config ./examples/demo-workspace/config.yaml
```

为什么只需要这一条命令：

- `config.yaml` 会把工作目录切到当前 demo workspace
- `.mcp.json` 会在该 workspace 下自动生效
- plugin 目录也已经指向 workspace 自己的 `.xxx-code/plugins`

如果你想用别的 provider，也可以先看：

- `examples/demo-workspace/.env.example`

## 快速 smoke

如果你只是想确认这个 demo workspace 的配置、plugin、MCP 和集成链路都还能正常工作，而不是立刻连真实模型，可以在 `xxx-code/` 仓库根目录执行：

```bash
bash ./scripts/demo-workspace-smoke.sh
```

它会把 smoke 日志与摘要写到：

```text
.artifacts/demo-workspace-smoke/
```

## 这个 demo 里有什么

### plugin

提供一个 tool：

```text
plugin__demo_helpers__emit_markdown_note
```

它接受结构化 JSON 输入，输出一段格式化的 Markdown 说明。

### MCP

workspace 自带一个 stdio MCP server：

```text
examples/demo-workspace/mcp-demo-server
```

它会暴露：

- 一个 tool：`mcp__demo__echo_text`
- 一个 resource：`memory://demo-guide`
- 一个 prompt：`review_demo`

### workflow

你可以直接让 agent 把任务拆成多个子步骤，比如：

- 一路读取 workspace 文件
- 一路读取 MCP resource / prompt
- 最后再用 plugin 生成整理后的说明

## 推荐体验顺序

### 1. 先看本地文件

你可以先让它：

```text
读取当前 workspace 里的 brief.md，总结这个 demo 想演示什么
```

### 2. 再用 MCP

然后让它：

```text
列出 MCP 暴露的 resources 和 prompts，并读取 demo-guide
```

### 3. 最后用 plugin

再让它：

```text
调用 plugin__demo_helpers__emit_markdown_note，把这个 demo 的扩展点整理成一份 markdown 备忘
```

### 4. 再试 workflow

最后可以测试编排面：

```text
把“读取 brief.md”“读取 MCP demo-guide”“生成 markdown 总结”拆成并行或分阶段子任务，最后汇总一个结论
```

## 调试建议

如果你怀疑某一层没接起来，可以按这个顺序查：

1. `:plugins`
2. `:mcp`
3. `:mcp-health`
4. `:mcp-resources`
5. `:mcp-prompts`

如果 plugin 和 MCP 都正常显示，说明 demo workspace 的扩展接线基本是好的。
