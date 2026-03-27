package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigValidJSON(t *testing.T) {
	dir := t.TempDir()
	cfgData := map[string]interface{}{
		"port":   8080,
		"dbPath": "/custom/path.db",
		"branding": map[string]interface{}{
			"siteName": "TestSite",
		},
		"mapDefaults": map[string]interface{}{
			"center": []float64{40.0, -74.0},
			"zoom":   12,
		},
		"regions": map[string]string{
			"SJC": "San Jose",
		},
		"healthThresholds": map[string]interface{}{
			"infraDegradedMs": 100000,
			"infraSilentMs":   200000,
			"nodeDegradedMs":  50000,
			"nodeSilentMs":    100000,
		},
		"liveMap": map[string]interface{}{
			"propagationBufferMs": 3000,
		},
	}
	data, _ := json.Marshal(cfgData)
	os.WriteFile(filepath.Join(dir, "config.json"), data, 0644)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Port)
	}
	if cfg.DBPath != "/custom/path.db" {
		t.Errorf("expected /custom/path.db, got %s", cfg.DBPath)
	}
	if cfg.MapDefaults.Zoom != 12 {
		t.Errorf("expected zoom 12, got %d", cfg.MapDefaults.Zoom)
	}
}

func TestLoadConfigFromDataSubdir(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	os.Mkdir(dataDir, 0755)
	cfgData := map[string]interface{}{"port": 9090}
	data, _ := json.Marshal(cfgData)
	os.WriteFile(filepath.Join(dataDir, "config.json"), data, 0644)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Port)
	}
}

func TestLoadConfigNoFiles(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 3000 {
		t.Errorf("expected default port 3000, got %d", cfg.Port)
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("{invalid"), 0644)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Should return defaults when JSON is invalid
	if cfg.Port != 3000 {
		t.Errorf("expected default port 3000, got %d", cfg.Port)
	}
}

func TestLoadConfigNoArgs(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}

func TestLoadThemeValidJSON(t *testing.T) {
	dir := t.TempDir()
	themeData := map[string]interface{}{
		"branding": map[string]interface{}{
			"siteName": "CustomTheme",
		},
		"theme": map[string]interface{}{
			"accent": "#ff0000",
		},
		"nodeColors": map[string]interface{}{
			"repeater": "#00ff00",
		},
	}
	data, _ := json.Marshal(themeData)
	os.WriteFile(filepath.Join(dir, "theme.json"), data, 0644)

	theme := LoadTheme(dir)
	if theme.Branding == nil {
		t.Fatal("expected branding")
	}
	if theme.Branding["siteName"] != "CustomTheme" {
		t.Errorf("expected CustomTheme, got %v", theme.Branding["siteName"])
	}
	if theme.Theme["accent"] != "#ff0000" {
		t.Errorf("expected #ff0000, got %v", theme.Theme["accent"])
	}
}

func TestLoadThemeFromDataSubdir(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	os.Mkdir(dataDir, 0755)
	themeData := map[string]interface{}{
		"branding": map[string]interface{}{"siteName": "DataTheme"},
	}
	data, _ := json.Marshal(themeData)
	os.WriteFile(filepath.Join(dataDir, "theme.json"), data, 0644)

	theme := LoadTheme(dir)
	if theme.Branding == nil {
		t.Fatal("expected branding")
	}
	if theme.Branding["siteName"] != "DataTheme" {
		t.Errorf("expected DataTheme, got %v", theme.Branding["siteName"])
	}
}

func TestLoadThemeNoFile(t *testing.T) {
	dir := t.TempDir()
	theme := LoadTheme(dir)
	if theme == nil {
		t.Fatal("expected non-nil theme")
	}
}

func TestLoadThemeNoArgs(t *testing.T) {
	theme := LoadTheme()
	if theme == nil {
		t.Fatal("expected non-nil theme")
	}
}

func TestLoadThemeInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "theme.json"), []byte("{bad json"), 0644)
	theme := LoadTheme(dir)
	// Should return empty theme
	if theme == nil {
		t.Fatal("expected non-nil theme")
	}
}

func TestGetHealthThresholdsDefaults(t *testing.T) {
	cfg := &Config{}
	ht := cfg.GetHealthThresholds()

	if ht.InfraDegradedMs != 86400000 {
		t.Errorf("expected 86400000, got %d", ht.InfraDegradedMs)
	}
	if ht.InfraSilentMs != 259200000 {
		t.Errorf("expected 259200000, got %d", ht.InfraSilentMs)
	}
	if ht.NodeDegradedMs != 3600000 {
		t.Errorf("expected 3600000, got %d", ht.NodeDegradedMs)
	}
	if ht.NodeSilentMs != 86400000 {
		t.Errorf("expected 86400000, got %d", ht.NodeSilentMs)
	}
}

func TestGetHealthThresholdsCustom(t *testing.T) {
	cfg := &Config{
		HealthThresholds: &HealthThresholds{
			InfraDegradedMs: 100000,
			InfraSilentMs:   200000,
			NodeDegradedMs:  50000,
			NodeSilentMs:    100000,
		},
	}
	ht := cfg.GetHealthThresholds()

	if ht.InfraDegradedMs != 100000 {
		t.Errorf("expected 100000, got %d", ht.InfraDegradedMs)
	}
	if ht.InfraSilentMs != 200000 {
		t.Errorf("expected 200000, got %d", ht.InfraSilentMs)
	}
	if ht.NodeDegradedMs != 50000 {
		t.Errorf("expected 50000, got %d", ht.NodeDegradedMs)
	}
	if ht.NodeSilentMs != 100000 {
		t.Errorf("expected 100000, got %d", ht.NodeSilentMs)
	}
}

func TestGetHealthThresholdsPartialCustom(t *testing.T) {
	cfg := &Config{
		HealthThresholds: &HealthThresholds{
			InfraDegradedMs: 100000,
			// Others left as zero → should use defaults
		},
	}
	ht := cfg.GetHealthThresholds()

	if ht.InfraDegradedMs != 100000 {
		t.Errorf("expected 100000, got %d", ht.InfraDegradedMs)
	}
	if ht.InfraSilentMs != 259200000 {
		t.Errorf("expected default 259200000, got %d", ht.InfraSilentMs)
	}
}

func TestGetHealthMs(t *testing.T) {
	ht := HealthThresholds{
		InfraDegradedMs: 86400000,
		InfraSilentMs:   259200000,
		NodeDegradedMs:  3600000,
		NodeSilentMs:    86400000,
	}

	tests := []struct {
		role       string
		wantDeg    int
		wantSilent int
	}{
		{"repeater", 86400000, 259200000},
		{"room", 86400000, 259200000},
		{"companion", 3600000, 86400000},
		{"sensor", 3600000, 86400000},
		{"unknown", 3600000, 86400000},
	}

	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			deg, sil := ht.GetHealthMs(tc.role)
			if deg != tc.wantDeg {
				t.Errorf("degraded: expected %d, got %d", tc.wantDeg, deg)
			}
			if sil != tc.wantSilent {
				t.Errorf("silent: expected %d, got %d", tc.wantSilent, sil)
			}
		})
	}
}

func TestResolveDBPath(t *testing.T) {
	t.Run("DBPath set", func(t *testing.T) {
		cfg := &Config{DBPath: "/explicit/path.db"}
		got := cfg.ResolveDBPath("/base")
		if got != "/explicit/path.db" {
			t.Errorf("expected /explicit/path.db, got %s", got)
		}
	})

	t.Run("env var", func(t *testing.T) {
		cfg := &Config{}
		t.Setenv("DB_PATH", "/env/path.db")
		got := cfg.ResolveDBPath("/base")
		if got != "/env/path.db" {
			t.Errorf("expected /env/path.db, got %s", got)
		}
	})

	t.Run("default", func(t *testing.T) {
		cfg := &Config{}
		t.Setenv("DB_PATH", "")
		got := cfg.ResolveDBPath("/base")
		expected := filepath.Join("/base", "data", "meshcore.db")
		if got != expected {
			t.Errorf("expected %s, got %s", expected, got)
		}
	})
}

func TestPropagationBufferMs(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		cfg := &Config{}
		if cfg.PropagationBufferMs() != 5000 {
			t.Errorf("expected 5000, got %d", cfg.PropagationBufferMs())
		}
	})

	t.Run("custom", func(t *testing.T) {
		cfg := &Config{}
		cfg.LiveMap.PropagationBufferMs = 3000
		if cfg.PropagationBufferMs() != 3000 {
			t.Errorf("expected 3000, got %d", cfg.PropagationBufferMs())
		}
	})
}
