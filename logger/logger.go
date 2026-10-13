// Package logger 基于 zerolog 的日志组件：控制台 + 文件输出(按天分目录、按级别分文件、按大小切割)、链路ID、调用栈。
//
//	if err := logger.Init(cfg); err != nil { ... }
//	defer logger.Close()
//	logger.Info(ctx).Str("user", "alice").Msg("登录成功")   // 自动带上 ctx 中的 request_id
//	logger.Error(ctx).Err(err).Msg("下单失败")
package logger

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// TimeFormat 日志时间格式
const TimeFormat = "2006-01-02 15:04:05.000"

// 字段名
const (
	FieldTime       = "ts"
	FieldLevel      = "level"
	FieldMessage    = "msg"
	FieldCaller     = "caller"
	FieldRequestID  = "request_id"
	FieldError      = "error"
	FieldStacktrace = "stacktrace"
)

// Config 日志配置
type Config struct {
	// Level 最低输出级别：debug、info、warn、error，默认 info
	Level string `json:"level" yaml:"level" mapstructure:"level"`
	// Path 日志目录，默认 logs
	Path string `json:"path" yaml:"path" mapstructure:"path"`
	// Filename 日志文件名前缀，默认 app；按级别分文件时为 app.error.log，否则为 app.log
	Filename string `json:"filename" yaml:"filename" mapstructure:"filename"`
	// JSON 文件是否为 JSON 格式，否则为无颜色的文本；控制台始终为带颜色的文本
	JSON bool `json:"json" yaml:"json" mapstructure:"json"`
	// Console 是否输出到控制台
	Console bool `json:"console" yaml:"console" mapstructure:"console"`
	// DisableFile 不写文件(只输出到控制台)
	DisableFile bool `json:"disableFile" yaml:"disableFile" mapstructure:"disableFile"`
	// SplitLevel 是否按级别分文件(每个级别只写入自己的文件)
	SplitLevel bool `json:"splitLevel" yaml:"splitLevel" mapstructure:"splitLevel"`
	// Daily 是否按天分目录：logs/2026-01-01/app.log
	Daily bool `json:"daily" yaml:"daily" mapstructure:"daily"`
	// MaxSize 单个文件最大大小(MB)，超过后切割，默认 100
	MaxSize int `json:"maxSize" yaml:"maxSize" mapstructure:"maxSize"`
	// MaxAge 保留天数，0 表示不清理；按天分目录时过期的日期目录整体删除
	MaxAge int `json:"maxAge" yaml:"maxAge" mapstructure:"maxAge"`
	// MaxBackups 切割后保留的旧文件个数，0 表示不限制
	MaxBackups int `json:"maxBackups" yaml:"maxBackups" mapstructure:"maxBackups"`
	// Compress 是否压缩切割后的旧文件
	Compress bool `json:"compress" yaml:"compress" mapstructure:"compress"`
	// Caller 是否记录调用位置(目录/文件:行号)
	Caller bool `json:"caller" yaml:"caller" mapstructure:"caller"`
	// Stacktrace error 及以上级别是否附带调用栈
	Stacktrace bool `json:"stacktrace" yaml:"stacktrace" mapstructure:"stacktrace"`
}

func (c Config) withDefaults() Config {
	if c.Level == "" {
		c.Level = "info"
	}
	if c.Path == "" {
		c.Path = "logs"
	}
	if c.Filename == "" {
		c.Filename = "app"
	}
	if c.MaxSize <= 0 {
		c.MaxSize = 100
	}
	return c
}

var (
	// L 全局 logger，Init 之前为空输出
	L = zerolog.Nop()

	mu    sync.Mutex
	files *levelFiles
)

// configure 统一字段名与时间格式(zerolog 的这些设置是全局的)
var configure = sync.OnceFunc(func() {
	zerolog.TimestampFieldName = FieldTime
	zerolog.LevelFieldName = FieldLevel
	zerolog.MessageFieldName = FieldMessage
	zerolog.CallerFieldName = FieldCaller
	zerolog.ErrorFieldName = FieldError
	zerolog.TimeFieldFormat = TimeFormat
	zerolog.CallerMarshalFunc = func(_ uintptr, file string, line int) string {
		return shortCaller(file, line)
	}
})

// Init 按配置创建全局 logger L，启动时调用一次(重复调用会关闭上一次的日志文件)；
// 同时设为 zerolog 的默认 context logger
func Init(cfg Config) error {
	configure()
	cfg = cfg.withDefaults()
	level, err := zerolog.ParseLevel(strings.ToLower(cfg.Level))
	if err != nil {
		return err
	}
	var out []io.Writer
	if cfg.Console {
		out = append(out, consoleWriter(os.Stdout, false))
	}
	var f *levelFiles
	if !cfg.DisableFile {
		if f, err = newLevelFiles(cfg, level); err != nil {
			return err
		}
		out = append(out, f)
	}
	l := zerolog.New(zerolog.MultiLevelWriter(out...)).Level(level).With().Timestamp().Logger()
	if cfg.Stacktrace {
		l = l.Hook(stackHook{})
	}
	if cfg.Caller {
		l = l.With().Caller().Logger()
	}

	mu.Lock()
	old := files
	L, files = l, f
	zerolog.DefaultContextLogger = &L
	mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// Close 关闭日志文件(服务退出时调用)；zerolog 没有缓冲，无需刷新
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	if files == nil {
		return nil
	}
	err := files.Close()
	files = nil
	return err
}

// Ctx 返回带 request_id 的全局 logger(ctx 中没有链路ID时不带)
func Ctx(ctx context.Context) *zerolog.Logger {
	l := L
	if id := RequestID(ctx); id != "" {
		l = l.With().Str(FieldRequestID, id).Logger()
	}
	return &l
}

// Debug 等快捷函数：带 request_id 的对应级别日志事件
func Debug(ctx context.Context) *zerolog.Event { return Ctx(ctx).Debug() }
func Info(ctx context.Context) *zerolog.Event  { return Ctx(ctx).Info() }
func Warn(ctx context.Context) *zerolog.Event  { return Ctx(ctx).Warn() }
func Error(ctx context.Context) *zerolog.Event { return Ctx(ctx).Error() }

type requestIDKey struct{}

// WithRequestID 把链路ID写入 context
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestID 从 context 读取链路ID，没有时返回空字符串。
// 依次查找 WithRequestID 写入的值、字符串键 "request_id" 的值；
// 后者使 *gin.Context 可以直接作为 ctx 使用(中间件中 c.Set("request_id", id) 即可)
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	id, _ := ctx.Value(FieldRequestID).(string)
	return id
}

// NewRequestID 生成一个链路ID：随机 UUID(v4)，如 3368607b-2c9b-4d03-9d07-3238c5bad607
func NewRequestID() string {
	return uuid.NewString()
}

// RequestIDFrom 处理外部传入的链路ID(如请求头 X-Request-ID)：是合法的 UUID 时沿用(统一为小写带连字符的格式)，否则生成新的
func RequestIDFrom(id string) string {
	if u, err := uuid.Parse(id); err == nil {
		return u.String()
	}
	return NewRequestID()
}

// shortCaller 调用位置只保留 目录/文件:行号
func shortCaller(file string, line int) string {
	if i := strings.LastIndex(file, "/"); i > 0 {
		if j := strings.LastIndex(file[:i], "/"); j >= 0 {
			file = file[j+1:]
		}
	}
	return file + ":" + strconv.Itoa(line)
}
