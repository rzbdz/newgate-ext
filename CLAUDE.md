# CLAUDE.md — 在 newgate-ext 里怎么干活

这个目录是 **newgate 的一个发行版仓库**，而且是**顶层**：写模块、编二进制、发版本
都在这里发生。内核源码作为 submodule 钉在 `core/`。

它和内核仓库（`github.com/rzbdz/newgate`）的分工是一句话：

> **内核只做机制**——链的复合、结局的推进、身份的定义、故障的隔离。
> **发行版做产品决策**——装哪些模块、修哪家的怪癖、按什么顺序修。

---

## 0. 两个仓库，四条规矩

```
newgate-ext/           ← 你在这里（发行版：产品）
├── core/              ← submodule：内核源码，钉在一个提交上
├── go/                ← 本发行版的 Go module（与 core/go 平行）
│   ├── go.mod         ← replace github.com/rzbdz/newgate/go => ../core/go
│   ├── modules/       ← 本发行版的模块
│   ├── testing/       ← 本发行版自己的测试
│   ├── manifest/      ← 生成物：规格书 → 装配选择（进版本控制）
│   ├── cmd/newgate/   ← 本发行版的 main（十几行）
│   └── tools/distgen/ ← 读规格书，生成 manifest/
├── dist.json          ← 规格书：本发行版由哪些模块组成、关掉内核的哪几个
├── build/build.sh     ← 唯一的构建入口
└── mock/              ← 本发行版模块的端到端（复用内核的假上游）
```

1. **`core/` 是 submodule，不是你的工作区**。要改内核，去内核仓库改、提交、推，
   再回到这里 `git -C core fetch && git -C core checkout <新提交>`、`git add core`。
   **在这里改完不推**，别人拿到的是一个指向不存在提交的指针。
2. **发行版是它自己的 Go module**（`go/go.mod`），内核只是它的一份依赖
   （`replace` 到 `../core/go`）。所以：
   - 内核版本**只有一个真相**——submodule 的 gitlink；没有第二个版本号要对;
   - 构建读的是**工作区**，不必先 commit；
   - 内核的测试可以在树内直接跑（流水线里的 `core-test` 就是这么来的）。
3. **两条分支**：`main` = 官方发行版；`template` = 给别人 fork 的骨架（只有两个样例
   模块）。发行版相关的改动单独提交，需要时 cherry-pick 到 template。
4. **内核对发行版一无所知**。`app.Selection` 是那条接缝：组合根的装配逻辑在内核，
   「装哪些」由这里给。别指望内核认识任何模块名——它连 `deepseek` 这个词都不该有。

## 1. 改哪边（判据）

> **换个发行版，这东西还该在吗？**

| 该在 → `core/` | 不该在 → `go/modules/` |
| --- | --- |
| 网关、熔断、接管、界面、配置、组件框架 | 上游怪癖修补（DeepSeek 尾部形状、GLM 思维链回传） |
| 客户端接入（Claude Code / opencode） | 客户端×模型的交叉语义 |
| 别的发行版也要用的**机制** | 只对某个上游/某个产品有意义的**取舍** |

好消息：这个判断**错了也不会静默**——写进哪个仓库、装不装，都在规格书里写着，
构建日志与 `newgate plugin` 会把实际装了什么报出来。

## 2. 模块怎么写

形态与内核 `modules/` 里**完全一样**：一个目录、一个 `module.go`、导出
`func New() modules.Component`，声明 `Requires` / `Provides` / `Start` / `Stop`。

- 对内核的 import 是 `github.com/rzbdz/newgate/go/...`（**用它的公开契约**，
  别 import 内部实现包——那些随时会动）；
- 模块之间的 import 是 `github.com/rzbdz/newgate-ext/go/modules/<名字>`；
- **目录名不必是 Go 标识符**（`simple-cli` 合法），生成器会把 import 别名拧成
  `ext_simple_cli`（判据在内核的 `tools/genmodules/scan.Ident`，两个仓库共用一份）；
- 依赖方向：可以 `Need`/`Optional` 内核提供的端口；**不要**依赖内核里某个具体模块的
  内部——那是内核自己的事，它也没给你那个口子。

写模块的完整规矩（fail-open、不静默、注释写「为什么」、字节手术、capability 而不是
直接调用）**沿用内核仓库那一套**，`core/CLAUDE.md` 与 `core/docs/` 是权威原文。

## 3. 编与跑

```bash
git submodule update --init --recursive   # 第一次
build/build.sh                            # → dist/newgate-default-<平台>-<架构>（只编主配置）
NEWGATE_ALL=1 build/build.sh              # 每份规格书都编一遍 → 多个二进制
NEWGATE_DISTS="dist.json dist-hello.json" build/build.sh out/   # 点名几份
NEWGATE_DIST=dist-simple-cli.json build/build.sh out/           # 只编那一份

# 发布时给一份平台矩阵（默认只编本机）
NEWGATE_PLATFORMS="linux/amd64 linux/arm64 darwin/arm64" build/build.sh dist/
```

- **产物名 = `newgate-<规格书里的 distribution>-<平台>-<架构>`**。今天是三份：
  `default`（旗舰）、`simple-cli`（换掉界面）、`hello`（**骨架**：整个框架 + 一个
  hello，`newgate` 跑起来就是一句 hello world）。多编几份是为了**调试**——
  想看骨架配置的行为，`NEWGATE_ALL=1` 编出来直接跑，不必切到 `template` 分支。
- **发什么由流水线决定**：Release 只发重点那份（`release.yml` 里的
  `NEWGATE_DISTS: dist.json`），CI 则编**全部**规格书（「另一份配置编不过」这种
  故障本地看不见，谁也不天天编骨架配置）。

- 脚本做四件事：检查 `core/` → 生成装配清单（`go/tools/distgen`）→ 编静态二进制
  → 拷到 `dist/`。本地与 CI（`.github/workflows/release.yml`）走的是同一条路。
- **多调用型二进制**：argv0 决定这次调用归谁（`newgate` / `claude` / `opencode` …）。
  所以拿产物去跑之前，**先按正确的名字落一份**——直接跑
  `dist/newgate-<发行版>-linux-amd64 plugin` 会被当成 profile 名，报
  「同时给了 profile … 和 …，不一致」（2026-09-20 实测）。
- 换规格书 = 改 `dist.json` 或加一份 `dist*.json`，**不用改任何 Go 代码**：
  `distgen` 会把全部规格书生成进 `go/manifest/modules_gen.go`（那份文件要提交），
  构建时用 `-X main.spec=<文件名>` 选一份。
- **`go/manifest/modules_gen.go` 是生成物但进版本控制**：它记着「这份提交装了什么」。
  改了规格书或模块目录就要重新生成（`cd go && go run ./tools/distgen`），
  CI 有一步 `-check` 拦「忘了生成」。

## 4. 测

分成两段，**发行版的流水线里第一项就是内核的全部测试**：

```bash
# core-test：内核自己的（离线、不依赖本仓库）
cd core/go && GOPROXY=off go test ./... && make check-fmt && make check-generate && go vet ./...

# dist-test：本发行版自己的
cd go && gofmt -l . && go vet ./... && go run ./tools/distgen -check && go test ./...

# 端到端：真二进制 + 内核的假上游（零 token）
build/build.sh dist && bash mock/e2e_claude_dist.sh
```

**测试跟着拥有者走**：
- 内核的测试只管内核的逻辑与内核的模块（`core/go/...`）；
- 本发行版的模块行为由本仓库的测试与 `mock/` 里的端到端锁；
- 需要真 token 的 `mock/e2e_reasoning_affinity.sh` **不在 CI 里**（它花真钱），
  按需人工跑。

内核的假上游（`mock/fake_upstream.py`）**按路径复用**（`core/mock/…`），不复制：
它是逐字节复刻真实上游行为的产物，复制必然漂移，而漂移出来的是「绿的假测试」。

## 5. 发布

```bash
git tag v0.1.0 && git push origin main --tags   # CI 编平台矩阵并挂到 GitHub Release
```

产物直接可下载（`gh release download <tag>` 或 Release 页面）。发行节奏是发行版
作者的事：内核今天合了什么，不该决定你的产品什么时候发新版。
