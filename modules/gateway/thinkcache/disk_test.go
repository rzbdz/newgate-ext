package thinkcache

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// 落盘冷层的核心价值：daemon 重启后找回上一进程攒的推理内容。
// 用一个新 DiskStore 打开同一份文件 = 模拟一次重启。
func TestDiskStoreSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thinkcache.bin")

	d1, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d1.appendKeys([]string{"tool:t1", "text:abc"}, []byte("这轮真实的推理"), time.Now())
	if err := d1.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启：新进程重新 open，replay 索引
	d2, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if blob, ok := d2.get("tool:t1"); !ok || string(blob) != "这轮真实的推理" {
		t.Fatalf("重启后没找回：ok=%v blob=%q", ok, blob)
	}
}

func TestOriginSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thinkcache.bin")
	want := Origin{Profile: "ark", Provider: "ark", Model: "ark-code-latest"}

	c1 := New(1<<20, time.Hour)
	d1, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c1.disk = d1
	c1.PutOrigin([]string{"call_restart"}, want)
	if err := d1.Close(); err != nil {
		t.Fatal(err)
	}

	c2 := New(1<<20, time.Hour)
	d2, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	c2.disk = d2
	if got, ok := c2.GetOrigin("call_restart"); !ok || got != want {
		t.Fatalf("重启后 tool origin 丢了: ok=%v got=%+v", ok, got)
	}
}

// 过期项在 replay 时就被跳过，不占索引。
func TestDiskStoreDropsExpiredOnReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thinkcache.bin")

	d1, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d1.appendKeys([]string{"tool:old"}, []byte("过期内容"), time.Now().Add(-2*time.Hour))
	d1.appendKeys([]string{"tool:new"}, []byte("新内容"), time.Now())
	_ = d1.Close()

	d2, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if _, ok := d2.get("tool:old"); ok {
		t.Fatal("过期项 replay 后不该还在")
	}
	if blob, ok := d2.get("tool:new"); !ok || string(blob) != "新内容" {
		t.Fatalf("没到期的项丢了：ok=%v blob=%q", ok, blob)
	}
}

// 超过 maxBytes*2 就重写一遍（滚动）：过期项被丢、活项保留、文件缩回。
func TestDiskStoreRollsOver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thinkcache.bin")

	d, err := openDisk(path, 1024, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now()
	blob := []byte("一段说长不长说短不短的推理内容，用来把文件顶过 2048 字节上限触发重写")
	for i := 0; i < 20; i++ {
		at := now
		if i < 10 {
			at = now.Add(-2 * time.Hour) // 前 10 条过期，后 10 条活
		}
		d.appendKeys([]string{fmt.Sprintf("tool:k%d", i)}, blob, at)
	}
	if d.size > d.max*2 {
		t.Fatalf("compact 后文件还是太大: size=%d max*2=%d", d.size, d.max*2)
	}
	if _, ok := d.get("tool:k0"); ok {
		t.Fatal("过期项 compact 后还在")
	}
	if _, ok := d.get("tool:k19"); !ok {
		t.Fatal("活项被 compact 丢了")
	}
}
