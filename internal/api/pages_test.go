package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"git.subcult.tv/subculture-collective/headcount/web"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	return New(nil, web.FS(), slog.New(slog.DiscardHandler))
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
	return w
}

func TestModuleImportsCarryVersion(t *testing.T) {
	s := &Server{web: fstest.MapFS{
		"index.html": {Data: []byte("<title>{{.Title}}</title>")},
		"app.js":     {Data: []byte("import { a } from './kit.js';\nimport { b } from './viz.js';\n")},
		"kit.js":     {Data: []byte("export const a = 1;")},
	}}
	if err := s.loadAssets(); err != nil {
		t.Fatal(err)
	}
	body := string(s.assets["/app.js"].body)
	for _, want := range []string{"'./kit.js?v=" + s.version + "'", "'./viz.js?v=" + s.version + "'"} {
		if !strings.Contains(body, want) {
			t.Errorf("app.js = %q, want it to contain %s", body, want)
		}
	}
	if _, ok := s.assets["/index.html"]; ok {
		t.Error("index.html must only be served as a rendered page")
	}
}

func TestAssetCaching(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	if got := get(t, h, "/app.js?v="+s.version).Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("current version: Cache-Control = %q, want immutable", got)
	}
	if got := get(t, h, "/app.js?v=old").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("stale version: Cache-Control = %q, want no-cache", got)
	}
	w := get(t, h, "/no-such-file.js")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `name="robots" content="noindex"`) {
		t.Errorf("missing file: status %d, want a noindex 404 page", w.Code)
	}
}

func TestStaticPagesRender(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	w := get(t, h, "/about")
	body := w.Body.String()
	for _, want := range []string{
		"<title>How the sus quotient works · Headcount</title>",
		`<link rel="canonical" href="https://headcount.subcult.tv/about">`,
		`src="/app.js?v=` + s.version + `"`,
		"<h1>How scoring works</h1>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/about is missing %s", want)
		}
	}
	if strings.Contains(body, "noindex") {
		t.Error("/about must be indexable")
	}
	if !strings.Contains(get(t, h, "/search?q=x").Body.String(), `content="noindex"`) {
		t.Error("search results must not be indexed")
	}
	if w := get(t, h, "/live"); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/?live=1" {
		t.Errorf("/live: %d %s, want a redirect to /?live=1", w.Code, w.Header().Get("Location"))
	}
	if body := get(t, h, "/robots.txt").Body.String(); !strings.Contains(body, "Sitemap: https://headcount.subcult.tv/sitemap.xml") {
		t.Errorf("robots.txt = %q", body)
	}
}

func TestJSONLDIsEscaped(t *testing.T) {
	s := testServer(t)
	p := page{Title: "t", Path: "/", JSONLD: breadcrumbs(s.base, link{Href: "/stream/1", Text: "</script><b>x"})}
	w := httptest.NewRecorder()
	s.render(w, httptest.NewRequest("GET", "/", nil), p)
	if strings.Contains(w.Body.String(), "</script><b>") {
		t.Error("a stream title broke out of the JSON-LD block")
	}
}

func TestThousands(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := thousands(in); got != want {
			t.Errorf("thousands(%d) = %s, want %s", in, got, want)
		}
	}
}
