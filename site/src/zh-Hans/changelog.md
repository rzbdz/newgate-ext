# 更新日志

这个站的更新日志就是 **[GitHub Releases](https://github.com/rzbdz/newgate-ext/releases)**
——那里是唯一一份真的：每一条都对应一个 tag、一份编好的二进制、一串校验和。

这里把最近几版做了些什么写成人话。更早的看 Releases 页面。

## 最近

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
