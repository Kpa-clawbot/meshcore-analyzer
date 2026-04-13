package main

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"unicode/utf8"
)

// UserChannelKey represents a user-added channel key stored in SQLite.
type UserChannelKey struct {
	Name      string `json:"name"`
	KeyHex    string `json:"key"`
	Source    string `json:"source"`             // "hashtag" or "psk"
	CreatedAt string `json:"created_at,omitempty"`
}

// ChannelKeyManager manages runtime channel decryption keys.
// Keys come from two sources: config (loaded by ingestor) and user-added (via API).
type ChannelKeyManager struct {
	mu   sync.RWMutex
	keys map[string]string // name → hex key
}

// NewChannelKeyManager creates a new empty key manager.
func NewChannelKeyManager() *ChannelKeyManager {
	return &ChannelKeyManager{keys: make(map[string]string)}
}

// AddKey adds or updates a channel key.
func (m *ChannelKeyManager) AddKey(name, keyHex string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[name] = keyHex
}

// GetKeys returns a snapshot of all keys.
func (m *ChannelKeyManager) GetKeys() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make(map[string]string, len(m.keys))
	for k, v := range m.keys {
		cp[k] = v
	}
	return cp
}

// RemoveKey removes a channel key by name. Returns true if it existed.
func (m *ChannelKeyManager) RemoveKey(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, existed := m.keys[name]
	delete(m.keys, name)
	return existed
}

// deriveHashtagKey derives an AES-128 key from a hashtag channel name.
// SHA-256(name)[:16] — name must include the # prefix.
func deriveHashtagKey(channelName string) string {
	h := sha256.Sum256([]byte(channelName))
	return hex.EncodeToString(h[:16])
}

// ensureUserChannelKeysTable creates the user_channel_keys table if it doesn't exist.
func ensureUserChannelKeysTable(dbPath string) error {
	rw, err := openRW(dbPath)
	if err != nil {
		return fmt.Errorf("open RW for user_channel_keys: %w", err)
	}
	defer rw.Close()

	_, err = rw.Exec(`CREATE TABLE IF NOT EXISTS user_channel_keys (
		name TEXT PRIMARY KEY,
		key_hex TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'hashtag',
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	return err
}

// loadUserChannelKeys loads all user-added channel keys from SQLite.
func loadUserChannelKeys(db *DB) ([]UserChannelKey, error) {
	rows, err := db.conn.Query(`SELECT name, key_hex, source, created_at FROM user_channel_keys ORDER BY created_at`)
	if err != nil {
		// Table might not exist yet — not an error
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	var keys []UserChannelKey
	for rows.Next() {
		var k UserChannelKey
		if err := rows.Scan(&k.Name, &k.KeyHex, &k.Source, &k.CreatedAt); err != nil {
			continue
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// saveUserChannelKey persists a user-added channel key to SQLite.
func saveUserChannelKey(dbPath string, key UserChannelKey) error {
	rw, err := openRW(dbPath)
	if err != nil {
		return err
	}
	defer rw.Close()

	_, err = rw.Exec(
		`INSERT OR REPLACE INTO user_channel_keys (name, key_hex, source) VALUES (?, ?, ?)`,
		key.Name, key.KeyHex, key.Source,
	)
	return err
}

// deleteUserChannelKey removes a user-added channel key from SQLite.
func deleteUserChannelKey(dbPath string, name string) error {
	rw, err := openRW(dbPath)
	if err != nil {
		return err
	}
	defer rw.Close()

	_, err = rw.Exec(`DELETE FROM user_channel_keys WHERE name = ?`, name)
	return err
}

// channelDecryptResult holds the result of decrypting a GRP_TXT message.
type channelDecryptResult struct {
	Timestamp uint32
	Flags     byte
	Sender    string
	Message   string
}

// decryptChannelMessage decrypts a GRP_TXT payload using a channel key.
// Implements MeshCore channel decryption: HMAC-SHA256 MAC verification + AES-128-ECB.
func decryptChannelMessage(ciphertextHex, macHex, channelKeyHex string) (*channelDecryptResult, error) {
	channelKey, err := hex.DecodeString(channelKeyHex)
	if err != nil || len(channelKey) != 16 {
		return nil, fmt.Errorf("invalid channel key")
	}

	macBytes, err := hex.DecodeString(macHex)
	if err != nil || len(macBytes) != 2 {
		return nil, fmt.Errorf("invalid MAC")
	}

	ciphertext, err := hex.DecodeString(ciphertextHex)
	if err != nil || len(ciphertext) == 0 {
		return nil, fmt.Errorf("invalid ciphertext")
	}

	// 32-byte channel secret: 16-byte key + 16 zero bytes
	channelSecret := make([]byte, 32)
	copy(channelSecret, channelKey)

	// Verify HMAC-SHA256 (first 2 bytes must match provided MAC)
	h := hmac.New(sha256.New, channelSecret)
	h.Write(ciphertext)
	calculatedMac := h.Sum(nil)
	if calculatedMac[0] != macBytes[0] || calculatedMac[1] != macBytes[1] {
		return nil, fmt.Errorf("MAC verification failed")
	}

	// AES-128-ECB decrypt (block-by-block, no padding)
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not aligned to AES block size")
	}
	block, err := aes.NewCipher(channelKey)
	if err != nil {
		return nil, fmt.Errorf("AES cipher: %w", err)
	}
	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}

	// Parse: timestamp(4 LE) + flags(1) + message(UTF-8, null-terminated)
	if len(plaintext) < 5 {
		return nil, fmt.Errorf("decrypted content too short")
	}
	timestamp := binary.LittleEndian.Uint32(plaintext[0:4])
	flags := plaintext[4]
	messageText := string(plaintext[5:])
	if idx := strings.IndexByte(messageText, 0); idx >= 0 {
		messageText = messageText[:idx]
	}

	// Validate decrypted text is printable UTF-8 (not binary garbage)
	if !utf8.ValidString(messageText) || countNonPrintable(messageText) > 2 {
		return nil, fmt.Errorf("decrypted text contains non-printable characters")
	}

	result := &channelDecryptResult{Timestamp: timestamp, Flags: flags}

	// Parse "sender: message" format
	colonIdx := strings.Index(messageText, ": ")
	if colonIdx > 0 && colonIdx < 50 {
		potentialSender := messageText[:colonIdx]
		if !strings.ContainsAny(potentialSender, ":[]") {
			result.Sender = potentialSender
			result.Message = messageText[colonIdx+2:]
		} else {
			result.Message = messageText
		}
	} else {
		result.Message = messageText
	}

	return result, nil
}

// retroactiveDecrypt scans undecrypted GRP_TXT packets and attempts decryption
// with the given channel name and key. Returns the number of packets decrypted.
func retroactiveDecrypt(dbPath string, readDB *DB, channelName, keyHex string) (int, error) {
	// Query undecrypted GRP_TXT packets (payload_type=5 for GRP_TXT)
	rows, err := readDB.conn.Query(`
		SELECT id, decoded_json FROM transmissions
		WHERE payload_type = 5
		  AND (decoded_json IS NULL
		       OR decoded_json NOT LIKE '%"decryptionStatus":"decrypted"%')
		ORDER BY first_seen DESC
		LIMIT 10000
	`)
	if err != nil {
		return 0, fmt.Errorf("query undecrypted packets: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		id          int64
		decodedJSON sql.NullString
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.decodedJSON); err != nil {
			continue
		}
		candidates = append(candidates, c)
	}

	if len(candidates) == 0 {
		return 0, nil
	}

	// Open RW connection for updates
	rw, err := openRW(dbPath)
	if err != nil {
		return 0, fmt.Errorf("open RW for retroactive decrypt: %w", err)
	}
	defer rw.Close()

	tx, err := rw.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}

	decrypted := 0
	for _, c := range candidates {
		if !c.decodedJSON.Valid || c.decodedJSON.String == "" {
			continue
		}

		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(c.decodedJSON.String), &decoded); err != nil {
			continue
		}

		// Get the encrypted data and MAC from the decoded JSON
		encData, _ := decoded["encryptedData"].(string)
		mac, _ := decoded["mac"].(string)
		if encData == "" || mac == "" {
			continue
		}

		// Skip if already decrypted
		if status, _ := decoded["decryptionStatus"].(string); status == "decrypted" {
			continue
		}

		// Attempt decryption
		result, err := decryptChannelMessage(encData, mac, keyHex)
		if err != nil {
			continue
		}

		// Build updated decoded JSON
		text := result.Message
		if result.Sender != "" && result.Message != "" {
			text = result.Sender + ": " + result.Message
		}
		decoded["type"] = "CHAN"
		decoded["channel"] = channelName
		decoded["decryptionStatus"] = "decrypted"
		decoded["sender"] = result.Sender
		decoded["text"] = text
		decoded["sender_timestamp"] = result.Timestamp

		updatedJSON, err := json.Marshal(decoded)
		if err != nil {
			continue
		}

		_, err = tx.Exec(`UPDATE transmissions SET decoded_json = ? WHERE id = ?`, string(updatedJSON), c.id)
		if err != nil {
			log.Printf("[channels] retroactive decrypt update failed for id=%d: %v", c.id, err)
			continue
		}
		decrypted++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit retroactive decrypt: %w", err)
	}

	return decrypted, nil
}
