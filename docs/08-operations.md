# CLI 与运维

## 1. 生命周期

```bash
newgate init
newgate start
newgate status
newgate restart
newgate stop
```

`start` 启动 daemon 并接管未显式关闭的 Agent。`stop` 释放接管并停止 daemon。
`restart` 优先走 listener fd 交接：旧进程排空在途请求，新进程接收新连接。

单独控制 Agent：

```bash
newgate on claude
newgate off claude
newgate on opencode
```

## 2. Profile 与路由

```bash
newgate profiles
newgate tier normal
newgate probe
newgate probe ds
```

`tier` 展示解析后的完整候选链和跳过原因。`probe` 主动验证 provider 连接、方言和
已知怪癖；它不能替代真实请求路径的双协议验证。

一次调用覆盖：

```bash
newgate claude --profile=ds
```

该覆盖编码到 Gateway URL，不修改全局默认。

**点名 profile 的调用不换人**：`--profile=xx` 说的是「这次就用它」，所以链在链头
就被截断——glm 挂了就如实报 glm 的错，不会悄悄从 ds 那儿拿个 200 回来。要全局关掉
换人（网页、curl、别的 agent 一起关），用 `newgate fallback off`；见
`docs/05-gateway.md` §3。

### 直接用：把 newgate 当 LLM 后端

```bash
newgate ask "把这段话译成英文：……"
echo "解释一下这段 diff" | newgate ask --tier light
newgate ask --profile ds --system "只回 JSON" "……"
```

`ask` 走**正在跑的那个代理**发一句问题，只把回答的正文打出来。它自己不起代理
（一次提问不该变成一次状态改变），也不是第二个客户端——档位链、fallback、熔断、
上游怪癖修补在它这条路上照样生效，因为它走的就是那条路。

选项：`--tier`（默认 `normal`，写的是**档位名**不是模型名）、`--profile`（单次
覆盖，不动全局默认）、`--system`、`--max-tokens`。没给问题就读管道。

**思维链走 stderr，正文走 stdout**：`answer=$(newgate ask …)` 拿到的必须是干净的
正文，而 reasoning 模型的一发回答里思维链常常比正文长好几倍（见 06-reasoning.md）。
人在终端上照样看得见它。

别的客户端想直接用这个后端，端点在 `http://127.0.0.1:<port>`（默认 8899）：

- `POST /v1/messages` —— anthropic 方言；
- `POST /v1/chat/completions` —— openai 方言；
- `POST /p/<profile>/v1/messages` —— 单次 profile 覆盖。

两种方言各自成对：报哪个方言的错、说哪个方言的话。用哪一条取决于手上的客户端。

## 3. 观测

```bash
newgate metrics
newgate st
newgate logs
newgate alllogs
newgate doctor
```

`doctor` 检查配置、provider、端口、接管和模块诊断。`st` 列出请求插件及存在原因。
`alllogs` 汇总版本、环境、状态和脱敏配置。

### 裸奔：临时压掉 Bash 分类器

```bash
newgate naked on          # 开 60 秒自限窗口
newgate naked 2m          # 自定义时长（30s / 2min / 1h …）
newgate naked forever     # 永久开（每次拦截都打 [naked] 日志，status 持续警告）
newgate naked off         # 关
```

这段时间内 Claude Code 的 Bash 安全分类器请求被直接批准，不经过任何安全检查
——等价于 Claude Code 自己的 `--dangerouslySkipPermissions`，只是发生在代理这
一层。用在「分类器误判了合法命令、会话卡住」的场景，不是长期配置。默认全程关，
必须显式打开；`on` 的窗口写进 `state.json` 并懒过期，daemon 不持 timer，重启
也不会变成永久。

`newgate st off classifier-naked` 是更快的摘除手段（不用改配置，直接从插件层
停用）。开了 forever 又忘了关时，`newgate status` 每次都会打印那一行警告。

## 4. 部署

**产品二进制从发行版仓库出**（2026-09-20 起，见 `docs/09-extension-guide.md` §8）。
本仓库的 `make build` / `make static` 只产出**内核自带**的二进制（十三个模块），
装到线上是一次降级——它没有 deepseek / glm 那些上游怪癖补丁。本仓库的构建只服务
内核自己的测试（`make e2e`）。

发一版产品：

```bash
cd <发行版仓库>            # 例如 /root/workspace/newgate-ext
build/build.sh dist/      # 本机平台；NEWGATE_PLATFORMS 可出平台矩阵
```

替换运行中二进制应先写新文件再 rename，避免 `Text file busy`。升级后比较
`/proc/<pid>/exe` 与目标二进制摘要，确认 daemon 已换成新版本——另外核一下
`newgate plugin` 的模块数（发行版 `dist.json` 那一份今天是 **30 个模块 / 7 个
可运行期开关点**，2026-09-29 实测（三套网页皮肤各算一个）；内核自带的那份是 **13 个模块 / 0 个开关点**），
少了就是装错了。
这个数跟着**发行版的规格书**走（装一个模块就加一），所以对不上时先看
`git -C core log --oneline -3` 里有没有「bump the kernel」——那份提交改了 gitlink，
模块表也就跟着变了。

## 5. 沙箱

设置 `NEWGATE_HOME` 可创建完全独立的配置和 daemon 状态。E2E 脚本依赖该机制，
不会改动真实用户配置。
