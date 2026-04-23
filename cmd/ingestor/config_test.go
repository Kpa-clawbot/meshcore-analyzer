package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigValidJSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{
		"dbPath": "/tmp/test.db",
		"mqttSources": [
			{"name": "s1", "broker": "tcp://localhost:1883", "topics": ["meshcore/#"]}
		]
	}`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBPath != "/tmp/test.db" {
		t.Errorf("dbPath=%s, want /tmp/test.db", cfg.DBPath)
	}
	if len(cfg.MQTTSources) != 1 {
		t.Fatalf("mqttSources len=%d, want 1", len(cfg.MQTTSources))
	}
	if cfg.MQTTSources[0].Broker != "tcp://localhost:1883" {
		t.Errorf("broker=%s", cfg.MQTTSources[0].Broker)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	t.Setenv("DB_PATH", "")
	t.Setenv("MQTT_BROKER", "")

	cfg, err := LoadConfig("/nonexistent/path/config.json")
	if err != nil {
		t.Fatalf("missing config should not error (zero-config mode), got: %v", err)
	}
	if cfg.DBPath != "data/meshcore.db" {
		t.Errorf("dbPath=%s, want data/meshcore.db", cfg.DBPath)
	}
	// Should default to localhost MQTT
	if len(cfg.MQTTSources) != 1 {
		t.Fatalf("mqttSources len=%d, want 1", len(cfg.MQTTSources))
	}
	if cfg.MQTTSources[0].Broker != "mqtt://localhost:1883" {
		t.Errorf("default broker=%s, want mqtt://localhost:1883", cfg.MQTTSources[0].Broker)
	}
	if cfg.MQTTSources[0].Name != "local" {
		t.Errorf("default source name=%s, want local", cfg.MQTTSources[0].Name)
	}
}

func TestLoadConfigMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.json")
	os.WriteFile(cfgPath, []byte(`{not valid json`), 0o644)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestLoadConfigEnvVarDBPath(t *testing.T) {
	t.Setenv("DB_PATH", "/override/db.sqlite")
	t.Setenv("MQTT_BROKER", "")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{"dbPath": "original.db"}`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBPath != "/override/db.sqlite" {
		t.Errorf("dbPath=%s, want /override/db.sqlite", cfg.DBPath)
	}
}

func TestLoadConfigEnvVarMQTTBroker(t *testing.T) {
	t.Setenv("MQTT_BROKER", "tcp://env-broker:1883")
	t.Setenv("MQTT_TOPIC", "custom/topic")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{"dbPath": "test.db"}`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MQTTSources) != 1 {
		t.Fatalf("mqttSources len=%d, want 1", len(cfg.MQTTSources))
	}
	src := cfg.MQTTSources[0]
	if src.Name != "env" {
		t.Errorf("name=%s, want env", src.Name)
	}
	if src.Broker != "tcp://env-broker:1883" {
		t.Errorf("broker=%s", src.Broker)
	}
	if len(src.Topics) != 1 || src.Topics[0] != "custom/topic" {
		t.Errorf("topics=%v, want [custom/topic]", src.Topics)
	}
}

func TestLoadConfigEnvVarMQTTBrokerDefaultTopic(t *testing.T) {
	t.Setenv("MQTT_BROKER", "tcp://env-broker:1883")
	t.Setenv("MQTT_TOPIC", "")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{"dbPath": "test.db"}`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MQTTSources[0].Topics[0] != "meshcore/#" {
		t.Errorf("default topic=%s, want meshcore/#", cfg.MQTTSources[0].Topics[0])
	}
}

func TestLoadConfigLegacyMQTT(t *testing.T) {
	t.Setenv("DB_PATH", "")
	t.Setenv("MQTT_BROKER", "")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{
		"dbPath": "test.db",
		"mqtt": {"broker": "tcp://legacy:1883", "topic": "old/topic"}
	}`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MQTTSources) != 1 {
		t.Fatalf("mqttSources len=%d, want 1", len(cfg.MQTTSources))
	}
	src := cfg.MQTTSources[0]
	if src.Name != "default" {
		t.Errorf("name=%s, want default", src.Name)
	}
	if src.Broker != "tcp://legacy:1883" {
		t.Errorf("broker=%s", src.Broker)
	}
	if len(src.Topics) != 2 || src.Topics[0] != "old/topic" || src.Topics[1] != "meshcore/#" {
		t.Errorf("topics=%v, want [old/topic meshcore/#]", src.Topics)
	}
}

func TestLoadConfigLegacyMQTTNotUsedWhenSourcesExist(t *testing.T) {
	t.Setenv("DB_PATH", "")
	t.Setenv("MQTT_BROKER", "")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{
		"dbPath": "test.db",
		"mqtt": {"broker": "tcp://legacy:1883", "topic": "old/topic"},
		"mqttSources": [{"name": "modern", "broker": "tcp://modern:1883", "topics": ["m/#"]}]
	}`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MQTTSources) != 1 {
		t.Fatalf("mqttSources len=%d, want 1", len(cfg.MQTTSources))
	}
	if cfg.MQTTSources[0].Name != "modern" {
		t.Errorf("should use modern source, got name=%s", cfg.MQTTSources[0].Name)
	}
}

func TestLoadConfigDefaultDBPath(t *testing.T) {
	t.Setenv("DB_PATH", "")
	t.Setenv("MQTT_BROKER", "")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{}`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBPath != "data/meshcore.db" {
		t.Errorf("dbPath=%s, want data/meshcore.db", cfg.DBPath)
	}
}

func TestLoadConfigLegacyMQTTEmptyBroker(t *testing.T) {
	t.Setenv("DB_PATH", "")
	t.Setenv("MQTT_BROKER", "")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{
		"dbPath": "test.db",
		"mqtt": {"broker": "", "topic": "t"}
	}`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MQTTSources) != 1 || cfg.MQTTSources[0].Name != "local" {
		t.Errorf("mqttSources should default to local broker when legacy broker is empty, got %v", cfg.MQTTSources)
	}
}

func TestResolvedSources(t *testing.T) {
	cfg := &Config{
		MQTTSources: []MQTTSource{
			{Name: "a", Broker: "tcp://a:1883"},
			{Name: "b", Broker: "tcp://b:1883"},
		},
	}
	sources := cfg.ResolvedSources()
	if len(sources) != 2 {
		t.Fatalf("len=%d, want 2", len(sources))
	}
	if sources[0].Name != "a" || sources[1].Name != "b" {
		t.Errorf("sources=%v", sources)
	}
}

func TestResolvedSourcesEmpty(t *testing.T) {
	cfg := &Config{}
	sources := cfg.ResolvedSources()
	if len(sources) != 0 {
		t.Errorf("len=%d, want 0", len(sources))
	}
}

func TestLoadConfigWithAllFields(t *testing.T) {
	t.Setenv("DB_PATH", "")
	t.Setenv("MQTT_BROKER", "")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	reject := false
	_ = reject
	os.WriteFile(cfgPath, []byte(`{
		"dbPath": "mydb.db",
		"logLevel": "debug",
		"mqttSources": [{
			"name": "full",
			"broker": "tcp://full:1883",
			"username": "user1",
			"password": "pass1",
			"rejectUnauthorized": false,
			"topics": ["a/#", "b/#"],
			"iataFilter": ["SJC", "LAX"]
		}]
	}`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("logLevel=%s, want debug", cfg.LogLevel)
	}
	src := cfg.MQTTSources[0]
	if src.Username != "user1" {
		t.Errorf("username=%s", src.Username)
	}
	if src.Password != "pass1" {
		t.Errorf("password=%s", src.Password)
	}
	if src.RejectUnauthorized == nil || *src.RejectUnauthorized != false {
		t.Error("rejectUnauthorized should be false")
	}
	if len(src.IATAFilter) != 2 || src.IATAFilter[0] != "SJC" {
		t.Errorf("iataFilter=%v", src.IATAFilter)
	}
}

// TestResolvedSourcesSchemeMapping verifies that mqtt:// and mqtts:// are translated
// to the paho-native tcp:// and ssl:// schemes, while ws:// and wss:// pass through
// unchanged (paho handles WebSocket connections natively).
func TestResolvedSourcesSchemeMapping(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"mqtt://host:1883", "tcp://host:1883"},
		{"mqtts://host:8883", "ssl://host:8883"},
		{"tcp://host:1883", "tcp://host:1883"},   // already translated
		{"ssl://host:8883", "ssl://host:8883"},   // already translated
		{"ws://host:9001", "ws://host:9001"},     // WebSocket: pass through
		{"wss://host:9001", "wss://host:9001"},   // WebSocket TLS: pass through
		{"ws://host:9001/mqtt", "ws://host:9001/mqtt"}, // with path
		{"wss://host:9001/mqtt", "wss://host:9001/mqtt"},
	}

	for _, tt := range tests {
		cfg := &Config{
			MQTTSources: []MQTTSource{
				{Name: "test", Broker: tt.input, Topics: []string{"meshcore/#"}},
			},
		}
		sources := cfg.ResolvedSources()
		if got := sources[0].Broker; got != tt.want {
			t.Errorf("ResolvedSources(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestLoadConfigWSSource verifies that a WebSocket MQTT source round-trips through
// LoadConfig correctly — username/password preserved, scheme unchanged.
func TestLoadConfigWSSource(t *testing.T) {
	t.Setenv("DB_PATH", "")
	t.Setenv("MQTT_BROKER", "")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{
		"dbPath": "test.db",
		"mqttSources": [
			{
				"name": "local-tcp",
				"broker": "mqtt://localhost:1883",
				"topics": ["meshcore/#"]
			},
			{
				"name": "wsmqtt-ws",
				"broker": "wss://wsmqtt.example.com/mqtt",
				"username": "corescope",
				"password": "s3cr3t",
				"topics": ["meshcore/#"]
			}
		]
	}`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MQTTSources) != 2 {
		t.Fatalf("mqttSources len=%d, want 2", len(cfg.MQTTSources))
	}

	tcp := cfg.MQTTSources[0]
	if tcp.Name != "local-tcp" {
		t.Errorf("name=%s, want local-tcp", tcp.Name)
	}

	ws := cfg.MQTTSources[1]
	if ws.Name != "wsmqtt-ws" {
		t.Errorf("name=%s, want wsmqtt-ws", ws.Name)
	}
	if ws.Broker != "wss://wsmqtt.example.com/mqtt" {
		t.Errorf("broker=%s, want wss://wsmqtt.example.com/mqtt", ws.Broker)
	}
	if ws.Username != "corescope" {
		t.Errorf("username=%s, want corescope", ws.Username)
	}
	if ws.Password != "s3cr3t" {
		t.Errorf("password=%s, want s3cr3t", ws.Password)
	}

	// Verify ResolvedSources leaves wss:// intact
	sources := cfg.ResolvedSources()
	if sources[1].Broker != "wss://wsmqtt.example.com/mqtt" {
		t.Errorf("ResolvedSources wss broker=%s, want unchanged", sources[1].Broker)
	}
}
