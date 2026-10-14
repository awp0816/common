// Package redis Redis 连接：单机/哨兵/集群、连接池与超时、可用状态检测、命令失败自动记录日志
//
//	r, err := redis.New(cfg)
//	defer r.Close()
//	r.StartHealthCheck(5 * time.Second) // 定时 Ping，维护 IsConnected
//	r.Client().Get(ctx, "key")          // 原生 go-redis 客户端
//
// 命令执行失败只返回错误、不记录日志，由业务代码按需记录；健康检查在状态切换(不可用 / 已恢复)时各记一次日志
package redis

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"

	"github.com/awp0816/common/logger"

	goredis "github.com/redis/go-redis/v9"
)

// 部署模式
const (
	ModeSingle   = "single"   // 单机
	ModeSentinel = "sentinel" // 哨兵
	ModeCluster  = "cluster"  // 集群
)

// Config Redis 配置
type Config struct {
	// Mode 部署模式：single(默认) | sentinel | cluster
	Mode string `json:"mode" yaml:"mode" mapstructure:"mode"`
	// Addresses 地址列表 host:port；单机只取第一个
	Addresses []string `json:"addresses" yaml:"addresses" mapstructure:"addresses"`
	// Master 哨兵模式的主节点名称
	Master string `json:"master" yaml:"master" mapstructure:"master"`
	// Username ACL 用户名，可留空
	Username string `json:"username" yaml:"username" mapstructure:"username"`
	// Password 密码
	Password string `json:"-" yaml:"password" mapstructure:"password"`
	// DB 数据库编号(集群模式不支持)
	DB int `json:"db" yaml:"db" mapstructure:"db"`
	// PoolSize 连接池大小，0 为 go-redis 默认值(CPU 核数 * 10)
	PoolSize int `json:"poolSize" yaml:"poolSize" mapstructure:"poolSize"`
	// DialTimeout 连接超时(毫秒)，默认 3000
	DialTimeout int `json:"dialTimeout" yaml:"dialTimeout" mapstructure:"dialTimeout"`
	// ReadTimeout 读超时(毫秒)，默认 2000
	ReadTimeout int `json:"readTimeout" yaml:"readTimeout" mapstructure:"readTimeout"`
	// WriteTimeout 写超时(毫秒)，默认 2000
	WriteTimeout int `json:"writeTimeout" yaml:"writeTimeout" mapstructure:"writeTimeout"`
}

func ms(v, def int) time.Duration {
	if v <= 0 {
		v = def
	}
	return time.Duration(v) * time.Millisecond
}

// Redis 客户端封装
type Redis struct {
	client    goredis.UniversalClient
	connected atomic.Bool
	once      sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
}

// New 创建客户端并 Ping 测试连接
func New(cfg Config) (*Redis, error) {
	if len(cfg.Addresses) == 0 {
		return nil, errors.New("redis: addresses 不能为空")
	}
	opts := &goredis.UniversalOptions{
		Addrs:        cfg.Addresses,
		Username:     cfg.Username,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  ms(cfg.DialTimeout, 3000),
		ReadTimeout:  ms(cfg.ReadTimeout, 2000),
		WriteTimeout: ms(cfg.WriteTimeout, 2000),
	}
	switch strings.ToLower(cfg.Mode) {
	case "", ModeSingle:
		opts.Addrs = cfg.Addresses[:1]
	case ModeSentinel:
		if cfg.Master == "" {
			return nil, errors.New("redis: 哨兵模式需要配置 master")
		}
		opts.MasterName = cfg.Master
	case ModeCluster:
		opts.IsClusterMode = true
	default:
		return nil, errors.Errorf("redis: 不支持的部署模式 %s", cfg.Mode)
	}
	r := &Redis{client: goredis.NewUniversalClient(opts)}

	ctx, cancel := context.WithTimeout(context.Background(), opts.DialTimeout)
	defer cancel()
	if err := r.client.Ping(ctx).Err(); err != nil {
		_ = r.client.Close()
		return nil, errors.Wrap(err, "redis: 连接失败")
	}
	r.connected.Store(true)
	return r, nil
}

// Client 原生 go-redis 客户端
func (r *Redis) Client() goredis.UniversalClient { return r.client }

// IsConnected 是否可用：由 StartHealthCheck 维护，不发起请求；未启动检测时始终为 true(Close 后为 false)
func (r *Redis) IsConnected() bool { return r.connected.Load() }

// Ping 健康检查
func (r *Redis) Ping(ctx context.Context) error { return r.client.Ping(ctx).Err() }

// StartHealthCheck 每隔 interval(默认 5 秒)Ping 一次，维护可用状态；状态切换时记录日志。重复调用无效
func (r *Redis) StartHealthCheck(interval time.Duration) {
	r.once.Do(func() {
		if interval <= 0 {
			interval = 5 * time.Second
		}
		ctx, cancel := context.WithCancel(context.Background())
		r.cancel, r.done = cancel, make(chan struct{})
		go func() {
			defer close(r.done)
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					r.check(ctx, interval)
				}
			}
		}()
	})
}

func (r *Redis) check(ctx context.Context, interval time.Duration) {
	c, cancel := context.WithTimeout(ctx, min(interval, 2*time.Second))
	defer cancel()
	if err := r.client.Ping(c).Err(); err != nil {
		if ctx.Err() == nil && r.connected.Swap(false) {
			logger.Error(context.Background()).Err(err).Msg("Redis 不可用")
		}
		return
	}
	if !r.connected.Swap(true) {
		logger.Warn(context.Background()).Msg("Redis 已恢复")
	}
}

// Close 停止健康检查并关闭连接
func (r *Redis) Close() error {
	if r.cancel != nil {
		r.cancel()
		<-r.done
	}
	r.connected.Store(false)
	return r.client.Close()
}
