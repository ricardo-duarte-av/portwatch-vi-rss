package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is loaded from config.yaml. Every field has a sensible default, so
// an empty file (or no file at all) is a valid configuration.
type Config struct {
	// Listen is the ip:port the HTTP server binds to.
	Listen string `yaml:"listen"`
	// TrackerURL is the JSON document the news items are read from.
	TrackerURL string `yaml:"tracker_url"`
	// SiteURL is the base used to build article links (SiteURL + "news/" + id).
	SiteURL string `yaml:"site_url"`
	// PublicURL is where this service is reachable from the outside, used for
	// the feeds' self links. When empty it is derived from each request.
	PublicURL string `yaml:"public_url"`
	// RefreshInterval is how often the tracker is polled. Upstream refreshes
	// roughly every 3 hours; requests are conditional so polling is cheap.
	RefreshInterval time.Duration `yaml:"refresh_interval"`
	// HTTPTimeout bounds a single upstream fetch.
	HTTPTimeout time.Duration `yaml:"http_timeout"`
	// UserAgent is sent on upstream requests.
	UserAgent string `yaml:"user_agent"`
	// MaxItems caps the number of entries in each feed (0 = no cap).
	MaxItems int `yaml:"max_items"`
	// FullContent includes the full article body (plus sources and image) in
	// the feed. When false, only the one-line summary is included.
	FullContent bool `yaml:"full_content"`

	Feed FeedMeta `yaml:"feed"`
}

type FeedMeta struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Author      string `yaml:"author"`
	Language    string `yaml:"language"`
}

func defaultConfig() Config {
	return Config{
		Listen:          "0.0.0.0:8080",
		TrackerURL:      "https://portwatch-vi.xyz/data/tracker.json",
		SiteURL:         "https://portwatch-vi.xyz/",
		RefreshInterval: 15 * time.Minute,
		HTTPTimeout:     30 * time.Second,
		UserAgent:       "portwatch-vi-rss/1.0 (+feed generator)",
		MaxItems:        50,
		FullContent:     true,
		Feed: FeedMeta{
			Title:       "PortWatch VI news",
			Description: "Takedowns, leaks, legal news, scams and real progress on running GTA VI on PC.",
			Author:      "PortWatch VI",
			Language:    "en",
		},
	}
}

// loadConfig reads path on top of the defaults. A missing file is not an
// error, so the binary also runs with zero configuration.
func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// keep defaults
	case err != nil:
		return cfg, fmt.Errorf("read config: %w", err)
	default:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.Listen == "" {
		return errors.New("listen must not be empty")
	}
	if c.RefreshInterval < time.Minute {
		return fmt.Errorf("refresh_interval must be at least 1m, got %s", c.RefreshInterval)
	}
	if c.HTTPTimeout <= 0 {
		return errors.New("http_timeout must be positive")
	}
	if c.MaxItems < 0 {
		return errors.New("max_items must be >= 0")
	}
	for name, p := range map[string]*string{"tracker_url": &c.TrackerURL, "site_url": &c.SiteURL, "public_url": &c.PublicURL} {
		if *p == "" {
			if name == "public_url" {
				continue
			}
			return fmt.Errorf("%s must not be empty", name)
		}
		u, err := url.Parse(*p)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s is not a valid http(s) URL: %q", name, *p)
		}
	}
	c.SiteURL = strings.TrimSuffix(c.SiteURL, "/") + "/"
	c.PublicURL = strings.TrimSuffix(c.PublicURL, "/")
	return nil
}
