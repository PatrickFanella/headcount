package store

import (
	"context"
	"time"
)

// SharedAccount is a young or suspended account that chatted in this stream,
// with the other channels it chatted in during the window.
type SharedAccount struct {
	UserID   int64
	Channels []int64
}

// SharedYoungChatters returns this stream's chatters whose accounts were
// created after youngSince or are suspended/deleted, together with the other
// channels each chatted in between from and to.
func (s *Store) SharedYoungChatters(ctx context.Context, streamID, channelID int64, youngSince, from, to time.Time) (young int, shared []SharedAccount, err error) {
	if err = s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM stream_chatters sc JOIN twitch_users u ON u.id = sc.user_id
		WHERE sc.stream_id = $1 AND (u.created_at > $2 OR u.missing)`, streamID, youngSince).Scan(&young); err != nil {
		return 0, nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		WITH young AS (
			SELECT sc.user_id FROM stream_chatters sc JOIN twitch_users u ON u.id = sc.user_id
			WHERE sc.stream_id = $1 AND (u.created_at > $2 OR u.missing)
		)
		SELECT y.user_id, array_agg(DISTINCT c.channel_id)
		FROM young y JOIN stream_chatters c ON c.user_id = y.user_id
		WHERE c.channel_id <> $3 AND c.last_seen_at BETWEEN $4 AND $5
		GROUP BY y.user_id`, streamID, youngSince, channelID, from, to)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a SharedAccount
		if err := rows.Scan(&a.UserID, &a.Channels); err != nil {
			return 0, nil, err
		}
		shared = append(shared, a)
	}
	return young, shared, rows.Err()
}

// ChannelLogins maps channel ids to logins.
func (s *Store) ChannelLogins(ctx context.Context, ids []int64) (map[int64]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, login FROM channels WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var login string
		if err := rows.Scan(&id, &login); err != nil {
			return nil, err
		}
		out[id] = login
	}
	return out, rows.Err()
}

// PreviousComponent returns a component from the stream's stored score, used
// when the chatter rows it was computed from have been pruned.
func (s *Store) PreviousComponent(ctx context.Context, streamID int64, name string) (raw []byte, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT components -> $2 FROM stream_scores WHERE stream_id = $1`,
		streamID, name).Scan(&raw)
	return raw, err
}

// ChattersPruned reports whether a stream's chatter rows were summarized and deleted.
func (s *Store) ChattersPruned(ctx context.Context, streamID int64) (bool, error) {
	var ok bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stream_chatter_summary WHERE stream_id = $1)`, streamID).Scan(&ok)
	return ok, err
}
