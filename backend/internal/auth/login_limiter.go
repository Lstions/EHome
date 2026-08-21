package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type loginBucket struct {
	count     int
	expiresAt time.Time
}

// LoginLimiter implements a bounded in-process sliding-window rate limiter for
// login failures. Redis 退役 (方案 v3.4 §4 任务B): 原 redis 加速/共享态分支已删除，
// 固定 memory 实现（单主体场景内存桶足够；重启清零可接受——锁定窗口仅 15 分钟）。
type LoginLimiter struct {
	maxFailures int
	window      time.Duration
	mu          sync.Mutex
	memory      map[string]loginBucket
}

func NewLoginLimiter(maxFailures int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{maxFailures: maxFailures, window: window, memory: make(map[string]loginBucket)}
}

// AllowFailure records a failed attempt. The attempt that reaches the limit is
// allowed; subsequent attempts are blocked until the window expires.
func (l *LoginLimiter) AllowFailure(ctx context.Context, sourceIP, username string) (bool, time.Duration, error) {
	keys := limiterKeys(sourceIP, username)
	return l.recordMemory(keys)
}

func (l *LoginLimiter) Reset(ctx context.Context, sourceIP, username string) {
	keys := limiterKeys(sourceIP, username)
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

func (l *LoginLimiter) String() string {
	return fmt.Sprintf("LoginLimiter(max=%d, window=%s)", l.maxFailures, l.window)
}
