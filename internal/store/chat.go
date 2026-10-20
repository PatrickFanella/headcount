package store

import (
	"context"
	"time"
)

type ChatMinute struct {
	ChannelID                                                                       int64
	StreamID                                                                        int64 // 0 = unknown
	Messages                                                                        int
	UniqueChatters                                                                  int
	NewChatters                                                                     int
	FirstMsgs                                                                       int
	EmoteOnly                                                                       int
	DistinctTexts                                                                   int
	TopTextCount                                                                    int
	Bans                                                                            int
	Timeouts                                                                        int
	Healthy                                                                         *bool
	Commands, UtilityMessages, BotBadgeMessages, DuplicateMessages, DelayedMessages int
	SourceDelayMS                                                                   int64
}

type ChatterMinute struct {
	StreamID                                    int64
	UserID                                      int64
	ChannelID                                   int64
	Messages                                    int
	Commands, UtilityMessages, BotBadgeMessages int
	GapBins                                     [4]int64
}

// RecordChat writes one minute of chat aggregates and folds the minute's
// chatters into stream_chatters.
func (s *Store) RecordChat(ctx context.Context, t time.Time, minutes []ChatMinute, chatters []ChatterMinute) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	accepted := map[int64]bool{}
	if len(minutes) > 0 {
		n := len(minutes)
		cols := make([][]int32, 14)
		healthy := make([]*bool, n)
		delay := make([]int64, n)
		for i := range cols {
			cols[i] = make([]int32, n)
		}
		channelIDs := make([]int64, n)
		streamIDs := make([]*int64, n)
		for i, m := range minutes {
			channelIDs[i] = m.ChannelID
			healthy[i] = m.Healthy
			delay[i] = m.SourceDelayMS
			if m.StreamID != 0 {
				sid := m.StreamID
				streamIDs[i] = &sid
			}
			for j, v := range []int{m.Messages, m.UniqueChatters, m.NewChatters, m.FirstMsgs, m.EmoteOnly,
				m.DistinctTexts, m.TopTextCount, m.Bans, m.Timeouts, m.Commands, m.UtilityMessages, m.BotBadgeMessages, m.DuplicateMessages, m.DelayedMessages} {
				cols[j][i] = int32(v)
			}
		}
		written, err := tx.Query(ctx, `
			INSERT INTO chat_minutes (ts, channel_id, stream_id, messages, unique_chatters, new_chatters, first_msgs,
			                          emote_only, distinct_texts, top_text_count, bans, timeouts, commands,utility_messages,bot_badge_messages,duplicate_messages,delayed_messages,healthy,source_delay_ms)
			SELECT $1, cid, sid, m, u, nc, fm, eo, dt, tt, b, tmo,cmd,util,bot,dup,late,h,delay
			FROM unnest($2::bigint[], $3::bigint[], $4::int[], $5::int[], $6::int[], $7::int[], $8::int[], $9::int[],
			            $10::int[], $11::int[], $12::int[], $13::int[],$14::int[],$15::int[],$16::int[],$17::int[], $18::bool[],$19::bigint[]) AS x(cid, sid, m, u, nc, fm, eo, dt, tt, b, tmo,cmd,util,bot,dup,late,h,delay)
			ON CONFLICT DO NOTHING RETURNING channel_id`,
			t, channelIDs, streamIDs, cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6], cols[7], cols[8], cols[9], cols[10], cols[11], cols[12], cols[13], healthy, delay)
		if err != nil {
			return err
		}
		for written.Next() {
			var id int64
			if err := written.Scan(&id); err != nil {
				written.Close()
				return err
			}
			accepted[id] = true
		}
		err = written.Err()
		written.Close()
		if err != nil {
			return err
		}
	}
	if len(chatters) > 0 {
		filtered := make([]ChatterMinute, 0, len(chatters))
		for _, c := range chatters {
			if accepted[c.ChannelID] {
				filtered = append(filtered, c)
			}
		}
		n := len(filtered)
		sids, uids, cids := make([]int64, n), make([]int64, n), make([]int64, n)
		columns := make([][]int64, 8)
		for i := range columns {
			columns[i] = make([]int64, n)
		}
		for i, c := range filtered {
			sids[i], uids[i], cids[i] = c.StreamID, c.UserID, c.ChannelID
			values := []int64{int64(c.Messages), int64(c.Commands), int64(c.UtilityMessages), int64(c.BotBadgeMessages), c.GapBins[0], c.GapBins[1], c.GapBins[2], c.GapBins[3]}
			for j, v := range values {
				columns[j][i] = v
			}
		}
		if n > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO stream_chatters(stream_id,user_id,channel_id,first_seen_at,last_seen_at,messages,active_minutes,commands,utility_messages,bot_badge_messages,gap_bins)
 SELECT sid,uid,cid,$1,$1,m,1,cmd,util,bot,ARRAY[g1,g2,g3,g4]
 FROM unnest($2::bigint[],$3::bigint[],$4::bigint[],$5::bigint[],$6::bigint[],$7::bigint[],$8::bigint[],$9::bigint[],$10::bigint[],$11::bigint[],$12::bigint[]) AS x(sid,uid,cid,m,cmd,util,bot,g1,g2,g3,g4)
 ON CONFLICT(stream_id,user_id) DO UPDATE SET
 first_seen_at=least(stream_chatters.first_seen_at,EXCLUDED.first_seen_at),last_seen_at=greatest(stream_chatters.last_seen_at,EXCLUDED.last_seen_at),
 messages=stream_chatters.messages+EXCLUDED.messages,active_minutes=stream_chatters.active_minutes+1,
 commands=stream_chatters.commands+EXCLUDED.commands,utility_messages=stream_chatters.utility_messages+EXCLUDED.utility_messages,
 bot_badge_messages=stream_chatters.bot_badge_messages+EXCLUDED.bot_badge_messages,
 gap_bins=ARRAY[stream_chatters.gap_bins[1]+EXCLUDED.gap_bins[1],stream_chatters.gap_bins[2]+EXCLUDED.gap_bins[2],stream_chatters.gap_bins[3]+EXCLUDED.gap_bins[3],stream_chatters.gap_bins[4]+EXCLUDED.gap_bins[4]]`, t, sids, uids, cids, columns[0], columns[1], columns[2], columns[3], columns[4], columns[5], columns[6], columns[7]); err != nil {
				return err
			}
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

// UnknownUsers returns new IDs and cached IDs due for refresh.
func (s *Store) UnknownUsers(ctx context.Context, ids []int64) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id FROM unnest($1::bigint[]) AS u(id)
		WHERE NOT EXISTS (SELECT 1 FROM twitch_users t WHERE t.id = u.id AND t.next_check_at > now())`, ids)
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
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
 INSERT INTO twitch_users (id,login,created_at,missing,fetched_at,omission_count,next_check_at)
 SELECT id,login,created,missing,now(),CASE WHEN missing THEN 1 ELSE 0 END,
 now()+CASE WHEN missing THEN interval '1 hour' ELSE interval '7 days' END
 FROM unnest($1::bigint[],$2::text[],$3::timestamptz[],$4::bool[]) AS u(id,login,created,missing)
 ON CONFLICT (id) DO UPDATE SET login=coalesce(EXCLUDED.login,twitch_users.login),
 created_at=coalesce(twitch_users.created_at,EXCLUDED.created_at),missing=EXCLUDED.missing,fetched_at=now(),
 omission_count=CASE WHEN EXCLUDED.missing THEN twitch_users.omission_count+1 ELSE 0 END,
 next_check_at=now()+CASE WHEN EXCLUDED.missing THEN interval '1 hour'*least(24,power(2,least(5,twitch_users.omission_count))) ELSE interval '7 days' END`, ids, logins, created, missing)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_observations (user_id,checked_at,available,created_at)
 SELECT id,now(),NOT missing,created FROM unnest($1::bigint[],$2::bool[],$3::timestamptz[]) AS u(id,missing,created)
 ON CONFLICT DO NOTHING`, ids, missing, created); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DueUsers also revisits omitted IDs after they stop chatting; work and API use are bounded.
func (s *Store) DueUsers(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `SELECT u.id FROM twitch_users u WHERE u.next_check_at<=now()
 AND EXISTS (SELECT 1 FROM stream_chatters c WHERE c.user_id=u.id AND c.last_seen_at>now()-interval '7 days')
 ORDER BY u.next_check_at LIMIT $1`, limit)
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
