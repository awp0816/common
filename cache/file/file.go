// Package file 本地文件缓存(基于 go.etcd.io/bbolt)，实现 cache.Cache 接口，与 cache/go-cache、cache/redis 行为一致。
// 数据保存在单个文件中，服务重启后仍在；只在本机有效，多实例之间不共享。
//   - 每次写入都落盘(事务提交)，进程崩溃不丢已写入的数据；写入比本地内存慢(毫秒级)，读取很快
//   - 同一个文件同时只能被一个进程打开，同机部署多个实例时需配置不同的路径
//   - 过期数据读取时视为不存在，后台每分钟清理一次
//   - 用完调用 Close 释放文件
package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/awp0816/common/cache/internal/errs"

	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
)

var bucket = []byte("cache")

// 值类型
const (
	kindString = 0
	kindSet    = 1
)

// entry 文件中保存的一条数据
type entry struct {
	Kind   int      `json:"k"`
	Exp    int64    `json:"e,omitempty"` // 过期时间(Unix 毫秒)，0 表示不过期
	Str    string   `json:"s,omitempty"`
	Member []string `json:"m,omitempty"`
}

func (e *entry) expired(now time.Time) bool { return e.Exp > 0 && now.UnixMilli() >= e.Exp }

func expireAt(ttl time.Duration) int64 {
	if ttl <= 0 {
		return 0
	}
	return time.Now().Add(ttl).UnixMilli()
}

// File 本地文件缓存，并发安全
type File struct {
	db   *bolt.DB
	path string
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Open 打开(不存在时创建)缓存文件，并启动过期数据清理
func Open(path string) (*File, error) {
	if path == "" {
		path = "data/cache.db"
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, errors.Wrapf(err, "cache: 创建缓存文件目录 %s 失败", dir)
		}
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		return nil, errors.Wrapf(err, "cache: 打开缓存文件 %s 失败(文件可能被其它进程占用)", path)
	}
	if err = db.Update(func(tx *bolt.Tx) error {
		_, e := tx.CreateBucketIfNotExists(bucket)
		return e
	}); err != nil {
		_ = db.Close()
		return nil, errors.Wrapf(err, "cache: 初始化缓存文件 %s 失败", path)
	}
	f := &File{db: db, path: path, stop: make(chan struct{}), done: make(chan struct{})}
	go f.cleanupLoop(time.Minute)
	return f, nil
}

// Close 停止清理并关闭文件，重复调用无效
func (f *File) Close() error {
	var err error
	f.once.Do(func() {
		close(f.stop)
		<-f.done
		err = errors.Wrapf(f.db.Close(), "cache: 关闭缓存文件 %s 失败", f.path)
	})
	return err
}

// load 读取未过期的数据，不存在或已过期返回 nil
func load(b *bolt.Bucket, key string) (*entry, error) {
	v := b.Get([]byte(key))
	if v == nil {
		return nil, nil
	}
	var e entry
	if err := json.Unmarshal(v, &e); err != nil {
		return nil, errors.Wrapf(err, "cache: 缓存文件中 %s 的数据已损坏", key)
	}
	if e.expired(time.Now()) {
		return nil, nil
	}
	return &e, nil
}

func save(b *bolt.Bucket, key string, e *entry) error {
	v, err := json.Marshal(e)
	if err != nil {
		return errors.Wrapf(err, "cache: %s 序列化失败", key)
	}
	return b.Put([]byte(key), v)
}

func wrongType(op, key string) error {
	return errors.Wrapf(errs.ErrWrongType, "cache: 文件缓存 %s %s 失败", op, key)
}

// view / update 在只读 / 读写事务中操作缓存桶
func (f *File) view(op, key string, fn func(b *bolt.Bucket) error) error {
	return f.wrap(f.db.View(func(tx *bolt.Tx) error { return fn(tx.Bucket(bucket)) }), op, key)
}

func (f *File) update(op, key string, fn func(b *bolt.Bucket) error) error {
	return f.wrap(f.db.Update(func(tx *bolt.Tx) error { return fn(tx.Bucket(bucket)) }), op, key)
}

// wrap 哨兵错误(未命中、类型不符)原样返回，其它错误说明操作与 key
func (f *File) wrap(err error, op, key string) error {
	if err == nil || errors.Is(err, errs.ErrMiss) || errors.Is(err, errs.ErrWrongType) {
		return err
	}
	return errors.Wrapf(err, "cache: 文件缓存 %s %s 失败", op, key)
}

func (f *File) Get(_ context.Context, key string) (string, error) {
	var s string
	err := f.view("GET", key, func(b *bolt.Bucket) error {
		e, err := load(b, key)
		switch {
		case err != nil:
			return err
		case e == nil:
			return errs.ErrMiss
		case e.Kind != kindString:
			return wrongType("GET", key)
		}
		s = e.Str
		return nil
	})
	return s, err
}

func (f *File) Set(_ context.Context, key, value string, ttl time.Duration) error {
	return f.update("SET", key, func(b *bolt.Bucket) error {
		return save(b, key, &entry{Kind: kindString, Str: value, Exp: expireAt(ttl)})
	})
}

func (f *File) SetNX(_ context.Context, key, value string, ttl time.Duration) (bool, error) {
	ok := false
	err := f.update("SETNX", key, func(b *bolt.Bucket) error {
		e, err := load(b, key)
		if err != nil || e != nil {
			return err
		}
		ok = true
		return save(b, key, &entry{Kind: kindString, Str: value, Exp: expireAt(ttl)})
	})
	return ok, err
}

func (f *File) Del(_ context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return f.update("DEL", keys[0], func(b *bolt.Bucket) error {
		for _, k := range keys {
			if err := b.Delete([]byte(k)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (f *File) Exists(_ context.Context, key string) (bool, error) {
	ok := false
	err := f.view("EXISTS", key, func(b *bolt.Bucket) error {
		e, err := load(b, key)
		ok = e != nil
		return err
	})
	return ok, err
}

func (f *File) Expire(_ context.Context, key string, ttl time.Duration) error {
	return f.update("EXPIRE", key, func(b *bolt.Bucket) error {
		e, err := load(b, key)
		if err != nil || e == nil {
			return err
		}
		e.Exp = expireAt(ttl)
		return save(b, key, e)
	})
}

func (f *File) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	var n int64
	err := f.update("INCR", key, func(b *bolt.Bucket) error {
		e, err := load(b, key)
		if err != nil {
			return err
		}
		if e == nil {
			n = 1
			return save(b, key, &entry{Kind: kindString, Str: "1", Exp: expireAt(ttl)})
		}
		if e.Kind != kindString {
			return wrongType("INCR", key)
		}
		if n, err = strconv.ParseInt(e.Str, 10, 64); err != nil {
			return wrongType("INCR", key)
		}
		n++
		e.Str = strconv.FormatInt(n, 10)
		return save(b, key, e)
	})
	return n, err
}

func (f *File) SAdd(_ context.Context, key string, ttl time.Duration, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	return f.update("SADD", key, func(b *bolt.Bucket) error {
		e, err := load(b, key)
		if err != nil {
			return err
		}
		if e == nil {
			e = &entry{Kind: kindSet}
		} else if e.Kind != kindSet {
			return wrongType("SADD", key)
		}
		for _, m := range members {
			if !slices.Contains(e.Member, m) {
				e.Member = append(e.Member, m)
			}
		}
		if ttl > 0 {
			e.Exp = expireAt(ttl)
		}
		return save(b, key, e)
	})
}

func (f *File) SMembers(_ context.Context, key string) ([]string, error) {
	var out []string
	err := f.view("SMEMBERS", key, func(b *bolt.Bucket) error {
		e, err := load(b, key)
		switch {
		case err != nil || e == nil:
			return err
		case e.Kind != kindSet:
			return wrongType("SMEMBERS", key)
		}
		out = slices.Clone(e.Member)
		slices.Sort(out)
		return nil
	})
	if out == nil && err == nil {
		out = []string{}
	}
	return out, err
}

func (f *File) SRem(_ context.Context, key string, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	return f.update("SREM", key, func(b *bolt.Bucket) error {
		e, err := load(b, key)
		switch {
		case err != nil || e == nil:
			return err
		case e.Kind != kindSet:
			return wrongType("SREM", key)
		}
		e.Member = slices.DeleteFunc(e.Member, func(m string) bool { return slices.Contains(members, m) })
		if len(e.Member) == 0 {
			return b.Delete([]byte(key))
		}
		return save(b, key, e)
	})
}

// cleanupLoop 定时删除过期数据
func (f *File) cleanupLoop(interval time.Duration) {
	defer close(f.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-t.C:
			_ = f.DeleteExpired()
		}
	}
}

// DeleteExpired 立即删除全部过期数据(后台每分钟自动执行一次)
func (f *File) DeleteExpired() error {
	now := time.Now()
	return f.update("清理过期数据", "", func(b *bolt.Bucket) error {
		var expired [][]byte
		err := b.ForEach(func(k, v []byte) error {
			var e entry
			if json.Unmarshal(v, &e) != nil || e.expired(now) { // 损坏的数据一并删除
				expired = append(expired, slices.Clone(k))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range expired {
			if err = b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}
