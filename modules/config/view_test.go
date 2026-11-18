package config

import (
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// This pair pins what **the current-config card** looks like in the UI, because
// its first version was wrong, and wrong in a way that is not easy to spot.
//
// What happened (pointed out by the user on 2026-09-21): the tier already in effect did not
// carry that pair of apply buttons -- that judgment was right ("set this one as the default"
// shown on itself is pure noise). But the implementation at the time was `return nil`, so
// **there was nothing at all on the card**: of sixteen tier cards fifteen had two buttons and
// one had none, and the two cards looked almost identical. The user could not tell which was in
// effect, and that is exactly what he most wants to know when opening this section. His words:
//
//	you should inject an info-only green: the current config / active profile effect!
//
// So now that card carries a green Note, while **the buttons are still not
// drawn**. The two must be asserted together: asserting only "there is a Note"
// would miss "the buttons grew back", and asserting only "no buttons" would
// miss "nothing was said" -- which is exactly the shape of the original bug.

func TestTheActiveProfileSaysSoAndTheOthersOfferToApply(t *testing.T) {
	testkit.Sandbox(t)
	seedFile(t, "demo.kv", "desc=demo base\nnormal=p/demo-model\n")
	seedFile(t, "alt.kv", "desc=alt base\nnormal=p/alt-model\n")
	if err := store.SetDefaultProfile("demo", false); err != nil {
		t.Fatalf("setting the default tier failed: %v", err)
	}
	all := []string{"demo", "alt"}

	active := profileConcept("demo", all)
	other := profileConcept("alt", all)

	// 1. The one in effect: a green statement, and **no** apply buttons.
	if active.Note == nil {
		t.Error("the active-profile card has no Note -- it looks just like the other cards in the UI," +
			"so the user cannot tell which one is in effect (that is exactly why this field exists, see profileNote)")
	} else {
		if active.Note.Text == "" {
			t.Error("Note.Text is empty: the card would draw an empty white box")
		}
		if active.Note.Tone != view.ToneOK {
			t.Errorf("the Note tone is %q, should be %q -- it says \"everything is fine, this is the one"+
				"in effect right now\", not a warning and not an error", active.Note.Tone, view.ToneOK)
		}
	}
	if len(active.Actions) != 0 {
		t.Errorf("the active-profile card carries %d buttons, should be 0: a \"set as default\""+
			"on its own card is pure noise", len(active.Actions))
	}

	// 2. Another one: the other way round -- two buttons, and **no** Note.
	if len(other.Actions) != 2 {
		t.Errorf("the other tier cards carry %d buttons, should be 2 (use / use globally)", len(other.Actions))
	}
	if other.Note != nil {
		t.Errorf("a card that is not the active profile also wrote a Note (%q): this sentence is a verdict,"+
			"and writing it on a card that is not in effect is a lie", other.Note.Text)
	}
}

// TestExactlyOneCardCarriesTheActiveNote is the **set-shape** half of the previous one.
//
// The previous one asks card by card "is this one right"; this one asks "how many say this
// sentence in total". They are not the same thing: if the criterion were changed to always
// true (or to compare another field), "each one looks right on its own" would still hold,
// and only the count would go red. And the sentence's meaning is **"this very one"** -- only
// one card in an assembly should be able to say it, one more means the criterion is wrong.
//
// Three cards rather than two, so that "exactly one"
// and "all of them drawn" are separable by count.
func TestExactlyOneCardCarriesTheActiveNote(t *testing.T) {
	testkit.Sandbox(t)
	names := []string{"demo", "alt", "third"}
	for _, n := range names {
		seedFile(t, n+".kv", "desc="+n+"\nnormal=p/"+n+"-model\n")
	}
	if err := store.SetDefaultProfile("alt", false); err != nil {
		t.Fatalf("setting the default tier failed: %v", err)
	}

	var marked []string
	for _, n := range names {
		if c := profileConcept(n, names); c.Note != nil {
			marked = append(marked, n)
		}
	}
	if len(marked) != 1 || marked[0] != "alt" {
		t.Errorf("the cards carrying the \"active profile\" sentence are %v, should be exactly [alt] -- this sentence is a verdict,"+
			"only one card in an assembly can say it (one more means the check is wrong, one fewer means nobody says it)", marked)
	}
}
