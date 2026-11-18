# 文档索引（发行版）

这里是 **newgate-ext**——一个基于 newgate 内核的 LLM API 网关实现。内核只提供
机制（组件框架、入口契约、i18n 引擎、工具）；**产品的一切**（网关、路由、接管、
界面、上游补丁）住在这一层的模块里。

内核文档（组件框架、装配模型、扩展指南、测试与安全、本地化机制）仍在
`core/docs/`（submodule）。

| 顺序 | 文档 | 回答的问题 |
| --- | --- | --- |
| 1 | [01-product.md](01-product.md) | newgate 解决什么问题？ |
| 4 | [04-configuration.md](04-configuration.md) | 配置和 fallback 如何解析？ |
| 5 | [05-gateway.md](05-gateway.md) | 请求如何经过 gateway？ |
| 6 | [06-reasoning.md](06-reasoning.md) | reasoning content 为什么会丢，如何恢复？ |
| 7 | [07-clients-runtime.md](07-clients-runtime.md) | Claude/OpenCode 如何被接管？ |
| 8 | [08-operations.md](08-operations.md) | 如何运行、升级和诊断？ |
| 11 | [11-troubleshooting.md](11-troubleshooting.md) | 常见故障如何定位？ |
| 12 | [12-newgate-remote.md](12-newgate-remote.md) | newgate-remote：基于 Tailscale 的分布式网关设计（仅设计文档，未实现） |

模块分层、依赖方向、以及「这个发行版装了哪些模块」见仓库根的
[CLAUDE.md](../CLAUDE.md) 与规格书（`dist*.json`）。
