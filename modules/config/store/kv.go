// kv.go inside package store: profile's KV text format (mappings/*.kv).
//
// Why a second format: nested JSON is easy to get wrong by hand (commas, brackets,
// quotes), and profiles are exactly what users edit most by hand -- change a
// model name, add a variant. KV is one field per line, readable at a glance:
//
//	# mappings/kimi-fast.kv
//	extends=kimi
//	excluded=true
//	desc=Kimi main loop on the high-speed version
//	mid=kimi-k2.7-code-highspeed
//
// Syntax: `key=value`, one per line; `#` starts a comment; blank lines are ignored.
// The tier abbreviations (heavy/normal/mid/light/vision) each take one key, and
// the value is `provider/model` or a bare model name (with extends, the provider is
// borrowed from base's same tier); multiple candidates are comma separated. Unknown
// keys **error**, not silently ignored (the old rule in docs/04-configuration.md).
//
// Precedence: when a .kv and a .json of the same name coexist, **.kv wins** -- a kv is only
// ever created deliberately, and its appearance is the latest intent. `newgate profile kv <name>
// --write` renames the .json to .bak when converting, so normal use never hits coexistence.
package store

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate/lib/i18n"
)

// tierKeys tier abbreviation -> roles key.
//
// Kept only for the "is this a built-in tier" judgment; the set of keys that
// can be written in a profile is wider -- module-contributed dynamic role keys
// (omo-sisyphus, cat-deep) can equally be written as a key, see IsKnownRole.
var tierKeys = map[string]bool{"heavy": true, "normal": true, "mid": true, "light": true, "vision": true}

// ParseProfileKV parses KV text into a Profile (Extends
// merging is not done here; LoadProfile is responsible for it).
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
		case key == "visible-agents" || key == "visible_agents":
			// Whitelist: only these agents may route through this profile. Comma separated.
			p.VisibleAgents = splitAgents(val)
		case key == "hidden-agents" || key == "hidden_agents":
			// Blacklist: these agents never see it. Comma separated; same value shape as above.
			p.HiddenAgents = splitAgents(val)
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
			// A tier, or a module-contributed dynamic role key (omo-sisyphus: omo's intra-agent slot). Both
			// classes are "roles", and the resolution path is exactly the same (docs/04-configuration.md).
			var c domain.Candidates
			if c, err = parseCandidates(val); err == nil {
				p.Roles[key] = c
			}
		case strings.HasPrefix(key, "role."):
			// Any tier name (including "*"): role.search=... / role.*=...
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

// splitAgents parses a comma-separated agent list (`visible-agents=gemini, codex`).
//
// Values are kept as written (lowercase ids in this distribution) and duplicates are dropped;
// an unknown id simply matches nothing, because the config layer does not know the client
// catalog (and must not: a distribution's client list is not config's knowledge).
func splitAgents(v string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
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

// parseCandidates `provider/model`, `@another key` (a reference), or
// a bare model name; commas separate several candidates. A bare name's
// provider is filled in when Extends merges (fillBareProviders).
func parseCandidates(val string) (domain.Candidates, error) {
	var out domain.Candidates
	for _, item := range strings.Split(val, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "@") {
			// A reference: the whole candidate is that key's
			// chain (expanded in place, see resolve.BuildChain)
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

// SerializeProfileKV writes a profile back to KV text (for conversion). Writes only
// **non-zero** fields, which together with Extends gives a compact variant declaration; multiple
// candidates per tier are comma separated. name is not written -- the file name is the name.
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
	if len(p.VisibleAgents) > 0 {
		put("visible-agents", strings.Join(p.VisibleAgents, ", "))
	}
	if len(p.HiddenAgents) > 0 {
		put("hidden-agents", strings.Join(p.HiddenAgents, ", "))
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
	// roles keys outside the 4 standard tiers (including "*")
	// go as role.<name>=, to guarantee a lossless round trip
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
