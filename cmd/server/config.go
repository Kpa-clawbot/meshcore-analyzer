package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Config mirrors the Node.js config.json structure (read-only fields).
type Config struct {
	Port    int    `json:"port"`
	APIKey  string `json:"apiKey"`
	DBPath  string `json:"dbPath"`

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
}

// PacketStoreConfig controls in-memory packet store limits.
type PacketStoreConfig struct {
	RetentionHours float64 `json:"retentionHours"` // max age of packets in hours (0 = unlimited)
	MaxMemoryMB    int     `json:"maxMemoryMB"`     // hard memory ceiling in MB (0 = unlimited)
}

type GeoFilterConfig struct {
	Polygon  [][2]float64 `json:"polygon,omitempty"`
	BufferKm float64      `json:"bufferKm,omitempty"`
	LatMin   *float64     `json:"latMin,omitempty"`
	LatMax   *float64     `json:"latMax,omitempty"`
	LonMin   *float64     `json:"lonMin,omitempty"`
	LonMax   *float64     `json:"lonMax,omitempty"`
}

type TimestampConfig struct {
	DefaultMode       string `json:"defaultMode"`       // "ago" | "absolute"
	Timezone          string `json:"timezone"`          // "local" | "utc"
	FormatPreset      string `json:"formatPreset"`      // "iso" | "iso-seconds" | "locale"
	CustomFormat      string `json:"customFormat"`      // freeform, only used when AllowCustomFormat=true
	AllowCustomFormat bool   `json:"allowCustomFormat"` // admin gate
}

type RetentionConfig struct {
	NodeDays int `json:"nodeDays"`
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

func (c *Config) PropagationBufferMs() int {
	if c.LiveMap.PropagationBufferMs > 0 {
		return c.LiveMap.PropagationBufferMs
	}
	return 5000
}

func (c *Config) NormalizeTimestampConfig() {
	defaults := defaultTimestampConfig()
	if c.Timestamps == nil {
		log.Printf("[config] timestamps not configured — using defaults (ago/local/iso)")
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
