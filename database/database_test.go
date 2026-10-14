package database_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awp0816/common/database"

	"github.com/pkg/errors"
	"gorm.io/gorm"
)

type User struct {
	ID   int64
	Name string `gorm:"uniqueIndex;size:64"`
}

// captureStdout 捕获 fn 执行期间打印到控制台的内容(gorm 日志在 Open 时绑定 os.Stdout，所以 Open 也要放在 fn 中)
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = old
	_ = w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func open(t *testing.T, debug bool) *gorm.DB {
	t.Helper()
	db, err := database.Open(database.Config{Type: database.TypeSQLite, Name: filepath.Join(t.TempDir(), "data", "t.db"), Debug: debug})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err = db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOpenSQLite(t *testing.T) {
	db := open(t, false)
	ctx := context.Background()
	if err := database.Ping(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&User{ID: 1, Name: "a"}).Error; err != nil {
		t.Fatal(err)
	}
	// 表名单数
	var n int64
	if err := db.Table("user").Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("count: %d %v", n, err)
	}
	// TranslateError：唯一键冲突返回 gorm.ErrDuplicatedKey
	if err := db.WithContext(ctx).Create(&User{ID: 2, Name: "a"}).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("want ErrDuplicatedKey, got %v", err)
	}
}

// debug 为 true 时 SQL 打印到控制台，为 false 时不打印
func TestDebug(t *testing.T) {
	out := captureStdout(t, func() {
		db := open(t, true)
		db.Exec("SELECT 42")
	})
	if !strings.Contains(out, "SELECT 42") {
		t.Fatalf("debug should print SQL, got %q", out)
	}

	out = captureStdout(t, func() {
		db := open(t, false)
		db.Exec("SELECT 42")
		db.Exec("SELECT * FROM not_exists")
	})
	if out != "" {
		t.Fatalf("should print nothing, got %q", out)
	}
}

func TestWithGorm(t *testing.T) {
	db, err := database.Open(database.Config{Type: database.TypeSQLite, Name: filepath.Join(t.TempDir(), "t.db")},
		func(c *gorm.Config) { c.DryRun = true })
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(db)
	if !db.DryRun {
		t.Fatal("option should be applied")
	}
}

func TestExecScript(t *testing.T) {
	db := open(t, false)
	script := "-- 初始化\r\nINSERT INTO user (id, name)\nVALUES (1, 'a');\n\nINSERT INTO user (id, name) VALUES (2, 'b');\n"
	if n, err := database.ExecScript(db, script); err != nil || n != 2 {
		t.Fatalf("exec: %d %v", n, err)
	}
	// 失败整体回滚
	if _, err := database.ExecScript(db, "INSERT INTO user (id, name) VALUES (3, 'c');\nINSERT INTO user (id, name) VALUES (4, 'a');"); err == nil {
		t.Fatal("duplicate should fail")
	}
	var n int64
	db.Model(&User{}).Count(&n)
	if n != 2 {
		t.Fatalf("should rollback, count=%d", n)
	}
}

func TestSplitSQL(t *testing.T) {
	got := database.SplitSQL("-- c\nA;\n  B\n  b2;\nC")
	if len(got) != 3 || got[0] != "A" || got[1] != "B\n  b2" || got[2] != "C" {
		t.Fatalf("%q", got)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := database.Open(database.Config{Type: "oracle"}); err == nil {
		t.Fatal("unsupported type should fail")
	}
	if _, err := database.Open(database.Config{Type: database.TypeSQLite}); err == nil {
		t.Fatal("sqlite without name should fail")
	}
}
