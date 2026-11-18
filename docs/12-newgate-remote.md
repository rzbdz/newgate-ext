# newgate-remote — Tailscale-based distributed gateway

> **状态**：设计文档，2026-09-17。尚未实现。
>
> 本文记录架构思路与关键决策。实现拆成独立插件，不改 core，可单独
> bisect / 回滚。

---

## 1. 动机

newgate 目前是「每台机器一个 daemon、每个 CLI 调本机 8899」的单机模型。
这带来三个痛点：

1. **配置碎片化**：每台机器各自维护一份 `providers.json` + `state.json`；
   加一个新 provider 要改 N 台机器，改错一台就不一致。

2. **计量不集中**：metrics / breaker 状态散在各端，看不到全局健康；
   一个上游今天限流了，只有撞上它的那台机器知道。

3. **本机资源占用**：分类器系统提示词 126KB + thinkcache 最大几百 MB，
   每台机器都在内存里跑一份，重复代价高。

newgate-remote 的目标：**所有 CLI 都指向同一个远程 daemon**，前后端彻底
分离。本机仍然有一个轻量 shim，但它不做路由，只做连接转发（透明代理）。

---

## 2. 整体架构

```
┌─────────────────────────────────────────────────────────────┐
│  本机（开发机 / 笔记本 / CI runner）                          │
│                                                             │
│   claude / opencode                                         │
│       │  ANTHROPIC_BASE_URL=http://100.x.y.z:8899          │
│       ↓                                                     │
│   （无 daemon，直接走 Tailscale 网络）                        │
└─────────────────────────────────────────────────────────────┘
           │  Tailscale WireGuard tunnel
           ↓
┌─────────────────────────────────────────────────────────────┐
│  newgate-remote 节点（专用服务器 / VPS / homelab）            │
│                                                             │
│   newgate daemon（标准单机版 + remote 插件）                  │
│   └─ providers.json：所有上游 key 集中管理                    │
│   └─ health.json：全局 breaker 状态                          │
│   └─ thinkcache：集中缓存                                    │
│   └─ metrics：全局计数                                       │
│                                                             │
│   newgate-remote 插件额外提供：                               │
│   ├─ 多租户身份识别（Tailscale 节点身份 → profile 映射）        │
│   ├─ 控制面 API（GUI/PWA 复用）                               │
│   └─ 审计日志（谁、什么时候、发了什么请求）                     │
└─────────────────────────────────────────────────────────────┘
```

本机零 daemon：ENV 变量指向远端，不需要 `newgate start`，不需要 PATH shim。
已有的 `newgate on claude` 流程只负责写 `ANTHROPIC_BASE_URL`，不启动本地服务。

---

## 3. 网络层：为什么是 Tailscale

| 方案 | 认证 | NAT 穿透 | 移动性 | 配置复杂度 |
|---|---|---|---|---|
| 公网 IP + TLS | 需自管证书 | 不需要 | 好 | 中 |
| WireGuard 手动 | 静态密钥 | 需手配 | 差 | 高 |
| **Tailscale** | **节点证书（WireGuard）** | **自动** | **好** | **极低** |
| ZeroTier | 类似 Tailscale | 自动 | 好 | 低 |

选 Tailscale 的具体理由：

- **节点身份即认证**：每个 Tailscale 节点有加密身份（节点证书）。
  newgate-remote 可以从 `X-Tailscale-Node-ID`（或 `/localapi`）取节点名，
  不需要再发 API key。
- **已有的 ACL 即权限层**：Tailscale ACL 控制哪些机器能访问 8899，
  不需要在 newgate 里实现网络级防火墙。
- **`tailscale serve`**：可以把 8899 发布成 `https://<hostname>.tailnet-name.ts.net`
  并自动颁发证书，控制面 GUI 直接 HTTPS 访问，不用管证书。
- **零配置 NAT 穿透**：开发机 → VPS 中间有 CGN 或双重 NAT 的情况下仍然能连，
  不需要固定公网 IP。

---

## 4. 多租户身份识别

单机版 daemon 没有「谁在发请求」的概念——所有请求都算同一个用户。
分布式场景里多台机器共用一个 daemon，必须区分身份。

**推荐路径**：读 Tailscale Local API（`100.100.100.100:80/localapi`）
或 `X-Forwarded-For` + Tailscale Magic DNS 反查，得到发请求的节点名
（`laptop-junzhong.tailnet-name.ts.net`），映射到一个 profile：

```json
// ~/.config/newgate/remote-tenants.json（服务端配置）
{
  "laptop-junzhong": "default",
  "snode1":           "server",
  "ci-runner-01":     "cheap"
}
```

节点名查不到 → 拒绝（或 fall back 到 `default`，可配）。
这张表用现有的 confighook 机制热更新，不需要重启 daemon。

**备选**：不做身份映射，所有节点共享一个 profile（最简单，适合个人使用）。
插件实现时这是 flag，不是两套代码路径。

---

## 5. 本机侧的变化

目前 `newgate on claude` 做三件事：

1. 装 PATH shim（`~/bin/claude → newgate`）
2. 启动本地 daemon（如果没跑）
3. 注入 `ANTHROPIC_BASE_URL=http://127.0.0.1:8899`

remote 模式下，第 2 步跳过，第 3 步改为注入远端地址：

```bash
newgate remote use 100.x.y.z          # 记录远端地址
newgate on claude                      # PATH shim 不变；注入远端 URL；不启本地 daemon
```

`newgate status` / `newgate breaker` 等查询命令通过控制面 API（`/__newgate/status`）
访问远端，行为与本机版一致——CLI 代码不用改，只是端点地址变了。

---

## 6. 插件边界

newgate-remote 作为一个**可选插件**（`modules/remote/`）而不是 core 变更：

```
modules/remote/
  module.go      New() — Requires: cliapi, configapi; Provides: RemoteCapability
  api.go         RemoteConfig、TenantResolver 端口
  tenant.go      Tailscale 节点名 → profile 解析
  audit.go       审计日志：谁、什么时候、发了什么
  cli.go         newgate remote use/status/off
```

core 里不出现任何 Tailscale 专有字符串。`TenantResolver` 是端口，
Tailscale 实现是其中一个适配器，也可以换成 Bearer token 或 mTLS。

**不改动**：
- `modules/gateway/forward`（转发热路径完全不知道 remote 的存在）
- `modules/breaker`（健康表单一实例，remote 只是更多客户端往里写）
- `modules/config/store`（配置文件格式不变，只是路径可以在 remote 节点上）
- 所有现有 CLI 命令（查询端点地址从本机改成远端，但命令本身不变）

---

## 7. 控制面 / GUI / PWA

newgate-remote 节点同时暴露一个控制面 HTTP API，供 GUI/PWA 复用：

```
GET  /__newgate/status          当前运行状态（已有）
GET  /__newgate/metrics         计数器（已有）
GET  /__newgate/breaker         熔断表（已有 /__newgate/status 的子集）
POST /__newgate/naked           设置裸奔模式（新增）
POST /__newgate/profile         切 profile（新增）
GET  /__newgate/tenants         租户 → profile 映射（remote 插件新增）
GET  /__newgate/audit           审计日志流（remote 插件新增，SSE）
```

认证：现有的 `control_token`（Bearer header）。
TLS：`tailscale serve` 自动搞定，不在 newgate 里实现。

PWA 是独立前端（`web/` 目录，不在本文档范围），通过上述 API 操作。
CLI 和 GUI 没有功能差集：CLI 能做的，API 一定暴露了。

---

## 8. 数据持久化与高可用

**初版（MVP）不考虑高可用**：单节点，`health.json` 和 `thinkcache.bin`
存本地磁盘，重启后自动装回（现有机制）。

**后续扩展路径**（不在本 doc 范围）：
- 多 daemon 节点 + 共享 Redis：`Breaker.UseRedis(addr)` 端口，不改状态机
- `health.json` 替换成 etcd/Consul：实现 `UseFile` 的一个备选后端
- 只读副本（只做路由、不做写入）：由 `TenantResolver` 配置

---

## 9. 迁移路径（单机 → remote）

1. 在 VPS / homelab 安装 Tailscale，`tailscale up`。
2. `make static && scp bin/newgate <vps>:~/.local/bin/`，在 VPS 上 `newgate start`。
3. 把 VPS 上的 `providers.json` 补全（迁移本机的密钥）。
4. 本机：`newgate remote use <tailscale-ip>`，然后 `newgate on claude`。
5. 本机的本地 daemon 可以 `newgate stop` 了（不再需要）。

**回滚**：`newgate remote off`（删掉记录的远端地址），`newgate start` 重新起本地。
两套配置互不干扰，本机的 `providers.json` 不用删。

---

## 10. 安全考量

- **密钥只在远端**：本机 `providers.json` 可以是空的（或只留 fallback）。
  上游 API key 不再复制到每台开发机，泄漏面缩小到一台服务器。
- **Tailscale ACL 是第一道防线**：不在 ACL 里的节点根本摸不到 8899 端口。
  `control_token` 是第二道（控制端点），普通请求由 Tailscale 身份识别鉴权。
- **审计日志**：remote 模式下每一条请求都记录发起节点名 + 时间 + 档位，
  不记录内容（符合「不静默」但也不侵权）。
- **裸奔限制**：`newgate naked forever` 只能由 remote 节点本地 CLI 设置，
  不能通过 API 远程打开——避免一台恶意节点把全局安全门关掉。
  （实现：API 端点返回 403，本地 CLI 直接写文件。）

---

## 11. 实现顺序（建议）

1. `modules/remote/cli.go`：`newgate remote use/status/off`，写 `state.json`。
2. 修改 `modules/runtime/commands.go` 的 `start` 路径：remote 模式下跳过本地 daemon 启动。
3. 修改 `modules/gateway/controlplane` 的控制面客户端：先看 remote 地址，再落回 127.0.0.1。
4. `modules/remote/tenant.go`：Tailscale 节点名解析（依赖 `tailscale.com/client/tailscale`）。
5. `modules/remote/audit.go`：审计日志（SSE + 文件落盘）。
6. `web/`：PWA 控制面（独立前端，不在本 module 里）。

每步独立可 bisect。第 1-3 步不依赖 Tailscale SDK，纯网络地址改写，
可以在没有 Tailscale 环境的机器上开发和测试。

---

*本文档是设计意图的记录，不是 API 契约。实现时如有出入，以代码和提交信息为准，
同时更新本文档。*
