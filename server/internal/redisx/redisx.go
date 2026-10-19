// Package redisx 封装 nonce 防重放、滑动窗口限流与短期票据（方案 11.8、12.2）。
package redisx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type Client struct{ rdb *redis.Client }

func New(url string) (*Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	return &Client{rdb: redis.NewClient(opt)}, nil
}

func (c *Client) Ping(ctx context.Context) error { return c.rdb.Ping(ctx).Err() }

// ClaimNonce 返回 true 表示首次出现；已存在返回 false（40106）。
func (c *Client) ClaimNonce(ctx context.Context, deviceID int64, nonce string, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, fmt.Sprintf("nonce:%d:%s", deviceID, nonce), 1, ttl).Result()
}

// Allow 滑动窗口限流：window 内最多 limit 次。返回是否放行与建议 Retry-After。
// 调用方决定 Redis 故障时 fail-open 还是 fail-closed。
func (c *Client) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	now := time.Now()
	k := "rl:" + key
	pipe := c.rdb.TxPipeline()
	pipe.ZRemRangeByScore(ctx, k, "0", strconv.FormatInt(now.Add(-window).UnixMicro(), 10))
	pipe.ZAdd(ctx, k, redis.Z{Score: float64(now.UnixMicro()), Member: uuid.NewString()})
	count := pipe.ZCard(ctx, k)
	oldest := pipe.ZRangeWithScores(ctx, k, 0, 0)
	pipe.Expire(ctx, k, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, err
	}
	if int(count.Val()) <= limit {
		return true, 0, nil
	}
	retry := window
	if zs := oldest.Val(); len(zs) == 1 {
		retry = time.Duration(int64(zs[0].Score)-now.Add(-window).UnixMicro()) * time.Microsecond
	}
	return false, retry, nil
}

// PutTicket / TakeTicket：一次性短期票据（恢复设备已满时的 restore_ticket，5 分钟）。
func (c *Client) PutTicket(ctx context.Context, kind string, payload string, ttl time.Duration) (string, error) {
	id := uuid.NewString()
	if err := c.rdb.Set(ctx, "ticket:"+kind+":"+id, payload, ttl).Err(); err != nil {
		return "", err
	}
	return id, nil
}

var ErrTicketNotFound = errors.New("ticket not found")

// ErrMissing 表示键不存在。调用方据此返回「验证已过期」，不要当成 Redis 故障。
var ErrMissing = errors.New("redis key missing")

// SetEX 写入带 TTL 的字符串。网页注册挑战用它暂存，不放业务库。
func (c *Client) SetEX(ctx context.Context, key, value string, ttl time.Duration) error {
	if c == nil || c.rdb == nil || key == "" || ttl <= 0 {
		return errors.New("redis unavailable")
	}
	return c.rdb.Set(ctx, key, value, ttl).Err()
}

// Get 读取字符串。键不存在时返回 ErrMissing。
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	if c == nil || c.rdb == nil || key == "" {
		return "", errors.New("redis unavailable")
	}
	v, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrMissing
	}
	return v, err
}

// Del 删除键。键已经不在也视为成功。
func (c *Client) Del(ctx context.Context, key string) error {
	if c == nil || c.rdb == nil || key == "" {
		return errors.New("redis unavailable")
	}
	return c.rdb.Del(ctx, key).Err()
}

// WindowCount 返回滑动窗口里已经记下的次数，不新增。
func (c *Client) WindowCount(ctx context.Context, key string, window time.Duration) (int64, error) {
	if c == nil || c.rdb == nil || key == "" || window <= 0 {
		return 0, errors.New("redis unavailable")
	}
	now := time.Now()
	k := "rl:" + key
	pipe := c.rdb.TxPipeline()
	pipe.ZRemRangeByScore(ctx, k, "0", strconv.FormatInt(now.Add(-window).UnixMicro(), 10))
	count := pipe.ZCard(ctx, k)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return count.Val(), nil
}

// AddWindow 往滑动窗口记一次，并返回记完后的次数。
func (c *Client) AddWindow(ctx context.Context, key string, window time.Duration) (int64, error) {
	if c == nil || c.rdb == nil || key == "" || window <= 0 {
		return 0, errors.New("redis unavailable")
	}
	now := time.Now()
	k := "rl:" + key
	pipe := c.rdb.TxPipeline()
	pipe.ZRemRangeByScore(ctx, k, "0", strconv.FormatInt(now.Add(-window).UnixMicro(), 10))
	pipe.ZAdd(ctx, k, redis.Z{Score: float64(now.UnixMicro()), Member: uuid.NewString()})
	count := pipe.ZCard(ctx, k)
	pipe.Expire(ctx, k, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return count.Val(), nil
}

func (c *Client) TakeTicket(ctx context.Context, kind, id string) (string, error) {
	v, err := c.rdb.GetDel(ctx, "ticket:"+kind+":"+id).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrTicketNotFound
	}
	return v, err
}
