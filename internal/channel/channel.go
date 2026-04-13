// Package channel provides MeshCore hashtag channel key derivation,
// decryption (HMAC-SHA256 MAC + AES-128-ECB), and plaintext parsing.
package channel

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"
)

// DeriveKey derives an AES-128 key from a channel name (e.g. "#wardriving").
// Returns 16 bytes: SHA-256(channelName)[:16].
func DeriveKey(channelName string) []byte {
	h := sha256.Sum256([]byte(channelName))
	return h[:16]
}

// ChannelHash returns the 1-byte channel hash used as the first byte of GRP_TXT payloads.
// It is the first byte of SHA-256 of the 16-byte key.
func ChannelHash(key []byte) byte {
	h := sha256.Sum256(key)
	return h[0]
}

// Decrypt verifies the 2-byte HMAC-SHA256 MAC and performs AES-128-ECB decryption.
// mac must be exactly 2 bytes. ciphertext must be a multiple of 16 bytes.
// Returns the plaintext and true if MAC verification succeeded, or nil and false otherwise.
func Decrypt(key []byte, mac []byte, ciphertext []byte) ([]byte, bool) {
	if len(key) != 16 || len(mac) != 2 || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, false
	}

	// 32-byte channel secret: 16-byte key + 16 zero bytes
	channelSecret := make([]byte, 32)
	copy(channelSecret, key)

	// Verify HMAC-SHA256 (first 2 bytes must match)
	h := hmac.New(sha256.New, channelSecret)
	h.Write(ciphertext)
	calculatedMac := h.Sum(nil)
	if calculatedMac[0] != mac[0] || calculatedMac[1] != mac[1] {
		return nil, false
	}

	// AES-128-ECB decrypt
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}

	return plaintext, true
}

// ParsePlaintext parses decrypted plaintext into timestamp, sender, and message.
// Format: timestamp(4 LE) + flags(1) + "sender: message\0..."
func ParsePlaintext(plaintext []byte) (timestamp uint32, sender string, message string, err error) {
	if len(plaintext) < 5 {
		return 0, "", "", fmt.Errorf("plaintext too short (%d bytes)", len(plaintext))
	}

	timestamp = binary.LittleEndian.Uint32(plaintext[0:4])
	text := string(plaintext[5:])
	if idx := strings.IndexByte(text, 0); idx >= 0 {
		text = text[:idx]
	}

	if !utf8.ValidString(text) || countNonPrintable(text) > 2 {
		return 0, "", "", fmt.Errorf("decrypted text contains non-printable characters")
	}

	// Parse "sender: message" format
	if colonIdx := strings.Index(text, ": "); colonIdx > 0 && colonIdx < 50 {
		potentialSender := text[:colonIdx]
		if !strings.ContainsAny(potentialSender, ":[]") {
			return timestamp, potentialSender, text[colonIdx+2:], nil
		}
	}

	return timestamp, "", text, nil
}

func countNonPrintable(s string) int {
	count := 0
	for _, r := range s {
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			count++
		}
	}
	return count
}
