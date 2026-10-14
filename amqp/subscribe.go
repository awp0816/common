package amqp

import (
	"context"
	"time"

	"github.com/awp0816/common/logger"

	"github.com/pkg/errors"
)

// Handler 消息处理函数，ctx 带有消息头中的链路ID(没有时生成新的)。
// 返回 nil 确认消息；返回错误时：首次投递重新入队，重复投递则丢弃(避免毒消息无限重试；可配合死信队列保留)
type Handler func(ctx context.Context, d Delivery) error

// Subscribe 订阅消费，阻塞直到 ctx 取消或 Close；连接断开后自动重新订阅。
// 消息逐条处理，handler 中的 panic 视为处理失败
func (c *Client) Subscribe(ctx context.Context, r Route, handler Handler) {
	backoff := minBackoff
	for {
		err := c.consume(ctx, r, handler)
		if ctx.Err() != nil || c.ctx.Err() != nil {
			return
		}
		logger.Warn(context.Background()).Str("queue", r.Queue).Err(err).Msg("RabbitMQ 消费中断，稍后重新订阅")
		select {
		case <-ctx.Done():
			return
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (c *Client) consume(ctx context.Context, r Route, handler Handler) error {
	ch, err := c.channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	queue := r.Queue
	if queue == "" { // 临时队列：本实例独占，断开后自动删除
		q, err := ch.QueueDeclare("", false, true, true, false, nil)
		if err != nil {
			return errors.Wrap(err, "amqp: 声明临时队列失败")
		}
		queue = q.Name
		if err = declare(ch, Route{Exchange: r.Exchange, ExchangeKind: r.ExchangeKind, Durable: r.Durable}); err != nil {
			return err
		}
		if err = ch.QueueBind(queue, r.RoutingKey, r.Exchange, false, nil); err != nil {
			return errors.Wrapf(err, "amqp: 绑定临时队列到交换机 %s 失败", r.Exchange)
		}
	} else if err = declare(ch, r); err != nil {
		return err
	}
	if err = ch.Qos(c.cfg.Prefetch, 0, false); err != nil {
		return errors.Wrap(err, "amqp: 设置 prefetch 失败")
	}
	deliveries, err := ch.Consume(queue, r.Consumer, false, false, false, false, nil)
	if err != nil {
		return errors.Wrapf(err, "amqp: 订阅队列 %s 失败", queue)
	}
	logger.Debug(context.Background()).Str("exchange", r.Exchange).Str("queue", queue).Msg("开始消费 RabbitMQ 消息")
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("amqp: 消息通道已关闭(连接断开)")
			}
			c.handle(ctx, d, handler)
		}
	}
}

func (c *Client) handle(ctx context.Context, d Delivery, handler Handler) {
	id, _ := d.Headers[HeaderRequestID].(string)
	if id == "" {
		id = logger.NewRequestID()
	}
	ctx = logger.WithRequestID(ctx, id)
	if err := safeHandle(ctx, d, handler); err != nil {
		logger.Error(ctx).Str("message_id", d.MessageId).Str("routing_key", d.RoutingKey).Bool("redelivered", d.Redelivered).
			Err(err).Msg("RabbitMQ 消息处理失败")
		_ = d.Nack(false, !d.Redelivered)
		return
	}
	_ = d.Ack(false)
}

func safeHandle(ctx context.Context, d Delivery, handler Handler) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = errors.Errorf("amqp: 消息处理函数 panic: %v", v)
		}
	}()
	return handler(ctx, d)
}
