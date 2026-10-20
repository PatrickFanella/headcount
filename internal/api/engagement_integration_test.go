package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/store"
	"git.subcult.tv/subculture-collective/headcount/web"
	"github.com/jackc/pgx/v5"
)

// The map must retain measurements when the scorer has no independent peers.
func TestEngagementWithoutScorerBaseline(t *testing.T) {
	raw := os.Getenv("HEADCOUNT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("HEADCOUNT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := store.Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("headcount_map_test_%d", time.Now().UnixNano())
	if _, err := admin.Pool.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Pool.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = st.Pool.Exec(ctx, `
 INSERT INTO channels(id,login,display_name,first_seen_at) VALUES(1,'example','Example',now());
 INSERT INTO streams(id,channel_id,started_at,first_seen_at,last_seen_at) SELECT i,1,now()-interval '2 hours',now()-interval '2 hours',now() FROM generate_series(1,3) i;
 INSERT INTO stream_scores(stream_id,channel_id,scored_at,version,minutes,quotient,confidence,components)
 SELECT i,1,now(),9,60,0,0.5,'{"chat_engagement":{"available":false,"reason":"no independent past peers"}}'::jsonb FROM generate_series(1,3) i;
 INSERT INTO stream_peer_metrics(stream_id,metric_version,source_last_seen,language,game_id,game_name,median_viewers,chatters_per_k)
 VALUES(1,1,now(),'en','1','Example',500,12), (2,99,now(),'en','1','Example',900,3);
 `)
	if err != nil {
		t.Fatal(err)
	}
	s := New(st.Pool, web.FS(), slog.New(slog.DiscardHandler))
	w := get(t, s.Handler(), "/api/engagement?days=7")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var data struct {
		Points []engagementPoint        `json:"points"`
		Fits   map[string]engagementFit `json:"fits"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Points) != 1 {
		t.Fatalf("want only supported measurements, got %+v", data.Points)
	}
	p := data.Points[0]
	if p.StreamID != "1" || p.MedianViewers != 500 || p.ChattersPerK != 12 || p.ExpectedPerK != nil || p.Z != nil {
		t.Fatalf("measurement or unavailable comparison changed: %+v", p)
	}
	if len(data.Fits) != 0 {
		t.Fatal("small sample invented a reference fit")
	}
}
