// Package store 里的 kv.go：profile 的 KV 文本格式（mappings/*.kv）。
//
// 为什么要有第二种格式：嵌套 JSON 手写容易错（逗号、括号、引号），
// 而 profile 恰恰是用户最常手改的东西——换个模型名、加个变体。KV 一行
// 一个字段，肉眼可查：
//
//	# mappings/kimi-fast.kv
//	extends=kimi
//	excluded=true
//	desc=Kimi 主循环换高速版
//	mid=kimi-k2.7-code-highspeed
//
// 语法：`key=value` 一行一个；`#` 开头是注释；空行忽略。档位缩写
// （heavy/normal/mid/light/vision）各占一个 key，值是 `provider/model` 或裸模型
// 名（extends 时从 base 同档位借 provider）；多个候选用逗号分隔。
// 未知 key **报错**，不静默忽略（docs/04-configuration.md 的老规矩）。
//
// 优先级：同名 .kv 和 .json 并存时 **.kv 赢**——kv 只会被人刻意创建，
// 它出现就是最新的意图。`newgate profile kv <名> --write` 转换时会把
// .json 改名成 .bak，正常用不会踩到并存。
package store

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate/lib/i18n"
)

// tierKeys 档位缩写 → roles key。
//
// 只留给「这是不是内置档位」的判断用；profile 里能写的 key 集合更宽——模块
// 贡献的动态角色键（omo-sisyphus、cat-deep）同样能当 key 写，见 IsKnownRole。
var tierKeys = map[string]bool{"heavy": true, "normal": true, "mid": true, "light": true, "vision": true}

// ParseProfileKV 把 KV 文本解析成 Profile（Extends 的合并不在这做，
// 由 LoadProfile 负责）。
func ParseProfileKV(text string) (*domain.Profile, error) {
	p := &domain.Profile{Roles: map[string]domain.Candidates{}}
	for ln, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, i18n.E("line {line} is not key=value: {text}",
				i18n.A{"line": ln + 1, "text": line})
		}
		key, val := strings.TrimSpace(line[:eq]), strings.TrimSpace(line[eq+1:])
		if val == "" {
			return nil, i18n.E("line {line}: {key}= has an empty value",
				i18n.A{"line": ln + 1, "key": key})
		}
		var err error
		switch {
		case key == "name":
			p.Name = val
		case key == "extends":
			p.Extends = val
		case key == "desc" || key == "description":
			p.Description = val
		case key == "prio" || key == "priority":
			var n int
			if n, err = strconv.Atoi(val); err == nil {
				v := n
				p.Priority = &v
			}
		case key == "excluded":
			p.Excluded, err = parseBool(val)
		case key == "pinned":
			p.Pinned, err = parseBool(val)
		case key == "window":
			p.ContextWindow, err = strconv.Atoi(val)
		case key == "compact":
			p.AutoCompactWindow, err = strconv.Atoi(val)
		case key == "fallback":
			var c domain.Candidates
			if c, err = parseCandidates(val); err == nil {
				p.Fallback = &c[0]
			}
		case domain.IsKnownRole(key):
			// 档位，或模块贡献的动态角色键（omo-sisyphus：omo 的 intra-agent 槽位）。
			// 两类都是「角色」，解析路径完全一样（docs/04-configuration.md）。
			var c domain.Candidates
			if c, err = parseCandidates(val); err == nil {
				p.Roles[key] = c
			}
		case strings.HasPrefix(key, "role."):
			// 任意档位名（含 "*"）：role.search=... / role.*=...
			var c domain.Candidates
			if c, err = parseCandidates(val); err == nil {
				p.Roles[key[len("role."):]] = c
			}
		default:
			return nil, i18n.E("line {line}: unknown key {key} (a typo would be silently "+
				"ignored, so this errors instead)", i18n.A{"line": ln + 1, "key": key})
		}
		if err != nil {
			return nil, i18n.Ef(err, "line {line}: {key}={value}: {err}",
				i18n.A{"line": ln + 1, "key": key, "value": val})
		}
	}
	return p, nil
}

func parseBool(v string) (bool, error) {
	switch v {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return false, i18n.E("booleans are true/false only, no guessing", nil)
}

// parseCandidates `provider/model`、`@别的键`（引用）或裸模型名，逗号分隔多个候选。
// 裸名的 provider 由 Extends 合并时补（fillBareProviders）。
func parseCandidates(val string) (domain.Candidates, error) {
	var out domain.Candidates
	for _, item := range strings.Split(val, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "@") {
			// 引用：整条候选就是这个键的链（就地展开，见 resolve.BuildChain）
			if bd, err := domain.ParseBindingString(item); err == nil {
				out = append(out, bd)
			} else {
				return nil, err
			}
			continue
		}
		if i := strings.IndexByte(item, '/'); i >= 0 {
			out = append(out, domain.Binding{Provider: item[:i], Model: item[i+1:]})
		} else {
			out = append(out, domain.Binding{Model: item})
		}
	}
	if len(out) == 0 {
		return nil, i18n.E("no valid candidate", nil)
	}
	return out, nil
}

// SerializeProfileKV 把 profile 写回 KV 文本（转换用）。只写**非零**字段，
// 配合 Extends 就是紧凑的变体声明；档位多候选用逗号展开。
// name 不写——文件名就是名字。
func SerializeProfileKV(p *domain.Profile) string {
	var sb strings.Builder
	put := func(k, v string) { fmt.Fprintf(&sb, "%s=%s\n", k, v) }
	if p.Extends != "" {
		put("extends", p.Extends)
	}
	if p.Description != "" {
		put("desc", p.Description)
	}
	if p.Priority != nil {
		put("prio", strconv.Itoa(*p.Priority))
	}
	if p.Excluded {
		put("excluded", "true")
	}
	if p.Pinned {
		put("pinned", "true")
	}
	if p.ContextWindow > 0 {
		put("window", strconv.Itoa(p.ContextWindow))
	}
	if p.AutoCompactWindow > 0 {
		put("compact", strconv.Itoa(p.AutoCompactWindow))
	}
	for _, tier := range domain.Roles {
		if c, ok := p.Roles[tier]; ok && len(c) > 0 {
			items := make([]string, len(c))
			for i, b := range c {
				items[i] = b.String()
			}
			put(tier, strings.Join(items, ", "))
		}
	}
	// 4 个标准档位之外的 roles key（含 "*"）走 role.<名>=，保证无损往返
	for _, k := range sortedRoleKeys(p.Roles) {
		if tierKeys[k] {
			continue
		}
		items := make([]string, len(p.Roles[k]))
		for i, b := range p.Roles[k] {
			items[i] = b.String()
		}
		put("role."+k, strings.Join(items, ", "))
	}
	if p.Fallback != nil {
		put("fallback", p.Fallback.String())
	}
	return sb.String()
}

func sortedRoleKeys(m map[string]domain.Candidates) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
