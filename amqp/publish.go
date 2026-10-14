package amqp

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/awp0816/common/logger"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Message 发布的消息属性，通过 PublishOption 设置
type Message = amqp.Publishing

// PublishOption 设置消息属性
type PublishOption func(*Message)

// WithHeaders 追加消息头
func WithHeaders(h Table) PublishOption {
	return func(m *Message) {
		if m.Headers == nil {
			m.Headers = Table{}
		}
		for k, v := range h {
			m.Headers[k] = v
		}
	}
}

// WithMessageID 指定消息ID(默认随机 UUID)，消费方可据此去重
func WithMessageID(id string) PublishOption {
	return func(m *Message) { m.MessageId = id }
}

// WithContentType 指定内容类型(PublishJSON 默认 application/json)
func WithContentType(t string) PublishOption {
	return func(m *Message) { m.ContentType = t }
}

// WithExpiration 消息过期时间，过期未消费则丢弃(或进入死信队列)
func WithExpiration(d time.Duration) PublishOption {
	return func(m *Message) { m.Expiration = formatMillis(d) }
}

// WithPriority 消息优先级 0~9(队列需声明 x-max-priority)
func WithPriority(p uint8) PublishOption {
	return func(m *Message) { m.Priority = p }
}

func formatMillis(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10)
}

// PublishJSON 按 JSON 序列化后发布
func (c *Client) PublishJSON(ctx context.Context, r Route, v any, opts ...PublishOption) error {
	body, err := json.Marshal(v)
	if err != nil {
		return errors.Wrap(err, "amqp: 消息序列化为 JSON 失败")
	}
	return c.Publish(ctx, r, body, append([]PublishOption{WithContentType("application/json")}, opts...)...)
}

// Publish 发布消息。首次发布某个 Route 时声明交换机(以及队列与绑定)；
// 开启 Config.Confirm 时等待服务端确认，被拒绝返回 ErrNacked。失败时返回错误并记录日志
func (c *Client) Publish(ctx context.Context, r Route, body []byte, opts ...PublishOption) error {
	msg := Message{
		MessageId: uuid.NewString(),
		Timestamp: time.Now(),
		Body:      body,
	}
	if r.Durable {
		msg.DeliveryMode = amqp.Persistent
	}
	if id := logger.RequestID(ctx); id != "" {
		msg.Headers = Table{HeaderRequestID: id}
	}
	for _, o := range opts {
		o(&msg)
	}

	dc, err := c.publish(ctx, r, msg)
	if err == nil && dc != nil {
		var ok bool
		if ok, err = dc.WaitContext(ctx); err != nil {
			err = errors.Wrap(err, "amqp: 等待发布确认失败")
		} else if !ok {
			err = ErrNacked
		}
	}
	if err != nil {
		logger.Error(ctx).Err(err).Str("exchange", r.Exchange).Str("routing_key", r.RoutingKey).
			Str("message_id", msg.MessageId).Msg("RabbitMQ 消息发布失败")
	}
	return err
}

// publish 在发布通道上声明并发送；通道异常时关闭，下次发布重新创建
func (c *Client) publish(ctx context.Context, r Route, msg Message) (*amqp.DeferredConfirmation, error) {
	c.pubMu.Lock()
	defer c.pubMu.Unlock()
	ch, err := c.publishChannel()
	if err != nil {
		return nil, err
	}
	if !c.declared[r] {
		if err = declare(ch, r); err != nil {
			c.resetPublishChannel()
			return nil, err
		}
		c.declared[r] = true
	}
	dc, err := ch.PublishWithDeferredConfirmWithContext(ctx, r.Exchange, r.RoutingKey, false, false, msg)
	if err != nil {
		c.resetPublishChannel()
		return nil, errors.Wrapf(err, "amqp: 发布消息到交换机 %s 失败", r.Exchange)
	}
	return dc, nil // 未开启发布确认时 dc 为 nil
}

func (c *Client) publishChannel() (*amqp.Channel, error) {
	if c.pubCh != nil && !c.pubCh.IsClosed() {
		return c.pubCh, nil
	}
	ch, err := c.channel()
	if err != nil {
		return nil, err
	}
	if c.cfg.Confirm {
		if err = ch.Confirm(false); err != nil {
			_ = ch.Close()
			return nil, errors.Wrap(err, "amqp: 开启发布确认失败")
		}
	}
	c.pubCh, c.declared = ch, map[Route]bool{}
	return ch, nil
}

func (c *Client) resetPublishChannel() {
	if c.pubCh != nil {
		_ = c.pubCh.Close()
		c.pubCh = nil
	}
}
