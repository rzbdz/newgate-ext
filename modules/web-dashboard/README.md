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

## 三种装配

| 装了什么 | 界面 |
| --- | --- |
| 默认（`cli`/`tui` + 本模块） | 终端与浏览器各一份，互不影响 |
| 关掉内核的 `cli`（本模块 + `simple-cli` 这类最小 ui） | 浏览器一份；终端只出最小渲染 |
| 不装本模块 | 没有任何 web 入口，`/ui` 掉进数据面的 catch-all（与任何未知路径一样） |

第三种成立是因为本模块的依赖全是 `Optional`：没装 porthub 就不挂载，没装 cli 就
不注册命令。**注意 `__serve` 这个入口是 ui 提供的**（`cli`/`simple-cli` 那条口），
所以「一个界面模块都不装」的装配里守护进程起不来——这不是本模块的限制，是
「入口由界面申报」那条设计的直接后果。

## 待办

- **porthub 缺席时的自起端口**：现在 `Start` 里拿不到 porthub 就什么都不做。要自起
  端口，判据必须是「这次调用是 `__serve`」（入口账本知道），否则每敲一条命令都会
  开一个监听。
- **`newgate web`**：打印 URL / 打开浏览器（模块自己说的入口，现在还没有）。
- **前端 i18n**：界面骨架上的英文（按钮、提示）还没有走目录表。概念标题已经是
  后端翻译好的。
- **日志（`KindLog`）**：契约里有这个 Kind，还没有贡献者，也还没有流式通道。
