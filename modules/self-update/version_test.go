package selfupdate

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in    string
		dist  string
		ok    bool
		want  version
		label string
	}{
		{"v0.8.0", "", true, version{minor: 8}, "bare tag"},
		{"default-v0.8.0-40-g59f5b3c", "default", true, version{minor: 8, ahead: 40, hash: "59f5b3c"}, "describe with distribution"},
		{"default-v0.8.0-40-g59f5b3c-dirty", "default", true, version{minor: 8, ahead: 40, hash: "59f5b3c", dirty: true}, "dirty"},
		{"v0.9.0-rc1", "", true, version{minor: 9, pre: "rc1"}, "pre-release"},
		{"v0.9.0-rc1-3-gabcdef0", "", true, version{minor: 9, pre: "rc1", ahead: 3, hash: "abcdef0"}, "pre-release + describe"},
		{"dev", "", false, version{}, "go run"},
		{"default-59f5b3c", "default", false, version{}, "describe without tags"},
	}
	for _, tt := range tests {
		got, ok := parseVersion(tt.in, tt.dist)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("%s: parseVersion(%q) = %+v, %v; want %+v, %v", tt.label, tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// 开发机的常态是「比最新 Release 新」：v0.8.0 之后 40 个提交不能被当成 v0.8.0，
// 否则 `newgate upgrade` 会把开发机换回 Release。
func TestCompareCountsCommitsAfterTheTag(t *testing.T) {
	v := func(s string) version {
		t.Helper()
		x, ok := parseVersion(s, "")
		if !ok {
			t.Fatalf("cannot parse %q", s)
		}
		return x
	}
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.8.0-40-gabcdef0", "v0.8.0", 1},
		{"v0.8.0", "v0.8.0", 0},
		{"v0.9.0", "v0.8.0-40-gabcdef0", 1},
		{"v0.9.0-rc1", "v0.9.0", -1},
		{"v0.10.0", "v0.9.9", 1},
		{"v1.0.0", "v0.99.0", 1},
	}
	for _, c := range cases {
		if got := compare(v(c.a), v(c.b)); got != c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
