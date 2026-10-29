# web-dashboard

浏览器界面 + 它的 BFF。挂在 gateway 那个端口（默认 8899）的 `/ui` 前缀下。

## 三件事，各有其人

| 谁 | 管什么 |
| --- | --- |
| 内核的 `modules/porthub` | 那个端口归谁用。本模块只负责 `Mount("/ui", …)` |
| 本模块的 `bff.go` | 四件事：发概念账本、把修改转交给概念的主人、转交扩展端点、发静态资源。**不认识任何模块** |
| 各业务模块 | 自己那一面长什么样、怎么写回去（`core/lib/view`，Optional 依赖） |

加一个模块的界面 = 那个模块在自己的 `Start` 里 `Register`，本包与前端都不用改
（前端只认 Kind，见 `web/src/api.ts`）。

### 第二条口子：`/api/ext/<name>`

概念账本只装**画得成一张卡**的东西。有一类功能不是一张卡——比如「点开模型下拉，
问一句这家上游此刻提供哪些模型」：它没有可持久化的形状，也不该在每次快照里碰
网络。这种时候模块 Optional 依赖本模块的 `web-extension` 端口，在自己的 `Start`
里登记**一个名字 + 一个函数**：

```go
rel, err := web.Register("modellist", func(r webapi.ExtensionRequest) (any, error) {
    // 形状由你定义；BFF 原样把请求体交给你，把你的返回值写成 JSON。
    return myAnswer, nil
})
```

前端 `POST /ui/api/ext/<name>`，体是 JSON。BFF 只做四件形状上的事：认 POST、
认 `application/json`、找名字、转交——它仍然不认识任何模块。没登记的扩展回 404
（界面据此回落到自己能拿到的那份数据），扩展返回 error 时回 400 并把那句话原样
给界面。今天的第一个使用者是 `modules/modellist`（模型选择器的清单来源）。

### 第三条口子：模块自带前端（插件）

前两条口子都要求**前端事先认识形状**（Kind、或者某个扩展端点的回答）。一个模块要
一块前端不认识的界面时，它不该往 `web/src` 里加组件，而是**自带前端代码**：

```go
//go:embed web
var webFiles embed.FS

plugins.RegisterPlugin(webapi.Plugin{
    Name:   "self-update",
    Assets: sub(webFiles, "web"),   // 模块自己的 ES module + 静态文件
    Entry:  "plugin.js",            // export function mount(el, host) { …; return unmount }
    Slots:  []webapi.PluginSlot{webapi.SlotTopbar},
})
```

BFF 在 `/api/plugins` 发清单、在 `/plugins/<名字>/…` 发文件；界面（`PluginSlot.svelte`）
在自己版面的**槽位**里给每个插件一个空容器和一个 host（`ext()` 调扩展端点、`asset()`、
`lang`）。界面认识的只有槽位与这份契约；插件的文案由它自己的扩展端点给（后端翻译），
样式用设计令牌。一个插件挂不上只丢它自己那一格。今天的槽位只有 `topbar`，要别的
（侧栏一节、整页）就在这里加一个槽位——那是给模块的**更高自由度的定制点**，不是让
界面去认识模块。第一个使用者是 `modules/self-update`（顶栏的「更新」）。

### 分层：界面 ← 包装层 ← 模块

- **界面（本模块）**只提供框架接口：Kind、槽位、插件契约、扩展端点的转交。不认识
  任何模块。
- **单个模块**的界面：它自己带插件（上面那条）。
- **多个模块拼起来的界面**（模型下拉要 modellist 的清单 + verify 的指纹）：不写进
  界面，也不让某一个业务模块去依赖另一个——放进一层**包装模块**，它把那几个模块当
  数据上游（Optional 依赖它们的端口），聚合之后一次注入给界面。抽象层次不清楚的时候
  就再加一层，而不是让界面越界。

### 谁越界了（2026-10-29 盘点，待迁）

`boundary_test.go` 的棘轮守着「前端源码不许写死扩展端点名」，下面前两条在它的名单上
（**只减不增**）；后几条它查不出来，列在这里：

| 在哪 | 越了什么界 | 该去哪 |
| --- | --- | --- |
| `kinds/Fingerprints.svelte` + `api.ts` 的 fingerprint 类型与 `/ext/fingerprint` | 整个指纹页是 modules/verify 的界面 | verify 自带插件（需要一个「整页」槽位） |
| `ModelPicker.svelte` 直接问 `/ext/modellist` 与 `/ext/fingerprint` | 模型下拉认识两个模块 | 界面定义一个「模型来源」端口；包装模块聚合 modellist + verify 去实现它 |
| 内核 `lib/view` 的 `KindFingerprints` | 内核认识一个发行版模块的形状 | 随指纹页迁成插件后删掉 |
| `kinds/Overview.svelte` 按动作 id 前缀 `probe` 分支 | 认识 home 那几个动作的命名 | 动作自己声明「会花多久 / 要不要转圈」这类语义字段 |
| `App.svelte` 顶栏只画 id 为 `fallback` 的那个节动作 | 认识某个模块的动作 id | 动作声明「要上顶栏」（一个 placement 字段），界面按字段摆 |

## 本地调试：怎么在浏览器里打开

**不要动线上那个 daemon**（它跑在 8899，你自己的请求也穿行在其中）。起一个沙箱
实例，用一个别的端口：

```bash
# 1. 一份最小配置（放在别处，别碰 ~/.config/newgate）
mkdir -p /tmp/ngui/home/mappings
cat > /tmp/ngui/home/providers.json <<'JSON'
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:9/demo","api_key":"sk-x"}}}
JSON
cat > /tmp/ngui/home/mappings/demo.json <<'JSON'
{"description":"debug profile","roles":{"normal":[{"provider":"demo","model":"demo-model"}]}}
JSON

# 2. 编一份产物，**按正确的名字落一份**（argv0 决定这次调用归谁）
build/build.sh
mkdir -p /tmp/ngui/bin && cp dist/newgate-default-linux-amd64 /tmp/ngui/bin/newgate

# 3. 起它
cd /tmp/ngui && NEWGATE_HOME=/tmp/ngui/home HOME=/tmp/ngui ./bin/newgate __serve --port 8901
```

然后打开 <http://127.0.0.1:8901/ui/>。`providers.json` 里那个 `api_key` 在界面上
应当显示成 `***`，而且那份文件是**只读**的。

改前端：`cd modules/web-dashboard/web && pnpm install && pnpm dev`（vite 的 dev
server 自己起在 5173，把 `/ui/api` 代理到上面的 8901 就能热更新），改完
`pnpm build` —— **产物要提交**（Go 的 `go:embed` 读它，`go build` 才能离线）。

## 装配：入口有三种形态，`newgate web` 会告诉你这次是哪种

**两条独立的轴**，别混：一条是「谁当终端界面」（`cli` / `tui` / `simple-cli`），
另一条是**这个界面自己从哪儿被服务**：

| porthub | serving | 界面在哪儿 | 这次装配的样子 |
| --- | --- | --- | --- |
| 在 | 任意 | 共享端口 `/ui`（与数据面同一个端口） | 常态 |
| 不在 | 在 | **自己的端口** `http://127.0.0.1:8898/ui/`（`NEWGATE_WEB_PORT` 可改） | 退路 |
| 不在 | 不在 | 没有入口 | `newgate web` 会说清楚为什么 |

退路只在**服务进程**里生效（登记在 `lib/serving` 那本账上，由拥有端口的一方在
开始服务时通知）——CLI 进程里一次都不会开监听。它丢掉的正是当初要共享端口的那个
理由（防火墙/反代/ACL 都按端口配），所以它是退路而不是常态。

终端界面那一轴与它无关，各自成立：默认（`cli`/`tui` + 本模块）是终端与浏览器各
一份；关掉内核的 `cli` 时浏览器照常、终端只剩最小渲染（`simple-cli` 顶上来）；
**不装本模块**则完全没有 web 入口，`/ui` 掉进数据面的 catch-all（与任何未知路径
一样）。这些组合都成立，是因为本模块的依赖**全是 `Optional`**。

**没有终端界面的装配里，浏览器是唯一的入口**（`dist-dashboard.json` 就是这么一份：
它关掉内核的 `cli`）。那时：

```bash
newgate --port 8899      # 无子命令 = 起服务（只有本入口自己的 flag 时不认领别的东西）
```

这条曾经不成立：`__serve` 原来是 `cli` 的一条命令，于是关掉 `cli` 的装配编得出来
却起不来（`no entry claimed this call`）。2026-09-20 之后守护进程本体是一个**入口
申报**（`component/entry`），与界面装不装无关；同一个改动还让「无参数 = 起服务」
在**没有终端界面**时成立——那时没有子命令可派，这个二进制唯一说得通的行为就是
服务。带别的参数（`newgate frobnicate`、`newgate --help`）仍然会得到「没人认领
这次调用」那句人话。

## 谁能碰它

它**能改配置、能拨运行期开关**，所以两道门是硬的（都在 `bff.go` 里，各有注释）：

1. **只答本机的 Host**（127.0.0.1 / localhost / ::1）。只监听 loopback 挡得住外面
   的连接，挡不住别人网页上的一段脚本：攻击者把自己控制的域名解析到 127.0.0.1
   （DNS rebinding），浏览器就认为同源。浏览器唯一不骗人的是它自己填的 Host。
   代价：在 `/etc/hosts` 里给自己起名的人会被挡在外面，报错里写了该用什么地址。
2. **写操作必须声明 `Content-Type: application/json`**。跨站表单与 no-cors 的
   fetch 发不出这个类型（会触发预检，而我们不回 CORS 头），但它们能发一个 body
   长得像 JSON 的 text/plain——而解码器不看 Content-Type。这条是那条路唯一的墙。

**残留风险说清楚**：本机的**任何进程**都还能直接读写这些端点（loopback 的边界
就是本机）。所以它和 `providers.json` 同级看待——凭据在那里脱敏、带凭据的文件
在界面上只读，理由都是这一条。

## 待办

- **打开浏览器**：`newgate web` 只打印地址，不替用户 `xdg-open`（跳板机上那种调用
  会挂住）。要加的话得先想清楚「没有图形界面时」的表现。
- **footgun 的时限选择**：界面上那些开关点里，`DangerFootgun` 的被放进一张**只读**
  卡片（写它们必须带时限，而这一版没有选时限的界面）。今天没有任何模块声明 footgun，
  所以这条是给将来铺路——现在无法用真实数据验证。
- **`gateway.special` 还不能写**：它是只读的，而**关掉 cli 的那份装配里
  `newgate st on|off` 也不存在**——那里要开关一个补丁，只能去 `config.file.state.json`
  那张 code 卡上手工改 `ModuleConfig["gateway"]`。补上它需要一条 CAS 写
  （`store.WriteIfUnchanged`，把 `StaleError` 翻成 `view.Conflict`），与 footgun 那条
  是同一类活。今天可以用真实数据验证（这份装配里注册了六个补丁）。
- **非 loopback 的暴露**：现在只答本机。要开放给别人用，得先有认证（控制令牌是现成
  的一半），见 `docs/12-newgate-remote.md` 那条路线。

## 今天有哪些卡片

| 概念 ID | Kind | 谁贡献的 | 能写吗 |
| --- | --- | --- | --- |
| `config.file.*` | `code` | config | `providers.json` 只读（凭据），其余可写 |
| `config.profile.*` | `mapping-editor` | config | 能 |
| `config.state` | `toggles` | config | 能 |
| `gateway.metrics` | `series` | gateway | 只读 |
| `gateway.log` | `log` | gateway | 只读 |
| `gateway.special` | `table` | gateway | 只读：**哪些补丁在动你的请求**（两种「关掉」各有各的说法与颜色；关掉了 cli 的装配里这是唯一的答案） |
| `claudecode.classifier` | `table` | claudecode | 只读：**Bash 分类器此刻被怎么对待**（裸奔关着 / 限时 / 永久；永久才画红）。同样是关掉 cli 的装配里唯一能看见它的地方 |
| `plugin-manager.switches` | `toggles` | plugin-manager | 能 |
| `plugin-manager.footguns` | `toggles` | plugin-manager | 只读（要带时限，见上） |
| `plugin-manager.modules` | `table` | plugin-manager | 只读：**这个构建由哪些模块组成** |
| `breaker.health` | `table` | breaker | 只读 |
| `runtime.takeover` | `table` | runtime | 只读：**谁在走 newgate**（四态：生效 / 声明了没生效 / 关过却没放开 / 直连） |
| `opencode-omo.slots` | `table` | opencode-omo | 只读：每个 omo 槽位现在的绑定（写归属只有 `newgate omo use` 一份语义） |

这张表是**产物**不是清单：加一个模块的那一面不需要改它，就像不需要改前端一样
（前端只认 Kind，账本只认注册）。写在这里是为了让人一眼看到「今天能看什么」，
别当成需要维护的名单。

`table` 那几张（今天六张）是 2026-09-20 加的：形状定义在 `core/lib/view`（列 + 行，行按**列 ID**
索引而不是数组下标，所以列顺序是纯展示的事），前端 `kinds/Table.svelte` 认它。
第二个贡献者（模块清单）一行前端代码都没改——那正是把它做成契约而不是一次性
渲染器的理由。
