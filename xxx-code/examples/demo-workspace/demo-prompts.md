# Suggested Prompts

## Read The Workspace

```text
读取当前 workspace 的 brief.md，并说明它想演示哪些能力
```

## Inspect MCP

```text
列出当前 MCP 的 resources 和 prompts，并读取 demo-guide resource
```

## Use The Plugin

```text
调用 plugin__demo_helpers__emit_markdown_note，生成一份标题为 Demo Summary 的 markdown 备忘，内容总结当前 workspace 的核心扩展点
```

## Combine Everything

```text
先读取 brief.md，再读取 MCP 的 demo-guide resource，最后调用 plugin__demo_helpers__emit_markdown_note 输出一份完整的 demo 说明
```

## Workflow Example

```text
把“读取 brief.md”“读取 MCP demo-guide”“生成 markdown 总结”拆成子任务并协调执行，最后汇总结果
```
