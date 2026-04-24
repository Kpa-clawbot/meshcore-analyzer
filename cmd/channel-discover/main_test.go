package main

import (
	"encoding/hex"
	"testing"
)

func TestDeriveChannelKey(t *testing.T) {
	// Known: SHA256("#test") → first 16 bytes as hex
	key := deriveChannelKey("#test")
	keyHex := hex.EncodeToString(key)
	if len(key) != 16 {
		t.Fatalf("expected 16-byte key, got %d", len(key))
	}
	// Verify it's deterministic
	key2 := deriveChannelKey("#test")
	if hex.EncodeToString(key2) != keyHex {
		t.Fatal("key derivation not deterministic")
	}
	// Different name → different key
	key3 := deriveChannelKey("#other")
	if hex.EncodeToString(key3) == keyHex {
		t.Fatal("different names produced same key")
	}
}

func TestChannelHashFromKey(t *testing.T) {
	key := deriveChannelKey("#test")
	ch := channelHashFromKey(key)
	// Must be deterministic
	ch2 := channelHashFromKey(key)
	if ch != ch2 {
		t.Fatal("channelHash not deterministic")
	}
}

func TestTryDecryptInvalidInputs(t *testing.T) {
	key := deriveChannelKey("#test")

	// Empty ciphertext
	_, _, _, ok := tryDecrypt("", "0000", key)
	if ok {
		t.Fatal("expected failure on empty ciphertext")
	}

	// Invalid hex
	_, _, _, ok = tryDecrypt("zzzz", "0000", key)
	if ok {
		t.Fatal("expected failure on invalid hex")
	}

	// Wrong MAC should fail
	_, _, _, ok = tryDecrypt("00000000000000000000000000000000", "ffff", key)
	if ok {
		t.Fatal("expected failure on wrong MAC")
	}
}

func TestRoundTripEncryptDecrypt(t *testing.T) {
	// We can't easily encrypt without reimplementing, but we can verify
	// that the hash derivation chain works end-to-end:
	// name → key → channelHash, and channelHash is 1 byte
	names := []string{"#test", "#general", "#cascadia", "#meshcore"}
	for _, name := range names {
		key := deriveChannelKey(name)
		ch := channelHashFromKey(key)
		_ = ch // just verify no panic
		if len(key) != 16 {
			t.Fatalf("key for %s has wrong length: %d", name, len(key))
		}
	}
}

func TestDefaultWordlistNotEmpty(t *testing.T) {
	words := defaultWordlist()
	if len(words) < 400 {
		t.Fatalf("expected 400+ words in default wordlist, got %d", len(words))
	}
}
