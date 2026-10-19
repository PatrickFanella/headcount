package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type StreamRef struct {
	ID          int64
	ChannelID   int64
	Language    string
	StartedAt   time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	Ended       bool
}

// StreamsToScore returns finished streams that have no final score yet, or
// whose final score came from an older scoring version.
func (s *Store) StreamsToScore(ctx context.Context, endedBefore time.Time, version, limit int) ([]StreamRef, error) {
	return s.streamRefs(ctx, `
		SELECT s.id, s.channel_id, coalesce(s.language, ''), s.started_at, s.first_seen_at, s.last_seen_at, true FROM streams s
		LEFT JOIN stream_scores sc ON sc.stream_id = s.id
		WHERE s.ended_at IS NOT NULL AND s.ended_at < $1
		  AND (s.scored_at IS NULL OR sc.version < $2)
		ORDER BY s.ended_at LIMIT $3`, endedBefore, version, limit)
}

// LiveStreamsToScore returns open streams observed for at least minMinutes.
func (s *Store) LiveStreamsToScore(ctx context.Context, minMinutes int) ([]StreamRef, error) {
	return s.streamRefs(ctx, `
		SELECT id, channel_id, coalesce(language, ''), started_at, first_seen_at, last_seen_at, false FROM streams
		WHERE ended_at IS NULL AND last_seen_at - first_seen_at >= make_interval(mins => $1)`, minMinutes)
}

func (s *Store) streamRefs(ctx context.Context, sql string, args ...any) ([]StreamRef, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StreamRef
	for rows.Next() {
		var r StreamRef
		if err := rows.Scan(&r.ID, &r.ChannelID, &r.Language, &r.StartedAt, &r.FirstSeenAt, &r.LastSeenAt, &r.Ended); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type ViewerPoint struct {
	TS      time.Time
	Viewers int
	GameID  string
}

type ChatPoint struct {
	TS       time.Time
	Messages int
	Chatters int
	TopText  int
}

type FollowerPoint struct {
	TS    time.Time
	Total int64
}

type EventPoint struct {
	TS     time.Time
	Kind   string
	Detail map[string]any
}

type StreamSeries struct {
	Viewers   []ViewerPoint
	Chat      []ChatPoint
	Followers []FollowerPoint
	Events    []EventPoint
}

// LoadSeries reads everything recorded for a stream. Events include the
// latest chat_mode before the stream so the starting chat mode is known.
func (s *Store) LoadSeries(ctx context.Context, r StreamRef) (StreamSeries, error) {
	var out StreamSeries
	from, to := r.FirstSeenAt, r.LastSeenAt
	rows, err := s.Pool.Query(ctx, `SELECT ts, viewers, coalesce(game_id, '') FROM viewer_samples
		WHERE channel_id = $1 AND stream_id = $2 AND ts BETWEEN $3 AND $4 ORDER BY ts`, r.ChannelID, r.ID, from, to)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p ViewerPoint
		if err := rows.Scan(&p.TS, &p.Viewers, &p.GameID); err != nil {
			rows.Close()
			return out, err
		}
		out.Viewers = append(out.Viewers, p)
	}
	rows.Close()

	rows, err = s.Pool.Query(ctx, `SELECT ts, messages, unique_chatters, top_text_count FROM chat_minutes
		WHERE channel_id = $1 AND ts BETWEEN $2 AND $3 ORDER BY ts`, r.ChannelID, from, to)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p ChatPoint
		if err := rows.Scan(&p.TS, &p.Messages, &p.Chatters, &p.TopText); err != nil {
			rows.Close()
			return out, err
		}
		out.Chat = append(out.Chat, p)
	}
	rows.Close()

	rows, err = s.Pool.Query(ctx, `SELECT ts, total FROM follower_samples
		WHERE channel_id = $1 AND ts BETWEEN $2 AND $3 ORDER BY ts`, r.ChannelID, from, to)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p FollowerPoint
		if err := rows.Scan(&p.TS, &p.Total); err != nil {
			rows.Close()
			return out, err
		}
		out.Followers = append(out.Followers, p)
	}
	rows.Close()

	rows, err = s.Pool.Query(ctx, `
		(SELECT ts, kind, detail FROM channel_events WHERE channel_id = $1 AND kind = 'chat_mode' AND ts < $2
		 ORDER BY ts DESC LIMIT 1)
		UNION ALL
		(SELECT ts, kind, detail FROM channel_events WHERE channel_id = $1 AND ts BETWEEN $2 AND $3)
		ORDER BY ts`, r.ChannelID, from, to.Add(time.Minute))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var p EventPoint
		var raw []byte
		if err := rows.Scan(&p.TS, &p.Kind, &raw); err != nil {
			return out, err
		}
		_ = json.Unmarshal(raw, &p.Detail)
		out.Events = append(out.Events, p)
	}
	return out, rows.Err()
}

// ChatterAccounts counts a stream's chatters with known accounts, those
// created within newWithin of the stream start, and missing accounts.
func (s *Store) ChatterAccounts(ctx context.Context, streamID int64, startedAt time.Time, newWithin time.Duration) (total, newAccounts, missing int, err error) {
	err = s.Pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE u.created_at > $2),
		       count(*) FILTER (WHERE u.missing)
		FROM stream_chatters sc JOIN twitch_users u ON u.id = sc.user_id
		WHERE sc.stream_id = $1`, streamID, startedAt.Add(-newWithin)).Scan(&total, &newAccounts, &missing)
	if err != nil || total > 0 {
		return
	}
	// Chatter rows of older streams are pruned after summarizing.
	err = s.Pool.QueryRow(ctx, `
		SELECT total, new_accounts, missing FROM stream_chatter_summary WHERE stream_id = $1`,
		streamID).Scan(&total, &newAccounts, &missing)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return
}

type PeerStat struct {
	Language      string
	GameID        string // category with the most viewer minutes
	GameName      string
	MedianViewers float64
	ChattersPerK  float64
}

// ChatPeerStats returns per-stream median viewers and median chatters per
// 1000 viewers over the recent window, for the engagement baseline.
func (s *Store) ChatPeerStats(ctx context.Context, since time.Time) ([]PeerStat, error) {
	rows, err := s.Pool.Query(ctx, `
		WITH per AS (
			SELECT coalesce(s.language, '') AS lang,
			       coalesce(mode() WITHIN GROUP (ORDER BY v.game_id), '') AS game_id,
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY v.viewers) AS viewers,
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY c.unique_chatters::float8 * 1000 / v.viewers) AS per_k
			FROM viewer_samples v
			JOIN chat_minutes c ON c.channel_id = v.channel_id AND c.ts = v.ts
			JOIN streams s ON s.id = v.stream_id
			WHERE v.ts > $1 AND v.viewers >= 100
			GROUP BY v.stream_id, s.language
			HAVING count(*) >= 30
		)
		SELECT per.lang, per.game_id, coalesce(g.game_name, ''), per.viewers, per.per_k
		FROM per LEFT JOIN LATERAL (
			SELECT game_name FROM streams
			WHERE game_id = per.game_id AND game_name IS NOT NULL
			ORDER BY last_seen_at DESC LIMIT 1) g ON true`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerStat
	for rows.Next() {
		var p PeerStat
		if err := rows.Scan(&p.Language, &p.GameID, &p.GameName, &p.MedianViewers, &p.ChattersPerK); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type Score struct {
	StreamID   int64
	ChannelID  int64
	LastSeenAt time.Time // the stream's last_seen_at when it was loaded
	Version    int
	Minutes    int
	Quotient   float64
	Confidence float64
	Final      bool
	Components any
}

func (s *Store) SaveScore(ctx context.Context, sc Score) error {
	comp, err := json.Marshal(sc.Components)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if sc.Final {
		// The stream may have reopened (or received new samples) since it was
		// loaded; then this score is only provisional.
		tag, err := tx.Exec(ctx, `UPDATE streams SET scored_at = now()
			WHERE id = $1 AND ended_at IS NOT NULL AND last_seen_at = $2`, sc.StreamID, sc.LastSeenAt)
		if err != nil {
			return err
		}
		sc.Final = tag.RowsAffected() == 1
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO stream_scores (stream_id, channel_id, scored_at, version, minutes, quotient, confidence, components, final)
		VALUES ($1, $2, now(), $3, $4, $5, $6, $7, $8)
		ON CONFLICT (stream_id) DO UPDATE SET scored_at = now(), version = $3, minutes = $4, quotient = $5,
			confidence = $6, components = $7, final = $8`,
		sc.StreamID, sc.ChannelID, sc.Version, sc.Minutes, sc.Quotient, sc.Confidence, comp, sc.Final); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateChannelQuotient recomputes a channel's quotient from its last ten
// stream scores, weighting each by confidence and recency. A live stream's
// provisional score counts as the most recent.
func (s *Store) UpdateChannelQuotient(ctx context.Context, channelID int64) error {
	_, err := s.Pool.Exec(ctx, `
		WITH recent AS (
			SELECT quotient, confidence, row_number() OVER (ORDER BY s.started_at DESC) - 1 AS i
			FROM stream_scores sc JOIN streams s ON s.id = sc.stream_id
			WHERE sc.channel_id = $1
			ORDER BY s.started_at DESC LIMIT 10
		), agg AS (
			SELECT sum(quotient * confidence * power(0.85, i)) AS num,
			       sum(confidence * power(0.85, i)) AS den,
			       sum(confidence) AS conf
			FROM recent
		)
		UPDATE channels SET
			-- The prior acts like half a full-confidence stream scoring 0, so a
			-- channel seen once at low confidence cannot top the leaderboard.
			quotient = CASE WHEN agg.den > 0 THEN agg.num / (agg.den + 0.5) END,
			quotient_confidence = LEAST(1, coalesce(agg.conf, 0) / 3),
			quotient_updated_at = now()
		FROM agg WHERE channels.id = $1`, channelID)
	return err
}

// ApplyTiers demotes channels that have stayed out of the top list for a week
// and repeatedly scored low, and restores cold channels whose quotient rose.
func (s *Store) ApplyTiers(ctx context.Context, lowQuotient, restoreQuotient float64) (demoted, restored int64, err error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE channels c SET tier = 'cold'
		WHERE tier = 'tracked' AND last_top_at < now() - interval '7 days'
		  AND quotient < $1 AND quotient_confidence >= 0.5
		  AND (SELECT count(*) FROM stream_scores sc WHERE sc.channel_id = c.id AND sc.final) >= 5`, lowQuotient)
	if err != nil {
		return 0, 0, err
	}
	demoted = tag.RowsAffected()
	tag, err = s.Pool.Exec(ctx, `UPDATE channels SET tier = 'tracked' WHERE tier = 'cold' AND quotient >= $1`, restoreQuotient)
	if err != nil {
		return demoted, 0, err
	}
	return demoted, tag.RowsAffected(), nil
}

type Onset struct {
	TS  time.Time
	Rel float64
}

// ReplaceOnsets stores a stream's spike onsets, replacing earlier ones.
func (s *Store) ReplaceOnsets(ctx context.Context, streamID, channelID int64, onsets []Onset) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM spike_onsets WHERE stream_id = $1`, streamID); err != nil {
		return err
	}
	if len(onsets) > 0 {
		ts := make([]time.Time, len(onsets))
		rel := make([]float32, len(onsets))
		for i, o := range onsets {
			ts[i], rel[i] = o.TS, float32(o.Rel)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO spike_onsets (stream_id, channel_id, ts, rel)
			SELECT $1, $2, ts, rel FROM unnest($3::timestamptz[], $4::real[]) AS u(ts, rel)
			ON CONFLICT DO NOTHING`, streamID, channelID, ts, rel); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type PartnerOnsets struct {
	ChannelID      int64
	Login          string
	OverlapMinutes int
	Onsets         []time.Time
}

// PartnerOnsets returns, for every other channel live during [from, to], its
// spike onsets in that window and how many minutes its streams overlapped.
func (s *Store) PartnerOnsets(ctx context.Context, channelID int64, from, to time.Time) ([]PartnerOnsets, error) {
	rows, err := s.Pool.Query(ctx, `
		WITH overlap AS (
			SELECT s.channel_id,
			       sum(extract(epoch FROM LEAST(s.last_seen_at, $3) - GREATEST(s.first_seen_at, $2)) / 60)::int AS minutes
			FROM streams s
			WHERE s.channel_id <> $1 AND s.first_seen_at < $3 AND s.last_seen_at > $2
			GROUP BY s.channel_id
		)
		SELECT o.channel_id, c.login, o.minutes,
		       coalesce((SELECT array_agg(so.ts ORDER BY so.ts) FROM spike_onsets so
		                 WHERE so.channel_id = o.channel_id AND so.ts BETWEEN $2 AND $3), '{}')
		FROM overlap o JOIN channels c ON c.id = o.channel_id
		WHERE o.minutes > 0`, channelID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PartnerOnsets
	for rows.Next() {
		var p PartnerOnsets
		if err := rows.Scan(&p.ChannelID, &p.Login, &p.OverlapMinutes, &p.Onsets); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
