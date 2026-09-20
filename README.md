# newgate-modules-ext —— 一个 newgate 发行版

这是 newgate（内核）的一个**发行版仓库**：模块、发行版的规格书、以及把这个发行版
造出来的构建脚本都在这里。fork 它 → 改 `dist.json` → 就有了你自己的发行版。

## 目录

| 路径 | 是什么 |
| --- | --- |
| `modules/<名字>/module.go` | 这个发行版自己的模块。形态与内核 `modules/` 里完全一致 |
| `dist.json` | **规格书**：这个发行版由哪些模块组成、关掉内核的哪几个 |
| `dist-simple-cli.json` | 第二个规格书：同一个仓库的另一个变体（换掉界面） |
| `testing/` | 这个发行版自己的测试（跑在内核的测试设施上） |
| `build/release.sh` | 从内核源码编出本发行版的静态二进制 |

## 两张纸：谁决定装什么

    "我这次构建用哪个发行版？"      → 内核仓库根的 modules-ext.json  （Pin）
    "这个发行版由哪些模块组成？"    → 本仓库根的 dist.json           （Spec）

Pin 是**构建者**的，Spec 是**发行版作者**的。分成两张纸是为了让 fork 有意义：把
Spec 放在内核仓库里的话，你 fork 本仓库加了模块，却还得回去改内核仓库的文件才
能启用它。

Pin 只钉三件事：`repo`（本仓库地址）、`revision`（哪一版，提交号/tag/分支）、
`spec`（规格书文件名，默认 `dist.json`）。构建期内核会按它把本仓库 clone 到
`<core>/go/modules-ext/`——**没有 submodule**：那东西会把「用哪个发行版」写进
内核的版本控制，而没有 ssh key 的人连内核都 clone 不下来。

## 造一个发行版

```bash
git clone <你 fork 的这个仓库> my-dist && cd my-dist
$EDITOR dist.json                     # 选模块、关掉不要的
build/release.sh                      # 编出 dist/newgate-<发行版>-<平台>-<架构>
```

`build/release.sh` 会：读 `dist.json` → 按里面的 `core` 拿一份内核源码（或直接用
`$NEWGATE_CORE` 指的那份）→ 写一张 Pin 指向**本仓库当前提交** → `make static`。
`.github/workflows/release.yml` 是同一个脚本的 CI 版。

## 分支

| 分支 | 是什么 |
| --- | --- |
| `main` | **官方发行版**：内核之外我们维护的模块（上游怪癖修补、客户端×模型交叉语义） |
| `template` | 给别人 fork 的骨架：只有 `hello` 与 `simple-cli` 两个样例模块 |

## 推之前

本仓库的包**编进内核的 module**（checkout 在 `<core>/go/modules-ext/`），所以它
们的不合格会在**内核**的 CI 里红。推之前在本机内核 checkout 里跑一遍：

```bash
cd <core>/go && gofmt -l . && go vet ./... && go test ./...
```

## 现有模块

| 模块 | 作用 |
| --- | --- |
| `hello` | 最小样例：Start 时打印一行 hello。零依赖，用来验证 external 通路是通的 |
| `simple-cli` | 极简界面：只渲染 `status` 与已注入的命令，用于验证「换掉界面，别的模块照常」 |
