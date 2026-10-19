package store

import (
	"context"
	"encoding/json"
	"time"
)

type TopEntry struct {
	ID          int64
	Login       string
	DisplayName string
	Rank        int
}

// UpsertTopChannels records the current top list: new channels are inserted,
// and every listed channel becomes hot.
func (s *Store) UpsertTopChannels(ctx context.Context, t time.Time, top []TopEntry) error {
	ids := make([]int64, len(top))
	logins := make([]string, len(top))
	names := make([]string, len(top))
	ranks := make([]int32, len(top))
	for i, e := range top {
		ids[i], logins[i], names[i], ranks[i] = e.ID, e.Login, e.DisplayName, int32(e.Rank)
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO channels (id, login, display_name, tier, first_seen_at, last_top_at, best_rank, last_live_at)
		SELECT id, login, name, 'hot', $1, $1, rank, $1
		FROM unnest($2::bigint[], $3::text[], $4::text[], $5::int[]) AS u(id, login, name, rank)
		ON CONFLICT (id) DO UPDATE SET
			login = EXCLUDED.login,
			display_name = EXCLUDED.display_name,
			tier = 'hot',
			last_top_at = $1,
			last_live_at = $1,
			best_rank = LEAST(channels.best_rank, EXCLUDED.best_rank)`,
		t, ids, logins, names, ranks)
	return err
}

// SeedChannels adds channels to the tracked tier without a top-list
// appearance. Existing channels are left unchanged.
func (s *Store) SeedChannels(ctx context.Context, t time.Time, chans []TopEntry) (int64, error) {
	var added int64
	for _, c := range chans {
		tag, err := s.Pool.Exec(ctx, `
			INSERT INTO channels (id, login, display_name, tier, first_seen_at)
			VALUES ($1, $2, $3, 'tracked', $4) ON CONFLICT (id) DO NOTHING`, c.ID, c.Login, c.DisplayName, t)
		if err != nil {
			return added, err
		}
		added += tag.RowsAffected()
	}
	return added, nil
}

// DemoteHot moves channels that left the top list to the tracked tier.
func (s *Store) DemoteHot(ctx context.Context, t time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE channels SET tier = 'tracked' WHERE tier = 'hot' AND last_top_at < $1`, t)
	return err
}

type Channel struct {
	ID    int64
	Login string
	Tier  string
}

func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, login, tier FROM channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.Login, &c.Tier); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type OpenStream struct {
	ID        int64
	ChannelID int64
	Login     string
	LastSeen  time.Time
	Title     string
	GameID    string
	GameName  string
	Peak      int
}

func (s *Store) OpenStreams(ctx context.Context) ([]OpenStream, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT s.id, s.channel_id, c.login, s.last_seen_at, coalesce(s.title, ''), coalesce(s.game_id, ''),
		       coalesce(s.game_name, ''), s.peak_viewers
		FROM streams s JOIN channels c ON c.id = s.channel_id
		WHERE s.ended_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenStream
	for rows.Next() {
		var o OpenStream
		if err := rows.Scan(&o.ID, &o.ChannelID, &o.Login, &o.LastSeen, &o.Title, &o.GameID, &o.GameName, &o.Peak); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

type LiveSample struct {
	StreamID  int64
	ChannelID int64
	Login     string
	Display   string
	StartedAt time.Time
	Title     string
	GameID    string
	GameName  string
	Language  string
	Viewers   int
	Rank      int // 0 when outside the top list
}

// RecordLive upserts streams, updates channel liveness, and writes one viewer
// sample per live stream for minute t.
func (s *Store) RecordLive(ctx context.Context, t time.Time, live []LiveSample) error {
	if len(live) == 0 {
		return nil
	}
	n := len(live)
	var (
		streamIDs  = make([]int64, n)
		channelIDs = make([]int64, n)
		logins     = make([]string, n)
		displays   = make([]string, n)
		started    = make([]time.Time, n)
		titles     = make([]string, n)
		gameIDs    = make([]string, n)
		gameNames  = make([]string, n)
		langs      = make([]string, n)
		viewers    = make([]int32, n)
		ranks      = make([]*int16, n)
	)
	for i, l := range live {
		streamIDs[i], channelIDs[i], logins[i], displays[i] = l.StreamID, l.ChannelID, l.Login, l.Display
		started[i], titles[i], gameIDs[i], gameNames[i], langs[i] = l.StartedAt, l.Title, l.GameID, l.GameName, l.Language
		viewers[i] = int32(l.Viewers)
		if l.Rank > 0 {
			r := int16(l.Rank)
			ranks[i] = &r
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE channels c SET login = u.login, display_name = u.display, last_live_at = $1
		FROM unnest($2::bigint[], $3::text[], $4::text[]) AS u(id, login, display)
		WHERE c.id = u.id`, t, channelIDs, logins, displays); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO streams (id, channel_id, started_at, first_seen_at, last_seen_at, title, game_id, game_name, language, peak_viewers)
		SELECT sid, cid, st, $1, $1, title, gid, gname, lang, v
		FROM unnest($2::bigint[], $3::bigint[], $4::timestamptz[], $5::text[], $6::text[], $7::text[], $8::text[], $9::int[])
		     AS u(sid, cid, st, title, gid, gname, lang, v)
		ON CONFLICT (id) DO UPDATE SET
			last_seen_at = $1,
			ended_at = NULL,
			title = EXCLUDED.title,
			game_id = EXCLUDED.game_id,
			game_name = EXCLUDED.game_name,
			peak_viewers = GREATEST(streams.peak_viewers, EXCLUDED.peak_viewers)`,
		t, streamIDs, channelIDs, started, titles, gameIDs, gameNames, langs, viewers); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO viewer_samples (ts, channel_id, stream_id, viewers, game_id, rank)
		SELECT $1, cid, sid, v, gid, r
		FROM unnest($2::bigint[], $3::bigint[], $4::int[], $5::text[], $6::smallint[]) AS u(cid, sid, v, gid, r)
		ON CONFLICT DO NOTHING`,
		t, channelIDs, streamIDs, viewers, gameIDs, ranks); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) EndStream(ctx context.Context, streamID int64, endedAt time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE streams SET ended_at = $2 WHERE id = $1 AND ended_at IS NULL`, streamID, endedAt)
	return err
}

type Event struct {
	TS        time.Time
	ChannelID int64
	StreamID  int64 // 0 = unknown
	Kind      string
	Detail    map[string]any
}

func (s *Store) InsertEvents(ctx context.Context, events []Event) error {
	for _, e := range events {
		detail, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		var sid *int64
		if e.StreamID != 0 {
			sid = &e.StreamID
		}
		if _, err := s.Pool.Exec(ctx, `INSERT INTO channel_events (ts, channel_id, stream_id, kind, detail) VALUES ($1, $2, $3, $4, $5)`,
			e.TS, e.ChannelID, sid, e.Kind, detail); err != nil {
			return err
		}
	}
	return nil
}

type FollowerSample struct {
	ChannelID int64
	Total     int64
}

func (s *Store) InsertFollowerSamples(ctx context.Context, t time.Time, samples []FollowerSample) error {
	if len(samples) == 0 {
		return nil
	}
	ids := make([]int64, len(samples))
	totals := make([]int64, len(samples))
	for i, f := range samples {
		ids[i], totals[i] = f.ChannelID, f.Total
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO follower_samples (ts, channel_id, total)
		SELECT $1, id, total FROM unnest($2::bigint[], $3::bigint[]) AS u(id, total)
		ON CONFLICT DO NOTHING`, t, ids, totals)
	return err
}
