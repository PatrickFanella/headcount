package score

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestScoreSnapshotRoundTripIntegration(t *testing.T) {
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
	name := fmt.Sprintf("headcount_score_test_%d", time.Now().UnixNano())
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
	db, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Minute).Add(-4 * time.Hour)
	end := at.Add(179 * time.Minute)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO channels(id,login,display_name,first_seen_at) VALUES(1,'fixture','Fixture',$1)`, at)
	exec(`INSERT INTO streams(id,channel_id,started_at,first_seen_at,last_seen_at,ended_at,game_id,game_name,language) VALUES(10,1,$1,$1,$2,$2,'1','Fixture category','en')`, at, end)
	exec(`INSERT INTO viewer_samples(ts,channel_id,stream_id,viewers,game_id) SELECT $1::timestamptz+i*interval '1 minute',1,10,1000,'1' FROM generate_series(0,179) AS g(i)`, at)
	exec(`INSERT INTO chat_minutes(ts,channel_id,stream_id,messages,unique_chatters,new_chatters,first_msgs,emote_only,distinct_texts,top_text_count,bans,timeouts,healthy) SELECT $1::timestamptz+i*interval '1 minute',1,10,50,20,0,0,0,49,1,0,0,true FROM generate_series(0,179) AS g(i)`, at)
	exec(`INSERT INTO follower_samples(ts,channel_id,total) SELECT $1::timestamptz+i*interval '1 minute',1,10000+i*4 FROM generate_series(0,179) AS g(i)`, at)
	exec(`INSERT INTO stream_chatters(stream_id,user_id,channel_id,first_seen_at,last_seen_at,messages,active_minutes) SELECT 10,i,1,$1,$1,1,1 FROM generate_series(2,101) AS g(i)`, at)
	exec(`INSERT INTO twitch_users(id,created_at,missing,fetched_at) SELECT i,$1::timestamptz-interval '365 days',false,now() FROM generate_series(2,101) AS g(i)`, at)
	scorer := New(db, slog.Default())
	if err := scorer.scoreStream(ctx, store.StreamRef{ID: 10}); err != nil {
		t.Fatal(err)
	}
	var input, output []byte
	if err := db.Pool.QueryRow(ctx, `SELECT input,output FROM score_snapshots WHERE stream_id=10 ORDER BY observed_at LIMIT 1`).Scan(&input, &output); err != nil {
		t.Fatal(err)
	}
	var frozen Snapshot
	var original Result
	if err := json.Unmarshal(input, &frozen); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output, &original); err != nil {
		t.Fatal(err)
	}
	if replayed := Evaluate(frozen, nil); !reflect.DeepEqual(original, replayed) {
		t.Fatal("database snapshot failed exact semantic replay")
	}
	peers, err := db.ChatPeerStats(ctx, end.Add(-7*24*time.Hour), end.Add(time.Hour), 999)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].ChannelID != 1 {
		t.Fatalf("shared peer metric unavailable: %+v", peers)
	}
	bp, err := db.BehaviorPeers(ctx, store.StreamRef{StartedAt: end.Add(time.Hour), Language: "en"}, "1", 1000)
	if err != nil || len(bp) != 1 {
		t.Fatalf("behavior references: %+v %v", bp, err)
	}
	exec(`UPDATE chat_minutes SET top_text_count=50`)
	if err := scorer.scoreStream(ctx, store.StreamRef{ID: 10}); err != nil {
		t.Fatal(err)
	}
	if replayed := Evaluate(frozen, nil); !reflect.DeepEqual(original, replayed) {
		t.Fatal("later observations changed frozen replay")
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM score_snapshots WHERE stream_id=10`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected two immutable observations, got %d", n)
	}
}
