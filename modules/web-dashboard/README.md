# web-dashboard

浏览器界面 + 它的 BFF。挂在 gateway 那个端口（默认 8899）的 `/ui` 前缀下。

## 三件事，各有其人

| 谁 | 管什么 |
| --- | --- |
| 内核的 `modules/porthub` | 那个端口归谁用。本模块只负责 `Mount("/ui", …)` |
| 本模块的 `bff.go` | 三条件：发概念账本、把修改转交给概念的主人、发静态资源。**不认识任何模块** |
| 各业务模块 | 自己那一面长什么样、怎么写回去（`core/lib/view`，Optional 依赖） |

加一个模块的界面 = 那个模块在自己的 `Start` 里 `Register`，本包与前端都不用改
（前端只认 Kind，见 `web/src/api.ts`）。

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

**注意 `__serve` 这个入口是 ui 提供的**（`cli`/`simple-cli` 那条口），所以「一个
界面模块都不装」的装配里守护进程起不来——这不是本模块的限制，是「入口由界面申报」
那条设计的直接后果。

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
- **`KindTable`**：契约里有这个 Kind，还没有贡献者，前端也还没有渲染器（落到原文 JSON）。
- **非 loopback 的暴露**：现在只答本机。要开放给别人用，得先有认证（控制令牌是现成
  的一半），见 `docs/12-newgate-remote.md` 那条路线。
