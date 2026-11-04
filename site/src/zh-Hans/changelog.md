# 更新日志

**`CHANGELOG.md` 是唯一一份真的**（按 tag 一节，Added / Changed / Fixed）：
[github.com/rzbdz/newgate-ext/blob/main/CHANGELOG.md](https://github.com/rzbdz/newgate-ext/blob/main/CHANGELOG.md)。
GitHub Release 的说明就是流水线从那里取出来的那一节——**找不到对应小节时发版会直接
失败**，不会退回自动生成的提交清单。

这里把最近几版做了些什么写成人话。更早的看 changelog 文件与 Releases 页面。

## 最近

### v0.8.1（2026-11-04）

- **自升级**：`newgate upgrade` 连二进制一起升。两种来源——GitHub Release（下本平台
  产物、对校验和），或者开发机上从本地仓库 `fetch` + `build/build.sh`；走哪种在第一次
  运行时定下来，`newgate self-update` 可以看、可以改。`newgate upgrade@<tag>` 钉版本：
  有 Release 用 Release，没有就临时 clone 那个 ref 现编。换之前先试跑，旧版留成
  `.prev`，`newgate self-update rollback` 换回去；daemon 走 socket 交接，会话不断，
  新二进制试跑失败会自动回滚。
- **`upgrade` 不再静默执行**：先看一遍，再分别为二进制和模块数据各问一次 y/N；
  `-y` 不问，`-f` 不问且允许重装/倒退。
- **网页顶栏的「更新」**：看当前版本、检查有没有新的、跳到 GitHub 发行版页。它是
  self-update 模块**自带的前端插件**——界面新开了一个插件口（槽位 + 契约），界面
  自己不认识任何插件。
- **模型指纹**：`newgate verify` 让上游吐一串数字，和模型参考库比对，报出它**实际**
  在服务的模型；binding 的名字对不上时给 warning。网页新增「指纹」页，模型下拉也会
  带出上一次的结论。`newgate upgrade` 顺带刷新模块自带的动态数据和指纹库。
- **模型下拉问上游**：`modellist` 直接问 provider 它提供什么模型，而不是靠本地列表。
- **`newgate nothink on|forever|off`**：全局不思考开关。
- **SSH 隧道**：`ssh-tunnel` 让你通过 SSH 打开远端机器的界面（终端与网页两条路）。
- **网页 home 模式**：一屏摊开每条链；每个客户端带自己的图标；卡片固定次序、
  pin-vs-follow、fallback 开关；主题变成插件口（自带三套）；侧栏按用途分组。
- **每个客户端自己的 profile**：`newgate status`、`newgate tui`、`newgate agents`
  都能看到谁跟着缺省、谁被单独钉在哪份 profile 上，以及每个槽位走哪一档。
- **修复**：唯一绑定了某个模型的 binding 被熔断时，模型名不再被误报成「既不是档位、
  也不在任何 profile 的绑定里」——按同档位的可用链走；点名 profile 绝不静默换
  profile；`newgate probe` 成功**立刻**解熔断，不再等 60s；opencode 的 agent 身份
  回到 base URL；Claude Code 的每一道分类器闸门都被放行；SSH 隧道的路由与数据
  读错机器。

### v0.8.0（2026-09-28）

- **网页面板**：网关端口上的浏览器界面——侧栏、标签页与分栏；配置工作台（原文
  编辑器边打边更新控件）；网页上配 provider；熔断表每行一个测试按钮；只唤醒需要
  的源的日志视图；按用途分组的开关；界面自己跟随解析出的语言。
- **Codex 接管**：安装引导、面板上的 Codex 卡，以及两种模式——往 `config.toml`
  写档位名，或写 codex 自己的模型名再认回档位（映射表在网页上加行即可）。
- **架构图**：`arch-diagram` 把同一张图挂到 `/arch`，`newgate arch` 打出地址。
- **公开文档站**：手写渲染器生成（没有 SSG），首页里嵌的是真界面。
- **`dashboard` 规格书**：只有网页、没有终端界面的那一份，证明「界面只是模块」。
- **Codex × DeepSeek 工具交叉**，以及 DeepSeek `/responses` 尾部形状修复。
- **`newgate ask`**：一句话进、正文出；思维链走 stderr。
- **1M 标记**：注入 Claude Code 的每个真实模型名都带上 1M 上下文标记。
- 每个模块说明自己是干什么的；每个客户端都报自己的 profile。

### v0.7.x

- **发行版说自己的话**：用户可见文案搬进 `modules/i18n/catalogs/zh-Hans.json`；
  内核提供机制，两个仓库用同一把尺子检查。对本地改过字符串的人是 breaking。

### v0.6.x

- 内核里的模块搬进发行版：终端界面、客户端模块，以及还没接线的 `configshare`。
- README 重写（全英文）。

## 怎么自己看

```bash
newgate version                   # CLI 文件的版本
newgate alllogs                   # 版本 + 环境 + 状态 + 脱敏配置，一次汇总
gh release list --repo rzbdz/newgate-ext
```

> `newgate version` 显示的是 **CLI 文件**的版本，它**不证明 daemon 换了血**。升级
> 之后要比 `/proc/<pid>/exe` 与目标二进制的摘要（见 [排障](/docs/troubleshooting/) §5）。
