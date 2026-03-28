package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

	GeoFilter *GeoFilterConfig `json:"geo_filter,omitempty"`
}

type GeoFilterConfig struct {
	Polygon  [][2]float64 `json:"polygon,omitempty"`
	BufferKm float64      `json:"bufferKm,omitempty"`
	LatMin   *float64     `json:"latMin,omitempty"`
	LatMax   *float64     `json:"latMax,omitempty"`
	LonMin   *float64     `json:"lonMin,omitempty"`
	LonMax   *float64     `json:"lonMax,omitempty"`
}

type RetentionConfig struct {
	NodeDays   int `json:"nodeDays"`
	PacketDays int `json:"packetDays"`
}

// NodeDaysOrDefault returns the configured retention.nodeDays or 7 if not set.
func (c *Config) NodeDaysOrDefault() int {
	if c.Retention != nil && c.Retention.NodeDays > 0 {
		return c.Retention.NodeDays
	}
	return 7
}

type HealthThresholds struct {
	InfraDegradedMs int `json:"infraDegradedMs"`
	InfraSilentMs   int `json:"infraSilentMs"`
	NodeDegradedMs  int `json:"nodeDegradedMs"`
	NodeSilentMs    int `json:"nodeSilentMs"`
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
		return cfg, nil
	}
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
		InfraDegradedMs: 86400000,
		InfraSilentMs:   259200000,
		NodeDegradedMs:  3600000,
		NodeSilentMs:    86400000,
	}
	if c.HealthThresholds != nil {
		if c.HealthThresholds.InfraDegradedMs > 0 {
			h.InfraDegradedMs = c.HealthThresholds.InfraDegradedMs
		}
		if c.HealthThresholds.InfraSilentMs > 0 {
			h.InfraSilentMs = c.HealthThresholds.InfraSilentMs
		}
		if c.HealthThresholds.NodeDegradedMs > 0 {
			h.NodeDegradedMs = c.HealthThresholds.NodeDegradedMs
		}
		if c.HealthThresholds.NodeSilentMs > 0 {
			h.NodeSilentMs = c.HealthThresholds.NodeSilentMs
		}
	}
	return h
}

// GetHealthMs returns degraded/silent thresholds for a given role.
func (h HealthThresholds) GetHealthMs(role string) (degradedMs, silentMs int) {
	if role == "repeater" || role == "room" {
		return h.InfraDegradedMs, h.InfraSilentMs
	}
	return h.NodeDegradedMs, h.NodeSilentMs
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
