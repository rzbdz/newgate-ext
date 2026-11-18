package thinking

import (
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// signatures is the **error text** for "the upstream says it cannot disable thinking", by dialect.
//
// # Why they live here, not in gateway/quirk
//
// The three texts are the dialects of Zhipu GLM, DeepSeek and Kimi, and the **patch** (turning
// `thinking:{"type":"disabled"}` into `enabled` + `reasoning_effort:low`, see st-always.go)
// also lives in this module. Per the repo's rule (modules/deepseek/shape.go says it clearly):
// "criteria stay in the module that knows the truth, so that module can test and evolve them,
// and no upstream-proprietary string ever appears in core".
//
// They used to be hard-coded in `gateway/quirk`, so the only way to add an upstream was to edit
// gateway. That cost was paid once: before kimi's wording was added, "no signature matched,
// quirk could never learn it", and every background call hit the 400 again; GLM had learned it
// days earlier only because its text was Chinese. Same pit, same patch, just misplaced text.
//
// # The bar for adding one
//
// It must be **reproduced live** error text (put the dump filename in the comment), and the
// patch must make sense. Guessed rules add fields to unrelated requests - worse than no fix.
func signatures() []quirk.Signature {
	return []quirk.Signature{{
		Flag: quirk.NoThinkingDisable,
		// Any holds **byte-exact upstream error text**: these are match criteria (the quirk side
		// does bytes.Contains), not user-facing copy, so they **skip i18n** - translating one means
		// it never matches again, symptom "quirk never learns this vendor" (noted at the file head).
		Any: []string{
			// Zhipu GLM, code 1210.
			"does not support disabling thinking",
			"始终思考",               // 同上，措辞变体
			"cannot be disabled", // deepseek: thinking options type cannot be disabled…
			"does not support disabling thinking",
			"thinking cannot be turned off",
			// kimi: live dumps dump/err-400-req000213, req000230 (route:
			// mid -> kimi/kimi-k2.7-code, a Claude Code background call whose thinking
			// field was absent; the claudecode background plugin added thinking:{"type":"disabled"}
			// and the upstream answered with this).
			"only type=enabled is allowed",
		},
		Label: i18n.T("this model always thinks", nil),
	}}
}
