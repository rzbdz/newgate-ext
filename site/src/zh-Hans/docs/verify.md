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
newgate upgrade                   # 先看一遍，再分两次问：换二进制吗？刷新模块数据吗？
newgate upgrade --check           # 只看有没有新的，什么都不动
newgate upgrade -y                # 不问，有事可做的直接做（-f 再加上允许重装/倒退）
newgate upgrade --list            # 有哪些动作（binary、fingerprint-data …）
newgate upgrade fingerprint-data  # 只刷新指纹参考库
newgate upgrade@v0.9.0            # 把二进制钉到某个 tag（也可以是分支、提交）
newgate upgrade --help            # 完整说明，含这份构建里实际有哪些动作
```

**不静默执行**：`upgrade` 先只看一遍，把每个动作「会做什么 / 已经是最新」打出来，
然后**分两次**问 y/N——一次问 newgate 二进制，一次问各模块挂进来的数据动作；已经
是最新的不问。没有终端又没给 `-y` 时不执行、以 1 退出（脚本看得出「其实没升」）。

`upgrade` 是模块**登记升级动作**的入口：谁拥有那份东西，谁提供怎么拿、怎么校验。
二进制本身也是其中一个动作（`binary`，由 `self-update` 模块登记），所以一条命令
连二进制带数据一起升；点了 `@版本` 时只有认识版本的动作会跑，其余的跳过并说一声。

### 二进制从哪来：两种模式

| 模式 | 给谁 | 做什么 |
| --- | --- | --- |
| `release` | 不编代码的机器 | 去规格书声明的 GitHub 仓库拿**最新 Release** 里本平台的产物，对 `SHA256SUMS`，换上 |
| `git` | 开发者、尝鲜的人 | 在本地 checkout 里 `fetch` → 能快进就快进 → `build/build.sh` → 换上 |

走哪个是**第一次运行这个二进制时**定下来的，记在 `state.json` 的 `self-update` 段：
编它的那份源码在本机、是发行版仓库 → `git`；不在（从 Release 下的）→ `release`。
之后只认那一段：

```bash
newgate self-update                 # 现在走哪条、仓库在哪、上一版留没留着
newgate self-update git [目录]       # 改成从本地仓库构建（缺省是编这个二进制的那份）
newgate self-update release [owner/repo]  # 改成装 GitHub Release（可以顺手换仓库）
newgate self-update rollback        # 换回上一版（再敲一次又换回来）
```

`@版本` 不分模式：先找那个 tag 的 Release，有本平台产物就下它；没有（只打了 tag
没发版、是分支或提交、或者本平台不在 Release 矩阵里，比如 riscv64）就在**临时目录**
里 clone 那个 ref 编一份——开发者手上那份 checkout 不会被切走。这条路要本机有
`git`、`go`、`bash`、`python3`。

几条保险：

- 新二进制先以 `newgate` 的名字跑一次 `version`，跑不起来、或者不是同一个发行版，
  就**不换**；
- 换法是同目录临时文件 + rename，旧的那份留成 `<路径>.prev`；
- 不给 `--force` 不倒退：开发机常常比最新 Release 新（`v0.8.0-40-g…`），这时
  `upgrade` 只说一句；
- git 模式**绝不替你做决定**：工作区有改动、本地有没推的提交、分叉、detached——
  都不拉，照编手上这份；
- daemon 在跑就用新二进制执行 `newgate restart`（socket 交接，会话不断），换完核对
  daemon 真的跑在新文件上、并且在答话；**交接失败、新进程起不来或不答话，自动换回
  旧版并用旧版把 daemon 拉回来**；
- 留不下 `.prev` 就不换（没有退路的替换不做）；下载/构建中途被打断留下的半成品，
  下一次升级时清掉。

发布仓库写在规格书里（`dist.json` 的 `"github": "owner/repo"`）：fork 的人改这一行，
升级通道就接到了自己的仓库。

## 致谢

指纹检测由 [lm-detector](https://github.com/Ikaleio/lm-detector) 提供（MIT），参考
数据来自 [lm.ikale.io](https://lm.ikale.io/docs/sdk)。谢谢这个项目。
