package database

import (
	"strings"

	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// ExecScript 在一个事务内依次执行 SQL 脚本中的语句(如初始化数据)，任一失败则全部回滚；返回执行的语句数。
// 脚本按行尾分号切分语句，忽略空行与 -- 开头的注释行(见 SplitSQL)
func ExecScript(db *gorm.DB, script string) (int, error) {
	stmts := SplitSQL(script)
	err := db.Transaction(func(tx *gorm.DB) error {
		for i, s := range stmts {
			if err := tx.Exec(s).Error; err != nil {
				return errors.Wrapf(err, "database: 执行脚本第 %d 条语句失败\n%s", i+1, s)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(stmts), nil
}

// SplitSQL 按行尾分号切分语句，忽略空行与 -- 开头的注释行；不处理字符串中跨行的分号
func SplitSQL(script string) []string {
	var (
		out []string
		cur strings.Builder
	)
	for _, line := range strings.Split(strings.ReplaceAll(script, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if strings.HasSuffix(t, ";") {
			if s := strings.TrimSuffix(strings.TrimSpace(cur.String()), ";"); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}
