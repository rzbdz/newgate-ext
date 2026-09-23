#!/usr/bin/env python3
"""把一份真快照摘成演示页能用的假数据。

    curl -s http://127.0.0.1:8899/ui/api/snapshot -o /tmp/snap.json
    python3 tools/demo-snapshot.py /tmp/snap.json \
        modules/web-dashboard/web/demo/snapshot.json

# 为什么要有这一步

站点上的演示页嵌的是**真组件**、喂的是假数据（见
`modules/web-dashboard/web/demo/data.ts` 的文件头）。手写一份「差不多的」假数据
必然会漂——形状一偏，屏幕上就是「这张卡画不出来」，而没有东西会红。所以那份数据
是从真快照摘下来的，这个脚本就是摘的那一步，写在仓库里而不是留在某个人的 shell
历史里。

# 它换掉了什么，为什么

1. **本机的上游地址**。`api.rvcompute.com:60000` 与 `ark.cn-beijing.volces.com`
   是这台机器真的在用的聚合器与厂商地址 → 换成 `*.example-*` 示例地址。地址本身
   不是凭据，但它指着一个真实的服务、且带着内部端口，没有理由出现在公开站上。
2. **磁盘路径**：`/root/.config/newgate` → `~/.config/newgate`。
3. **CAS 指纹**（`base: "sha256:…"`）。那是磁盘上那份文件的稳定标识：同一份配置
   在任何机器上算出来都一样，把它公开等于把「这个人的配置长这样」也一并给出去了。
   换成按文件名派生的假指纹——形状与长度不变（界面上那格只是在比「还是不是原来
   那份」），同一份文件每次摘出来都一样（免得重摘一次就是满屏 diff）。
4. **上游的 call id**（推理回填那几行日志里的 `call_00_…`）：同一条推理链的 id 会
   反复出现在日志里，它是上游侧的标识。换成固定的 `call_mock_*`。
5. **`generated_at`** 固定成一个常量：它每跑一次都会变，而这份数据进版本控制。

# 它**没有**换掉什么（有意保留）

模型名与 provider 名（`smt-deepseek` / `gpt-5.6-sol` / `MiniMax-M3` …）。演示页要
演的正是「这些名字落到哪家模型上」，抹掉它这张链卡就没有内容了。它们不是凭据。

# 摘完还会自检

脚本最后按几条正则复扫一遍产物（真的主机名、`sk-` 开头的 key、`Bearer`、
`control_token` 的值、`/root/`）。扫到任何一条就**非零退出**、一个字节都不写——
摘录这种事出错的样子是「悄悄把不该公开的东西写进了公开站」，那不能靠人记得看一眼。
"""

import hashlib
import json
import re
import sys

# 替换表：左列是这台机器上真的东西，右列是演示页上该看到的东西。
SUBS = [
    ("https://api.rvcompute.com:60000/v1", "https://api.example-gateway.com/v1"),
    (
        "https://ark.cn-beijing.volces.com/api/coding",
        "https://ark.example-cloud.com/api/coding",
    ),
    ("/root/.config/newgate", "~/.config/newgate"),
]

# 自检：这些东西不该出现在产物里（逐条对应上面那张表，外加凭据）。
FORBIDDEN = [
    (r"rvcompute", "真的聚合器主机名"),
    (r"volces", "真的厂商主机名"),
    (r"/root/", "绝对的 home 路径"),
    (r"sk-[A-Za-z0-9_\-]{10,}", "看起来像 API key 的串"),
    (r"Bearer [A-Za-z0-9]", "Bearer 令牌"),
    (r'"control_token"\s*:\s*"(?!\*\*\*)', "控制令牌的值"),
    (r"call_00_[A-Za-z0-9]{10,}", "上游的 call id"),
]

TAIL_LOG_LINES = 60
GENERATED_AT = "2026-09-23T00:00:00Z"


def fake_digest(seed: str) -> str:
    return "sha256:" + hashlib.sha256(seed.encode()).hexdigest()


def scrub(text: str) -> str:
    for old, new in SUBS:
        text = text.replace(old, new)
    text = re.sub(r"call_00_[A-Za-z0-9]+", "call_mock_0001", text)
    text = re.sub(r"call_01_[A-Za-z0-9_]+", "call_mock_0002", text)
    text = re.sub(r"call_[0-9a-f]{20,}", "call_mock_0003", text)
    text = re.sub(r"call_[A-Za-z0-9_]{12,}", "call_mock_0004", text)
    return text


def walk(value):
    if isinstance(value, str):
        return scrub(value)
    if isinstance(value, list):
        return [walk(v) for v in value]
    if isinstance(value, dict):
        return {k: walk(v) for k, v in value.items()}
    return value


def swap_digests(concept: dict) -> None:
    """把这一张卡里所有的 CAS 指纹换成按文件派生的假指纹。

    只认 `sha256:` 开头的那种串，别的字符串一个不碰（界面上那些「为什么」是散文，
    随手改一个字都可能改掉它的意思）。
    """

    def visit(node):
        if isinstance(node, dict):
            base = node.get("base")
            if isinstance(base, str) and base.startswith("sha256:"):
                seed = str(
                    node.get("file") or node.get("path") or concept.get("id") or ""
                )
                node["base"] = fake_digest(seed)
            for v in node.values():
                visit(v)
        elif isinstance(node, list):
            for v in node:
                visit(v)

    visit(concept.get("data") or {})


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__.split("# 为什么")[0].strip(), file=sys.stderr)
        return 2
    src, dst = sys.argv[1], sys.argv[2]

    snap = json.load(open(src, encoding="utf-8"))
    snap = walk(snap)
    for concept in snap.get("concepts") or []:
        swap_digests(concept)
        if concept.get("id") == "gateway.log":
            lines = (concept.get("data") or {}).get("lines") or []
            concept["data"]["lines"] = lines[-TAIL_LOG_LINES:]
            concept["data"]["truncated"] = True
    snap["generated_at"] = GENERATED_AT

    out = json.dumps(snap, ensure_ascii=False, indent=2) + "\n"

    # 先自检再写盘：发现了东西就一个字节都不写。
    bad = []
    for pattern, what in FORBIDDEN:
        hits = sorted(set(re.findall(pattern, out)))[:3]
        if hits:
            bad.append(f"  {what}：{hits}")
    if bad:
        print("摘录失败——产物里还有不该公开的东西（一个字节都没写）：", file=sys.stderr)
        print("\n".join(bad), file=sys.stderr)
        return 1

    with open(dst, "w", encoding="utf-8") as f:
        f.write(out)
    concepts = len(snap.get("concepts") or [])
    sections = len(snap.get("sections") or [])
    print(f"写出 {dst}：{sections} 节 / {concepts} 张卡 / {len(out.encode())} 字节")
    return 0


if __name__ == "__main__":
    sys.exit(main())
