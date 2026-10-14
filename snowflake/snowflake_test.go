package snowflake

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestGenerate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "data", "sf")
	if err := Init(1, file); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("epoch file not written: %v", err)
	}

	seen := map[int64]bool{}
	var last int64
	for i := 0; i < 10000; i++ {
		id := GenInt64()
		if id <= last || seen[id] {
			t.Fatalf("id not increasing or duplicated: %d after %d", id, last)
		}
		seen[id], last = true, id
	}
	if _, err = strconv.ParseInt(GenID(), 10, 64); err != nil {
		t.Fatalf("GenID: %v", err)
	}

	// 重启(真实重启远超 1ms，同一毫秒内重新 Init 序号会归零)：沿用文件中的基点，ID 继续递增
	time.Sleep(5 * time.Millisecond)
	if err = Init(1, file); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(file); string(again) != string(body) {
		t.Fatalf("epoch changed after restart: %s -> %s", body, again)
	}
	if id := GenInt64(); id <= last {
		t.Fatalf("id after restart %d <= %d", id, last)
	}
}
