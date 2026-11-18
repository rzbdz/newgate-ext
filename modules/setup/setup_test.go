package setup

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"path/filepath"
	"strings"
	"testing"

	term "github.com/rzbdz/newgate-ext/lib/term"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate-ext/modules/runtime"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// 本文件用一台**假终端**驱动整个向导：脚本化的按键进来，屏幕画到 buffer 里。
// 它锁的是那些「用户看不见、但决定这台机器变成什么样」的判断：
//
//	已经配好的人跑第二遍   → 不问上游，只装/接管他选的那几家
//	没勾的客户端           → 一个都不碰（多选那一屏写着的话必须是真的）
//	装不上                 → 说清楚，别把那一步算成成功
//	没有终端               → 什么都不写，逐条告诉人敲什么

// fakeScreen 按脚本吐按键与整行输入。
type fakeScreen struct {
	events []term.Event
	lines  []string
	picked []func(string, string, int) // 每次 render 都会被记下来（可选断言）
	sent   *bytes.Buffer
}

func (f *fakeScreen) ReadEvent() term.Event {
	if len(f.events) == 0 {
		return term.Event{Key: term.KeyInterrupt}
	}
	ev := f.events[0]
	f.events = f.events[1:]
	return ev
}

func (f *fakeScreen) ReadLine(mode term.LineMode, render func(string, string, int)) term.LineResult {
	if len(f.lines) == 0 {
		return term.LineResult{Cancel: true}
	}
	line := f.lines[0]
	f.lines = f.lines[1:]
	if mode.Validate != nil {
		if msg := mode.Validate(line); msg != "" {
			return term.LineResult{Cancel: true}
		}
	}
	return term.LineResult{Text: line}
}

func keys(ks ...term.Key) []term.Event {
	out := make([]term.Event, len(ks))
	for i, k := range ks {
		out[i] = term.Event{Key: k}
	}
	return out
}

func concat(parts ...[]term.Event) []term.Event {
	var out []term.Event
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func runes(rs ...rune) []term.Event {
	out := make([]term.Event, len(rs))
	for i, r := range rs {
		out[i] = term.Event{Key: term.KeyRune, Rune: r}
	}
	return out
}

// fakeCatalog 是最小的代理目录：说清「谁在、谁能被装上」。
type fakeCatalog struct {
	agents       map[string]*agentapi.Agent
	installed    map[string]bool
	installs     *[]string // 记下跑过哪些安装命令的 agent
	installFails map[string]bool
}

func (c fakeCatalog) Get(id string) (*agentapi.Agent, bool) { a, ok := c.agents[id]; return a, ok }
func (c fakeCatalog) Names() []string {
	out := make([]string, 0, len(c.agents))
	for id := range c.agents {
		out = append(out, id)
	}
	return out
}
func (c fakeCatalog) Facts(string) agentapi.AgentFacts { return nil }
func (c fakeCatalog) Installer(id string) agentapi.AgentInstaller {
	if _, ok := c.agents[id]; !ok {
		return nil
	}
	return fakeInstaller{id: id, catalog: c}
}
func (c fakeCatalog) Installed(id string) bool { return c.installed[id] }

type fakeInstaller struct {
	id      string
	catalog fakeCatalog
}

func (f fakeInstaller) Command() []string { return []string{"fake-install", f.id} }
func (f fakeInstaller) Install(out io.Writer) error {
	if f.catalog.installFails != nil && f.catalog.installFails[f.id] {
		return errors.New("fake install failed")
	}
	if f.catalog.installs != nil {
		*f.catalog.installs = append(*f.catalog.installs, f.id)
	}
	f.catalog.installed[f.id] = true
	return nil
}

// fakeControl 记下每一次接管/起代理的调用。
type fakeControl struct {
	status      []runtimeapi.Status
	takenOver   []string
	started     int
	startOpts   runtimeapi.StartOptions
	takeoverErr map[string]error
}

func (c *fakeControl) TakeOver(agent string, port int) runtimeapi.Result {
	if err := c.takeoverErr[agent]; err != nil {
		return runtimeapi.Result{Agent: agent, Err: err}
	}
	c.takenOver = append(c.takenOver, agent)
	return runtimeapi.Result{Agent: agent, Lines: []string{"taken"}}
}
func (c *fakeControl) Release(agent string) runtimeapi.Result { return runtimeapi.Result{Agent: agent} }
func (c *fakeControl) TakeOverAll(port int) []runtimeapi.Result {
	return nil
}
func (c *fakeControl) TakeoverStatus() []runtimeapi.Status { return c.status }
func (c *fakeControl) Start(opts runtimeapi.StartOptions) (runtimeapi.StartReport, error) {
	c.started++
	c.startOpts = opts
	return runtimeapi.StartReport{Running: true, PID: 1, Port: 8899, Profile: "default"}, nil
}

// stubModels 把「问上游清单」换成一个固定答案，测试因而不出网。
func stubModels(t *testing.T, models []string, why string) {
	t.Helper()
	old := probeModels
	probeModels = func(string, string, string) ([]string, string) { return models, why }
	t.Cleanup(func() { probeModels = old })
}

func sandbox(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
}

func catalogWith(ids ...string) fakeCatalog {
	c := fakeCatalog{agents: map[string]*agentapi.Agent{}, installed: map[string]bool{}}
	for _, id := range ids {
		// 只带一个 Bin，够 pick/EnsureAgents 用它；接管走的是 fakeControl。
		c.agents[id] = &agentapi.Agent{ID: id, Bin: []string{id}}
		c.installed[id] = true
	}
	return c
}

// TestSecondRunDoesNotAskForUpstream 是「已经能跑的人重跑向导」那条路：他们多半
// 只是想装一个新的客户端。被问到 base URL 时最可能的结果是直接 Ctrl-C——而他想要
// 的那件事一件都没做。
func TestSecondRunDoesNotAskForUpstream(t *testing.T) {
	sandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatal(err)
	}
	// 让它「可用」：占位 provider 全部配上 key。
	provs, _ := store.LoadProviders()
	for name, p := range provs.Providers {
		p.APIKey, p.APIKeyEnv = "sk-real", ""
		provs.Providers[name] = p
	}
	if err := store.SaveProviders(provs); err != nil {
		t.Fatal(err)
	}
	if !Inspect().HasUsable {
		t.Fatal("前置条件没成立：这份配置本该被判成可用")
	}

	// 一行都不许被问：脚本里不给任何输入，走到上游那一屏就会取消。
	scr := &fakeScreen{events: keys(term.KeyEnter)}
	cat := catalogWith("claude")
	ctl := &fakeControl{}
	var out bytes.Buffer

	code := runWithScreen(&out, Deps{Agents: cat, Control: ctl, Out: &out}, Options{NoStart: true}, scr)
	if code != 0 {
		t.Fatalf("退出码 %d，想要 0", code)
	}
	if len(ctl.takenOver) != 1 || ctl.takenOver[0] != "claude" {
		t.Fatalf("接管了 %v，想要 [claude]", ctl.takenOver)
	}
	if got := store.LoadState().DefaultProfile; got == ProfileName {
		t.Fatal("第二次运行把全局默认改成了 default（它不该碰配置）")
	}
}

// TestUncheckedClientsAreNotTouched：多选那一屏的话是「跳过它们就不变」。这一条
// 把它钉住——`Start` 必须带着 SkipTakeover 被调用，而且接管只发生在勾了的那几家。
func TestUncheckedClientsAreNotTouched(t *testing.T) {
	sandbox(t)
	// 没有可用配置 → 会问上游（脚本里给现成的答案）。
	cat := catalogWith("claude", "codex")
	// codex 没装、claude 已接管 → 默认只勾 codex；把它取消、勾上 claude。
	cat.installed["codex"] = false
	ctl := &fakeControl{status: []runtimeapi.Status{{Agent: "claude", Active: true}}}
	var out bytes.Buffer

	stubModels(t, []string{"demo-model"}, "")
	scr := &fakeScreen{
		// 列表顺序 claude, codex：光标在 codex（默认勾着），取消它，跳到 claude 勾上。
		events: concat(keys(term.KeyUp), runes(' '), keys(term.KeyDown, term.KeyEnter, term.KeyEnter)),
		lines:  []string{"https://api.example.com/v1", "sk-test", "demo"},
	}
	code := runWithScreen(&out, Deps{Agents: cat, Control: ctl, Out: &out}, Options{NoStart: true, Yes: false}, scr)
	if code != 0 {
		t.Fatalf("退出码 %d（输出：%s）", code, out.String())
	}
	for _, id := range ctl.takenOver {
		if id == "codex" {
			t.Fatalf("没勾的 codex 被接管了（输出：%s）", out.String())
		}
	}
	if len(ctl.takenOver) != 1 || ctl.takenOver[0] != "claude" {
		t.Fatalf("接管了 %v，想要 [claude]", ctl.takenOver)
	}
	if !strings.Contains(out.String(), "Next") {
		t.Fatalf("收尾那一屏没画出来：%s", out.String())
	}
}

// TestNotTakenOverYetIsSaidOutLoud：配置型客户端第一次接管会「跳过（没有配置文件）」，
// 那不是接管成功。汇总里必须写明——否则用户在它里面发请求走的是直连，而他以为配好了。
func TestNotTakenOverYetIsSaidOutLoud(t *testing.T) {
	sandbox(t)
	step := Step{Agent: "codex", Installed: true, TakenOver: false,
		Takeover: runtimeapi.Result{Agent: "codex", Lines: []string{"~/.codex/config.toml (skipped: no such file)"}}}
	var b bytes.Buffer
	Report(&b, []Step{step})
	if !strings.Contains(b.String(), "not taken over yet") {
		t.Fatalf("跳过的接管被报成了成功：%s", b.String())
	}

	var out strings.Builder
	printOutcome(&out, ApplyResult{}, []Step{step}, StartReporter{Next: nextSteps([]Step{step})})
	if !strings.Contains(out.String(), "not taken over yet") {
		t.Fatalf("收尾那一屏把跳过的接管报成了成功：%s", out.String())
	}
	if !strings.Contains(out.String(), "then: newgate on codex") {
		t.Fatalf("收尾没给出「先跑一次再接管」那条路：%s", out.String())
	}
}

// TestNoTTYWritesNothing：非交互环境里绝不静默改配置。
func TestNoTTYWritesNothing(t *testing.T) {
	sandbox(t)
	before := snapshot(t)
	var out bytes.Buffer
	code := noTTY(Deps{Out: &out}, Options{}, term.ErrNotATTY)
	if code == 0 {
		t.Fatal("没有终端却报了成功")
	}
	if after := snapshot(t); after != before {
		t.Fatal("没有终端时改动了配置目录")
	}
}

func snapshot(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, p := range []string{paths.ProvidersFile(), paths.StateFile()} {
		if raw, err := ioutil.ReadFile(p); err == nil {
			b.WriteString(p)
			b.Write(raw)
		}
	}
	ents, _ := ioutil.ReadDir(paths.Mappings())
	for _, e := range ents {
		b.WriteString(e.Name())
	}
	_ = filepath.Separator
	return b.String()
}
