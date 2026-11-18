# Clients 与 Runtime

## 1. Agent descriptor

Agent descriptor 定义：

- 稳定 ID 和可执行文件候选；
- base URL、认证和模型槽位环境变量；
- 需要从子进程移除的冲突环境变量；
- 可选配置 takeover；
- 查找真实可执行文件的方法。

Agent 通过 ConfigHook capability 注册。Catalog 返回副本，consumer 不能修改
registry 内部对象。

## 2. Claude Code

Claude Code 使用 PATH shim。`newgate on claude` 在专用目录创建同名入口；
shim 找到真实 Claude 可执行文件，构造 Gateway URL 和 role 槽位环境变量，然后
exec 替换当前进程。

动态模式注入 role 名，让已经运行的会话在下一请求读取最新 profile。显式
`--profile` 则把本次调用钉在指定 profile。

## 3. OpenCode

OpenCode 通过配置 takeover 注入 `newgate` provider，并重写选中的模型引用。
原文件保存在 backup 中；`newgate off opencode` 做逐字节恢复。

OMO 作为独立组件消费 OpenCode、Config、ConfigHook 和 CLI capabilities，注册
额外角色、takeover、诊断和命令。

## 4. Runtime

Runtime 拥有四类行为：

- daemon：spawn、stop、running、优雅交接；
- launch：解析真实程序、确保 proxy 存活、注入环境并 exec；
- injection：管理 PATH shim；
- takeover：按 Agent 机制执行 on/off/list。

CLI 通过 Runtime capability 使用 launch。仍存在的 package compatibility catalog
是迁移边界，不应成为新代码依赖。

## 5. 环境冲突

Shell 中已有 `ANTHROPIC_BASE_URL` 等变量可能覆盖配置文件。Shim 在子进程环境中
显式设置或删除相关变量，并在接管时给出可见警告。
