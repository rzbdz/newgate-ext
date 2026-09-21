# fork 一份发行版

newgate 分成两半，而**这个分法本身就是重点**：

| | |
| --- | --- |
| [rzbdz/newgate](https://github.com/rzbdz/newgate) | **内核**：链、熔断、网关、升级交接、模块框架。不做产品决定——它不认识任何一个上游或客户端的名字。 |
| **rzbdz/newgate-ext**（就是这一份） | 一个**发行版**：装哪些模块、修哪家的怪癖、按什么顺序修。这是产品决定，也是你说了算的那一半。 |

所以 fork 不是「改别人的代码」，是**换一份装配**。你现在看的这些文档讲的是旗舰
那一份（`dist.json`）；你要的那一份，可能只要其中一半。

## 规格书就是全部

```jsonc
// dist-mine.json  →  build/build.sh  →  每个平台一个静态二进制
{
  "distribution": "mine",
  "modules": ["tui", "deepseek", "glm", "claudecode", "opencode"],
  "disable": []
}
```

- `modules` 是**你自己的** `modules/` 里要装的目录名；
- `disable` 是要关掉的内核模块。想关掉内核**全部**（骨架发行版就是这么干的）写
  `"disable": ["*"]`——**别把目录名列满**：列满的名单是内核那张模块表的副本，内核
  加一个模块时它不会跟着变，于是你会静默多装一个。
- 编出来叫 `newgate-mine-<平台>-<架构>`。

```bash
NEWGATE_ALL=1 build/build.sh        # 每份规格书都编一遍
NEWGATE_DISTS="dist.json dist-mine.json" build/build.sh out/
NEWGATE_PLATFORMS="linux/amd64 linux/arm64 darwin/arm64" build/build.sh dist/
```

## 从哪儿起步

**从 `dist-hello.json` 开始**：框架 + 一个 `hello`，编出来 `newgate` 跑起来就是一句
hello world。它是一份**活的**最小样例——每一行都能跑，而不是一份说明文档。

往上加东西的顺序大致是：

1. 一个客户端（`claudecode` / `codex` / `opencode`）——不接管客户端的话，网关只是
   一个本地端口；
2. 一个界面（`tui` 或者 `web-dashboard`）——`dist-dashboard.json` 那份**关掉了内核
   的 cli**，只剩浏览器，是「界面无关」这句话的唯一检验；
3. 你真正在用的上游的怪癖补丁（`deepseek`、`glm`……）；
4. 你自己写的模块（见 [写一个模块](/docs/modules/)）。

## 哪些东西该放进哪个仓库

判据一句话：

> **换个发行版，这东西还该在吗？**

| 该在内核 | 该在你的发行版 |
| --- | --- |
| 网关、熔断、接管、界面、配置、组件框架 | 上游怪癖修补（DeepSeek 尾部形状、GLM 思维链回传） |
| 客户端接入（Claude Code / Codex / OpenCode） | 客户端 × 模型的交叉语义 |
| 别的发行版也要用的**机制** | 只对某个上游/某个产品有意义的**取舍** |

判断错了也不会静默：写进哪个仓库、装不装，都在规格书里写着，构建日志与
`newgate plugin` 会把实际装了什么报出来。

## 发你自己的版本

```bash
git tag v0.1.0 && git push origin main --tags
```

CI 会编一份平台矩阵、挂到 Release 上。**发行节奏是你的事**：内核今天合了什么，
不该决定你的产品什么时候发新版。

## 升级到新内核

内核在这个仓库里是一个 submodule（钉在一个提交上），不是一份要先发布的依赖：

```bash
# 在内核仓库里改、提交、推
git -C core push origin main
# 回到发行版，把指针挪过去
git -C core fetch && git -C core checkout <新提交>
git add core && git commit -m "bump the kernel"
```

**在这儿改完不推**，别人拿到的是一个指向不存在提交的指针——CI 会在 checkout 那一
步整体失败。仓库带了一份 `.githooks/pre-push`，它专门拦这一条（还有另外三段判据）：

```bash
git config core.hooksPath .githooks   # 每个 clone 装一次
```

绕开用 `git push --no-verify`。这个口子**故意留着**：钩子自己出毛病时不能把人卡死。
