// Package cache 缓存：本地内存(cache/go-cache)、本地文件(cache/file)、Redis(cache/redis)三选一，由配置决定，业务代码只依赖 Cache 接口
//
//	c, err := cache.New(cfg.Cache, r)                // driver 不是 redis 时 r 可传 nil
//	defer c.Close()
//	err = cache.SetJSON(ctx, c, "user:1", u, time.Minute)
//	err = cache.GetJSON(ctx, c, "user:1", &u)        // 未命中返回 cache.ErrMiss
//
// 三种实现行为一致：ttl 为 0 表示不过期；未命中返回 ErrMiss；对类型不符的 key 操作返回 ErrWrongType。
//   - memory：存在服务进程内存中，最快；重启丢失，多实例不共享
//   - file：存在本地文件中，重启后仍在；多实例不共享，同一文件只能被一个进程打开
//   - redis：多实例共享，重启后仍在(取决于 Redis 持久化配置)
package cache

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/awp0816/common/cache/file"
	gocache "github.com/awp0816/common/cache/go-cache"
	"github.com/awp0816/common/cache/internal/errs"
	cacheredis "github.com/awp0816/common/cache/redis"
	"github.com/awp0816/common/redis"

	"github.com/pkg/errors"
)

var (
	// ErrMiss 缓存未命中
	ErrMiss = errs.ErrMiss
	// ErrUnavailable 缓存不可用(Redis 健康检查失败)
	ErrUnavailable = errs.ErrUnavailable
	// ErrWrongType key 中的数据类型与操作不符
	ErrWrongType = errs.ErrWrongType
)

// 缓存驱动
const (
	DriverMemory = "memory" // 本地内存
	DriverFile   = "file"   // 本地文件
	DriverRedis  = "redis"  // Redis
)

// Config 缓存配置
type Config struct {
	// Driver 缓存驱动：memory(默认) | file | redis
	Driver string `json:"driver" yaml:"driver" mapstructure:"driver"`
	// Path driver 为 file 时的缓存文件路径，默认 data/cache.db
	Path string `json:"path" yaml:"path" mapstructure:"path"`
}

// Cache 缓存操作，gocache.Memory、file.File、cacheredis.Store 都实现了该接口
type Cache interface {
	// Get 读取，未命中返回 ErrMiss
	Get(ctx context.Context, key string) (string, error)
	// Set 写入，ttl 为 0 表示不过期
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	// SetNX 不存在时才写入，返回是否写入成功(用于加锁、幂等)
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	// Del 删除，key 不存在时不报错
	Del(ctx context.Context, keys ...string) error
	// Exists 是否存在
	Exists(ctx context.Context, key string) (bool, error)
	// Expire 重新设置过期时间，ttl 为 0 时改为不过期；key 不存在时不报错
	Expire(ctx context.Context, key string, ttl time.Duration) error
	// Incr 自增 1 并返回新值，key 首次创建时设置过期时间 ttl(用于计数、限流)
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	// SAdd 添加集合成员，ttl 大于 0 时刷新过期时间
	SAdd(ctx context.Context, key string, ttl time.Duration, members ...string) error
	// SMembers 集合全部成员，key 不存在时返回空
	SMembers(ctx context.Context, key string) ([]string, error)
	// SRem 移除集合成员，集合为空时删除 key
	SRem(ctx context.Context, key string, members ...string) error
	// Close 释放资源：file 关闭文件；memory 与 redis 无操作(Redis 连接由调用方关闭)
	Close() error
}

var (
	_ Cache = (*gocache.Memory)(nil)
	_ Cache = (*file.File)(nil)
	_ Cache = (*cacheredis.Store)(nil)
)

// New 按配置创建缓存；driver 为 redis 时 r 不能为 nil，其它驱动不使用 r(可传 nil)
func New(cfg Config, r *redis.Redis) (Cache, error) {
	switch strings.ToLower(cfg.Driver) {
	case "", DriverMemory:
		return gocache.New(), nil
	case DriverFile:
		return file.Open(cfg.Path)
	case DriverRedis:
		if r == nil {
			return nil, errors.New("cache: driver 为 redis 时需要传入 Redis 连接")
		}
		return cacheredis.NewStore(r), nil
	default:
		return nil, errors.Errorf("cache: 不支持的驱动 %s，可选 memory | file | redis", cfg.Driver)
	}
}

// GetJSON 读取并按 JSON 解析到 v(指针)，未命中返回 ErrMiss
func GetJSON(ctx context.Context, c Cache, key string, v any) error {
	s, err := c.Get(ctx, key)
	if err != nil {
		return err
	}
	return errors.Wrapf(json.Unmarshal([]byte(s), v), "cache: %s 的值不是合法的 JSON", key)
}

// SetJSON 按 JSON 序列化后写入
func SetJSON(ctx context.Context, c Cache, key string, v any, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return errors.Wrapf(err, "cache: %s 的值序列化为 JSON 失败", key)
	}
	return c.Set(ctx, key, string(b), ttl)
}
