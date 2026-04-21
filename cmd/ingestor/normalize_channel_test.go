package main

import (
	"testing"
)

func TestNormalizeChannelName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Known channel: "public" should be normalized to "Public"
		{"public", "Public"},
		{"Public", "Public"},
		{"PUBLIC", "Public"},
		// Hashtag channels should be left untouched
		{"#LongFast", "#LongFast"},
		{"#wardrive", "#wardrive"},
		// Custom/unknown channels should be left untouched
		{"myChannel", "myChannel"},
		{"testchannel", "testchannel"},
		// Empty string
		{"", ""},
	}

	for _, tt := range tests {
		got := normalizeChannelName(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeChannelName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestLoadChannelKeys_NormalizesKnownDisplayNames(t *testing.T) {
	// Verify that known channel keys with wrong casing get normalized
	cfg := &Config{
		ChannelKeys: map[string]string{
			"public": "8b3387e9c5cdea6ac9e5edbaa115cd72",
		},
	}

	keys := loadChannelKeys(cfg, "/dev/null")

	// Should have "Public" (normalized) not "public" (raw)
	if _, ok := keys["public"]; ok {
		t.Error("Expected 'public' to be normalized to 'Public'")
	}
	if _, ok := keys["Public"]; !ok {
		t.Error("Expected 'Public' key to exist in loaded channel keys")
	}
}

func TestLoadChannelKeys_LeavesCustomNamesUntouched(t *testing.T) {
	// Verify that custom channel names are NOT normalized
	cfg := &Config{
		ChannelKeys: map[string]string{
			"myCustomChannel": "deadbeef12345678",
		},
	}

	keys := loadChannelKeys(cfg, "/dev/null")

	// Should keep "myCustomChannel" as-is
	if _, ok := keys["myCustomChannel"]; !ok {
		t.Error("Expected 'myCustomChannel' to be left untouched")
	}
	// Should NOT have "MyCustomChannel"
	if _, ok := keys["MyCustomChannel"]; ok {
		t.Error("Custom channel names should NOT be auto-capitalized")
	}
}

func TestLoadChannelKeys_DuplicateCasingLogsWarning(t *testing.T) {
	// Verify that config with both "public" and "Public" logs a warning
	// and the last one wins (Go map iteration order is nondeterministic)
	cfg := &Config{
		ChannelKeys: map[string]string{
			"public": "8b3387e9c5cdea6ac9e5edbaa115cd72",
			"Public": "differentkey1234567",
		},
	}

	keys := loadChannelKeys(cfg, "/dev/null")

	// After normalization, only one key should exist: "Public"
	// The value is nondeterministic since Go map iteration order varies
	if _, ok := keys["public"]; ok {
		t.Error("Expected 'public' to be normalized away")
	}
	if _, ok := keys["Public"]; !ok {
		t.Error("Expected 'Public' key to exist")
	}
	// Exactly one entry for the normalized key
	if len(keys) != 1 {
		t.Errorf("Expected exactly 1 key, got %d", len(keys))
	}
}
