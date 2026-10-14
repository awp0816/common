// Package redis Redis 缓存(github.com/awp0816/common/redis 之上的常用命令封装)，实现 cache.Cache 接口，与 cache/go-cache 行为一致；
// 另外提供列表、发布订阅等 Redis 特有的操作。
// 连接被健康检查标记为不可用时直接返回 cache.ErrUnavailable(快速失败)，避免请求堆积在超时上
package redis

import (
	"context"
	"strings"
	"time"

	"github.com/awp0816/common/cache/internal/errs"
	rds "github.com/awp0816/common/redis"

	"github.com/pkg/errors"
	goredis "github.com/redis/go-redis/v9"
)

// Store Redis 缓存操作
type Store struct {
	r *rds.Redis
}

// NewStore 创建缓存操作
func NewStore(r *rds.Redis) *Store { return &Store{r: r} }

// wrap 包装 Redis 错误：说明操作与 key；类型不符(WRONGTYPE、对非数字自增)转换为 cache.ErrWrongType；err 为 nil 时返回 nil
func wrap(err error, op, key string) error {
	if err == nil {
		return nil
	}
	if msg := err.Error(); strings.HasPrefix(msg, "WRONGTYPE") || strings.HasPrefix(msg, "ERR value is not an integer") {
		err = errs.ErrWrongType
	}
	return errors.Wrapf(err, "cache: Redis %s %s 失败", op, key)
}

func (s *Store) cli() (goredis.UniversalClient, error) {
	if !s.r.IsConnected() {
		return nil, errs.ErrUnavailable
	}
	return s.r.Client(), nil
}

// Get 读取字符串，未命中返回 ErrMiss
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	cli, err := s.cli()
	if err != nil {
		return "", err
	}
	v, err := cli.Get(ctx, key).Result()
	if errors.Is(err, goredis.Nil) {
		return "", errs.ErrMiss
	}
	return v, wrap(err, "GET", key)
}

// Set 写入字符串，ttl 为 0 表示不过期
func (s *Store) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	cli, err := s.cli()
	if err != nil {
		return err
	}
	return wrap(cli.Set(ctx, key, value, ttl).Err(), "SET", key)
}

// SetNX 不存在时才设置，用于分布式锁、幂等
func (s *Store) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	cli, err := s.cli()
	if err != nil {
		return false, err
	}
	ok, err := cli.SetNX(ctx, key, value, ttl).Result()
	return ok, wrap(err, "SETNX", key)
}

// Del 删除
func (s *Store) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	cli, err := s.cli()
	if err != nil {
		return err
	}
	return wrap(cli.Del(ctx, keys...).Err(), "DEL", strings.Join(keys, ","))
}

// Exists 是否存在
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	cli, err := s.cli()
	if err != nil {
		return false, err
	}
	n, err := cli.Exists(ctx, key).Result()
	return n > 0, wrap(err, "EXISTS", key)
}

// Expire 设置过期时间，ttl 为 0 时改为不过期
func (s *Store) Expire(ctx context.Context, key string, ttl time.Duration) error {
	cli, err := s.cli()
	if err != nil {
		return err
	}
	if ttl <= 0 {
		return wrap(cli.Persist(ctx, key).Err(), "PERSIST", key)
	}
	return wrap(cli.PExpire(ctx, key, ttl).Err(), "PEXPIRE", key)
}

// Incr 自增，首次创建时设置过期时间(用于计数、限流)
func (s *Store) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	cli, err := s.cli()
	if err != nil {
		return 0, err
	}
	n, err := cli.Incr(ctx, key).Result()
	if err != nil {
		return 0, wrap(err, "INCR", key)
	}
	if n == 1 && ttl > 0 {
		if err = cli.PExpire(ctx, key, ttl).Err(); err != nil {
			return n, wrap(err, "PEXPIRE", key)
		}
	}
	return n, nil
}

// SAdd 添加集合成员并刷新过期时间
func (s *Store) SAdd(ctx context.Context, key string, ttl time.Duration, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	cli, err := s.cli()
	if err != nil {
		return err
	}
	_, err = cli.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		p.SAdd(ctx, key, toArgs(members)...)
		if ttl > 0 {
			p.PExpire(ctx, key, ttl)
		}
		return nil
	})
	return wrap(err, "SADD", key)
}

// SMembers 集合全部成员
func (s *Store) SMembers(ctx context.Context, key string) ([]string, error) {
	cli, err := s.cli()
	if err != nil {
		return nil, err
	}
	m, err := cli.SMembers(ctx, key).Result()
	return m, wrap(err, "SMEMBERS", key)
}

// SRem 移除集合成员
func (s *Store) SRem(ctx context.Context, key string, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	cli, err := s.cli()
	if err != nil {
		return err
	}
	return wrap(cli.SRem(ctx, key, toArgs(members)...).Err(), "SREM", key)
}

// ListPush 追加到列表尾部，并裁剪为最多 max 条(保留最新，0 不裁剪)，同时刷新过期时间
func (s *Store) ListPush(ctx context.Context, key string, max int64, ttl time.Duration, values ...string) error {
	if len(values) == 0 {
		return nil
	}
	cli, err := s.cli()
	if err != nil {
		return err
	}
	_, err = cli.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		p.RPush(ctx, key, toArgs(values)...)
		if max > 0 {
			p.LTrim(ctx, key, -max, -1)
		}
		if ttl > 0 {
			p.PExpire(ctx, key, ttl)
		}
		return nil
	})
	return wrap(err, "RPUSH", key)
}

// ListPopAll 原子地取出并清空整个列表
func (s *Store) ListPopAll(ctx context.Context, key string) ([]string, error) {
	cli, err := s.cli()
	if err != nil {
		return nil, err
	}
	var rng *goredis.StringSliceCmd
	_, err = cli.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		rng = p.LRange(ctx, key, 0, -1)
		p.Del(ctx, key)
		return nil
	})
	if err != nil {
		return nil, wrap(err, "LRANGE+DEL", key)
	}
	return rng.Val(), nil
}

// Publish 发布消息到频道(跨实例通知)
func (s *Store) Publish(ctx context.Context, channel string, msg any) error {
	cli, err := s.cli()
	if err != nil {
		return err
	}
	return wrap(cli.Publish(ctx, channel, msg).Err(), "PUBLISH", channel)
}

// Close 无操作，实现 cache.Cache 接口；Redis 连接由创建方关闭
func (s *Store) Close() error { return nil }

// Subscribe 订阅频道(跨实例通知)，调用方负责 Close
func (s *Store) Subscribe(ctx context.Context, channels ...string) *goredis.PubSub {
	return s.r.Client().Subscribe(ctx, channels...)
}

func toArgs(list []string) []any {
	args := make([]any, len(list))
	for i, v := range list {
		args[i] = v
	}
	return args
}
