package selfupdate

import (
	"regexp"
	"strconv"
	"strings"
)

// 版本号的形状来自 build/build.sh：`<distribution>-<git describe --tags --always --dirty>`，
// 例如 `default-v0.8.0-40-g59f5b3c-dirty`。Release 的 tag 是裸的 `v0.8.0`。
//
// 比较只在需要回答「要不要换」时用：Release 比手上的新才换（不 --force 不倒退）。
// 开发机上的二进制常常**比最新 Release 新**（`v0.8.0-40-g…` 是 v0.8.0 之后 40 个
// 提交），所以 describe 的那段提交数要参与比较——只比 v0.8.0 会把它当成「一样」，
// 进而把开发机换回 Release。

var (
	describeTail = regexp.MustCompile(`-(\d+)-g([0-9a-f]{4,})$`)
	semverCore   = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)
)

type version struct {
	major, minor, patch int
	pre                 string // v0.9.0-rc1 的 rc1
	ahead               int    // describe 的提交数（tag 之后几个提交）
	hash                string
	dirty               bool
}

// parseVersion 解析一个 tag 或 describe 串（可以带 distribution 前缀）。
// 认不出来（`dev`、只有提交号的 describe）时 ok 为假：那种版本没法比新旧。
func parseVersion(s, distribution string) (version, bool) {
	var v version
	if distribution != "" {
		s = strings.TrimPrefix(s, distribution+"-")
	}
	if strings.HasSuffix(s, "-dirty") {
		v.dirty = true
		s = strings.TrimSuffix(s, "-dirty")
	}
	if m := describeTail.FindStringSubmatch(s); m != nil {
		v.ahead, _ = strconv.Atoi(m[1])
		v.hash = m[2]
		s = s[:len(s)-len(m[0])]
	}
	m := semverCore.FindStringSubmatch(s)
	if m == nil {
		return v, false
	}
	v.major, _ = strconv.Atoi(m[1])
	v.minor, _ = strconv.Atoi(m[2])
	v.patch, _ = strconv.Atoi(m[3])
	v.pre = m[4]
	return v, true
}

// compare 返回 -1/0/1。预发布低于同号正式版（semver）；同一个 tag 之后，提交数
// 多的更新。dirty 不参与：它说的是「工作区改过」，不是更新或更旧。
func compare(a, b version) int {
	for _, d := range []int{a.major - b.major, a.minor - b.minor, a.patch - b.patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case a.pre == "" && b.pre != "":
		return 1
	case a.pre != "" && b.pre == "":
		return -1
	case a.pre != b.pre:
		if a.pre < b.pre {
			return -1
		}
		return 1
	}
	return sign(a.ahead - b.ahead)
}

func sign(d int) int {
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	}
	return 0
}
