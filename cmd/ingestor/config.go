package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/meshcore-analyzer/geofilter"
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
}

// MQTTLegacy is the old single-broker config format.
type MQTTLegacy struct {
	Broker string `json:"broker"`
	Topic  string `json:"topic"`
}

// Config holds the ingestor configuration, compatible with the Node.js config.json format.
type Config struct {
	DBPath          string            `json:"dbPath"`
	MQTT            *MQTTLegacy       `json:"mqtt,omitempty"`
	MQTTSources     []MQTTSource      `json:"mqttSources,omitempty"`
	LogLevel        string            `json:"logLevel,omitempty"`
	ChannelKeysPath string            `json:"channelKeysPath,omitempty"`
	ChannelKeys     map[string]string `json:"channelKeys,omitempty"`
	HashChannels    []string          `json:"hashChannels,omitempty"`
	Retention       *RetentionConfig  `json:"retention,omitempty"`
	Metrics         *MetricsConfig    `json:"metrics,omitempty"`
	GeoFilter       *GeoFilterConfig  `json:"geo_filter,omitempty"`
}

// GeoFilterConfig is an alias for the shared geofilter.Config type.
type GeoFilterConfig = geofilter.Config

// RetentionConfig controls how long stale nodes are kept before being moved to inactive_nodes.
type RetentionConfig struct {
	NodeDays    int `json:"nodeDays"`
	MetricsDays int `json:"metricsDays"`
}

// MetricsConfig controls observer metrics collection.
type MetricsConfig struct {
	SampleIntervalSec int `json:"sampleIntervalSec"`
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

// LoadConfig reads configuration from a JSON file, with env var overrides.
// If the config file does not exist, sensible defaults are used (zero-config startup).
func LoadConfig(path string) (*Config, error) {
	var cfg Config

	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
		// Config file doesn't exist — use defaults (zero-config mode)
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
		// Single broker from env — create a source
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
func (c *Config) ResolvedSources() []MQTTSource {
	for i := range c.MQTTSources {
		// paho uses tcp:// and ssl:// not mqtt:// and mqtts://
		b := c.MQTTSources[i].Broker
		if strings.HasPrefix(b, "mqtt://") {
			c.MQTTSources[i].Broker = "tcp://" + b[7:]
		} else if strings.HasPrefix(b, "mqtts://") {
			c.MQTTSources[i].Broker = "ssl://" + b[8:]
		}
	}
	return c.MQTTSources
}
