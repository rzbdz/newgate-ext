# newgate modules-ext —— 外部模块发行版

这是 newgate 的**外部模块仓库**：模块放在这里，用户在离线或自建发行版时把它
clone 进 `go/modules-ext/`，再用仓库根的 `modules-ext.json` 点名要装哪几个。

## 它和 newgate 主仓库的关系

- **没有 go.mod**。这里的目录会被当成主 module 里的包（`modules-ext/<名字>`），
  所以主仓库能直接编它们。加一份 go.mod 反而会让它们变成另一个 module，主仓库
  就得写 `replace` —— 那是构建方的东西，换台机器就失效。
- **形态与 `go/modules/` 里完全一致**：一个目录、一个 `module.go`、导出
  `func New() modules.Component`、声明 Requires/Provides/Start/Stop。
  「把一个模块从这儿拷进主仓库的 modules/」也应该能工作——两条路的区别只是
  **谁决定装**，不是**怎么装**。
- 项目根另一份 `modules-ext.json` 声明本仓库、钉一个提交号、点名单个模块。
  构建期会跑 `git ls-remote` 核对该提交，不一致就拒绝构建。

## 现有的模块

| 模块 | 作用 |
| --- | --- |
| `hello` | 最小样例：Start 时打印一行 hello。零依赖，用来验证 external 通路。 |
| `simple-cli` | 极简界面：只提供 `status` 与「已注入的命令」，用于验证「换掉界面，别的模块照常」 |
