# 接管客户端

「接管」是两件事：**改客户端自己的配置**，以及**给它一个 PATH shim**。做之前按
字节备份，`off` 做逐字节恢复。

```bash
newgate on claude      # / codex / opencode / hermes
newgate off claude
newgate status         # 一眼看到每家现在是什么状态
```

`newgate status` 那行里每一家是四态之一：

| 屏幕上 | 意思 |
| --- | --- |
| 生效 | 它现在确实在走网关 |
| 声明了没生效 | 配置写着要接管，但这条路没接上（多半是 shell 里有冲突的环境变量） |
| 关过却没放开 | 你关过它，但环境里还留着指向网关的东西 |
| 直连 | 它没走这里，走的是自己的上游 |

**改完要重开 shell**：shim 是 PATH 上的一个入口，已经开着的终端不会重新读 PATH。

## Claude Code

用 PATH shim：在专用目录里放一个同名入口，它找到真正的 `claude`，把 Gateway URL
与档位槽位注进环境，然后 exec 顶替掉自己。

**动态模式**注进去的是档位名，于是**已经在跑的会话**下一发请求就会读到最新的
profile——换了 profile 不用重开会话。显式 `--profile` 则把这**一次调用**钉死在某
一份上。

## OpenCode

走配置 takeover：注入一个 `newgate` provider，并改写选中的模型引用。原文件在
backup 里，`newgate off opencode` 逐字节恢复。

Oh My OpenAgent（OMO）是一个独立的模块，它消费 OpenCode、Config 这两条公开端口，
登记自己的档位、takeover、诊断与命令——所以关掉 OMO 不影响 OpenCode 那一半。

## Codex

Codex 有自己的两套形态，见 [Codex 模式](/docs/codex/)——它的模型目录**编在它自己
的二进制里**，所以这里的做法与其他两家不一样。

## Hermes

[Hermes Agent](https://github.com/NousResearch/hermes-agent) 走配置 takeover：改写
`$HERMES_HOME/config.yaml`（缺省 `~/.hermes`）。它会话里能用 `/model` 换模型，所以
接管之后两种用法并存：

- **跟着 newgate 走**：缺省模型写成档位名（`normal`），落到哪个真模型由 newgate 的
  profile 决定，profile 改了下一条消息就跟着变。delegation 与辅助任务（看图、压缩、
  起标题……）同样按档位走，档位在 Hermes 那张卡上改。
- **在 Hermes 里直接点名**：`/model` 的列表里有一行 `newgate`（只有档位），以及每份
  profile 一行 `newgate-<profile>`——钉在那份 profile 上，列出它的档位和它绑定的每个
  具体模型（`provider/model`）。

profile 变了，守护进程会在几秒内把那份清单同步进 `config.yaml`（**只动
`providers.newgate*` 那几行**，你在 Hermes 里 `/model … --global` 选过的不会被改回去）。
Hermes 的消息网关每条消息都重读这份文件，所以**不用重启**；唯一要重启的是第一次接管
之后——网关启动时把看图 / 网页摘要 / 审批的模型抄进了环境变量。`newgate status` 会提示，
重启与否你决定：

```bash
newgate hermes-gateway            # 清单新不新、网关要不要重启
newgate hermes-gateway sync       # 现在就同步一次
newgate hermes-gateway restart    # 让 Hermes 收尾正在跑的会话，再重启网关
```

改 `config.yaml` 是字节手术：只碰上面那几格，别的逐字节不动；写之前还会用 **Hermes
自己的 YAML 解析器**比一遍「原文件 + 这次改动」与「改完的文件」，对不上就不写。
`newgate off hermes` 按键还原：接管写过的几格放回原样，你在接管期间改过的别的东西保留。

## 出问题时

先跑 `newgate doctor`，它会检查配置、provider、端口、接管与模块诊断。

最容易踩的一条：**shell 里已经存在的 `ANTHROPIC_BASE_URL` 这类变量会盖过配置**。
shim 会在子进程环境里显式设置或删掉这些变量，接管时也会给出可见的警告——看到那行
警告就去 `~/.zshrc` / `~/.bashrc` 里找。
