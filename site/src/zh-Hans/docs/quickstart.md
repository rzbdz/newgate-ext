# 快速开始

从零到一个能干活的环境，四步。

## 1. 装

```bash
gh release download --repo rzbdz/newgate-ext -p '*linux-amd64'
mv newgate-default-linux-amd64 ~/.local/bin/newgate
chmod +x ~/.local/bin/newgate
```

一个静态二进制，没有运行期依赖。别的平台把文件名里那一段换成 `darwin-arm64`
这类即可（`gh release view` 里有全部产物）。

## 2. 铺配置

```bash
newgate init
```

它在 `~/.config/newgate/` 下铺一份带占位符的配置：

```text
providers.json      上游是谁、怎么认证
mappings/*.kv       每个 profile 的档位绑定
state.json          当前 profile、接管状态
```

## 3. 填上游

打开 `providers.json`，把示例里的 `base_url` 与 key 换成你自己的：

```jsonc
{
  "providers": {
    "ark": { "protocol": "anthropic", "base_url": "https://…", "api_key": "sk-…" }
  }
}
```

密钥也可以走环境变量（`api_key_env`），那样它就不落在文件里。带凭据的
`providers.json` 在网页面板上**只读**——它本来就不该从浏览器改。

## 4. 让客户端走这条路

```bash
newgate on claude        # 还有：newgate on codex / newgate on opencode
```

它改的是**客户端自己的配置**（改之前按字节备份），并注入一个 PATH shim。做完
之后重开一次 shell，然后像平时一样用你的客户端——它自己不知道中间多了一层。

## 确认它真的在管事

```bash
newgate status      # daemon 在跑吗、哪几家客户端被接管了
newgate tier normal # normal 这一档此刻解析到哪条链、谁被跳过、为什么
newgate web         # 打出网页面板的地址（挂在网关自己那个端口上）
```

`newgate tier` 是最有用的一条：它把「这一次请求实际会打给谁」连同跳过的原因一起
打出来。请求没走通时先看它。

## 下一步

- [档位与 profile](/docs/tiers/) —— heavy / normal / light 到底是什么
- [接管客户端](/docs/clients/) —— 三个客户端各自做了什么
- [配置与命令](/docs/config/) —— 文件形状与命令速查
