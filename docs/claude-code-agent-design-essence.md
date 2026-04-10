# Claude Code 的 Agent 设计精髓

## 0. 这份文档想回答什么

上一篇文档讲的是 Claude Code 整体源码结构；这一篇专门聚焦一件事：

> Claude Code 的 Agent 设计，到底“高级”在什么地方。

很多系统也有：

- system prompt
- tool calling
- subagent
- workflow

但 Claude Code 读下来会明显感觉层次不一样。原因不在于它“功能更多”，而在于它在几个关键架构点上做对了。

我会把这些点拆成：

- 它试图解决的根问题
- 它的设计原则
- 关键实现落点
- 为什么这些设计成立
- 如果要在别的语言或框架里复用，应该怎么学

---

## 1. 先给结论：Claude Code 把 Agent 从“提示词对象”提升成了“运行时对象”

在很多 agent 系统里，Agent 基本等于：

- 一段 system prompt
- 一个模型
- 一组工具

Claude Code 不是这么看的。

从 `AgentTool.tsx`、`runAgent.ts`、`forkedAgent.ts`、`Task.ts`、`sessionStorage.ts` 这些文件综合看，Claude Code 里的 Agent 更像：

- 一个有独立消息链的执行单元
- 一个有工具池与权限边界的运行时实例
- 一个可以前台或后台运行的任务
- 一个可写 transcript、可恢复、可观察、可取消的线程

所以它的第一层精髓不是“子代理很强”，而是：

> 它先把 Agent 的实体边界定义清楚了。

没有这一步，后面所有高级能力都会漂浮在 prompt 层。

---

## 2. Claude Code 真正解决的，不是“怎么多问几次模型”，而是“怎么让多个执行单元长期共存”

多 Agent 系统的难点从来不是 spawn，而是下面这些现实问题：

- 父子线程共享多少上下文才合适
- 子线程能不能污染父线程状态
- 多个 agent 如何共用工具和权限体系
- 出错、取消、resume 时系统如何保持一致
- 长上下文如何不把成本打爆
- 后台 agent 如何被 UI、日志、恢复系统看见

Claude Code 的设计几乎都在围绕这些问题展开。

所以如果一定要提炼一句话：

> Claude Code 的 Agent 设计，是一套“多执行单元共存”的 runtime 设计，而不是“多角色 prompt”的设计。

---

## 3. 第一原则：统一执行内核，比“做很多 Agent”更重要

这是我认为 Claude Code 最核心的原则。

### 3.1 它没有为不同 Agent 写不同 runtime

主线程、普通子代理、fork 子代理、session memory agent，本质上最后都跑进 `query()`。

差异主要体现在输入侧：

- messages
- system prompt
- user/system context
- `ToolUseContext`
- tool pool
- permission mode
- agent 定义
- MCP client 集合

这意味着 Claude Code 选择的是：

> 一个执行内核，多种运行 profile。

### 3.2 这件事为什么重要

因为一旦所有 agent 都走同一个内核，下面这些能力就天然共享：

- compact / microcompact / reactive compact
- tool result budget
- streaming fallback
- stop hooks
- permission handling
- usage / cost / tracing
- token budget

这会带来三个直接收益：

1. 行为一致  
主线程能用的恢复与保护逻辑，子代理天然也能用。

2. 维护成本低  
复杂能力只需要在 `query.ts` 和工具运行时里实现一次。

3. specialization 更便宜  
想做新 agent，主要是改“配置面”，而不是复制一套“执行面”。

### 3.3 这也是 Claude Code 和很多框架的根本差异

很多 Agent 框架喜欢：

- 一个 planner runtime
- 一个 worker runtime
- 一个 verifier runtime

Claude Code 并没有这么做。它选择统一内核，把差异放在：

- prompt
- tool whitelist / blacklist
- model / effort / permission mode
- background / isolation

这是一种明显更成熟的架构观。

---

## 4. 第二原则：Tool 和 Task 必须分层

这是 Claude Code 非常值钱、也非常容易被忽视的设计点。

### 4.1 Tool 解决什么

在 Claude Code 里，Tool 负责：

- 单次动作
- 结构化输入输出
- 直接和一轮 query 对接
- 权限判定
- 中断行为
- 并发安全标记
- UI 呈现

它属于“当前回合里的动作”。

### 4.2 Task 解决什么

Task 负责：

- 长生命周期执行
- 后台运行
- 可取消
- 可轮询
- 可恢复
- 可通知
- 可输出到磁盘

它属于“跨回合、跨 UI、可持续存在的执行体”。

### 4.3 为什么这个分层是成熟标志

很多系统一开始喜欢把一切都做成 tool：

- `spawn_agent`
- `run_workflow`
- `background_shell`

短期很快，长期会出问题：

- 没有统一任务状态
- UI 无法追踪
- 进程结束后不可恢复
- 前后台切换困难
- 很难做通知、输出文件、resume

Claude Code 因为从一开始就把 Tool 和 Task 分开，所以：

- 主线程可以 background 成 `LocalMainSessionTask`
- 子代理可以变成 `LocalAgentTask`
- 远程任务可以变成 `RemoteAgentTask`
- workflow / monitor 也能进入同一套生命周期

这是“从 demo 走向产品”的关键一步。

---

## 5. 第三原则：默认隔离，显式共享

如果只选一个最值得学的函数，我会选 `createSubagentContext()`。

### 5.1 多 Agent 最难的题不是 spawn，而是 state isolation

多 Agent 的经典两难是：

- 共享太少：子代理没上下文、没缓存命中、没协作能力
- 共享太多：子代理互相污染，状态不可控

Claude Code 的答案不是二选一，而是：

> 默认隔离，显式共享。

### 5.2 `createSubagentContext()` 默认隔离什么

从实现看，默认会隔离或重建这些状态：

- `readFileState`
- `nestedMemoryAttachmentTriggers`
- `loadedNestedMemoryPaths`
- `dynamicSkillDirTriggers`
- `discoveredSkillNames`
- `toolDecisions`
- 新的 child `AbortController`
- 大部分 UI callback
- `setAppState`
- `setResponseLength`

这意味着子代理默认不会去动父线程的交互与局部状态。

### 5.3 它选择共享什么

只有在明确指定时，才共享：

- `shareAbortController`
- `shareSetAppState`
- `shareSetResponseLength`
- 自定义 `getAppState`

这种共享不是隐式的，而是调用者有意识地开启。

### 5.4 这套设计为什么成立

因为 Claude Code 很清楚：不同状态的风险等级不一样。

适合隔离的：

- 文件读缓存
- nested memory 跟踪
- UI 行为状态
- denial tracking

可能需要共享的：

- 权限上下文
- 某些交互式 UI 回调
- 父子取消关系

所以它不是简单的“深拷贝一份 context”，也不是“直接把 parent context 传下去”，而是做了受控组装。

### 5.5 这是可生产多 Agent 系统的必要条件

没有这一层，系统会很快出现这些问题：

- 子代理造成父线程 permission 提示错乱
- 子代理污染读缓存或替换状态
- 多条执行链互相覆盖 UI
- resume 后状态不一致

Claude Code 在这里的设计，非常接近真实操作系统里的“进程隔离 + 少量显式共享”。

---

## 6. 第四原则：prompt cache 稳定性不是优化题，而是架构题

这是 Claude Code 最少见、也最值得学的部分之一。

### 6.1 大多数系统对缓存的理解太晚

很多系统的思路是：

- 先把 agent 跑起来
- 再看看能不能做 cache

Claude Code 不是这样。它从 system prompt、tool pool、fork context 的设计开始，就在考虑 cache hit。

### 6.2 它具体怎么做

源码里至少有这些明确证据：

- `constants/prompts.ts` 里有 `SYSTEM_PROMPT_DYNAMIC_BOUNDARY`
- `splitSysPromptPrefix()` 会按 static / dynamic block 拆分缓存域
- `toolToAPISchema()` 对 tool schema 做 session-stable cache
- `assembleToolPool()` 为 built-in 与 MCP tool 做稳定排序
- `forkedAgent.ts` 定义 `CacheSafeParams`
- `createSubagentContext()` 默认克隆 `contentReplacementState`
- API beta header 会 latch，避免会话中途变动导致 cache key 抖动

### 6.3 为什么这不是“小优化”

因为一旦把 prompt cache 当成架构约束，很多设计都会跟着变：

- prompt 的分段方式
- tool pool 的排序策略
- fork 子代理时哪些状态能重建，哪些必须克隆
- 何时能动态插入 MCP 指令，何时会打爆缓存

也就是说，缓存命中率并不是后面加个 LRU 可以解决的，它会反过来塑造系统结构。

### 6.4 这点对多 Agent 尤其重要

子代理越多、context 越大，缓存收益越高。

Claude Code 的高明之处在于，它不是“让每个子代理都从零开始”，而是尽量让它们共享父会话的稳定前缀。这直接影响：

- 首 token 延迟
- 成本
- 大规模 agent fan-out 的可承受性

---

## 7. 第五原则：Agent specialization 应该通过“约束模板”来做，而不是写新框架

看看 Claude Code 的 `Explore`、`Plan`、`verification` 会发现一个很重要的思路。

### 7.1 它没有为专家 agent 造新执行器

这些 built-in agent 的差异主要是：

- prompt 偏置不同
- 工具权限不同
- 模型选择不同
- `omitClaudeMd`、`background` 之类策略不同

但它们依然走同一套 query runtime。

### 7.2 这意味着 Claude Code 对“专家 agent”的理解是正确的

专家 agent 的本质不是：

- 另一种引擎
- 另一种消息协议
- 另一种工具系统

而是：

> 在同一执行内核上施加不同的能力约束和认知偏置。

`Explore` 就是典型例子：

- 强调只读
- 禁止写文件
- 鼓励并发搜索
- 追求快速返回

`Plan` 也是一样：

- 仍然只读
- 更强调架构理解与实施路径

`verification` 则进一步体现了 Claude Code 的成熟度：

- 强制命令证据
- 强制 PASS / FAIL / PARTIAL verdict
- 默认后台执行
- 明确禁止修改项目文件

这说明 Claude Code 的“agent specialization”不是 prompt 花活，而是工程化 profile。

---

## 8. 第六原则：失败恢复必须是主路径，不是异常分支

成熟 Agent 系统和 demo 系统的差异，很多时候就体现在这里。

### 8.1 Claude Code 明显把失败恢复当成主设计目标

从 `query.ts`、`StreamingToolExecutor.ts`、`services/api/claude.ts`、`sessionStorage.ts` 这些模块看，它显式处理了很多异常路径：

- tool_use / tool_result pairing 修复
- streaming fallback 后的 tombstone
- prompt too long 后的 reactive compact
- max output tokens 恢复
- fallback model 切换
- 中断时 synthetic tool_result
- resume 后的状态重建
- legacy progress entry 桥接

### 8.2 为什么这点对 Agent 特别重要

因为 Agent 不是一次函数调用，而是一条长链路：

- 模型可能报错
- 工具可能失败
- 上下文可能超长
- 用户可能中断
- 进程可能重启

如果系统只在 happy path 下成立，那它只适合 demo，不适合长期使用。

### 8.3 Claude Code 的策略不是“避免失败”，而是“失败后还能继续”

这是一种很成熟的工程观：

- 不要求每轮都漂亮结束
- 但要求系统不要轻易进入不可恢复状态

因此它会投入很多看似“不显眼”的代码，专门保证消息链、任务状态、cache、resume 能继续工作。

---

## 9. 第七原则：可观测性与可恢复性必须写进架构，而不是事后补日志

Claude Code 的 Agent 为什么容易做成产品？一个很大原因是它几乎处处留痕。

### 9.1 它记录的不只是聊天记录

它有：

- main transcript
- sidechain transcript
- task output file
- usage / requestId / cost
- query tracking
- permission denial
- MCP resource / prompt / client state
- agent name registry

这意味着 agent 不只是“运行过”，而是“被系统记住了”。

### 9.2 这带来什么能力

有了这些结构化痕迹，系统才能：

- resume
- 做 UI 面板
- 做 background 任务通知
- 做 SDK 输出与 replay
- 做成本统计
- 做排错与产品分析

很多系统只有“当前进程内的 agent 状态”，一旦进程退出，一切都没了。Claude Code 明显不是这样。

### 9.3 这也是它能支持 background / remote / daemon 风格能力的前提

只要 Agent 运行会留下结构化状态，它就更容易被：

- UI 观察
- 远程进程恢复
- 后台任务系统接管

所以 transcript 在 Claude Code 里不是附属日志，而是 runtime substrate。

---

## 10. 第八原则：权限治理必须嵌进 Agent runtime，而不是外包给最外层

很多人设计 agent 时，会把权限做成一个外围代理层：

- 允许
- 拒绝
- 询问

Claude Code 更进一步，它把权限和 agent 运行时绑死了。

### 10.1 权限判定不是一层开关，而是上下文相关的

它和这些因素一起工作：

- permission mode
- tool 类型
- dangerous action
- settings source
- auto mode
- MCP server 匹配
- prompt/tool 决策来源

### 10.2 为什么这对 Agent 重要

因为 Agent 的行为不是预定义脚本，而是动态生成的。

如果权限系统太薄，会导致：

- 多 agent 情况下规则不一致
- prompt 注入带来的危险执行
- 自动模式无法安全扩展

Claude Code 的做法是把权限塞进 Tool 运行时和 Agent mode 里，让它天然参与：

- 工具可否执行
- 是否展示 UI prompt
- 是否进入 denial tracking
- 是否被 telemetry 记录

这使得治理成为“系统属性”，而不是“外部策略”。

---

## 11. 第九原则：Agent 定义应该是数据，而不是硬编码分支

这也是 Claude Code 非常强的一点。

### 11.1 Agent 不是 switch-case 写出来的

`loadAgentsDir.ts` 展示的是一种很清晰的数据驱动思路：

- markdown/frontmatter
- JSON config
- built-in
- plugin
- user/project/policy/flag settings

所有这些来源都能贡献 agent 定义。

### 11.2 这会带来什么

好处非常直接：

- 用户可以自定义 agent
- 项目可以带项目级 agent
- 企业或策略层可以覆盖 agent
- plugin 能引入 agent
- 内建 agent 只是默认配置的一部分

这比“在代码里再加一个特殊 agent 类型”要健康得多。

### 11.3 这和统一内核是强耦合的

正因为执行内核统一，agent 才能主要通过数据来定义。

如果每个 agent 都需要不同执行器，数据驱动就会失去意义。

---

## 12. 第十原则：memory、compact、resume 这些都应该被看成 Agent 系统的一部分

Claude Code 的另一个成熟之处，在于它不把这些能力看成附属模块。

### 12.1 长上下文问题不是“模型问题”，而是 Agent 运行时问题

如果不解决：

- 消息会无限增长
- 子代理会越来越贵
- resume 后会越来越慢

所以 Claude Code 需要：

- `microcompact`
- `autoCompact`
- `reactiveCompact`
- `contextCollapse`
- `sessionMemory`

### 12.2 为什么这和 Agent 设计是同一件事

因为 Agent 从来不是单轮调用，它会：

- 跨多个 turn 工作
- 反复使用工具
- 派生子代理
- 保持上下文

因此如果上下文治理不属于 Agent runtime，那这个 runtime 就不完整。

Claude Code 恰恰是把这些治理手段都塞进统一内核里了。

---

## 13. 可以把 Claude Code 的 Agent 设计压缩成一个公式

我会把它总结成下面这句话：

> Agent = 统一 query 内核 + 受控上下文隔离 + Tool/Task 分层 + prompt cache 稳定性 + 权限治理 + transcript / resume + 长上下文治理

少掉任何一项，系统都会明显退化。

### 没有统一 query 内核

不同 agent 的行为会越来越不一致。

### 没有上下文隔离

多 agent 很快互相污染。

### 没有 Tool/Task 分层

后台 agent 只会变成悬空 Promise。

### 没有 prompt cache 稳定性

多 agent 成本和延迟会快速失控。

### 没有权限治理

系统很难真正上线长期使用。

### 没有 transcript / resume

agent 只能活在当前进程里。

### 没有上下文治理

长会话迟早崩。

---

## 14. 如果把这些经验迁移到别的实现，该怎么学

如果你要在 Go、Rust、Python 里做自己的版本，我建议按这个顺序学。

### 第一阶段：先学“形”

先复制这些最关键结构：

- 会话控制器 + 单轮内核分层
- Tool 抽象
- Task 抽象
- transcript 持久化
- 子代理上下文构造器

### 第二阶段：再学“神”

重点把这些难点复刻出来：

- 默认隔离、显式共享
- prompt cache 稳定性
- tool pool 稳定排序
- compact / resume / sidechain
- permission mode

### 第三阶段：最后再补“产品面”

- REPL / UI
- MCP
- plugin
- remote / daemon
- built-in expert agents

很多人会先做最后一阶段，结果看起来很炫，但基础很虚。Claude Code 的源码说明顺序最好反过来。

---

## 15. 反过来看，Claude Code 避开了哪些常见反模式

读完源码后，我觉得它至少避开了下面这些常见坑。

### 15.1 避免把 Agent 设计成“递归 prompt”

它没有简单地把父 prompt 复制一份然后再问一次，而是构造新的运行时上下文。

### 15.2 避免把后台执行硬塞进 Tool

它专门做了 Task 层。

### 15.3 避免把 cache 当成透明优化

它让 cache 反过来约束架构。

### 15.4 避免把专家 agent 做成新框架

它用 profile 化约束来做 specialization。

### 15.5 避免把恢复留给“下次再说”

它从 transcript 设计时就考虑 resume。

### 15.6 避免把权限做成全局大开关

它把权限嵌进具体 runtime。

---

## 16. 我认为最值得抄的 5 个点

如果最后只留 5 个点，我会选这 5 个。

### 1. 一个 query 内核，所有 Agent 复用

这是整个系统可维护的基础。

### 2. Tool / Task 分层

这是系统能不能产品化的分水岭。

### 3. `createSubagentContext()` 这套默认隔离机制

这是多 Agent 真正能稳定工作的关键。

### 4. 把 prompt cache 稳定性写进架构

这一点极少见，但非常正确。

### 5. transcript / sidechain / resume 一体化

没有它，就不会有真正的长生命周期 Agent runtime。

---

## 17. 最后的判断

Claude Code 的 Agent 设计最精髓的地方，不是：

- 它能起很多子代理
- 它 prompt 写得很长
- 它工具很多

而是：

> 它把 Agent 当成一个有生命周期、有边界、有状态、有治理、有恢复能力的执行单元来设计。

这背后真正的设计哲学是：

- 统一内核
- 受控隔离
- 显式共享
- 长期运行
- 成本可控
- 失败可恢复

这也是为什么 Claude Code 看上去像一个“终端里的 AI 工具”，但源码读下来更像一个真正的 Agent runtime。
