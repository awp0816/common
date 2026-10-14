package amqp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/awp0816/common/logger"
	"github.com/pkg/errors"
)

// fakeAck 记录 Ack/Nack
type fakeAck struct {
	acked, nacked, requeue bool
}

func (f *fakeAck) Ack(uint64, bool) error { f.acked = true; return nil }
func (f *fakeAck) Nack(_ uint64, _ bool, requeue bool) error {
	f.nacked, f.requeue = true, requeue
	return nil
}
func (f *fakeAck) Reject(uint64, bool) error { return nil }

func TestHandle(t *testing.T) {
	_ = logger.Init(logger.Config{DisableFile: true})
	c := &Client{}
	fail := errors.New("fail")
	cases := []struct {
		name        string
		redelivered bool
		handler     Handler
		ack, nack   bool
		requeue     bool
	}{
		{"success", false, func(context.Context, Delivery) error { return nil }, true, false, false},
		{"first failure requeue", false, func(context.Context, Delivery) error { return fail }, false, true, true},
		{"redelivered failure drop", true, func(context.Context, Delivery) error { return fail }, false, true, false},
		{"panic", false, func(context.Context, Delivery) error { panic("boom") }, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &fakeAck{}
			c.handle(context.Background(), Delivery{Acknowledger: a, Redelivered: tc.redelivered}, tc.handler)
			if a.acked != tc.ack || a.nacked != tc.nack || a.requeue != tc.requeue {
				t.Fatalf("got ack=%v nack=%v requeue=%v", a.acked, a.nacked, a.requeue)
			}
		})
	}
}

// 消息头中的链路ID传入 handler 的 ctx；没有时生成新的
func TestHandleRequestID(t *testing.T) {
	c := &Client{}
	var got string
	h := func(ctx context.Context, _ Delivery) error { got = logger.RequestID(ctx); return nil }

	c.handle(context.Background(), Delivery{Acknowledger: &fakeAck{}, Headers: Table{HeaderRequestID: "rid-mq"}}, h)
	if got != "rid-mq" {
		t.Fatalf("request id: %q", got)
	}
	c.handle(context.Background(), Delivery{Acknowledger: &fakeAck{}}, h)
	if got == "" || got == "rid-mq" {
		t.Fatalf("should generate new request id: %q", got)
	}
}

func TestPublishOptions(t *testing.T) {
	m := Message{Headers: Table{"a": 1}}
	for _, o := range []PublishOption{WithHeaders(Table{"b": 2}), WithMessageID("m1"), WithContentType("text/plain"),
		WithExpiration(1500 * time.Millisecond), WithPriority(5)} {
		o(&m)
	}
	if m.Headers["a"] != 1 || m.Headers["b"] != 2 || m.MessageId != "m1" || m.ContentType != "text/plain" ||
		m.Expiration != "1500" || m.Priority != 5 {
		t.Fatalf("message: %+v", m)
	}
}

func TestNewErrors(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty url should fail")
	}
	if _, err := New(Config{Url: "amqp://guest:guest@127.0.0.1:1/"}); err == nil {
		t.Fatal("unreachable broker should fail")
	}
}

// 集成测试：设置环境变量 AMQP_URL(如 amqp://guest:guest@127.0.0.1:5672/)后运行
func TestPublishSubscribe(t *testing.T) {
	url := os.Getenv("AMQP_URL")
	if url == "" {
		t.Skip("AMQP_URL 未设置")
	}
	_ = logger.Init(logger.Config{DisableFile: true})
	c, err := New(Config{Url: url, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := Route{Exchange: "common.test", Queue: "common.test.q", RoutingKey: "k"}
	pubCtx := logger.WithRequestID(ctx, "rid-it")
	if err = c.PublishJSON(pubCtx, r, map[string]string{"hello": "world"}); err != nil {
		t.Fatal(err)
	}

	got := make(chan string, 1)
	go c.Subscribe(ctx, r, func(ctx context.Context, d Delivery) error {
		got <- logger.RequestID(ctx) + " " + string(d.Body)
		return nil
	})
	select {
	case v := <-got:
		if v != `rid-it {"hello":"world"}` {
			t.Fatalf("got %q", v)
		}
	case <-ctx.Done():
		t.Fatal("message not received")
	}
}
