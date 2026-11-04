package hermes

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
)

// 本文件是接管的**第二道闸**：写盘之前，让 hermes 自己的 YAML 解析器确认这次手术
// 没有改坏任何东西。
//
// # 为什么要有它（2026-11-04 用户：「直接手术我担心会不会有兼容性问题」）
//
// 字节手术的好处是只碰该碰的行；它的风险是**我们对 YAML 的理解**与 hermes 的
// 解析器不一致——一处缩进算错，改的就是另一个键，而文件看起来完全正常。所以不靠
// 「我们觉得对」，而是问**唯一有发言权的那一方**：
//
//	load(原文件) + 这次的改动（按路径套上去）  ==  load(手术后的文件)
//
// 两边逐值相等，才写盘。这一句同时钉住两件事：我们**想改的**确实改成了（右边有），
// 我们**没想改的**一个都没动（左边其余部分原样）。不相等就整个放弃，文件不动，
// 报出第一处不同在哪个路径。
//
// # 解析器从哪来
//
// hermes 是 Python 程序，它读 config.yaml 用的是它 venv 里的 PyYAML（safe_load）。
// 这台机器上只要装着 hermes，那个解释器就在：它就是 `hermes` 这个入口脚本的
// shebang。所以这里不引入任何新依赖，也不假设系统 Python 有 PyYAML。
//
// 找不到解释器（入口不是 Python 脚本、venv 被挪了）时**不放行也不卡死**：退回到
// 本包自己的读回校验（每个写进去的键都要读得回来），并在报告里写明「没有经过
// hermes 的解析器确认」——降级是看得见的。

// verifyScript 读两份文件与一串改动，打印第一处不一致（一致则什么都不打印）。
//
// 用 safe_load 而不是 hermes 的 load_config：后者会合并缺省值、迁移旧键，比的就不再
// 是「文件里写了什么」。我们关心的正是文件本身。
const verifyScript = `
import json, sys, yaml
base_path, new_path, edits_path, orig_path = sys.argv[1:5]
with open(base_path) as f: base = yaml.safe_load(f) or {}
with open(new_path) as f: new = yaml.safe_load(f) or {}
with open(orig_path) as f: orig = yaml.safe_load(f) or {}
with open(edits_path) as f: edits = json.load(f)
MISSING = object()
def get(doc, path):
    for k in path:
        if not isinstance(doc, dict) or k not in doc: return MISSING
        doc = doc[k]
    return doc
want = base
for e in edits:
    path = e["path"]
    val = get(orig, path) if e.get("orig") else e.get("value", MISSING)
    if val is None and not e.get("orig"):
        val = MISSING  # a null value in an edit means: remove the key
    if val is MISSING:
        parent = get(want, path[:-1])
        if isinstance(parent, dict):
            parent.pop(path[-1], None)
        continue
    node = want
    for k in path[:-1]:
        nxt = node.get(k)
        if not isinstance(nxt, dict):
            nxt = {}
            node[k] = nxt
        node = nxt
    node[path[-1]] = val
def diff(a, b, at):
    if isinstance(a, dict) and isinstance(b, dict):
        for k in list(a) + [k for k in b if k not in a]:
            if k not in a: return at + [str(k)], "unexpected key"
            if k not in b: return at + [str(k)], "key went missing"
            d = diff(a[k], b[k], at + [str(k)])
            if d: return d
        return None
    if a != b or type(a) != type(b):
        return at, "value differs: want %r, got %r" % (a, b)
    return None
d = diff(want, new, [])
if d: print(".".join(d[0]) + ": " + d[1])
`

// verified 是一次校验的结论：Parser 说明是谁确认的（空 = 没有外部解析器确认）。
type verified struct {
	Parser string
}

// verifyEdits 校验 out 是不是「src + edits」。不一致返回错误（文件不该写）。
//
// orig 是 FromOrig 那类改动取值的来源（还原时 = 接管前的原件；接管时用不到，传空串）。
func verifyEdits(src, out string, edits []edit, orig string) (verified, error) {
	// 第一道：本包自己读回。每个写进去的标量都要读得回来，删掉的都要不在——这一道
	// 不依赖任何外部程序，所以永远跑。
	d := parseDoc(out)
	for _, e := range edits {
		if e.FromOrig {
			continue // 取值在原件里，本包读回比不了它的块；交给下面的解析器比
		}
		if e.Value == nil {
			if _, ok := d.lookup(e.Path...); ok {
				return verified{}, i18n.E("{path} was supposed to be removed but is still there",
					i18n.A{"path": strings.Join(e.Path, ".")})
			}
			continue
		}
		if want, ok := scalarLit(e.Value); ok {
			got, ok := d.get(e.Path...)
			if !ok || got != unquote(want) {
				return verified{}, i18n.E("{path} does not read back as written",
					i18n.A{"path": strings.Join(e.Path, ".")})
			}
		}
	}

	py := hermesPython()
	if py == "" {
		return verified{}, nil
	}
	dir, err := ioutil.TempDir("", "newgate-hermes-verify-")
	if err != nil {
		return verified{}, nil
	}
	defer os.RemoveAll(dir)
	type jsonEdit struct {
		Path  []string `json:"path"`
		Value any      `json:"value"`
		Orig  bool     `json:"orig,omitempty"`
	}
	js := make([]jsonEdit, len(edits))
	for i, e := range edits {
		js[i] = jsonEdit{Path: e.Path, Value: e.Value, Orig: e.FromOrig}
	}
	eb, err := json.Marshal(js)
	if err != nil {
		return verified{}, err
	}
	files := map[string][]byte{"base.yaml": []byte(src), "new.yaml": []byte(out),
		"edits.json": eb, "orig.yaml": []byte(orig)}
	for name, b := range files {
		if err := ioutil.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return verified{}, nil
		}
	}
	// -I：隔离模式，不读当前目录、不读用户 site——这一步只该用到 PyYAML。
	cmd := exec.Command(py, "-I", "-c", verifyScript,
		filepath.Join(dir, "base.yaml"), filepath.Join(dir, "new.yaml"),
		filepath.Join(dir, "edits.json"), filepath.Join(dir, "orig.yaml"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return verified{}, nil
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		return verified{}, nil
	}
	if err != nil {
		// 解析器自己起不来（PyYAML 缺失这类）≠ 文件坏了：降级成本包的读回校验。
		// 但**原文件**解析失败是另一回事——那说明它本来就是坏的，手术更不该做。
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "yaml.") && strings.Contains(msg, "Error") {
			return verified{}, i18n.E("hermes' YAML parser rejects the result: {err}",
				i18n.A{"err": lastLine(msg)})
		}
		return verified{}, nil
	}
	if d := strings.TrimSpace(stdout.String()); d != "" {
		return verified{}, i18n.E("hermes' YAML parser disagrees with the planned change at {where}",
			i18n.A{"where": d})
	}
	return verified{Parser: py}, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// hermesPython 是 hermes 自己那个解释器：`hermes` 入口脚本的 shebang。
//
// 找的顺序：真实入口（跳过 shim，见 Agent().FindReal 的同一条判据）→ 解析链接 →
// 读第一行。任何一步不成立返回空串（调用方降级，见文件头）。
func hermesPython() string {
	bin := realHermes()
	if bin == "" {
		return ""
	}
	f, err := os.Open(bin)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 256)
	n, _ := f.Read(head)
	line := string(head[:n])
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if !strings.HasPrefix(line, "#!") {
		return ""
	}
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(fields) == 0 {
		return ""
	}
	py := fields[0]
	// `#!/usr/bin/env python3` 这种：取 env 后面那个名字，在 PATH 上找。
	if filepath.Base(py) == "env" && len(fields) > 1 {
		p, err := exec.LookPath(fields[1])
		if err != nil {
			return ""
		}
		py = p
	}
	if info, err := os.Stat(py); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return ""
	}
	return py
}
