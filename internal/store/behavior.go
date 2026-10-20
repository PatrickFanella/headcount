package store

import (
	"context"
	"encoding/json"
	"time"
)

// BehaviorStats are experimental diagnostics, deliberately absent from weights.
// No author IDs or text are copied into score snapshots.
func (s *Store) BehaviorStats(ctx context.Context, streamID int64) (map[string]float64, error) {
	var authors, resolved, cohort int
	var medianMessages, p95Messages, active, commands, utility, badge float64
	var gaps []int64
	err := s.reader().QueryRow(ctx, `WITH a AS (
 SELECT c.*,u.created_at FROM stream_chatters c LEFT JOIN twitch_users u ON u.id=c.user_id WHERE stream_id=$1
 ), cohorts AS (SELECT date_trunc('day',created_at,'UTC') AS day,count(*) n FROM a WHERE created_at IS NOT NULL GROUP BY 1)
 SELECT count(*),count(created_at),coalesce((SELECT max(n) FROM cohorts),0),
 coalesce(percentile_cont(0.5) WITHIN GROUP(ORDER BY messages),0),coalesce(percentile_cont(0.95) WITHIN GROUP(ORDER BY messages),0),
 coalesce(avg(active_minutes) FILTER(WHERE active_minutes>0),0),coalesce(sum(commands)::float8/nullif(sum(messages),0),0),
 coalesce(sum(utility_messages)::float8/nullif(sum(messages),0),0),coalesce(sum(bot_badge_messages)::float8/nullif(sum(messages),0),0),
 ARRAY[coalesce(sum(gap_bins[1]),0)::bigint,coalesce(sum(gap_bins[2]),0)::bigint,coalesce(sum(gap_bins[3]),0)::bigint,coalesce(sum(gap_bins[4]),0)::bigint] FROM a`, streamID).
		Scan(&authors, &resolved, &cohort, &medianMessages, &p95Messages, &active, &commands, &utility, &badge, &gaps)
	if err != nil {
		return nil, err
	}
	out := map[string]float64{"authors": float64(authors), "creation_dates": float64(resolved), "largest_creation_day": float64(cohort), "median_messages": medianMessages, "p95_messages": p95Messages, "mean_active_minutes": active, "command_share": commands, "utility_message_share": utility, "bot_badge_message_share": badge}
	if resolved > 0 {
		out["largest_creation_day_share"] = float64(cohort) / float64(resolved)
	}
	for i, k := range []string{"gap_under_1s", "gap_1_to_5s", "gap_5_to_15s", "gap_15s_plus"} {
		if i < len(gaps) {
			out[k] = float64(gaps[i])
		}
	}
	return out, nil
}

func (s *Store) PruneSnapshots(ctx context.Context, before time.Time) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM score_snapshots WHERE observed_at<$1`, before)
	return err
}
func (s *Store) PruneObservations(ctx context.Context, before time.Time) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM user_observations WHERE checked_at<$1`, before)
	return err
}

type BehaviorPeer struct {
	StreamID, ChannelID int64
	EndedAt             time.Time
	Metrics             map[string]float64
}

// BehaviorPeers are descriptive references, not negative bot labels. Comparable
// streams share language/category and an audience size within a factor of two.
func (s *Store) BehaviorPeers(ctx context.Context, r StreamRef, game string, viewers float64) ([]BehaviorPeer, error) {
	rows, err := s.reader().Query(ctx, `SELECT s.id,s.channel_id,s.last_seen_at,b.metrics
 FROM stream_behavior_metrics b JOIN streams s ON s.id=b.stream_id JOIN stream_peer_metrics p ON p.stream_id=s.id
 WHERE s.ended_at<$1 AND s.ended_at>$1-interval '7 days' AND b.source_last_seen=s.last_seen_at AND p.source_last_seen=s.last_seen_at
 AND coalesce(s.language,'')=$2 AND p.game_id=$3 AND p.median_viewers BETWEEN $4::float8/2 AND $4::float8*2
 ORDER BY s.last_seen_at DESC LIMIT 200`, r.StartedAt, r.Language, game, viewers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BehaviorPeer
	for rows.Next() {
		var p BehaviorPeer
		var raw []byte
		if err := rows.Scan(&p.StreamID, &p.ChannelID, &p.EndedAt, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &p.Metrics); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
