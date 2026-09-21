# 为 vibecoding 而生的最佳网关

一个本地网关，站在 Claude Code / Codex / OpenCode 和你选定的模型之间。它不挡在流程
里：切换上游时你的会话照常跑；上游出新模型、或者冒出一个新怪癖时，适配它是**一个
小模块**，在你自己 fork 里、按你自己的节奏。

## 看一眼它长什么样

上面那张不是截图——它是**真的界面**，抽出来喂了一份假数据。点得动、改得了、切得
了模式，只是最后那一下「应用」不会落盘（这里没有后端）。它在你自己的机器上是
`newgate web` 打出的那个地址，数据换成你 gateway 此刻的真实快照。

**接管你的 CLI，而 CLI 毫无察觉。**
一个 PATH shim，加上客户端自己的配置被就地改写——环境变量静默注入，改之前先按字节
备份。装一次，此后不用再手改配置，也不用重开会话。

**换哪个 API 应答是一条命令，不是一次迁移。**

```bash
newgate --set-profile ark
```

下一发请求就走新链。客户端继续跑着，它的配置没被动过，旧上游离你一条命令。

**该全局就全局，小活可以临时来。**
档位配一次，所有客户端跟着走。某个小任务想换个模型，就在那一次调用上换：

```bash
newgate claude --profile=kimi
```

什么都不改全局的。这个选择走在**请求路径**里，所以同时跑着的两个客户端永远不会
拿错对方的选择。

**单个上游挂掉，不等于你挂掉。**
每个档位都是一条**有序的候选链**：你的规则优先，然后是预测的首字节时间。每一次跳过
都有解释，每一次改道都记账。

**出了什么事，看得见。**

```bash
newgate metrics     # 按上下文大小分的延迟、改道次数、首字节超时、字节改写，每个计数器都带一句话
newgate logs -f     # 跟着看代理日志
newgate debug on    # 把每一发请求都倒出来
newgate doctor      # 出问题时先跑它
```

**同一份画面，浏览器里也有一份。**
`newgate web` 打出地址——就挂在网关已经在听的那个端口上。档位绑定就地编辑、运行期
开关直接拨、计数器、日志尾巴、绑定健康、此刻谁在走网关，都在那儿。每一次改动都走
**命令行用的那条写入路径**，并带上你加载时的那份修订：你在看页面时命令行改过的文件，
会摆成一个冲突让你决定，而不是被盖掉。

**升级不会打断你。**
`newgate restart` 把监听 socket 交给新二进制，并把在途的请求排干——流式的也算，
所以在一个很长的 agent 会话中间换版本是安全的，哪怕那个会话正走在这条被替换掉的
网关上。

**上游的怪癖是模块，不是 `if`。**
DeepSeek 的 reasoning 回传与尾部形状、GLM 的思维链回传、Claude Code 的后台分类器
调用——每个都写了它存在是为了防住哪个故障，可以在运行期开关。客户端 × 模型的桥也是
模块（Claude Code × DeepSeek、Claude Code × GLM、OpenCode 里 Oh My OpenAgent 的
槽位）。读一个，换掉它，或者给一个我们从没听说过的上游写补丁。

## Fork 下来，改成你自己的

一个模块就是一个目录、里面一个 `New()`。把我们的路由策略换成你的、扔掉你不在乎的
怪癖、留下网关和熔断：

```jsonc
// dist-mine.json  →  build/build.sh  →  每个平台一个静态二进制
{ "distribution": "mine",
  "modules": ["tui", "deepseek", "glm", "claudecode", "opencode"],
  "disable": [] }
```

网关、档位解析、熔断、界面、入口——全都是模块，所以换一个策略 = 换一个模块，而不是
给内核打补丁。`newgate restart` 会把 socket 交出去，所以你发自己的版本不会打断任何
人，包括正在会话里的你自己。想从几乎什么都没有开始，就从 `dist-hello.json` 起步
（框架 + 一个 `hello`）。

## 安装

```bash
gh release download --repo rzbdz/newgate-ext -p '*linux-amd64'
mv newgate-default-linux-amd64 ~/.local/bin/newgate && chmod +x ~/.local/bin/newgate
newgate init && newgate on claude     # 还有：newgate on codex / newgate on opencode
```

一个静态二进制，没有运行期依赖。`newgate init` 铺下一份带占位符的配置；把 key 放进
`providers.json`（或者放进 `NEWGATE_KEY_*` 环境变量），重开一次 shell 就能干活了。
