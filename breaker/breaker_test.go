package breaker

import (
	"testing"
	"time"

	"github.com/pkg/errors"
)

var errFail = errors.New("fail")

func TestBreakerTripsOnConsecutiveFailures(t *testing.T) {
	b := New(Settings{Name: "t", ConsecutiveFailures: 3, Timeout: 50 * time.Millisecond, MaxRequests: 1})

	for i := 0; i < 3; i++ {
		_ = b.Execute(func() error { return errFail })
	}
	if b.State() != StateOpen {
		t.Fatalf("want open, got %s", b.State())
	}
	if err := b.Execute(func() error { return nil }); !errors.Is(err, ErrOpenState) {
		t.Fatalf("want ErrOpenState, got %v", err)
	}

	// 超时后进入半开，探测成功恢复关闭
	time.Sleep(60 * time.Millisecond)
	if b.State() != StateHalfOpen {
		t.Fatalf("want half-open, got %s", b.State())
	}
	if err := b.Execute(func() error { return nil }); err != nil {
		t.Fatalf("probe should pass: %v", err)
	}
	if b.State() != StateClosed {
		t.Fatalf("want closed, got %s", b.State())
	}
}

func TestBreakerTripsOnFailureRatio(t *testing.T) {
	b := New(Settings{Name: "r", FailureRatio: 0.5, MinRequests: 4, Timeout: time.Second})
	_ = b.Execute(func() error { return nil })
	_ = b.Execute(func() error { return nil })
	_ = b.Execute(func() error { return errFail })
	if b.State() != StateClosed {
		t.Fatalf("below min requests should stay closed")
	}
	_ = b.Execute(func() error { return errFail })
	if b.State() != StateOpen {
		t.Fatalf("want open at 50%% failure ratio, got %s", b.State())
	}
}

func TestHalfOpenFailureReopens(t *testing.T) {
	b := New(Settings{Name: "h", ConsecutiveFailures: 1, Timeout: 20 * time.Millisecond, MaxRequests: 2})
	_ = b.Execute(func() error { return errFail })
	time.Sleep(30 * time.Millisecond)

	done, err := b.Allow()
	if err != nil {
		t.Fatalf("half-open should allow probe: %v", err)
	}
	done(false)
	if b.State() != StateOpen {
		t.Fatalf("failed probe should reopen, got %s", b.State())
	}
}
