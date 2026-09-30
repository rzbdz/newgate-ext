package sshtunnel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 这一族验的是**读配置**这件事的判据。它是这套东西里最容易被写松的一层：读坏了
// 不报，看起来只是「那条 target 不存在」——而用户会去查 SSH、查防火墙、查远端，
// 唯独不会想到是自己那行配置被静默跳过了。

func sandbox(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	sandbox(t)
	want := Target{
		ID: "snode1", Host: "10.0.50.11", User: "panjunzhong",
		RemotePort: 8899, LocalPort: 9401, Persistent: true, HostKey: HostKeyAcceptNew,
	}
	if err := Save([]Target{want.normalize()}); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if len(got.Rejected) != 0 {
		t.Fatalf("刚写下去的配置被拒了：%+v", got.Rejected)
	}
	if len(got.Targets) != 1 {
		t.Fatalf("读回 %d 条，想要 1", len(got.Targets))
	}
	if got.Targets[0] != want.normalize() {
		t.Errorf("读回的是 %+v，写下去的是 %+v", got.Targets[0], want.normalize())
	}
}

func TestDefaultsAreFilledInOnce(t *testing.T) {
	// 归一化只发生一次（读的那一刻），于是「界面上显示的值」与「实际用的值」不会
	// 有两份真相——两者不一致的症状是「我看着是 8899，它连的却是别的」。
	sandbox(t)
	if err := Save([]Target{{ID: "a", Host: "h"}}); err != nil {
		t.Fatal(err)
	}
	got := Load().Targets[0]
	if got.Port != DefaultSSHPort {
		t.Errorf("ssh 端口 = %d，想要 %d", got.Port, DefaultSSHPort)
	}
	if got.RemoteHost != DefaultRemoteHost || got.RemotePort != DefaultRemotePort {
		t.Errorf("远端 = %s:%d，想要 %s:%d", got.RemoteHost, got.RemotePort, DefaultRemoteHost, DefaultRemotePort)
	}
	if got.IdleSeconds != DefaultIdleSecs {
		t.Errorf("空闲 = %d，想要 %d", got.IdleSeconds, DefaultIdleSecs)
	}
	if got.HostKey != HostKeyStrict {
		t.Errorf("host_key = %q，默认必须是 strict", got.HostKey)
	}
	if got.Label != "a" {
		t.Errorf("label = %q，缺省该用 id", got.Label)
	}
	if got.Persistent {
		t.Errorf("默认必须是懒连接（常驻是显式选择）")
	}
}

func TestABrokenEntryIsSkippedButReported(t *testing.T) {
	// 坏掉的那一条**跳过，但不咽下去**：它唯一的出口就是界面上那张卡与
	// `newgate tunnel ls`。只写日志等于只对会翻日志的人可见。
	sandbox(t)
	raw := `{"targets":[
		{"id":"good","host":"h"},
		{"id":"","host":"h"},
		{"id":"nohost"},
		{"id":"bad/id","host":"h"},
		{"id":"good","host":"h2"}
	]}`
	writeRaw(t, raw)

	got := Load()
	if len(got.Targets) != 1 || got.Targets[0].ID != "good" {
		t.Fatalf("该只剩一条 good，实际 %+v", got.Targets)
	}
	if len(got.Rejected) != 4 {
		t.Fatalf("该报出 4 条坏配置，实际 %d：%+v", len(got.Rejected), got.Rejected)
	}
	// 每条都要说得出**为什么**：一句「无效配置」等于没说。
	for _, bad := range got.Rejected {
		if bad.Why == "" || bad.Line == "" {
			t.Errorf("被拒的那条没有原因：%+v", bad)
		}
	}
	joined := ""
	for _, bad := range got.Rejected {
		joined += bad.Why + "\n"
	}
	for _, want := range []string{"id is required", "host is required", "path segment", "share this id"} {
		if !strings.Contains(joined, want) {
			t.Errorf("没有报出 %q，实际：\n%s", want, joined)
		}
	}
}

func TestTwoTargetsCannotShareALocalPort(t *testing.T) {
	// 两个 target 抢同一个本地端口：后起的那个 `net.Listen` 会失败，而失败的样子
	// 是「这条 tunnel 没起来」——用户不会想到是**另一条**占了端口。
	sandbox(t)
	writeRaw(t, `{"targets":[
		{"id":"a","host":"h","local_port":9401},
		{"id":"b","host":"h","local_port":9401}
	]}`)
	got := Load()
	if len(got.Targets) != 1 || got.Targets[0].ID != "a" {
		t.Fatalf("该留下先声明的 a，实际 %+v", got.Targets)
	}
	if len(got.Rejected) != 1 || !strings.Contains(got.Rejected[0].Why, "9401") {
		t.Fatalf("该报出端口冲突并说出是哪个端口：%+v", got.Rejected)
	}
}

func TestGarbageInTheSectionIsNotFatal(t *testing.T) {
	// fail-open：读不出来得到的是空表而不是错误。一个偏好读不出来不该让整个界面
	// 打不开（与 web-dashboard 读皮肤选择同一条）。
	sandbox(t)
	// 我们那一格是合法 JSON、但**不是我们认的形状**（一个对象而不是一个数组）。
	// 真的把整个文件写坏是另一回事——那时 store.LoadState 自己 fail-open 成空
	// 状态，我们连这一格都读不到（也就无从报起）。
	writeRaw(t, `{"targets":{"nope":1}}`)
	got := Load()
	if len(got.Targets) != 0 {
		t.Errorf("坏 JSON 该得到空表，实际 %+v", got.Targets)
	}
	if len(got.Rejected) != 1 {
		t.Errorf("坏 JSON 该被报出来（不然它是静默的），实际 %+v", got.Rejected)
	}
}

func TestTargetsComeBackInAStableOrder(t *testing.T) {
	// 顺序稳定，界面上的行才不会在两次刷新之间跳来跳去（用户刚想点的那一行
	// 换了个位置——那是「点击后重排序」那一类让人不敢用的行为）。
	sandbox(t)
	if err := Save([]Target{{ID: "zeta", Host: "h"}, {ID: "alpha", Host: "h"}, {ID: "mid", Host: "h"}}); err != nil {
		t.Fatal(err)
	}
	got := Load().Targets
	if len(got) != 3 || got[0].ID != "alpha" || got[1].ID != "mid" || got[2].ID != "zeta" {
		t.Errorf("顺序不对：%+v", got)
	}
}

func TestMissingConfigIsSimplyEmpty(t *testing.T) {
	// 全新安装：连那一格都没有。这不是错误，是一条也没有。
	sandbox(t)
	got := Load()
	if len(got.Targets) != 0 || len(got.Rejected) != 0 {
		t.Errorf("全新安装该是干干净净的空表，实际 %+v / %+v", got.Targets, got.Rejected)
	}
}

func TestSaveRefusesToClobberSomeoneElsesWrite(t *testing.T) {
	// 命令行与 web 界面可能同时改这份文件（一个人在终端敲 `tunnel add`，另一个人
	// 在浏览器里拖字段）。不判断基线的话后写的那次会**静默**盖掉前一次——而两次
	// 改动可能毫不相干。
	sandbox(t)
	if err := Save([]Target{{ID: "a", Host: "h"}}); err != nil {
		t.Fatal(err)
	}
	stale := Load().Base

	// 别人改了一次。
	if err := Save([]Target{{ID: "a", Host: "h"}, {ID: "b", Host: "h"}}); err != nil {
		t.Fatal(err)
	}

	// 我拿着旧基线写。
	_, err := save([]Target{{ID: "c", Host: "h"}}, stale)
	if err == nil {
		t.Fatal("拿着过期基线写该失败")
	}
	var se *store.StaleError
	if !errorAs(err, &se) {
		t.Fatalf("该是 StaleError，实际 %T: %v", err, err)
	}
	// 关键：盘上那份没被覆盖。
	if got := Load(); len(got.Targets) != 2 {
		t.Errorf("冲突时盘上那份被覆盖了：%+v", got.Targets)
	}
}

func TestTheIDIsAURLSegment(t *testing.T) {
	// id 直接进 URL。带 `/` 的话 `/ui/remote/a/b/` 指向谁就不确定了；带 `?#`
	// 的话会被浏览器的 URL 解析吃掉。
	for _, id := range []string{"a/b", "a?b", "a#b", "a b", "a\tb", ""} {
		if why := validate(Target{ID: id, Host: "h"}.normalize()); why == "" {
			t.Errorf("id %q 该被拒", id)
		}
	}
	for _, id := range []string{"snode1", "a-b", "a_b", "a.b", "work-2"} {
		if why := validate(Target{ID: id, Host: "h"}.normalize()); why != "" {
			t.Errorf("id %q 该被接受，实际：%s", id, why)
		}
	}
}

// ---------- 界面写回来的那份 ----------

func TestApplyFromTheInterfaceReplacesTheWholeSet(t *testing.T) {
	// 契约与 config 的 applyProviders 同一份：界面交回来的是**全部记录**，没交的
	// 就是删了。所以「删一条」不需要第二个接口。
	sandbox(t)
	if err := Save([]Target{{ID: "old", Host: "h"}}); err != nil {
		t.Fatal(err)
	}
	base := Load().Base

	edit := `{"items":[
		{"id":"old","values":{"id":"old","label":"renamed","host":"10.0.0.1","user":"u",
		 "port":"2222","remote_port":"8899","local_port":"9401","persistent":"persistent",
		 "idle_seconds":"60","host_key":"accept-new"}},
		{"id":"","values":{"id":"new","host":"10.0.0.2"}}
	],"removed":[{"id":"gone"}]}`

	if _, err := applyTargets(json.RawMessage(edit), base); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if len(got.Targets) != 2 {
		t.Fatalf("该有两条，实际 %+v（被拒：%+v）", got.Targets, got.Rejected)
	}
	byID := map[string]Target{}
	for _, t := range got.Targets {
		byID[t.ID] = t
	}
	old := byID["old"]
	if old.Label != "renamed" || old.Port != 2222 || !old.Persistent || old.HostKey != HostKeyAcceptNew {
		t.Errorf("改过的那条没落对：%+v", old)
	}
	if old.User != "u" || old.LocalPort != 9401 || old.IdleSeconds != 60 {
		t.Errorf("改过的那条有几个字段丢了：%+v", old)
	}
	fresh := byID["new"]
	if fresh.Host != "10.0.0.2" || fresh.IdleSeconds != DefaultIdleSecs {
		t.Errorf("新加的那条没落对：%+v", fresh)
	}
}

func TestApplyRefusesABadRowWhileTheUserIsLookingAtIt(t *testing.T) {
	// 在**这一刻**就拒，不要存下去等到下次读的时候再跳过：用户刚敲完的那一格就是
	// 他正看着的东西，此刻说「id 里不能有斜杠」他立刻知道改哪儿。
	sandbox(t)
	base := Load().Base
	edit := `{"items":[{"id":"","values":{"id":"a/b","host":"h"}}]}`
	if _, err := applyTargets(json.RawMessage(edit), base); err == nil {
		t.Fatal("坏 id 该被拒")
	} else if !strings.Contains(err.Error(), "path segment") {
		t.Errorf("错误该说清是 id 的问题：%v", err)
	}
	if got := Load(); len(got.Targets) != 0 {
		t.Errorf("被拒之后不该落盘：%+v", got.Targets)
	}
}

func TestApplyRefusesDuplicateIDsUpFront(t *testing.T) {
	sandbox(t)
	base := Load().Base
	edit := `{"items":[
		{"id":"","values":{"id":"dup","host":"h"}},
		{"id":"","values":{"id":"dup","host":"h2"}}
	]}`
	if _, err := applyTargets(json.RawMessage(edit), base); err == nil {
		t.Fatal("重名该被拒")
	}
}

// ---------- 工具 ----------

func writeRaw(t *testing.T, body string) {
	t.Helper()
	st := store.LoadState()
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[StateKey] = []byte(body)
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
}

// errorAs 是 errors.As 的一层薄包装，只为让上面那几处读起来短一点。
func errorAs(err error, target **store.StaleError) bool {
	for err != nil {
		if se, ok := err.(*store.StaleError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
