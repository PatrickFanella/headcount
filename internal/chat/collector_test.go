package chat

import (
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
