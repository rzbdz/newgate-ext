# newgate-ext —— 一个 newgate 发行版（顶层仓库）

newgate 的内核只做机制；**产品决策在发行版这一层**：装哪些模块、修哪家的怪癖、
按什么顺序修。这个仓库就是那个发行版，而且它是**顶层**——内核源码作为 submodule
钉在 `core/`，写模块、编二进制、发版本都在这里发生。

fork 它 → 改 `dist.json` 与 `modules/` → 就有了你自己的发行版。

## 目录

| 路径 | 是什么 |
| --- | --- |
| `core/` | **submodule**：内核源码（`github.com/rzbdz/newgate`），钉在一个提交上 |
| `modules/<名字>/module.go` | 本发行版的模块，形态与内核的 `modules/` 完全一致 |
| `dist.json` | **规格书**：本发行版由哪些模块组成、关掉内核的哪几个 |
| `dist-simple-cli.json` | 第二个规格书：同一个仓库的另一个变体（换掉界面） |
| `testing/` | 本发行版自己的测试（跑在内核的测试设施上） |
| `build/build.sh` | 唯一的构建入口：内核 + 本仓库 → 静态二进制 |
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

`build/build.sh` 写一张 Pin 指向**本仓库当前提交**，让内核把这份仓库 clone 进它的
`go/modules-ext/`，然后 `make static`。内核仓库根那份 `modules-ext.json` 是**默认
发行版**的配置，不是我们的——脚本不碰它，用 `NEWGATE_MODULES_PIN` 指一份临时的。

**构建编的是已提交的状态**（Pin 钉提交号），未提交的改动不在二进制里：改完先 commit。
要回退就先打个 WIP commit，别用裸 `git stash`。

## 一个必然的副作用

装配清单（`core/go/app/modules_gen.go`）跟着发行版走：编一次本发行版，内核那份
清单就被重写。`git -C core status` 会显示它被改过，**这是正常的**——别提交回内核，
`git -C core checkout -- go/app/modules_gen.go` 可还原（拉内核/换分支前先还原）。

## 推之前

本仓库的包**编进内核的 module**，所以它们的不合格会红在**内核**的 CI 上：

```bash
cd core/go && gofmt -l . && go vet ./... && go test ./...
```

## 现有模块

| 模块 | 作用 |
| --- | --- |
| `hello` | 最小样例：Start 时打印一行 hello。零依赖，用来验证 external 通路是通的 |
| `simple-cli` | 极简界面：只渲染 `status` 与已注入的命令，用于验证「换掉界面，别的模块照常」 |
| `deepseek` | DeepSeek 的请求改写：思考模式要求逐字回传推理内容，客户端会剥掉/丢帧 → 补形状 |
| `glm` | GLM 的思维链回传与默认思考开关 |
| `claudecode_deepseek` / `claudecode_glm` | 客户端×模型的交叉语义（Claude Code 会剥 thinking 块等） |
