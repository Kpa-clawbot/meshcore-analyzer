package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/geofilter"
	"github.com/meshcore-analyzer/packetpath"
)

// MQTTSource represents a single MQTT broker connection.
type MQTTSource struct {
	Name               string   `json:"name"`
	Broker             string   `json:"broker"`
	Username           string   `json:"username,omitempty"`
	Password           string   `json:"password,omitempty"`
	RejectUnauthorized *bool    `json:"rejectUnauthorized,omitempty"`
	Topics             []string `json:"topics"`
	IATAFilter         []string `json:"iataFilter,omitempty"`
	ConnectTimeoutSec  int      `json:"connectTimeoutSec,omitempty"`
	Region             string   `json:"region,omitempty"`
}

// ConnectTimeoutOrDefault returns the per-source connect timeout in seconds,
// or 30 if not set (matching the WaitTimeout default from #926).
func (s MQTTSource) ConnectTimeoutOrDefault() int {
	if s.ConnectTimeoutSec > 0 {
		return s.ConnectTimeoutSec
	}
	return 30
}

// MQTTLegacy is the old single-broker config format.
type MQTTLegacy struct {
	Broker string `json:"broker"`
	Topic  string `json:"topic"`
}

// Config holds the ingestor configuration, compatible with the Node.js config.json format.
type Config struct {
	DBPath               string                      `json:"dbPath"`
	MQTT                 *MQTTLegacy                 `json:"mqtt,omitempty"`
	MQTTSources          []MQTTSource                `json:"mqttSources,omitempty"`
	LogLevel             string                      `json:"logLevel,omitempty"`
	ChannelKeysPath      string                      `json:"channelKeysPath,omitempty"`
	ChannelKeys          map[string]string           `json:"channelKeys,omitempty"`
	HashChannels         []string                    `json:"hashChannels,omitempty"`
	HashRegions          []string                    `json:"hashRegions,omitempty"`
	Retention            *RetentionConfig            `json:"retention,omitempty"`
	Metrics              *MetricsConfig              `json:"metrics,omitempty"`
	Runtime              *RuntimeConfig              `json:"runtime,omitempty"`
	ClientRxCoverage     *ClientRxCoverageConfig     `json:"clientRxCoverage,omitempty"`
	ClientRxObservations *ClientRxObservationsConfig `json:"clientRxObservations,omitempty"`
	ClientRfSamples      *ClientRfSamplesConfig      `json:"clientRfSamples,omitempty"`
	GeoFilter            *GeoFilterConfig            `json:"geo_filter,omitempty"`
	// PathTrust configures the minimum path-hash prefix length trusted as
	// mapping/topology evidence (issue #1784).
	PathTrust          *PathTrustConfig     `json:"pathTrust,omitempty"`
	ForeignAdverts     *ForeignAdvertConfig `json:"foreignAdverts,omitempty"`
	ValidateSignatures *bool                `json:"validateSignatures,omitempty"`
	DB                 *DBConfig            `json:"db,omitempty"`

	// ObserverIATAWhitelist restricts which observer IATA regions are processed.
	// When non-empty, only observers whose IATA code (from the MQTT topic) matches
	// one of these entries are accepted. Case-insensitive. An empty list means all
	// IATA codes are allowed. This applies globally, unlike the per-source iataFilter.
	ObserverIATAWhitelist []string `json:"observerIATAWhitelist,omitempty"`

	// obsIATAWhitelistCached is the lazily-built uppercase set for O(1) lookups.
	obsIATAWhitelistCached map[string]bool
	obsIATAWhitelistOnce   sync.Once

	// ObserverBlacklist is a list of observer public keys to drop at ingest.
	// Messages from blacklisted observers are silently discarded â€” no DB writes,
	// no UpsertObserver, no observations, no metrics.
	ObserverBlacklist []string `json:"observerBlacklist,omitempty"`

	// obsBlacklistSetCached is the lazily-built lowercase set for O(1) lookups.
	obsBlacklistSetCached map[string]bool
	obsBlacklistOnce      sync.Once

	// NeighborEdgesMaxAgeDays controls neighbor_edges row retention
	// (#1287 â€” moved from cmd/server). 0 = default 5.
	NeighborEdgesMaxAgeDays int `json:"neighborEdgesMaxAgeDays,omitempty"`

	// IngestBufferSize caps the in-memory queue (number of MQTT messages) held
	// while the single SQLite writer is blocked by startup migrations/prunes
	// (#1608). Received messages are drained once the write path is ready.
	// 0 / unset => default. Bounded memory.
	IngestBufferSize int `json:"ingestBufferSize,omitempty"`
}

// NeighborEdgesDaysOrDefault returns the configured pruning window or 5.
func (c *Config) NeighborEdgesDaysOrDefault() int {
	if c == nil || c.NeighborEdgesMaxAgeDays <= 0 {
		return 5
	}
	return c.NeighborEdgesMaxAgeDays
}

// IngestBufferSizeOrDefault returns the ingest buffer capacity. Default 50000:
// at typical mesh rates (~1-2 msg/s) that is many minutes of headroom while a
// startup migration holds the writer; each queued item is a small closure, so
// worst-case memory stays in the tens of MB.
func (c *Config) IngestBufferSizeOrDefault() int {
	if c.IngestBufferSize > 0 {
		return c.IngestBufferSize
	}
	return 50000
}

// GeoFilterConfig is an alias for the shared geofilter.Config type.
type GeoFilterConfig = geofilter.Config

// PathTrustConfig is an alias for the shared packetpath.TrustConfig type
// (issue #1784). See packetpath.TrustConfig for the full doc comment.
type PathTrustConfig = packetpath.TrustConfig

// ForeignAdvertConfig controls how the ingestor handles ADVERTs whose GPS lies
// outside the configured geofilter polygon (#730). Modes:
//   - "flag" (default): store the advert/node and tag it foreign for visibility.
//   - "drop":           silently discard the advert (legacy behavior).
type ForeignAdvertConfig struct {
	Mode string `json:"mode,omitempty"`
}

// IsDropMode reports whether the foreign-advert config is set to "drop".
// Defaults to false ("flag" mode) when nil or unset.
func (f *ForeignAdvertConfig) IsDropMode() bool {
	if f == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(f.Mode), "drop")
}

// ClientRxCoverageConfig controls the opt-in mobile client-RX coverage feature.
type ClientRxCoverageConfig struct {
	Enabled bool `json:"enabled"`
}

// ClientRxCoverageEnabled reports whether the opt-in mobile client-RX coverage
// feature is on. Absent/nil â‡’ off (the safe default).
func (c *Config) ClientRxCoverageEnabled() bool {
	return c.ClientRxCoverage != nil && c.ClientRxCoverage.Enabled
}

// ClientRxObservationsConfig controls the opt-in diagnostic RF observation
// table. Separate from clientRxCoverage: a deployment may well want coverage
// without the far higher-volume diagnostic stream.
type ClientRxObservationsConfig struct {
	Enabled bool `json:"enabled"`
}

// ClientRxObservationsEnabled reports whether diagnostic client RF observations
// are recorded. Default false.
func (c *Config) ClientRxObservationsEnabled() bool {
	return c.ClientRxObservations != nil && c.ClientRxObservations.Enabled
}

// ClientRfSamplesConfig controls the opt-in RF environment sample stream.
type ClientRfSamplesConfig struct {
	Enabled bool `json:"enabled"`
}

// ClientRfSamplesEnabled reports whether RF samples are recorded. Default false.
func (c *Config) ClientRfSamplesEnabled() bool {
	return c.ClientRfSamples != nil && c.ClientRfSamples.Enabled
}

// RetentionConfig controls how long stale nodes are kept before being moved to inactive_nodes.
type RetentionConfig struct {
	NodeDays     int `json:"nodeDays"`
	ObserverDays int `json:"observerDays"`
	// ObserverPurgeDays hard-deletes observers that observerDays already
	// soft-deleted, once they are older than this and no observation,
	// metric or dropped-packet row still references them; 0 disables.
	// Only meaningful above observerDays and packetDays — below those the
	// reference guards keep every candidate row anyway.
	ObserverPurgeDays int `json:"observerPurgeDays"`
	MetricsDays       int `json:"metricsDays"`
	// PacketDays is the retention window for transmissions (#1283).
	// Ownership moved from cmd/server to cmd/ingestor; 0 disables.
	PacketDays int `json:"packetDays"`
	// ClientRxDays is the retention window (by rx_at) for mobile client-RX
	// coverage rows in client_receptions / client_observers; 0 disables. Bounds
	// the table the opt-in coverage feature would otherwise grow without limit.
	ClientRxDays int `json:"clientRxDays"`
	// ClientRxObsDays is the retention window (by rx_at) for the diagnostic
	// client_rx_observations table; 0 disables. Shorter than clientRxDays â€”
	// this table is diagnostic, not archival.
	ClientRxObsDays int `json:"clientRxObsDays"`
	// ClientRfDays is the retention window (by sampled_at) for the
	// client_rf_samples table; 0 disables. Bounds the table the opt-in RF
	// sample stream would otherwise grow without limit.
	ClientRfDays int `json:"clientRfDays"`
}

// PacketDaysOrZero returns the configured retention.packetDays or 0
// (disabled) if not set.
func (c *Config) PacketDaysOrZero() int {
	if c.Retention != nil && c.Retention.PacketDays > 0 {
		return c.Retention.PacketDays
	}
	return 0
}

// ObserverPurgeDaysOrZero returns the configured retention.observerPurgeDays
// or 0 (disabled) if not set.
func (c *Config) ObserverPurgeDaysOrZero() int {
	if c.Retention != nil && c.Retention.ObserverPurgeDays > 0 {
		return c.Retention.ObserverPurgeDays
	}
	return 0
}

// ClientRxDaysOrZero returns the configured retention.clientRxDays or 0
// (disabled) if not set.
func (c *Config) ClientRxDaysOrZero() int {
	if c.Retention != nil && c.Retention.ClientRxDays > 0 {
		return c.Retention.ClientRxDays
	}
	return 0
}

// ClientRxObsDaysOrZero returns retention.clientRxObsDays or 0 (disabled).
func (c *Config) ClientRxObsDaysOrZero() int {
	if c.Retention != nil && c.Retention.ClientRxObsDays > 0 {
		return c.Retention.ClientRxObsDays
	}
	return 0
}

// ClientRfDaysOrZero returns retention.clientRfDays or 0 (disabled).
func (c *Config) ClientRfDaysOrZero() int {
	if c.Retention != nil && c.Retention.ClientRfDays > 0 {
		return c.Retention.ClientRfDays
	}
	return 0
}

// MetricsConfig controls observer metrics collection.
type MetricsConfig struct {
	SampleIntervalSec int `json:"sampleIntervalSec"`
}

// RuntimeConfig holds Go runtime tuning knobs (#1010).
type RuntimeConfig struct {
	// MaxMemoryMB is the soft memory limit (GOMEMLIMIT) in MiB applied via
	// runtime/debug.SetMemoryLimit at startup. The GOMEMLIMIT environment
	// variable, when set, takes precedence over this value. 0/unset means
	// no limit is applied and default Go runtime behavior is preserved.
	MaxMemoryMB int `json:"maxMemoryMB"`
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

// ShouldValidateSignatures returns true (default) unless explicitly disabled.
func (c *Config) ShouldValidateSignatures() bool {
	if c.ValidateSignatures != nil {
		return *c.ValidateSignatures
	}
	return true
}

// MetricsSampleInterval returns the configured sample interval or 300s default.
func (c *Config) MetricsSampleInterval() int {
	if c.Metrics != nil && c.Metrics.SampleIntervalSec > 0 {
		return c.Metrics.SampleIntervalSec
	}
	return 300
}

// MetricsRetentionDays returns configured metrics retention or 30 days default.
func (c *Config) MetricsRetentionDays() int {
	if c.Retention != nil && c.Retention.MetricsDays > 0 {
		return c.Retention.MetricsDays
	}
	return 30
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

// GetPathTrust returns the effective path-trust config, applying
// DefaultMinHashBytesForMapping when unset (issue #1784).
func (c *Config) GetPathTrust() PathTrustConfig {
	if c != nil && c.PathTrust != nil {
		return *c.PathTrust
	}
	return PathTrustConfig{MinHashBytesForMapping: packetpath.DefaultMinHashBytesForMapping}
}

// IsObserverBlacklisted returns true if the given observer ID is in the observerBlacklist.
func (c *Config) IsObserverBlacklisted(id string) bool {
	if c == nil || len(c.ObserverBlacklist) == 0 {
		return false
	}
	c.obsBlacklistOnce.Do(func() {
		m := make(map[string]bool, len(c.ObserverBlacklist))
		for _, pk := range c.ObserverBlacklist {
			trimmed := strings.ToLower(strings.TrimSpace(pk))
			if trimmed != "" {
				m[trimmed] = true
			}
		}
		c.obsBlacklistSetCached = m
	})
	return c.obsBlacklistSetCached[strings.ToLower(strings.TrimSpace(id))]
}

// IsObserverIATAAllowed returns true if the given IATA code is permitted.
// When ObserverIATAWhitelist is empty, all codes are allowed.
func (c *Config) IsObserverIATAAllowed(iata string) bool {
	if c == nil || len(c.ObserverIATAWhitelist) == 0 {
		return true
	}
	c.obsIATAWhitelistOnce.Do(func() {
		m := make(map[string]bool, len(c.ObserverIATAWhitelist))
		for _, code := range c.ObserverIATAWhitelist {
			trimmed := strings.ToUpper(strings.TrimSpace(code))
			if trimmed != "" {
				m[trimmed] = true
			}
		}
		c.obsIATAWhitelistCached = m
	})
	return c.obsIATAWhitelistCached[strings.ToUpper(strings.TrimSpace(iata))]
}

// LoadConfig reads configuration from a JSON file, with env var overrides.
// If the config file does not exist, sensible defaults are used (zero-config startup).
func LoadConfig(path string) (*Config, error) {
	var cfg Config

	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
		// Config file doesn't exist â€” use defaults (zero-config mode)
		log.Printf("config file %s not found, using sensible defaults", path)
	} else {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}
	}

	// Env var overrides
	if v := os.Getenv("DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("MQTT_BROKER"); v != "" {
		// Single broker from env â€” create a source
		topic := os.Getenv("MQTT_TOPIC")
		if topic == "" {
			topic = "meshcore/#"
		}
		cfg.MQTTSources = []MQTTSource{{
			Name:   "env",
			Broker: v,
			Topics: []string{topic},
		}}
	}

	// Default DB path
	if cfg.DBPath == "" {
		cfg.DBPath = "data/meshcore.db"
	}

	// Normalize: convert legacy single mqtt config to mqttSources
	if len(cfg.MQTTSources) == 0 && cfg.MQTT != nil && cfg.MQTT.Broker != "" {
		cfg.MQTTSources = []MQTTSource{{
			Name:   "default",
			Broker: cfg.MQTT.Broker,
			Topics: []string{cfg.MQTT.Topic, "meshcore/#"},
		}}
	}

	// Default MQTT source: connect to localhost broker when no sources configured
	if len(cfg.MQTTSources) == 0 {
		cfg.MQTTSources = []MQTTSource{{
			Name:   "local",
			Broker: "mqtt://localhost:1883",
			Topics: []string{"meshcore/#"},
		}}
		log.Printf("no MQTT sources configured, defaulting to mqtt://localhost:1883")
	}

	return &cfg, nil
}

// ResolvedSources returns the final list of MQTT sources to connect to.
//
// Scheme mapping:
//
//	mqtt://  â†’ tcp://   (paho plain TCP)
//	mqtts:// â†’ ssl://   (paho TLS over TCP)
//	ws://               (paho WebSocket â€” passed through, no mapping needed)
//	wss://              (paho WebSocket TLS â€” passed through, no mapping needed)
func (c *Config) ResolvedSources() []MQTTSource {
	for i := range c.MQTTSources {
		// paho uses tcp:// and ssl:// for plain MQTT; ws:// and wss:// are accepted natively.
		b := c.MQTTSources[i].Broker
		if strings.HasPrefix(b, "mqtt://") {
			c.MQTTSources[i].Broker = "tcp://" + b[7:]
		} else if strings.HasPrefix(b, "mqtts://") {
			c.MQTTSources[i].Broker = "ssl://" + b[8:]
		}
		// ws:// and wss:// pass through unchanged â€” paho handles WebSocket
		// connections natively via gorilla/websocket.
	}
	return c.MQTTSources
}
