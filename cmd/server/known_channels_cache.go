package main

// Known-channels catalogue cache (issue #1323).
//
// Fetches a community-maintained catalogue of hashtag channels (default:
// https://raw.githubusercontent.com/marcelverdult/meshcore-channels/main/channels-by-country.json)
// every N hours into an in-memory snapshot. Never blocks startup; never
// blocks UI on the fetch; fail-soft to last-known. No DB, no disk cache.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultKnownChannelsURL is the suggested upstream catalogue, pinned to a
// specific commit SHA so a hostile or compromised future commit on the
// community repo cannot be silently fetched by deployments that opt in.
// Operators should periodically bump this pin (see config.example.json).
// NOTE: this constant is only used by tests and as documentation — the
// feature is OPT-IN: an empty cfg.KnownChannelsURL leaves the cache
// disabled (no background fetch, /api/known-channels serves empty).
const DefaultKnownChannelsURL = "https://raw.githubusercontent.com/marcelverdult/meshcore-channels/072bc25b6fc983aa2aa7e9d399a97a5f4899ea71/channels-by-country.json"

// DefaultKnownChannelsRefresh is the default refresh interval (24h).
const DefaultKnownChannelsRefresh = 24 * time.Hour

// maxKnownChannelsBytes caps the upstream response size we are willing to
// parse (the catalogue is ~80 KB today; 4 MB ceiling is plenty of headroom
// and bounds memory if upstream ever ships a malicious oversize payload).
const maxKnownChannelsBytes = 4 * 1024 * 1024

// KnownChannelEntry is one catalogue entry, region-stamped.
type KnownChannelEntry struct {
	Channel     string `json:"channel"` // e.g. "#antwerpen" (# prefix preserved)
	Description string `json:"description,omitempty"`
	Key         string `json:"key,omitempty"` // optional PSK (base64) — present for some entries
	Region      string `json:"region"`        // ISO 3166-1 alpha-2 lowercase
	RegionName  string `json:"regionName,omitempty"`
}

// KnownChannelsSnapshot is the immutable parsed catalogue surfaced over /api.
type KnownChannelsSnapshot struct {
	GeneratedAt string              `json:"generatedAt,omitempty"` // upstream generation timestamp
	License     string              `json:"license,omitempty"`
	FetchedAt   time.Time           `json:"fetchedAt"`
	Source      string              `json:"source"`
	Entries     []KnownChannelEntry `json:"entries"`
}

// upstreamPayload mirrors the channels-by-country.json shape.
type upstreamPayload struct {
	GeneratedAt  string                              `json:"generated_at"`
	License      string                              `json:"license"`
	Countries    map[string][]upstreamCountryChannel `json:"countries"`
	CountryNames map[string]string                   `json:"countryNames,omitempty"` // optional extension
}

type upstreamCountryChannel struct {
	Channel     string `json:"channel"`
	Description string `json:"description"`
	Key         string `json:"key,omitempty"`
}

// parseKnownChannelsJSON parses the upstream JSON into a snapshot.
// Tolerant: missing/empty countries are skipped silently; entries with
// empty channel strings are dropped.
func parseKnownChannelsJSON(raw []byte, source string, now time.Time) (*KnownChannelsSnapshot, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty payload")
	}
	var p upstreamPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode catalogue: %w", err)
	}
	out := &KnownChannelsSnapshot{
		GeneratedAt: p.GeneratedAt,
		License:     p.License,
		FetchedAt:   now,
		Source:      source,
		Entries:     make([]KnownChannelEntry, 0, 256),
	}
	for code, list := range p.Countries {
		if len(list) == 0 {
			continue
		}
		region := strings.ToLower(strings.TrimSpace(code))
		name := p.CountryNames[code]
		for _, c := range list {
			ch := strings.TrimSpace(c.Channel)
			if ch == "" {
				continue
			}
			out.Entries = append(out.Entries, KnownChannelEntry{
				Channel:     ch,
				Description: c.Description,
				Key:         c.Key,
				Region:      region,
				RegionName:  name,
			})
		}
	}
	return out, nil
}

// filterSnapshotByRegion returns a copy filtered to the given region
// (case-insensitive). Empty/whitespace region returns the original snapshot
// (entry slice shared — callers must not mutate). Unknown region returns
// a snapshot with an empty (but non-nil) Entries slice so JSON marshals as `[]`.
func filterSnapshotByRegion(snap *KnownChannelsSnapshot, region string) *KnownChannelsSnapshot {
	if snap == nil {
		return nil
	}
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" {
		return snap
	}
	out := &KnownChannelsSnapshot{
		GeneratedAt: snap.GeneratedAt,
		License:     snap.License,
		FetchedAt:   snap.FetchedAt,
		Source:      snap.Source,
		Entries:     []KnownChannelEntry{},
	}
	for _, e := range snap.Entries {
		if e.Region == region {
			out.Entries = append(out.Entries, e)
		}
	}
	return out
}

// knownChannelsCache holds the atomic snapshot pointer + config.
type knownChannelsCache struct {
	ptr     atomic.Pointer[KnownChannelsSnapshot]
	url     string
	refresh time.Duration
	client  *http.Client

	fetchCount atomic.Int64 // # successful upstream fetches
	failCount  atomic.Int64 // # failed fetches (fail-soft)
}

func newKnownChannelsCache(url string, refresh time.Duration) *knownChannelsCache {
	if refresh <= 0 {
		refresh = DefaultKnownChannelsRefresh
	}
	return &knownChannelsCache{
		url:     url,
		refresh: refresh,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// load returns the current snapshot or nil if never populated.
func (c *knownChannelsCache) load() *KnownChannelsSnapshot {
	return c.ptr.Load()
}

// fetchOnce performs a single upstream fetch. Updates ptr on success;
// leaves last-known snapshot in place on failure (fail-soft).
func (c *knownChannelsCache) fetchOnce(ctx context.Context) error {
	if c.url == "" {
		return errors.New("known channels url not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		c.failCount.Add(1)
		return err
	}
	req.Header.Set("User-Agent", "CoreScope-KnownChannels/1.0 (+https://github.com/Kpa-clawbot/CoreScope)")
	resp, err := c.client.Do(req)
	if err != nil {
		c.failCount.Add(1)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.failCount.Add(1)
		return fmt.Errorf("upstream status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKnownChannelsBytes))
	if err != nil {
		c.failCount.Add(1)
		return err
	}
	snap, err := parseKnownChannelsJSON(body, c.url, time.Now())
	if err != nil {
		c.failCount.Add(1)
		return err
	}
	c.ptr.Store(snap)
	c.fetchCount.Add(1)
	return nil
}

// run kicks off the background fetch loop in a new goroutine. Does an
// initial fetch (fail-soft) and then ticks every refresh interval until
// ctx is cancelled. Never blocks the caller — startup proceeds immediately
// even if the upstream is slow or unreachable.
func (c *knownChannelsCache) run(ctx context.Context) {
	if c.url == "" {
		return
	}
	go func() {
		_ = c.fetchOnce(ctx) // initial fetch, fail-soft
		t := time.NewTicker(c.refresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = c.fetchOnce(ctx)
			}
		}
	}()
}
