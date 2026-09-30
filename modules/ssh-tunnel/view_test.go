package sshtunnel

import (
	"encoding/json"
	"testing"

	viewapi "github.com/rzbdz/newgate/lib/view"
)

// 这一族锁的是**这张卡在界面上能不能用**。全部是「到 wire 上」那类判据：同一个
// 值在 Go 这边对、在界面上丢了一格，用户看到的东西就完全不是一回事，而编译、测试、
// review 一个都不会红。

// TestABlankTargetHasEveryField：一份**一条都没有**的配置，点「新增」要能填。
//
// 这是这个模块的常态（一台还没配过远端的机器），而它过去是坏的：字段的形状长在
// 每一条记录上，一条都没有的时候界面无处可抄，于是新增出来的是一张**一个输入框
// 都没有**的空卡片。这一条防的就是那个回归——它只在一个空集上显形。
func TestABlankTargetHasEveryField(t *testing.T) {
	c := targetsConcept(Loaded{})
	recs, ok := c.Data.(viewapi.Records)
	if !ok {
		t.Fatalf("Data 不是 Records：%T", c.Data)
	}
	if !recs.CanAdd {
		t.Fatal("这张卡该能加")
	}
	if len(recs.Blank) == 0 {
		t.Fatal("空集上 Blank 是空的——点「新增」会得到一张没有输入框的卡片")
	}

	// 字段表必须与一条真记录的**逐字相同**。少一格的那种错看起来只像「这个字段
	// 本来就没法配」，而它其实只是没被带出来。
	full := targetFields(Target{}.normalize())
	if len(recs.Blank) != len(full) {
		t.Fatalf("Blank 有 %d 格，一条真记录有 %d 格", len(recs.Blank), len(full))
	}
	for i := range full {
		if recs.Blank[i].ID != full[i].ID {
			t.Errorf("第 %d 格是 %q，想要 %q", i, recs.Blank[i].ID, full[i].ID)
		}
	}

	// 下拉的候选必须带上：没有 Options 的 select 是一个**空的**下拉，而用户对着
	// 它只会以为界面坏了。
	for _, f := range recs.Blank {
		if f.Kind != viewapi.FieldSelect {
			continue
		}
		if len(f.Options) == 0 {
			t.Errorf("下拉 %q 没有候选", f.ID)
		}
	}
}

// TestEveryBlankFieldSurvivesJSON：上面那条是在 Go 的值上比，这条比的是**真过一遍
// JSON** 之后的样子——界面拿到的是 JSON，中间少一格这里才看得出来。
func TestEveryBlankFieldSurvivesJSON(t *testing.T) {
	recs := targetsConcept(Loaded{}).Data.(viewapi.Records)
	raw, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Blank []struct {
			ID      string   `json:"id"`
			Kind    string   `json:"kind"`
			Options []string `json:"options"`
		} `json:"blank"`
		CanAdd bool `json:"can_add"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("解不出来：%v", err)
	}
	if !back.CanAdd || len(back.Blank) != len(recs.Blank) {
		t.Fatalf("过了 JSON 之后 can_add=%v，blank=%d 格（Go 那边 %d 格）",
			back.CanAdd, len(back.Blank), len(recs.Blank))
	}
	seen := map[string]bool{}
	for _, f := range back.Blank {
		seen[f.ID] = true
	}
	for _, want := range []string{"id", "host", "user", "port", "identity",
		"remote_host", "remote_port", "local_port", "persistent", "idle_seconds", "host_key"} {
		if !seen[want] {
			t.Errorf("界面上少了一格 %q", want)
		}
	}
}

// TestTheAddressesAreClickable：两条路的地址都该是**能点的链接**，而端口没起来的
// 那条不该有。
//
// 给一个点开就是连不上的地址，比不给链接更糟：用户会以为自己配错了。
func TestTheAddressesAreClickable(t *testing.T) {
	loaded := Loaded{Targets: []Target{target("ds")}}
	m := newManager(newPool(newFakeDialer("127.0.0.1:1")), func() Loaded { return loaded })
	defer m.shutdown()

	c := statusConcept(m)
	tbl, ok := c.Data.(viewapi.Table)
	if !ok {
		t.Fatalf("Data 不是 Table：%T", c.Data)
	}
	if len(tbl.Rows) != 1 {
		t.Fatalf("该有一行，实际 %d", len(tbl.Rows))
	}
	route := tbl.Rows[0].Cells["route"]
	if route.Href != "/ui/remote/ds/" {
		t.Errorf("route 那一格不是链接：%+v", route)
	}
	if route.Text != route.Href {
		t.Errorf("链接的字与地址不一致：%+v", route)
	}
	// 这个 target 没有 local_port，所以那一格是「没有」——**不该有链接**。
	if port := tbl.Rows[0].Cells["port"]; port.Href != "" {
		t.Errorf("没有转发端口却有链接：%+v", port)
	}
}

func TestAForwardingPortLinksToItsOwnRoot(t *testing.T) {
	// 转发端口的地址是它自己的根（`http://127.0.0.1:9401/ui/`），不是共享端口上
	// 那条 route——两者是不同的地址，点错一个会让人以为「保真那条坏了」。
	got := forwardHref(TargetStatus{
		Target:  target("ds"),
		Forward: forwardStatus{Addr: "127.0.0.1:9401"},
	})
	if got != "http://127.0.0.1:9401/ui/" {
		t.Errorf("forwardHref = %q", got)
	}
	// 端口没起来：不给链接（点开就是连不上，比没有链接更让人以为配错了）。
	if got := forwardHref(TargetStatus{ForwardErr: "address already in use"}); got != "" {
		t.Errorf("端口没起来却给了链接：%q", got)
	}
}
