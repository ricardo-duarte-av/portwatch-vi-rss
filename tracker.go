package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Tracker is the subset of data/tracker.json this service cares about.
type Tracker struct {
	Updated time.Time  `json:"updated"`
	News    []NewsItem `json:"news"`
}

type NewsItem struct {
	ID        string   `json:"id"`
	Date      string   `json:"date"`      // YYYY-MM-DD
	UpdatedAt string   `json:"updatedAt"` // YYYY-MM-DD or RFC 3339, optional
	Category  string   `json:"category"`
	Title     string   `json:"title"`
	Dek       string   `json:"dek"`
	Body      []string `json:"body"`
	Sources   []Source `json:"sources"`
	Image     *Image   `json:"image"`
}

type Source struct {
	Outlet string `json:"outlet"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

type Image struct {
	URL    string `json:"url"`
	Credit string `json:"credit"`
	Link   string `json:"link"`
}

// Fetcher polls the tracker and keeps the latest good copy in memory.
type Fetcher struct {
	cfg    Config
	client *http.Client

	mu           sync.RWMutex
	data         *Tracker
	etag         string
	lastModified string
	lastAttempt  time.Time
	lastSuccess  time.Time
	lastErr      error
}

func NewFetcher(cfg Config) *Fetcher {
	return &Fetcher{cfg: cfg, client: &http.Client{Timeout: cfg.HTTPTimeout}}
}

// Snapshot returns the current tracker data (nil until the first success).
func (f *Fetcher) Snapshot() (*Tracker, time.Time, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.data, f.lastSuccess, f.lastErr
}

// Run fetches immediately and then on every refresh interval until ctx ends.
// After a failure it retries sooner (1 minute) instead of waiting a full cycle.
func (f *Fetcher) Run(ctx context.Context) {
	for {
		wait := f.cfg.RefreshInterval
		if err := f.refresh(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("tracker refresh failed", "err", err)
			wait = min(wait, time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (f *Fetcher) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.cfg.TrackerURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", f.cfg.UserAgent)
	req.Header.Set("Accept", "application/json")
	f.mu.RLock()
	if f.data != nil {
		if f.etag != "" {
			req.Header.Set("If-None-Match", f.etag)
		}
		if f.lastModified != "" {
			req.Header.Set("If-Modified-Since", f.lastModified)
		}
	}
	f.mu.RUnlock()

	err = func() error {
		resp, err := f.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotModified {
			slog.Debug("tracker not modified")
			return nil
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected status %s", resp.Status)
		}
		// The document is ~250 KB today; 32 MB leaves plenty of headroom.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
		var t Tracker
		if err := json.Unmarshal(body, &t); err != nil {
			return fmt.Errorf("decode tracker: %w", err)
		}
		if len(t.News) == 0 {
			return fmt.Errorf("tracker has no news items; keeping previous data")
		}
		f.mu.Lock()
		f.data = &t
		f.etag = resp.Header.Get("ETag")
		f.lastModified = resp.Header.Get("Last-Modified")
		f.mu.Unlock()
		slog.Info("tracker refreshed", "items", len(t.News), "updated", t.Updated)
		return nil
	}()

	f.mu.Lock()
	f.lastAttempt = time.Now()
	f.lastErr = err
	if err == nil {
		f.lastSuccess = f.lastAttempt
	}
	f.mu.Unlock()
	return err
}
