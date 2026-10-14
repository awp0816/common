// Package breaker 熔断器实现(三态：关闭 -> 打开 -> 半开)
//
//	关闭(Closed)   ：正常放行，统计窗口内的成功/失败次数；失败率或连续失败次数达到阈值时转为打开
//	打开(Open)     ：直接拒绝请求(快速失败，触发降级)；经过 Timeout 后转为半开
//	半开(HalfOpen) ：最多放行 MaxRequests 个探测请求；全部成功则恢复关闭，任一失败重新打开
package breaker

import (
	"sync"
	"time"

	"github.com/pkg/errors"
)

type State int

const (
	StateClosed State = iota
	StateHalfOpen
	StateOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half-open"
	case StateOpen:
		return "open"
	default:
		return "unknown"
	}
}

var (
	// ErrOpenState 熔断器处于打开状态
	ErrOpenState = errors.New("circuit breaker is open")
	// ErrTooManyRequests 半开状态下探测请求已满
	ErrTooManyRequests = errors.New("circuit breaker: too many requests in half-open state")
)

// Counts 统计窗口内的请求计数
type Counts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

func (c *Counts) onRequest() { c.Requests++ }

func (c *Counts) onSuccess() {
	c.TotalSuccesses++
	c.ConsecutiveSuccesses++
	c.ConsecutiveFailures = 0
}

func (c *Counts) onFailure() {
	c.TotalFailures++
	c.ConsecutiveFailures++
	c.ConsecutiveSuccesses = 0
}

func (c *Counts) clear() { *c = Counts{} }

// Settings 熔断器配置
type Settings struct {
	Name                string
	MaxRequests         uint32        // 半开状态最多放行的探测请求数，默认1
	Interval            time.Duration // 关闭状态下计数清零周期，0表示不清零
	Timeout             time.Duration // 打开状态持续时间，默认60s
	MinRequests         uint32        // 窗口内请求数达到该值才计算失败率
	FailureRatio        float64       // 失败率阈值(0~1)，0表示不按失败率熔断
	ConsecutiveFailures uint32        // 连续失败阈值，0表示不按连续失败熔断
	OnStateChange       func(name string, from, to State)
}

// Breaker 单个熔断器，并发安全
type Breaker struct {
	settings   Settings
	mu         sync.Mutex
	state      State
	generation uint64
	counts     Counts
	expiry     time.Time
}

func New(s Settings) *Breaker {
	if s.MaxRequests == 0 {
		s.MaxRequests = 1
	}
	if s.Timeout <= 0 {
		s.Timeout = 60 * time.Second
	}
	if s.FailureRatio <= 0 && s.ConsecutiveFailures == 0 {
		s.ConsecutiveFailures = 5
	}
	b := &Breaker{settings: s}
	b.toNewGeneration(time.Now())
	return b
}

func (b *Breaker) Name() string { return b.settings.Name }

// State 当前状态
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, _ := b.currentState(time.Now())
	return state
}

// Counts 当前窗口计数
func (b *Breaker) Counts() Counts {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts
}

// Allow 两段式调用：先申请放行，业务执行完后调用 done(成功与否)
// 适合在HTTP中间件等无法用闭包包裹的场景使用
func (b *Breaker) Allow() (done func(success bool), err error) {
	generation, err := b.beforeRequest()
	if err != nil {
		return nil, err
	}
	return func(success bool) { b.afterRequest(generation, success) }, nil
}

// Execute 执行 fn，fn 返回 error 视为失败；panic 也计为失败并继续抛出
func (b *Breaker) Execute(fn func() error) error {
	generation, err := b.beforeRequest()
	if err != nil {
		return err
	}
	defer func() {
		if e := recover(); e != nil {
			b.afterRequest(generation, false)
			panic(e)
		}
	}()
	err = fn()
	b.afterRequest(generation, err == nil)
	return err
}

func (b *Breaker) beforeRequest() (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	state, generation := b.currentState(now)
	if state == StateOpen {
		return generation, ErrOpenState
	}
	if state == StateHalfOpen && b.counts.Requests >= b.settings.MaxRequests {
		return generation, ErrTooManyRequests
	}
	b.counts.onRequest()
	return generation, nil
}

func (b *Breaker) afterRequest(before uint64, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	state, generation := b.currentState(now)
	// 状态已切换过(换代)，旧请求的结果丢弃
	if generation != before {
		return
	}
	if success {
		b.onSuccess(state, now)
	} else {
		b.onFailure(state, now)
	}
}

func (b *Breaker) onSuccess(state State, now time.Time) {
	b.counts.onSuccess()
	if state == StateHalfOpen && b.counts.ConsecutiveSuccesses >= b.settings.MaxRequests {
		b.setState(StateClosed, now)
	}
}

func (b *Breaker) onFailure(state State, now time.Time) {
	b.counts.onFailure()
	switch state {
	case StateClosed:
		if b.readyToTrip() {
			b.setState(StateOpen, now)
		}
	case StateHalfOpen:
		b.setState(StateOpen, now)
	}
}

func (b *Breaker) readyToTrip() bool {
	c := b.counts
	if b.settings.ConsecutiveFailures > 0 && c.ConsecutiveFailures >= b.settings.ConsecutiveFailures {
		return true
	}
	if b.settings.FailureRatio > 0 && c.Requests >= b.settings.MinRequests && c.Requests > 0 {
		return float64(c.TotalFailures)/float64(c.Requests) >= b.settings.FailureRatio
	}
	return false
}

func (b *Breaker) currentState(now time.Time) (State, uint64) {
	switch b.state {
	case StateClosed:
		if !b.expiry.IsZero() && b.expiry.Before(now) {
			b.toNewGeneration(now)
		}
	case StateOpen:
		if b.expiry.Before(now) {
			b.setState(StateHalfOpen, now)
		}
	}
	return b.state, b.generation
}

func (b *Breaker) setState(state State, now time.Time) {
	if b.state == state {
		return
	}
	prev := b.state
	b.state = state
	b.toNewGeneration(now)
	if b.settings.OnStateChange != nil {
		// 回调中可能有IO操作(日志/告警)，异步执行避免持锁阻塞
		go b.settings.OnStateChange(b.settings.Name, prev, state)
	}
}

func (b *Breaker) toNewGeneration(now time.Time) {
	b.generation++
	b.counts.clear()
	var zero time.Time
	switch b.state {
	case StateClosed:
		if b.settings.Interval == 0 {
			b.expiry = zero
		} else {
			b.expiry = now.Add(b.settings.Interval)
		}
	case StateOpen:
		b.expiry = now.Add(b.settings.Timeout)
	default:
		b.expiry = zero
	}
}

// Group 按名称管理一组熔断器(如按接口路由)，懒加载创建
type Group struct {
	mu       sync.RWMutex
	settings Settings
	items    map[string]*Breaker
}

func NewGroup(s Settings) *Group {
	return &Group{settings: s, items: make(map[string]*Breaker)}
}

// Get 获取指定名称的熔断器，不存在则创建
func (g *Group) Get(name string) *Breaker {
	g.mu.RLock()
	b, ok := g.items[name]
	g.mu.RUnlock()
	if ok {
		return b
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if b, ok = g.items[name]; ok {
		return b
	}
	s := g.settings
	s.Name = name
	b = New(s)
	g.items[name] = b
	return b
}

// Reset 使用新配置重建所有熔断器(配置热更新时调用)
func (g *Group) Reset(s Settings) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.settings = s
	g.items = make(map[string]*Breaker)
}

// Snapshot 所有熔断器当前状态，用于监控展示
func (g *Group) Snapshot() map[string]string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make(map[string]string, len(g.items))
	for name, b := range g.items {
		out[name] = b.State().String()
	}
	return out
}
