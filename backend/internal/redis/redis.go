package redis

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

var Client *redis.Client
var ctx = context.Background()

// Connect initializes Redis connection
func Connect(addr string) error {
	Client = redis.NewClient(&redis.Options{
		Addr:     addr,
		PoolSize: 10,
	})

	if err := Client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping failed: %w", err)
	}

	log.Println("Redis connected")
	return nil
}

// SetHeartbeat sets a collector heartbeat with TTL
func SetHeartbeat(deviceID string, ttl time.Duration) error {
	key := fmt.Sprintf("collector:heartbeat:%s", deviceID)
	return Client.Set(ctx, key, time.Now().Unix(), ttl).Err()
}

// GetHeartbeat gets the last heartbeat time for a collector
func GetHeartbeat(deviceID string) (int64, error) {
	key := fmt.Sprintf("collector:heartbeat:%s", deviceID)
	val, err := Client.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}

// IsOnline checks if a collector is online (heartbeat exists and not expired)
func IsOnline(deviceID string) bool {
	key := fmt.Sprintf("collector:heartbeat:%s", deviceID)
	ttl := Client.TTL(ctx, key).Val()
	return ttl > 0
}

// GetAllCollectors returns all collector IDs that have heartbeats using SCAN (production-safe)
func GetAllCollectors() ([]string, error) {
	var ids []string
	var cursor uint64
	pattern := "collector:heartbeat:*"

	for {
		keys, nextCursor, err := Client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if len(k) > len("collector:heartbeat:") {
				id := k[len("collector:heartbeat:"):]
				ids = append(ids, id)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return ids, nil
}

// DeleteHeartbeat removes a collector's heartbeat
func DeleteHeartbeat(deviceID string) error {
	key := fmt.Sprintf("collector:heartbeat:%s", deviceID)
	return Client.Del(ctx, key).Err()
}
