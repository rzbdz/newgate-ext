package injection

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
	"github.com/rzbdz/newgate/lib/i18n"
)

const (
	beginMark = "# >>> newgate >>>"
	endMark   = "# <<< newgate <<<"
)

// Dir is the directory holding the shims. It is prepended to PATH.
func Dir() string { return paths.ShimDir() }

// Install creates a shim for a tool: Dir()/<tool> points at newgate itself.
//
// Why a PATH shim rather than writing the tool's config file: the user's shell rc often
// already exports ANTHROPIC_BASE_URL / AUTH_TOKEN, and the shell environment outranks
// settings.json's env block - a config write is silently overridden, the hardest fault
// to diagnose. The shim sets env in the child process and beats everything.
func Install(toolID string) (string, error) {
	agents := agentstate.Catalog()
	if _, ok := agents.Get(toolID); !ok {
		return "", i18n.E("unknown tool {tool} (known: {list})",
			i18n.A{"tool": toolID, "list": strings.Join(agents.Names(), ", ")})
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return "", err
	}
	link := filepath.Join(Dir(), toolID)
	_ = os.Remove(link)
	if err := os.Symlink(self, link); err != nil {
		return "", err
	}
	return link, nil
}

// Uninstall pulls one shim. Two gates: the name must be a known agent, the link must be ours.
// A real executable of the same name, a user's claude-bak, a hand-written wrapper are left
// alone - keeping them beats deleting them, they are the user's emergency tool. Use Foreign().
func Uninstall(toolID string) error {
	agents := agentstate.Catalog()
	if _, ok := agents.Get(toolID); !ok {
		return i18n.E("{tool} is not a known agent, left untouched (the rest of the shim dir does not belong to newgate)",
			i18n.A{"tool": toolID})
	}
	link := filepath.Join(Dir(), toolID)
	if _, err := os.Lstat(link); err != nil {
		return err
	}
	if !isOurs(link) {
		return i18n.E("{link} is not a link newgate installed, left untouched", i18n.A{"link": link})
	}
	return os.Remove(link)
}

// Installed lists the shims **we installed**.
//
// Only entries "whose name is a known agent and which is a symlink to the newgate binary".
// Why so strict: newgate stop deletes this list one by one. The dir may hold a renamed
// claude-bak, a hand-placed script - not ours, neither "taken over" nor to be deleted.
// Use Foreign() to see them.
func Installed() []string {
	agents := agentstate.Catalog()
	ents, err := ioutil.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if _, ok := agents.Get(e.Name()); !ok {
			continue
		}
		if !isOurs(filepath.Join(Dir(), e.Name())) {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

// Foreign lists entries in the shim directory that are not ours. Shown to the user, untouched.
func Foreign() []string {
	agents := agentstate.Catalog()
	ents, err := ioutil.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if _, ok := agents.Get(name); ok && isOurs(filepath.Join(Dir(), name)) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// isOurs: is this path a symlink **we installed**.
//
// Either of two criteria suffices:
//   - it points at the current executable (a fresh shim always does, the most reliable)
//   - the target name contains newgate (binary moved/upgraded, link still points old)
//
// Why not the name alone: a user may install the binary under another name, and then
// judging by name calls our own link a foreign one, and stop cannot pull it.
func isOurs(link string) bool {
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	dst, err := os.Readlink(link)
	if err != nil {
		return false
	}
	if self, err := os.Executable(); err == nil && sameFile(dst, self) {
		return true
	}
	return strings.Contains(filepath.Base(dst), "newgate")
}

// sameFile: do two paths point at the same file (following symlinks, which resolves
// equivalent paths like /var and /private/var).
func sameFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// RCFiles are the shell startup files needing PATH (only existing ones).
func RCFiles() []string {
	home := paths.Home()
	var out []string
	for _, f := range []string{".zshrc", ".bashrc", ".bash_profile", ".profile"} {
		p := filepath.Join(home, f)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func block() string {
	return fmt.Sprintf("%s\n# %s\n# %s\nexport PATH=\"%s:$PATH\"\n%s\n",
		beginMark,
		i18n.T("newgate's shim dir must come first in PATH so that it can intercept claude and other commands", nil),
		i18n.T("remove this block (or run newgate shim uninstall) to restore everything", nil),
		Dir(), endMark)
}

// HasBlock: does that rc file hold our block.
func HasBlock(path string) bool {
	b, err := ioutil.ReadFile(path)
	return err == nil && strings.Contains(string(b), beginMark)
}

// AddToRC appends a PATH block to an rc file. Idempotent. The original is backed up first.
// **Append** only, touching no existing line - including the user's own ANTHROPIC_* exports
// (the shim overrides them in the child, no need to touch them).
func AddToRC(path string) (changed bool, err error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return false, err
	}
	if strings.Contains(string(b), beginMark) {
		return false, nil
	}
	if err := backupRC(path, b); err != nil {
		return false, err
	}
	out := string(b)
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += "\n" + block()
	return true, writeAtomicMode(path, []byte(out), 0o644)
}

// RemoveFromRC deletes exactly our block, leaving other bytes alone.
func RemoveFromRC(path string) (changed bool, err error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return false, err
	}
	s := string(b)
	i := strings.Index(s, beginMark)
	if i < 0 {
		return false, nil
	}
	j := strings.Index(s[i:], endMark)
	if j < 0 {
		return false, i18n.E("{file} has a begin marker but no end marker; not editing it automatically, please delete the lines after {mark} by hand",
			i18n.A{"file": path, "mark": beginMark})
	}
	end := i + j + len(endMark)
	// Swallow the newline that follows along with it
	for end < len(s) && s[end] == '\n' {
		end++
	}
	// Swallow the blank line we added before the block
	start := i
	for start > 0 && s[start-1] == '\n' {
		start--
		if start > 0 && s[start-1] != '\n' {
			break
		}
	}
	if err := backupRC(path, b); err != nil {
		return false, err
	}
	return true, writeAtomicMode(path, []byte(s[:start]+"\n"+s[end:]), 0o644)
}

func backupRC(path string, content []byte) error {
	dir := filepath.Join(paths.BackupDir(), "rc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(dir, filepath.Base(path)+".bak"), content, 0o600)
}

// writeAtomicMode is an atomic write with a permission mode. writeAtomic in config_file.go
// is fixed at 0600 (config files); rc files need 0644.
//
// Files in the **shared config dir** (omo-slots.json) use 0660: the daemon often runs as
// another user (root installs, claude runs), and 0600 makes it unreadable. Hit live on
// 2026-09-16: takeover succeeded, the file was written, and the daemon registered no slot
// key at all - symptom "the file is right there, the daemon says it does not know the model".
//
// The explicit chmod is necessary: WriteFile applies mode only to **new** files, and a
// leftover tmp file keeps the old permissions.
func writeAtomicMode(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".newgate.tmp"
	if err := ioutil.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// InPath: is our shim dir in this process's PATH, and ahead of the real tool.
func InPath() bool {
	want := filepath.Clean(Dir())
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(d) == want {
			return true
		}
	}
	return false
}
