// Package database 数据库连接(gorm)：MySQL / PostgreSQL / SQLite，数据库不存在时自动创建，连接池
//
//	db, err := database.Open(cfg)
//	defer database.Close(db)
//
// SQL 日志：Debug 为 true 时用 gorm 自带的日志把 SQL 打印到控制台，否则不打印；不写入日志文件
package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pkg/errors"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// 数据库类型
const (
	TypeMySQL    = "mysql"
	TypePostgres = "postgres"
	TypeSQLite   = "sqlite"
)

// Config 数据库配置
type Config struct {
	// Type 数据库类型(必填)：mysql | postgres | sqlite
	Type string `json:"type" yaml:"type" mapstructure:"type"`
	// Name 数据库名；sqlite 为数据库文件路径
	Name string `json:"name" yaml:"name" mapstructure:"name"`
	// Address 地址 host:port
	Address string `json:"address" yaml:"address" mapstructure:"address"`
	// Username 用户名
	Username string `json:"username" yaml:"username" mapstructure:"username"`
	// Password 密码
	Password string `json:"-" yaml:"password" mapstructure:"password"`
	// Params 追加到连接串的参数：mysql 为 key=value&key=value，postgres 为 key=value key=value
	Params string `json:"params" yaml:"params" mapstructure:"params"`
	// AutoCreate 数据库不存在时自动创建(mysql、postgres)
	AutoCreate bool `json:"autoCreate" yaml:"autoCreate" mapstructure:"autoCreate"`
	// MaxIdleConns 最大空闲连接数(必须配置)，0 表示不保留空闲连接，每次查询都新建连接
	MaxIdleConns int `json:"maxIdleConns" yaml:"maxIdleConns" mapstructure:"maxIdleConns"`
	// MaxOpenConns 最大连接数(必须配置)，0 表示不限制
	MaxOpenConns int `json:"maxOpenConns" yaml:"maxOpenConns" mapstructure:"maxOpenConns"`
	// MaxLifetime 连接最长存活时间(分钟，必须配置)，0 表示永不过期
	MaxLifetime int `json:"maxLifetime" yaml:"maxLifetime" mapstructure:"maxLifetime"`
	// Debug 把执行的 SQL 打印到控制台(含耗时、行数，超过 200ms 标为慢 SQL)，开发时使用；为 false 时不打印
	Debug bool `json:"debug" yaml:"debug" mapstructure:"debug"`
	// StringSize mysql 中未指定长度的字符串字段的默认长度(varchar(n))，0 时为 255。
	// 已有数据库改用本组件时应与原来的值保持一致，否则启动迁移时会修改这些列的长度
	StringSize uint `json:"stringSize" yaml:"stringSize" mapstructure:"stringSize"`
}

// Option 修改 gorm 配置(在默认配置之后执行)，如命名策略、替换 Logger
type Option func(*gorm.Config)

// PrintlnSql SQL 日志：debug 为 true 时打印到控制台，否则不打印
func PrintlnSql(debug bool) glog.Interface {
	if debug {
		return glog.New(log.New(os.Stdout, "\r\n", log.LstdFlags), glog.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  glog.Info,
			IgnoreRecordNotFoundError: false,
			Colorful:                  true,
		})
	}
	return glog.Default.LogMode(glog.Silent)
}

// Open 打开数据库并 Ping 测试连接。
// 默认 gorm 配置：表名单数、迁移时不建外键、不为单条写操作开启默认事务、翻译数据库错误(如 gorm.ErrDuplicatedKey)
func Open(cfg Config, opts ...Option) (*gorm.DB, error) {
	dial, err := _Dial(cfg)
	if err != nil {
		return nil, err
	}
	gc := &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		SkipDefaultTransaction:                   true,
		TranslateError:                           true,
		NamingStrategy:                           schema.NamingStrategy{SingularTable: true},
		Logger:                                   PrintlnSql(cfg.Debug),
	}
	for _, fn := range opts {
		fn(gc)
	}
	db, err := gorm.Open(dial, gc)
	if err != nil {
		return nil, errors.Wrapf(err, "database: 打开数据库 %s(%s %s)失败", cfg.Name, cfg.Type, cfg.Address)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, errors.Wrap(err, "database: 获取连接池失败")
	}
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.MaxLifetime) * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, errors.Wrapf(err, "database: 连接数据库 %s(%s %s)失败", cfg.Name, cfg.Type, cfg.Address)
	}
	return db, nil
}

func _Dial(cfg Config) (gorm.Dialector, error) {
	switch strings.ToLower(cfg.Type) {
	case TypeMySQL:
		if cfg.AutoCreate {
			if err := createMySQL(cfg); err != nil {
				return nil, err
			}
		}
		dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=3s",
			cfg.Username, cfg.Password, cfg.Address, cfg.Name)
		if cfg.Params != "" {
			dsn += "&" + cfg.Params
		}
		size := cfg.StringSize
		if size == 0 {
			size = 255
		}
		return mysql.New(mysql.Config{
			DSN:                      dsn,
			DefaultStringSize:        size,
			DisableDatetimePrecision: true,
			DontSupportRenameIndex:   true,
			DontSupportRenameColumn:  true,
		}), nil
	case TypePostgres:
		host, port, err := net.SplitHostPort(cfg.Address)
		if err != nil {
			return nil, errors.Wrapf(err, "database: 地址 %s 格式不正确，应为 host:port", cfg.Address)
		}
		if cfg.AutoCreate {
			if err = createPostgres(cfg, host, port); err != nil {
				return nil, err
			}
		}
		dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable TimeZone=%s",
			host, port, cfg.Username, cfg.Password, cfg.Name, localTimeZone())
		if cfg.Params != "" {
			dsn += " " + cfg.Params
		}
		return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), nil
	case TypeSQLite:
		if cfg.Name == "" {
			return nil, errors.New("database: sqlite 需要配置 name(数据库文件路径)")
		}
		if dir := filepath.Dir(cfg.Name); dir != "." && !strings.HasPrefix(cfg.Name, "file:") {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, errors.Wrapf(err, "database: 创建 sqlite 数据库目录 %s 失败", dir)
			}
		}
		return sqlite.Open(cfg.Name), nil
	default:
		return nil, errors.Errorf("database: 不支持的数据库类型 %s，可选 mysql | postgres | sqlite", cfg.Type)
	}
}

func createMySQL(cfg Config) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/?charset=utf8mb4&timeout=3s", cfg.Username, cfg.Password, cfg.Address)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return errors.Wrapf(err, "database: 连接 MySQL %s 失败", cfg.Address)
	}
	defer db.Close()
	_, err = db.Exec(fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` DEFAULT CHARACTER SET utf8mb4 DEFAULT COLLATE utf8mb4_general_ci", cfg.Name))
	return errors.Wrapf(err, "database: 创建 MySQL 数据库 %s 失败", cfg.Name)
}

// createPostgres CREATE DATABASE 不能在事务中执行，也没有 IF NOT EXISTS，先查询再创建
func createPostgres(cfg Config, host, port string) error {
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=postgres sslmode=disable",
		host, port, cfg.Username, cfg.Password)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return errors.Wrapf(err, "database: 连接 PostgreSQL %s:%s 失败", host, port)
	}
	defer db.Close()
	var exists bool
	if err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", cfg.Name).Scan(&exists); err != nil {
		return errors.Wrapf(err, "database: 查询 PostgreSQL 数据库 %s 是否存在失败", cfg.Name)
	}
	if exists {
		return nil
	}
	_, err = db.Exec(fmt.Sprintf(`CREATE DATABASE "%s" WITH ENCODING 'UTF8'`, cfg.Name))
	return errors.Wrapf(err, "database: 创建 PostgreSQL 数据库 %s 失败", cfg.Name)
}

func localTimeZone() string {
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	return "Asia/Shanghai"
}

// Close 关闭连接池
func Close(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return errors.Wrap(err, "database: 获取连接池失败")
	}
	return errors.Wrap(sqlDB.Close(), "database: 关闭连接池失败")
}

// Ping 健康检查
func Ping(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return errors.Wrap(err, "database: 获取连接池失败")
	}
	return errors.Wrap(sqlDB.PingContext(ctx), "database: Ping 失败")
}
