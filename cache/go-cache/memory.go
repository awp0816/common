// Package gocache 本地内存缓存(基于 github.com/patrickmn/go-cache)，实现 cache.Cache 接口，与 cache/redis 行为一致。
// 数据只在当前进程内有效，重启后丢失，多实例之间不共享
package gocache

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/awp0816/common/cache/internal/errs"

	gocache "github.com/patrickmn/go-cache"
	"github.com/pkg/errors"
)

// set 集合，写时复制(不修改已存入缓存的 map)
type set map[string]struct{}

// Memory 本地内存缓存，并发安全
type Memory struct {
	c  *gocache.Cache
	mu sync.Mutex // 写操作加锁，保证 SetNX、Incr、集合等读改写操作的原子性
}

// New 创建本地缓存，过期数据每分钟清理一次
func New() *Memory {
	return &Memory{c: gocache.New(gocache.NoExpiration, time.Minute)}
}

// wrongType 与 Redis 实现的错误格式一致
func wrongType(op, key string) error {
	return errors.Wrapf(errs.ErrWrongType, "cache: 本地缓存 %s %s 失败", op, key)
}

// expiration ttl 为 0 表示不过期
func expiration(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return gocache.NoExpiration
	}
	return ttl
}

// remaining 剩余有效期，不过期时返回 NoExpiration
func remaining(exp time.Time) time.Duration {
	if exp.IsZero() {
		return gocache.NoExpiration
	}
	if d := time.Until(exp); d > 0 {
		return d
	}
	return time.Nanosecond
}

func (m *Memory) Get(_ context.Context, key string) (string, error) {
	v, ok := m.c.Get(key)
	if !ok {
		return "", errs.ErrMiss
	}
	s, ok := v.(string)
	if !ok {
		return "", wrongType("GET", key)
	}
	return s, nil
}

func (m *Memory) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.c.Set(key, value, expiration(ttl))
	return nil
}

func (m *Memory) SetNX(_ context.Context, key, value string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.c.Add(key, value, expiration(ttl)) == nil, nil
}

func (m *Memory) Del(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		m.c.Delete(k)
	}
	return nil
}

func (m *Memory) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.c.Get(key)
	return ok, nil
}

func (m *Memory) Expire(_ context.Context, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.c.Get(key); ok {
		m.c.Set(key, v, expiration(ttl))
	}
	return nil
}

func (m *Memory) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, exp, ok := m.c.GetWithExpiration(key)
	if !ok {
		m.c.Set(key, "1", expiration(ttl))
		return 1, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return 0, wrongType("INCR", key)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, wrongType("INCR", key)
	}
	n++
	m.c.Set(key, strconv.FormatInt(n, 10), remaining(exp))
	return n, nil
}

func (m *Memory) SAdd(_ context.Context, key string, ttl time.Duration, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, exp, err := m.getSet(key)
	if err != nil {
		return err
	}
	s := make(set, len(old)+len(members))
	for k := range old {
		s[k] = struct{}{}
	}
	for _, k := range members {
		s[k] = struct{}{}
	}
	d := remaining(exp)
	if old == nil {
		d = gocache.NoExpiration
	}
	if ttl > 0 {
		d = ttl
	}
	m.c.Set(key, s, d)
	return nil
}

func (m *Memory) SMembers(_ context.Context, key string) ([]string, error) {
	s, _, err := m.getSet(key)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	slices.Sort(out)
	return out, nil
}

func (m *Memory) SRem(_ context.Context, key string, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, exp, err := m.getSet(key)
	if err != nil || old == nil {
		return err
	}
	s := make(set, len(old))
	for k := range old {
		s[k] = struct{}{}
	}
	for _, k := range members {
		delete(s, k)
	}
	if len(s) == 0 {
		m.c.Delete(key)
		return nil
	}
	m.c.Set(key, s, remaining(exp))
	return nil
}

// getSet 读取集合，key 不存在时返回 nil
func (m *Memory) getSet(key string) (set, time.Time, error) {
	v, exp, ok := m.c.GetWithExpiration(key)
	if !ok {
		return nil, time.Time{}, nil
	}
	s, isSet := v.(set)
	if !isSet {
		return nil, time.Time{}, wrongType("集合操作", key)
	}
	return s, exp, nil
}

// DeletePrefix 删除指定前缀的全部 key(Redis 实现中没有该方法)
func (m *Memory) DeletePrefix(prefix string) {
	if prefix == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.c.Items() {
		if strings.HasPrefix(k, prefix) {
			m.c.Delete(k)
		}
	}
}

// Flush 清空
func (m *Memory) Flush() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.c.Flush()
}

// Close 无操作，实现 cache.Cache 接口
func (m *Memory) Close() error { return nil }

// Count 缓存条数(含已过期未清理的)
func (m *Memory) Count() int { return m.c.ItemCount() }
