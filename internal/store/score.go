package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	rows, err := s.reader().Query(ctx, sql, args...)
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
	TS                                                                              time.Time
	Messages                                                                        int
	Chatters                                                                        int
	TopText                                                                         int
	Healthy                                                                         *bool
	Commands, UtilityMessages, BotBadgeMessages, DuplicateMessages, DelayedMessages int
	SourceDelayMS                                                                   int64
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
	rows, err := s.reader().Query(ctx, `SELECT ts, viewers, coalesce(game_id, '') FROM viewer_samples
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
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	rows, err = s.reader().Query(ctx, `SELECT ts, messages, unique_chatters, top_text_count,healthy,commands,utility_messages,bot_badge_messages,duplicate_messages,delayed_messages,source_delay_ms FROM chat_minutes
		WHERE channel_id = $1 AND ts BETWEEN $2 AND $3 ORDER BY ts`, r.ChannelID, from, to)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p ChatPoint
		if err := rows.Scan(&p.TS, &p.Messages, &p.Chatters, &p.TopText, &p.Healthy, &p.Commands, &p.UtilityMessages, &p.BotBadgeMessages, &p.DuplicateMessages, &p.DelayedMessages, &p.SourceDelayMS); err != nil {
			rows.Close()
			return out, err
		}
		out.Chat = append(out.Chat, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	rows, err = s.reader().Query(ctx, `SELECT ts, total FROM follower_samples
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
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	rows, err = s.reader().Query(ctx, `
		(SELECT DISTINCT ON(kind) ts, kind, detail FROM channel_events WHERE channel_id = $1 AND kind IN ('chat_mode','chat_settings','shared_chat') AND ts < $2
		 ORDER BY kind,ts DESC)
  UNION ALL
  (SELECT ts,kind,detail FROM channel_events WHERE channel_id=$1 AND kind='distribution_context' AND ts<$2 AND (detail->>'until')::timestamptz>$2)
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

// AccountStats keeps all observed authors in the denominator. Unavailability
// describes a lookup, never the reason an account was omitted.
type AccountStats struct {
	Total         int  `json:"total"`
	Resolved      int  `json:"resolved"`
	New           int  `json:"new"`
	Unavailable   int  `json:"unavailable"`
	Pending       int  `json:"pending"`
	CoverageKnown bool `json:"coverage_known"`
}

func (a AccountStats) Coverage() float64 {
	if a.Total == 0 || !a.CoverageKnown {
		return 0
	}
	return float64(a.Resolved) / float64(a.Total)
}

func (s *Store) ChatterAccounts(ctx context.Context, streamID int64, startedAt, endedAt time.Time, newWithin time.Duration) (a AccountStats, err error) {
	a.CoverageKnown = true
	err = s.reader().QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE u.created_at IS NOT NULL),
 count(*) FILTER(WHERE u.created_at>$2 AND u.created_at<=$3 AND c.utility_messages<c.messages),count(*) FILTER(WHERE u.missing),count(*) FILTER(WHERE u.id IS NULL)
 FROM stream_chatters c LEFT JOIN twitch_users u ON u.id=c.user_id WHERE c.stream_id=$1`, streamID, startedAt.Add(-newWithin), endedAt).Scan(&a.Total, &a.Resolved, &a.New, &a.Unavailable, &a.Pending)
	if err != nil || a.Total > 0 {
		return
	}
	err = s.reader().QueryRow(ctx, `SELECT total,coalesce(resolved,0),new_accounts,missing,coverage_known,coalesce(pending,0) FROM stream_chatter_summary WHERE stream_id=$1`, streamID).Scan(&a.Total, &a.Resolved, &a.New, &a.Unavailable, &a.CoverageKnown, &a.Pending)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}

	return
}

type PeerStat struct {
	StreamID, ChannelID int64
	StartedAt, EndedAt  time.Time
	Language            string
	GameID              string // category with the most viewer minutes
	GameName            string
	MedianViewers       float64
	ChattersPerK        float64
}

// ChatPeerStats returns per-stream median viewers and median chatters per
// 1000 viewers over the recent window, for the engagement baseline.
func (s *Store) ChatPeerStats(ctx context.Context, since, before time.Time, exclude int64) ([]PeerStat, error) {
	rows, err := s.reader().Query(ctx, `
 SELECT DISTINCT ON(s.channel_id) s.id,s.channel_id,s.started_at,s.last_seen_at,
 p.language,p.game_id,p.game_name,p.median_viewers,p.chatters_per_k
 FROM stream_peer_metrics p JOIN streams s ON s.id=p.stream_id
 WHERE p.metric_version=1 AND s.channel_id<>$3 AND s.ended_at>$1 AND s.ended_at<$2
 AND p.source_last_seen=s.last_seen_at
 ORDER BY s.channel_id,s.last_seen_at DESC,s.id DESC`, since, before, exclude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerStat
	for rows.Next() {
		var p PeerStat
		if err := rows.Scan(&p.StreamID, &p.ChannelID, &p.StartedAt, &p.EndedAt, &p.Language, &p.GameID, &p.GameName, &p.MedianViewers, &p.ChattersPerK); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type Score struct {
	StreamID       int64
	ChannelID      int64
	LastSeenAt     time.Time // the stream's last_seen_at when it was loaded
	Version        int
	Minutes        int
	Quotient       float64
	Confidence     float64
	Final          bool
	Components     any
	Evidence       any
	SnapshotInput  any
	SnapshotOutput any
	ObservedAt     time.Time
	Peer           *PeerStat
	Behavior       map[string]float64
}

func (s *Store) SaveScore(ctx context.Context, sc Score) error {
	comp, err := json.Marshal(sc.Components)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(sc.Evidence)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if sc.Behavior != nil {
		raw, err := json.Marshal(sc.Behavior)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stream_behavior_metrics(stream_id,source_last_seen,metrics) VALUES($1,$2,$3) ON CONFLICT(stream_id) DO UPDATE SET source_last_seen=$2,metrics=$3`, sc.StreamID, sc.LastSeenAt, raw); err != nil {
			return err
		}
	}
	if sc.Peer != nil {
		p := sc.Peer
		if _, err := tx.Exec(ctx, `INSERT INTO stream_peer_metrics(stream_id,metric_version,source_last_seen,language,game_id,game_name,median_viewers,chatters_per_k)
 VALUES($1,1,$2,$3,$4,$5,$6,$7) ON CONFLICT(stream_id) DO UPDATE SET metric_version=1,source_last_seen=$2,language=$3,game_id=$4,game_name=$5,median_viewers=$6,chatters_per_k=$7`, sc.StreamID, sc.LastSeenAt, p.Language, p.GameID, p.GameName, p.MedianViewers, p.ChattersPerK); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `DELETE FROM stream_peer_metrics WHERE stream_id=$1`, sc.StreamID); err != nil {
			return err
		}
	}
	if sc.SnapshotInput != nil {
		input, err := json.Marshal(sc.SnapshotInput)
		if err != nil {
			return err
		}
		output, err := json.Marshal(sc.SnapshotOutput)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO score_snapshots (stream_id,observed_at,version,input,output) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, sc.StreamID, sc.ObservedAt, sc.Version, input, output)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var same bool
			if err := tx.QueryRow(ctx, `SELECT input=$4::jsonb AND output=$5::jsonb FROM score_snapshots WHERE stream_id=$1 AND observed_at=$2 AND version=$3`, sc.StreamID, sc.ObservedAt, sc.Version, input, output).Scan(&same); err != nil {
				return err
			}
			if !same {
				return fmt.Errorf("snapshot identity collision with different evidence")
			}
		}
	}
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
		INSERT INTO stream_scores (stream_id, channel_id, scored_at, version, minutes, quotient, confidence, components, final,evidence)
		VALUES ($1, $2, now(), $3, $4, $5, $6, $7, $8,$9)
		ON CONFLICT (stream_id) DO UPDATE SET scored_at = now(), version = $3, minutes = $4, quotient = $5,
			confidence = $6, components = $7, final = $8,evidence=$9`,
		sc.StreamID, sc.ChannelID, sc.Version, sc.Minutes, sc.Quotient, sc.Confidence, comp, sc.Final, evidence); err != nil {
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
func (s *Store) ReplaceOnsets(ctx context.Context, streamID, channelID int64, onsets []Onset, version int) error {
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
			INSERT INTO spike_onsets (stream_id, channel_id, ts, rel,version)
			SELECT $1, $2, ts, rel,$5 FROM unnest($3::timestamptz[], $4::real[]) AS u(ts, rel)
			ON CONFLICT DO NOTHING`, streamID, channelID, ts, rel, version); err != nil {
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
func (s *Store) PartnerOnsets(ctx context.Context, channelID int64, from, to time.Time, version int) ([]PartnerOnsets, error) {
	rows, err := s.reader().Query(ctx, `
		WITH overlap AS (
			SELECT s.channel_id,
			       sum(extract(epoch FROM LEAST(s.last_seen_at, $3) - GREATEST(s.first_seen_at, $2)) / 60)::int AS minutes
			FROM streams s
			WHERE s.channel_id <> $1 AND s.first_seen_at < $3 AND s.last_seen_at > $2
			GROUP BY s.channel_id
		)
		SELECT o.channel_id, c.login, o.minutes,
		       coalesce((SELECT array_agg(so.ts ORDER BY so.ts) FROM spike_onsets so
		                 WHERE so.channel_id = o.channel_id AND so.version=$4 AND so.ts BETWEEN $2 AND $3), '{}')
		FROM overlap o JOIN channels c ON c.id = o.channel_id
		WHERE o.minutes > 0`, channelID, from, to, version)
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

func (s *Store) StreamRef(ctx context.Context, id int64) (StreamRef, error) {
	rs, err := s.streamRefs(ctx, `SELECT id,channel_id,coalesce(language,''),started_at,first_seen_at,last_seen_at,ended_at IS NOT NULL FROM streams WHERE id=$1`, id)
	if err != nil {
		return StreamRef{}, err
	}
	if len(rs) != 1 {
		return StreamRef{}, pgx.ErrNoRows
	}
	return rs[0], nil
}
