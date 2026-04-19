package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/meshcore-analyzer/geofilter"
)

// Config mirrors the Node.js config.json structure (read-only fields).
type Config struct {
	Port    int    `json:"port"`
	APIKey  string `json:"apiKey"`
	DBPath  string `json:"dbPath"`

	// NodeBlacklist is a list of public keys to exclude from all API responses.
	// Blacklisted nodes are hidden from node lists, search, detail, map, and stats.
	// Use this to filter out trolls, nodes with offensive names, or nodes
	// reporting deliberately false data (e.g. wrong GPS position) that the
	// operator refuses to fix.
	NodeBlacklist []string `json:"nodeBlacklist"`

	// blacklistSetCached is the lazily-built set version of NodeBlacklist.
	blacklistSetCached map[string]bool
	blacklistOnce      sync.Once

	Branding   map[string]interface{} `json:"branding"`
	Theme      map[string]interface{} `json:"theme"`
	ThemeDark  map[string]interface{} `json:"themeDark"`
	NodeColors map[string]interface{} `json:"nodeColors"`
	TypeColors map[string]interface{} `json:"typeColors"`
	Home       map[string]interface{} `json:"home"`

	MapDefaults struct {
		Center []float64 `json:"center"`
		Zoom   int       `json:"zoom"`
	} `json:"mapDefaults"`

	Regions map[string]string `json:"regions"`

	Roles            map[string]interface{} `json:"roles"`
	HealthThresholds *HealthThresholds      `json:"healthThresholds"`
	Tiles            map[string]interface{} `json:"tiles"`
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
	} `json:"liveMap"`

	CacheTTL map[string]interface{} `json:"cacheTTL"`

	Retention *RetentionConfig `json:"retention,omitempty"`

	PacketStore *PacketStoreConfig `json:"packetStore,omitempty"`

	GeoFilter *GeoFilterConfig `json:"geo_filter,omitempty"`

	Timestamps *TimestampConfig `json:"timestamps,omitempty"`

	DebugAffinity bool `json:"debugAffinity,omitempty"`

	ResolvedPath  *ResolvedPathConfig  `json:"resolvedPath,omitempty"`
	NeighborGraph *NeighborGraphConfig `json:"neighborGraph,omitempty"`
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

// ResolvedPathConfig controls async backfill behavior.
type ResolvedPathConfig struct {
	BackfillHours int `json:"backfillHours"` // how far back (hours) to scan for NULL resolved_path (default 24)
}

// NeighborGraphConfig controls neighbor edge pruning.
type NeighborGraphConfig struct {
	MaxAgeDays int `json:"maxAgeDays"` // edges older than this are pruned (default 5)
}

// PacketStoreConfig controls in-memory packet store limits.
type PacketStoreConfig struct {
	RetentionHours float64 `json:"retentionHours"` // max age of packets in hours (0 = unlimited)
	MaxMemoryMB    int     `json:"maxMemoryMB"`     // hard memory ceiling in MB (0 = unlimited)
}

// GeoFilterConfig is an alias for the shared geofilter.Config type.
type GeoFilterConfig = geofilter.Config

type RetentionConfig struct {
	NodeDays      int `json:"nodeDays"`
	ObserverDays  int `json:"observerDays"`
	PacketDays    int `json:"packetDays"`
	MetricsDays   int `json:"metricsDays"`
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
}

// ThemeFile mirrors theme.json overlay.
type ThemeFile struct {
	Branding   map[string]interface{} `json:"branding"`
	Theme      map[string]interface{} `json:"theme"`
	ThemeDark  map[string]interface{} `json:"themeDark"`
	NodeColors map[string]interface{} `json:"nodeColors"`
	TypeColors map[string]interface{} `json:"typeColors"`
	Home       map[string]interface{} `json:"home"`
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
		return cfg, nil
	}
	cfg.NormalizeTimestampConfig()
	return cfg, nil // defaults
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
	}
	return h
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
	return map[string]int{
		"infraDegradedMs": int(h.InfraDegradedHours * hourMs),
		"infraSilentMs":   int(h.InfraSilentHours * hourMs),
		"nodeDegradedMs":  int(h.NodeDegradedHours * hourMs),
		"nodeSilentMs":    int(h.NodeSilentHours * hourMs),
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
// blacklistSet lazily builds and caches the nodeBlacklist as a set for O(1) lookups.
// Uses sync.Once to eliminate the data race on first concurrent access.
func (c *Config) blacklistSet() map[string]bool {
	c.blacklistOnce.Do(func() {
		if len(c.NodeBlacklist) == 0 {
			return
		}
		m := make(map[string]bool, len(c.NodeBlacklist))
		for _, pk := range c.NodeBlacklist {
			trimmed := strings.ToLower(strings.TrimSpace(pk))
			if trimmed != "" {
				m[trimmed] = true
			}
		}
		c.blacklistSetCached = m
	})
	return c.blacklistSetCached
}

// IsBlacklisted returns true if the given public key is in the nodeBlacklist.
func (c *Config) IsBlacklisted(pubkey string) bool {
	if c == nil || len(c.NodeBlacklist) == 0 {
		return false
	}
	return c.blacklistSet()[strings.ToLower(strings.TrimSpace(pubkey))]
}
