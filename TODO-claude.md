# TODO-claude.md

给**下一个接手的人**（人或 agent）的状态交接。写于 2026-09-22。

## 一句话现状

仓库**可编译、可测试、全绿**；内核那半边（链的读端口 + `chains` 这个 Kind）已经
改完、推上去了。剩下的两件事都在**发行版这一侧**（外加前端一小步），见下面。

```bash
gofmt -l .                                  # 干净
GOPROXY=off go vet ./...                    # 干净
go run ./tools/distgen -check               # 干净
go run github.com/rzbdz/newgate/tools/i18n check -root . \
  -catalogs modules/i18n/catalogs -allowlist tools/i18n-allowlist.json
GOPROXY=off go test ./...                   # 全过
```

## 已经做完的（别重做）

**内核**（`core/` = submodule，gitlink 已挪到 `47475dc`，那个提交**已推到
`origin/main`**）：

- `lib/view`：新增 `KindChains = "chains"` 与 `Chains`/`ChainCard`/`ChainRow`/
  `ChainStep` 四个形状（`core/lib/view/view.go:68`、`:316` 起）。
- `modules/config`：`Config` 端口上新增**读**方法 `Chains(profile, keys...)`
  （`core/modules/config/api.go:96`、`core/modules/config/chains.go`），
  实现 + 9 条单测在 `core/modules/config/chains_test.go`。它一次快照算完全部档位，
  `MaxSteps: 0`（不按 maxAttempts 剪链），profile 不存在报错而不给空链。
- `i18n` 账本重新生成过（943 条）。

**发行版**（本仓库，**已提交**）：

- `modules/web-dashboard/web/src/kinds/Chains.svelte` —— `chains` 这个 Kind 的
  渲染器（新文件）。
- `web/src/ConceptCard.svelte` 加了一支分派（`{:else if concept.kind === "chains"}`），
  `web/src/api.ts` 的 `Kind` 联合类型加了 `"chains"`，`web/src/i18n.ts` 加了两句
  （`no steps` / `no profiles`，en + zh-Hans 两边）。
- `web/dist` 已重新 `pnpm build`。

  为什么这一小步不能省：`modules/web-dashboard/kinds_test.go` 的
  `TestEveryDeclaredKindHasARenderer` 会在「内核声明了 Kind、前端没有渲染分支」时
  变红。内核 gitlink 一挪过去，本仓库的测试就红了——**这就是它该红的样子**，
  别去改那条测试。

- `site/src/site.css`：演示窗口（`.browser iframe`）从写死的
  `height: 640px`（窄屏 480px）改成 `aspect-ratio: 4 / 3; max-height: 600px`，
  并**删掉了** `@media (max-width: 820px)` 里那条 `height: 480px`
  （`height` 与 `aspect-ratio` 同时给时高度赢，那条会把比例悄悄废掉）。
  另外 `--wrap` / `--prose` 两条宽度与「共用同一条左轴」的说明。

  注意这条只改了**框**。框里现在是老的演示页（三张卡），见下面第 2 件事。

- `site/src/zh-Hans/index.md`：删掉 hero 下面那段 tagline（大字报下面再接一段
  解释，读起来像在给自己打圆场）。

## 待办 1：`modules/home` —— 控制台里的聚合主页（计划的第 6 步）

**方向**（用户定的，别再问）：控制台今天是一张张卡平铺（实测 4303px ≈ 4.8 屏），
改一个开关要滚三屏。要做的是**一屏看到我这些链**：一份 profile 一张卡，卡里一档
一行（`heavy → ark/deepseek-v3`），行上带「换成谁」的按钮，就地切。

**内核那一半已经就绪**，这一半是纯发行版模块，新建 `modules/home/`：

```
module.go   // New()；Need(configapi.Capability)；Optional(viewapi.Capability)；
            // Start 里注册那一节（照 modules/codex/view.go 的写法）
chains.go   // 聚合：configapi.Chains() 的结论 → view.Chains。纯函数，可单测
actions.go  // 行上的动作：把某一档换到另一个候选（见下面「写路径」）
view.go     // Concept{ID:"home.chains", Kind: view.KindChains, Order:…}
```

几条**已经核过、别再重查**的事实：

- `configapi.Config` 现在的形状（`core/modules/config/api.go`）：
  `RegisterRoleProvider`（写）+ `Chains(profile string, keys ...string) (*Chains, error)`
  （读）。`Chains.Profile/Default/Keys`，`Chain.Key/Steps/Skips`，
  `Step`/`Skip` 是 `configapi` 上的别名（不用去 import `resolve`）。
- `Skip{Profile, Target, Reason, Kind}`：`Reason` 已经是给人看的那句话，
  `Kind` 是机器标记（`excluded`/`undefined`/`no-key`/`unavailable`/`disabled`/
  `max-attempts`/`duplicate`/`cycle`/`other`，定义在
  `core/modules/config/resolve/chain.go:51` 起）。**分组按 Kind，展示按 Reason。**
- 默认落点那一格**已经全通**：`view.Section.Default` → `.Landing()`（`view.go:698`）
  → `SectionInfo.Default`（`view.go:722`，多个人声明时取 Source 最小的那个）
  → BFF `sectionDoc`（`modules/web-dashboard/bff.go:254`）
  → `api.ts` 的 `Section.default` → `App.svelte` 的 `defaultRoute()`。
  所以 `modules/home` 只要在它那一节上调一次 `.Landing()` 就行，**不用改前端路由**。
- 动作**走 `onRowAction` 那条路**，不是 `Concept.Actions`。理由写在
  `Chains.svelte` 的 props 注释里：行上的动作改的是**这张卡里的一行**，跑完卡还在
  而这一屏上的链都变了，重读快照就对；卡上的动作（`RunConceptAction`）是给
  「把这一份设为默认」那种改完**要换一张卡**的事情用的。
  前端已经把行 ID 拼成 `` `${profile}\u0000${tier}` ``（`\u0000` 在档位名里不出现，
  所以它是一道分不开的分隔符）——**后端在 `RunRowAction(conceptID, rowID, actionID)`
  里按 `\u0000` 拆**。这条契约目前只有 `Chains.svelte` 的注释写着，两边实现时
  **要一起写、一起测**。

**写路径**（还没做，落笔时按这个来）：`configapi` **没有**暴露「改某一档的绑定」
这个方法，所以要么

- 走 `modules/codex/modelsview.go` 那条已经验证过的路：`store.LoadProfileRaw(name)`
  拿原文 → 读进 `map[string]json.RawMessage`（保住不认识的键）→ 只改那一档 →
  `store.WriteIfUnchanged(file, base, …)`（CAS）→ 失败时把 `*store.StaleError`
  翻成可读的冲突话术。`modules/config/{store,paths,domain,resolve}` 是**共享叶子**
  （`core/CLAUDE.md` 明说），发行版直接 import 不算破例；
- 或者**给 `configapi` 再加一个写方法**（更干净，但要再动一次内核 + 再推一次
  gitlink）。两条都行，选哪条取决于你愿不愿再动内核。

**还没做的小事**：`dist.json` 加 `home`（`dist-dev.json`、`dist-dashboard.json`
也该加——`dashboard` 那份没有终端界面，主页在那里最有用）→ `go run ./tools/distgen`
→ `manifest/modules_gen.go` 一起提交。

## 待办 2：站点上的演示页要换成一整个前端

用户的第二件事，**原话**：「把 preview 换成我们的完整版前端，只是数据都是 mock、
无真实有效」。现在框里是 `modules/web-dashboard/web/demo/`（第二个 vite 入口，
可点、喂假数据、点「应用」弹一句「这是演示」），但它只摆了**三张卡**
（`codex.models` / `codex.model` / `runtime.takeover`），不是一整个界面。

还差的：

- `demo/data.ts` 要造出一份**完整的** `Snapshot`（现在只有两节三张卡）；
- 演示页要能显示**侧栏 + 全部节**，不是一张 tab 条；
- 用户点名的第三件事：**加一个 overview 聚合页**（见待办 3），演示页上要能点到它。

前两件事的做法与取舍写在 `modules/web-dashboard/web/vite.demo.config.ts` 的注释里
（为什么是第二个入口而不是第二个工程、为什么 outDir 指到 `site/dist/demo`、
为什么 `site/dist` 不进版本控制），先读那一份。

## 待办 3：聚合 overview 页（用户点名，要「彻底抄」cc-switch）

参考图在 **`/tmp/ccs/reference.png`**（原来是仓库根的 `cc-switch.png`，未跟踪，
我已经挪到 /tmp 了——**别把它提交进去**，它是取证素材不是仓库内容）。
裁好的局部图也在 `/tmp/ccs/`（`01-sidebar.png`、`02-top.png`、`03-main-left.png`、
`04-main-right.png` 等）。

用户的要求原话是「截图里面到底有什么效果，有什么 icon 之类的东西，你一定要搞清楚！
然后彻底抄袭」，以及「你他妈抄袭都抄袭不到位」（上一版做得太水）。所以这一件事
**第一步是取证**：把 `/tmp/ccs/reference.png` 连同那几张裁图**重新看一遍**，把
「有哪些 icon、每个 icon 表达什么、状态徽章长什么样、卡片怎么排、间距与层级怎么
走」列成一张清单，再动手。没有那份清单就开始写代码，做出来又会是三脚猫。

用户对预览区还钉了两条：**4:3、响应式**（框那半边已经做完，见上面），
以及演示里**只让它显示 overview 一页**（其他导航项不可点）——注意这条说的是
**站点上那张嵌进去的演示**，不是产品里。

---

## 踩过的坑（省你一次）

- `command grep`，别用裸 `grep`（本机是 ugrep 包装，会静默吞结果）。
- 前端改了源码**必须** `cd modules/web-dashboard/web && pnpm build` 再提交
  （`web/dist` 进版本控制，`web_test.go` / `i18n_test.go` 两条棘轮看着它）。
  演示页是另一个入口：`pnpm build:demo`。
- `modules/web-dashboard` 的测试里 `testkit.Sandbox(t)` **每调用一次就换一个临时
  目录**——同一个测试里调用两次，前面 seed 的文件会落在上一份目录里
  （症状：「明明写了 demo.kv 却说没有这份档位」）。要 seed 多个文件，自己开一次
  Sandbox 然后直接写 `paths.Root()`。例子见 `core/modules/config/chains_test.go`
  的 `seedProvidersIn`。
- 内核改了要**先在内核仓库提交并推**，再回本仓库 `git add core` 挪 gitlink。推一个
  没推上去的 gitlink，CI 会在 checkout 那一步整体失败，一个 job 都不起。
