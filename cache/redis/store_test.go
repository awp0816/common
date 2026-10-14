package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/awp0816/common/cache"
	cacheredis "github.com/awp0816/common/cache/redis"
	"github.com/awp0816/common/logger"
	"github.com/awp0816/common/redis"
	"github.com/pkg/errors"
)

func newStore(t *testing.T) (*cacheredis.Store, *miniredis.Miniredis, *redis.Redis) {
	t.Helper()
	_ = logger.Init(logger.Config{DisableFile: true})
	mr := miniredis.RunT(t)
	r, err := redis.New(redis.Config{Addresses: []string{mr.Addr()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return cacheredis.NewStore(r), mr, r
}

// 通用操作见 cache 包的 TestCache，这里只测 Redis 特有的列表操作
func TestList(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_ = s.ListPush(ctx, "list", 2, time.Minute, "1", "2", "3")
	if l, _ := s.ListPopAll(ctx, "list"); len(l) != 2 || l[0] != "2" || l[1] != "3" {
		t.Fatalf("list: %v", l)
	}
	if ok, _ := s.Exists(ctx, "list"); ok {
		t.Fatal("list should be removed")
	}
}

func TestStoreUnavailable(t *testing.T) {
	s, _, r := newStore(t)
	_ = r.Close()
	if err := s.Set(context.Background(), "k", "v", 0); !errors.Is(err, cache.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}
