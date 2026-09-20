# HANDOFF —— 2026-09-20 那次重构的交接

这份文件是**一次性的**：它记的是「上一轮结束时世界长什么样、还剩什么没做」，不是
长期规矩（长期规矩在 `CLAUDE.md`，每次会话都会自动读）。做完下面「遗留」那几条，
这份就可以删了。

## 这次做完的

**内核只做机制，产品决策进发行版。** 四个落点：

1. **core 只剩内核**：上游怪癖模块（`deepseek` / `glm` / `claudecode_deepseek` /
   `claudecode_glm`）搬进本仓库；客户端接入（claudecode / opencode）与内核机制留在
   core。判据：*换个发行版，这东西还该在吗？*
2. **发行版是顶层**：`core/` 是 submodule，`modules/` `testing/` `dist.json`
   `build/build.sh` 是发行版自己的。所有编译在这里发生。
3. **只有 built-in 不可摘**：`core/go/app/matrix_test.go` 逐个模块摘一遍
   （顺手抓出 `modules/cli` 在 Start 里 `MustGet` 一个自己没声明的端口，已改成
   fail-open；硬依赖由 `modules/wrapper` 声明 `Need(entry.Capability)`）。
4. **两条棘轮**守方向：组合根不认识任何模块（`app/direction_test.go`）、
   数据面不认识任何策略（`modules/gateway/direction_test.go`）。

## 结束时的状态（这一节最容易过期，先看它）

| | 值 |
| --- | --- |
| 内核 `main` | `d9040f1`（CI 绿：run 35487040324） |
| 内核钉的发行版（`modules-ext.json` 的 `revision`） | `953831f` |
| 发行版本仓库 `main` | `f3f7a47`（release 流水线绿：run 35487187903） |
| 发行版 `template` 分支 | `2c84661`（只剩 `hello` + `simple-cli`，fork 骨架） |
| 本目录的 `core/` submodule | `b87578d` |
| 线上 daemon | pid 见 `~/.config/newgate/.newgate.pid`，二进制 md5 `348bd1d3…` |

**Pin 落后于发行版 main 是有意的**（`953831f` → `f3f7a47` 之间是两个纯文档提交）：
Pin 是一次**发布决定**，不是「跟着 main 跑」。要同步：改 `modules-ext.json` 的
`revision`，回到内核仓库跑 `make generate`。

## 遗留

1. **`docs/06-reasoning.md` §2b 那句话指错了表**（在内核仓库里）。它写「判据见 §1 的
   表」，而 §1 那张三条件表是**诊断**判据（「这个 400 像不像尾部形状问题」），
   `repairTailShape` 实现的是**修复**判据（两条件：最后一条 user 消息的 `content[]`
   非空且全是 `tool_result`，且其后没有 assistant）。第三方条件（中途有别的 request
   id）**刻意没实现**——实测 3426 次尾部修复、0 次之后真的撞上 `must be passed back`，
   过修一次只花一个「继续」，欠修一次是 400 + 静默换链。**代码是对的，文案要分开写。**
2. **`make build` 会重跑 generate**：换发行版必须把 `NEWGATE_MODULES_PIN` 给**整个
   make 调用**，只给 `make generate` 那一发没用。
3. **「改完直接编」还没做到**：构建编的是已提交状态（Pin 钉提交号）。要消除这个摩擦
   得让 `core/go/modules-ext` 变成指回本目录的符号链接；代价是 `go test ./...` 不
   跟进符号链接（测试要显式点名 `./modules-ext/...`），且内核的 `.gitignore` 里
   `/go/modules-ext/` 的尾斜杠要去掉（否则符号链接不算被忽略）。**没做，是个选项。**
4. **git 老提示 `git prune`**（内核 worktree 有 1.6 万个不可达对象 + 一个 `gc.log`）：
   建议**不** prune（对象保留期是安全网），清掉
   `/root/workspace/newgate/.git/worktrees/newgate-ponyfish/gc.log` 就够了。

## 记忆（可选）

本次会话积累的记忆在这个路径下（按项目目录名派生）。想带到新目录：

```bash
mkdir -p ~/.claude/projects/-root-workspace-newgate-ext/memory
cp ~/.claude/projects/-root-workspace-newgate/memory/*.md \
   ~/.claude/projects/-root-workspace-newgate-ext/memory/
```

里面有几条还写着内核仓库那边的路径（「线上源码在哪个 worktree」之类），过去之后按
新上下文改一下。
