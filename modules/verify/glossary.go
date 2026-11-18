package verify

import (
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// verifyGlossary is the "fingerprint" row in the help screen's glossary.
//
// Why this module reports it: fingerprinding is a term verify introduced, defined only here;
// the UI (cli) does not know it and should not import this module for one row (cliapi.Glossarist).
type verifyGlossary struct{}

var _ cliapi.Glossarist = verifyGlossary{}

func (verifyGlossary) Glossary() []cliapi.GlossaryLine {
	return []cliapi.GlossaryLine{{
		Rank: 40,
		Term: i18n.T("fingerprint", nil),
		Definition: i18n.T(
			"a statistical identity computed from hundreds of integers the upstream generates; compared against the reference bank to see which model really answers",
			nil),
	}}
}
