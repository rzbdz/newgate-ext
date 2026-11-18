package locale

import (
	"testing"
)

// env builds a table-lookup-style getenv.
func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// TestResolveOrder pins the priority table down.
//
// The order is a **trade-off** (see the passage in resolve.go), so it must have a test: someone
// wanting "environment first" later changes one line here and the pinned expectation -- rather
// than discovering what they changed when a user complains "I set it and it did nothing".
func TestResolveOrder(t *testing.T) {
	cases := []struct {
		name       string
		env        map[string]string
		configured string
		wantTag    string
		wantSource Source
	}{
		{
			name:       "NEWGATE_LANG beats everything",
			env:        map[string]string{"NEWGATE_LANG": "zh-Hans", "LANG": "en_US.UTF-8"},
			configured: "de",
			wantTag:    "zh-Hans", wantSource: SourceEnvOverride,
		},
		{
			name:       "config beats LANG (this is how an English system reads Chinese)",
			env:        map[string]string{"LANG": "en_US.UTF-8"},
			configured: "zh-Hans",
			wantTag:    "zh-Hans", wantSource: SourceConfig,
		},
		{
			name:    "follow LANG when there is no config",
			env:     map[string]string{"LANG": "zh_CN.UTF-8"},
			wantTag: "zh-CN", wantSource: SourceSystem,
		},
		{
			name:    "LC_ALL beats LC_MESSAGES beats LANG",
			env:     map[string]string{"LC_ALL": "zh_TW.UTF-8", "LC_MESSAGES": "en_US.UTF-8", "LANG": "de_DE.UTF-8"},
			wantTag: "zh-TW", wantSource: SourceSystem,
		},
		{
			name:    "LANG=C means no opinion",
			env:     map[string]string{"LANG": "C"},
			wantTag: "en", wantSource: SourceDefault,
		},
		{
			name:    "LANG=POSIX means no opinion",
			env:     map[string]string{"LANG": "POSIX"},
			wantTag: "en", wantSource: SourceDefault,
		},
		{
			name:    "nothing at all -> the source language",
			env:     map[string]string{},
			wantTag: "en", wantSource: SourceDefault,
		},
		{
			name:    "an empty NEWGATE_LANG does not count",
			env:     map[string]string{"NEWGATE_LANG": "", "LANG": "zh_CN.UTF-8"},
			wantTag: "zh-CN", wantSource: SourceSystem,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolve(env(tc.env), tc.configured)
			if got.Tag != tc.wantTag || got.Source != tc.wantSource {
				t.Errorf("resolve = {%q, %q}, want {%q, %q}",
					got.Tag, got.Source, tc.wantTag, tc.wantSource)
			}
		})
	}
}
