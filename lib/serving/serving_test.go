package serving

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// 这条边的全部行为就是「什么时候跑」和「坏了怎么办」。两条都要锁死：跑早了等于
// 每条 CLI 命令都开一个监听（这正是它存在的原因），坏了拖垮别人等于一个界面
// 起不来把数据面带走。

func TestRegistrationDoesNotStartAnything(t *testing.T) {
	r := NewRegistry()
	var started atomic.Int64
	if _, err := r.OnServe("web", func() (func(), error) {
		started.Add(1)
		return func() {}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := started.Load(); n != 0 {
		t.Fatalf("登记就跑起来了 %d 次——Start 期间登记，而 Start 每条命令都跑", n)
	}
	stop, err := r.Notify()
	if err != nil {
		t.Fatal(err)
	}
	if n := started.Load(); n != 1 {
		t.Fatalf("通知时该跑一次，实际 %d", n)
	}
	stop()
}

func TestNotifyIsIdempotent(t *testing.T) {
	r := NewRegistry()
	var started atomic.Int64
	if _, err := r.OnServe("web", func() (func(), error) {
		started.Add(1)
		return func() {}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Notify(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Notify(); err != nil {
		t.Fatal(err)
	}
	if n := started.Load(); n != 1 {
		t.Errorf("同一个进程里被通知两次，监听只该起一次（第二次是重复绑定端口）: %d", n)
	}
}

// TestOneFailureDoesNotStopTheOthers：fail-open。一个界面起不来（端口被占、
// 权限不对）不该让整个服务进程进入失败状态——数据面照常转发。
func TestOneFailureDoesNotStopTheOthers(t *testing.T) {
	r := NewRegistry()
	var okRan atomic.Int64
	if _, err := r.OnServe("broken", func() (func(), error) {
		return nil, errors.New("bind: address already in use")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.OnServe("web", func() (func(), error) {
		okRan.Add(1)
		return func() {}, nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Notify()
	if err == nil {
		t.Fatal("失败该报出来（调用方要写日志），只是不许拦住别人")
	}
	if !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("报错要点名是谁、以及为什么: %v", err)
	}
	if okRan.Load() != 1 {
		t.Errorf("前面的失败不该挡住后面的监听: %d", okRan.Load())
	}
}

// TestStopIsReverseOrderAndIdempotent：停机按登记的**逆序**走（后起的先停，
// 与组件图的生命周期同一条规矩），而且重复调用只生效一次（退出路径可能两次走到）。
func TestStopIsReverseOrderAndIdempotent(t *testing.T) {
	r := NewRegistry()
	var order []string
	for _, name := range []string{"first", "second"} {
		n := name
		if _, err := r.OnServe(n, func() (func(), error) {
			return func() { order = append(order, n) }, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	stop, err := r.Notify()
	if err != nil {
		t.Fatal(err)
	}
	stop()
	stop()
	if got := strings.Join(order, ","); got != "second,first" {
		t.Errorf("停机顺序错了（该逆序、且只走一遍）: %q", got)
	}
}

func TestReleaseOnlyRemovesItsOwn(t *testing.T) {
	r := NewRegistry()
	var ran atomic.Int64
	rel, err := r.OnServe("web", func() (func(), error) {
		ran.Add(1)
		return func() {}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rel(); err != nil {
		t.Fatal(err)
	}
	if err := rel(); err != nil {
		t.Fatalf("撤销该是幂等的: %v", err)
	}
	if _, err := r.Notify(); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 0 {
		t.Error("撤销之后不该还被通知")
	}
}

func TestRejectsNamelessOrNil(t *testing.T) {
	r := NewRegistry()
	if _, err := r.OnServe("", func() (func(), error) { return nil, nil }); err == nil {
		t.Error("没有名字的监听该被拒绝（失败时报错要点名是谁）")
	}
	if _, err := r.OnServe("x", nil); err == nil {
		t.Error("nil 回调该被拒绝")
	}
}
