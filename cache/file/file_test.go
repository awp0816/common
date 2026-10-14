package file_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/awp0816/common/cache/file"
)

// 通用操作见 cache 包的 TestCache，这里测文件缓存特有的：重启后数据仍在、过期数据清理、文件独占
func TestPersist(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	f, err := file.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Set(ctx, "keep", "v", time.Hour)
	_ = f.Set(ctx, "short", "v", 50*time.Millisecond)
	_ = f.SAdd(ctx, "set", 0, "a", "b")
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	// 重新打开(模拟服务重启)
	f, err = file.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if v, err := f.Get(ctx, "keep"); err != nil || v != "v" {
		t.Fatalf("keep after reopen: %q %v", v, err)
	}
	if ok, _ := f.Exists(ctx, "short"); ok {
		t.Fatal("short should be expired after reopen")
	}
	if m, _ := f.SMembers(ctx, "set"); len(m) != 2 {
		t.Fatalf("set after reopen: %v", m)
	}
	if err = f.DeleteExpired(); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Get(ctx, "keep"); v != "v" {
		t.Fatal("DeleteExpired should keep unexpired data")
	}
}

func TestOpenLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	f, err := file.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = file.Open(path); err == nil {
		t.Fatal("same file should not be opened twice")
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatalf("second Close should be no-op: %v", err)
	}
}
