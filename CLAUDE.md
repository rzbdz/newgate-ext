# CLAUDE.md — 在 newgate-ext 里怎么干活

这个目录是 **newgate 的一个发行版仓库**，而且是**顶层**：写模块、编二进制、发版本
都在这里发生。内核源码作为 submodule 钉在 `core/`。

它和内核仓库（`github.com/rzbdz/newgate`）的分工是一句话：

> **内核只做机制**——链的复合、结局的推进、身份的定义、故障的隔离。
> **发行版做产品决策**——装哪些模块、修哪家的怪癖、按什么顺序修。

---

## 0. 两个仓库，三条规矩

```
newgate-ext/           ← 你在这里（发行版：产品）
├── core/              ← submodule：内核源码，钉在一个提交上
├── modules/           ← 本发行版的模块
├── testing/           ← 本发行版的测试（跑在内核的测试设施上）
├── dist.json          ← 规格书：本发行版由哪些模块组成、关掉内核的哪几个
└── build/build.sh     ← 唯一的构建入口
```

1. **`core/` 是 submodule，不是你的工作区**。要改内核（新增 capability、修组件框架、
   改网关），进 `core/` 里改、提交、推到内核仓库，再回到这里 `git add core` 把
   gitlink 挪到新提交。**在这里改完不推**，别人拿到的是一个指向不存在提交的指针。
2. **submodule 在这个方向是对的**（内核塞进发行版），反方向不是：这里构建**真的需要**
   那份内核源码，所以 `clone --recursive` 就该拿到一棵能编的树；而内核构建**不需要**
   发行版的代码，它需要的是一张声明（装哪个发行版）——那是数据，不该进内核的版本控制。
3. **两条分支**：`main` = 官方发行版；`template` = 给别人 fork 的骨架（只有两个样例
   模块）。发行版相关的改动（build / chore）单独提交，需要时 cherry-pick 到 template。

---

## 1. 改哪边（判据）

> **换个发行版，这东西还该在吗？**

| 该在 → `core/` | 不该在 → `modules/` |
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
- 模块之间的 import 是 `github.com/rzbdz/newgate/go/modules-ext/modules/<名字>`；
- **目录名不必是 Go 标识符**（`simple-cli` 合法），生成器会把 import 别名拧成
  `ext_simple_cli`；
- 依赖方向：可以 `Need`/`Optional` 内核提供的端口；**不要**依赖内核里某个具体模块的
  内部——那是内核自己的事，它也没给你那个口子。

写模块的完整规矩（fail-open、不静默、注释写「为什么」、字节手术、capability 而不是
直接调用）**沿用内核仓库那一套**，`core/CLAUDE.md` 与 `core/docs/` 是权威原文。

## 3. 编与跑

```bash
git submodule update --init --recursive   # 第一次
build/build.sh                            # → dist/newgate-<发行版>-<平台>-<架构>
NEWGATE_DIST=dist-simple-cli.json build/build.sh out/   # 换规格书的变体
```

- 脚本做四件事：检查 `core/` → 写一张 Pin（指向**本仓库当前提交**）→
  `core/go` 里 `make static` → 拷出来。本地与 CI（`.github/workflows/release.yml`）
  走的是同一条路。
- 它**不写任何东西进内核仓库根**：内核根那份 `modules-ext.json` 是**默认发行版**的
  配置，不是我们的，所以用 `NEWGATE_MODULES_PIN` 指一份临时的。
- **构建编的是「已提交的状态」**：Pin 钉的是提交号，内核会把这份仓库 clone 到
  `core/go/modules-ext/`，未提交的改动不在里面。所以改完模块**先 commit**（要回退就先
  打一个 WIP commit，别用裸 `git stash`）。脚本在工作区脏时会明确警告。
  （想做到「改完直接编」得让 `core/go/modules-ext` 变成指回本目录的符号链接；
  代价是 `go test ./...` 不跟进符号链接，测试得显式点名——目前没做。）

### 一个必然的副作用：内核 checkout 会变脏

装配清单（`core/go/app/modules_gen.go`）是**跟着发行版走**的生成物：编一次本发行版，
内核那份清单就被重写成「内核自带 + 本发行版点名的模块」。于是 `git -C core status`
会显示它被改过——**这是正常的**，不是你的改动。

- 不要把它提交回内核仓库（那是内核的默认发行版清单，由内核的 Pin 决定）。
- 要还原：`git -C core checkout -- go/app/modules_gen.go`。
- 后果要说清楚：只要内核 checkout 脏着，`git -C core pull` / `checkout` 会被
  工作区挡住。换分支或拉内核之前先还原它。

## 4. 测

本发行版的 `testing/` 与各模块的 `_test.go` **编在内核的 module 里**，所以测试在
内核那侧跑：

```bash
cd core/go && gofmt -l . && go vet ./... && go test ./...
```

它带上的是 clone 进 `core/go/modules-ext/` 的那一份 = 你**已提交**的状态。推之前
跑这三条——内核的 CI 会把它们连内核自己的测试一起跑一遍，这里的格式/vet 不合格会
红在**内核**的流水线上（离真因隔着一次上下文切换，所以在这里先跑）。

## 5. 发布

```bash
git tag v0.1.0 && git push origin main --tags   # CI 编静态二进制并上传 artifact
```

发行节奏是发行版作者的事：内核今天合了什么，不该决定你的产品什么时候发新版。
