// Command portwatch-vi-rss serves RSS 2.0 and Atom feeds of the news
// published on https://portwatch-vi.xyz/, built from its data/tracker.json.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on the configured listen address and exit (for container health checks)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(2)
	}

	if *healthcheck {
		os.Exit(runHealthcheck(cfg))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fetcher := NewFetcher(cfg)
	go fetcher.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           newMux(cfg, fetcher),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("listening", "addr", cfg.Listen, "tracker", cfg.TrackerURL, "refresh", cfg.RefreshInterval)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func newMux(cfg Config, f *Fetcher) *http.ServeMux {
	mux := http.NewServeMux()
	rss := feedHandler(cfg, f, "application/rss+xml; charset=utf-8", renderRSS)
	atom := feedHandler(cfg, f, "application/atom+xml; charset=utf-8", renderAtom)
	for _, p := range []string{"/rss", "/rss.xml", "/feed.xml"} {
		mux.Handle("GET "+p, rss)
	}
	for _, p := range []string{"/atom", "/atom.xml"} {
		mux.Handle("GET "+p, atom)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		data, last, err := f.Snapshot()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if data == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "no data yet (last error: %v)\n", err)
			return
		}
		fmt.Fprintf(w, "ok: %d items, last successful fetch %s\n", len(data.News), last.UTC().Format(time.RFC3339))
		if err != nil {
			fmt.Fprintf(w, "last attempt failed: %v\n", err)
		}
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		base := baseURL(cfg, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>%[1]s</title>
<link rel="alternate" type="application/rss+xml" title="%[1]s (RSS)" href="%[2]s/rss.xml">
<link rel="alternate" type="application/atom+xml" title="%[1]s (Atom)" href="%[2]s/atom.xml">
</head><body><h1>%[1]s</h1><ul>
<li><a href="%[2]s/rss.xml">RSS 2.0</a></li>
<li><a href="%[2]s/atom.xml">Atom 1.0</a></li>
</ul></body></html>
`, htmlEscape(cfg.Feed.Title), htmlEscape(base))
	})
	return mux
}

type renderFunc func(Config, *Tracker, string) ([]byte, error)

func feedHandler(cfg Config, f *Fetcher, contentType string, render renderFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _, _ := f.Snapshot()
		if data == nil {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "feed not available yet, upstream has not been fetched", http.StatusServiceUnavailable)
			return
		}
		body, err := render(cfg, data, baseURL(cfg, r)+r.URL.Path)
		if err != nil {
			slog.Error("render feed", "path", r.URL.Path, "err", err)
			http.Error(w, "failed to render feed", http.StatusInternalServerError)
			return
		}
		sum := sha256.Sum256(body)
		etag := `"` + hex.EncodeToString(sum[:16]) + `"`
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=300")
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(body)
	})
}

// baseURL is the configured public_url, or one derived from the request
// (honouring X-Forwarded-Proto/Host when behind a reverse proxy).
func baseURL(cfg Config, r *http.Request) string {
	if cfg.PublicURL != "" {
		return cfg.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.TrimSpace(strings.Split(h, ",")[0])
	}
	return scheme + "://" + host
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// runHealthcheck lets distroless images (no shell, no curl) run a Docker
// HEALTHCHECK by invoking the binary itself.
func runHealthcheck(cfg Config) int {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy:", resp.Status)
		return 1
	}
	return 0
}
