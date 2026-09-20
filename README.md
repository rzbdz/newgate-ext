# newgate-ext —— 一个 newgate 发行版（顶层仓库）

newgate 的内核只做机制；**产品决策在发行版这一层**：装哪些模块、修哪家的怪癖、
按什么顺序修。这个仓库就是那个发行版，而且它是**顶层**——内核源码作为 submodule
钉在 `core/`，写模块、编二进制、发版本都在这里发生。

fork 它 → 改 `dist.json` 与 `go/modules/` → 就有了你自己的发行版。

## 目录

| 路径 | 是什么 |
| --- | --- |
| `core/` | **submodule**：内核源码（`github.com/rzbdz/newgate`），钉在一个提交上 |
| `go/` | 本发行版的 **Go module**（与 `core/go` 平行） |
| `go/go.mod` | `replace github.com/rzbdz/newgate/go => ../core/go`——内核是这里的一份依赖 |
| `go/modules/<名字>/module.go` | 本发行版的模块，形态与内核的 `modules/` 完全一致 |
| `go/manifest/modules_gen.go` | **生成物**（进版本控制）：规格书 → 装配选择 |
| `go/tools/distgen/` | 读规格书、生成上面那份清单 |
| `go/cmd/newgate/` | 本发行版的 main：交出「装哪张图 + 版本号」，其余交给内核的组合根 |
| `dist.json` | **规格书**：本发行版由哪些模块组成、关掉内核的哪几个 |
| `dist-simple-cli.json` | 第二个规格书：同一个仓库的另一个变体（换掉界面） |
| `mock/` | 本发行版模块的端到端（复用内核的假上游） |
| `build/build.sh` | 唯一的构建入口 |
| `CLAUDE.md` | 在这里干活的人（和 agent）要先读的那份说明 |

## 两条分支

| 分支 | 是什么 |
| --- | --- |
| `main` | **官方发行版**：内核之外我们维护的模块 |
| `template` | 给别人 fork 的骨架：只有 `hello` 与 `simple-cli` 两个样例模块 |

## 编一个

```bash
git clone --recursive <这个仓库> my-dist && cd my-dist
$EDITOR dist.json                     # 选模块、关掉不要的
build/build.sh                        # → dist/newgate-<发行版>-<平台>-<架构>
```

编的是**你自己的 main + core/ 那一发的内核**：发行版是独立 module（`go/go.mod`），
内核只是它的一份依赖（replace 到 submodule）。于是没有中间产物要同步——改完直接编，
不必先 commit，内核的工作区也不会被改写。

要发布多平台产物：

```bash
NEWGATE_PLATFORMS="linux/amd64 linux/arm64 darwin/arm64" build/build.sh dist/
```

**产物是多调用型的**（argv0 决定入口：`newgate` / `claude` / `opencode`…），
拿去跑之前先按正确的名字落一份：

```bash
cp dist/newgate-<发行版>-linux-amd64 /tmp/newgate && /tmp/newgate version
```

## 推之前

两个仓库各管各的测试，发行版的流水线里**第一项是内核的全部测试**：

```bash
cd core/go && GOPROXY=off go test ./...        # core-test：内核自己的（离线）
cd go && gofmt -l . && go vet ./... && go test ./...   # dist-test：发行版自己的
build/build.sh dist && bash mock/e2e_claude_dist.sh    # 端到端（零 token）
```

## 现有模块

| 模块 | 作用 |
| --- | --- |
| `hello` | 最小样例：Start 时打印一行 hello。零依赖，用来验证 external 通路是通的 |
| `simple-cli` | 极简界面：只渲染 `status` 与已注入的命令，用于验证「换掉界面，别的模块照常」 |
| `deepseek` | DeepSeek 的请求改写：思考模式要求逐字回传推理内容，客户端会剥掉/丢帧 → 补形状 |
| `glm` | GLM 的思维链回传与默认思考开关 |
| `claudecode_deepseek` / `claudecode_glm` | 客户端×模型的交叉语义（Claude Code 会剥 thinking 块等） |
