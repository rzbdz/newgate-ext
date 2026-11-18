package domain

import (
	"sync"
	"testing"
)

// TestExtraRolesSurvivesConcurrentRefresh 动态角色键的读写必须并发安全。
//
// 这不是一条假想的并发：**生产里就是两个协程**。写的是 daemon 的配置
// watcher（每秒一次 store.Reload → store.Load → roleprov.Refresh →
// SetExtraRoles），读的是请求协程（resolve.BuildChain → CandidatesFor →
// DefaultBindingFor → ExtraRoleOf），凡是请求里带了 omo 槽位键的每一发都走
// 那条读路径。
//
// 2026-09-18 之前 extraRoles 是一句裸赋值，这里会命中 DATA RACE。后果不只是
// 「读到旧值」：slice header 是三个机器字，撕裂的头让 range 越界遍历，轻则把
// 垃圾判成已知角色键解析出错误绑定，重则 panic 在请求协程里。
//
// 所以这条测试**必须在 -race 下跑**（`make test-race`）；不带 -race 时它依然
// 有用——它锁住的是「换页之后旧切片不许再被改写」这条无锁读的前提。
func TestExtraRolesSurvivesConcurrentRefresh(t *testing.T) {
	defer SetExtraRoles(nil)

	// 每轮换一份**新的**切片（不是复用同一个）：无锁读的前提是旧切片换页后
	// 永不改写，如果哪天实现改成原地改切片内容，这条测试会跟着 -race 一起红。
	batch := func(i int) []ExtraRole {
		return []ExtraRole{
			{Key: "omo-sisyphus", Default: "@normal", Source: "omo"},
			{Key: "omo-librarian", Default: "@light", Source: "omo"},
			{Key: "omo-cat-deep" + string(rune('a'+i%26)), Source: "omo"},
		}
	}

	var wg sync.WaitGroup
	const rounds = 2000

	wg.Add(1) // 写者：watcher 协程
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			SetExtraRoles(batch(i))
		}
	}()

	wg.Add(2) // 读者：请求协程
	for r := 0; r < 2; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				// 这三条就是热路径上的读法。
				_, _ = ExtraRoleOf("omo-sisyphus")
				_, _ = DefaultBindingFor("omo-librarian")
				_ = IsKnownRole("normal")
				_ = ExtraRoles()
			}
		}()
	}
	wg.Wait()

	// 换页之后读到的必须是完整的最后一份，不是半份。
	SetExtraRoles(batch(7))
	roles := ExtraRoles()
	if len(roles) != len(builtinAliases)+3 {
		t.Fatalf("最后一份快照应完整可见，实际 %d 条: %v", len(roles), roles)
	}
	if _, ok := ExtraRoleOf("omo-sisyphus"); !ok {
		t.Error("写入之后 omo-sisyphus 应当可见")
	}
}

// TestExtraRolesCallerCannotMutateTheSnapshot 读者拿到的切片是只读快照：
// 改它不许影响下一次读。这条守的是无锁读的另一个前提——没有第三方会写。
func TestExtraRolesCallerCannotMutateTheSnapshot(t *testing.T) {
	defer SetExtraRoles(nil)

	source := []ExtraRole{{Key: "omo-sisyphus", Default: "@normal", Source: "omo"}}
	SetExtraRoles(source)

	// 写入方之后复用同一个切片（roleprov.Refresh 每轮重新构建，但这是契约）
	source[0].Key = "改坏了"

	got, ok := ExtraRoleOf("omo-sisyphus")
	if !ok {
		t.Fatal("SetExtraRoles 应当把切片拷走，写入方之后改它不该影响已存的快照")
	}
	if got.Key != "omo-sisyphus" {
		t.Errorf("快照被写入方改动了: %q", got.Key)
	}

	// 读者改自己拿到的那份，也不该影响下一次读。
	all := ExtraRoles()
	for i := range all {
		all[i].Key = "改坏了"
	}
	if _, ok := ExtraRoleOf("omo-sisyphus"); !ok {
		t.Error("ExtraRoles 应当返回副本，读者改它不该影响快照")
	}
}
