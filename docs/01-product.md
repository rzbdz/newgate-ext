# 产品模型

## 1. 问题

AI CLI 通常把真实模型名写进自己的配置。工具数量和 provider 数量增长后，模型
换代需要修改许多互不相同的配置，而且无法统一表达 fallback、健康状态和上游
兼容行为。

newgate 在客户端与真实模型之间加入语义层：

```text
client request -> role -> profile candidate chain -> provider/model
```

客户端长期使用 role；真实模型变化只修改 profile。

## 2. Role

四档按能力从高到低排列：

```text
heavy > normal > mid > light
```

`normal` 是主力档。为兼容稀疏 profile，未声明 `normal` 时可以由 `mid`
承接。`vision` 不在强弱阶梯中，它表示请求还需要视觉能力。

Role 是用户意图，不是模型别名。Gateway 不自动猜“性价比最优”，只执行明确
profile 和健康约束。

## 3. Profile 和候选链

一个 profile 可以为每个 role 配置多个候选：

```text
normal = primary/opus, backup/deepseek
mid    = backup/deepseek
light  = fast/glm-air
```

请求先走链头。连接失败、可转移上游错误或首字节超时后，gateway 才尝试下一站。
固定 binding、排除项和稀疏层由纯函数 resolver 处理。

## 4. 两个核心系统

1. **Gateway**：解析 role、构造链、改写 model、执行扩展、转发并观测结果。
2. **Configuration hooks**：把 Agent 的配置或启动环境指向本地 Gateway。

其余能力都是向这两个系统提供或消费 typed capability 的组件。

## 5. 明确边界

newgate 不负责自动任务规划、长期记忆、上下文压缩、额度聚合或模型排行榜。
它只负责语义路由、可靠 fallback、客户端接管和必要的上游协议修补。
