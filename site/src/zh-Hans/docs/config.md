# 配置与命令

## 磁盘布局

配置根是 `~/.config/newgate`，用 `NEWGATE_HOME` 可以整体换到别处（沙箱、测试、
第二实例都靠它）。

```text
providers.json       上游是谁、怎么认证
mappings/*.kv        profile 的档位绑定（.json 也认）
state.json           当前 profile、接管状态、控制 token
newgate.log          daemon 日志
thinkcache.bin       reasoning cache 的冷层
dump/                请求级取证（开了 debug 才有）
```

写入走**同目录临时文件 + rename**，所以永远读不到半份 JSON。文件一变，watcher 会
重新加载整份快照；加载失败时**保留上一份有效快照**并把错误报出来，而不是把自己
清空。

## providers.json

```jsonc
{
  "providers": {
    "ark": {
      "protocol": "anthropic",          // anthropic | openai
      "base_url": "https://…",
      "api_key": "sk-…",                // 或者用下面的 api_key_env
      "api_key_env": "NEWGATE_KEY_ARK"  // 密钥不落盘
    }
  }
}
```

`provider/model` 合起来叫一个 **binding**，它就是 profile 里写的东西。

## mappings/*.kv

一份 profile 给每一档配若干候选，顺序即 fallback 顺序：

```text
description = 日常主力
normal = ark/deepseek-v3, backup/gpt-5.6
light  = fast/glm-air
```

没写出来的档位向上承接（`normal` 缺席由 `mid` 接）。`.json` 后缀也认，形状
`newgate profile kv ark` 摊出来的那份就是。

## 命令速查

生命周期：

```bash
newgate init          # 铺一份带占位符的配置
newgate start         # 起 daemon，并接管没显式关掉的客户端
newgate restart       # 交接 socket，排空在途请求（流式的也算）
newgate stop
newgate status
```

profile 与路由：

```bash
newgate profiles           # 有哪些 profile
newgate tier normal        # 这一档此刻解析到哪条链、谁被跳过、为什么
newgate --set-profile ark  # 换全局默认
newgate claude --profile=ds  # 单次覆盖，不动全局
newgate probe              # 主动验证 provider 连接、方言与已知怪癖
```

接管：

```bash
newgate on claude          # / codex / opencode
newgate off claude
```

观测：

```bash
newgate metrics    # 延迟（按上下文大小分）、改道、首字节超时、字节改写
newgate st         # 请求插件表：每个补丁存在是为了防住什么，能当场开关
newgate logs       # 日志（-f 跟着看）
newgate alllogs    # 版本 + 环境 + 状态 + 脱敏配置，一次汇总
newgate doctor     # 出问题时先跑它
newgate web        # 打出网页面板的地址
```

`doctor` 查配置、provider、端口、接管和模块诊断——它是「不知道哪儿不对」时的第一
条命令。

## 把 newgate 当后端直接用

```bash
newgate ask "把这段话译成英文：……"
echo "解释一下这段 diff" | newgate ask --tier light
newgate ask --profile ds --system "只回 JSON" "……"
```

`ask` 走**正在跑的那个代理**发一句问题，只把正文打出来。它自己不起代理（一次提问
不该变成一次状态改变），也不是第二个客户端——档位链、fallback、熔断、上游怪癖修补
在它这条路上照样生效，因为它走的就是那条路。

> **思维链走 stderr，正文走 stdout**：`answer=$(newgate ask …)` 拿到的必须是干净的
> 正文，而 reasoning 模型的思维链常常比正文长好几倍。人在终端上照样看得见它。

别的客户端想直接用这个后端，端点在 `http://127.0.0.1:8899`（`--port` 可改）：

| 路径 | 方言 |
| --- | --- |
| `POST /v1/messages` | anthropic |
| `POST /v1/chat/completions` | openai |
| `POST /p/<profile>/v1/messages` | anthropic，单次 profile 覆盖 |

两种方言各自成对：报哪个方言的错、说哪个方言的话。

## 运行期开关

有些补丁可以当场开关，不用重启、不用改配置：

```bash
newgate st                     # 先看有哪些、每个是干什么的
newgate st off classifier-naked   # 摘掉一个
```

`newgate naked` 是其中一类的快捷方式——临时压掉 Claude Code 的 Bash 安全分类器：

```bash
newgate naked on          # 60 秒自限窗口
newgate naked 2m          # 自定义时长
newgate naked forever     # 永久（status 会一直警告）
newgate naked off
```

它等价于 Claude Code 自己的 `--dangerouslySkipPermissions`，只是发生在代理这一层。
用在「分类器误判了合法命令、会话卡住」的时候，不是长期配置。默认全程关，必须显式
打开；`on` 的窗口写进 `state.json` 并**懒过期**——daemon 不持 timer，所以重启不会
把它变成永久。
