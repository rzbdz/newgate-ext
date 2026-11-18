# 来自另一个 Claude 的架构建议（newgate-ext）

> 调查日期：2026-11-18。只读代码调查，未修改代码、未运行构建。
> 结论基于当天两棵树的状态：内核 `core/` **零模块**，发行版 `modules/` 42 个模块、5 份规格书。

---

## 0. 一句话结论

**2026-11-18 把内核清成“零模块”这一步，方向完全正确，而且消掉了原架构里最大的一笔维护成本。**

剩下的问题不再是“core 里混了产品模块”，而是：

> **模块的“来源”仍然只有一个——这个仓库的 `modules/` 目录。**

于是“扩展”与“加入这个产品的维护”仍然是同一件事。要真正支持生态，下一步不是再加分发层，而是把**模块来源**从目录扫描换成 git 模块清单，并让“官方 app”退化成一份默认清单。

---

## 1. 现状：哪些已经做对了

### 1.1 内核确实只剩机制

- `core/app/modules_gen.go:22` 生成出来的内核模块表是空的：`return []Entry{}`。
- `core/CLAUDE.md:67` 明确写着这条不变量由 `TestKernelShipsNoModules` 守着（`core/app/selection_test.go:25`）。
- `core/CLAUDE.md` 开头的判据是“换个发行版，这东西还该在吗？”，并且网关、熔断、界面、配置、客户端接入全部搬到发行版。
- 产品文档也迁到了发行版 `docs/`，内核只留框架、扩展、测试安全、本地化四篇。

这已经非常接近 Linux kernel / desktop 的分法，而且是**代码事实**，不是文档口号。

### 1.2 组合机制本身是资产，应该保留

- typed capability：`core/component/component.go` 的 `Capability` / `Need` / `Optional` / `Provide`。
- 注册可撤销：`component.Release`、`ReleaseAll`、`component.Registry`。
- 装配与启动分离：`core/component/plan.go` 的 `Plan` / `Resolve` / `Manager`。
- 构建期冻结顺序：`Precomputed`（`core/component/plan.go:60`）。
- 组合根接缝：`app.Selection`（`core/app/manifest.go:42`）。
- 模块契约测试：`testing/kernel/` 里的 `declared_test.go`（Start 里 `MustGet` 的端口必须在 `Requires` 里）、`matrix_test.go`（逐个摘模块）、`ordering_test.go`。

这些正是“模块能安全组合”的底层能力。**不要为了动态加载重写它们。**

### 1.3 发行版已经是独立 Go module

`go.mod` 用 `replace github.com/rzbdz/newgate => ./core`，内核版本只有 submodule gitlink 一个真相。这是对的，也让“改内核 / 改产品”能各自测试。

---

## 2. 现状：仍然存在的结构性问题

### 2.1 模块来源被写死在本仓库

`tools/distgen/main.go:45`：

```go
importPath = "github.com/rzbdz/newgate-ext"
```

`tools/distgen/main.go:123` 只扫描本仓库的 `modules/`；`main.go:330` 把目录名拼成 `modules/<dir>`。生成物 `manifest/modules_gen.go` 再把这些包**无条件 import 进二进制**。

所以今天“新增一个模块”的实际步骤是：

1. 把代码放进 `newgate-ext/modules/<name>`；
2. 改 `dist*.json`；
3. 跑 `go run ./tools/distgen` 并提交三份生成物；
4. 跟随本仓库的 core gitlink、CI、release 通道；
5. 由本仓库维护者替你发版。

这不是“模块仓库”，这是“成为发行版的一部分”。

### 2.2 42 个模块共享一个发布节奏

`dist.json` 已经列出 42 个模块名，`modules/` 下也是 42 个目录。后果：

- 任何一个模块需要 core 新接口，都要挪整个仓库的 core gitlink；
- 任何一个模块的行为变化，都可能影响默认产品的 `newgate plugin` 数量、i18n 账本、端到端脚本；
- 第三方无法只发布 `newgate-foo`，只能等 `newgate-ext` 发版。

### 2.3 模块之间直接 import 发行版的包

调查时 `modules/` 下有 277 个 Go 文件 import 了 `github.com/rzbdz/newgate-ext/modules/...` 或 `.../lib/...`。

这条边本身不一定错——能力契约就该被 import。问题是它把“模块”和“本仓库的 import path”绑死了。模块一旦拆仓，这些 import 必须改成指向真正的 owner 模块仓库，否则拆出去就编译不过。

### 2.4 规格书把“产品”和“开发夹具”混在一层

`dist.json` / `dist-dev.json` / `dist-dashboard.json` / `dist-simple-cli.json` / `dist-hello.json` 全部由 `distgen` 扫进同一个 `manifest/modules_gen.go`，再靠 `-ldflags -X distinfo.spec=...` 选一份。

其中 `dev`、`dashboard`、`hello` 更像是**验收夹具**或**骨架样例**，不是对外产品。它们和正式产品共用一套生成物、CI 和发布路径，会让产品仓库持续背上调试配置的维护面。

### 2.5 没有模块兼容版本的概念

`component` 里没有任何 `apiVersion` / 兼容范围字段（调查时 grep `Version|Compat|Deprecated` 无命中）。模块和内核之间只有“编译得过 / 编译不过”。

对于单仓库产品这够用；对于外部模块，这会让“我的模块支持哪个 newgate”只能靠用户自己试。

### 2.6 现有 `pluginmanager` 不是模块加载器

`core/modules` 已不存在，但发行版的 `modules/pluginmanager` 仍在。它的 `RegisterSelf` 管的是**模块内部的运行期功能开关**（`deepseek.tail-shape` 这类），不是安装、发现、加载或卸载模块。

同样，`web-dashboard` 的前端插件（`modules/web-dashboard/plugin.go`）也只接受已编译进二进制的模块注册的资源。

**不要把这两者误当成外部模块系统。**

---

## 3. 目标形态

```text
newgate（内核/SDK，可发布版本）
        │  稳定契约：component、entry、i18n、app.Selection、testing/*
        ▼
newgate-ext（官方 app：默认体验 + 安装器 + 发布渠道）
        │  只提交一份默认模块清单
        ▼
newgate-module-<name>（每个模块独立仓库、独立版本、独立 CI）
        │
        ▼
用户的 modules.json + modules.lock（组合属于用户）
```

四层职责：

| 层 | 负责 | 不负责 |
| --- | --- | --- |
| `newgate` | capability 框架、装配器、入口契约、SDK、测试工具 | 网关、界面、任何上游/客户端名字 |
| `newgate-ext` | 开箱即用的默认组合、安装升级、官方发布 | 承载所有第三方模块 |
| 模块仓库 | 自己的代码、测试、资源、版本、兼容声明 | fork 整个 app |
| 用户清单 | 选哪些 Git 仓库、哪个版本、启用哪些 | 复制一份 app 源码 |

---

## 4. 具体设计

### 4.1 模块清单（用户可改）

`newgate-ext` 提交一份 `modules.json` 作为默认值；用户可以在自己的目录放同名文件覆盖。

```json
{
  "core": { "source": "github.com/rzbdz/newgate", "version": "v0.2.0" },
  "modules": {
    "deepseek": {
      "source": "github.com/rzbdz/newgate-module-deepseek",
      "version": "v1.3.0"
    },
    "my-client": {
      "source": "git.example.com/team/newgate-my-client.git",
      "ref": "main"
    },
    "local-experiment": { "path": "./modules/local-experiment" }
  }
}
```

规则：

- `source` 支持 https、ssh 和本地路径；
- `version` 是 tag，`ref` 是开发分支，`path` 是本地目录；
- **只有 `version` 能进正式 lock**；`ref` / `path` 允许但会在 `module doctor` 里标成“不可复现”。

### 4.2 锁文件（可复现）

```json
{
  "lockVersion": 1,
  "core": {
    "source": "github.com/rzbdz/newgate",
    "version": "v0.2.0",
    "commit": "…40 hex…"
  },
  "modules": {
    "deepseek": {
      "source": "github.com/rzbdz/newgate-module-deepseek",
      "version": "v1.3.0",
      "commit": "…40 hex…",
      "digest": "sha256:…"
    }
  }
}
```

- 生成后默认只读；
- 构建和启动都只认 lock，不认“当前分支 HEAD”；
- `module update` 改 `modules.json` 再重算 lock，产出可 review 的 diff。

### 4.3 模块仓库自己声明身份

建议每个模块仓库根放一份 `module.json`：

```json
{
  "apiVersion": "newgate.dev/module/v1",
  "id": "deepseek",
  "entry": "./module.go",
  "provides": ["deepseek.family"],
  "requires": {
    "gateway": "^1.0",
    "plugin-manager": "^1.0"
  },
  "core": ">=0.2 <0.3"
}
```

`distgen` 从“扫描目录”变成“读清单 + 读模块声明”：

1. 解析 `modules.json`；
2. 按 `modules.lock` 取源码到缓存/工作区；
3. 校验每个模块的 `core` 范围与本仓库内核版本；
4. 校验 `requires` 能被满足；
5. 生成 `manifest/modules_gen.go`（import path 来自各模块仓库，不再写死 `newgate-ext`）。

### 4.4 两种构建模式

**A. 固定二进制（发布用）**

- 仍然由 `distgen` 生成静态清单；
- 仍然用 `PrecomputedOrder` 冻结启动顺序；
- 产物可复现、可签名、可离线部署；
- 用户新增模块 = 重新跑一次 `newgate build`，不是热加载。

**B. 外部模块启动（开发/自用）**

- `newgate` 启动时读取 lock，解析模块入口；
- 使用现有 `component.Resolve` 在启动前构图；
- 任何缺失能力、版本不匹配、重复组件都在**启动第一秒**报错；
- 顺序不预计算，规模在几十个模块时成本可接受。

**关键：第一阶段只做“启动期加载 + 重装配”，不做运行中热插拔。**

用户改完模块后重启/交接服务即可；这对 CLI、网关、守护进程都已经足够。

### 4.5 不要用 Go 原生 `plugin`

- 当前发布是 `CGO_ENABLED=0` 全静态（`build/build.sh:133`）；
- `plugin` 包要求模块与主程序的 Go 版本、依赖版本、编译参数严格一致；
- 跨平台（linux/amd64、arm64、riscv64、darwin/arm64）会立刻变成支持矩阵。

用“源码模块 + 构建期生成”，或者“启动期加载编译好的独立入口”，都比 `plugin` 稳。

### 4.6 版本兼容要分三层

1. **模块 ↔ 内核**：`core: ">=x.y <z"`，在解析阶段拒绝；
2. **模块 ↔ 模块**：`requires: {id: "^1.0"}`，在解析阶段拒绝；
3. **用户配置 / CLI / HTTP / UI**：单独写兼容说明，不要混进上两层。

对内保持 `component` 的 `New() Component` 形态不变；对外只承诺 `api.go` 中导出的 service 和 `component` 的公开 API。

### 4.7 官方 app 的收敛

`newgate-ext` 应该只剩：

- `modules.json` + `modules.lock`；
- 安装器 / 自更新 / 发布脚本；
- 默认组合的验收测试；
- `docs/`（用户文档）。

42 个模块的绝大多数应该迁到各自仓库。`dist-dev.json` / `dist-hello.json` 这类夹具可以留在 `testing/` 或 `examples/`，不再进入产品发布规格。

---

## 5. 迁移顺序（建议）

### 第一批：先把“来源”抽象出来，不改行为

1. 新增 `modules.json`，内容等价于今天的 `dist.json` 模块列表；
2. 新增 lock 生成逻辑，但先让所有 source 指向本仓库（`path: ./modules/<name>`）；
3. `distgen` 改成“读清单 → 解析 path → 生成同样的 import”，产物不变；
4. CI 增加一条“lock 与 modules.json 一致”的检查。

这一批不改变任何运行行为，但把“模块来源”从隐含的目录扫描变成显式数据。

### 第二批：拆出 2–3 个低耦合模块做样板

优先选依赖少、边界清楚的模块，例如 `theme-dark`、`theme-light`、`nothink`、`hello`。

1. 建独立仓库 `rzbdz/newgate-module-<name>`；
2. 把 `module.go` / `api.go` / 测试 / 资源搬过去；
3. 从 `newgate-ext/modules/` 删除；
4. `modules.json` 改成 `source + version`；
5. `distgen` 从 git 拉取并生成。

这一步会暴露所有隐藏耦合（import path、i18n 账本、测试设施），也是最有价值的验证。

### 第三批：官方 app 只保留默认组合

1. 把 `dev` / `dashboard` / `hello` 规格移到测试或示例；
2. `newgate-ext` 的默认 `dist.json` 变成 `modules.json` 的一个视图；
3. Release 产物仍由默认 lock 构建，但用户可以拿同一套工具构建自己的组合。

### 第四批（可选）：启动期外部模块加载

1. 支持 `newgate run --modules ./modules.json`；
2. 复用 `Resolve` 做启动前校验；
3. 在 `newgate doctor` 里报告 lock、版本、能力缺口；
4. 再评估是否需要“运行中加载”。

---

## 6. 明确的“不要做”

1. **不要新增 distribution 分支或发行版仓库。** 每加一个 distribution，就多一份要同步的模块表和发布流程。
2. **不要用 Go `plugin` 作为第一版方案。** 静态多平台发布与它冲突。
3. **不要做运行中热卸载。** `Manager` 现在拥有整张图，模块的 Stop 也不保证能撤销后台 goroutine / 硬件句柄。先做“启动期组合”，再谈热插拔。
4. **不要把 `pluginmanager` 当成模块系统。** 它是功能开关账本。
5. **不要把模块兼容性检查放进 `component`。** 内核只提供机制；版本策略属于 app / 模块工具。

---

## 7. 风险与取舍

- **拆仓库会增加“取源码”的复杂度。** 用缓存目录 + lock 固定 commit，可以把它变成一次性的 `module sync`。
- **生成物仍然要提交。** 这样 CI 和离线构建仍然成立；lock 只是多一份可复现的来源记录。
- **模块间 import 需要迁移。** 建议先保留一个过渡包 `newgate-ext/lib/contracts`，等模块拆完再删。
- **测试设施是跨仓库 API。** `testing/testkit`、`testing/system`、`testing/kernel` 一旦对外，就要按 semver 对待。

---

## 8. 证据索引

| 事实 | 位置 |
| --- | --- |
| 内核模块表为空 | `core/app/modules_gen.go:22` |
| 内核零模块不变量 | `core/app/selection_test.go:25`、`core/CLAUDE.md:67` |
| 组合根接缝 | `core/app/manifest.go:42` |
| 装配/启动分离与预计算 | `core/component/plan.go:60` |
| capability 与生命周期 | `core/component/component.go` |
| distgen 写死发行版 import path | `tools/distgen/main.go:45` |
| distgen 只扫本仓库 modules/ | `tools/distgen/main.go:123`、`:330` |
| 五份规格书 | `dist*.json` |
| 静态多平台构建 | `build/build.sh:133` |
| 模块契约测试 | `testing/kernel/declared_test.go`、`matrix_test.go`、`ordering_test.go` |
