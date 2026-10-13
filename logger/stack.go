package logger

import (
	"bytes"
	"io"
	"runtime"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
)

// consoleWriter 文本格式输出(控制台、文本格式的文件)：调用栈不作为字段挤在一行，而是换行缩进输出在该条日志之后
func consoleWriter(out io.Writer, noColor bool) zerolog.ConsoleWriter {
	return zerolog.ConsoleWriter{
		Out:           out,
		NoColor:       noColor,
		TimeFormat:    TimeFormat,
		FieldsExclude: []string{FieldStacktrace},
		FormatExtra: func(evt map[string]any, buf *bytes.Buffer) error {
			st, _ := evt[FieldStacktrace].(string)
			for _, line := range strings.Split(st, "\n") {
				if line != "" {
					buf.WriteString("\n\t")
					buf.WriteString(line)
				}
			}
			return nil
		},
	}
}

// stackHook error 及以上级别附带调用栈
type stackHook struct{}

func (stackHook) Run(e *zerolog.Event, level zerolog.Level, _ string) {
	if level >= zerolog.ErrorLevel && level < zerolog.NoLevel {
		e.Str(FieldStacktrace, stack())
	}
}

// 生成调用栈时跳过的文件：Go 运行时、zerolog、本包
var stackSkipFiles = []string{"/src/runtime/", "/rs/zerolog", "/logger/stack.go", "/logger/logger.go"}

// stack 从打印日志的业务代码开始的调用栈，不含日志库与运行时内部的帧：
//
//	main.(*Order).Create
//		D:/project/order.go:27
//	main.main
//		D:/project/main.go:12
func stack() string {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	var sb strings.Builder
	for {
		f, more := frames.Next()
		if !skipFrame(f.File) {
			sb.WriteString(f.Function)
			sb.WriteString("\n\t")
			sb.WriteString(f.File)
			sb.WriteByte(':')
			sb.WriteString(strconv.Itoa(f.Line))
			sb.WriteByte('\n')
		}
		if !more {
			break
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

func skipFrame(file string) bool {
	for _, s := range stackSkipFiles {
		if strings.Contains(file, s) {
			return true
		}
	}
	return false
}
