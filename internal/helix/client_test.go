package helix

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A drained bucket with no known reset time must schedule a refill instead of
// waiting forever for a rate-limit header.
func TestAcquireRecoversWithoutHeaders(t *testing.T) {
	c := New("id", "secret")
	c.remaining = 0

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.acquire(ctx, PriorityHigh); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire from empty bucket: %v, want deadline", err)
	}
	c.budgetMu.Lock()
	reset := c.resetAt
	c.budgetMu.Unlock()
	if until := time.Until(reset); until < 50*time.Second || until > time.Minute {
		t.Fatalf("reset scheduled in %v, want about a minute", until)
	}

	// Once the scheduled reset passes, the bucket refills.
	c.budgetMu.Lock()
	c.resetAt = time.Now().Add(-time.Second)
	c.budgetMu.Unlock()
	if err := c.acquire(context.Background(), PriorityLow); err != nil {
		t.Fatalf("acquire after reset: %v", err)
	}
}

func TestRefundCapsAtLimit(t *testing.T) {
	c := New("id", "secret")
	c.refund()
	if c.remaining != c.limit {
		t.Fatalf("remaining %d exceeds limit %d", c.remaining, c.limit)
	}
}
