package token_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	cacheredis "github.com/awp0816/common/cache/redis"
	"github.com/awp0816/common/logger"
	"github.com/awp0816/common/redis"
	"github.com/awp0816/common/token"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pkg/errors"
)

type session struct {
	Username string `json:"uname"`
	Root     bool   `json:"root,omitempty"`
}

func newStore(t *testing.T) *cacheredis.Store {
	t.Helper()
	_ = logger.Init(logger.Config{DisableFile: true})
	mr := miniredis.RunT(t)
	r, err := redis.New(redis.Config{Addresses: []string{mr.Addr()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return cacheredis.NewStore(r)
}

func TestIssueVerifyRevoke(t *testing.T) {
	ctx := context.Background()
	m, err := token.New[session](token.Config{Secret: "s", Issuer: "test"}, newStore(t))
	if err != nil {
		t.Fatal(err)
	}
	tok, c, err := m.Issue(ctx, "1", session{Username: "alice", Root: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID() != "1" || c.TokenID() == "" || time.Until(c.ExpireAt()) < 119*time.Minute {
		t.Fatalf("claims: %+v", c)
	}
	got, renewed, err := m.Verify(ctx, tok)
	if err != nil || renewed != "" || got.Data.Username != "alice" || !got.Data.Root {
		t.Fatalf("verify: %+v %q %v", got, renewed, err)
	}

	if err = m.Revoke(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.Verify(ctx, tok); !errors.Is(err, token.ErrRevoked) {
		t.Fatalf("want ErrRevoked, got %v", err)
	}
}

func TestRevokeUser(t *testing.T) {
	ctx := context.Background()
	m, _ := token.New[session](token.Config{Secret: "s"}, newStore(t))
	t1, _, _ := m.Issue(ctx, "1", session{})
	t2, _, _ := m.Issue(ctx, "1", session{})
	other, _, _ := m.Issue(ctx, "2", session{})
	if ids, _ := m.UserTokens(ctx, "1"); len(ids) != 2 {
		t.Fatalf("user tokens: %v", ids)
	}

	if err := m.RevokeUser(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{t1, t2} {
		if _, _, err := m.Verify(ctx, tok); !errors.Is(err, token.ErrRevoked) {
			t.Fatalf("want ErrRevoked, got %v", err)
		}
	}
	if _, _, err := m.Verify(ctx, other); err != nil {
		t.Fatalf("other user should not be affected: %v", err)
	}
}

// Buffer 大于有效期：每次校验都续期；同一 token 只续期一次，旧 token 在宽限期内仍有效
func TestRenew(t *testing.T) {
	ctx := context.Background()
	m, _ := token.New[session](token.Config{Secret: "s", Expires: 10, Buffer: 20, Grace: 30}, newStore(t))
	old, _, _ := m.Issue(ctx, "1", session{Username: "alice"})

	_, renewed, err := m.Verify(ctx, old)
	if err != nil || renewed == "" {
		t.Fatalf("should renew: %q %v", renewed, err)
	}
	if _, again, _ := m.Verify(ctx, old); again != "" {
		t.Fatal("same token should renew only once")
	}
	c, _, err := m.Verify(ctx, renewed)
	if err != nil || c.Data.Username != "alice" || c.UserID() != "1" {
		t.Fatalf("renewed token: %+v %v", c, err)
	}
}

func TestParseErrors(t *testing.T) {
	m, _ := token.New[session](token.Config{Secret: "s", Issuer: "a"}, nil)

	if _, err := m.Parse("bad"); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	// 其它密钥签发
	other, _ := token.New[session](token.Config{Secret: "x", Issuer: "a"}, nil)
	tok, _, _ := other.Issue(context.Background(), "1", session{})
	if _, err := m.Parse(tok); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("wrong secret: want ErrInvalid, got %v", err)
	}
	// 签发者不一致
	otherIss, _ := token.New[session](token.Config{Secret: "s", Issuer: "b"}, nil)
	tok, _, _ = otherIss.Issue(context.Background(), "1", session{})
	if _, err := m.Parse(tok); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("wrong issuer: want ErrInvalid, got %v", err)
	}
	// 已过期
	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, token.Claims[session]{RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "a", Subject: "1", ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
	}})
	tok, _ = expired.SignedString([]byte("s"))
	if _, err := m.Parse(tok); !errors.Is(err, token.ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
	// 算法不一致(none)
	none := jwt.NewWithClaims(jwt.SigningMethodNone, token.Claims[session]{RegisteredClaims: jwt.RegisteredClaims{Issuer: "a"}})
	tok, _ = none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := m.Parse(tok); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("alg none: want ErrInvalid, got %v", err)
	}
}

// 无 Store：无状态 JWT，校验只看签名与有效期，Revoke 不报错
func TestStateless(t *testing.T) {
	ctx := context.Background()
	m, _ := token.New[session](token.Config{Secret: "s"}, nil)
	tok, c, _ := m.Issue(ctx, "1", session{})
	if err := m.Revoke(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Verify(ctx, tok); err != nil {
		t.Fatalf("stateless verify: %v", err)
	}
}

func TestNewErrors(t *testing.T) {
	if _, err := token.New[session](token.Config{}, nil); err == nil {
		t.Fatal("empty secret should fail")
	}
	if _, err := token.New[session](token.Config{Secret: "s", Method: "RS256"}, nil); err == nil {
		t.Fatal("unsupported method should fail")
	}
}
