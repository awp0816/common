package logger

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"gopkg.in/natefinch/lumberjack.v2"
)

// levelFiles 日志文件输出：按级别分文件时每个级别一个文件，否则所有级别共用一个文件；
// 低于最低级别的文件不会创建，文件在首次写入时才生成
type levelFiles struct {
	json    bool
	writers map[zerolog.Level]*rotateWriter
	shared  *rotateWriter
}

func newLevelFiles(cfg Config, min zerolog.Level) (*levelFiles, error) {
	if err := os.MkdirAll(cfg.Path, 0o755); err != nil {
		return nil, fmt.Errorf("logger: create log dir: %w", err)
	}
	f := &levelFiles{json: cfg.JSON, writers: map[zerolog.Level]*rotateWriter{}}
	if cfg.SplitLevel {
		for _, l := range []zerolog.Level{zerolog.DebugLevel, zerolog.InfoLevel, zerolog.WarnLevel,
			zerolog.ErrorLevel, zerolog.FatalLevel, zerolog.PanicLevel} {
			if l >= min {
				f.writers[l] = &rotateWriter{cfg: cfg, name: fmt.Sprintf("%s.%s.log", cfg.Filename, l)}
			}
		}
	} else {
		f.shared = &rotateWriter{cfg: cfg, name: cfg.Filename + ".log"}
	}
	if cfg.Daily {
		cleanupOldDirs(cfg)
	}
	return f, nil
}

func (f *levelFiles) Write(p []byte) (int, error) { return f.WriteLevel(zerolog.NoLevel, p) }

// WriteLevel 按级别写入对应的文件；文本格式时转为无颜色的控制台格式
func (f *levelFiles) WriteLevel(l zerolog.Level, p []byte) (int, error) {
	var w io.Writer
	if f.shared != nil {
		w = f.shared
	} else if l == zerolog.TraceLevel {
		w = f.writers[zerolog.DebugLevel]
	} else if rw := f.writers[l]; rw != nil {
		w = rw
	}
	if w == nil {
		return len(p), nil
	}
	if f.json {
		return w.Write(p)
	}
	if _, err := consoleWriter(w, true).Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close 关闭所有文件
func (f *levelFiles) Close() error {
	var errs []error
	if f.shared != nil {
		errs = append(errs, f.shared.Close())
	}
	for _, w := range f.writers {
		errs = append(errs, w.Close())
	}
	return errors.Join(errs...)
}

// rotateWriter 单个日志文件：由 lumberjack 按大小切割；按天分目录时跨天自动切换到新目录
type rotateWriter struct {
	mu   sync.Mutex
	cfg  Config
	name string
	day  string
	lj   *lumberjack.Logger
}

func (w *rotateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	day := ""
	if w.cfg.Daily {
		day = time.Now().Format(time.DateOnly)
	}
	if w.lj == nil || day != w.day {
		if w.lj != nil {
			_ = w.lj.Close()
			go cleanupOldDirs(w.cfg)
		}
		dir := filepath.Join(w.cfg.Path, day)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, err
		}
		w.lj = &lumberjack.Logger{
			Filename:   filepath.Join(dir, w.name),
			MaxSize:    w.cfg.MaxSize,
			MaxAge:     w.cfg.MaxAge,
			MaxBackups: w.cfg.MaxBackups,
			LocalTime:  true,
			Compress:   w.cfg.Compress,
		}
		w.day = day
	}
	return w.lj.Write(p)
}

func (w *rotateWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lj == nil {
		return nil
	}
	err := w.lj.Close()
	w.lj = nil
	return err
}

var cleanupMu sync.Mutex

// cleanupOldDirs 按天分目录时删除超过保留天数的日期目录(lumberjack 只清理同一目录内的文件)；
// 创建日志和每次跨天时自动调用
func cleanupOldDirs(cfg Config) {
	if cfg.MaxAge <= 0 {
		return
	}
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	entries, err := os.ReadDir(cfg.Path)
	if err != nil {
		return
	}
	y, m, d := time.Now().Date()
	deadline := time.Date(y, m, d, 0, 0, 0, 0, time.Local).AddDate(0, 0, -cfg.MaxAge)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		day, err := time.ParseInLocation(time.DateOnly, e.Name(), time.Local)
		if err == nil && day.Before(deadline) {
			_ = os.RemoveAll(filepath.Join(cfg.Path, e.Name()))
		}
	}
}
