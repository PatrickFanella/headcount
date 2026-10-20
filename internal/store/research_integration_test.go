package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Runs all real migrations against an isolated database on a caller-selected
// test server. Never points this at the production server.
func TestResearchEvidenceIntegration(t *testing.T) {
	raw := os.Getenv("HEADCOUNT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("HEADCOUNT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("headcount_test_%d", time.Now().UnixNano())
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
	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Minute).Add(-4 * time.Hour)
	end := at.Add(3 * time.Hour)
	_, err = s.Pool.Exec(ctx, `INSERT INTO channels(id,login,display_name,first_seen_at) VALUES(1,'test','Test',$1);
 `, at)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO streams(id,channel_id,started_at,first_seen_at,last_seen_at,ended_at) VALUES(10,1,$1,$1,$2,$2)`, at, end)
	if err != nil {
		t.Fatal(err)
	}
	h := true
	minutes := []ChatMinute{{ChannelID: 1, StreamID: 10, Messages: 4, UniqueChatters: 3, Healthy: &h, Commands: 1}}
	authors := []ChatterMinute{{StreamID: 10, UserID: 2, ChannelID: 1, Messages: 2, Commands: 1, GapBins: [4]int64{1, 0, 0, 0}}, {StreamID: 10, UserID: 3, ChannelID: 1, Messages: 1}, {StreamID: 10, UserID: 4, ChannelID: 1, Messages: 1}}
	if err := s.RecordChat(ctx, at, minutes, authors); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordChat(ctx, at, minutes, authors); err != nil {
		t.Fatal(err)
	}
	var msgs, active int
	if err := s.Pool.QueryRow(ctx, `SELECT messages,active_minutes FROM stream_chatters WHERE user_id=2`).Scan(&msgs, &active); err != nil {
		t.Fatal(err)
	}
	if msgs != 2 || active != 1 {
		t.Fatalf("retry double counted: %d %d", msgs, active)
	}
	created := at.Add(-24 * time.Hour)
	if err := s.InsertUsers(ctx, []TwitchUser{{ID: 2, Login: "young", CreatedAt: &created}, {ID: 3}}); err != nil {
		t.Fatal(err)
	}
	a, err := s.ChatterAccounts(ctx, 10, at, end, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if a.Total != 3 || a.Resolved != 1 || a.New != 1 || a.Unavailable != 1 || a.Pending != 1 {
		t.Fatalf("denominator lost pending authors: %+v", a)
	}
	if err := s.InsertUsers(ctx, []TwitchUser{{ID: 2}}); err != nil {
		t.Fatal(err)
	}
	var date *time.Time
	var omissions int
	if err := s.Pool.QueryRow(ctx, `SELECT created_at,omission_count FROM twitch_users WHERE id=2`).Scan(&date, &omissions); err != nil {
		t.Fatal(err)
	}
	if date == nil || !date.Equal(created) || omissions != 1 {
		t.Fatal("omission erased creation history")
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE twitch_users SET next_check_at=now()-interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueUsers(ctx, 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("bounded refresh: %v %v", due, err)
	}
	unknown, err := s.UnknownUsers(ctx, []int64{2, 4})
	if err != nil || len(unknown) != 2 {
		t.Fatalf("due cache never refreshed: %v %v", unknown, err)
	}
	if err := s.InsertUsers(ctx, []TwitchUser{{ID: 3, Login: "recovered", CreatedAt: &created}}); err != nil {
		t.Fatal(err)
	}
	behavior, err := s.BehaviorStats(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if behavior["gap_under_1s"] != 1 {
		t.Fatalf("gap summaries lost: %v", behavior)
	}
	if err := s.InsertEvents(ctx, []Event{{TS: at.Add(-time.Minute), ChannelID: 1, StreamID: 10, Kind: "chat_settings", Detail: map[string]any{"follower_mode": true}}, {TS: at, ChannelID: 1, StreamID: 10, Kind: "shared_chat", Detail: map[string]any{"active": false}}}); err != nil {
		t.Fatal(err)
	}
	ref := StreamRef{ID: 10, ChannelID: 1, StartedAt: at, FirstSeenAt: at, LastSeenAt: end, Ended: true}
	series, err := s.LoadSeries(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Chat) != 1 || series.Chat[0].Healthy == nil || !*series.Chat[0].Healthy || len(series.Events) != 2 {
		t.Fatalf("series evidence mismatch: %+v", series)
	}
	if _, err := s.ChatPeerStats(ctx, at.Add(-7*24*time.Hour), at, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SharedYoungChatters(ctx, 10, 1, at.Add(-90*24*time.Hour), at.Add(-7*24*time.Hour), end); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDistributionContext(ctx, 10, at.Add(30*time.Minute), at.Add(60*time.Minute), "promotion", "synthetic fixture"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDistributionContext(ctx, 10, at.Add(-time.Hour), end, "promotion", "fixture"); err == nil {
		t.Fatal("out of bounds context accepted")
	}
	series, err = s.LoadSeries(ctx, StreamRef{ID: 10, ChannelID: 1, StartedAt: at, FirstSeenAt: at.Add(45 * time.Minute), LastSeenAt: end})
	if err != nil {
		t.Fatal(err)
	}
	foundContext := false
	for _, e := range series.Events {
		if e.Kind == "distribution_context" {
			foundContext = true
		}
	}
	if !foundContext {
		t.Fatal("context starting before observation window lost")
	}
	sc := Score{StreamID: 10, ChannelID: 1, LastSeenAt: end, Version: 9, Minutes: 180, Quotient: 30, Confidence: 0.5, Final: true, Components: map[string]any{}, Evidence: map[string]string{"status": "observed_anomalies"}, SnapshotInput: map[string]int{"schema": 1}, SnapshotOutput: map[string]int{"version": 9}, ObservedAt: time.Now().UTC()}
	if err := s.SaveScore(ctx, sc); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveScore(ctx, sc); err != nil {
		t.Fatal(err)
	}
	var n int
	var evidence []byte
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM score_snapshots`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("snapshot overwritten/duplicated: %d", n)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT evidence FROM stream_scores WHERE stream_id=10`).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if json.Unmarshal(evidence, &ev) != nil || ev["status"] != "observed_anomalies" {
		t.Fatal("evidence missing")
	}
	different := sc
	different.SnapshotInput = map[string]int{"schema": 2}
	if err := s.SaveScore(ctx, different); err == nil {
		t.Fatal("conflicting evidence reused a snapshot identity")
	}
	var allowed bool
	if err := s.Pool.QueryRow(ctx, `SELECT has_table_privilege('susq_api','score_snapshots','SELECT') OR has_table_privilege('susq_api','user_observations','SELECT')`).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("private research data granted to public API role")
	}
	if err := s.ReadSnapshot(ctx, func(view *Store, _ time.Time) error {
		before, err := view.ChatterAccounts(ctx, 10, at, end, 30*24*time.Hour)
		if err != nil {
			return err
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE twitch_users SET missing=false WHERE id=2`); err != nil {
			return err
		}
		after, err := view.ChatterAccounts(ctx, 10, at, end, 30*24*time.Hour)
		if err != nil {
			return err
		}
		if before.Unavailable != after.Unavailable {
			t.Fatal("evidence reads crossed database snapshots")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateChannelQuotient(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PruneChatters(ctx, time.Now().UTC(), 30*24*time.Hour, 1); err != nil {
		t.Fatal(err)
	}
	a, err = s.ChatterAccounts(ctx, 10, at, end, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if a.Total != 3 || a.Resolved != 2 || a.Pending != 1 || !a.CoverageKnown {
		t.Fatalf("retention denominator drift: %+v", a)
	}
	if err := s.PruneObservations(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
}
