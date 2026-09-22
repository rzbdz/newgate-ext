# 档位与 profile

## 档位是意图，不是模型名

客户端说的**永远不是**「用 gpt-5.6」，而是「这一发要 heavy」。中间那层语义：

```text
客户端请求  →  档位  →  profile 的候选链  →  provider/model
```

好处是模型换代不需要动客户端：换的是**链**，客户端里那一行从来没变过。

四档按能力从高到低：

```text
heavy  >  normal  >  mid  >  light
```

- `normal` 是主力档，日常都落在这里；
- `mid` 是**兼容层**：一份 profile 没写 `normal` 时，由它承接（稀疏 profile 不该
  逼着你把每一档都写全）；
- `vision` 不在这个阶梯里——它说的是「这一发还需要看得见图」，与强弱无关，单独判断；
- 认不出来的档位**不猜**，直接报一句可诊断的错。

## profile 是一条有序的候选链

一个 profile 给每一档配**若干**候选，顺序就是 fallback 顺序：

```text
normal = ark/deepseek-v3, backup/gpt-5.6
mid    = backup/gpt-5.6
light  = fast/glm-air
```

请求先走链头。**连接失败、上游错误可以转移、首字节超时**这三种情况下才试下一站。
每一次跳过都会说明原因——`newgate tier` 就是把这些原因摊开给你看的那条命令。

```bash
newgate tier normal
```

## 谁来定用哪一份 profile

优先级从高到低：

1. **这一次调用显式点名**：`newgate claude --profile=kimi`；
2. **这个客户端绑定的**：`state.json` 里按 agent 记的那一份；
3. **全局默认**：`newgate --set-profile ark`。

第 1 条是**单次覆盖**，不动任何全局状态。它编码在请求路径里，所以两个客户端同时
跑着，永远不会拿错对方的选择。

## 改一份 profile

```bash
newgate web                    # 在网页面板上点着改
newgate profile kv ark         # 把 ark 摊成 KV 文本
newgate profile kv ark --write # 改完写回盘上
$EDITOR ~/.config/newgate/mappings/ark.json   # 或直接改文件，watch 会捡起来
```

文件本身住在 `~/.config/newgate/mappings/`，`.kv` 与 `.json` 两种后缀都认。

## 动态档位

除了上面四个，模块还能登记**自己的档位**。今天有两处：

- **OpenCode × Oh My OpenAgent 的槽位**（`newgate omo` 管的那张表）；
- **Codex 自己的模型名**（见 [Codex 模式](/docs/codex/)）。

它们是同一条解析路径上的东西：请求进来时先看「这个名字是不是一个认得的档位」，
是就照常建链。所以「客户端点名一个具体模型名」与「客户端说要 heavy」在网关这一层
没有区别。
