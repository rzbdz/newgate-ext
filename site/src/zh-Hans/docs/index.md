# 文档

这些页面讲的是**这个发行版**：装了什么、怎么用、怎么改成你自己的。

内核（链怎么解析、熔断怎么做、模块框架长什么样）的权威原文在内核仓库的
[docs/](https://github.com/rzbdz/newgate/tree/main/docs) 里。这里不复制它——复制
一份的东西会过期，而过期的那份会让人以为系统是那样。

## 从这里开始

| 想干的事 | 看这一页 |
| --- | --- |
| 装上、接管一个客户端、跑通第一条链 | [快速开始](/docs/quickstart/) |
| 搞明白 heavy / normal / light 到底是什么 | [档位与 profile](/docs/tiers/) |
| 让 Claude Code / Codex / OpenCode 走这条网关 | [接管客户端](/docs/clients/) |
| 在 codex 里换模型（而不是换配置） | [Codex 模式](/docs/codex/) |
| 配置文件的形状、常用命令 | [配置与命令](/docs/config/) |
| 出问题了 | [排障](/docs/troubleshooting/) |
| 写一个自己的模块 | [写一个模块](/docs/modules/) |
| fork 一份属于你的发行版 | [fork 一份发行版](/docs/fork/) |

## 这一页在这一份装配里意味着什么

这个站点的每一页都在讲**发行版 `newgate-ext` 的默认规格**（`dist.json`）：
13 个模块，Claude Code / Codex / OpenCode 三家客户端，TUI + 浏览器两份界面。

换成别的规格书（`dist-simple-cli.json`、`dist-dashboard.json`、`dist-hello.json`），
装上的模块不一样，于是**界面上能看到的、命令行里能敲的**都会跟着变——那不是文档
不对，那正是「一切都是模块」的意思。想知道你手上那份装了什么：

```bash
newgate plugin
```
