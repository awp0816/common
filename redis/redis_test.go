package redis_test

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/awp0816/common/logger"
	"github.com/awp0816/common/redis"
)

func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestHealthCheck(t *testing.T) {
	_ = logger.Init(logger.Config{DisableFile: true})
	mr := miniredis.RunT(t)
	r, err := redis.New(redis.Config{Addresses: []string{mr.Addr()}, DialTimeout: 200, ReadTimeout: 200})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.StartHealthCheck(30 * time.Millisecond)

	mr.Close()
	waitUntil(t, func() bool { return !r.IsConnected() }, "should be marked down")
	if err = mr.Restart(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, r.IsConnected, "should recover")
}

func TestNewErrors(t *testing.T) {
	if _, err := redis.New(redis.Config{}); err == nil {
		t.Fatal("empty addresses should fail")
	}
	if _, err := redis.New(redis.Config{Mode: "sentinel", Addresses: []string{"127.0.0.1:1"}}); err == nil {
		t.Fatal("sentinel without master should fail")
	}
	if _, err := redis.New(redis.Config{Mode: "foo", Addresses: []string{"127.0.0.1:1"}}); err == nil {
		t.Fatal("unknown mode should fail")
	}
}
