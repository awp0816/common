package logger

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s：%v", path, err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func TestSplitJSON(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Config{Level: "debug", Path: dir, Filename: "svc", JSON: true,
		SplitLevel: true, Daily: true, Caller: true, Stacktrace: true}); err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestID(context.Background(), "req-1")
	Debug(ctx).Msg("debug message")
	Info(ctx).Msg("info message")
	Warn(ctx).Msg("warn message")
	Error(ctx).Msg("error message")
	Info(context.Background()).Msg("no request id")
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	day := filepath.Join(dir, time.Now().Format(time.DateOnly))
	for _, l := range []string{"debug", "info", "warn", "error"} {
		lines := readLines(t, filepath.Join(day, "svc."+l+".log"))
		if want := map[bool]int{true: 2, false: 1}[l == "info"]; len(lines) != want {
			t.Fatalf("%s 文件应有 %d 行：%q", l, want, lines)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
			t.Fatalf("%s 不是 JSON：%s", l, lines[0])
		}
		if m[FieldLevel] != l || m[FieldMessage] != l+" message" || m[FieldRequestID] != "req-1" {
			t.Fatalf("%s 字段不对：%v", l, m)
		}
		if ts, _ := m[FieldTime].(string); len(ts) != len(TimeFormat) {
			t.Fatalf("时间格式不对：%v", m[FieldTime])
		}
		if c, _ := m[FieldCaller].(string); !strings.HasPrefix(c, "logger/logger_test.go:") {
			t.Fatalf("caller 不对：%v", m[FieldCaller])
		}
		st, ok := m[FieldStacktrace].(string)
		if ok != (l == "error") {
			t.Fatalf("%s 调用栈：期望 %v", l, l == "error")
		}
		// 调用栈从测试函数开始，不含 zerolog 与本包内部的帧
		if ok && (!strings.HasPrefix(st, "github.com/awp0816/common/logger.TestSplitJSON\n") || strings.Contains(st, "zerolog")) {
			t.Fatalf("调用栈不对：\n%s", st)
		}
	}
	if _, err := os.Stat(filepath.Join(day, "svc.fatal.log")); !os.IsNotExist(err) {
		t.Fatal("没有写入的级别不应生成文件")
	}
}

func TestSingleText(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Config{Level: "warn", Path: dir}); err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestID(context.Background(), "req-2")
	Info(ctx).Msg("info text")
	Warn(ctx).Msg("warn text")
	Error(ctx).Msg("error text")
	_ = Close()
	lines := readLines(t, filepath.Join(dir, "app.log"))
	if len(lines) != 2 || !strings.Contains(lines[0], "WRN warn text request_id=req-2") || !strings.Contains(lines[1], "ERR error text") {
		t.Fatalf("应只有 warn、error 两行文本：%q", lines)
	}
	if strings.Contains(lines[0], "\x1b[") || strings.Contains(lines[1], FieldStacktrace) {
		t.Fatalf("文件中不应有颜色，未开启时不应有调用栈：%q", lines)
	}
}

func TestConsoleOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := Init(Config{Path: dir, DisableFile: true}); err != nil {
		t.Fatal(err)
	}
	Info(context.Background()).Msg("nothing")
	_ = Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("DisableFile 时不应创建日志目录")
	}
}

func TestRequestID(t *testing.T) {
	if RequestID(WithRequestID(context.Background(), "abc")) != "abc" || RequestID(context.Background()) != "" {
		t.Fatal("RequestID")
	}
	gctx := context.WithValue(context.Background(), FieldRequestID, "from-gin") //nolint:staticcheck // 模拟 gin.Context 的字符串键
	if RequestID(gctx) != "from-gin" || len(NewRequestID()) != 32 {
		t.Fatal("字符串键 request_id / NewRequestID")
	}
}

func TestCleanupOldDirs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	keep := []string{now.Format(time.DateOnly), now.AddDate(0, 0, -3).Format(time.DateOnly), "other"}
	drop := []string{now.AddDate(0, 0, -4).Format(time.DateOnly), now.AddDate(0, -2, 0).Format(time.DateOnly)}
	for _, d := range append(append([]string{}, keep...), drop...) {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	cleanupOldDirs(Config{Path: dir, MaxAge: 3})
	for _, d := range keep {
		if _, err := os.Stat(filepath.Join(dir, d)); err != nil {
			t.Fatalf("%s 不应被删除", d)
		}
	}
	for _, d := range drop {
		if _, err := os.Stat(filepath.Join(dir, d)); !os.IsNotExist(err) {
			t.Fatalf("%s 应被删除", d)
		}
	}
}
