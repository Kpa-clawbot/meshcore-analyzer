package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/geofilter"
	"github.com/meshcore-analyzer/packetpath"
)

// AreaEntry defines a geographic area by polygon or bounding box.
type AreaEntry struct {
	Label   string       `json:"label"`
	Polygon [][2]float64 `json:"polygon,omitempty"`
	LatMin  *float64     `json:"latMin,omitempty"`
	LatMax  *float64     `json:"latMax,omitempty"`
	LonMin  *float64     `json:"lonMin,omitempty"`
	LonMax  *float64     `json:"lonMax,omitempty"`
}

// ListLimitsConfig defines maximum row limits for list endpoints to prevent DoS.
type ListLimitsConfig struct {
	PacketsMax         int `json:"packetsMax"`
	NodesMax           int `json:"nodesMax"`
	AnalyticsMax       int `json:"analyticsMax"`
	ChannelMessagesMax int `json:"channelMessagesMax"`
	BulkHealthMax      int `json:"bulkHealthMax"`
}

// Config mirrors the Node.js config.json structure (read-only fields).
type Config struct {
	Port       int               `json:"port"`
	APIKey     string            `json:"apiKey"`
	DBPath     string            `json:"dbPath"`
	ListLimits *ListLimitsConfig `json:"listLimits"`

	// NodeBlacklist is a list of public keys to exclude from all API responses.
	// Blacklisted nodes are hidden from node lists, search, detail, map, and stats.
	// Use this to filter out trolls, nodes with offensive names, or nodes
	// reporting deliberately false data (e.g. wrong GPS position) that the
	// operator refuses to fix.
	NodeBlacklist []string `json:"nodeBlacklist"`

	// HiddenNamePrefixes is a list of name prefixes that mark a node as
	// hidden from API responses (issue #1181). The default `["🚫"]` mirrors
	// a convention used by other MeshCore map dashboards: operators who
	// rename their node with the prefix get hidden from the map without
	// waiting for normal retention to clear stale data. DB rows are
	// preserved — the filter is applied at the API layer only, so the
	// underlying observation history remains intact.
	HiddenNamePrefixes []string `json:"hiddenNamePrefixes"`

	// hiddenPrefixesPtr holds the active prefix slice as an atomic pointer.
	// Read path (IsNameHidden) is a single atomic load — no mutex, no
	// sync.Once. Writers always replace the whole slice; readers see either
	// the old or the new slice as a single value, never a partial state.
	// Mirrors blacklistSetPtr.
	hiddenPrefixesPtr atomic.Pointer[[]string]

	// hiddenPrefixesGen is a monotonic counter bumped every time the
	// hidden-prefix list mutates via SetHiddenNamePrefixes. Cache wiring
	// is left for follow-up; the counter is the prerequisite primitive
	// callers will key on (mirrors blacklistGen / #1629).
	hiddenPrefixesGen atomic.Uint64

	// blacklistSetPtr holds the active lookup set as an atomic pointer.
	// Read path is a single atomic load — no mutex, no sync.Once. Writers
	// always replace the whole map; readers see either the old or the new
	// map as a single value, never a partially-built one.
	blacklistSetPtr atomic.Pointer[map[string]bool]

	// blacklistGen is a monotonic generation counter bumped every time the
	// blacklist mutates via SetNodeBlacklist. Callers that cache responses
	// keyed by pubkey (e.g. /api/nodes/{pubkey}/reach, #1629) include this
	// generation in their cache key so any blacklist change naturally
	// invalidates prior entries on the next request.
	blacklistGen atomic.Uint64

	Branding   map[string]interface{} `json:"branding"`
	Theme      map[string]interface{} `json:"theme"`
	ThemeDark  map[string]interface{} `json:"themeDark"`
	NodeColors map[string]interface{} `json:"nodeColors"`
	TypeColors map[string]interface{} `json:"typeColors"`
	Home       map[string]interface{} `json:"home"`

	// #1488 — marker stroke (outline) settings. Operators dial color, width
	// and opacity to soften the default white outline when hundreds of
	// nodes feel overwhelming. Frontend reads these as CSS vars; see
	// public/customize-v2.js applyCSS markerStroke block.
	MarkerStroke map[string]interface{} `json:"markerStroke,omitempty"`

	MapDefaults struct {
		Center []float64 `json:"center"`
		Zoom   int       `json:"zoom"`
	} `json:"mapDefaults"`

	Regions map[string]string `json:"regions"`

	Roles            map[string]interface{} `json:"roles"`
	HealthThresholds *HealthThresholds      `json:"healthThresholds"`
	PathTrust        *PathTrustConfig       `json:"pathTrust,omitempty"`
	Map              map[string]interface{} `json:"map"`
	Tiles            map[string]interface{} `json:"tiles"` // deprecated
	SnrThresholds    map[string]interface{} `json:"snrThresholds"`
	DistThresholds   map[string]interface{} `json:"distThresholds"`
	MaxHopDist       *float64               `json:"maxHopDist"`
	Limits           map[string]interface{} `json:"limits"`
	PerfSlowMs       *int                   `json:"perfSlowMs"`
	WsReconnectMs    *int                   `json:"wsReconnectMs"`
	CacheInvalidMs   *int                   `json:"cacheInvalidateMs"`
	ExternalUrls     map[string]interface{} `json:"externalUrls"`

	LiveMap struct {
		PropagationBufferMs int `json:"propagationBufferMs"`
		MaxNodes            int `json:"maxNodes"`
	} `json:"liveMap"`

	CacheTTL map[string]interface{} `json:"cacheTTL"`

	Retention *RetentionConfig `json:"retention,omitempty"`

	DB *DBConfig `json:"db,omitempty"`

	PacketStore *PacketStoreConfig `json:"packetStore,omitempty"`

	// Runtime holds Go runtime tuning knobs (#1010).
	// Currently exposes runtime.maxMemoryMB which sets a soft memory limit
	// (GOMEMLIMIT) via runtime/debug.SetMemoryLimit at startup. The
	// GOMEMLIMIT environment variable, when set, takes precedence.
	Runtime   *RuntimeConfig   `json:"runtime,omitempty"`
	GeoFilter *GeoFilterConfig `json:"geo_filter,omitempty"`

	Areas map[string]AreaEntry `json:"areas,omitempty"`

	Timestamps *TimestampConfig `json:"timestamps,omitempty"`

	// CORSAllowedOrigins is the list of origins permitted to make cross-origin
	// requests. When empty (default), no Access-Control-* headers are sent,
	// so browsers enforce same-origin policy. Set to ["*"] to allow all origins.
	CORSAllowedOrigins []string `json:"corsAllowedOrigins,omitempty"`

	// WebSocket carries the /ws transport limits from #1794. Omitted entirely
	// means: rate limit at its default, connection cap off, no deny list.
	WebSocket *WebSocketConfig `json:"webSocket,omitempty"`

	DebugAffinity bool `json:"debugAffinity,omitempty"`

	// MapDarkTileProvider selects the default dark-mode basemap provider for
	// new visitors. Deprecated: use Map.Tiles.DarkDefault instead.
	MapDarkTileProvider string `json:"mapDarkTileProvider,omitempty"`

	// ObserverBlacklist is a list of observer public keys to exclude from API
	// responses (defense in depth — ingestor drops at ingest, server filters
	// any that slipped through from a prior unblocked window).
	ObserverBlacklist []string `json:"observerBlacklist,omitempty"`

	// obsBlacklistSetCached is the lazily-built set version of ObserverBlacklist.
	obsBlacklistSetCached map[string]bool
	obsBlacklistOnce      sync.Once

	Compression *CompressionConfig `json:"compression,omitempty"`

	// ClientRxCoverage gates the opt-in mobile client-RX coverage feature
	// (corescope-rx companions publishing GPS-tagged receptions). Absent/nil
	// ⇒ off; see ClientRxCoverageEnabled.
	ClientRxCoverage *ClientRxCoverageConfig `json:"clientRxCoverage,omitempty"`

	ResolvedPath  *ResolvedPathConfig  `json:"resolvedPath,omitempty"`
	NeighborGraph *NeighborGraphConfig `json:"neighborGraph,omitempty"`

	// Observers cache settings (#1481 P0-3 / #1483).
	ObserversCache *ObserversCacheConfig `json:"observersCache,omitempty"`

	// Analytics steady-state background recompute (issue #1240).
	Analytics *AnalyticsConfig `json:"analytics,omitempty"`

	// BatteryThresholds: voltage cutoffs for low/critical alerts (#663).
	BatteryThresholds *BatteryThresholdsConfig `json:"batteryThresholds,omitempty"`

	// Customizer controls operator-side knobs for the in-app customizer modal
	// (theme/branding/etc.). See CustomizerConfig and issue #1508.
	Customizer *CustomizerConfig `json:"customizer,omitempty"`

	// Known-channels catalogue integration (issue #1323).
	// URL of a JSON catalogue file (channels-by-country shape) fetched
	// periodically and exposed via /api/known-channels. Empty disables.
	KnownChannelsURL string `json:"knownChannelsUrl,omitempty"`
	// Refresh interval in milliseconds. 0/missing => default 24h.
	KnownChannelsRefreshMs int64 `json:"knownChannelsRefreshMs,omitempty"`
}

// CustomizerConfig holds operator-side knobs for the in-app customizer modal.
// Today only DisabledTabs is exposed: a list of tab ids the operator wants to
// hide from end users (e.g. ["branding","geofilter","export"]). The frontend
// (public/customize-v2.js _renderTabs) reads this from /api/config/client and
// filters those tabs out before rendering. Issue #1508.
type CustomizerConfig struct {
	DisabledTabs []string `json:"disabledTabs"`
}

// weakAPIKeys is the blocklist of known default/example API keys that must be rejected.
var weakAPIKeys = map[string]bool{
	"your-secret-api-key-here": true,
	"change-me":                true,
	"example":                  true,
	"test":                     true,
	"password":                 true,
	"admin":                    true,
	"apikey":                   true,
	"api-key":                  true,
	"secret":                   true,
	"default":                  true,
}

// IsWeakAPIKey returns true if the key is in the blocklist or shorter than 16 characters.
func IsWeakAPIKey(key string) bool {
	if key == "" {
		return false // empty is handled separately (endpoints disabled)
	}
	if weakAPIKeys[strings.ToLower(key)] {
		return true
	}
	if len(key) < 16 {
		return true
	}
	return false
}

// CompressionConfig controls HTTP gzip and WebSocket permessage-deflate compression.
// Both are disabled by default — enable only when the upstream proxy does not already compress.
type CompressionConfig struct {
	GZip      bool `json:"gzip"`
	Websocket bool `json:"websocket"`

	// Level is the gzip compression level (1=BestSpeed … 9=BestCompression).
	// 0 / out-of-range means "use compress/gzip.DefaultCompression".
	Level int `json:"level,omitempty"`

	// MinSizeBytes is an advisory minimum response size below which gzip
	// would not pay off. Currently informational — kept here so operators
	// can express intent and so future small-body fast-paths can use it.
	MinSizeBytes int `json:"minSizeBytes,omitempty"`

	// ContentTypes overrides the default compressible-MIME allow-list. When
	// empty, a conservative default (application/json, text/html, text/css,
	// application/javascript, text/plain, image/svg+xml, application/xml)
	// is used. Already-compressed types (image/*, video/*, application/zip,
	// application/x-gzip, …) are always skipped.
	ContentTypes []string `json:"contentTypes,omitempty"`
}

// GZipEnabled returns true when HTTP gzip compression is explicitly enabled.
func (c *Config) GZipEnabled() bool {
	return c.Compression != nil && c.Compression.GZip
}

// ClientRxCoverageConfig gates the opt-in mobile client-RX coverage feature.
type ClientRxCoverageConfig struct {
	Enabled bool `json:"enabled"`
}

// ClientRxCoverageEnabled reports whether the opt-in mobile client-RX coverage
// feature is on. Nil config or absent/nil section ⇒ off (the safe default).
func (c *Config) ClientRxCoverageEnabled() bool {
	return c != nil && c.ClientRxCoverage != nil && c.ClientRxCoverage.Enabled
}

// WSCompressionEnabled returns true when WebSocket permessage-deflate is explicitly enabled.
func (c *Config) WSCompressionEnabled() bool {
	return c.Compression != nil && c.Compression.Websocket
}

// ResolvedPathConfig controls async backfill behavior.
type ResolvedPathConfig struct {
	BackfillHours int `json:"backfillHours"` // how far back (hours) to scan for NULL resolved_path (default 24)
}

// NeighborGraphConfig controls neighbor edge pruning.
type NeighborGraphConfig struct {
	MaxAgeDays int     `json:"maxAgeDays"` // edges older than this are pruned (default 5)
	MaxEdgeKm  float64 `json:"maxEdgeKm"`  // geo-implausibility threshold (km); 0 = default 500; negative disables (#1228)

	// CacheRecomputeIntervalSeconds: cadence for the background
	// recomputer that rebuilds the default-shape neighbor-graph
	// response (#1481 P0-1). 0/missing = default 300 (5 min).
	// Lower = fresher data, more CPU per minute. #1483.
	CacheRecomputeIntervalSeconds int `json:"cacheRecomputeIntervalSeconds,omitempty"`
}

// ObserversCacheConfig controls the /api/observers default-shape cache.
// #1481 P0-3 / #1483.
type ObserversCacheConfig struct {
	// TTLSeconds: how long the cached default-shape /api/observers
	// response is served before a singleflight-collapsed refill.
	// 0/missing = default 30. Lower = fresher data, more SQL pressure.
	TTLSeconds int `json:"ttlSeconds,omitempty"`
}

// PacketStoreConfig controls in-memory packet store limits.
type PacketStoreConfig struct {
	RetentionHours                float64 `json:"retentionHours"`                // max age of packets in hours (0 = unlimited)
	MaxMemoryMB                   int     `json:"maxMemoryMB"`                   // hard memory ceiling in MB (0 = unlimited)
	MaxResolvedPubkeyIndexEntries int     `json:"maxResolvedPubkeyIndexEntries"` // warning threshold for index size (0 = 5M default)
	HotStartupHours               float64 `json:"hotStartupHours"`               // load only this many hours synchronously; 0 = disabled
}

// GeoFilterConfig is an alias for the shared geofilter.Config type.
type GeoFilterConfig = geofilter.Config

// PathTrustConfig is an alias for the shared packetpath.TrustConfig type
// (issue #1784). See packetpath.TrustConfig for the full doc comment.
type PathTrustConfig = packetpath.TrustConfig

// RuntimeConfig holds Go runtime tuning knobs (#1010).
type RuntimeConfig struct {
	// MaxMemoryMB sets the Go soft memory limit (GOMEMLIMIT) in MiB via
	// runtime/debug.SetMemoryLimit at startup. Takes precedence over the
	// implicit limit derived from packetStore.maxMemoryMB. The GOMEMLIMIT
	// environment variable, when set, takes precedence over this value.
	// 0/unset preserves default behavior.
	MaxMemoryMB int `json:"maxMemoryMB"`
}

type RetentionConfig struct {
	NodeDays     int `json:"nodeDays"`
	ObserverDays int `json:"observerDays"`
	PacketDays   int `json:"packetDays"`
	MetricsDays  int `json:"metricsDays"`
}

// DBConfig is the shared SQLite vacuum/maintenance config (#919, #921).
type DBConfig = dbconfig.DBConfig

// IncrementalVacuumPages returns the configured pages per vacuum or 1024 default.
func (c *Config) IncrementalVacuumPages() int {
	if c.DB != nil && c.DB.IncrementalVacuumPages > 0 {
		return c.DB.IncrementalVacuumPages
	}
	return 1024
}

// MetricsRetentionDays returns configured metrics retention or 30 days default.
func (c *Config) MetricsRetentionDays() int {
	if c.Retention != nil && c.Retention.MetricsDays > 0 {
		return c.Retention.MetricsDays
	}
	return 30
}

// BackfillHours returns configured backfill window or 24h default.
func (c *Config) BackfillHours() int {
	if c.ResolvedPath != nil && c.ResolvedPath.BackfillHours > 0 {
		return c.ResolvedPath.BackfillHours
	}
	return 24
}

// NeighborMaxAgeDays returns configured max edge age or 30 days default.
func (c *Config) NeighborMaxAgeDays() int {
	if c.NeighborGraph != nil && c.NeighborGraph.MaxAgeDays > 0 {
		return c.NeighborGraph.MaxAgeDays
	}
	return 5
}

// NeighborMaxEdgeKm returns the geo-implausibility threshold in km.
// 0 (unset) → DefaultMaxEdgeKm (500). Negative → 0 (filter disabled).
// See issue #1228.
func (c *Config) NeighborMaxEdgeKm() float64 {
	if c == nil || c.NeighborGraph == nil || c.NeighborGraph.MaxEdgeKm == 0 {
		return DefaultMaxEdgeKm
	}
	if c.NeighborGraph.MaxEdgeKm < 0 {
		return 0
	}
	return c.NeighborGraph.MaxEdgeKm
}

type TimestampConfig struct {
	DefaultMode       string `json:"defaultMode"`       // "ago" | "absolute"
	Timezone          string `json:"timezone"`          // "local" | "utc"
	FormatPreset      string `json:"formatPreset"`      // "iso" | "iso-seconds" | "locale"
	CustomFormat      string `json:"customFormat"`      // freeform, only used when AllowCustomFormat=true
	AllowCustomFormat bool   `json:"allowCustomFormat"` // admin gate
}

func defaultTimestampConfig() TimestampConfig {
	return TimestampConfig{
		DefaultMode:       "ago",
		Timezone:          "local",
		FormatPreset:      "iso",
		CustomFormat:      "",
		AllowCustomFormat: false,
	}
}

// NodeDaysOrDefault returns the configured retention.nodeDays or 7 if not set.
func (c *Config) NodeDaysOrDefault() int {
	if c.Retention != nil && c.Retention.NodeDays > 0 {
		return c.Retention.NodeDays
	}
	return 7
}

// ObserverDaysOrDefault returns the configured retention.observerDays or 14 if not set.
// A value of -1 means observers are never removed.
func (c *Config) ObserverDaysOrDefault() int {
	if c.Retention != nil && c.Retention.ObserverDays != 0 {
		return c.Retention.ObserverDays
	}
	return 14
}

type HealthThresholds struct {
	InfraDegradedHours float64 `json:"infraDegradedHours"`
	InfraSilentHours   float64 `json:"infraSilentHours"`
	NodeDegradedHours  float64 `json:"nodeDegradedHours"`
	NodeSilentHours    float64 `json:"nodeSilentHours"`
	// RelayActiveHours: how recent a path-hop appearance must be for a
	// repeater to be considered "actively relaying" vs only "alive
	// (advert-only)". See issue #662. Defaults to 24h.
	RelayActiveHours float64 `json:"relayActiveHours"`
	// Issue #1552 — observer health classification thresholds (minutes).
	// Defaults match prior hardcoded behavior in public/observers.js (10/60).
	ObserverOnlineMinutes int `json:"observerOnlineMinutes"`
	ObserverStaleMinutes  int `json:"observerStaleMinutes"`
}

// ThemeFile mirrors theme.json overlay.
type ThemeFile struct {
	Branding   map[string]interface{} `json:"branding"`
	Theme      map[string]interface{} `json:"theme"`
	ThemeDark  map[string]interface{} `json:"themeDark"`
	NodeColors map[string]interface{} `json:"nodeColors"`
	TypeColors map[string]interface{} `json:"typeColors"`
	Home       map[string]interface{} `json:"home"`
	// #1488 — marker stroke overlay from theme.json.
	MarkerStroke map[string]interface{} `json:"markerStroke,omitempty"`
}

func LoadConfig(baseDirs ...string) (*Config, error) {
	if len(baseDirs) == 0 {
		baseDirs = []string{"."}
	}
	paths := make([]string, 0)
	for _, d := range baseDirs {
		paths = append(paths, filepath.Join(d, "config.json"))
		paths = append(paths, filepath.Join(d, "data", "config.json"))
	}

	cfg := &Config{Port: 3000}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			continue
		}
		cfg.NormalizeTimestampConfig()
		cfg.migrateDeprecatedConfig()
		cfg.applyListLimitsDefaults()
		applyCORSEnv(cfg)
		return cfg, nil
	}
	cfg.NormalizeTimestampConfig()
	cfg.migrateDeprecatedConfig()
	cfg.applyListLimitsDefaults()
	applyCORSEnv(cfg)
	return cfg, nil // defaults
}

// WebSocketConfig holds the /ws transport limits (#1794). These sit behind
// the CheckOrigin allowlist (#1793) and catch non-browser clients, which can
// omit or forge Origin.
type WebSocketConfig struct {
	// MaxConnsPerIP caps concurrent /ws connections from one client address.
	// DEFAULT 0, meaning OFF, and that is a deliberate departure from the
	// value floated on #1794.
	//
	// A cap of 5 is safe only when one address means one household. It does
	// not on mobile: carrier-grade NAT puts thousands of unrelated subscribers
	// behind a single public IPv4, so a low cap would refuse real visitors on
	// phones while barely inconveniencing a scraper that can rent more
	// addresses. Operators who know their audience can set it; we must not
	// pick it for them.
	MaxConnsPerIP int `json:"maxConnsPerIP,omitempty"`

	// UpgradesPerMinPerIP caps handshakes per minute from one client address.
	// DEFAULT 30, on. Unlike the connection cap this is safe under CGNAT: a
	// real client upgrades a handful of times per minute even while
	// reconnecting, so 30 leaves ordinary traffic untouched while flattening
	// the reconnect loop that makes scrapers expensive.
	UpgradesPerMinPerIP *int `json:"upgradesPerMinPerIP,omitempty"`

	// TrustedProxies lists the addresses or CIDRs of reverse proxies whose
	// X-Forwarded-For may be believed. REQUIRED for the limits to do anything
	// behind nginx/Caddy/Traefik/ingress: without it every visitor shares the
	// proxy's address, so the limits are skipped and a warning is logged.
	// Never list an address you do not control; anyone reaching the server
	// from a trusted address can name any client IP they like.
	TrustedProxies []string `json:"trustedProxies,omitempty"`

	// Deny blocks addresses outright at the upgrade, before the handshake.
	// Accepts both bare addresses ("1.2.3.4") and CIDRs ("1.2.3.0/24").
	// Applies even when clients cannot be told apart, because it is an
	// explicit instruction rather than an inference.
	Deny []string `json:"deny,omitempty"`
}

// WSMaxConnsPerIP returns the configured concurrent-connection cap, 0 = off.
func (c *Config) WSMaxConnsPerIP() int {
	if c.WebSocket == nil || c.WebSocket.MaxConnsPerIP < 0 {
		return 0
	}
	return c.WebSocket.MaxConnsPerIP
}

// WSUpgradesPerMinPerIP returns the upgrade-rate cap, defaulting to 30 when
// unset. A pointer distinguishes "not configured" (use the default) from an
// explicit 0, which an operator uses to turn the limit off.
func (c *Config) WSUpgradesPerMinPerIP() int {
	if c.WebSocket == nil || c.WebSocket.UpgradesPerMinPerIP == nil {
		return 30
	}
	if v := *c.WebSocket.UpgradesPerMinPerIP; v > 0 {
		return v
	}
	return 0
}

// WSTrustedProxies returns the configured reverse-proxy addresses, if any.
func (c *Config) WSTrustedProxies() []string {
	if c.WebSocket == nil {
		return nil
	}
	return c.WebSocket.TrustedProxies
}

// WSDeny returns the configured deny entries, if any.
func (c *Config) WSDeny() []string {
	if c.WebSocket == nil {
		return nil
	}
	return c.WebSocket.Deny
}

func (c *Config) applyListLimitsDefaults() {
	if c.ListLimits == nil {
		c.ListLimits = &ListLimitsConfig{}
	}
	if c.ListLimits.PacketsMax <= 0 {
		c.ListLimits.PacketsMax = 10000
	}
	if c.ListLimits.NodesMax <= 0 {
		c.ListLimits.NodesMax = 2000
	}
	if c.ListLimits.AnalyticsMax <= 0 {
		c.ListLimits.AnalyticsMax = 200
	}
	if c.ListLimits.ChannelMessagesMax <= 0 {
		c.ListLimits.ChannelMessagesMax = 500
	}
	if c.ListLimits.BulkHealthMax <= 0 {
		c.ListLimits.BulkHealthMax = 200
	}
}

func (c *Config) migrateDeprecatedConfig() {
	migrated := false
	if c.Map == nil {
		c.Map = make(map[string]interface{})
	}
	if c.Map["tiles"] == nil {
		c.Map["tiles"] = make(map[string]interface{})
	}
	tilesMap, ok := c.Map["tiles"].(map[string]interface{})
	if !ok {
		return
	}

	if c.MapDarkTileProvider != "" {
		if tilesMap["darkDefault"] == nil {
			tilesMap["darkDefault"] = c.MapDarkTileProvider
		}
		migrated = true
	}
	if len(c.Tiles) > 0 {
		for k, v := range c.Tiles {
			if tilesMap[k] == nil {
				tilesMap[k] = v
			}
		}
		migrated = true
	}
	if migrated {
		fmt.Fprintf(os.Stderr, "[deprecated] Top-level 'mapDarkTileProvider' and 'tiles' keys in config.json are deprecated and will be ignored in v3.5.0 (see #1165). Please move them into 'map': { 'tiles': { ... } }.\n")
	}
}

func LoadTheme(baseDirs ...string) *ThemeFile {
	if len(baseDirs) == 0 {
		baseDirs = []string{"."}
	}
	for _, d := range baseDirs {
		for _, name := range []string{"theme.json"} {
			p := filepath.Join(d, name)
			data, err := os.ReadFile(p)
			if err != nil {
				p = filepath.Join(d, "data", name)
				data, err = os.ReadFile(p)
				if err != nil {
					continue
				}
			}
			var t ThemeFile
			if json.Unmarshal(data, &t) == nil {
				return &t
			}
		}
	}
	return &ThemeFile{}
}

func (c *Config) GetHealthThresholds() HealthThresholds {
	h := HealthThresholds{
		InfraDegradedHours: 24,
		InfraSilentHours:   72,
		NodeDegradedHours:  1,
		NodeSilentHours:    24,
		RelayActiveHours:   24,
	}
	if c.HealthThresholds != nil {
		if c.HealthThresholds.InfraDegradedHours > 0 {
			h.InfraDegradedHours = c.HealthThresholds.InfraDegradedHours
		}
		if c.HealthThresholds.InfraSilentHours > 0 {
			h.InfraSilentHours = c.HealthThresholds.InfraSilentHours
		}
		if c.HealthThresholds.NodeDegradedHours > 0 {
			h.NodeDegradedHours = c.HealthThresholds.NodeDegradedHours
		}
		if c.HealthThresholds.NodeSilentHours > 0 {
			h.NodeSilentHours = c.HealthThresholds.NodeSilentHours
		}
		if c.HealthThresholds.RelayActiveHours > 0 {
			h.RelayActiveHours = c.HealthThresholds.RelayActiveHours
		}
		if c.HealthThresholds.ObserverOnlineMinutes > 0 {
			h.ObserverOnlineMinutes = c.HealthThresholds.ObserverOnlineMinutes
		}
		if c.HealthThresholds.ObserverStaleMinutes > 0 {
			h.ObserverStaleMinutes = c.HealthThresholds.ObserverStaleMinutes
		}
	}
	if h.ObserverOnlineMinutes <= 0 {
		h.ObserverOnlineMinutes = 60
	}
	if h.ObserverStaleMinutes <= 0 {
		h.ObserverStaleMinutes = 1440
	}
	return h
}

// GetPathTrust returns the effective path-trust config, applying
// DefaultMinHashBytesForMapping when unset (issue #1784).
func (c *Config) GetPathTrust() PathTrustConfig {
	if c != nil && c.PathTrust != nil {
		return *c.PathTrust
	}
	return PathTrustConfig{MinHashBytesForMapping: packetpath.DefaultMinHashBytesForMapping}
}

// GetHealthMs returns degraded/silent thresholds in ms for a given role.
func (h HealthThresholds) GetHealthMs(role string) (degradedMs, silentMs int) {
	const hourMs = 3600000
	if role == "repeater" || role == "room" {
		return int(h.InfraDegradedHours * hourMs), int(h.InfraSilentHours * hourMs)
	}
	return int(h.NodeDegradedHours * hourMs), int(h.NodeSilentHours * hourMs)
}

// ToClientMs returns the thresholds as ms for the frontend.
func (h HealthThresholds) ToClientMs() map[string]int {
	const hourMs = 3600000
	const minMs = 60000
	return map[string]int{
		"infraDegradedMs":  int(h.InfraDegradedHours * hourMs),
		"infraSilentMs":    int(h.InfraSilentHours * hourMs),
		"nodeDegradedMs":   int(h.NodeDegradedHours * hourMs),
		"nodeSilentMs":     int(h.NodeSilentHours * hourMs),
		"observerOnlineMs": h.ObserverOnlineMinutes * minMs,
		"observerStaleMs":  h.ObserverStaleMinutes * minMs,
	}
}

func (c *Config) ResolveDBPath(baseDir string) string {
	if c.DBPath != "" {
		return c.DBPath
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		return v
	}
	return filepath.Join(baseDir, "data", "meshcore.db")
}

func (c *Config) NormalizeTimestampConfig() {
	defaults := defaultTimestampConfig()
	if c.Timestamps == nil {
		log.Printf("[config] timestamps not configured - using defaults (ago/local/iso)")
		c.Timestamps = &defaults
		return
	}

	origMode := c.Timestamps.DefaultMode
	mode := strings.ToLower(strings.TrimSpace(origMode))
	switch mode {
	case "ago", "absolute":
		c.Timestamps.DefaultMode = mode
	default:
		log.Printf("[config] warning: timestamps.defaultMode=%q is invalid, using %q", origMode, defaults.DefaultMode)
		c.Timestamps.DefaultMode = defaults.DefaultMode
	}

	origTimezone := c.Timestamps.Timezone
	timezone := strings.ToLower(strings.TrimSpace(origTimezone))
	switch timezone {
	case "local", "utc":
		c.Timestamps.Timezone = timezone
	default:
		log.Printf("[config] warning: timestamps.timezone=%q is invalid, using %q", origTimezone, defaults.Timezone)
		c.Timestamps.Timezone = defaults.Timezone
	}

	origPreset := c.Timestamps.FormatPreset
	formatPreset := strings.ToLower(strings.TrimSpace(origPreset))
	switch formatPreset {
	case "iso", "iso-seconds", "locale":
		c.Timestamps.FormatPreset = formatPreset
	default:
		log.Printf("[config] warning: timestamps.formatPreset=%q is invalid, using %q", origPreset, defaults.FormatPreset)
		c.Timestamps.FormatPreset = defaults.FormatPreset
	}
}

func (c *Config) GetTimestampConfig() TimestampConfig {
	if c == nil || c.Timestamps == nil {
		return defaultTimestampConfig()
	}
	return *c.Timestamps
}
func (c *Config) PropagationBufferMs() int {
	if c.LiveMap.PropagationBufferMs > 0 {
		return c.LiveMap.PropagationBufferMs
	}
	return 5000
}

// LiveMapMaxNodes returns the operator-configured cap on how many nodes
// the live map fetches (and thus renders) in a single page. Default is
// 2000; values are clamped to [100, 20000] to defang misconfig.
// Negative/zero falls back to default. See #1574.
func (c *Config) LiveMapMaxNodes() int {
	const def = 2000
	const min = 100
	const max = 20000
	if c == nil || c.LiveMap.MaxNodes <= 0 {
		return def
	}
	v := c.LiveMap.MaxNodes
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// buildBlacklistSet recomputes the lookup set from pks and returns it.
// Empty/whitespace-only entries are skipped. Keys are lowercased + trimmed.
// Returns nil for an empty effective set so callers can `len(m) == 0` short-circuit.
func buildBlacklistSet(pks []string) map[string]bool {
	if len(pks) == 0 {
		return nil
	}
	m := make(map[string]bool, len(pks))
	for _, pk := range pks {
		trimmed := strings.ToLower(strings.TrimSpace(pk))
		if trimmed != "" {
			m[trimmed] = true
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// SetNodeBlacklist atomically replaces NodeBlacklist with pks, rebuilds the
// lookup set, and bumps the generation counter so any cache keyed on the
// generation invalidates on the next request (#1629). Safe for concurrent
// use with IsBlacklisted / BlacklistGeneration.
func (c *Config) SetNodeBlacklist(pks []string) {
	if c == nil {
		return
	}
	// Copy so callers can mutate their slice without affecting us.
	cp := make([]string, len(pks))
	copy(cp, pks)
	c.NodeBlacklist = cp
	m := buildBlacklistSet(cp)
	c.blacklistSetPtr.Store(&m)
	c.blacklistGen.Add(1)
}

// BlacklistGeneration returns a monotonic counter that increments on every
// SetNodeBlacklist call. Response caches keyed per-pubkey embed this value
// in their cache key so any blacklist mutation invalidates prior entries on
// the next request (#1629).
func (c *Config) BlacklistGeneration() uint64 {
	if c == nil {
		return 0
	}
	return c.blacklistGen.Load()
}

// IsBlacklisted returns true if the given public key is in the nodeBlacklist.
// Hot read path: a single atomic pointer load + map lookup. No locks, no
// sync.Once. The in-memory set is populated either via SetNodeBlacklist or
// lazily on first read from c.NodeBlacklist (covering the JSON-load path
// where the setter was never called).
func (c *Config) IsBlacklisted(pubkey string) bool {
	if c == nil {
		return false
	}
	mp := c.blacklistSetPtr.Load()
	if mp == nil {
		// Lazy first-read materialisation from the JSON-loaded slice.
		// CAS-style: if another goroutine wins the race, drop ours.
		built := buildBlacklistSet(c.NodeBlacklist)
		if c.blacklistSetPtr.CompareAndSwap(nil, &built) {
			mp = &built
		} else {
			mp = c.blacklistSetPtr.Load()
		}
	}
	if mp == nil || len(*mp) == 0 {
		return false
	}
	return (*mp)[strings.ToLower(strings.TrimSpace(pubkey))]
}

// IsNameHidden returns true if the given node name starts with any of the
// operator-configured HiddenNamePrefixes (issue #1181). Empty/whitespace
// prefixes are ignored. Used to drop nodes from /api/nodes, /api/nodes/search
// and /api/nodes/{pubkey} without deleting the underlying DB row, so observer
// history stays intact even after the operator hides the node.
//
// Hot read path: a single atomic pointer load. No locks, no sync.Once.
// Writers always replace the whole slice; readers see either the old or
// the new slice as a single value, never a partially-built one. Mirrors
// IsBlacklisted's CAS-style lazy first-read materialisation for the
// JSON-load path where SetHiddenNamePrefixes was never called.
func (c *Config) IsNameHidden(name string) bool {
	if c == nil {
		return false
	}
	pp := c.hiddenPrefixesPtr.Load()
	if pp == nil {
		// Lazy first-read materialisation from the JSON-loaded slice.
		// CAS-style: if another goroutine wins the race, drop ours.
		built := make([]string, len(c.HiddenNamePrefixes))
		copy(built, c.HiddenNamePrefixes)
		if c.hiddenPrefixesPtr.CompareAndSwap(nil, &built) {
			pp = &built
		} else {
			pp = c.hiddenPrefixesPtr.Load()
		}
	}
	if pp == nil || len(*pp) == 0 {
		return false
	}
	for _, p := range *pp {
		if p == "" {
			continue
		}
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// SetHiddenNamePrefixes atomically replaces HiddenNamePrefixes with the
// given slice and bumps the generation counter. Safe for concurrent use
// with IsNameHidden / HiddenNamePrefixesGeneration. Mirrors
// SetNodeBlacklist (#1629).
func (c *Config) SetHiddenNamePrefixes(prefixes []string) {
	if c == nil {
		return
	}
	cp := make([]string, len(prefixes))
	copy(cp, prefixes)
	c.HiddenNamePrefixes = cp
	c.hiddenPrefixesPtr.Store(&cp)
	c.hiddenPrefixesGen.Add(1)
}

// HiddenNamePrefixesGeneration returns a monotonic counter that increments
// on every SetHiddenNamePrefixes call. Response caches keyed per-pubkey can
// embed this value in their cache key so any prefix mutation invalidates
// prior entries on the next request — same pattern as BlacklistGeneration.
func (c *Config) HiddenNamePrefixesGeneration() uint64 {
	if c == nil {
		return 0
	}
	return c.hiddenPrefixesGen.Load()
}

// SaveGeoFilter writes the geo_filter section back to config.json on disk.
// Pass gf=nil to remove the filter. The rest of config.json is preserved as-is.
func SaveGeoFilter(configDir string, gf *GeoFilterConfig) error {
	var configPath string
	for _, p := range []string{
		filepath.Join(configDir, "config.json"),
		filepath.Join(configDir, "data", "config.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			configPath = p
			break
		}
	}
	if configPath == "" {
		return fmt.Errorf("config.json not found in %s", configDir)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	// Parse as a raw map so non-struct fields (_comment, etc.) are preserved.
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	if gf == nil || len(gf.Polygon) == 0 {
		delete(raw, "geo_filter")
	} else {
		// Round-trip through JSON to get a plain interface{} value.
		b, _ := json.Marshal(gf)
		var v interface{}
		_ = json.Unmarshal(b, &v)
		raw["geo_filter"] = v
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	out = append(out, '\n')

	// Atomic write: temp file + rename.
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

// obsBlacklistSet lazily builds and caches the observerBlacklist as a set for O(1) lookups.
func (c *Config) obsBlacklistSet() map[string]bool {
	c.obsBlacklistOnce.Do(func() {
		if len(c.ObserverBlacklist) == 0 {
			return
		}
		m := make(map[string]bool, len(c.ObserverBlacklist))
		for _, pk := range c.ObserverBlacklist {
			trimmed := strings.ToLower(strings.TrimSpace(pk))
			if trimmed != "" {
				m[trimmed] = true
			}
		}
		c.obsBlacklistSetCached = m
	})
	return c.obsBlacklistSetCached
}

// IsObserverBlacklisted returns true if the given observer ID is in the observerBlacklist.
func (c *Config) IsObserverBlacklisted(id string) bool {
	if c == nil || len(c.ObserverBlacklist) == 0 {
		return false
	}
	return c.obsBlacklistSet()[strings.ToLower(strings.TrimSpace(id))]
}

// AnalyticsConfig controls steady-state background recompute of
// analytics endpoints (issue #1240).
//
// DefaultIntervalSeconds applies to every endpoint that does not have
// an explicit per-endpoint override in RecomputeIntervalSeconds. The
// project default is 300 (5 minutes): the operator's guiding principle
// is "serving slightly stale data quickly is better than real-time
// data slowly." Lower values give fresher data at higher CPU cost.
//
// RecomputeIntervalSeconds keys (all optional):
//
//	topology, rf, distance, channels, hashCollisions, hashSizes, roles, observersClockSkew, nodesClockSkew
type AnalyticsConfig struct {
	DefaultIntervalSeconds   int            `json:"defaultIntervalSeconds,omitempty"`
	RecomputeIntervalSeconds map[string]int `json:"recomputeIntervalSeconds,omitempty"`
	// LoRaPreset is the assumed PHY preset used by the relay-airtime-share
	// metric to compute true Time-on-Air (issue #1768). Defaults to the
	// EU MeshCore deployment: 869.6 MHz / BW 62.5 kHz / SF 8 / CR 4/5.
	// freq is informational only and surfaces in the analytics caption.
	LoRaPreset *LoRaPresetConfig `json:"loraPreset,omitempty"`
}

// LoRaPresetConfig is the user-facing PHY preset for ToA scoring.
// Only the four free params live here; CRC/IH/DE are firmware-fixed
// in internal/lora and intentionally not surfaced as config.
type LoRaPresetConfig struct {
	FreqHz float64 `json:"freq,omitempty"` // e.g. 869.6e6
	BWkHz  float64 `json:"bw,omitempty"`   // e.g. 62.5
	SF     int     `json:"sf,omitempty"`   // e.g. 8
	CR     int     `json:"cr,omitempty"`   // 5..8 (denominator suffix of 4/5..4/8)
}

// AnalyticsDefaultRecomputeInterval returns the configured default
// recompute interval, or 5 minutes if unset/invalid.
func (c *Config) AnalyticsDefaultRecomputeInterval() time.Duration {
	if c != nil && c.Analytics != nil && c.Analytics.DefaultIntervalSeconds > 0 {
		return time.Duration(c.Analytics.DefaultIntervalSeconds) * time.Second
	}
	return 5 * time.Minute
}

// AnalyticsRecomputeIntervals returns the per-endpoint override map.
// Returns the zero value (all defaults) if the analytics block is
// absent or empty.
func (c *Config) AnalyticsRecomputeIntervals() AnalyticsRecomputeIntervals {
	out := AnalyticsRecomputeIntervals{}
	if c == nil || c.Analytics == nil || c.Analytics.RecomputeIntervalSeconds == nil {
		return out
	}
	get := func(key string) time.Duration {
		v, ok := c.Analytics.RecomputeIntervalSeconds[key]
		if !ok || v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	}
	out.Topology = get("topology")
	out.RF = get("rf")
	out.Distance = get("distance")
	out.Channels = get("channels")
	out.HashCollisions = get("hashCollisions")
	out.HashSizes = get("hashSizes")
	out.Roles = get("roles")
	out.ObserversClockSkew = get("observersClockSkew")
	out.NodesClockSkew = get("nodesClockSkew")
	return out
}
