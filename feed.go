package main

import (
	"encoding/xml"
	"html"
	"net/url"
	"strings"
	"time"
)

// entry is the format-neutral representation shared by the RSS and Atom writers.
type entry struct {
	ID, Link, Title, Summary, Content, Category string
	Published, Updated                          time.Time
}

// buildEntries turns tracker news into feed entries, newest first.
//
// News items only carry a calendar date. To give readers a stable order
// within a day, the first item of a date (the newest, as the tracker lists
// news newest-first) gets 12:00 UTC and each following item of the same date
// is one minute earlier.
func buildEntries(cfg Config, t *Tracker) []entry {
	perDay := map[string]int{}
	out := make([]entry, 0, len(t.News))
	for _, n := range t.News {
		if n.ID == "" || n.Title == "" {
			continue
		}
		day, err := time.Parse(time.DateOnly, n.Date)
		if err != nil {
			continue
		}
		k := perDay[n.Date]
		perDay[n.Date]++
		published := day.Add(12*time.Hour - time.Duration(k)*time.Minute)

		updated := published
		if u, ok := parseFlexibleTime(n.UpdatedAt); ok {
			if len(n.UpdatedAt) == len(time.DateOnly) {
				u = u.Add(12*time.Hour - time.Duration(k)*time.Minute)
			}
			if u.After(updated) {
				updated = u
			}
		}

		link := cfg.SiteURL + "news/" + url.PathEscape(n.ID)
		e := entry{
			ID:        link,
			Link:      link,
			Title:     n.Title,
			Summary:   n.Dek,
			Category:  n.Category,
			Published: published,
			Updated:   updated,
		}
		if cfg.FullContent {
			e.Content = renderHTML(n)
		}
		out = append(out, e)
		if cfg.MaxItems > 0 && len(out) >= cfg.MaxItems {
			break
		}
	}
	return out
}

func parseFlexibleTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// renderHTML builds the article body. All tracker text is plain prose, so
// everything is escaped; only URLs with an http(s) scheme become links.
func renderHTML(n NewsItem) string {
	var b strings.Builder
	esc := html.EscapeString
	if n.Image != nil && safeURL(n.Image.URL) {
		b.WriteString(`<figure><img src="` + esc(n.Image.URL) + `" alt="">`)
		if n.Image.Credit != "" {
			b.WriteString("<figcaption>")
			if safeURL(n.Image.Link) {
				b.WriteString(`<a href="` + esc(n.Image.Link) + `">` + esc(n.Image.Credit) + "</a>")
			} else {
				b.WriteString(esc(n.Image.Credit))
			}
			b.WriteString("</figcaption>")
		}
		b.WriteString("</figure>")
	}
	if n.Dek != "" {
		b.WriteString("<p><strong>" + esc(n.Dek) + "</strong></p>")
	}
	for _, p := range n.Body {
		b.WriteString("<p>" + esc(p) + "</p>")
	}
	var srcs []Source
	for _, s := range n.Sources {
		if safeURL(s.URL) {
			srcs = append(srcs, s)
		}
	}
	if len(srcs) > 0 {
		b.WriteString("<h3>Sources</h3><ul>")
		for _, s := range srcs {
			title := s.Title
			if title == "" {
				title = s.URL
			}
			b.WriteString(`<li><a href="` + esc(s.URL) + `">` + esc(title) + "</a>")
			if s.Outlet != "" {
				b.WriteString(" (" + esc(s.Outlet) + ")")
			}
			b.WriteString("</li>")
		}
		b.WriteString("</ul>")
	}
	return b.String()
}

func safeURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// ---- RSS 2.0 ----

type rssDoc struct {
	XMLName   xml.Name   `xml:"rss"`
	Version   string     `xml:"version,attr"`
	AtomNS    string     `xml:"xmlns:atom,attr"`
	ContentNS string     `xml:"xmlns:content,attr"`
	Channel   rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	Language      string    `xml:"language,omitempty"`
	LastBuildDate string    `xml:"lastBuildDate"`
	Generator     string    `xml:"generator"`
	AtomLink      atomLink  `xml:"atom:link"`
	Items         []rssItem `xml:"item"`
}

type rssGUID struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssItem struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	GUID        rssGUID `xml:"guid"`
	PubDate     string  `xml:"pubDate"`
	Category    string  `xml:"category,omitempty"`
	Description string  `xml:"description"`
	Content     string  `xml:"content:encoded,omitempty"`
}

func renderRSS(cfg Config, t *Tracker, selfURL string) ([]byte, error) {
	doc := rssDoc{
		Version:   "2.0",
		AtomNS:    "http://www.w3.org/2005/Atom",
		ContentNS: "http://purl.org/rss/1.0/modules/content/",
		Channel: rssChannel{
			Title:         cfg.Feed.Title,
			Link:          cfg.SiteURL + "news/",
			Description:   cfg.Feed.Description,
			Language:      cfg.Feed.Language,
			LastBuildDate: feedUpdated(t).Format(time.RFC1123Z),
			Generator:     "portwatch-vi-rss",
			AtomLink:      atomLink{Href: selfURL, Rel: "self", Type: "application/rss+xml"},
		},
	}
	for _, e := range buildEntries(cfg, t) {
		doc.Channel.Items = append(doc.Channel.Items, rssItem{
			Title:       e.Title,
			Link:        e.Link,
			GUID:        rssGUID{IsPermaLink: true, Value: e.ID},
			PubDate:     e.Published.Format(time.RFC1123Z),
			Category:    e.Category,
			Description: e.Summary,
			Content:     e.Content,
		})
	}
	return marshal(doc)
}

// ---- Atom 1.0 ----

type atomDoc struct {
	XMLName  xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Lang     string      `xml:"xml:lang,attr,omitempty"`
	ID       string      `xml:"id"`
	Title    string      `xml:"title"`
	Subtitle string      `xml:"subtitle,omitempty"`
	Updated  string      `xml:"updated"`
	Author   atomAuthor  `xml:"author"`
	Links    []atomLink  `xml:"link"`
	Gen      string      `xml:"generator"`
	Entries  []atomEntry `xml:"entry"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
}

type atomText struct {
	Type  string `xml:"type,attr,omitempty"`
	Value string `xml:",chardata"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

type atomEntry struct {
	ID        string        `xml:"id"`
	Title     string        `xml:"title"`
	Link      atomLink      `xml:"link"`
	Published string        `xml:"published"`
	Updated   string        `xml:"updated"`
	Category  *atomCategory `xml:"category,omitempty"`
	Summary   *atomText     `xml:"summary,omitempty"`
	Content   *atomText     `xml:"content,omitempty"`
}

func renderAtom(cfg Config, t *Tracker, selfURL string) ([]byte, error) {
	doc := atomDoc{
		Lang:     cfg.Feed.Language,
		ID:       cfg.SiteURL + "news/",
		Title:    cfg.Feed.Title,
		Subtitle: cfg.Feed.Description,
		Updated:  feedUpdated(t).Format(time.RFC3339),
		Author:   atomAuthor{Name: cfg.Feed.Author},
		Links: []atomLink{
			{Href: selfURL, Rel: "self", Type: "application/atom+xml"},
			{Href: cfg.SiteURL + "news/", Rel: "alternate", Type: "text/html"},
		},
		Gen: "portwatch-vi-rss",
	}
	for _, e := range buildEntries(cfg, t) {
		ae := atomEntry{
			ID:        e.ID,
			Title:     e.Title,
			Link:      atomLink{Href: e.Link, Rel: "alternate", Type: "text/html"},
			Published: e.Published.Format(time.RFC3339),
			Updated:   e.Updated.Format(time.RFC3339),
		}
		if e.Category != "" {
			ae.Category = &atomCategory{Term: e.Category}
		}
		if e.Summary != "" {
			ae.Summary = &atomText{Type: "text", Value: e.Summary}
		}
		if e.Content != "" {
			ae.Content = &atomText{Type: "html", Value: e.Content}
		}
		doc.Entries = append(doc.Entries, ae)
	}
	return marshal(doc)
}

// feedUpdated is the tracker's own timestamp, falling back to the newest entry.
func feedUpdated(t *Tracker) time.Time {
	if !t.Updated.IsZero() {
		return t.Updated.UTC()
	}
	var newest time.Time
	for _, n := range t.News {
		if d, ok := parseFlexibleTime(n.Date); ok && d.After(newest) {
			newest = d
		}
	}
	return newest
}

func marshal(v any) ([]byte, error) {
	out, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}
