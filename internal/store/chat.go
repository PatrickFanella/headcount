package store

import (
	"context"
	"time"
)

type ChatMinute struct {
	ChannelID      int64
	StreamID       int64 // 0 = unknown
	Messages       int
	UniqueChatters int
	NewChatters    int
	FirstMsgs      int
	EmoteOnly      int
	DistinctTexts  int
	TopTextCount   int
	Bans           int
	Timeouts       int
}

type ChatterMinute struct {
	StreamID  int64
	UserID    int64
	ChannelID int64
	Messages  int
}

// RecordChat writes one minute of chat aggregates and folds the minute's
// chatters into stream_chatters.
func (s *Store) RecordChat(ctx context.Context, t time.Time, minutes []ChatMinute, chatters []ChatterMinute) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if len(minutes) > 0 {
		n := len(minutes)
		cols := make([][]int32, 10)
		for i := range cols {
			cols[i] = make([]int32, n)
		}
		channelIDs := make([]int64, n)
		streamIDs := make([]*int64, n)
		for i, m := range minutes {
			channelIDs[i] = m.ChannelID
			if m.StreamID != 0 {
				sid := m.StreamID
				streamIDs[i] = &sid
			}
			for j, v := range []int{m.Messages, m.UniqueChatters, m.NewChatters, m.FirstMsgs, m.EmoteOnly,
				m.DistinctTexts, m.TopTextCount, m.Bans, m.Timeouts, 0} {
				cols[j][i] = int32(v)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO chat_minutes (ts, channel_id, stream_id, messages, unique_chatters, new_chatters, first_msgs,
			                          emote_only, distinct_texts, top_text_count, bans, timeouts)
			SELECT $1, cid, sid, m, u, nc, fm, eo, dt, tt, b, tmo
			FROM unnest($2::bigint[], $3::bigint[], $4::int[], $5::int[], $6::int[], $7::int[], $8::int[], $9::int[],
			            $10::int[], $11::int[], $12::int[]) AS x(cid, sid, m, u, nc, fm, eo, dt, tt, b, tmo)
			ON CONFLICT DO NOTHING`,
			t, channelIDs, streamIDs, cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6], cols[7], cols[8]); err != nil {
			return err
		}
	}
	if len(chatters) > 0 {
		n := len(chatters)
		sids := make([]int64, n)
		uids := make([]int64, n)
		cids := make([]int64, n)
		msgs := make([]int32, n)
		for i, c := range chatters {
			sids[i], uids[i], cids[i], msgs[i] = c.StreamID, c.UserID, c.ChannelID, int32(c.Messages)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO stream_chatters (stream_id, user_id, channel_id, first_seen_at, last_seen_at, messages)
			SELECT sid, uid, cid, $1, $1, m FROM unnest($2::bigint[], $3::bigint[], $4::bigint[], $5::int[]) AS x(sid, uid, cid, m)
			ON CONFLICT (stream_id, user_id) DO UPDATE SET
				last_seen_at = $1,
				messages = stream_chatters.messages + EXCLUDED.messages`,
			t, sids, uids, cids, msgs); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// StreamChatterIDs returns users already seen in a stream, used to rebuild the
// in-memory "seen" set after a restart.
func (s *Store) StreamChatterIDs(ctx context.Context, streamID int64) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `SELECT user_id FROM stream_chatters WHERE stream_id = $1`, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UnknownUsers filters ids down to those without a twitch_users row.
func (s *Store) UnknownUsers(ctx context.Context, ids []int64) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id FROM unnest($1::bigint[]) AS u(id)
		WHERE NOT EXISTS (SELECT 1 FROM twitch_users t WHERE t.id = u.id)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type TwitchUser struct {
	ID        int64
	Login     string
	CreatedAt *time.Time // nil when missing
}

func (s *Store) InsertUsers(ctx context.Context, users []TwitchUser) error {
	if len(users) == 0 {
		return nil
	}
	n := len(users)
	ids := make([]int64, n)
	logins := make([]*string, n)
	created := make([]*time.Time, n)
	missing := make([]bool, n)
	for i, u := range users {
		ids[i] = u.ID
		if u.CreatedAt == nil {
			missing[i] = true
		} else {
			l := u.Login
			logins[i] = &l
			created[i] = u.CreatedAt
		}
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO twitch_users (id, login, created_at, missing, fetched_at)
		SELECT id, login, created, missing, now()
		FROM unnest($1::bigint[], $2::text[], $3::timestamptz[], $4::bool[]) AS u(id, login, created, missing)
		ON CONFLICT (id) DO UPDATE SET login = EXCLUDED.login, created_at = EXCLUDED.created_at,
			missing = EXCLUDED.missing, fetched_at = now()`, ids, logins, created, missing)
	return err
}
