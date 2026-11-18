# Gateway 数据面

## 1. 请求路径

```text
HTTP request
  -> identify Agent/profile/dialect
  -> resolve role candidate chain
  -> run route hooks
  -> rewrite model and schema
  -> run request hooks
  -> forward candidate
  -> observe first byte and result
  -> fail over when allowed
  -> audit response and stream to client
```

请求体不做整体 JSON decode/encode。Gateway 使用 byte splice 原语只修改目标字段，
保持大整数、未知键、字段顺序和原始表示。

进入模型解析之前，有几类请求在本地就答掉：`/api/hello`（Claude Code 的连通性
探针，200）、`/v1/models`（带不带 `/a/<agent>[/p/<profile>]` 前缀都一样，列档位）、
以及**没有 body 的 GET/HEAD**——它们不是模型调用（hermes 会对本机 base_url 探
`/api/tags`、`/v1/props`、`/version` 这类「你是不是 ollama / lm-studio」），一律
404，且**不计入失败数**。带 body 却缺 `model` 的请求仍是 400：那是真的形状不对。

## 2. 方言

Gateway 支持 Anthropic `/v1/messages` 与 OpenAI
`/v1/chat/completions`。Provider 可分别配置方言入口。探活和真实转发使用相同
URL 选择规则，避免“probe 绿但真实请求 404”。

## 3. Fallback 与熔断

链上每个候选（binding = provider/model）有独立健康账本。一次请求的上游收场先
过**决策表** `modules/breaker.Classify`（纯函数，输入是「实际发生了什么」，输出
是「沿不沿链走」+「记进哪本账」），数据面只如实描述事实、执行判决：

**这张表住在哪**（2026-09-18 起）：表本身、三本账的阈值、`[shape-400]` 那个字面量、
`breaker.opened` / `breaker.spared` 这些计数器，**全都在 `modules/breaker`**。数据
面只提供四个决策点和一本策略注册表（`docs/09-extension-guide.md` §7），健康表在
它自己的 `Start` 里注册进来。判据可机械核对：`modules/gateway` 的源码里不出现
"breaker"。所以下面这张表读作「**当前装着的策略**怎么判」，不是内核写死的规则
——把 breaker 摘掉，网关照常转发：全部候选可用、按机制沿不沿链、不记账、不留痕。

| 收场 | 沿链走 | 记账 |
| --- | --- | --- |
| 连接失败 / 首字节超时 / 408 / 409 / 5xx | 是 | 可用性 |
| 流中途断（已往客户端写过字节） | 否（收不回） | 可用性 |
| 429 | 是 | 限流 |
| 401 / 402 / 403 / 404 | 是 | 配置 |
| 400 命中形状判据 | 是（**不受** `fallback_on_400` 约束） | 只计数，永不摘牌 |
| 400 其它 / 其它 4xx / 3xx | 否（`fallback_on_400` 打开时 400 才走） | 不记 |
| 客户端取消 | 否（停整条链） | 不记 |
| 200 | — | 记成功 |

不变式两条：**已经开始往客户端写字节就不能换站**（写出去收不回），链上
没有下一站也没得换。任何 Kind、任何状态码下都成立，测试穷举过。

**为什么 400 一律不记在上游头上**：400 的含义就是「你这份请求不对」，而形状
检测器只是几条字符串匹配，认不出来不等于问题在上游——按认不出来的 400 摘牌，
等于让一个补丁的盲区决定摘谁。这正是 2026-09-17 那次事故的形状：
DeepSeek 的「reasoning_content 必须逐字回传」400（同一份请求换哪个 provider 都
一样错，是**请求形状**问题，不是**可用性**问题）被当成可用性失败连续记两次就
把整条 binding 摘掉，而 `newgate probe` 发的最小请求不带历史、不带
`reasoning_content`，永远探不到这条校验，于是「probe 绿、真实流量被摘」。形状
错误现在只累加计数：metrics 里的 `breaker.skipped.shape_error` 和
`newgate breaker` 的「请求形状」列。**「这家上游根本不通」不是被动流量能下的
结论**——`newgate probe` 才是权威手段（它发最小合法请求，base 错/版本错的
provider 会当场被摘掉）。

三本账各有阈值与冷却，不是一个开关：

| 账本 | 连续失败 | 首次冷却 | 退避 | 上限 |
| --- | --- | --- | --- | --- |
| 可用性 | 2 | 60s | ×2 | 10m |
| 限流 | 3 | 20s | ×2 | 2m |
| 配置 | 1 | 5m | ×1（不退避） | 5m |
| 请求形状 | 永不摘牌 | — | — | — |

理由是测出来的：429 是秒级恢复的，用可用性那套 60s×2 去摘它过重；
401/402/403/404（402 = 余额不足，与 key 无效/无此模型同族：这家确定性地接不住
这次请求）是确定性的，一次即摘，退避也没意义——重试只是在等用户去改配置。
**换账本要
重新数**：连着吃一次连接失败和一次 429，两边各一次，还不足以说明它坏了。

### 半开恢复

状态机 `closed → open → half-open → closed`：

- 同类失败累计到该账本的阈值 → `open`，`openUntil = now + cooldown`；
- `Available` 是**唯一**推进状态机的读路径。冷却期满时它把 binding 推进半开、
  发放**一次**真实请求的试探名额；半开期间其它并发请求仍然被挡在外面（严格
  一次），试探过期（45s）没回报就当它丢了，允许再放一次；
- 试探失败 → 立刻回闸，`cooldown ×= 2`（上限 10 分钟后封顶）；
- 试探成功 → 合闸，退避归位；
- 隔离期内的**真实流量**成功/失败都**不构成新证据**（它们来自更早建链的请求），
  既不提前解封、也不再数阈值或延长隔离。主动探活是唯一的例外——它的结论是
  权威的，见下。

为什么要有半开：「摘了只能靠手动 `newgate probe` 放回来」是 2026-09-17 之前的
硬伤——ark 被一次真实连接超时（`http2: timeout awaiting response headers`，那次
超时是真故障，熔断是对的）摘掉后卡了 16 分钟，直到用户手敲 `newgate probe`。
现在真实流量自己就能证明恢复。安全性由三点保住：只在冷却期满放行、半开期严格
一次、失败即退避 ×2，所以坏上游不会每分钟回来撞一次。

手动探活仍然是**立刻**改结论的手段，而且不受阈值约束：结论差就当场摘，结论好
就当场放——**隔离期内也一样**（2026-11-04 起）。那条「最短隔离」硬下限只挡真实
流量的半开试探，不挡人主动点的 probe：probe 只在用户敲命令/点测试时发生，不存在
「自动探活把抖动中的上游放回来」那种风险，而把它挡在隔离期外等于让唯一的恢复
动作变成安慰剂。

**一处已知的语义差**（既存事实，2026-09-18 记下，不在那一轮的范围内）：`newgate
tier` 这类命令走的是 `controlplane.Doc.Available()`，它是**只读**的——按 wire
快照里的 `Open` 重建闭包，**不发放半开试探名额**。这是刻意的（看一眼现状不该烧掉
一次真实试探），但要知道它和上面那条「`Available` 是唯一推进状态机的读路径」说的
**不是同一个东西**：命令行里显示「熔断中」与数据面此刻是否正好放行了一次试探，
可能差一拍。改结论的手段是 `newgate probe`，不是盯着 `tier` 看。

### 上闸前的诊断探活

连续失败数**到达阈值的那一刻**先不摘：调用注入的 verifier（由数据面实现的
主动探活，见 `Breaker.SetVerifier`），探活说它还通就**不摘**、失败计数清零，
并在快照的 `spared` 上 +1；探活也不通才照摘，理由里写明「诊断探活也不通」，
与「纯被动流量摘的」区分得开。

这是 2026-09-17 现场的直接产物：smt-deepseek 被真实流量的**首字节超时**连续
数到阈值摘掉，而每次 `newgate probe` 都是 fluent。两者结论不一致时，探活的
证据更硬——它是**主动、可控、可重复**的；被动流量是单点，受上下文尺寸和排队
影响。而摘牌的代价（用户被悄悄换成别的模型）与留着的代价不对称，所以到阈值
时再要一次主动证据。

三条边界：

- **只在到达阈值时探**，不是每次失败都探。计数清零是有意义的：下一次失败要
  重新从 1 数起，否则一条间歇性抖动的 binding 会每个请求都探活一次。
- **半开试探失败不再探**。那一发是**主动**证据（我们自己放行、结果自己收到），
  再要一次诊断只会拖长坏上游的隔离。
- **并发失败只探一次**。记录上有一个 `verifying` 标位，同时撞进阈值的失败共用
  同一次诊断结论。

verifier 为 nil（默认）时行为与以前完全一致——这一步是纯粹的加固，不是必需
依赖。

### 记账盲区

`health.json` 与 `newgate breaker` 里的行 = 账本记录 ∪ 探活结论 ∪ 延迟评分。
2026-09-17 之前少了第一项的「失败过但还没到阈值」这一半，于是「失败 1 次、闸还
没开」既看不见、重启还会丢——等于每次重启白送一次免死金牌。

Route headers 和日志记录实际 profile、链和最终 provider/model。Fallback 不得
静默发生。

### 谁让链停下来：点名 profile 与全局开关

上面那张决策表管的是「一次收场该不该换人」。还有两个**上游的事**让它根本不换：
链在进数据面之前就被**截断到链头**，于是「有没有下一站」这个问题的答案是「没有」，
决策表照常判、只是没得可判。两条路，一个截断点，措辞不同（日志里能分开验）：

| 谁 | 什么时候 | 粒度 |
| --- | --- | --- |
| `newgate <agent> --profile=xx` | launch 只在**用户点了名**时才把 `/p/<name>` 写进 base URL，代理从路径里读回来 | 这一次调用 |
| `newgate fallback off` | `state.json` 的 `ModuleConfig["gateway"].fallback` | **全局**：所有没点名 profile 的请求（网页、curl、别的 agent 都从这儿过） |

点名的语义是「这次就用这个 profile」。它**不需要**新状态——URL 路径已经把 profile
钉住了，代理原来只是没据此动手。所以点名的运行现在如实报链头自己的错，而不是悄悄
从别人那儿拿个 200——那正是最坏的形状：配置显示 glm、界面显示 glm、实际跑的是 ds。

实现上，被点名的 profile 是**唯一**参与建链的那一份（2026-11-04 起）：只在解析后
截断不够——被点名 profile 自己的候选全被摘掉时（熔断/禁用），链头已经是别人的
profile，截断留下的恰好是「静默换到别人」。收敛放在建链前，截断只负责同一份
profile 内部不换人。

**「无视所有 timeout」是推出来的，不是写的**。超时预算的守卫带着 `i > 0`，而一步
的链里 `i` 只可能是 0，所以 `budget-exhausted` 那条分支**不可能**触发；`Budget()`
与 `Timeouts` 一个字节都没动。单次尝试自己的传输超时照旧生效。

**为什么不是 `Opts.MaxSteps = 1`**：路由插件给出 override binding 时（`OverrideChain`），
那条路会先把档位链截到 1 再**前插**override 那一步，于是链又有两步、照样换人。切片
截断没有这道缝。

截断必须留痕，而且砍掉了谁要说得出来（`profile glm is pinned, dropping 1 fallback
step: ds:ds/deepseek-chat` / `fallback is off, dropping 1 chain step: glm:glm/glm-4.5-air`）。
`status` 也报 `fallback=off`：**这一行尤其要报**，因为它关掉的是「换人」这件事，
症状只在上游真挂了的时候才显形——平时请求照常 200，忘了自己拨过这个开关的人会把
「没有 fallback」读成「链上没人可换」。

## 4. Special treatment

上游或客户端怪癖通过插件注册：

- `Match` 决定适用范围；
- `Why` 解释存在原因；
- `Apply` 做 byte-level 改写并返回 notes；
- `Before`/`After` 表达具名顺序。

插件失败默认 fail-open；任何成功改写都必须输出 notes。用户可用
`newgate st` 查看并单独关闭插件。

### 短路（Responder）：让请求根本不用发

`Apply` 是**改请求**，`Respond` 是**替上游回答**。后者先于构链调用，命中就一个
字节都不发。当前的唯一用户是「裸奔」（`newgate naked`，client 侧那个用来压掉
Bash 安全分类器的插件）。它只在用户显式打开时生效，而且分两档被刻意设计得
不可能被忘记：

| 模式 | 开关 | 过期 | 可见性 |
| --- | --- | --- | --- |
| `on` | `newgate naked on` / `<时长>`（`30s`/`2m`/`1h`…） | 默认 60s | status 显示剩余时间 |
| `forever` | `newgate naked forever` | 不过期 | 每次拦下都打 `[naked]` 日志，status 持续警告 |

`newgate naked off` 随时关闭。配置存在 `state.json` 的 `classifier_naked` 字段
（读写都懒过期，daemon 不持 timer，重启不残留）。只认 Claude Code 的
security-monitor 标记，别的客户端、流式请求、非分类器后台调用一律放行。

响应头和日志都留痕：`X-Newgate-Route: naked:<插件名>`，日志打一行由插件自己写
的说明（`special.NoteProvider`）——热路径不去猜「这次判决意味着什么」，只负责
执行判决和记录。`newgate st off classifier-naked` 可以把它从插件层单独摘掉，
比改配置还快。

**为什么这不是一个静默的后门**：它等价于 Claude Code 自己的
`--dangerouslySkipPermissions`，只是发生在代理这一层、并且对用户可见（够不着
命令行的 `forever` 也一样会打日志、上 status）。自限窗口 + 永久的可见性，
是「方便」和「忘了它开着」之间的平衡点。

## 5. 观测与取证

Metrics 统计请求、链失败、failover、插件改写和 count_tokens 路径。上游 4xx 会
保存 client-sent、we-sent、upstream-said 三份证据，便于证明代理是否改变语义。
