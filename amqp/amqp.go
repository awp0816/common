// Package amqp RabbitMQ 客户端(基于 github.com/rabbitmq/amqp091-go)
// 发布 + 订阅，断线自动重连(指数退避+抖动)，
// 订阅在重连后自动恢复，发布通道在重连后自动重建
//
//	c, err := amqp.New(cfg)
//	defer c.Close()
//	route := amqp.Route{Exchange: "order", ExchangeKind: amqp.ExchangeDirect, Queue: "order.created", RoutingKey: "created", Durable: true}
//	err = c.PublishJSON(ctx, route, order)                                   // 发布
//	go c.Subscribe(ctx, route, func(ctx context.Context, d amqp.Delivery) error { ... }) // 订阅(阻塞)
//
// 链路ID：发布时把 ctx 中的 request_id 写入消息头，消费时取出放入 handler 的 ctx，生产方与消费方的日志可以串起来
package amqp

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/awp0816/common/logger"

	"github.com/pkg/errors"
	amqp "github.com/rabbitmq/amqp091-go"
)

// 交换机类型
const (
	ExchangeDirect  = amqp.ExchangeDirect  // 直连：路由键完全匹配
	ExchangeTopic   = amqp.ExchangeTopic   // 主题：路由键通配匹配(* 一个词，# 零个或多个词)
	ExchangeFanout  = amqp.ExchangeFanout  // 广播：忽略路由键，发给所有绑定的队列
	ExchangeHeaders = amqp.ExchangeHeaders // 头：按消息头匹配
)

// HeaderRequestID 消息头中的链路ID
const HeaderRequestID = logger.FieldRequestID

const (
	minBackoff = 2 * time.Second
	maxBackoff = 30 * time.Second
)

// Delivery 收到的消息
type Delivery = amqp.Delivery

// Table 消息头
type Table = amqp.Table

var (
	// ErrNotConnected 未连接(正在重连或已关闭)
	ErrNotConnected = errors.New("amqp: not connected")
	// ErrNacked 服务端拒绝了消息(开启发布确认时)
	ErrNacked = errors.New("amqp: message nacked by broker")
)

// Config 配置
type Config struct {
	// Url 连接地址 amqp://user:password@host:5672/vhost
	Url string `json:"-" yaml:"url" mapstructure:"url"`
	// Heartbeat 心跳间隔(秒)，默认 10
	Heartbeat int `json:"heartbeat" yaml:"heartbeat" mapstructure:"heartbeat"`
	// Prefetch 每个消费者未确认消息的上限，默认 10
	Prefetch int `json:"prefetch" yaml:"prefetch" mapstructure:"prefetch"`
	// Confirm 发布确认：Publish 等待服务端确认后才返回，保证消息已被服务端接收(性能略低)
	Confirm bool `json:"confirm" yaml:"confirm" mapstructure:"confirm"`
}

// Route 交换机/队列/路由键，发布与订阅使用同一个 Route 即可
type Route struct {
	// Exchange 交换机名称
	Exchange string
	// ExchangeKind 交换机类型，默认 direct
	ExchangeKind string
	// Queue 队列名称。发布时不为空则同时声明队列并绑定(没有消费者时消息也不会丢失)；
	// 订阅时为空则声明一个本实例独占、断开后自动删除的临时队列(配合 fanout 实现每个实例都收到的广播)
	Queue string
	// RoutingKey 路由键
	RoutingKey string
	// Durable 交换机、队列持久化，消息也持久化(服务端重启后不丢失)
	Durable bool
	// Consumer 消费者标识，为空时由服务端生成
	Consumer string
}

// Client RabbitMQ 客户端，并发安全
type Client struct {
	cfg    Config
	mu     sync.RWMutex
	conn   *amqp.Connection
	ctx    context.Context
	cancel context.CancelFunc

	pubMu    sync.Mutex
	pubCh    *amqp.Channel
	declared map[Route]bool // 发布通道上已声明过的 Route，通道重建后清空
}

// New 建立连接并启动断线重连
func New(cfg Config) (*Client, error) {
	if cfg.Url == "" {
		return nil, errors.New("amqp: url 不能为空")
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 10
	}
	if cfg.Prefetch <= 0 {
		cfg.Prefetch = 10
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{cfg: cfg, ctx: ctx, cancel: cancel}
	if err := c.connect(); err != nil {
		cancel()
		return nil, err
	}
	return c, nil
}

func (c *Client) connect() error {
	conn, err := amqp.DialConfig(c.cfg.Url, amqp.Config{Heartbeat: time.Duration(c.cfg.Heartbeat) * time.Second})
	if err != nil {
		return errors.Wrap(err, "amqp: 连接 RabbitMQ 失败")
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	closeCh := conn.NotifyClose(make(chan *amqp.Error, 1))
	go func() {
		select {
		case <-c.ctx.Done():
		case e := <-closeCh:
			if c.ctx.Err() != nil {
				return
			}
			logger.Warn(context.Background()).Interface("reason", e).Msg("RabbitMQ 连接断开，开始重连")
			c.reconnect()
		}
	}()
	return nil
}

func (c *Client) reconnect() {
	backoff := minBackoff
	for {
		jitter := time.Duration(rand.Int64N(int64(time.Second)))
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff + jitter):
		}
		err := c.connect()
		if err == nil {
			logger.Warn(context.Background()).Msg("RabbitMQ 重连成功")
			return
		}
		logger.Warn(context.Background()).Str("next", backoff.String()).Err(err).Msg("RabbitMQ 重连失败")
		backoff = min(time.Duration(float64(backoff)*1.5), maxBackoff)
	}
}

// IsConnected 是否已连接
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn != nil && !c.conn.IsClosed()
}

func (c *Client) channel() (*amqp.Channel, error) {
	if c.ctx.Err() != nil {
		return nil, ErrNotConnected
	}
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil || conn.IsClosed() {
		return nil, ErrNotConnected
	}
	ch, err := conn.Channel()
	return ch, errors.Wrap(err, "amqp: 创建通道失败")
}

func exchangeKind(r Route) string {
	if r.ExchangeKind == "" {
		return ExchangeDirect
	}
	return r.ExchangeKind
}

// declare 声明交换机；Queue 不为空时声明队列并绑定
func declare(ch *amqp.Channel, r Route) error {
	if r.Exchange != "" {
		if err := ch.ExchangeDeclare(r.Exchange, exchangeKind(r), r.Durable, false, false, false, nil); err != nil {
			return errors.Wrapf(err, "amqp: 声明交换机 %s 失败", r.Exchange)
		}
	}
	if r.Queue == "" {
		return nil
	}
	if _, err := ch.QueueDeclare(r.Queue, r.Durable, false, false, false, nil); err != nil {
		return errors.Wrapf(err, "amqp: 声明队列 %s 失败", r.Queue)
	}
	if r.Exchange == "" {
		return nil
	}
	return errors.Wrapf(ch.QueueBind(r.Queue, r.RoutingKey, r.Exchange, false, nil),
		"amqp: 绑定队列 %s 到交换机 %s 失败", r.Queue, r.Exchange)
}

// Close 关闭连接，停止重连与订阅
func (c *Client) Close() {
	c.cancel()
	c.pubMu.Lock()
	if c.pubCh != nil {
		_ = c.pubCh.Close()
		c.pubCh = nil
	}
	c.pubMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}
