# 故障排查

## 1. reasoning 回传 400（三种方言）

**先别读这句话的字面意思。** 上游那句——OpenAI 方言
`The \`reasoning_content\` … must be passed back to the API.`、Anthropic 方言
`The \`content[].thinking\` …`、Responses 方言 `The \`reasoning_text\` …`——是
**误报**：它把「这一轮没有新指令」报成了「推理没回传」。真实判据有三条（缺一
不可），完整推导在 `docs/06-reasoning.md` §1，那里是唯一出处：

1. **裸尾**：`messages` 方言看最后一条 `role:"user"` 消息的 `content[]` 非空、
   且**全是** `tool_result`；`Responses` 方言看顶层 `input[]` 的最后一项是不是
   `function_call_output`；
2. **锚点**：是「最后一条 `role:user`」不是数组最后一项——Claude Code 会在
   `tool_result` 后面插一条 `role:"system"` 的 mid-turn 消息（Responses 没有
   这一层，锚点就是 `input[len-1]`）；
3. **出身**：那条 `tool_result` / `function_call_output` 引用的 tool call 里，
   **至少有一个不是本聚合器产的**（`call_NN_…` 是它产的——`NN` 是两位序号，
   一轮里并行的几个 tool call 就是 00/01/02…；`call_<hex>`、`toolu_…` 是别家来
   的）——否则是 200，不是 400。

第三种方言补一句：`reasoning_text` 走的是 Responses 端点，插件按同一套判据认领
（第 5 手，开关 `deepseek.tail-shape-responses`，见 `docs/06-reasoning.md` §2e）。

### 排查顺序

1. 日志里有没有 `[shape-400] … 判据 deepseek`——有就是这类，往下走；
2. 看 `dump/shape-400-deepseek/req-*/client-sent.json`：
   - `messages` 方言：把 `messages` 里 `tool_use` 的 id 列出来，看**有没有不
     是 `call_NN_`（`call_` + 两位序号 + `_`）形态的**（一条就够）；再看最后一条
     user 的 `content[]` 是不是只有 `tool_result`；
   - `Responses` 方言：看顶层 `input[]` 最后一项是不是 `function_call_output`，
     并把 `call_id` 列出来，看**有没有不是 `call_NN_` 形态的**；
3. 三条都中 → 这条 400 是**必然**的，且它说明这一发之前的某一轮被 fallback
   交给了别家上游。想看那一轮是谁：日志里往前找 `(已转移)` /
   `X-Newgate-Chain`。

**为什么会「偶尔」出现**：绝大多数轮次不满足，因为 Claude Code 通常不留裸尾
（它会补一条 `role:"system"` 插话，或在同一轮里带文字），而只有 fallback 才会
产生别家的 id。两者叠加才是「用着用着突然来一发」。**没开 fallback 的会话
永远不会遇到**——id 始终自洽，第三条永远不成立。

### 这两类 400 都不该摘牌（2026-09-17 起）

它是请求形状问题，同一份 body 换个 provider 一样错，跟 provider「能不能用」
无关。`newgate breaker` 只显示真正的可用性熔断；要看这类在不在发生，看
`newgate metrics` 的 `breaker.skipped.shape_error`（涨就是预期）。

### 插件做了什么、没做什么

- **做（messages 方言，第 4 手）**：三条全中（裸 `tool_result` 收尾、那条 user
  轮之后没有 assistant、历史里有外来 tool id）时，往它的 `content[]` 追加
  **一条最简指令**（`"continue"`）。实测 400 → 200，每格 3/3。
- **做（Responses 方言，第 5 手）**：`input[]` 最后一项是
  `function_call_output` 且历史里有外来 `call_id` 时，往 `input[]` 末尾追加一条
  普通 user 消息。开关点 `deepseek.tail-shape-responses`。
- **出身这一维不能省**：裸尾 + 全部自产 id 本来就是 200，无条件注入改写的
  是本来会成功的请求（旧判据的注入面：09-22 那半天 2930 发里 973 发被注入，约
  1/3，全在 messages 方言；09-21 整天是 56%——见 `docs/06-reasoning.md` §1 那段
  注与 §2b / §2e）。
- **不做**：cache 未命中时**不编占位符**（上游自己没给过的推理，编了也只是烧
  token——实测对这条 400 毫无影响）；「二族」（裸尾之后还有 assistant）
  **故意不碰**——实测往那个 user 轮追加指令 **3/3 还是 400**，塞一句模型看不见
  效果的噪音比 400 更糟。

### 曾经删掉过一次（别再删）

2026-09-18 这一手被整手删过，理由是判据「不看上游是否真的报错」。删掉之后错得
很隐蔽：**上游 400 之后自动沿链转移，客户端拿到 200，日志里那行 400 也看不出来**
——「看起来没坏」实际是「每一发都悄悄降级到别的模型」，形状判据的证据也一起没了。
**判据是对的，措辞是错的；只改措辞，别删判据。** 复现命令与全过程见
`docs/06-reasoning.md` §2b 末段。

### 归档留样（历史）：`dump/` 里曾经那 15 份 `err-400-*`

| 上游原文 | 份数 | 归属 |
| --- | --- | --- |
| `The \`reasoning_content\` in the thinking mode must be passed back` | 6 | 本节 |
| `[1210][该模型始终思考，不支持关闭思考；请使用 low、high 或 max。]` | 2 | glm；走 quirk 学习（见 §3） |
| `invalid thinking: only type=enabled is allowed for this model` | 2 | kimi；**措辞不在 `quirk.signatures` 里，学不到，会反复撞** |
| `invalid params, Mismatch type ***.ClaudeContent with value string` | 4 | 客户端发来的 content 块类型不符，与推理无关 |
| `An assistant message with 'tool_calls' must be followed by tool messages` | — | 链上出现未闭合的 tool_use，非本类 |

那 6 份的形状全部是「最后一条 user 消息只有 tool_result」，其中 2 份尾部还跟着
`role:"system"` 插话（`req000464`、`req000412`）——**旧判据要求数组最后一项就是
user，那 2 份被漏修**，是重写判据的直接动因。另有两份（`req000412`、
`req000464`）的历史里混着别家产的 tool id，正是 §1 的第三个维度。

新出现的 400 仍按 §3 分类器处理（形状错误永不摘牌、只计数）。

**2026-09-22 追记：上表这批 `err-400-*` 今天在磁盘上已经没有了。** 它们不是被
正常滚动清掉的，而是被 dump 清理的排序 bug 整批删掉的：`logx.PruneDir` 那版按
名字字典序「留最大」的 keep 组，`err-400-…` 字典序最小、永远第一个被删；而
`saveErrEvidence` 结尾就调它，所以刚写完的 `err-400-*` 当场被自己清掉（日志里
15 次「完整证据已存」，磁盘上一个 `err-400-*` 都没有）。现已改成**按 mtime 留
最新的 keep 组**（同 mtime 用名字升序做稳定 tiebreak；stat 不到年龄的排最新、
不先删），`req-*` 与 `err-*` 走同一份排序。样本本身回不来了，上面那几段因果是
从当时的记录里复述的。

**被形状判据认领的 400 会额外存一份专属证据**（2026-09-17 起）：
`dump/shape-400-<判据名>/req-<id>-<纳秒>/`，里面是 client-sent / we-sent /
upstream-said / audit.txt / meta.txt 五件。目录名里的判据名就是日志那行
`判据 X` 里的 X（名字会过一遍白名单，非字母数字一律换成 `_`），所以看到
`dump/shape-400-deepseek/` 就知道是 DeepSeek 那条 reasoning 回传校验认的。
这份证据**不参与 `req-*` / `err-*` 那套按条数滚动清理**，只按总字节封顶
（512MB，超了从最旧的子目录开始删），因为它是「判据为什么这么判」的唯一
现场；`dump/err-400-*` 记的是「上游回了什么」，两者常常同时出现、看的角度
不同。

**形状 400 的证据不再依赖它落在链上哪一段**（2026-09-22 起）：形状证据归档与
`[shape-400]` 日志改由**转移路径与终局路径共用同一份实现**——形状 400 沿链转移
时也照样留 `dump/shape-400-deepseek/`、照样打那行 `[shape-400]`，不再「一转移就
丢证据」。查这类问题时 `[shape-400]` 与 `dump/shape-400-deepseek/` 都在；日志
那行 `上游说:`（形如 `normal -> smt-deepseek/deepseek-flash -> 400  上游说:
{…}`）仍是快速定位的入口。`dump/err-400-*` 记的是「上游回了什么」，只在定案
分支落，两者看的角度不同。

## 2. Probe 绿但请求 404 / 400

Provider 可能只配置了声明协议入口。分别测试 Anthropic `/v1/messages` 和
OpenAI `/v1/chat/completions`，检查 `anthropic_url` 与通用 base URL。

**「probe 绿、真实流量 400/被摘牌」的另一种成因**：probe 发的是最小请求
（1 条消息、`max_tokens:4`、无 tools、无历史），探不到只在长对话/思考模式
下才触发的上游校验（reasoning_content 就是一例）。这不是 bug，是 probe 的
设计边界——它验证的是"连通性 + 方言"，不是"这条长对话能不能过"。混淆
两者会得出"probe 说健康，为什么还被摘"的错误期待；2026-09-17 起这类 400
已经不再触发摘牌（见 §1），但 probe 本身仍然测不到它。

## 3. 熔断表：谁被摘了、为什么

```bash
newgate breaker      # 两段：被摘牌的（状态/账本/还要等多久/原因）+ 只计数没摘牌的
newgate probe        # 不等冷却，立刻用一次主动探活改结论
newgate tier <档位>   # 这个 binding 在链里排第几、是不是被 maxAttempts 截断
```

熔断的恢复是**半开**的：冷却期满后放行一次真实请求作试探，成功即合闸，失败
则回闸并把冷却翻倍（60s → 120s → … → 10 分钟封顶）。所以「摘了只能等 probe」
已经是过去式——`newgate probe` 现在的作用是**立刻**改结论，以及处理那些真实
流量触发不到的问题（典型例子见 §1：probe 发的最小请求不带历史，探不到
reasoning 校验，反过来也探不到依赖长上下文的故障）。

半开的代价是坏上游会周期性被放回来撞一次用户请求，所以设计上刻意收紧了三点：
只在冷却期满放行、半开期严格一次（并发请求仍被挡在外面）、试探失败即退避 ×2。
`newgate breaker` 的「还要等」列显示的就是退避之后的实际冷却，对不上 60s 基准
是正常的。

看到「明明能用却被摘了」，先看账本那一列：

- **可用性**：连接超时/5xx/流中断，真的不行；
- **限流**：429，20s 起就回来；
- **配置**：401/403/404，一次即摘，去查 key / base / 模型名；
- **请求形状**：永不摘牌，只累加计数——它出现在第二段里，说明有 400 被挡住了
  但上游没被摘（见 §1）。

`newgate metrics` 的 `breaker.skipped.shape_error` 是同一件事的计数器。

**「明明能用却被摘了」还有第三种可能：诊断探活也没救回来。** 连续失败到阈值时
会先做一次主动探活，探活说还通就不摘（记进 `spared`）。所以如果一条 binding
确实被摘了，说明**被动流量和主动探活都说不通**——这时候去看原因那一列：

- 只写「真实流量连续失败」→ 探活也跑了但结论是不通（或没有 verifier）；
- 写「诊断探活也不通」→ 双重证据，摘得没冤枉它；
- 「半开试探失败」→ 它在冷却期满后拿到过一次真实试探名额，那一发也失败了。

`newgate breaker` 第二段的「救回」列是相反方向的记录：这条 binding 一共被
诊断探活从摘牌边缘拉回来过几次（跨重启累计）。这个数字在涨而 `可用性` 账本
一直是 0，说明它间歇性抖动但每次都被探活证明还活着——不是故障，是上游在喘。

## 4. 接管后仍然直连

```bash
newgate status
newgate doctor
newgate shim status
```

检查 PATH 顺序、真实 executable、shell 中旧 base URL 环境变量，以及 OpenCode
配置是否被另一个工具覆盖。PATH 改动通常需要新 shell。

## 5. Restart 后行为没变


`newgate version` 显示 CLI 文件版本，不证明 daemon 已换血。读取 pidfile 后比较：

```bash
md5sum /proc/<pid>/exe /path/to/newgate
```

不一致说明升级没有接管成功。

## 6. 权限错误

Daemon 用户必须能写 state、pid、lock、log 和 thinkcache 文件。目录建议保留
group 继承位，文件需要 daemon 所在组可写。优雅交接 500 或冷层关闭通常是同一类
权限问题。

## 7. Fallback 不符合预期

用 `newgate tier <role>` 查看 resolver 输出，用 `newgate metrics` 看
`chain.step_failed` 与 `chain.failover`，再从响应头确认最终 route。不要只看
profile 文本猜链。

## 8. 请求被改坏

以 dump 三联文件为唯一报文证据。对比 client-sent 和 we-sent，找到具体 splice；
再用日志 notes 确认是 schema repair、special treatment 还是 model rewrite。


## 9. Codex 显示 `stream disconnected before completion`

先看请求是否走 Responses 方言，以及错误正文是否包含「始终思考，不支持关闭思考；请使用
low、high 或 max」。Codex 的 `model_reasoning_effort` 在 `~/.codex/config.toml` 中，
不是 newgate 管理的档位；始终思考的上游使用 `low`、`high` 或 `max`。这里看的是
`reasoning.effort`，不是旧路径的顶层 `reasoning_effort`，而且嵌套值优先。

Responses 上游在 `stream: true` 时会用 HTTP 200 加 SSE `response.failed` 报这类错误，
所以客户端把它误写成断流。它不是网络断开。先用 `newgate debug on` 保留三联报文，再
检查 `client-sent` 里的嵌套对象；`newgate probe <profile>` 可以让网关提前学习模型
的 quirk。学习发生前的第一次坏请求仍可能失败，重启后缓存不可用时也一样。

## 10. Codex 的工具声明了，但模型说没有工具

Codex 0.155.1 会把工具树放在 `input[]` 的 `additional_tools` 项中，例如其中再包
一层 `namespace`。这属于 Codex 客户端方言，不是 Responses 上游普遍读取的顶层
`tools`。DeepSeek、GLM 和 Ark 等上游可能因此返回 HTTP 200 和一段正常文本，却从未
产生 `tool_result`；这种静默失败比 400 更难定位。

修复的第一步是把工具树拍平抬到顶层 `tools`，但保留原来的 `input[0]` 项：实测上游
不反对它，删除它没有依据。通用 lift 是 Codex 方言层的动作，应对所有上游生效；
`codex_deepseek` 另有一条 DeepSeek 专属规则，把 `custom`（除 `apply_patch` 外）降成
`function`，因为 DeepSeek 对其它 custom 工具返回 400。这两件事不要混为「DeepSeek
才需要 lift」。看到没有 `tool_result`，先对比 dump 的 client-sent 与 we-sent，确认
顶层 `tools` 是否出现，再看 `newgate st` 的插件开关和 notes。
