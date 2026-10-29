# 更新日志

这个站的更新日志就是 **[GitHub Releases](https://github.com/rzbdz/newgate-ext/releases)**
——那里是唯一一份真的：每一条都对应一个 tag、一份编好的二进制、一串校验和。

这里把最近几版做了些什么写成人话。更早的看 Releases 页面。

## 最近

### v0.9.x

- **自升级**：`newgate upgrade` 连二进制一起升。两种来源——GitHub Release（下本平台
  产物、对校验和），或者开发机上从本地仓库 `fetch` + `build/build.sh`；走哪种在第一次
  运行时定下来，`newgate self-update` 可以看、可以改。`newgate upgrade@<tag>` 钉版本：
  有 Release 用 Release，没有就临时 clone 那个 ref 现编。换之前先试跑，旧版留成
  `.prev`，`newgate self-update rollback` 换回去；daemon 走 socket 交接，会话不断，
  交接失败会自动回滚。
- **`upgrade` 不再静默执行**：先看一遍，再分别为二进制和模块数据各问一次 y/N；
  `-y` 不问，`-f` 不问且允许重装/倒退。
- **网页顶栏的「更新」**：看当前版本、检查有没有新的、跳到 GitHub 发行版页。它是
  self-update 模块**自带的前端插件**——界面新开了一个插件口（槽位 + 契约），界面
  自己不认识任何插件。
- 规格书多了一个 `github` 字段：发布在哪个仓库。fork 的人改这一行，升级通道就是自己的。

### v0.8.x

- **探活成了正式命令**：`newgate probe` 对每条候选真打一发，报连通性、方言能力和
  学到的上游怪癖；结论落盘，下一轮不再重复花 token。
- **模型指纹**：`newgate verify` 让上游吐几百个整数，和模型参考库比对，报出它
  **实际**在服务的模型；binding 的名字对不上时给 warning。网页新增「指纹」页，
  模型下拉也会带出上一次的结论。
- **动态数据升级**：`newgate upgrade` 跑所有模块登记的升级动作（第一版只做动态
  数据/配置，不换二进制）。指纹参考库默认每 2 小时检查一次新版本。
- **终端界面跟上新模块**：`newgate status` 多了一块「客户端」——每个客户端用哪个
  profile、是跟随缺省还是单独设置，以及各槽位走哪一档；`newgate agents` 直接给出
  每个槽位的绑定和客户端自己的配置文件（codex 的 config.toml、opencode 的两个
  json）。`newgate tui` 变成两栏：左栏选客户端或全局缺省，右栏选 profile，下半屏
  预览绑定；`d` 让某个客户端回到跟随缺省。help 补上了 `omo`、`lang` 和「指纹」
  术语，长命令不再和说明挤在一行。

### v0.7.x

- **Codex 的两种接管模式**：接管模式（往 `config.toml` 写档位名）与**改名模式**
  （写 codex 自己的模型名，进来时认回档位）。后者解决的正是「在 codex 里切不了
  模型」——见 [Codex 模式](/docs/codex/)。
- **那张映射表变成了能填的界面**：codex 的哪个模型名算哪一档，在网页面板上加一行
  就行，不用等我们发版，也不用去编辑 JSON。
- **i18n 账本的重生成接进了构建**：改一行代码不再让翻译账本过期（它以前记着
  `文件:行号`，任何一次行偏移都会让它失效）。

### v0.6.x

- **`newgate ask`**：一句话进、正文出。思维链走 stderr，所以
  `answer=$(newgate ask …)` 拿到的是干净的正文。
- **网页面板上的档位卡**多了一句状态说明：当前生效的那一份画成绿色的「当前配置」，
  而不是什么都不显示。

## 怎么自己看

```bash
newgate version                   # CLI 文件的版本
newgate alllogs                   # 版本 + 环境 + 状态 + 脱敏配置，一次汇总
gh release list --repo rzbdz/newgate-ext
```

> `newgate version` 显示的是 **CLI 文件**的版本，它**不证明 daemon 换了血**。升级
> 之后要比 `/proc/<pid>/exe` 与目标二进制的摘要（见 [排障](/docs/troubleshooting/) §5）。
