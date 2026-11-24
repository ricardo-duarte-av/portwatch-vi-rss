package main

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const fixture = `{
  "updated": "2026-11-24T16:24:31.934Z",
  "news": [
    {"id": "2026-11-24-b", "date": "2026-11-24", "category": "Port", "title": "Second <of> day",
     "dek": "Dek B & more", "body": ["Para 1", "Para <2>"],
     "sources": [{"outlet": "X", "title": "A post", "url": "https://x.com/a"},
                 {"outlet": "Bad", "title": "js", "url": "javascript:alert(1)"}],
     "image": {"url": "https://img.example/a.png", "credit": "GitHub", "link": "https://github.com/a"}},
    {"id": "2026-11-24-a", "date": "2026-11-24", "updatedAt": "2026-11-25", "category": "Leak",
     "title": "First of day", "dek": "Dek A", "body": ["x"], "sources": [], "image": null},
    {"id": "2026-11-20-c", "date": "2026-11-20", "category": "Legal", "title": "Older",
     "dek": "Dek C", "body": [], "sources": [], "image": null},
    {"id": "bad-date", "date": "nope", "title": "Skipped"}
  ]
}`

func newTestServer(t *testing.T, cfg Config) (*httptest.Server, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(fixture))
	}))
	t.Cleanup(upstream.Close)
	cfg.TrackerURL = upstream.URL
	f := NewFetcher(cfg)
	if err := f.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	// second refresh exercises the 304 path and must keep the data
	if err := f.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newMux(cfg, f))
	t.Cleanup(srv.Close)
	return srv, upstream
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp, b.String()
}

func TestAtom(t *testing.T) {
	srv, _ := newTestServer(t, defaultConfig())
	resp, body := get(t, srv.URL+"/atom.xml")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/atom+xml") {
		t.Fatalf("status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var doc struct {
		Entries []struct {
			ID        string `xml:"id"`
			Title     string `xml:"title"`
			Published string `xml:"published"`
			Updated   string `xml:"updated"`
			Content   string `xml:"content"`
		} `xml:"entry"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, body)
	}
	if len(doc.Entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(doc.Entries))
	}
	e0, e1 := doc.Entries[0], doc.Entries[1]
	if e0.ID != "https://portwatch-vi.xyz/news/2026-11-24-b" || e0.Title != "Second <of> day" {
		t.Errorf("entry 0: %+v", e0)
	}
	if e0.Published != "2026-11-24T12:00:00Z" || e1.Published != "2026-11-24T11:59:00Z" {
		t.Errorf("intra-day ordering: %s, %s", e0.Published, e1.Published)
	}
	if e1.Updated != "2026-11-25T11:59:00Z" {
		t.Errorf("updatedAt not applied: %s", e1.Updated)
	}
	for _, want := range []string{"<p>Para &lt;2&gt;</p>", `<a href="https://x.com/a">A post</a> (X)`, `<img src="https://img.example/a.png"`} {
		if !strings.Contains(e0.Content, want) {
			t.Errorf("content missing %q:\n%s", want, e0.Content)
		}
	}
	if strings.Contains(e0.Content, "javascript:") {
		t.Errorf("unsafe URL leaked into content")
	}
	if doc.Links[0].Rel != "self" || doc.Links[0].Href != srv.URL+"/atom.xml" {
		t.Errorf("self link: %+v", doc.Links[0])
	}

	// conditional GET
	req, _ := http.NewRequest("GET", srv.URL+"/atom.xml", nil)
	req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusNotModified {
		t.Errorf("want 304, got %d", r2.StatusCode)
	}
}

func TestRSS(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxItems = 2
	cfg.PublicURL = "https://feeds.example.com"
	srv, _ := newTestServer(t, cfg)
	resp, body := get(t, srv.URL+"/rss.xml")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/rss+xml") {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var doc struct {
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				PubDate     string `xml:"pubDate"`
				Description string `xml:"description"`
				GUID        string `xml:"guid"`
				Content     string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, body)
	}
	if len(doc.Channel.Items) != 2 {
		t.Fatalf("max_items not applied: %d", len(doc.Channel.Items))
	}
	it := doc.Channel.Items[0]
	if it.PubDate != "Tue, 24 Nov 2026 12:00:00 +0000" || it.Description != "Dek B & more" || !strings.Contains(it.Content, "<p>Para 1</p>") {
		t.Errorf("item: %+v", it)
	}
	if !strings.Contains(body, `<atom:link href="https://feeds.example.com/rss.xml" rel="self"`) {
		t.Errorf("public_url self link missing:\n%s", body)
	}
	if _, err := time.Parse(time.RFC1123Z, it.PubDate); err != nil {
		t.Error(err)
	}
}

func TestNotReady(t *testing.T) {
	cfg := defaultConfig()
	srv := httptest.NewServer(newMux(cfg, NewFetcher(cfg)))
	defer srv.Close()
	for _, p := range []string{"/rss.xml", "/healthz"} {
		resp, _ := get(t, srv.URL+p)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: want 503, got %d", p, resp.StatusCode)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	cfg := defaultConfig()
	cfg.RefreshInterval = time.Second
	if cfg.validate() == nil {
		t.Error("expected refresh_interval error")
	}
	cfg = defaultConfig()
	cfg.SiteURL = "ftp://x"
	if cfg.validate() == nil {
		t.Error("expected site_url error")
	}
}
