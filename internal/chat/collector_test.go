package chat

import (
	"git.subcult.tv/subculture-collective/headcount/internal/live"
	twitch "github.com/gempir/go-twitch-irc/v4"
	"log/slog"
	"testing"
	"time"
)

func TestReceivingNeedsTrafficAllMinute(t *testing.T) {
	minute := time.Now().Truncate(time.Minute).Add(-time.Minute)
	now := minute.Add(time.Minute)

	cn := &conn{}
	cn.connectedAt.Store(minute.Add(-time.Hour).UnixNano())
	cn.lastRx.Store(now.Add(-5 * time.Second).UnixNano())
	if !cn.receiving(minute, now) {
		t.Error("healthy connection not receiving")
	}

	// A 40-second silence inside the minute (e.g. a stalled reconnect).
	cn.maxGap.Store(int64(40 * time.Second))
	if cn.receiving(minute, now) {
		t.Error("connection with a long gap counted as receiving")
	}
	if cn.receiving(minute, now) != true {
		t.Error("gap tracker not reset after flush")
	}

	// Nothing received for the last 30 seconds.
	cn.lastRx.Store(now.Add(-30 * time.Second).UnixNano())
	if cn.receiving(minute, now) {
		t.Error("silent tail counted as receiving")
	}

	// Connected during the minute.
	cn.lastRx.Store(now.Add(-time.Second).UnixNano())
	cn.connectedAt.Store(minute.Add(10 * time.Second).UnixNano())
	if cn.receiving(minute, now) {
		t.Error("connection opened mid-minute counted as receiving")
	}
}

func TestDuplicateAndDelayedMessagesDoNotInflateActivity(t *testing.T) {
	c := New(live.New(), nil, nil, slog.Default())
	now := time.Now().Truncate(time.Minute).Add(20 * time.Second)
	m := twitch.PrivateMessage{ID: "id", RoomID: "1", User: twitch.User{ID: "2"}, Message: "hello", Time: now}
	c.onMessage(m, now)
	c.onMessage(m, now.Add(time.Second))
	if a := c.aggs[1]; a.messages != 1 || a.duplicates != 1 {
		t.Fatalf("duplicate counted: %+v", a)
	}
	m.ID = "late"
	m.Time = now.Add(-2 * time.Minute)
	c.onMessage(m, now)
	if a := c.aggs[1]; a.messages != 1 || a.delayed != 1 {
		t.Fatalf("late message counted as current: %+v", a)
	}
}
