# 指纹与升级

## 这个功能回答什么问题

配置里写着 `provider/model`，但上游**真的**在用这个模型吗？转发一条消息问问「你是谁」
没有用——它当然会照着你写的名字回。指纹换一种问法：让模型吐几百个 1–355 之间的
整数，再拿这些数字和一份模型参考库比一遍。不同的模型在选数上有各自稳定的统计特征，
所以这件事比名字可靠得多。

`newgate verify` 做一次这样的验证；网页上对应侧栏里的**指纹**页。

## 命令行

```bash
newgate verify                          # 全部 profile，每条 binding 验一次
newgate verify work                     # 只验一份 profile
newgate verify --provider vendor --model model-a --count 3
newgate verify --json                   # 机器可读
```

`--count` 是挑战数（1–3）。1 条就能给出排名（`partial`，没有校准概率）；3 条是
参考库推荐值，`scored` 时会给出概率。每次验证都会真打上游、会花 token。

结果会按 `provider/model` 缓存到
`~/.config/newgate/fingerprints/cache.json`，所以网页和模型下拉能看到「上一次验到的
是谁」，不必每看一次就重验。

## 网页

侧栏「指纹」页里：

- **按档位文件**：筛一份 profile，列出每条 binding 的上次结论、名字匹配、时间，
  每行一个「验证」按钮；「全部验证」会把当前筛选里的去重目标跑一遍。
- **挑一个模型**：不经过任何 profile，直接选上游、模型、API 和挑战数，现场取一份
  指纹，并显示参考库的 Top 候选。

配置页的模型下拉也接上了：选中模型旁边会带出上一次的指纹和「验证」按钮，下拉列表
每一行也会标注上次的匹配结果。

## 名字匹配怎么判

| 结论 | 含义 |
| --- | --- |
| `match` | 去掉分隔符后名字完全一致 |
| `dated` | 同一个模型，另一个日期/快照版 |
| `family` | 同族里的**另一个**模型（会警告） |
| `mismatch` | 名字对不上，也不是同族（会警告） |
| `unknown` | 这一轮没有可用的排名（样本失败、不是模型输出） |

`family` 和 `mismatch` 是**警告，不是命令失败**。指纹是证据，不是证明：参考库是闭集
排名，库里没有的新模型会被判成「最接近的那个」；重要判断建议固定请求参数、隔一会儿
再验一轮，并结合概率和 Top 候选一起看。

## 参考数据有多新

参考库是**动态数据**，不跟 newgate 的版本绑死：

- 默认每 **2 小时**检查一次新版本（下载 `bundle.json`，只有 detector 的 sha 变了
  才下约 3.6 MB）；
- 想要立刻刷新：`newgate upgrade`（见下）；
- 调整检查间隔：`NEWGATE_FINGERPRINT_TTL=6h`（`time.ParseDuration` 允许的任何写法）；
- 完全离线：`NEWGATE_FINGERPRINT_DATA=/path/to/data` 指向一份含
  `unified_bank.json` + `shared_detector.bin` 的目录。

第一次验证要下载约 3.6 MB 并缓存在 `~/.config/newgate/fingerprints/data.bin`；
之后本地加载是几十毫秒，打分是毫秒级——**真正慢的是上游生成那几百个整数**，一次
通常几十秒。

## `newgate upgrade`

```bash
newgate upgrade                 # 跑所有模块登记的升级动作
newgate upgrade --list          # 只看有哪些动作
newgate upgrade fingerprint-data  # 只刷新指纹参考库
```

`upgrade` 是模块**登记动态数据升级动作**的入口：谁拥有那份数据，谁提供怎么拿、怎么
校验。第一版只做动态数据/配置（指纹参考库、将来的远端配置、插件自己的数据文件），
**不换二进制**。绑定一个 git remote 自动 fetch + 编译 + 安装同样还没有做——静态升级
仍然走「换二进制 + `newgate restart` 优雅交接」那条路。

## 致谢

指纹检测由 [lm-detector](https://github.com/Ikaleio/lm-detector) 提供（MIT），参考
数据来自 [lm.ikale.io](https://lm.ikale.io/docs/sdk)。谢谢这个项目。
