package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Endpoints that feed the dashboard's charts. Each query is bounded by a
// stream id, a stream's time range, or a recent time window plus a LIMIT.

const engagementLimit = 3000

type engagementPoint struct {
	StreamID      string    `json:"stream_id"`
	Login         string    `json:"login"`
	DisplayName   string    `json:"display_name"`
	Language      string    `json:"language"`
	GameName      *string   `json:"game_name"`
	StartedAt     time.Time `json:"started_at"`
	Quotient      float64   `json:"quotient"`
	Confidence    float64   `json:"confidence"`
	Final         bool      `json:"final"`
	MedianViewers float64   `json:"median_viewers"`
	ChattersPerK  float64   `json:"chatters_per_k"`
	ExpectedPerK  *float64  `json:"expected_per_k"`
	Z             *float64  `json:"z"`
	Strength      float64   `json:"strength"`
}

// engagement returns recently scored streams with their chat-engagement
// inputs, and log-log fits through them (global and per language).
func (s *Server) engagement(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days < 1 || days > 14 {
		days = 7
	}
	key := "engagement:" + strconv.Itoa(days)
	if raw := s.cached(key, time.Minute); raw != nil {
		s.writeJSON(w, raw)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	// stream_scores_recent_idx bounds the scan to the window.
	// Measurements are saved with the score even when independent past peers
	// are insufficient for a residual. Keep those points on the descriptive map.
	rows, err := s.db.Query(ctx, `
		SELECT sc.stream_id::text, c.login, c.display_name, coalesce(st.language, ''), st.game_name, st.started_at,
		       sc.quotient, sc.confidence, sc.final,
		       coalesce(pm.median_viewers, (sc.components #>> '{chat_engagement,values,median_viewers}')::float8),
		       coalesce(pm.chatters_per_k, (sc.components #>> '{chat_engagement,values,chatters_per_k}')::float8),
		       (sc.components #>> '{chat_engagement,values,expected_per_k}')::float8,
		       (sc.components #>> '{chat_engagement,values,z}')::float8,
		       coalesce((sc.components #>> '{chat_engagement,strength}')::float8, 0)
		FROM stream_scores sc
		JOIN streams st ON st.id = sc.stream_id
		JOIN channels c ON c.id = sc.channel_id
		LEFT JOIN stream_peer_metrics pm ON pm.stream_id = sc.stream_id AND pm.metric_version = 1
		WHERE sc.scored_at > now() - make_interval(days => $1)
		  AND (pm.stream_id IS NOT NULL OR sc.components #> '{chat_engagement,values}' ? 'median_viewers')
		ORDER BY sc.scored_at DESC
		LIMIT $2`, days, engagementLimit)
	if err != nil {
		s.fail(w, err)
		return
	}
	points := []engagementPoint{}
	for rows.Next() {
		var p engagementPoint
		if err := rows.Scan(&p.StreamID, &p.Login, &p.DisplayName, &p.Language, &p.GameName, &p.StartedAt,
			&p.Quotient, &p.Confidence, &p.Final, &p.MedianViewers, &p.ChattersPerK, &p.ExpectedPerK, &p.Z, &p.Strength); err != nil {
			rows.Close()
			s.fail(w, err)
			return
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, err)
		return
	}
	fits := map[string]engagementFit{}
	byLang := map[string][2][]float64{}
	var gx, gy []float64
	for _, p := range points {
		gx, gy = append(gx, p.MedianViewers), append(gy, p.ChattersPerK)
		if p.Language != "" {
			g := byLang[p.Language]
			byLang[p.Language] = [2][]float64{append(g[0], p.MedianViewers), append(g[1], p.ChattersPerK)}
		}
	}
	if f, ok := fitEngagement(gx, gy, minFitGlobal); ok {
		fits[""] = f
	}
	for lang, g := range byLang {
		if f, ok := fitEngagement(g[0], g[1], minFitLang); ok {
			fits[lang] = f
		}
	}
	byCat := map[string][2][]float64{}
	for _, p := range points {
		if p.GameName != nil && *p.GameName != "" {
			g := byCat[*p.GameName]
			byCat[*p.GameName] = [2][]float64{append(g[0], p.MedianViewers), append(g[1], p.ChattersPerK)}
		}
	}
	for cat, g := range byCat {
		if f, ok := fitEngagement(g[0], g[1], minFitLang); ok {
			fits["cat:"+cat] = f
		}
	}
	raw, err := json.Marshal(map[string]any{
		"days": days, "limit": engagementLimit, "truncated": len(points) == engagementLimit,
		"points": points, "fits": fits,
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.store(key, raw)
	s.writeJSON(w, json.RawMessage(raw))
}

const (
	onsetTolerance   = 3 // minutes, as in the scorer
	onsetPartnersMax = 8
)

// streamOnsets returns this stream's spike onsets and the onsets of other
// channels inside the stream's time range, with match counts.
func (s *Server) streamOnsets(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad stream id", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var channelID int64
	var from, to time.Time
	var reason *string
	err = s.db.QueryRow(ctx, `
		SELECT st.channel_id, st.first_seen_at, st.last_seen_at, sc.components #>> '{synchronized_spikes,reason}'
		FROM streams st LEFT JOIN stream_scores sc ON sc.stream_id = st.id
		WHERE st.id = $1`, id).Scan(&channelID, &from, &to, &reason)
	if err != nil {
		s.fail(w, err)
		return
	}
	named := ""
	if reason != nil {
		named, _ = strings.CutPrefix(*reason, "best partner: ")
		if named == *reason {
			named = ""
		}
	}

	type ownOnset struct {
		TS  int64   `json:"ts"`
		Rel float64 `json:"rel"`
	}
	own := []ownOnset{}
	var ownTS []int64
	rows, err := s.db.Query(ctx, `
		SELECT extract(epoch FROM ts)::bigint, rel FROM spike_onsets WHERE stream_id = $1 ORDER BY ts`, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	for rows.Next() {
		var o ownOnset
		if err := rows.Scan(&o.TS, &o.Rel); err != nil {
			rows.Close()
			s.fail(w, err)
			return
		}
		own = append(own, o)
		ownTS = append(ownTS, o.TS)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, err)
		return
	}

	// Other channels' onsets in the same window as the scorer compares
	// (spike_onsets_ts_idx), then their live overlap with that window.
	partners := []onsetPartner{}
	rows, err = s.db.Query(ctx, `
		SELECT so.channel_id::text, c.login, c.display_name,
		       array_agg(extract(epoch FROM so.ts)::bigint ORDER BY so.ts)
		FROM spike_onsets so JOIN channels c ON c.id = so.channel_id
		WHERE so.ts BETWEEN $1 AND $2 AND so.channel_id <> $3
		GROUP BY so.channel_id, c.login, c.display_name`, from, to, channelID)
	if err != nil {
		s.fail(w, err)
		return
	}
	idx := map[string]int{}
	var ids []int64
	for rows.Next() {
		var p onsetPartner
		if err := rows.Scan(&p.ChannelID, &p.Login, &p.DisplayName, &p.Onsets); err != nil {
			rows.Close()
			s.fail(w, err)
			return
		}
		idx[p.ChannelID] = len(partners)
		cid, _ := strconv.ParseInt(p.ChannelID, 10, 64)
		ids = append(ids, cid)
		partners = append(partners, p)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, err)
		return
	}
	if len(ids) > 0 {
		rows, err = s.db.Query(ctx, `
			SELECT channel_id::text,
			       sum(extract(epoch FROM LEAST(last_seen_at, $3) - GREATEST(first_seen_at, $2)) / 60)::int
			FROM streams
			WHERE channel_id = ANY($1) AND first_seen_at < $3 AND last_seen_at > $2
			GROUP BY channel_id`, ids, from, to)
		if err != nil {
			s.fail(w, err)
			return
		}
		for rows.Next() {
			var cid string
			var minutes int
			if err := rows.Scan(&cid, &minutes); err != nil {
				rows.Close()
				s.fail(w, err)
				return
			}
			if i, ok := idx[cid]; ok {
				partners[i].OverlapMinutes = minutes
			}
		}
		if err := rows.Err(); err != nil {
			s.fail(w, err)
			return
		}
	}
	scoreOnsetPartners(ownTS, partners, onsetTolerance)
	picked, tested := pickOnsetPartners(partners, named, onsetPartnersMax)
	if picked == nil {
		picked = []onsetPartner{}
	}
	s.writeJSON(w, map[string]any{
		"from": from, "to": to, "tolerance_min": onsetTolerance,
		"own": own, "partners": picked, "partners_tested": tested, "channels_with_onsets": len(partners),
	})
}

// streamAccountAges serves the chatter account-creation histogram.
func (s *Server) streamAccountAges(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad stream id", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	raw, err := s.accountAgeHistogram(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writeJSON(w, raw)
}

// accountAgeHistogram buckets the creation dates of a stream's chatters by
// month (all time) and by week (the year before the stream), and counts
// unavailable and not-yet-looked-up accounts. It reads per-chatter
// rows from stream_chatters, so it returns an empty histogram once those rows
// have been pruned for an old stream.
func (s *Server) accountAgeHistogram(ctx context.Context, streamID int64) (json.RawMessage, error) {
	// Streams past chatter retention keep only a summary: monthly counts, no weeks.
	summary, err := s.queryJSON(ctx, `
		SELECT json_build_object(
			'started_at', st.started_at, 'chatters', x.total, 'known',x.resolved,'missing',x.missing,'pending',x.pending,'coverage_known',x.coverage_known,
			'months', (SELECT coalesce(json_agg(json_build_array(k, v::int) ORDER BY k), '[]')
			           FROM jsonb_each_text(x.created_months) AS m(k, v)),
			'weeks', '[]'::json, 'summarized', true)
		FROM stream_chatter_summary x JOIN streams st ON st.id = x.stream_id
		WHERE x.stream_id = $1`, streamID)
	if err == nil {
		return summary, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return s.queryJSON(ctx, `
		WITH st AS (SELECT started_at FROM streams WHERE id = $1),
		u AS MATERIALIZED (
			SELECT u.created_at IS NOT NULL AS known, coalesce(u.missing, false) AS missing, u.created_at
			FROM stream_chatters sc LEFT JOIN twitch_users u ON u.id = sc.user_id
			WHERE sc.stream_id = $1
		)
		SELECT json_build_object(
			'started_at', (SELECT started_at FROM st),
			'chatters', (SELECT count(*) FROM u),
			'known', (SELECT count(*) FILTER (WHERE known) FROM u),
			'missing', (SELECT count(*) FILTER (WHERE missing) FROM u),
			'months', (SELECT coalesce(json_agg(json_build_array(to_char(m AT TIME ZONE 'UTC', 'YYYY-MM'), n) ORDER BY m), '[]') FROM (
				SELECT date_trunc('month', created_at, 'UTC') m, count(*) n FROM u
				WHERE created_at IS NOT NULL GROUP BY 1) x),
			'weeks', (SELECT coalesce(json_agg(json_build_array(to_char(wk AT TIME ZONE 'UTC', 'YYYY-MM-DD'), n) ORDER BY wk), '[]') FROM (
				SELECT date_trunc('week', created_at, 'UTC') wk, count(*) n FROM u, st
				WHERE created_at IS NOT NULL AND created_at > st.started_at - interval '364 days'
				GROUP BY 1) x)
		)
		WHERE EXISTS (SELECT 1 FROM st)`, streamID)
}

// sparklines returns every channel's viewers over the last three hours in
// ten-minute buckets: one query for the whole leaderboard.
func (s *Server) sparklines(w http.ResponseWriter, r *http.Request) {
	const key = "sparklines"
	if raw := s.cached(key, time.Minute); raw != nil {
		s.writeJSON(w, raw)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	raw, err := s.queryJSON(ctx, `
		SELECT json_build_object(
			'window_minutes', 180, 'bucket_minutes', 10, 'now', extract(epoch FROM now())::bigint,
			'channels', (SELECT coalesce(json_object_agg(channel_id::text, pts), '{}') FROM (
				SELECT channel_id, json_agg(json_build_array(extract(epoch FROM b)::bigint, round(v)) ORDER BY b) pts
				FROM (SELECT channel_id, time_bucket(interval '10 minutes', ts) b, avg(viewers) v
				      FROM viewer_samples WHERE ts > now() - interval '3 hours'
				      GROUP BY 1, 2) x
				GROUP BY channel_id) y)
		)`)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.store(key, raw)
	s.writeJSON(w, raw)
}

// cached returns a stored response younger than maxAge, or nil.
func (s *Server) cached(key string, maxAge time.Duration) json.RawMessage {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	e, ok := s.cache[key]
	if !ok || time.Since(e.at) > maxAge {
		return nil
	}
	return e.raw
}

func (s *Server) store(key string, raw json.RawMessage) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cache == nil {
		s.cache = map[string]cacheEntry{}
	}
	s.cache[key] = cacheEntry{at: time.Now(), raw: raw}
}

type cacheEntry struct {
	at  time.Time
	raw json.RawMessage
}
