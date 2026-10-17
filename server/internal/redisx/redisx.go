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

func (c *Client) TakeTicket(ctx context.Context, kind, id string) (string, error) {
	v, err := c.rdb.GetDel(ctx, "ticket:"+kind+":"+id).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrTicketNotFound
	}
	return v, err
}
