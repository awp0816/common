// Package token 登录令牌：JWT 签发/校验/滑动续期 + Redis 白名单(可撤销、踢下线)
//
//	type Session struct{ Username string; RoleKey string }           // 项目自定义的载荷
//	m, err := token.New[Session](cfg, cacheredis.NewStore(r))         // store 为 nil 时为无状态 JWT(不可撤销)
//	tok, claims, err := m.Issue(ctx, userID, Session{...})
//	claims, renewed, err := m.Verify(ctx, tok)                        // renewed 不为空时通过响应头下发新 token
//	err = m.RevokeUser(ctx, userID)                                   // 停用、改密时踢下线
package token

import (
	"context"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

var (
	// ErrInvalid token 格式、签名、签发者不正确
	ErrInvalid = errors.New("token: invalid")
	// ErrExpired token 已过期
	ErrExpired = errors.New("token: expired")
	// ErrRevoked token 已注销(不在白名单中)
	ErrRevoked = errors.New("token: revoked")
)

// Config 令牌配置
type Config struct {
	// Secret 签名密钥(必填)，建议 32 位以上随机字符串
	Secret string `json:"-" yaml:"secret" mapstructure:"secret"`
	// Method 签名算法：HS256(默认) | HS384 | HS512
	Method string `json:"method" yaml:"method" mapstructure:"method"`
	// Issuer 签发者，校验时必须一致
	Issuer string `json:"issuer" yaml:"issuer" mapstructure:"issuer"`
	// Expires 有效期(分钟)，默认 120
	Expires int `json:"expires" yaml:"expires" mapstructure:"expires"`
	// Buffer 剩余有效期小于该值(分钟)时自动续期，0 不续期
	Buffer int `json:"buffer" yaml:"buffer" mapstructure:"buffer"`
	// Grace 续期后旧 token 的宽限期(秒)，避免并发请求失败，默认 30
	Grace int `json:"grace" yaml:"grace" mapstructure:"grace"`
	// KeyPrefix 白名单在 Redis 中的 key 前缀，默认 token:
	KeyPrefix string `json:"keyPrefix" yaml:"keyPrefix" mapstructure:"keyPrefix"`
}

func (c Config) withDefaults() Config {
	if c.Expires <= 0 {
		c.Expires = 120
	}
	if c.Grace <= 0 {
		c.Grace = 30
	}
	if c.KeyPrefix == "" {
		c.KeyPrefix = "token:"
	}
	return c
}

func (c Config) expires() time.Duration { return time.Duration(c.Expires) * time.Minute }
func (c Config) buffer() time.Duration  { return time.Duration(c.Buffer) * time.Minute }
func (c Config) grace() time.Duration   { return time.Duration(c.Grace) * time.Second }

// Store 白名单存储，github.com/awp0816/common/cache/redis.Store 已实现
type Store interface {
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	Exists(ctx context.Context, key string) (bool, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
	SAdd(ctx context.Context, key string, ttl time.Duration, members ...string) error
	SMembers(ctx context.Context, key string) ([]string, error)
	SRem(ctx context.Context, key string, members ...string) error
}

// Claims 令牌载荷：Data 为项目自定义数据；Subject 为用户ID，ID(jti)为令牌ID
type Claims[T any] struct {
	Data T `json:"data"`
	jwt.RegisteredClaims
}

// UserID 用户ID
func (c *Claims[T]) UserID() string { return c.Subject }

// TokenID 令牌ID
func (c *Claims[T]) TokenID() string { return c.ID }

// ExpireAt 过期时间
func (c *Claims[T]) ExpireAt() time.Time {
	if c.ExpiresAt == nil {
		return time.Time{}
	}
	return c.ExpiresAt.Time
}

// Manager 签发/校验/续期/注销，并发安全
//   - 有 Store 时：签发后令牌ID写入白名单，注销/踢下线时删除，实现 JWT 可撤销
//   - 剩余有效期小于 Buffer 时自动续期，旧 token 保留 Grace 宽限期
type Manager[T any] struct {
	cfg    Config
	store  Store
	method jwt.SigningMethod
}

// New 创建令牌管理；store 为 nil 时不使用白名单(无法撤销，Revoke 系列方法直接返回 nil)
func New[T any](cfg Config, store Store) (*Manager[T], error) {
	if cfg.Secret == "" {
		return nil, errors.New("token: secret 不能为空")
	}
	cfg = cfg.withDefaults()
	var method jwt.SigningMethod
	switch strings.ToUpper(cfg.Method) {
	case "", "HS256":
		method = jwt.SigningMethodHS256
	case "HS384":
		method = jwt.SigningMethodHS384
	case "HS512":
		method = jwt.SigningMethodHS512
	default:
		return nil, errors.Errorf("token: 不支持的签名算法 %s，可选 HS256 | HS384 | HS512", cfg.Method)
	}
	return &Manager[T]{cfg: cfg, store: store, method: method}, nil
}

func (m *Manager[T]) tokenKey(id string) string    { return m.cfg.KeyPrefix + "jti:" + id }
func (m *Manager[T]) userKey(userID string) string { return m.cfg.KeyPrefix + "user:" + userID }

// Issue 签发新 token
func (m *Manager[T]) Issue(ctx context.Context, userID string, data T) (string, *Claims[T], error) {
	now := time.Now()
	c := &Claims[T]{Data: data, RegisteredClaims: jwt.RegisteredClaims{
		ID:        uuid.NewString(),
		Issuer:    m.cfg.Issuer,
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(m.cfg.expires())),
	}}
	tok, err := jwt.NewWithClaims(m.method, c).SignedString([]byte(m.cfg.Secret))
	if err != nil {
		return "", nil, errors.Wrapf(err, "token: 签发用户 %s 的令牌失败", userID)
	}
	if m.store != nil {
		ttl := m.cfg.expires()
		if err = m.store.Set(ctx, m.tokenKey(c.ID), userID, ttl); err != nil {
			return "", nil, errors.Wrapf(err, "token: 用户 %s 的令牌写入白名单失败", userID)
		}
		if err = m.store.SAdd(ctx, m.userKey(userID), ttl, c.ID); err != nil {
			return "", nil, errors.Wrapf(err, "token: 用户 %s 的令牌写入用户令牌集合失败", userID)
		}
	}
	return tok, c, nil
}

// Parse 只校验签名、签发者与有效期(不查白名单)；过期返回 ErrExpired，其它返回包装了原因的 ErrInvalid
func (m *Manager[T]) Parse(tokenStr string) (*Claims[T], error) {
	c := &Claims[T]{}
	_, err := jwt.ParseWithClaims(tokenStr, c, func(*jwt.Token) (any, error) {
		return []byte(m.cfg.Secret), nil
	}, jwt.WithValidMethods([]string{m.method.Alg()}), jwt.WithIssuer(m.cfg.Issuer), jwt.WithLeeway(5*time.Second))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpired
		}
		return nil, errors.Wrap(ErrInvalid, err.Error()) // 保留 errors.Is(err, ErrInvalid)，原因写入错误信息
	}
	return c, nil
}

// Verify 校验 token(签名 + 有效期 + 白名单)；需要续期时返回新 token(否则为空)，续期失败不影响本次校验结果
func (m *Manager[T]) Verify(ctx context.Context, tokenStr string) (*Claims[T], string, error) {
	c, err := m.Parse(tokenStr)
	if err != nil {
		return nil, "", err
	}
	if m.store != nil {
		ok, err := m.store.Exists(ctx, m.tokenKey(c.ID))
		if err != nil {
			return nil, "", errors.Wrapf(err, "token: 查询用户 %s 的令牌白名单失败", c.Subject)
		}
		if !ok {
			return nil, "", ErrRevoked
		}
	}
	if m.cfg.Buffer <= 0 || time.Until(c.ExpireAt()) > m.cfg.buffer() {
		return c, "", nil
	}
	// 续期：同一 token 并发请求只续期一次；旧 token 进入宽限期
	if m.store != nil {
		if locked, e := m.store.SetNX(ctx, m.tokenKey(c.ID)+":renew", "1", m.cfg.grace()); e != nil || !locked {
			return c, "", nil
		}
	}
	renewed, _, err := m.Issue(ctx, c.Subject, c.Data)
	if err != nil {
		return c, "", nil
	}
	if m.store != nil {
		_ = m.store.Expire(ctx, m.tokenKey(c.ID), m.cfg.grace())
	}
	return c, renewed, nil
}

// Revoke 注销 token(退出登录)
func (m *Manager[T]) Revoke(ctx context.Context, c *Claims[T]) error {
	return m.RevokeToken(ctx, c.Subject, c.ID)
}

// RevokeToken 注销用户的指定 token
func (m *Manager[T]) RevokeToken(ctx context.Context, userID, tokenID string) error {
	if m.store == nil {
		return nil
	}
	if err := m.store.Del(ctx, m.tokenKey(tokenID)); err != nil {
		return errors.Wrapf(err, "token: 注销用户 %s 的令牌失败", userID)
	}
	return errors.Wrapf(m.store.SRem(ctx, m.userKey(userID), tokenID), "token: 从用户 %s 的令牌集合中移除失败", userID)
}

// RevokeUser 注销用户的全部 token(停用、删除、改密、改权限时踢下线)
func (m *Manager[T]) RevokeUser(ctx context.Context, userID string) error {
	if m.store == nil {
		return nil
	}
	ids, err := m.store.SMembers(ctx, m.userKey(userID))
	if err != nil {
		return errors.Wrapf(err, "token: 查询用户 %s 的令牌失败", userID)
	}
	keys := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		keys = append(keys, m.tokenKey(id))
	}
	return errors.Wrapf(m.store.Del(ctx, append(keys, m.userKey(userID))...), "token: 注销用户 %s 的全部令牌失败", userID)
}

// UserTokens 用户当前有效的令牌ID(在线设备)；无 Store 时返回空
func (m *Manager[T]) UserTokens(ctx context.Context, userID string) ([]string, error) {
	if m.store == nil {
		return nil, nil
	}
	ids, err := m.store.SMembers(ctx, m.userKey(userID))
	if err != nil {
		return nil, errors.Wrapf(err, "token: 查询用户 %s 的令牌失败", userID)
	}
	out := ids[:0]
	for _, id := range ids {
		if ok, err := m.store.Exists(ctx, m.tokenKey(id)); err == nil && ok {
			out = append(out, id)
		}
	}
	return out, nil
}
