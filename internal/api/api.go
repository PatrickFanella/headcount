// Package api serves read-only JSON for the dashboard and the embedded web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	db  *pgxpool.Pool
	log *slog.Logger
	web fs.FS

	overviewMu  sync.Mutex
	overviewAt  time.Time
	overviewRaw json.RawMessage
}

func New(db *pgxpool.Pool, web fs.FS, log *slog.Logger) *Server {
	return &Server{db: db, web: web, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/overview", s.overview)
	mux.HandleFunc("GET /api/channels", s.channels)
	mux.HandleFunc("GET /api/channels/{login}", s.channel)
	mux.HandleFunc("GET /api/streams/{id}", s.stream)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.db.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /", http.FileServerFS(s.web))
	return mux
}

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Warn("encode response", "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.log.Error("request failed", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// queryJSON runs a query whose single column is a json value and decodes it.
func (s *Server) queryJSON(ctx context.Context, sql string, args ...any) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.db.QueryRow(ctx, sql, args...).Scan(&raw)
	return raw, err
}

// overview is cached briefly; it is requested on every leaderboard load.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	s.overviewMu.Lock()
	defer s.overviewMu.Unlock()
	if time.Since(s.overviewAt) < 30*time.Second && s.overviewRaw != nil {
		s.writeJSON(w, s.overviewRaw)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	raw, err := s.queryJSON(ctx, `
		SELECT json_build_object(
			'channels', (SELECT count(*) FROM channels),
			'tiers', (SELECT json_object_agg(tier, n) FROM (SELECT tier, count(*) n FROM channels GROUP BY tier) t),
			'live', (SELECT count(*) FROM streams WHERE ended_at IS NULL),
			'scored_streams', (SELECT count(*) FROM stream_scores WHERE final),
			'first_sample', (SELECT min(first_seen_at) FROM streams),
			'last_sample', (SELECT max(last_seen_at) FROM streams),
			'chat_messages_24h', (SELECT coalesce(sum(messages), 0) FROM chat_minutes WHERE ts > now() - interval '24 hours'),
			'chatters_known', (SELECT greatest(reltuples, 0)::bigint FROM pg_class WHERE oid = 'twitch_users'::regclass)
		)`)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.overviewRaw, s.overviewAt = raw, time.Now()
	s.writeJSON(w, raw)
}

// channels lists tracked channels with their quotient and live state.
func (s *Server) channels(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > 2000 {
		limit = 500
	}
	liveOnly := r.URL.Query().Get("live") == "1"
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := s.queryJSON(ctx, `
		SELECT coalesce(json_agg(row_to_json(x)), '[]') FROM (
			SELECT c.id::text AS id, c.login, c.display_name, c.tier, c.best_rank, c.first_seen_at, c.last_live_at,
			       c.quotient, c.quotient_confidence,
			       (SELECT count(*) FROM stream_scores sc WHERE sc.channel_id = c.id AND sc.final) AS scored_streams,
			       s.id::text AS live_stream_id, s.game_name, s.title, s.started_at,
			       lv.viewers, lv.rank,
			       ls.quotient AS live_quotient, ls.confidence AS live_confidence
			FROM channels c
			LEFT JOIN streams s ON s.channel_id = c.id AND s.ended_at IS NULL
			LEFT JOIN stream_scores ls ON ls.stream_id = s.id
			-- Bounded to the stream's last minutes so offline channels and old
			-- (compressed) chunks are never scanned.
			LEFT JOIN LATERAL (
				SELECT v.viewers, v.rank FROM viewer_samples v
				WHERE v.channel_id = c.id AND v.stream_id = s.id
				  AND v.ts BETWEEN s.last_seen_at - interval '10 minutes' AND s.last_seen_at
				ORDER BY v.ts DESC LIMIT 1
			) lv ON s.id IS NOT NULL
			WHERE (NOT $2 OR s.id IS NOT NULL)
			ORDER BY coalesce(CASE WHEN ls.confidence >= 0.1 THEN ls.quotient END,
			                  CASE WHEN c.quotient_confidence >= 0.1 THEN c.quotient END) DESC NULLS LAST, viewers DESC NULLS LAST, c.best_rank
			LIMIT $1
		) x`, limit, liveOnly)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writeJSON(w, raw)
}

func (s *Server) channel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := s.queryJSON(ctx, `
		SELECT json_build_object(
			'channel', row_to_json(c),
			'streams', (SELECT coalesce(json_agg(row_to_json(x) ORDER BY x.started_at DESC), '[]') FROM (
				SELECT s.id::text AS id, s.started_at, s.ended_at, s.title, s.game_name, s.peak_viewers,
				       sc.quotient, sc.confidence, sc.final, sc.minutes,
				       (SELECT round(avg(v.viewers)) FROM viewer_samples v WHERE v.channel_id = s.channel_id AND v.stream_id = s.id
				        AND v.ts BETWEEN s.first_seen_at AND s.last_seen_at) AS avg_viewers
				FROM streams s LEFT JOIN stream_scores sc ON sc.stream_id = s.id
				WHERE s.channel_id = c.id ORDER BY s.started_at DESC LIMIT 50) x)
		)
		FROM (SELECT id::text AS id, login, display_name, tier, best_rank, first_seen_at, last_top_at, last_live_at,
		             quotient, quotient_confidence, quotient_updated_at
		      FROM channels WHERE login = lower($1)) c`, r.PathValue("login"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writeJSON(w, raw)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad stream id", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := s.queryJSON(ctx, `
		WITH st AS (SELECT * FROM streams WHERE id = $1)
		SELECT json_build_object(
			'stream', (SELECT json_build_object('id', st.id::text, 'channel_id', st.channel_id::text, 'login', c.login,
				'display_name', c.display_name, 'started_at', st.started_at, 'first_seen_at', st.first_seen_at,
				'last_seen_at', st.last_seen_at, 'ended_at', st.ended_at, 'title', st.title, 'game_name', st.game_name,
				'peak_viewers', st.peak_viewers) FROM st JOIN channels c ON c.id = st.channel_id),
			'score', (SELECT row_to_json(x) FROM (SELECT quotient, confidence, final, minutes, version, scored_at, components
				FROM stream_scores WHERE stream_id = $1) x),
			'viewers', (SELECT coalesce(json_agg(json_build_array(extract(epoch FROM v.ts)::bigint, v.viewers) ORDER BY v.ts), '[]')
				FROM viewer_samples v, st WHERE v.channel_id = st.channel_id AND v.stream_id = st.id
				AND v.ts BETWEEN st.first_seen_at AND st.last_seen_at),
			'chat', (SELECT coalesce(json_agg(json_build_array(extract(epoch FROM m.ts)::bigint, m.messages, m.unique_chatters,
				m.new_chatters, m.top_text_count) ORDER BY m.ts), '[]')
				FROM chat_minutes m, st WHERE m.channel_id = st.channel_id AND m.ts BETWEEN st.first_seen_at AND st.last_seen_at),
			'followers', (SELECT coalesce(json_agg(json_build_array(extract(epoch FROM f.ts)::bigint, f.total) ORDER BY f.ts), '[]')
				FROM follower_samples f, st WHERE f.channel_id = st.channel_id
				AND f.ts BETWEEN st.first_seen_at - interval '5 minutes' AND st.last_seen_at),
			'events', (SELECT coalesce(json_agg(json_build_object('ts', e.ts, 'kind', e.kind, 'detail', e.detail) ORDER BY e.ts), '[]')
				FROM channel_events e, st WHERE e.channel_id = st.channel_id AND e.kind IN ('raid_in', 'game_change', 'title_change', 'chat_mode')
				AND e.ts BETWEEN st.first_seen_at AND st.last_seen_at + interval '1 minute'),
			'accounts', (SELECT json_build_object('chatters', count(*),
				'known', count(u.id), 'new_30d', count(*) FILTER (WHERE u.created_at > (SELECT started_at FROM st) - interval '30 days'),
				'missing', count(*) FILTER (WHERE u.missing))
				FROM stream_chatters sc LEFT JOIN twitch_users u ON u.id = sc.user_id WHERE sc.stream_id = $1)
		)
		WHERE EXISTS (SELECT 1 FROM st)`, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writeJSON(w, raw)
}
