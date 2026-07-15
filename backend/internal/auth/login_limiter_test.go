package auth

import (
	"context"
	"testing"
	"time"
)

func TestLoginLimiterBlocksAfterConfiguredFailures(t *testing.T) {
	limiter := NewLoginLimiter(nil, 3, time.Minute)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		allowed, _, err := limiter.AllowFailure(ctx, "127.0.0.1", "admin")
		if err != nil {
			t.Fatal(err)
		}
		if !allowed {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	allowed, retryAfter, err := limiter.AllowFailure(ctx, "127.0.0.1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if allowed || retryAfter <= 0 {
		t.Fatalf("fourth failure should be blocked, allowed=%v retry=%v", allowed, retryAfter)
	}
}

func TestLoginLimiterResetClearsAccountAndSourceFailures(t *testing.T) {
	limiter := NewLoginLimiter(nil, 1, time.Minute)
	ctx := context.Background()
	allowed, _, _ := limiter.AllowFailure(ctx, "127.0.0.1", "admin")
	if !allowed {
		t.Fatal("first failure should be allowed")
	}
	limiter.Reset(ctx, "127.0.0.1", "admin")
	allowed, _, _ = limiter.AllowFailure(ctx, "127.0.0.1", "admin")
	if !allowed {
		t.Fatal("failure after reset should be allowed")
	}
}
