package cache_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/awp0816/common/cache"
	"github.com/awp0816/common/redis"
	"github.com/pkg/errors"
)

// 两种实现跑同一套用例，保证行为一致
func drivers(t *testing.T) map[string]func(t *testing.T) (cache.Cache, func(time.Duration)) {
	return map[string]func(t *testing.T) (cache.Cache, func(time.Duration)){
		cache.DriverMemory: func(t *testing.T) (cache.Cache, func(time.Duration)) {
			c, err := cache.New(cache.Config{Driver: cache.DriverMemory}, nil)
			if err != nil {
				t.Fatal(err)
			}
			return c, time.Sleep
		},
		cache.DriverFile: func(t *testing.T) (cache.Cache, func(time.Duration)) {
			c, err := cache.New(cache.Config{Driver: cache.DriverFile, Path: filepath.Join(t.TempDir(), "data", "cache.db")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			return c, time.Sleep
		},
		cache.DriverRedis: func(t *testing.T) (cache.Cache, func(time.Duration)) {
			mr := miniredis.RunT(t)
			r, err := redis.New(redis.Config{Addresses: []string{mr.Addr()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			c, err := cache.New(cache.Config{Driver: cache.DriverRedis}, r)
			if err != nil {
				t.Fatal(err)
			}
			return c, mr.FastForward // miniredis 的过期需要手动推进时间
		},
	}
}

func TestCache(t *testing.T) {
	for name, newCache := range drivers(t) {
		t.Run(name, func(t *testing.T) {
			c, wait := newCache(t)
			ctx := context.Background()

			// Get / Set / 未命中
			if _, err := c.Get(ctx, "none"); !errors.Is(err, cache.ErrMiss) {
				t.Fatalf("want ErrMiss, got %v", err)
			}
			_ = c.Set(ctx, "k", "v", 0)
			if v, err := c.Get(ctx, "k"); err != nil || v != "v" {
				t.Fatalf("get: %q %v", v, err)
			}

			// 过期
			_ = c.Set(ctx, "short", "v", 50*time.Millisecond)
			wait(100 * time.Millisecond)
			if ok, _ := c.Exists(ctx, "short"); ok {
				t.Fatal("short should expire")
			}
			_ = c.Expire(ctx, "k", 50*time.Millisecond)
			wait(100 * time.Millisecond)
			if ok, _ := c.Exists(ctx, "k"); ok {
				t.Fatal("k should expire after Expire")
			}
			_ = c.Set(ctx, "p", "v", 50*time.Millisecond)
			_ = c.Expire(ctx, "p", 0)
			wait(100 * time.Millisecond)
			if ok, _ := c.Exists(ctx, "p"); !ok {
				t.Fatal("Expire 0 should make key persistent")
			}
			if err := c.Expire(ctx, "none", time.Second); err != nil {
				t.Fatalf("expire missing key: %v", err)
			}

			// JSON
			type user struct{ Name string }
			if err := cache.SetJSON(ctx, c, "u", user{"alice"}, time.Minute); err != nil {
				t.Fatal(err)
			}
			var u user
			if err := cache.GetJSON(ctx, c, "u", &u); err != nil || u.Name != "alice" {
				t.Fatalf("json: %v %v", u, err)
			}

			// SetNX
			if ok, _ := c.SetNX(ctx, "lock", "1", time.Minute); !ok {
				t.Fatal("first SetNX should succeed")
			}
			if ok, _ := c.SetNX(ctx, "lock", "2", time.Minute); ok {
				t.Fatal("second SetNX should fail")
			}

			// Incr：首次设置过期时间，之后保持
			for i := 1; i <= 3; i++ {
				if n, err := c.Incr(ctx, "cnt", 50*time.Millisecond); err != nil || n != int64(i) {
					t.Fatalf("incr: %d %v", n, err)
				}
			}
			wait(100 * time.Millisecond)
			if n, _ := c.Incr(ctx, "cnt", 0); n != 1 {
				t.Fatalf("incr after expire should restart, got %d", n)
			}
			if _, err := c.Incr(ctx, "u", 0); !errors.Is(err, cache.ErrWrongType) {
				t.Fatalf("incr json value: want ErrWrongType, got %v", err)
			}

			// 集合
			_ = c.SAdd(ctx, "set", time.Minute, "b", "a")
			_ = c.SAdd(ctx, "set", 0, "c")
			_ = c.SRem(ctx, "set", "b")
			if m, _ := c.SMembers(ctx, "set"); len(m) != 2 || !contains(m, "a") || !contains(m, "c") {
				t.Fatalf("set: %v", m)
			}
			_ = c.SRem(ctx, "set", "a", "c")
			if ok, _ := c.Exists(ctx, "set"); ok {
				t.Fatal("empty set should be removed")
			}
			if m, err := c.SMembers(ctx, "none"); err != nil || len(m) != 0 {
				t.Fatalf("members of missing key: %v %v", m, err)
			}
			if err := c.SAdd(ctx, "lock", 0, "x"); !errors.Is(err, cache.ErrWrongType) {
				t.Fatalf("sadd on string: want ErrWrongType, got %v", err)
			}
			if _, err := c.Get(ctx, "set2"); !errors.Is(err, cache.ErrMiss) {
				t.Fatal(err)
			}
			_ = c.SAdd(ctx, "set2", 0, "x")
			if _, err := c.Get(ctx, "set2"); !errors.Is(err, cache.ErrWrongType) {
				t.Fatalf("get on set: want ErrWrongType, got %v", err)
			}

			// Del
			if err := c.Del(ctx, "u", "lock", "none"); err != nil {
				t.Fatal(err)
			}
			if ok, _ := c.Exists(ctx, "u"); ok {
				t.Fatal("u should be deleted")
			}
		})
	}
}

func TestNew(t *testing.T) {
	if c, err := cache.New(cache.Config{}, nil); err != nil || c == nil {
		t.Fatalf("default driver should be memory: %v", err)
	}
	if _, err := cache.New(cache.Config{Driver: cache.DriverRedis}, nil); err == nil {
		t.Fatal("redis without connection should fail")
	}
	if _, err := cache.New(cache.Config{Driver: "memcached"}, nil); err == nil {
		t.Fatal("unknown driver should fail")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
