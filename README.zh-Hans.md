<div align="center">

# newgate-ext

### 为 **vibecoding** 而生的最佳网关。

<sub>无感接管 · 一条命令切换 API · 自己会解释的故障转移 · metrics 与调试日志 · 零停机优雅升级 · 彻底模块化</sub>

[![CI](https://github.com/rzbdz/newgate-ext/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/rzbdz/newgate-ext/actions/workflows/ci.yml)
![release](https://github.com/rzbdz/newgate-ext/actions/workflows/release.yml/badge.svg)
![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)
![dependencies](https://img.shields.io/badge/third--party_deps-0-brightgreen)

<sub><a href="README.md">English</a> · <b>中文</b></sub>

</div>

---

一个本地网关，坐在 Claude Code / OpenCode 与你选的模型之间。它不打断工作流：切换上游
时会话照跑；来了新模型、或者某个上游又冒出新怪癖时，适配它就是一个小模块——在你自己
fork 的仓库里，按你自己的节奏。

**接管你的 CLI，而 CLI 毫无察觉。**
PATH shim 加上就地改写客户端自己的配置——env 静默注入，动手前先做逐字节备份。装一次，
之后你再也不用改配置文件，也不用重开会话。

**换哪个 API 应答是一条命令，不是一次迁移。**
`newgate --set-profile ark`——下一个请求就走新链。客户端照跑，配置不动，老上游留着
一条命令的距离。

**单个上游挂掉，不等于你挂掉。**
每个档位都是一条有序的候选链——先按你的规则排，再按预测的首字节时间排。每一次跳过
都有解释，每一次改道都有日志。

**出了什么事，看得见。**
`newgate metrics`——按上下文长度分桶的延迟、故障转移、首字节超时、改写次数，每个计数器
配一句人话解释；`newgate logs -f` 跟着看代理日志，`newgate debug on` 打全量请求，
不对劲时找 `newgate doctor`。

**升级不会打断你。**
`newgate restart` 把监听 socket 交给新二进制，旧进程把在途请求排空——流式响应也一样。
在长会话中间升级是安全的，哪怕那个会话正穿行在你替换的这个网关上。

**上游的怪癖是模块，不是 `if`。**
DeepSeek 的推理回传与尾部形状、GLM 的思维链回传、Claude Code 的后台分类器调用——
`newgate st` 把六个全列出来，每个都写着它防的是哪种故障，运行期可开关。客户端 × 模型的
交叉语义同样是模块（Claude Code × DeepSeek、Claude Code × GLM，以及 OpenCode 里的
Oh My OpenAgent 槽位）。读一个、改一个，或者为你从没听过的上游写一个。

## Fork 下来，改成你自己的

一个模块就是一个目录加一个 `New()`。把我们的路由策略换成你的、删掉你不在乎的怪癖、
留下网关与熔断：

```jsonc
// dist-mine.json  →  build/build.sh  →  每个平台一个静态二进制
{ "distribution": "mine",
  "modules": ["tui", "deepseek", "glm", "claudecode", "opencode"],
  "disable": [] }
```

网关、档位解析、熔断、界面、入口——全是模块，所以「换一套策略」等于换一个模块，而不是
给内核打补丁。`newgate restart` 负责把 socket 交出去，所以你发自己的版本不会打断任何人，
包括会话中的你自己。想从近乎空白开始，就拿 `dist-hello.json`（框架 + 一个 `hello`）。

## 安装

```bash
gh release download --repo rzbdz/newgate-ext -p '*linux-amd64'
mv newgate-default-linux-amd64 ~/.local/bin/newgate && chmod +x ~/.local/bin/newgate
newgate init && newgate on claude     # 以及：newgate on opencode
```

一个静态二进制，无运行期依赖。`newgate init` 铺一份带占位符的配置；把 key 填进
`providers.json`（或用 `NEWGATE_KEY_*` 环境变量），重开 shell 继续干活。

## 这个仓库是什么

newgate 分成两半，这个分法本身就是要紧的东西：

| | |
| --- | --- |
| [**rzbdz/newgate**](https://github.com/rzbdz/newgate) | **内核**：链、熔断、网关、升级交接、模块框架。不含任何产品决策——它不认识任何上游、任何客户端的名字。 |
| **rzbdz/newgate-ext**（这里） | 一个**发行版**：装哪些模块、修哪家的怪癖、按什么顺序。这是产品决策，也是你可以改的那部分。 |

这里是旗舰版：Claude Code 与 OpenCode，跑在你选的档位上，你用到的上游怪癖已经修好。
`dist.json` 就是它的全部——其余都是模块。

## 链接

[内核](https://github.com/rzbdz/newgate) ·
[为什么这么设计](https://github.com/rzbdz/newgate/blob/main/docs/03-architecture.md) ·
[扩展指南](https://github.com/rzbdz/newgate/blob/main/docs/09-extension-guide.md) ·
[发布](https://github.com/rzbdz/newgate-ext/releases) ·
[CLAUDE.md](CLAUDE.md) — 贡献者（人或 agent）要守的家规
