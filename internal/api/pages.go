package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The dashboard is a single-page app, but each page has its own URL. The
// server renders index.html for every page with that page's title,
// description and a plain HTML summary, so search engines and link previews
// see real content before the script runs.

const siteName = "Headcount"

type asset struct {
	body []byte
	etag string
}

type link struct{ Href, Text, Note string }

type page struct {
	Title       string // full <title>
	Description string
	Path        string // canonical path
	NoIndex     bool
	JSONLD      any
	H1, Intro   string
	Links       []link
	status      int
}

type pageData struct {
	page
	Base, V string
}

// importRe matches the static imports between the app's modules.
var importRe = regexp.MustCompile(`(from\s+'\./[\w-]+\.js)'`)

// loadAssets reads the embedded files into memory. The version is a hash of
// all of them; index.html and the modules' imports carry it, so a deploy
// changes every URL and those URLs can be cached for good.
func (s *Server) loadAssets() error {
	s.assets = map[string]asset{}
	sum := sha256.New()
	var names []string
	err := fs.WalkDir(s.web, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return err
	})
	if err != nil {
		return err
	}
	sort.Strings(names)
	raw := map[string][]byte{}
	for _, name := range names {
		b, err := fs.ReadFile(s.web, name)
		if err != nil {
			return err
		}
		raw[name] = b
		fmt.Fprintf(sum, "%s\x00%d\x00", name, len(b))
		sum.Write(b)
	}
	s.version = hex.EncodeToString(sum.Sum(nil))[:12]
	for name, b := range raw {
		if name == "index.html" {
			continue
		}
		if strings.HasSuffix(name, ".js") {
			b = importRe.ReplaceAll(b, []byte("$1?v="+s.version+"'"))
		}
		h := sha256.Sum256(b)
		s.assets["/"+name] = asset{body: b, etag: `"` + hex.EncodeToString(h[:8]) + `"`}
	}
	idx, ok := raw["index.html"]
	if !ok {
		return errors.New("index.html missing")
	}
	s.index, err = template.New("index").Parse(string(idx))
	return err
}

// serveAsset serves an embedded file, or the not-found page.
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	a, ok := s.assets[r.URL.Path]
	if !ok {
		s.render(w, r, notFoundPage())
		return
	}
	switch {
	case r.URL.Query().Get("v") == s.version:
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case strings.HasPrefix(r.URL.Path, "/brand/"):
		w.Header().Set("Cache-Control", "public, max-age=86400")
	default:
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("ETag", a.etag)
	http.ServeContent(w, r, path.Base(r.URL.Path), time.Time{}, bytes.NewReader(a.body))
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, p page) {
	var buf bytes.Buffer
	if err := s.index.Execute(&buf, pageData{page: p, Base: s.base, V: s.version}); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if p.status != 0 {
		w.WriteHeader(p.status)
	}
	if r.Method != http.MethodHead {
		w.Write(buf.Bytes())
	}
}

// pageHandler renders a page built by fn. A missing row is a 404 page.
func (s *Server) pageHandler(fn func(ctx context.Context, r *http.Request) (page, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		p, err := fn(ctx, r)
		if errors.Is(err, pgx.ErrNoRows) {
			s.render(w, r, notFoundPage())
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		s.render(w, r, p)
	}
}

func static(p page) func(context.Context, *http.Request) (page, error) {
	return func(context.Context, *http.Request) (page, error) { return p, nil }
}

func title(t string) string { return t + " · " + siteName }

func notFoundPage() page {
	return page{Title: title("Page not found"), Description: "There is nothing at this address.", Path: "/", NoIndex: true,
		H1: "Page not found", Intro: "There is nothing at this address.", Links: []link{{Href: "/", Text: "Go to the leaderboard"}},
		status: http.StatusNotFound}
}

const homeDescription = "Viewbot, follow-bot and chat-bot signals for every channel that reaches the Twitch top 200, " +
	"scored minute by minute from viewers, chat and follows."

func (s *Server) homePage(ctx context.Context, r *http.Request) (page, error) {
	rows, err := s.db.Query(ctx, `
		SELECT login, display_name, best_rank FROM channels
		WHERE best_rank IS NOT NULL ORDER BY best_rank, login LIMIT 100`)
	if err != nil {
		return page{}, err
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (link, error) {
		var login, name string
		var rank int
		err := row.Scan(&login, &name, &rank)
		return link{Href: "/channel/" + login, Text: name, Note: fmt.Sprintf("best rank #%d", rank)}, err
	})
	if err != nil {
		return page{}, err
	}
	return page{
		Title: siteName + ": Twitch viewbot signals for the top 200 channels", Description: homeDescription, Path: "/",
		JSONLD: map[string]any{
			"@context": "https://schema.org", "@type": "WebSite", "name": siteName, "url": s.base + "/",
			"description": homeDescription,
			"publisher":   map[string]any{"@type": "Organization", "name": "SUBCULT", "url": "https://subcult.tv"},
			"potentialAction": map[string]any{
				"@type": "SearchAction", "target": s.base + "/search?q={search_term_string}",
				"query-input": "required name=search_term_string",
			},
		},
		H1: "Leaderboard",
		Intro: "Channels that have reached the Twitch top 200. A high quotient means the data has patterns that bots produce; " +
			"it is not proof of botting.",
		Links: links,
	}, nil
}

func (s *Server) channelPage(ctx context.Context, r *http.Request) (page, error) {
	var (
		login, name     string
		rank            *int
		since           time.Time
		quotient, conf  *float64
		scored, streams int
	)
	err := s.db.QueryRow(ctx, `
		SELECT c.login, c.display_name, c.best_rank, c.first_seen_at, c.quotient, c.quotient_confidence,
		       (SELECT count(*) FROM stream_scores sc WHERE sc.channel_id = c.id AND sc.final),
		       (SELECT count(*) FROM streams s WHERE s.channel_id = c.id)
		FROM channels c WHERE c.login = lower($1)`, r.PathValue("login")).
		Scan(&login, &name, &rank, &since, &quotient, &conf, &scored, &streams)
	if err != nil {
		return page{}, err
	}
	rows, err := s.db.Query(ctx, `
		SELECT id::text, started_at, coalesce(title, ''), coalesce(game_name, '') FROM streams
		WHERE channel_id = (SELECT id FROM channels WHERE login = $1) ORDER BY started_at DESC LIMIT 20`, login)
	if err != nil {
		return page{}, err
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (link, error) {
		var id, t, game string
		var at time.Time
		err := row.Scan(&id, &at, &t, &game)
		return link{Href: "/stream/" + id, Text: day(at) + ": " + orUntitled(t), Note: game}, err
	})
	if err != nil {
		return page{}, err
	}
	var facts []string
	if quotient != nil && conf != nil && *conf >= 0.1 {
		facts = append(facts, fmt.Sprintf("sus quotient %d of 100 from %s (%d%% confidence)",
			int(math.Round(*quotient)), plural(scored, "scored stream"), int(math.Round(*conf*100))))
	} else {
		facts = append(facts, "not scored yet")
	}
	if rank != nil {
		facts = append(facts, fmt.Sprintf("best rank #%d", *rank))
	}
	desc := fmt.Sprintf("%s on Twitch: %s. Viewers, chat and follows for each stream since %s. "+
		"A high quotient is not proof of botting.", name, strings.Join(facts, ", "), day(since))
	return page{
		Title: title(name + ": Twitch viewers, chat and sus quotient"), Description: desc, Path: "/channel/" + login,
		JSONLD: breadcrumbs(s.base, link{Href: "/channel/" + login, Text: name}),
		H1:     name, Intro: desc, Links: links,
	}, nil
}

func (s *Server) streamPage(ctx context.Context, r *http.Request) (page, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return page{}, pgx.ErrNoRows
	}
	var (
		login, name, t, game string
		started              time.Time
		peak                 *int
		quotient, conf       *float64
		final                *bool
	)
	err = s.db.QueryRow(ctx, `
		SELECT c.login, c.display_name, coalesce(st.title, ''), coalesce(st.game_name, ''), st.started_at, st.peak_viewers,
		       sc.quotient, sc.confidence, sc.final
		FROM streams st JOIN channels c ON c.id = st.channel_id LEFT JOIN stream_scores sc ON sc.stream_id = st.id
		WHERE st.id = $1`, id).Scan(&login, &name, &t, &game, &started, &peak, &quotient, &conf, &final)
	if err != nil {
		return page{}, err
	}
	desc := name + " streamed"
	if game != "" {
		desc += " " + game
	}
	desc += " on " + day(started)
	if peak != nil {
		desc += fmt.Sprintf(", peaking at %s viewers", thousands(*peak))
	}
	desc += ". "
	if quotient != nil && conf != nil && *conf >= 0.1 {
		kind := "Sus quotient"
		if final != nil && !*final {
			kind = "Provisional sus quotient"
		}
		desc += fmt.Sprintf("%s %d of 100 (%d%% confidence). ", kind, int(math.Round(*quotient)), int(math.Round(*conf*100)))
	}
	desc += "Viewers, chat and follows minute by minute."
	p := "/stream/" + strconv.FormatInt(id, 10)
	return page{
		Title: title(fmt.Sprintf("%s stream, %s", name, day(started))), Description: desc, Path: p,
		JSONLD: breadcrumbs(s.base, link{Href: "/channel/" + login, Text: name}, link{Href: p, Text: orUntitled(t)}),
		H1:     orUntitled(t), Intro: desc,
		Links: []link{{Href: "/channel/" + login, Text: "All streams by " + name}},
	}, nil
}

func (s *Server) categoryPage(ctx context.Context, r *http.Request) (page, error) {
	id := r.PathValue("id")
	if id == "" || len(id) > 20 || strings.Trim(id, "0123456789") != "" {
		return page{}, pgx.ErrNoRows
	}
	var name string
	if err := s.db.QueryRow(ctx, `
		SELECT game_name FROM streams WHERE game_id = $1 AND game_name IS NOT NULL
		ORDER BY last_seen_at DESC LIMIT 1`, id).Scan(&name); err != nil {
		return page{}, err
	}
	desc := fmt.Sprintf("Tracked Twitch channels that streamed %s in the last 90 days, with each channel's stream scores "+
		"compared against similar categories.", name)
	p := "/category/" + id
	return page{
		Title: title(name + ": tracked Twitch channels"), Description: desc, Path: p,
		JSONLD: breadcrumbs(s.base, link{Href: p, Text: name}),
		H1:     name, Intro: desc,
	}, nil
}

var (
	aboutPage = page{
		Title: title("How the sus quotient works"), Path: "/about",
		Description: "How Headcount scores Twitch streams for view-bot, follow-bot and chat-bot patterns: the twelve " +
			"detectors, their weights, confidence, and the limits of the method.",
		H1: "How scoring works",
		Intro: "Every channel that reaches the Twitch top 200 is tracked from then on. Each minute the collector records its " +
			"viewer count, its chat activity and its follower total. When a stream ends, detectors each rate one kind of " +
			"evidence and are combined into a quotient from 0 to 100. A high quotient says the data looks botted, not who did it.",
	}
	engagementPage = page{
		Title: title("Twitch chat engagement map"), Path: "/engagement",
		Description: "Every recently scored Twitch stream plotted by audience size against chatters per 1,000 viewers, " +
			"with the typical band for each size, language and category.",
		H1: "Engagement map",
		Intro: "Every recently scored stream, placed by audience size and how many of those viewers chat. Streams far below " +
			"the band have a big audience but a quiet chat, which is what bot viewers produce. That is a signal, not proof.",
	}
	searchPage = page{
		Title: title("Search"), Path: "/search", NoIndex: true,
		Description: "Search the Twitch channels and categories Headcount tracks.",
		H1:          "Search", Intro: "Search the Twitch channels and categories Headcount tracks.",
	}
)

func breadcrumbs(base string, trail ...link) map[string]any {
	items := []map[string]any{{"@type": "ListItem", "position": 1, "name": siteName, "item": base + "/"}}
	for i, l := range trail {
		items = append(items, map[string]any{"@type": "ListItem", "position": i + 2, "name": l.Text, "item": base + l.Href})
	}
	return map[string]any{"@context": "https://schema.org", "@type": "BreadcrumbList", "itemListElement": items}
}

func day(t time.Time) string { return t.UTC().Format("Jan 2, 2006") }

func orUntitled(t string) string {
	if strings.TrimSpace(t) == "" {
		return "Untitled stream"
	}
	return t
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func robots(base string) http.HandlerFunc {
	body := "User-agent: *\nDisallow: /api/\n\nSitemap: " + base + "/sitemap.xml\n"
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write([]byte(body))
	}
}

// sitemap lists the site's pages, every channel, categories streamed in the
// last 90 days and streams from the last 14. It is rebuilt at most hourly.
func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	raw := s.cached("sitemap", time.Hour)
	if raw == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		rows, err := s.db.Query(ctx, `
			SELECT '/channel/' || login, greatest(last_live_at, quotient_updated_at, first_seen_at) FROM channels
			UNION ALL
			SELECT '/category/' || game_id, max(last_seen_at) FROM streams
			WHERE game_id IS NOT NULL AND game_name IS NOT NULL AND last_seen_at > now() - interval '90 days' GROUP BY game_id
			UNION ALL
			(SELECT '/stream/' || id, coalesce(ended_at, last_seen_at) FROM streams
			 WHERE started_at > now() - interval '14 days' ORDER BY started_at DESC LIMIT 40000)`)
		if err != nil {
			s.fail(w, err)
			return
		}
		var b bytes.Buffer
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
		for _, p := range []string{"/", "/about", "/engagement"} {
			fmt.Fprintf(&b, "<url><loc>%s%s</loc></url>\n", s.base, p)
		}
		for rows.Next() {
			var p string
			var mod *time.Time
			if err := rows.Scan(&p, &mod); err != nil {
				rows.Close()
				s.fail(w, err)
				return
			}
			b.WriteString("<url><loc>")
			template.HTMLEscape(&b, []byte(s.base+urlPath(p)))
			b.WriteString("</loc>")
			if mod != nil {
				fmt.Fprintf(&b, "<lastmod>%s</lastmod>", mod.UTC().Format("2006-01-02"))
			}
			b.WriteString("</url>\n")
		}
		if err := rows.Err(); err != nil {
			s.fail(w, err)
			return
		}
		b.WriteString("</urlset>\n")
		raw = b.Bytes()
		s.store("sitemap", raw)
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(raw)
}

// urlPath escapes each segment of p for use in a URL.
func urlPath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
