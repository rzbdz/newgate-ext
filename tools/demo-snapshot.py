#!/usr/bin/env python3
"""Redact a real snapshot into fake data the demo page can use.

    curl -s http://127.0.0.1:8899/ui/api/snapshot -o /tmp/snap.json
    python3 tools/demo-snapshot.py /tmp/snap.json \
        modules/web-dashboard/web/demo/snapshot.json

# Why this step has to exist

The demo page on the site embeds **real components** and feeds them fake data (see
the file header of `modules/web-dashboard/web/demo/data.ts`). Hand-writing a "roughly similar" set of fake data
would inevitably drift - once the shape is off, the screen says "this card cannot be drawn", and nothing goes red. So that data
is redacted from a real snapshot, and this script is the redaction step, kept in the repo rather than left in someone's shell
history.

# What it replaces, and why

1. **Local upstream addresses**. `api.rvcompute.com:60000` and `ark.cn-beijing.volces.com`
   are the aggregator and vendor addresses this machine really uses -> replaced with `*.example-*` example addresses. The address itself
   is not a credential, but it points at a real service and carries internal ports, and there is no reason for it to appear on a public site.
2. **Disk paths**: `/root/.config/newgate` -> `~/.config/newgate`.
3. **CAS fingerprints** (`base: "sha256:…"`). That is a stable identifier of the file on disk: the same config
   computes to the same value on any machine, and publishing it also gives away "what this person's config looks like".
   Replaced with a fake fingerprint derived from the file name - the shape and length stay the same (that cell on the screen only compares "is it still the same
   one"), and the same file redacts to the same thing every time (so re-redacting does not produce a diff on every line).
4. **Upstream call ids** (the `call_00_…` in the reasoning-backfill log lines): the same reasoning chain's id appears
   repeatedly in the log, and it is an upstream-side identifier. Replaced with a fixed `call_mock_*`.
5. **`generated_at`** is fixed to a constant: it changes on every run, and this data goes into version control.

# What it does **not** replace (deliberately kept)

Model names and provider names (`smt-deepseek` / `gpt-5.6-sol` / `MiniMax-M3` ...). What the demo page
shows is exactly "which vendor's model these names land on", and erasing it would leave this chain card with no content. They are not credentials.

# After redacting it also self-checks

At the end the script rescans the output with a few regexes (real host names, keys starting with `sk-`, `Bearer`,
the value of `control_token`, `/root/`). Any single hit means **non-zero exit** and not a byte written -
the failure mode of redaction is "quietly writing something that should not be public into a public site", and that cannot rely on someone remembering to look.
"""

import hashlib
import json
import os
import re
import sys

# Replacement table: the left column is what really exists on this machine, the right column is what the demo page should show.
SUBS = [
    ("https://api.rvcompute.com:60000/v1", "https://api.example-gateway.com/v1"),
    (
        "https://ark.cn-beijing.volces.com/api/coding",
        "https://ark.example-cloud.com/api/coding",
    ),
    ("/root/.config/newgate", "~/.config/newgate"),
]

# Self-check: these things must not appear in the output (one by one corresponding to the table above, plus credentials).
FORBIDDEN = [
    (r"rvcompute", "the real aggregator host name"),
    (r"volces", "the real vendor host name"),
    (r"/root/", "an absolute home path"),
    (r"sk-[A-Za-z0-9_\-]{10,}", "a string that looks like an API key"),
    (r"Bearer [A-Za-z0-9]", "a Bearer token"),
    (r'"control_token"\s*:\s*"(?!\*\*\*)', "the value of the control token"),
    (r"call_00_[A-Za-z0-9]{10,}", "an upstream call id"),
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
    """Replace every CAS fingerprint in this card with a fake one derived from the file.

    Only strings starting with `sha256:` are matched, and no other string is touched (the "why" text on the screen is prose,
    and changing one character at random can change its meaning).
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


def scan(out: str) -> list[str]:
    """Rescan the output against the forbidden-word list and return "what was found"."""
    bad = []
    for pattern, what in FORBIDDEN:
        hits = sorted(set(re.findall(pattern, out)))[:3]
        if hits:
            bad.append(f"  {what}：{hits}")
    return bad


def main() -> int:
    if len(sys.argv) not in (3, 4):
        print(__doc__.split("# Why")[0].strip(), file=sys.stderr)
        return 2
    src, dst = sys.argv[1], sys.argv[2]
    # The third argument is the raw text of `GET /ui/api/themes` (optional). If given, write `themes.json` in the same directory -
    # the demo page needs to show themes, and themes are **not in the snapshot** (they are the UI's own preference,
    # see modules/web-dashboard/api.go). Same rule as the snapshot: **redact from the real backend, do not hand-copy**,
    # hand-copying a "roughly similar theme table" is keeping an unguarded copy, and its drift mode is exactly
    # "the theme on the demo page does not match the real UI" - nobody reports that kind of error as a bug.
    themes_src = sys.argv[3] if len(sys.argv) == 4 else None

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

    # Themes go through **the same forbidden-word list** too: it is a new source of content, and the reason that list exists is
    # "the failure mode of redaction is quietly writing something that should not be public into a public site" - one more source means one more scan,
    # and we cannot assume "themes will not contain those things" (today they indeed do not, but that is a fact, not a guarantee).
    themes_out = None
    if themes_src:
        doc = json.load(open(themes_src, encoding="utf-8"))
        themes_out = json.dumps(doc, ensure_ascii=False, indent=2) + "\n"

    # Self-check before writing: if anything was found, not a byte is written.
    bad = scan(out) + (scan(themes_out) if themes_out else [])
    if bad:
        print("redaction failed - the output still has things that should not be public (not a byte was written):", file=sys.stderr)
        print("\n".join(bad), file=sys.stderr)
        return 1

    with open(dst, "w", encoding="utf-8") as f:
        f.write(out)
    concepts = len(snap.get("concepts") or [])
    sections = len(snap.get("sections") or [])
    print(f"wrote {dst}: {sections} sections / {concepts} cards / {len(out.encode())} bytes")
    if themes_out:
        tpath = os.path.join(os.path.dirname(dst), "themes.json")
        with open(tpath, "w", encoding="utf-8") as f:
            f.write(themes_out)
        print(f"wrote {tpath}: {len((doc.get('themes') or []))} themes / {len(themes_out.encode())} bytes")
    return 0


if __name__ == "__main__":
    sys.exit(main())
