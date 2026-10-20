package helix

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
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

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestContextContractsAndUnknownSettings(t *testing.T) {
	c := New("app", "unused")
	c.token = "test"
	c.tokenExpiry = time.Now().Add(2 * time.Hour)
	body := `{"data":[{"follower_mode":true,"follower_mode_duration":10,"subscriber_mode":false,"emote_mode":false,"slow_mode":false}]}`
	c.http = &http.Client{Transport: responseTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("broadcaster_id") != "1" {
			t.Error("missing broadcaster ID")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	settings, err := c.ChatSettings(context.Background(), "1")
	if err != nil || !settings.FollowerMode || settings.FollowerMinutes == nil || *settings.FollowerMinutes != 10 {
		t.Fatalf("settings: %+v %v", settings, err)
	}
	body = `{"data":[]}`
	if _, err := c.ChatSettings(context.Background(), "1"); err == nil {
		t.Fatal("empty settings became unrestricted chat")
	}
	if session, err := c.SharedChat(context.Background(), "1"); err != nil || session != nil {
		t.Fatal("empty shared session not inactive")
	}
	body = `{"data":[{"session_id":"session","participants":[{"broadcaster_id":"1"},{"broadcaster_id":"2"}]}]}`
	session, err := c.SharedChat(context.Background(), "1")
	if err != nil || session.ID != "session" || len(session.Participants) != 2 {
		t.Fatalf("shared chat: %+v %v", session, err)
	}
}
