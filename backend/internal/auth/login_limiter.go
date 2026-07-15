package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	redisclient "github.com/redis/go-redis/v9"
)

type loginBucket struct {
	count     int
	expiresAt time.Time
}

type LoginLimiter struct {
	redis       *redisclient.Client
	maxFailures int
	window      time.Duration
	mu          sync.Mutex
	memory      map[string]loginBucket
}

func NewLoginLimiter(client *redisclient.Client, maxFailures int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{redis: client, maxFailures: maxFailures, window: window, memory: make(map[string]loginBucket)}
}

// AllowFailure records a failed attempt. The attempt that reaches the limit is
// allowed; subsequent attempts are blocked until the window expires.
func (l *LoginLimiter) AllowFailure(ctx context.Context, sourceIP, username string) (bool, time.Duration, error) {
	keys := limiterKeys(sourceIP, username)
	if l.redis != nil {
		return l.recordRedis(ctx, keys)
	}
	return l.recordMemory(keys)
}

func (l *LoginLimiter) Reset(ctx context.Context, sourceIP, username string) {
	keys := limiterKeys(sourceIP, username)
	if l.redis != nil {
		_ = l.redis.Del(ctx, keys...).Err()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		delete(l.memory, key)
	}
}

func limiterKeys(sourceIP, username string) []string {
	username = strings.ToLower(strings.TrimSpace(username))
	return []string{
		"auth:login:ip:" + sourceIP,
		"auth:login:user:" + username,
		"auth:login:pair:" + sourceIP + ":" + username,
	}
}

func (l *LoginLimiter) recordMemory(keys []string) (bool, time.Duration, error) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		bucket := l.memory[key]
		if now.Before(bucket.expiresAt) && bucket.count >= l.maxFailures {
			return false, time.Until(bucket.expiresAt), nil
		}
	}
	for _, key := range keys {
		bucket := l.memory[key]
		if !now.Before(bucket.expiresAt) {
			bucket = loginBucket{expiresAt: now.Add(l.window)}
		}
		bucket.count++
		l.memory[key] = bucket
	}
	return true, 0, nil
}

func (l *LoginLimiter) recordRedis(ctx context.Context, keys []string) (bool, time.Duration, error) {
	for _, key := range keys {
		count, err := l.redis.Get(ctx, key).Int()
		if err != nil && err != redisclient.Nil {
			// Redis is an acceleration/shared-state layer. Fall back to the bounded
			// in-process limiter rather than failing open.
			return l.recordMemory(keys)
		}
		if count >= l.maxFailures {
			ttl, _ := l.redis.TTL(ctx, key).Result()
			if ttl <= 0 {
				ttl = l.window
			}
			return false, ttl, nil
		}
	}
	pipe := l.redis.TxPipeline()
	for _, key := range keys {
		pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, l.window)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return l.recordMemory(keys)
	}
	return true, 0, nil
}

func (l *LoginLimiter) String() string {
	return fmt.Sprintf("LoginLimiter(max=%d, window=%s)", l.maxFailures, l.window)
}
