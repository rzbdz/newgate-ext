# Reasoning continuity

## 1. 那句 400 到底在说什么（2026-09-18 实测重写）

上游原文（三种方言各一句，同一件事）：

```text
The `reasoning_content` in the thinking mode must be passed back to the API.
The `content[].thinking` in the thinking mode must be passed back to the API.
The `reasoning_text` in the thinking mode must be passed back to the API.
```

前两句是 **messages 载体**：OpenAI 方言点名 `reasoning_content`、Anthropic
方言点名 `content[].thinking`，形状都挂在 `messages[]` 上。第三句是
**Responses 方言**（点 `reasoning_text`），形状挂在顶层 `input[]` 上。三句
都实测抓到过。

**这句话不可信。** 它把「这一轮没有新指令」报成了「推理没回传」。实测（打
真实 `smt-deepseek/deepseek-flash`，每格 3/3，一手数据在发行版仓库
`newgate-ext` 的 `modules/deepseek/st-reasoning.go` 文件头与本文 §2b）结论是：

> 这条 400 的**唯一**触发条件是**尾部形状**——messages 方言看**最后一条
> `role:"user"` 消息的 `content[]` 非空、且里面全是 `tool_result` 块**，
> Responses 方言看**顶层 `input[]` 的最后一项是 `function_call_output`**。
>
> `reasoning_content` / `content[].thinking` / `reasoning_text` 在不在、是
> 什么内容，对结果**毫无影响**。

判据的完整形状（三个条件，缺一不可）。同一套三条件有两种**载体形态**：
messages（OpenAI / Anthropic 方言）挂在 `messages[]` 上，Responses 方言挂在
顶层 `input[]` 上：

| 条件 | messages 方言（OpenAI / Anthropic） | Responses 方言 |
| --- | --- | --- |
| 尾部 | 锚在**最后一条 `role:"user"` 消息**，不是数组最后一项（见 §2c 的 `role:"system"`） | 锚在**顶层 `input[]` 的最后一项**，它得是 `function_call_output` |
| 内容 | 该 `content[]` 非空且**全是** `tool_result`；出现任何别的块（`text`/`image`）就算有指令 | 最后一项**就是** `function_call_output`（Responses 没有 `content[]` 那种块层级） |
| 历史 | 那条 `tool_result` 引用的 tool call 里，**至少有一个不是这个聚合器自己产出的**（见 §1.1） | 历史里出现的 `call_id` 里，**至少有一个不是聚合器自产的**（见 §1.1） |

三条全中 → 400（messages 方言说前两句，Responses 方言说 `reasoning_text`）。
任何一条不中 → 200。

> **第三维（出身）2026-09-22 起已实现**：光有「裸尾」还不够，见下一节。
> 第 4 手在只有前两维时是「多发了几次没必要的老老实实请求」，不是错误——但
> 实测「裸尾 + 全部自产 id」本来就是 200，无条件注入等于改写了本来会成功的
> 请求。旧判据的注入面（`newgate.log.1` 里数两行：request-start 行 `← … 开始`、
> note 行 `追加一条继续指令`）：**2026-09-22 00:00–11:59** 那半天 2930 发请求里
> 973 发被注入（约三分之一）；**09-21 整天** 9077 发里 5060 发（约 56%）。现在
> 三条全中才注入，修形状从「宁可多修」变成**精确命中**。

### 1.1 第三个维度：tool call 的「出身」

聚合器（newgate 走的那家）把各家上游的 tool call id 统一改写成自己的
`call_NN_…` 形态（`NN` 是**两位序号**：一轮里并行发几个 tool call，就按 00、
01、02… 依次编号），并且**记得自己发过哪些 id**。续写那一轮它靠这份记忆把该轮
的推理（`reasoning_content` / `reasoning_text`）补回去。遇到**不是它产的** id，
它补不上，于是一路漏到 DeepSeek，DeepSeek 报那句话。

实测（`msg[….]` 位置、id 出处各一个变量，其余字节完全相同，每格 3/3）：

| 尾部 | 历史里的 tool id | 结果 |
| --- | --- | --- |
| 裸 `[tool_result]` | 全部是聚合器自产的 `call_NN_…` | **200** |
| 裸 `[tool_result]` | 混进一个别家来的 id | **400** |
| `[tool_result, text]` | 混进一个别家来的 id | **200** |
| 裸 `[tool_result]` | 聚合器自己产的、但那一轮它没思考 | 200 |

「别家来的」有几种形态，都是实际抓到的：

| id 形态 | 谁产的 | 真实抓包里出现处 |
| --- | --- | --- |
| `call_NN_XXXXXXXX…`（`NN` 两位序号） | **本聚合器** | 全部正常轮次 |
| `call_<hex>`（如 `call_faadac561213487ebeea7731`） | 聚合器背后的 OpenAI 系上游 | `err-400-req000412` 的 30 个 |
| `toolu_01XXXXXXXX…` | Anthropic 系（走 `smt-claude` 的 Claude） | `err-400-req000464` 的 70 个 |

**位置无关**：陌生 id 出现在历史任何一处都算（第一轮 foreign、最后一轮也
foreign，一样 400）。唯一的要求是它**存在**于历史里。

#### 「记得」这件事本身：2026-09-22 直接量了一遍

上面那张表测的是「id 是谁产的」，这次把**聚合器那份记忆**当作变量直接测（打真实
`smt-deepseek/deepseek-flash` 的 `/v1/responses`，每格 3/3；`input[]` 尾部都是
`[function_call, function_call_output]`）：

| 尾部的 id | 它是什么 | 结果 |
| --- | --- | --- |
| `call_00_A3cAfjy83jzydpZWE2I29764`（聚合器一分钟前刚发的） | 自产，但**只回了这一轮两个 id 里的一个** | **400** |
| `call_00_…` + `call_01_…`（同一轮的两个 id 都回，各带 output） | 自产、**成套** | **200** |
| `call_01_605TMR…`（把上一行那个自产 id 的序号从 00 改成 01，后缀一字不差） | 聚合器**没发过** | **400** |
| 成套的自产一对，但历史里**前面**还有一对 `call_<hex>` | 混着 | **400** |

四行读出来三件事：

1. **过的不是前缀，是「它记不记得这个 id」**——后缀一字不差、只把序号改一位，
   立刻 400。所以「自产形态」只是那份记忆在真实流量里的**代理**；
2. **记起来还不够，那一轮要成套**——同一轮的两个 id 只回一个，它也补不上
   （这一条下面的修复判据看不见，见文末的说明）；
3. **历史里任何一处有外来 id 就够它 400**，哪怕尾部那一轮全是自产——与上面那句
   「位置无关」是同一条，这里是复测。

真实流量里的分布（2026-09-22 数 `~/.codex/sessions` 的 rollout，`function_call` /
`function_call_output` 共 4622 条）：`call_00_` 2917、`call_<hex>` 940、
`call_01_` 705、`call_`+base64 34、`call_02_` 16、`call_03_` 8、`call_04_` 2。
所以自产那一族必须按 `call_NN_…` 认——**只认 `call_00_` 会把「一轮发了两个工具」
这种最常见的正常轮次判成外来**，于是修形状又变成「在健康链路上也动手」。

#### Responses 方言的同一张表（2026-09-22 实测）

同一套「裸尾 + 外来 id」换到 Responses 载体上重测（打真实
`smt-deepseek/deepseek-flash`，`/v1/responses`，每格 3/3）：

| 顶层 `input[]` 尾部 | 历史里的 `call_id` | 结果 |
| --- | --- | --- |
| `[function_call, function_call_output]` | 聚合器自产 | **200** |
| 同上 | 外来（`call_faedac561213487ebeea7731`） | **400**，逐字 `The \`reasoning_text\` in the thinking mode must be passed back to the API.` |
| 同上 + 末尾补一条普通 user 消息 | 外来 | **200** |
| 同上，但把 reasoning item 原样带回 | 外来 | **200** |
| 只有 `function_call_output`、缺对应的 `function_call` | 自产 | **400**，另一句文案 `No tool call found for tool output` |

最后一行是额外的一手：上游对 `input[]` 还做**结构化校验**，要能找到与
`function_call_output` 配对的 `function_call`。所以尾部只看到一个 output
还不够——第 5 手的判据因此也要求历史里存在 `call_id`（见 §2e）。

经代理端到端同样成立：同一份 body 400（响应头带
`X-Newgate-Evidence: err-400-req001840`），末尾补上那条 user 消息 → 200。

**Anthropic 方言那一格没能合成复现**：要凑出 `content[].thinking` 的裸尾得先有
真实 thinking 块，而 `stream=false` 的上游响应不返回 thinking 块，手头造不出
那份字节。这一格如实记为「未复现」，不是「复现了 200」。

### 1.2 为什么这个 400 看起来「偶尔才出现」——两个叠加的原因

绝大多数时候它不会发生，因为：

1. **Claude Code 通常不留裸尾**：它要么在 `tool_result` 后面再追加一条
   `role:"system"` 的插话（mid-turn injection：「The user sent a new message
   while you were working: …」），要么在同一轮里带上文字。这些都不是裸尾。
2. **单一上游的会话不会产生陌生 id**：只有 newgate 的 fallback 把一发请求交给
   了别家（`smt-claude` / `minimax` / `smt-codex` / ark…），那一轮产生的 id 才
   会污染会话。

两者叠加，才是「用着用着突然来一发」。朋友那边从来没遇到，通常是**没有开
fallback**——它一家的 id 始终自洽，第二条就永远不成立。

`/responses` 那一侧还更容易命中：它的尾部只要上一轮是工具调用就必然是
`function_call_output`（没有 Claude Code 那种「同轮里还带文字」的缓冲），一个
外来 id 进来之后，工具循环里的每一发都会 400——§1.4 的现场正是如此。

历史数据完全对得上：9321 条 `client-sent` 里，形态 400 共 **65 次**（那天
18:00–21:30 四小时集中出现，其余日子基本为 0），而那 65 次里两侧条件都满足的
是少数。`newgate metrics` 的 `breaker.skipped.shape_error` 只涨这个数。

### 1.3 一个真实抓包的完整因果

`dump/err-400-req000412.client-sent.json`（真实 Claude Code，764KB、229 条
消息、UA `claude-cli/2.1.273`）：

```text
msg[  1..158]  tool id 全是 call_00_…   ← 本聚合器
msg[160..225]  tool id 全是 call_<hex>  ← 别家（fallback 那一轮）
msg[227]       call_00_…                ← 又回来了
最后一条 user  = [tool_result]          ← 裸尾
                                    → 400 must be passed back
```

只把一个变量搬走（把那几轮 foreign id 删掉），尾部**仍然是裸 `[tool_result]`**，
同一个请求体立刻 200。这就是第三个维度存在的直接证据。

> **2026-09-22 追记**：`dump/err-400-req000412*` / `err-400-req000464*` 这类
> `err-400-*` 样本今天在磁盘上已经**没了**——被 dump 清理的排序 bug 整批删掉了
> （见 `docs/11-troubleshooting.md` §1 末段）。上面这段因果是从当时的记录里复述
> 的，不是现在还能翻到的文件。

### 1.4 Responses 方言的真实因果（2026-09-22 现场）

§1.3 那份抓包是 messages 方言。Responses 方言（`/responses`）是**另一个入口**，
2026-09-22 的现场把这条链完整走了一遍（newgate 日志 + 客户端 rollout 两侧的
时间戳对得上，本地时间）：

```text
15:26:46  尾部还是自产 id（call_00_ET_ymM…），正常
15:26:46→ 那一发在前三家上游连着失败（deepseek 500 / minimax 503 /
          smt-claude 503），沿链转到 smt-codex/gpt-5.6-terra，
15:27:58  200 回来，首字节 71691ms——它发的 tool call 是
          call_ggl3W7MO5ujBCBvN33cFn9rx（call_<随机> 形态，别家产的）
15:27:59  下一发带着这个 id 的 function_call_output 收尾
15:28:03  上游 400，逐字 The `reasoning_text` … must be passed back
          链上没有可换的上游（没有 (已转移)）→ **定案给客户端**，
          客户端这一轮就死在这里（rollout 紧跟着 task_complete）
```

两条读出来：第 4 手当时已经在跑了，可这一发走的是 `/responses`——**同一条形状
判据在两种方言上是两件事**，这是第 5 手（§2e）的由来；以及 `smt-codex` 这类
**Codex 原生上游也是外来 id 的来源**（`call_<随机>` 那一族），不只是
`smt-claude` / `minimax`。

这一发的证据文件（`err-400-req000589.*`）也在当天被 dump 清理的排序 bug 一起删
掉了，日志里只剩截断到 4000 字节的 body 前缀——上面这段是靠客户端 rollout 侧
的记录复盘的（`docs/11-troubleshooting.md` §1 末段记了这个 bug）。

## 2. Thinkcache

Gateway 在流式响应经过时提取真实 reasoning，并用稳定的消息/tool key 保存。
下一轮请求进入时，DeepSeek 插件在 assistant 消息上查找：

1. 客户端仍带真实 reasoning：保留；
2. cache 命中：回填真实原文；
3. cache 未命中：**一个字节都不补**（2026-09-18 起，见 §2d），跳过这条消息，
   并把「为什么没有原文」分类报进日志。

Anthropic 方言回填 `content[]` 的 thinking block，且必须排在 tool use 前。
OpenAI 方言回填 `reasoning_content`。

## 2b. 尾部形状：判据、被证伪的假设、以及一手修复

### 判据

**两套判据，别混。** §1 那张表是**诊断**判据，回答「这个 400 像不像尾部形状问题」
（三条：裸尾、锚点、那条 tool call 引用了外来 tool call）。`repairTailShape`
实现的是**修复**判据。**2026-09-22 起修复也是三条，与 §1 一一对应：**

1. **裸尾**：最后一条 `role:"user"` 的 `content[]` 非空、且**全是** `tool_result` 块；
2. **锚点**：锚在「最后一条 `role:user`」而不是 `messages[len-1]`，且那条 user 轮
   之后**没有 `assistant`**（有就不碰，见下「跳过二族」）；
3. **出身**：历史里的 tool id 至少有一个**不是聚合器自产的**（`call_NN_…` 是自产，
   `call_<hex>` / `toolu_…` 是外来）——否则不注入。

**为什么第三条不能省（2026-09-22 补上）。** 一度它被刻意不实现，账看着很硬：
修复要动的字节在尾部，而「出身」得翻整段历史、比对每个 tool id 是谁产的，代价和
收益不成比例（当时统计 3426 次尾部修复里，修完真的撞上 `must be passed back`
的是 **0 次**）。2026-09-22 的实测把这笔账推翻了：**「裸尾 + 全部自产 id」本来
就是 200**（§1.1），所以无条件注入不是「多修一次不亏」，而是**改写了本来会成功
的请求**——旧判据的注入面见 §1 那段注（09-22 那半天约三分之一，09-21 整天约
56%）。要动别人的请求字节，得先确认真的是它坏了。诊断那条留在 §1，是因为它还解释「为什么这个 400 看起来
偶尔才出现」。

这里只记**怎么测出来的**：

- 打真实上游，10 例边界矩阵，每例 3/3；
- 在一份**真实 Claude Code 抓包**上做 V1–V5 变体（V1 400、V2 400、V3 200、
  V4 200、V5 400 `missing field text`）；
- P1–P3 证明「空 `text` 块」那条检查是**逐条消息**查的，不只查尾部；
- A–I 证明 `thinking` 开关、`tools` 在不在**全都无关**；
- M1–M4 证明跨上游迁移时是**同一条规则**（外来 reasoning + 裸尾 400、
  外来 reasoning + 合规尾 200）；
- `signature` 无关；签名占位符从来不会出现在线上字节里。

### 三条被证伪的假设（别再重犯）

1. **不是「数组最后一项」**：Claude Code 会在 `tool_result` 之后追加一条
   `role:"system"` 的插话，数组最后一项是那条 system，被拒的却是前面那条
   user 轮。现场：`err-400-req000464`、`err-400-req000412`。
2. **不是「没有 text 块」**：`[tool_result, image]` 和只有 `[image]` 的尾部
   实测都 200。「全是 tool_result」才是那条线。
3. **不挂在 `thinkingOn` 上**：显式写 `thinking:{"type":"disabled"}`、连
   `tools` 都不带，同一条校验照样触发。

### 一手修复（`repairTailShape`）

命中判据时，往那个 `user` 轮的 `content[]` 末尾追加**一条最简指令**
（`toolLoopRebasePrompt = "continue"`，见 §2d 的措辞说明）。

- **只改「一族」**：那条 user 轮就是数组末尾（最多后面跟一条 `role:"system"`）。
- **跳过「二族」**：裸 `tool_result` 之后还有 `assistant` 的，**故意不碰**。
  实测（用聚合器**真实产出**的 assistant 消息，不是手编的）往那个 user 轮追加
  指令 **3/3 还是 400**。改不好就别改——往用户的对话历史里塞一句模型看不见效果
  的噪音，比 400 更糟。真实客户端也到不了这个形状。
- **不是挂在 `Match` 上的可选项**：它跟着插件走，`newgate st off deepseek`
  可以整手摘掉。

### 曾被删掉一次（记下来）

2026-09-18 当天这一手被**整手删掉**过，理由是它的判据是「请求形状」而不是
「上游真的报了这个错」，加上注入的是一句**英文长句**，会跟着请求进上游、进
对话历史——用户报的是措辞（原话：「就算以后要用，也只用最少字」）。

删掉之后错得很隐蔽：不再有那条 note，日志里也看不到 400——因为上游 400 之后
**自动沿链转移**，客户端拿到的是下一个 provider 的 200。**「看起来没坏」实际是
「每一发都悄悄降级到别的模型」**，而且形状判据认领的那份 `[shape-400]` 证据也
一起没了。

复现路径（下次别再靠猜）——把 `dump/` 里任意一份 Claude Code 的 `client-sent`
原样 POST 到 `/p/ds/v1/messages`，日志里就会出现：

```text
-> 400 ... must be passed back
→ 沿链下一步: minimax          ← 紧接着就转移了，客户端拿到 200
```

**结论：判据是对的，措辞是错的。只改措辞，别删判据。**

### 现状的量

恢复当天（22:18 起）的窗口：**1153 条请求里命中 397 次**。这个比例说明「裸尾」
在真实工作流里**很常见**（Bash 工具轮就是这个形状），不是稀有边角。第 9 章
的 e2e 就是锁这件事：尾部从 `tool_result` 变成 `tool_result,text`，上游收到
的那一句逐字是 `continue`。

> **2026-09-22 的修正**：上面那个「命中」是**只有前两维**时的命中。补上出身
> 这一维之后，命中面收窄到「裸尾 **且** 历史里有外来 id」——旧判据会把「裸尾」
> 全注入（§1 那段注：09-22 那半天 2930 发里 973 发；其中多少本来就 200，按 §1.1
> 那份 id 分布看是多数）。现在只有真会 400 的才动。

## 2c. Claude Code 会往 `messages` 里插 `role:"system"`

这不是边角知识，它是 §2b 判据里「锚最后一条 `role:user`」的直接原因，也是
**未来任何按「数组最后一项」写判据的补丁都会踩的坑**。

Claude Code 的用户中途插话（mid-turn injection）会以 `role:"system"` 的身份
出现在 `messages` 数组里，正文形如：

```text
The user sent a new message while you were working: …
```

上游**不认** system 里的指令——它只看最后一条 user。所以：

- 尾部锚点必须是「最后一条 `role:"user"`」，不是 `messages[len-1]`；
- 写 `repairTailShape` 那个下标是因为这个（`rewrite.AppendArrayItemArrayAt`
  按下标插，而不是 `AppendLastArrayItemArray`）。

## 2d. 不该编，不该长

两条都是 2026-09-18 定下来的硬契约，各有实测依据。

### 不编：cache 未命中就跳过，不写占位符

原来 cache 未命中时补一个非空占位符（先是中文长句，后来缩成
`"No thinking in this round"`）。**那整件事是错的**：

上游自己那一轮就没给过推理，那「must be passed back」要求回传的东西
**根本不存在**，我们凭什么替它编一个？编出来的字会进上游、进对话历史、
每轮烧 token，而信息量是零。

实测依据（这才是关键）：`reasoning_content` 的形态对那个 400 **毫无影响**
（真实原文 / 省略字段 / 空串 / 占位符，四格全一样）。也就是说**占位符一个
400 也没救回来**，只是纯粹的编造 + 烧 token。

顺带被推翻的还有官方文档口径——「历史里每条 assistant 都得带非空推理」在
这个上游上**不成立**：两条 assistant 都缺、尾部干净，实测 3/3 是 200。

全库 3494 条历史占位符，逐条拿 tool id 反查「记下本轮推理内容」——**一条都
没命中**，说明这些推理上游从来没给过我们，不是缓存弄丢的。

正确动作：**跳过这条消息**，并把原因分类报进日志（`notool` / `nocache` /
`nokey`，定义见 `skipCause`）。

### 不长：注入的指令只有一个词

`toolLoopRebasePrompt` 从一句英文长句改成一个词，2026-09-22 的定稿值是
`"continue"`（在那之前是中文 `"继续"`）。

它会被注入的请求、**也会进用户下一轮的对话历史**，越长越像「有人在替我
说话」。用户的原话：「prompt 注入也尽可能用最简单的、不影响流程的，比如
（"继续"）这种」。

**这一个词不走 i18n，而且永远不该走**：它被 `json.Marshal` 之后写进**请求体
的字节**，是发给上游的协议数据，不是给谁看的文案。翻译它等于改变发出去的
内容（上游对这段文本没有语义要求，但用户的历史里会长住这段字）。它同时是
包级 `const`，而包初始化早于装语言。

必须诚实说清：起作用的是「尾部多了一条**普通用户指令**」这个事实本身，不是
那句话的内容——上游要的是「这一轮有新指令」，不是一个解释。实测同一个请求体
只加一个 `" "`（空格）就 200。

但**空串不行**：`[tool_result, text""]` 会被上游以 `missing field text` 拒掉
（它的解析层把空串等同于字段缺席），而且那条检查是**逐条消息**查的，不只查
尾部。

## 2e. 第 5 手：Responses 方言的尾部形状修复（2026-09-22）

DeepSeek 的 Responses 方言端点（顶层 `input[]`，报错说 `reasoning_text`）是同
一套毛病换了载体。第 5 手修它，开关点 `deepseek.tail-shape-responses`。

### 判据（三条，与 §1 诊断表同源）

1. **裸尾**：顶层 `input[]` 的最后一项是 `function_call_output`；
2. **锚点**：就是最后一项本身——Responses 没有 `messages` 那层
   `role:"system"` 插话，不用像第 4 手那样往前退一条；
3. **出身**：历史里存在**非聚合器自产**的 `call_id`（`call_NN_…` 自产，
   `call_<hex>` / `toolu_…` 外来）——否则不碰。

### 动作

往 `input[]` 末尾追加一项普通用户消息，逐字字节是：

```json
{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
```

`continue` 就是 §2d 那条「一个词」的契约，同样是发给上游的协议字节、不走 i18n。

### 为什么「出身」这一维不能省

Responses 方言在这一点上比 messages 方言更容易看清楚：**「裸尾 + 全部自产
`call_id`」实测本来就是 200**（§1.1 那张表第一行）。无条件注入等于把一份上游
愿意接受的请求改写掉——而且上游对 `input[]` 还做**结构化校验**（缺配对的
`function_call` 会报 `No tool call found for tool output`），改写是在人家的
语法树上动刀。09-21/09-22 那批无条件注入（§1 那段注）全发生在第 4 手（messages
方言）上——`/responses` 这一侧当时一处都没修，既没被污染，也一直裸着撞 400
（§1.4 那发现场）。出身这一维在两种方言上都要，判据刻意保持一致。

### Anthropic 方言没复现（如实记）

第 4/5 手覆盖的是「尾部形状」这一根因，三家方言理论上是同一件事。但
Anthropic 方言（`content[].thinking`）那一格**没能合成复现**：要凑出
「裸 `[tool_result]` 尾」得先有真实 thinking 块，而 `stream=false` 的上游响应
不返回 thinking 块，手头造不出那份字节。记为「未复现」，不假装复现过。

**出身判据的近似性（如实记）**：第 4/5 手只能看见 body 里有什么，看不见
「聚合器记的那一轮是不是成套」（上一节第 2 条）。真实客户端发整段历史、一轮的
id 不会拆开发，所以「全部自产 ⇒ 能过」在实测里成立；但它不是物理定律——真出现
反例时，第 4/5 手会**少补一次**（后果是 400；沿链转移的那一发会被 `[shape-400]`
记下来，不再无声），而这条判据无法自查。取舍写在这里，不藏在代码里。

**为什么先接受这条近似（2026-09-22 的决定记录）**：那天复盘了线上唯一一发 400
（§1.4），它是「历史里有外来 id」那一类——正是判据覆盖的那一类，不是这一条。
要真正消掉这条近似，得把聚合器「那一轮发过哪几个 id」单独记一份（thinkcache 每
个 key 只挂自己那一条，要按值反查出成套是脆的），随之而来的是新状态、新失效模式
（按会话分桶、淘汰、重启后装回来）与「记岔了反而多补一次」的代价。实测还没遇到反例之前
不加：真遇到时先有的是证据（`[shape-400]` + `dump/shape-400-deepseek/`），再谈
要不要加这份记忆。

字节层面由零 token 的 `mock/e2e_deepseek_tail_origin.sh` 锁（真二进制 + 内核假
上游，断言的是**我们发出去的字节**：第 4 手补在哪、第 5 手补在哪、全自产时一个
字节不动、开关点真的停手）。它不模拟聚合器那份记忆，所以上游的 200/400 由本文
两张表的实测数据背书，不由那条脚本断言。


## 2f. Codex Responses 的嵌套 `reasoning.effort`（2026-09-22）

前面的 `reasoning_effort` 是 messages/OpenAI 旧路径的**顶层**字段。Codex 0.155.1
走 Responses 方言时，客户端自己的设置 `model_reasoning_effort`（位于
`~/.codex/config.toml`）会变成顶层 `reasoning` 对象里的 `effort`。这两种字段不能
混为一谈：嵌套值优先，body 同时带顶层 `reasoning_effort` 和坏的嵌套值时，仍按嵌套值
判定并返回 400。

真实上游 `smt-glm/glm-5.3` 的测量（2026-09-22，Responses `/v1/responses`，每格
3/3）如下：

| `reasoning` 形状 | `effort` | 结果 |
| --- | --- | --- |
| 对象 | `low` / `high` / `max` | **200** |
| 对象 | `medium` / `minimal` / `none` | **400**，始终思考，不支持关闭思考 |
| 缺席、`{"summary":"auto"}`、`{}`、`"effort":null` | 无可用值 | **200** |

这个 Responses 规则**不以 tools 为条件**：带 tools 和不带 tools 都一样。不要把它和
较早的 Anthropic/聚合器故事（「带 tools、没有显式顶层强度」）推广成所有方言的
判据。

`always-thinks` 只在 quirk 表已经标记该 `(provider, model)` 时改写，把被拒的
`reasoning.effort` 改成 `low`；对象里的 `summary` 和其余字节保持不动，并在 notes
中报告改写。客户端自己的 `model_reasoning_effort` 属于「档位之外的旋钮」，Codex
接管目前不管理它；给始终思考的上游时把它设成 `low`、`high` 或 `max`，通常设
`low` 最接近「少想一点」的原意。

quirk 有三个来源：转发时撞上的 4xx、**事件流里报的那次失败**（见 §2i）、以及
`newgate probe`；后两者都会进能力缓存。因此 daemon 刚重启、缓存缺失或首次遇到新
模型时，**第一发坏值仍可能先被上游拒一次**；随后才会知道该模型始终思考并改写。
若请求使用 `stream: true`，上游可能以 HTTP 200 发回一条 SSE `response.failed`，
Codex 会把它显示成 `stream disconnected before completion: …`。这不是连接故障，
而是请求里的 nested effort 被拒绝。

## 2g. Thinkcache 观测 Responses 方言（2026-09-23）

Codex 那一整条路走 `/v1/responses`，而上面 §2 那两段判据当初只写了另两种方言。
三个症状一个根：**这条线上的请求与响应，我们的观测器一个都要不认**。

**一、增量的形状。** `sseChunk` 把 `delta` 声明成对象，而 Responses 的推理增量是
`{"type":"response.reasoning_text.delta","delta":"…"}`——`delta` 是个**裸字符串**。
于是那个流的每一个 chunk 反序列化都整块失败、被计进 badChunks 丢掉：一轮 Codex
下来一个推理字节都没记下，而日志上它和「上游真没给推理」长得一模一样（Wire 那行
只会报一堆坏块）。现在 `delta` 是 `json.RawMessage`，各方言各自解自己的形状。

**二、「未闭合的 tool loop」在 responses 里不算数。** `ContinuationOrigin` 只认
Anthropic 的 `content[]` 与 OpenAI 尾部连续的 `role:"tool"`；Responses 把对话挂在
顶层 `input[]`、**根本没有 `messages`**。于是一个 Codex 的 tool loop 在这条判据眼里
压根不算工具循环——那一轮 tool call 是哪家产的**从来没被记过**，`forward` 也就不
会去调 `special.RebaseToolLoop`，中途切到 DeepSeek 的那一发带着别家产的 tool 状态
直接上去，上游 400（§4）。补的那一支与 OpenAI 那支同形：从 `input[]` 末尾往回走
连续的 `function_call_output`，中间夹了别的项就说明这一轮已经闭合。

**三、同一段推理来两遍。** 这条线上推理文本有两条来路——增量事件，以及
`output_item.done` 里那份**完整**原文。两条都收，下一轮补回去的就是重复内容，而上游
要的是逐字原文。所以按 item id 记「这个 item 吐过增量没有」：吐过就以增量为准。
`response.completed` 里那份 `response.output` 只在**整条流一个推理字节都没收到**时
兜底。`summary_text` 有意不认——它是给人看的摘要，不是上游要求回传的那份原文
（§2d「不编」）。

## 2h. 学到的上游毛病活过重启（2026-09-23）

`quirk` 表**只在内存里**（`modules/gateway/quirk/quirk.go` 文件头：落进
`providers.json` 就是在悄悄改用户的配置文件）。而补丁的判据只认这张表，于是
「重启之后第一发」重新撞一次同一个 400——2026-09-22 用户报的
`stream disconnected before completion` 正是这一形态：必现，过一会儿自己好。

落盘那份（`probe-capabilities.json`）以前**只有 `newgate probe` 会写**，而
Codex 那条路上的失败是「HTTP 200 + 事件流里一条 `response.failed`」，`Learn`
只看 `status >= 400`，永远学不到。两条加起来就是那个窗口。

现在转发路上学到的也落盘，两个时刻都算：

| 时机 | 为什么是这一刻 |
| --- | --- |
| **交棒之前**（`handleControlUpgrade`，flush 在 `SpawnHandoff`/`AdoptRuntime` 之前） | 新进程的 `Start` 已经跑完、已经从盘上读过了。落盘晚一步，这一笔要等**下一个**新进程才生效——也就是「换版后的第一发」照样撞 400（实测踩过） |
| **排空之后**（交接路径的 drain 完成处） | 排空期在途的那几发可能又学到新的，而换版路径上只有这一处会叫它 |
| **停机时**（`Shutdown`） | 收到信号／控制端点停机那条路，与交接那条**完全不重叠** |

写出去的是**并集**（`probe.MergeQuirks`），不是覆盖：同一条目标上，方言那两位
来自一次真探活，而这里并进去的是转发撞出来的毛病位，谁都不该抹掉对方。只增不减
是唯一安全的写法——少记一位的后果是重启后多撞一次 400（可恢复），多抹一位的
后果是这个补丁从此不生效（不可恢复，且没有症状指向它）。

## 2i. 上游把失败塞在**流里**时，我们照样学得到（2026-09-23）

§2f 那条实测表里最坑的一行是：`stream: true` 时上游**先 200 开流**，再把拒绝塞进
事件流（`response.failed`）。于是数据面那条 `learnQuirks`（判据是 `status >= 400`）
在 Codex 这条路上**一次都不会触发**——表永远是空的，`always-thinks` 的 Match 永远
不绿，用户看到的是「必现、过一会儿自己好」（「好」是因为恰好有人 `probe` 过那一家，
那是运气，不是机制）。

现在这条失败由**观测者**报给数据面：`thinkcache.Observer` 本来就在读这条流
（§2g），`response.failed` 到它就记下来，流结束时 `forward` 问一次
（`learnStreamFailure`），把那份**原文**按「上游拒了这一发」交给 `quirk.Learn`。
传进去的状态码是构造出来的 400——4xx 表达的是「上游拒了你的请求形状」（与 5xx 的
「上游自己挂了」相对），而这个失败**就是**这一族，只是它没走状态码；真正重要的是
原文，它是签名的匹配输入。

三处刻意的取舍：

- **观测者不做 egress。** 它一开始被写成实现 `special.Egressor` 去想认领这条流，
  那是错的：`special.ClaimEgress` 里只有一个认领者能赢，观测者赢了就等于把真正的
  响应改写挤掉。而它本来就能拿到上游的**原字节**（`Observer.Write`），一个认领
  都不需要。
- **`Observer.StreamFailure` 只记第一条**，且 HTTP 200 也照报——「上游拒了这一发」
  这件事本身比那句话重要：取不到原文时留一句兜底的，不能当没发生。
- **`ContinuationOrigin` 那条路上没有信箱。** 曾经设计过一份「上游刚在流里报过
  失败」的内存信箱（`RecordStreamFailure`/`TakeStreamFailure`），想给「预测下一发
  会不会被拒」用。实测下来没必要：`quirk.Default.Learn` 在流结束那一瞬就标上了，
  下一发的 Match 直接绿——多一层信箱只多一处能在重启时出错的形状。

## 3. 冷层

内存 cache 支持快速查找，`thinkcache.bin` 保存重启后的冷层。冷层损坏时必须报告，
不能伪装成成功恢复。

## 4. 跨 provider tool loop

不同 provider 的 reasoning 状态不一定兼容。优先选择能安全继续当前 tool loop 的
候选；只有安全候选失败后，模型族插件才可执行显式有损 rebase。Rebase 原因和动作
写入日志与 metrics。

**这不是通用限制**（2026-09-16 对同一份真实 thinking + tool_use 做的 A/B）：

| 迁移 | 结果 |
| --- | --- |
| Ark → Ark | 3/3 200 |
| **Ark → DeepSeek** | **3/3 400 must be passed back** |
| DeepSeek → Ark | 3/3 200 |
| DeepSeek → DeepSeek | 3/3 200 |

API 是无状态的；不兼容的是请求里携带的 reasoning/tool 编码。只拦**迁入**
DeepSeek，别把这个上游怪癖扩大成所有 provider 都失去 fallback。

**而 §1.1 给了这件事一个更完整的解释**：迁移本身不是问题，问题是迁移之后
历史里留下了一批**别家产的 tool id**，聚合器在续写那一轮认不出它们。所以
`RebaseToolLoop` 的动作（保留工具结果、追加一条普通用户继续指令）之所以有效，
和 §2b 是一回事：把那一轮变成「有指令的新回合」，绕开「续写」这条路径。

## 5. 不允许的做法

- 不写空串：上游的解析层把空串等同于字段缺席，报 `missing field text`；
- 不凭空伪造「真实推理」：cache 未命中就**跳过**，占位符那条路已删（§2d）；
- 注入的指令不写长句：它进对话历史，越长越像替用户说话（§2d）；
- **不无条件改写本来会成功的请求**：「裸尾 + 全部自产 id」是 200，出身这一维
  不能省（§2b / §2e）——动请求字节前先确认真的是它坏了；
- 不把 provider 特例散落在 forward 热路径；
- **不要按上游的报错文案判据**：这句话的文案与真实原因不一致（§1），
  按文案写会两边都错——修不好该修的，还会误伤不该碰的。
