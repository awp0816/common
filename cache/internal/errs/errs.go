// Package errs 缓存的公共错误，由 cache 包对外导出(cache.ErrMiss 等)；单独成包是为了让两个实现子包引用而不形成循环依赖
package errs

import "github.com/pkg/errors"

var (
	// ErrMiss 缓存未命中
	ErrMiss = errors.New("cache: miss")
	// ErrUnavailable 缓存不可用(Redis 健康检查失败)
	ErrUnavailable = errors.New("cache: unavailable")
	// ErrWrongType key 中的数据类型与操作不符(如对字符串执行集合操作)
	ErrWrongType = errors.New("cache: wrong type")
)
