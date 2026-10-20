package store

import (
	"context"
	"time"
)

// PruneChatters summarizes and deletes stream_chatters rows for up to limit
// final-scored streams that ended before cutoff. newWithin matches the
// scorer's new-account window.
func (s *Store) PruneChatters(ctx context.Context, cutoff time.Time, newWithin time.Duration, limit int) (streams int, rows int64, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		WITH due AS (
			SELECT s.id, s.started_at,s.last_seen_at FROM streams s
			JOIN stream_scores sc ON sc.stream_id = s.id AND sc.final
			WHERE s.ended_at < $1 AND s.scored_at IS NOT NULL
			  AND NOT EXISTS (SELECT 1 FROM stream_chatter_summary x WHERE x.stream_id = s.id)
			ORDER BY s.ended_at
			LIMIT $3
		), months AS (
			SELECT d.id, to_char(date_trunc('month', u.created_at), 'YYYY-MM') AS m, count(*) AS n
			FROM due d JOIN stream_chatters c ON c.stream_id = d.id JOIN twitch_users u ON u.id = c.user_id
			WHERE u.created_at IS NOT NULL
			GROUP BY 1, 2
		)
		INSERT INTO stream_chatter_summary (stream_id, total, new_accounts, missing, created_months, summarized_at,resolved,coverage_known,pending)
		SELECT d.id,
		       count(c.user_id),
		       count(*) FILTER (WHERE c.utility_messages<c.messages AND u.created_at > d.started_at - $2::interval AND u.created_at<=d.last_seen_at),
		       count(*) FILTER (WHERE u.missing),
		       coalesce((SELECT jsonb_object_agg(m.m, m.n) FROM months m WHERE m.id = d.id), '{}'),
		       now(),count(*) FILTER(WHERE u.created_at IS NOT NULL),true,count(c.user_id) FILTER(WHERE u.id IS NULL)
		FROM due d
		LEFT JOIN stream_chatters c ON c.stream_id = d.id
		LEFT JOIN twitch_users u ON u.id = c.user_id
		GROUP BY d.id`, cutoff, newWithin, limit)
	if err != nil {
		return 0, 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, 0, nil
	}
	del, err := tx.Exec(ctx, `
		DELETE FROM stream_chatters c USING stream_chatter_summary x
		WHERE c.stream_id = x.stream_id AND x.summarized_at = now()`)
	if err != nil {
		return 0, 0, err
	}
	return int(tag.RowsAffected()), del.RowsAffected(), tx.Commit(ctx)
}

// PruneUsers deletes up to limit cached accounts fetched before cutoff that
// no remaining stream_chatters row references. They are looked up again if
// they chat in a tracked stream later.
func (s *Store) PruneUsers(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `
		DELETE FROM twitch_users WHERE id IN (
			SELECT u.id FROM twitch_users u
			WHERE u.fetched_at < $1
			  AND NOT EXISTS (SELECT 1 FROM stream_chatters c WHERE c.user_id = u.id)
			LIMIT $2)`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DatabaseSize returns the current database size in bytes.
func (s *Store) DatabaseSize(ctx context.Context) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&n)
	return n, err
}
