package api

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestLimiterRefills(t *testing.T) {
	l := newLimiter(60, 2, nil)
	a := netip.MustParseAddr("192.0.2.1")
	now := time.Now()
	for i := range 2 {
		if ok, _ := l.allow(a, now); !ok {
			t.Fatalf("request %d rejected within burst", i)
		}
	}
	if ok, wait := l.allow(a, now); ok || wait <= 0 {
		t.Fatalf("third request allowed=%v wait=%v, want rejection", ok, wait)
	}
	if ok, _ := l.allow(a, now.Add(time.Second)); !ok {
		t.Fatal("request after refill rejected")
	}
	if ok, _ := l.allow(netip.MustParseAddr("192.0.2.2"), now); !ok {
		t.Fatal("other client rejected")
	}
}

func TestClientIPTrustsOnlyProxies(t *testing.T) {
	trusted, err := ParsePrefixes("10.0.0.200, 172.16.0.0/12")
	if err != nil {
		t.Fatal(err)
	}
	l := newLimiter(60, 10, trusted)
	r := httptest.NewRequest("GET", "/api/overview", nil)
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.9")

	r.RemoteAddr = "203.0.113.50:1234"
	if got := l.clientIP(r); got.String() != "203.0.113.50" {
		t.Fatalf("untrusted peer: got %v", got)
	}
	r.RemoteAddr = "10.0.0.200:1234"
	if got := l.clientIP(r); got.String() != "203.0.113.9" {
		t.Fatalf("trusted peer: got %v, want last hop", got)
	}
}
