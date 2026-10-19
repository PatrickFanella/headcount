package api

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/headcount/internal/score"
)

const (
	searchMaxLen     = 50
	searchChannels   = 20
	searchCategories = 10
	categoryDays     = 30
)

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// likePattern matches q anywhere; prefixPattern only at the start. Wildcards
// in q match literally.
func likePattern(q string) string   { return "%" + likeEscaper.Replace(q) + "%" }
func prefixPattern(q string) string { return likeEscaper.Replace(q) + "%" }

// search finds tracked channels by login or display name, and categories
// streamed by tracked channels in the last 90 days. Results are not cached,
// since every query string would be a new cache key.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || utf8.RuneCountInString(q) > searchMaxLen {
		http.Error(w, "q must be 1 to 50 characters", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	raw, err := s.queryJSON(ctx, `
		SELECT json_build_object(
			'channels', (SELECT coalesce(json_agg(row_to_json(x)), '[]') FROM (
				SELECT c.id::text AS id, c.login, c.display_name, c.tier, c.best_rank, c.last_live_at,
				       c.quotient, c.quotient_confidence,
				       EXISTS (SELECT 1 FROM streams s WHERE s.channel_id = c.id AND s.ended_at IS NULL) AS live
				FROM channels c
				WHERE c.login ILIKE $2 OR c.display_name ILIKE $2
				ORDER BY (lower(c.login) = lower($1) OR lower(c.display_name) = lower($1)) DESC,
				         (c.login ILIKE $3 OR c.display_name ILIKE $3) DESC,
				         c.best_rank NULLS LAST, c.login
				LIMIT $4) x),
			'categories', (SELECT coalesce(json_agg(row_to_json(x)), '[]') FROM (
				SELECT game_id, max(game_name) AS game_name, count(*) AS streams,
				       count(DISTINCT channel_id) AS channels
				FROM streams
				WHERE game_name ILIKE $2 AND game_id IS NOT NULL
				  AND last_seen_at > now() - interval '90 days'
				GROUP BY game_id
				ORDER BY (lower(max(game_name)) = lower($1)) DESC, count(*) DESC
				LIMIT $5) x)
		)`, q, likePattern(q), prefixPattern(q), searchChannels, searchCategories)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writeJSON(w, raw)
}

// category lists the tracked channels that streamed a category recently,
// with their scores there, and other categories in the same group.
func (s *Server) category(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || len(id) > 20 || strings.Trim(id, "0123456789") != "" {
		http.Error(w, "bad category id", http.StatusBadRequest)
		return
	}
	key := "category:" + id
	if raw := s.cached(key, time.Minute); raw != nil {
		s.writeJSON(w, raw)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var name string
	if err := s.db.QueryRow(ctx, `
		SELECT game_name FROM streams WHERE game_id = $1 AND game_name IS NOT NULL
		ORDER BY last_seen_at DESC LIMIT 1`, id).Scan(&name); err != nil {
		s.fail(w, err)
		return
	}
	family := score.FamilyOf(name)
	var related []string
	if family != "games" {
		for n, f := range score.Families() {
			if f == family && n != name {
				related = append(related, n)
			}
		}
	}
	// streams_game_idx bounds the scan to the category and window.
	raw, err := s.queryJSON(ctx, `
		WITH st AS (
			SELECT s.id, s.channel_id, s.started_at, s.ended_at, s.peak_viewers, sc.quotient, sc.confidence, sc.final
			FROM streams s LEFT JOIN stream_scores sc ON sc.stream_id = s.id
			WHERE s.game_id = $1 AND s.last_seen_at > now() - make_interval(days => $2)
		)
		SELECT json_build_object(
			'game_id', $1::text, 'game_name', $3::text, 'family', $4::text, 'days', $2,
			'streams', (SELECT count(*) FROM st),
			'scored', (SELECT count(*) FROM st WHERE final AND confidence >= 0.1),
			'median_quotient', (SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY quotient)
				FROM st WHERE final AND confidence >= 0.1),
			'channels', (SELECT coalesce(json_agg(row_to_json(x) ORDER BY x.max_quotient DESC NULLS LAST, x.peak DESC), '[]') FROM (
				SELECT c.login, c.display_name, c.tier, c.quotient AS channel_quotient,
				       c.quotient_confidence AS channel_confidence,
				       count(*) AS streams, max(st.peak_viewers) AS peak,
				       bool_or(st.ended_at IS NULL) AS live,
				       round(avg(st.quotient) FILTER (WHERE st.final AND st.confidence >= 0.1)::numeric, 1) AS avg_quotient,
				       max(st.quotient) FILTER (WHERE st.final AND st.confidence >= 0.1) AS max_quotient,
				       (array_agg(st.id::text ORDER BY st.quotient DESC NULLS LAST)
				         FILTER (WHERE st.final AND st.confidence >= 0.1))[1] AS top_stream_id
				FROM st JOIN channels c ON c.id = st.channel_id
				GROUP BY c.id LIMIT 500) x),
			'related', (SELECT coalesce(json_agg(row_to_json(x) ORDER BY x.streams DESC), '[]') FROM (
				SELECT game_id, max(game_name) AS game_name, count(*) AS streams
				FROM streams WHERE game_name = ANY($5) AND game_id IS NOT NULL
				  AND last_seen_at > now() - make_interval(days => $2)
				GROUP BY game_id) x)
		)`, id, categoryDays, name, family, related)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.store(key, raw)
	s.writeJSON(w, raw)
}
