package webdashboard

import (
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The front end's two dictionaries (`web/src/i18n.ts` en / zh-Hans) must have **aligned
// keys**, and every sentence must really appear in **the shipped bundle**.
//
// Why a test guards it: when `t()` misses, it **returns the English sentence verbatim** -
// deliberate (an English sentence beats a blank or a key name). The cost: "translation
// missing" and "deliberately untranslated" look the same at run time; only alignment tells.
//
// Read TS/JS from Go instead of installing a front-end test runner: CI must be **fully
// offline** (the front-end toolchain only exists on dev machines, see web_test.go). This is
// a **text-level** check, no node needed - the same trade-off as artifacts-in-git + Go gate.
//
// Boundary stated plainly: it cannot check "are the translations right" (that needs a human),
// nor all of "is the bundle current" - only **the dictionary half** (see the bundle test).

var dictStart = regexp.MustCompile(`(?m)^const (en|zhHans): Record<string, string> = \{`)

// keyLine grabs the key at the start of a line. Both spellings must be recognized:
//
//	"no rows": "no data",
//	tail: "tail end",
//
// It also accepts **key and value on separate lines** (long sentences); key lines end in ':'.
var keyLine = regexp.MustCompile(`(?m)^\s*(?:"((?:[^"\\]|\\.)*)"|([A-Za-z_$][A-Za-z0-9_$]*))\s*:`)

func TestFrontendDictionariesHaveTheSameKeys(t *testing.T) {
	en := dictionaryKeys(t, "en")
	zh := dictionaryKeys(t, "zhHans")

	var missingZh, missingEn []string
	for k := range en {
		if !zh[k] {
			missingZh = append(missingZh, k)
		}
	}
	for k := range zh {
		if !en[k] {
			missingEn = append(missingEn, k)
		}
	}
	sort.Strings(missingZh)
	sort.Strings(missingEn)

	for _, k := range missingZh {
		t.Errorf("zh-Hans is missing this entry (the UI would silently show English): %q", k)
	}
	for _, k := range missingEn {
		t.Errorf("en is missing this entry (the key is that English sentence; missing means it was deleted elsewhere): %q", k)
	}
}

// TestShippedBundleHasEverySentence gates "edited the front end, forgot `pnpm build`".
//
// web_test.go is explicit: it cannot check "is the artifact current" (that needs node, and
// CI must be offline). This one covers **the most common half**: every UI sentence must
// appear in the bundle that really ships. Add a string without rebuilding and the source
// has it, the bundle does not - that sentence never shows (falls back to the key, English).
//
// The other half stays unchecked: style, layout and logic changes are invisible here.
// An honest boundary beats a fake guarantee that looks complete.
func TestShippedBundleHasEverySentence(t *testing.T) {
	bundle := shippedJS(t)
	if bundle == "" {
		t.Fatal("not a single .js in the output -- was web/dist committed without being built?")
	}

	var missing []string
	for k := range dictionaryKeys(t, "en") {
		if !strings.Contains(bundle, jsStringLiteral(k)) {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		t.Errorf("this sentence is in the source dictionary but not in the shipped bundle: %q\n"+
			"    -- after changing the front end run `cd web && pnpm build` and commit (the output is version-controlled, see README)", k)
	}
}

// dictionaryKeys pulls the keys of one dictionary out of `web/src/i18n.ts`.
func dictionaryKeys(t *testing.T, want string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("web/src/i18n.ts")
	if err != nil {
		t.Fatalf("cannot read the front-end dictionary (%v) -- if the path changed, change this test too", err)
	}
	src := string(raw)
	locs := dictStart.FindAllStringSubmatchIndex(src, -1)
	if len(locs) != 2 {
		t.Fatalf("should find both the en and zhHans dictionaries, actual %d --"+
			"if the form changes (renamed, different type annotation), these two regexes must change with it", len(locs))
	}
	for _, loc := range locs {
		if src[loc[2]:loc[3]] != want {
			continue
		}
		body := src[loc[1]:]
		if end := strings.Index(body, "\n};"); end >= 0 {
			body = body[:end]
		}
		keys := map[string]bool{}
		for _, m := range keyLine.FindAllStringSubmatch(body, -1) {
			k := m[1]
			if k == "" {
				k = m[2]
			}
			keys[strings.ReplaceAll(k, `\"`, `"`)] = true
		}
		if len(keys) == 0 {
			t.Fatalf("not a single key was parsed out of dictionary %s -- the regex no longer matches the file", want)
		}
		return keys
	}
	t.Fatalf("there is no dictionary named %s", want)
	return nil
}

// shippedJS reads out the JS that is **actually shipped**.
//
// It reads the embedded copy (`assets`), not the source on disk: that is what the user
// really gets, and the only meaningful object under the artifacts-in-git trade-off. Files
// are content-hashed, so names change every build - walk by suffix, never hard-code.
func shippedJS(t *testing.T) string {
	t.Helper()
	sub, err := fs.Sub(assets, AssetDir)
	if err != nil {
		t.Fatalf("cannot read the embedded front-end directory (does %s exist?): %v", AssetDir, err)
	}
	var out strings.Builder
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return walkErr
		}
		raw, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		out.Write(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the output: %v", err)
	}
	return out.String()
}

// jsStringLiteral renders a sentence as a JS literal (quotes it, escapes inner quotes),
// for a **literal** lookup in the bundle.
//
// Contains, not a regex: keys contain `.`, `{`, `(`, `-` and the like; as a regex that
// misjudges (at best misses, at worst matches what it should not).
func jsStringLiteral(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// TestFrontendDictionaryKeysAreSentences pins "key = the English sentence".
//
// Front-end keys follow the same tradition as backend i18n msgids (see core/docs i18n):
// no "message ID" indirection. So a key that **looks like an identifier** (`saveButton`)
// means someone started a new style on the side - and that turns "where did this UI
// sentence come from" back into a lookup question, the very thing this style kills.
//
// The exception is the short words (`tail` / `follow` / `revert` / `unsaved` ...): they
// are the English sentence, not a program-facing name. The criterion is shape, not a list.
func TestFrontendDictionaryKeysAreSentences(t *testing.T) {
	// camelCase / snake_case is the "identifier-like" key. A single lowercase word (tail,
	// follow) is a sentence; let it pass.
	camel := regexp.MustCompile(`[a-z][A-Z]|_`)
	for k := range dictionaryKeys(t, "en") {
		if camel.MatchString(k) {
			t.Errorf("key %q looks like an identifier -- the key should be that English sentence itself (see the top of web/src/i18n.ts)", k)
		}
	}
}
