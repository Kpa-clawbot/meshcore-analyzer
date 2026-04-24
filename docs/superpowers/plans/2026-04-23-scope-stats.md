# Scope Stats Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a "Scopes" tab to the Analytics page showing scoped vs non-scoped transport-route statistics with per-region breakdowns, driven by a `hashRegions` config list.

**Architecture:** At ingest, each transport-route packet (route_type 0 or 3) with Code1 ≠ `0000` is matched against HMAC-SHA256 codes derived from configured region names — mirroring the `hashChannels`/`channel_hash` pattern. The matched name (or `""` for unknown) goes into a new `scope_name` column. The server exposes `/api/scope-stats?window=` which the Analytics "Scopes" tab calls for summary counts, a per-region table, and a two-line time-series chart.

**Tech Stack:** Go (`crypto/hmac`, `crypto/sha256`), SQLite, vanilla JS (no new libraries)

**Spec:** `docs/superpowers/specs/2026-04-23-scope-stats-design.md`

---

## File Map

| File | Change |
|---|---|
| `cmd/ingestor/decoder.go` | Add `PayloadRaw []byte` to `DecodedPacket` |
| `cmd/ingestor/decoder_test.go` | Test `PayloadRaw` is populated |
| `cmd/ingestor/config.go` | Add `HashRegions []string` to `Config` |
| `cmd/ingestor/main.go` | Add `loadRegionKeys`, `matchScope`, call backfill |
| `cmd/ingestor/main_test.go` | Tests for `loadRegionKeys` and `matchScope` |
| `cmd/ingestor/db.go` | Migration for `scope_name` column + `BackfillScopeNames` method |
| `cmd/ingestor/db_test.go` | Test migration and backfill |
| `cmd/server/db.go` | Add `hasScopeName` to schema detection + `GetScopeStats` |
| `cmd/server/db_test.go` | Test `GetScopeStats` |
| `cmd/server/types.go` | Add `ScopeStatsResponse` and sub-types |
| `cmd/server/routes.go` | Add `handleScopeStats`, register route, add cache fields |
| `public/analytics.js` | Add "Scopes" tab button + `renderScopesTab` function |
| `public/index.html` | No change needed (tab is inside Analytics) |

---

## Task 1: Expose `PayloadRaw` in the decoder

**Files:**
- Modify: `cmd/ingestor/decoder.go` — `DecodedPacket` struct + `DecodePacket` function
- Test: `cmd/ingestor/decoder_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/ingestor/decoder_test.go`:

```go
func TestDecodePacketPayloadRaw(t *testing.T) {
	// Build a minimal TRANSPORT_FLOOD packet (route_type=0):
	// header(1) + transport_codes(4) + path_len(1) + payload(N)
	// Header 0x00 = route_type=TRANSPORT_FLOOD, payload_type=0, version=0
	// Code1=9A52, Code2=0000, path_len=0x00 (0 hops, hash_size=1)
	payload := []byte("hello")
	raw := []byte{0x00, 0x9A, 0x52, 0x00, 0x00, 0x00}
	raw = append(raw, payload...)
	hexStr := strings.ToUpper(hex.EncodeToString(raw))

	decoded, err := DecodePacket(hexStr, nil, false)
	if err != nil {
		t.Fatalf("DecodePacket: %v", err)
	}
	if decoded.TransportCodes == nil {
		t.Fatal("expected TransportCodes, got nil")
	}
	if string(decoded.PayloadRaw) != string(payload) {
		t.Errorf("PayloadRaw = %v, want %v", decoded.PayloadRaw, payload)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```
cd cmd/ingestor && go test -run TestDecodePacketPayloadRaw -v
```
Expected: compile error — `PayloadRaw` field does not exist on `DecodedPacket`.

- [ ] **Step 3: Add `PayloadRaw` to `DecodedPacket`**

In `cmd/ingestor/decoder.go`, find the `DecodedPacket` struct (around line 141) and add the field:

```go
type DecodedPacket struct {
	Header         Header          `json:"header"`
	TransportCodes *TransportCodes `json:"transportCodes"`
	Path           Path            `json:"path"`
	Payload        Payload         `json:"payload"`
	Raw            string          `json:"raw"`
	Anomaly        string          `json:"anomaly,omitempty"`
	PayloadRaw     []byte          `json:"-"` // raw encrypted payload bytes, for HMAC matching
}
```

Then in `DecodePacket` (around line 589), after `payloadBuf := buf[offset:]`, populate the field in the return statement (around line 635):

```go
return &DecodedPacket{
	Header:         header,
	TransportCodes: tc,
	Path:           path,
	Payload:        payload,
	Raw:            strings.ToUpper(hexString),
	Anomaly:        anomaly,
	PayloadRaw:     payloadBuf,
}, nil
```

- [ ] **Step 4: Run test to verify it passes**

```
cd cmd/ingestor && go test -run TestDecodePacketPayloadRaw -v
```
Expected: PASS

- [ ] **Step 5: Run all ingestor tests**

```
cd cmd/ingestor && go test ./... -v 2>&1 | tail -20
```
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/ingestor/decoder.go cmd/ingestor/decoder_test.go
git commit -m "feat(ingestor/decoder): expose PayloadRaw bytes on DecodedPacket (#899)"
```

---

## Task 2: Config field + region key helpers

**Files:**
- Modify: `cmd/ingestor/config.go` — add `HashRegions []string`
- Modify: `cmd/ingestor/main.go` — add `loadRegionKeys` and `matchScope`
- Test: `cmd/ingestor/main_test.go`

- [ ] **Step 1: Write failing tests**

Add to `cmd/ingestor/main_test.go`:

```go
func TestLoadRegionKeys(t *testing.T) {
	cfg := &Config{HashRegions: []string{"#belgium", "eu", "  #Test  ", "", "#belgium"}}
	keys := loadRegionKeys(cfg)

	// Deduplication + normalization
	if len(keys) != 3 {
		t.Fatalf("len(keys) = %d, want 3", len(keys))
	}
	// "#belgium" key = SHA256("#belgium")[:16]
	h := sha256.Sum256([]byte("#belgium"))
	want := h[:16]
	if got := keys["#belgium"]; !bytes.Equal(got, want) {
		t.Errorf("#belgium key mismatch: got %x, want %x", got, want)
	}
	// "eu" should be normalized to "#eu"
	if _, ok := keys["#eu"]; !ok {
		t.Error("expected #eu key")
	}
	// "  #Test  " should be normalized to "#Test"
	if _, ok := keys["#Test"]; !ok {
		t.Error("expected #Test key")
	}
}

func TestMatchScope(t *testing.T) {
	// Build a known Code1 for region "#test" and payload type 5, payload "hello"
	name := "#test"
	h := sha256.Sum256([]byte(name))
	key := h[:16]

	payloadType := byte(0x05)
	payloadRaw := []byte("hello")

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte{payloadType})
	mac.Write(payloadRaw)
	hmacBytes := mac.Sum(nil)
	code := uint16(hmacBytes[0]) | uint16(hmacBytes[1])<<8
	if code == 0 {
		code = 1
	} else if code == 0xFFFF {
		code = 0xFFFE
	}
	code1Bytes := [2]byte{byte(code & 0xFF), byte(code >> 8)}
	code1 := strings.ToUpper(hex.EncodeToString(code1Bytes[:]))

	regionKeys := map[string][]byte{name: key}

	got := matchScope(regionKeys, payloadType, payloadRaw, code1)
	if got != name {
		t.Errorf("matchScope = %q, want %q", got, name)
	}

	// Unscoped (Code1 = 0000) → empty
	if got := matchScope(regionKeys, payloadType, payloadRaw, "0000"); got != "" {
		t.Errorf("unscoped: matchScope = %q, want empty", got)
	}

	// Scoped but no match → empty string sentinel
	if got := matchScope(regionKeys, payloadType, payloadRaw, "BEEF"); got != "" {
		t.Errorf("no match: matchScope = %q, want empty", got)
	}
}
```

Also add the needed imports to the test file (`bytes`, `crypto/hmac`, `crypto/sha256`, `encoding/hex`, `strings`).

- [ ] **Step 2: Run to verify they fail**

```
cd cmd/ingestor && go test -run "TestLoadRegionKeys|TestMatchScope" -v
```
Expected: compile error — `loadRegionKeys` and `matchScope` not defined.

- [ ] **Step 3: Add `HashRegions` to Config**

In `cmd/ingestor/config.go`, add the field after `HashChannels`:

```go
HashChannels    []string          `json:"hashChannels,omitempty"`
HashRegions     []string          `json:"hashRegions,omitempty"`
```

- [ ] **Step 4: Add `loadRegionKeys` and `matchScope` to main.go**

Add to `cmd/ingestor/main.go` (near `loadChannelKeys`, around line 755):

```go
// loadRegionKeys derives 16-byte HMAC keys from configured region names.
// Key derivation matches firmware: SHA256("#regionname")[:16].
// Names without a leading '#' are prefixed automatically.
func loadRegionKeys(cfg *Config) map[string][]byte {
	keys := make(map[string][]byte)
	for _, raw := range cfg.HashRegions {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if !strings.HasPrefix(name, "#") {
			name = "#" + name
		}
		if _, exists := keys[name]; exists {
			continue // deduplicate
		}
		h := sha256.Sum256([]byte(name))
		keys[name] = h[:16]
	}
	if len(keys) > 0 {
		log.Printf("[regions] %d region key(s) loaded", len(keys))
	}
	return keys
}

// matchScope tries each configured region key against Code1 using the same
// HMAC derivation as the firmware (TransportKey::calcTransportCode).
// Returns the matched region name, "" if scoped but no match, or "" if Code1 is "0000".
func matchScope(regionKeys map[string][]byte, payloadType byte, payloadRaw []byte, code1 string) string {
	if code1 == "0000" || len(regionKeys) == 0 || len(payloadRaw) == 0 {
		return ""
	}
	for name, key := range regionKeys {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte{payloadType})
		mac.Write(payloadRaw)
		hmacBytes := mac.Sum(nil)
		code := uint16(hmacBytes[0]) | uint16(hmacBytes[1])<<8
		if code == 0 {
			code = 1
		} else if code == 0xFFFF {
			code = 0xFFFE
		}
		codeBytes := [2]byte{byte(code & 0xFF), byte(code >> 8)}
		if strings.ToUpper(hex.EncodeToString(codeBytes[:])) == code1 {
			return name
		}
	}
	return "" // scoped but no configured region matched
}
```

Add `"crypto/hmac"` to the imports in `main.go` (it already imports `crypto/sha256`).

- [ ] **Step 5: Run tests**

```
cd cmd/ingestor && go test -run "TestLoadRegionKeys|TestMatchScope" -v
```
Expected: PASS

- [ ] **Step 6: Run all ingestor tests**

```
cd cmd/ingestor && go test ./... 2>&1 | tail -10
```
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add cmd/ingestor/config.go cmd/ingestor/main.go cmd/ingestor/main_test.go
git commit -m "feat(ingestor): add hashRegions config + loadRegionKeys + matchScope (#899)"
```

---

## Task 3: DB migration — `scope_name` column

**Files:**
- Modify: `cmd/ingestor/db.go` — migration block + `BackfillScopeNames`
- Test: `cmd/ingestor/db_test.go` (or `main_test.go`)

- [ ] **Step 1: Write the failing migration test**

Add to `cmd/ingestor/db_test.go` (create the file if it doesn't exist, otherwise append):

```go
func TestScopeNameMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Verify scope_name column exists
	rows, err := store.db.Query("PRAGMA table_info(transmissions)")
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk); err == nil {
			if colName == "scope_name" {
				found = true
			}
		}
	}
	if !found {
		t.Error("scope_name column not found in transmissions")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```
cd cmd/ingestor && go test -run TestScopeNameMigration -v
```
Expected: FAIL — scope_name column not found.

- [ ] **Step 3: Add the migration to `cmd/ingestor/db.go`**

Append after the last migration block (after the `observations_raw_hex_v1` block, before `return nil`):

```go
// Migration: add scope_name column for transport-route region matching (#899)
row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'scope_name_v1'")
if row.Scan(&migDone) != nil {
	log.Println("[migration] Adding scope_name column to transmissions...")
	db.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_tx_scope_name ON transmissions(scope_name) WHERE scope_name IS NOT NULL`)
	db.Exec(`INSERT INTO _migrations (name) VALUES ('scope_name_v1')`)
	log.Println("[migration] scope_name column added")
}
```

- [ ] **Step 4: Run test to verify it passes**

```
cd cmd/ingestor && go test -run TestScopeNameMigration -v
```
Expected: PASS

- [ ] **Step 5: Add `BackfillScopeNames` to `cmd/ingestor/db.go`**

Add this method to `Store` (near the bottom of db.go, before the closing brace):

```go
// BackfillScopeNames re-decodes raw_hex for existing transport-route rows
// and populates scope_name using the given region keys.
// Skips rows that already have scope_name set.
// Safe to call with empty regionKeys — returns immediately.
func (s *Store) BackfillScopeNames(regionKeys map[string][]byte) {
	if len(regionKeys) == 0 {
		return
	}
	rows, err := s.db.Query(`
		SELECT id, raw_hex FROM transmissions
		WHERE route_type IN (0, 3) AND scope_name IS NULL AND raw_hex IS NOT NULL
	`)
	if err != nil {
		log.Printf("[backfill] scope_name query: %v", err)
		return
	}
	defer rows.Close()

	type row struct {
		id     int64
		rawHex string
	}
	var pending []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.rawHex) == nil && r.rawHex != "" {
			pending = append(pending, r)
		}
	}

	updated := 0
	for _, r := range pending {
		decoded, err := DecodePacket(r.rawHex, nil, false)
		if err != nil || decoded.TransportCodes == nil {
			continue
		}
		if decoded.TransportCodes.Code1 == "0000" {
			continue // unscoped transport — leave NULL
		}
		scopeName := matchScope(regionKeys, byte(decoded.Header.PayloadType), decoded.PayloadRaw, decoded.TransportCodes.Code1)
		// scopeName == "" means scoped but unknown — write empty string to distinguish from NULL
		s.db.Exec(`UPDATE transmissions SET scope_name = ? WHERE id = ?`, scopeName, r.id)
		updated++
	}
	if updated > 0 {
		log.Printf("[backfill] scope_name set for %d/%d transport-route rows", updated, len(pending))
	}
}
```

- [ ] **Step 6: Add backfill test**

Add to `cmd/ingestor/db_test.go`:

```go
func TestBackfillScopeNames(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Insert a transport-route packet with known Code1
	regionName := "#test"
	h := sha256.Sum256([]byte(regionName))
	key := h[:16]
	payloadType := byte(0x05)
	payloadRaw := []byte("hello")

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte{payloadType})
	mac.Write(payloadRaw)
	hmacBytes := mac.Sum(nil)
	code := uint16(hmacBytes[0]) | uint16(hmacBytes[1])<<8
	if code == 0 { code = 1 } else if code == 0xFFFF { code = 0xFFFE }
	codeBytes := [2]byte{byte(code & 0xFF), byte(code >> 8)}

	// Build raw packet bytes: header(1) + Code1(2) + Code2(2) + path_len(1) + payload
	header := byte(0x00) | (payloadType << 2) // TRANSPORT_FLOOD + payload_type in bits 2-5
	raw := []byte{header}
	raw = append(raw, codeBytes[:]...)
	raw = append(raw, 0x00, 0x00) // Code2 = 0000
	raw = append(raw, 0x00)       // path_len = 0 hops
	raw = append(raw, payloadRaw...)
	rawHex := strings.ToUpper(hex.EncodeToString(raw))

	store.db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
		VALUES (?, 'testhash1', datetime('now'), 0, 5, 0, '{}')`, rawHex)

	store.BackfillScopeNames(map[string][]byte{regionName: key})

	var scopeName *string
	store.db.QueryRow(`SELECT scope_name FROM transmissions WHERE hash = 'testhash1'`).Scan(&scopeName)
	if scopeName == nil || *scopeName != regionName {
		t.Errorf("scope_name = %v, want %q", scopeName, regionName)
	}
}
```

Add needed imports to db_test.go: `crypto/hmac`, `crypto/sha256`, `encoding/hex`, `strings`.

- [ ] **Step 7: Run tests**

```
cd cmd/ingestor && go test -run "TestScopeNameMigration|TestBackfillScopeNames" -v
```
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add cmd/ingestor/db.go cmd/ingestor/db_test.go
git commit -m "feat(ingestor/db): add scope_name migration and BackfillScopeNames (#899)"
```

---

## Task 4: Wire scope matching into ingest + backfill call

**Files:**
- Modify: `cmd/ingestor/db.go` — `PacketData` struct, `stmtInsertTransmission`, `InsertTransmission`
- Modify: `cmd/ingestor/main.go` — `BuildPacketData` and startup call to `BackfillScopeNames`

- [ ] **Step 1: Write the failing integration test**

Add to `cmd/ingestor/main_test.go`:

```go
func TestBuildPacketDataScopeMatching(t *testing.T) {
	// Build region key for "#test"
	regionName := "#test"
	h := sha256.Sum256([]byte(regionName))
	key := h[:16]

	payloadType := byte(0x05)
	payloadRaw := []byte("hello")

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte{payloadType})
	mac.Write(payloadRaw)
	hmacBytes := mac.Sum(nil)
	code := uint16(hmacBytes[0]) | uint16(hmacBytes[1])<<8
	if code == 0 { code = 1 } else if code == 0xFFFF { code = 0xFFFE }
	codeBytes := [2]byte{byte(code & 0xFF), byte(code >> 8)}

	// TRANSPORT_FLOOD header with payloadType in bits 2-5
	header := byte(0x00) | (payloadType << 2)
	raw := []byte{header}
	raw = append(raw, codeBytes[:]...)
	raw = append(raw, 0x00, 0x00) // Code2
	raw = append(raw, 0x00)       // path_len
	raw = append(raw, payloadRaw...)
	rawHex := strings.ToUpper(hex.EncodeToString(raw))

	decoded, err := DecodePacket(rawHex, nil, false)
	if err != nil {
		t.Fatalf("DecodePacket: %v", err)
	}

	msg := &MQTTPacketMessage{Raw: rawHex}
	regionKeys := map[string][]byte{regionName: key}

	pktData := BuildPacketData(msg, decoded, "obs1", "region1", regionKeys)
	if pktData.ScopeName != regionName {
		t.Errorf("ScopeName = %q, want %q", pktData.ScopeName, regionName)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```
cd cmd/ingestor && go test -run TestBuildPacketDataScopeMatching -v
```
Expected: compile error — `ScopeName` not on `PacketData`, `BuildPacketData` signature mismatch.

- [ ] **Step 3: Add `ScopeName` to `PacketData`**

In `cmd/ingestor/db.go`, in the `PacketData` struct (around line 908):

```go
type PacketData struct {
	RawHex         string
	Timestamp      string
	ObserverID     string
	ObserverName   string
	SNR            *float64
	RSSI           *float64
	Score          *float64
	Direction      *string
	Hash           string
	RouteType      int
	PayloadType    int
	PayloadVersion int
	PathJSON       string
	DecodedJSON    string
	ChannelHash    string
	ScopeName      string // "" = scoped but unknown; only set for transport routes with Code1≠0000
}
```

- [ ] **Step 4: Update `stmtInsertTransmission` in `prepareStatements`**

In `cmd/ingestor/db.go`, find `stmtInsertTransmission` prepare (around line 432) and update:

```go
s.stmtInsertTransmission, err = s.db.Prepare(`
	INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, channel_hash, scope_name)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`)
```

- [ ] **Step 5: Update `InsertTransmission` Exec call**

In `cmd/ingestor/db.go`, find the `stmtInsertTransmission.Exec` call (around line 559) and add the `scope_name` argument:

```go
result, err := s.stmtInsertTransmission.Exec(
	data.RawHex, hash, now,
	data.RouteType, data.PayloadType, data.PayloadVersion,
	data.DecodedJSON, nilIfEmpty(data.ChannelHash),
	nilIfEmpty(data.ScopeName),
)
```

Note: `nilIfEmpty` maps `""` → `nil` (SQL NULL). But the spec requires `""` (empty string) for "scoped but unknown". Use a different helper:

Replace the last argument with:
```go
scopeNameVal,
```

And before the `Exec` call, add:
```go
var scopeNameVal interface{}
if data.RouteType == 0 || data.RouteType == 3 {
	// For transport routes, store the matched name (or "" for unknown scoped)
	// Only leave NULL for non-transport routes
	scopeNameVal = data.ScopeName // may be "" or "#regionname"
	if data.ScopeName == "" && /* check Code1 == "0000": */ (decoded TransportCodes is nil check) {
```

Wait — `InsertTransmission` takes `*PacketData`, not the decoded packet. We don't have Code1 here. We need a different approach.

Use a new convention: populate `ScopeName` in `BuildPacketData` as follows:
- Non-transport route → `ScopeName = ""` with a sentinel meaning "not applicable" → store as NULL
- Transport route + Code1 == "0000" → `ScopeName = ""` → store as NULL  
- Transport route + Code1 ≠ "0000" + no match → `ScopeName = "\x00"` (internal sentinel for "scoped/unknown")
- Transport route + Code1 ≠ "0000" + match → `ScopeName = "#regionname"`

Actually, this is getting complicated. Simpler: add `IsTransportScoped bool` to `PacketData`:

```go
type PacketData struct {
	// ... existing fields ...
	ScopeName        string // matched region name, or "" 
	IsTransportScoped bool  // true = transport route with Code1≠0000 (even if name unknown)
}
```

Then in `InsertTransmission`:
```go
var scopeNameVal interface{}
if data.IsTransportScoped {
	scopeNameVal = data.ScopeName // "" or "#regionname" — both stored as non-NULL
} // else: NULL (not a transport-scoped packet)
```

This cleanly encodes the three-state semantics without sentinels.

- [ ] **Step 5 (revised): Update `PacketData`, `InsertTransmission`, `BuildPacketData`**

In `cmd/ingestor/db.go`, `PacketData` struct:

```go
type PacketData struct {
	RawHex           string
	Timestamp        string
	ObserverID       string
	ObserverName     string
	SNR              *float64
	RSSI             *float64
	Score            *float64
	Direction        *string
	Hash             string
	RouteType        int
	PayloadType      int
	PayloadVersion   int
	PathJSON         string
	DecodedJSON      string
	ChannelHash      string
	ScopeName        string // matched region name, or "" for unknown-scoped
	IsTransportScoped bool  // true when route_type IN (0,3) AND Code1 ≠ "0000"
}
```

In `InsertTransmission` Exec call, replace `nilIfEmpty(data.ChannelHash),` line and add `scope_name`:

```go
result, err := s.stmtInsertTransmission.Exec(
	data.RawHex, hash, now,
	data.RouteType, data.PayloadType, data.PayloadVersion,
	data.DecodedJSON, nilIfEmpty(data.ChannelHash),
	scopeNameForDB(data),
)
```

Add helper function in `db.go`:

```go
// scopeNameForDB converts PacketData scope fields to the DB value.
// NULL = not a transport-scoped packet; "" = scoped but region unknown; "#name" = matched.
func scopeNameForDB(data *PacketData) interface{} {
	if !data.IsTransportScoped {
		return nil
	}
	return data.ScopeName // "" or "#regionname"
}
```

- [ ] **Step 6: Update `BuildPacketData` signature and body**

In `cmd/ingestor/main.go`, find `BuildPacketData` (around line 948) and update signature:

```go
func BuildPacketData(msg *MQTTPacketMessage, decoded *DecodedPacket, observerID, region string, regionKeys map[string][]byte) *PacketData {
```

At the end of `BuildPacketData`, before the `return pd` line, add scope matching:

```go
// Scope matching for transport-route packets
if decoded.TransportCodes != nil && decoded.TransportCodes.Code1 != "0000" {
	pd.IsTransportScoped = true
	pd.ScopeName = matchScope(regionKeys, byte(decoded.Header.PayloadType), decoded.PayloadRaw, decoded.TransportCodes.Code1)
}
```

- [ ] **Step 7: Update both `BuildPacketData` call sites in `main.go`**

Both calls (lines 354 and 377) become:

```go
pktData := BuildPacketData(mqttMsg, decoded, observerID, region, regionKeys)
```

`regionKeys` is loaded once at startup (see Step 9).

- [ ] **Step 8: Load region keys at startup and call backfill**

In `main()` in `cmd/ingestor/main.go`, after `loadChannelKeys` is called, add:

```go
regionKeys := loadRegionKeys(cfg)
```

Pass `regionKeys` to `BuildPacketData` where it's called (already done in Step 7).

Also call backfill after the store is opened (find where `store` is initialized):

```go
go store.BackfillScopeNames(regionKeys)
```

Run in a goroutine so it doesn't block startup.

- [ ] **Step 9: Run all ingestor tests**

```
cd cmd/ingestor && go test ./... 2>&1 | tail -20
```
Expected: all PASS (including `TestBuildPacketDataScopeMatching`)

- [ ] **Step 10: Commit**

```bash
git add cmd/ingestor/db.go cmd/ingestor/main.go cmd/ingestor/main_test.go
git commit -m "feat(ingestor): wire scope matching into ingest pipeline (#899)"
```

---

## Task 5: Server — schema detection + `GetScopeStats`

**Files:**
- Modify: `cmd/server/db.go` — `detectSchema`, add `hasScopeName bool`, add `GetScopeStats`
- Test: `cmd/server/db_test.go`

- [ ] **Step 1: Write failing test**

Add to `cmd/server/db_test.go`:

```go
func TestGetScopeStats(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Create minimal schema
	db.conn.Exec(`CREATE TABLE IF NOT EXISTS transmissions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		raw_hex TEXT, hash TEXT, first_seen TEXT, route_type INTEGER,
		payload_type INTEGER, payload_version INTEGER, decoded_json TEXT,
		scope_name TEXT DEFAULT NULL
	)`)
	// Manually set hasScopeName since we bypassed the detector
	db.hasScopeName = true

	now := time.Now().UTC().Format(time.RFC3339)
	// Transport scoped, known region
	db.conn.Exec(`INSERT INTO transmissions (hash, first_seen, route_type, scope_name) VALUES ('a', ?, 0, '#belgium')`, now)
	// Transport scoped, unknown
	db.conn.Exec(`INSERT INTO transmissions (hash, first_seen, route_type, scope_name) VALUES ('b', ?, 0, '')`, now)
	// Transport unscoped (NULL)
	db.conn.Exec(`INSERT INTO transmissions (hash, first_seen, route_type, scope_name) VALUES ('c', ?, 0, NULL)`, now)
	// Non-transport (should not count)
	db.conn.Exec(`INSERT INTO transmissions (hash, first_seen, route_type, scope_name) VALUES ('d', ?, 1, NULL)`, now)

	stats, err := db.GetScopeStats("24h")
	if err != nil {
		t.Fatalf("GetScopeStats: %v", err)
	}
	if stats.Summary.TransportTotal != 3 {
		t.Errorf("TransportTotal = %d, want 3", stats.Summary.TransportTotal)
	}
	if stats.Summary.Scoped != 2 {
		t.Errorf("Scoped = %d, want 2", stats.Summary.Scoped)
	}
	if stats.Summary.Unscoped != 1 {
		t.Errorf("Unscoped = %d, want 1", stats.Summary.Unscoped)
	}
	if stats.Summary.UnknownScope != 1 {
		t.Errorf("UnknownScope = %d, want 1", stats.Summary.UnknownScope)
	}
	if len(stats.ByRegion) != 1 || stats.ByRegion[0].Name != "#belgium" || stats.ByRegion[0].Count != 1 {
		t.Errorf("ByRegion = %+v, want [{#belgium 1}]", stats.ByRegion)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```
cd cmd/server && go test -run TestGetScopeStats -v
```
Expected: compile error — `GetScopeStats` not defined, `hasScopeName` not a field.

- [ ] **Step 3: Add `hasScopeName` to DB struct and `detectSchema`**

In `cmd/server/db.go`, add to `DB` struct:

```go
type DB struct {
	conn             *sql.DB
	path             string
	isV3             bool
	hasResolvedPath  bool
	hasObsRawHex     bool
	hasScopeName     bool  // transmissions.scope_name column exists (#899)
	// ... cache fields ...
}
```

In `detectSchema`, in the loop that scans column names, add:

```go
if colName == "scope_name" {
	db.hasScopeName = true
}
```

- [ ] **Step 4: Add `ScopeStatsResponse` types to `cmd/server/types.go`**

Add after the `StatsResponse` section:

```go
// ─── Scope Stats ───────────────────────────────────────────────────────────────

type ScopeStatsSummary struct {
	TransportTotal int `json:"transportTotal"`
	Scoped         int `json:"scoped"`
	Unscoped       int `json:"unscoped"`
	UnknownScope   int `json:"unknownScope"`
}

type ScopeRegionCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type ScopeTimePoint struct {
	T       string `json:"t"`
	Scoped  int    `json:"scoped"`
	Unscoped int   `json:"unscoped"`
}

type ScopeStatsResponse struct {
	Window     string             `json:"window"`
	Summary    ScopeStatsSummary  `json:"summary"`
	ByRegion   []ScopeRegionCount `json:"byRegion"`
	TimeSeries []ScopeTimePoint   `json:"timeSeries"`
}
```

- [ ] **Step 5: Add `GetScopeStats` to `cmd/server/db.go`**

Add at the end of db.go, before the closing line:

```go
// GetScopeStats returns scope statistics for the given window ("1h", "24h", "7d").
func (db *DB) GetScopeStats(window string) (*ScopeStatsResponse, error) {
	if !db.hasScopeName {
		return nil, fmt.Errorf("scope_name column not present — run ingestor to apply migrations")
	}

	var since string
	var bucketExpr string
	switch window {
	case "1h":
		since = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
		// 5-minute buckets
		bucketExpr = `strftime('%Y-%m-%dT%H:', first_seen) || printf('%02d', (CAST(strftime('%M', first_seen) AS INTEGER) / 5) * 5) || ':00Z'`
	case "7d":
		since = time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
		// 6-hour buckets
		bucketExpr = `strftime('%Y-%m-%dT', first_seen) || printf('%02d', (CAST(strftime('%H', first_seen) AS INTEGER) / 6) * 6) || ':00:00Z'`
	default: // "24h"
		window = "24h"
		since = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
		// 1-hour buckets
		bucketExpr = `strftime('%Y-%m-%dT%H:00:00Z', first_seen)`
	}

	resp := &ScopeStatsResponse{Window: window}

	// Summary counts
	row := db.conn.QueryRow(`
		SELECT
			COUNT(*) AS transport_total,
			COUNT(scope_name) AS scoped,
			SUM(CASE WHEN scope_name IS NULL THEN 1 ELSE 0 END) AS unscoped,
			SUM(CASE WHEN scope_name = '' THEN 1 ELSE 0 END) AS unknown_scope
		FROM transmissions
		WHERE route_type IN (0, 3) AND first_seen >= ?
	`, since)
	if err := row.Scan(
		&resp.Summary.TransportTotal,
		&resp.Summary.Scoped,
		&resp.Summary.Unscoped,
		&resp.Summary.UnknownScope,
	); err != nil {
		return nil, fmt.Errorf("scope summary query: %w", err)
	}

	// Per-region counts (named regions only)
	rows, err := db.conn.Query(`
		SELECT scope_name, COUNT(*) AS cnt
		FROM transmissions
		WHERE route_type IN (0, 3) AND scope_name IS NOT NULL AND scope_name != '' AND first_seen >= ?
		GROUP BY scope_name
		ORDER BY cnt DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("scope byRegion query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rc ScopeRegionCount
		if rows.Scan(&rc.Name, &rc.Count) == nil {
			resp.ByRegion = append(resp.ByRegion, rc)
		}
	}
	if resp.ByRegion == nil {
		resp.ByRegion = []ScopeRegionCount{}
	}

	// Time series
	tsQuery := fmt.Sprintf(`
		SELECT %s AS bucket,
			COUNT(scope_name) AS scoped,
			SUM(CASE WHEN scope_name IS NULL THEN 1 ELSE 0 END) AS unscoped
		FROM transmissions
		WHERE route_type IN (0, 3) AND first_seen >= ?
		GROUP BY bucket
		ORDER BY bucket
	`, bucketExpr)
	tsRows, err := db.conn.Query(tsQuery, since)
	if err != nil {
		return nil, fmt.Errorf("scope timeseries query: %w", err)
	}
	defer tsRows.Close()
	for tsRows.Next() {
		var pt ScopeTimePoint
		if tsRows.Scan(&pt.T, &pt.Scoped, &pt.Unscoped) == nil {
			resp.TimeSeries = append(resp.TimeSeries, pt)
		}
	}
	if resp.TimeSeries == nil {
		resp.TimeSeries = []ScopeTimePoint{}
	}

	return resp, nil
}
```

- [ ] **Step 6: Run test**

```
cd cmd/server && go test -run TestGetScopeStats -v
```
Expected: PASS

- [ ] **Step 7: Run all server tests**

```
cd cmd/server && go test ./... 2>&1 | tail -20
```
Expected: all PASS

- [ ] **Step 8: Commit**

```bash
git add cmd/server/db.go cmd/server/db_test.go cmd/server/types.go
git commit -m "feat(server/db): add GetScopeStats and ScopeStatsResponse types (#899)"
```

---

## Task 6: Server — HTTP handler + route registration

**Files:**
- Modify: `cmd/server/routes.go` — add cache fields to `Server`, register route, add `handleScopeStats`

- [ ] **Step 1: Write failing handler test**

Add to `cmd/server/routes_test.go`:

```go
func TestHandleScopeStats(t *testing.T) {
	srv := newTestServer(t)
	// Manually mark hasScopeName on the test DB
	srv.db.hasScopeName = true

	req := httptest.NewRequest("GET", "/api/scope-stats?window=24h", nil)
	w := httptest.NewRecorder()
	srv.handleScopeStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp ScopeStatsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Window != "24h" {
		t.Errorf("window = %q, want 24h", resp.Window)
	}
	// TimeSeries and ByRegion are always non-nil slices
	if resp.TimeSeries == nil {
		t.Error("timeSeries is nil, want empty slice")
	}
	if resp.ByRegion == nil {
		t.Error("byRegion is nil, want empty slice")
	}
}
```

Check `routes_test.go` for how `newTestServer` is implemented and adapt if needed.

- [ ] **Step 2: Run to verify it fails**

```
cd cmd/server && go test -run TestHandleScopeStats -v
```
Expected: compile error — `handleScopeStats` not defined.

- [ ] **Step 3: Add cache fields to `Server` struct**

In `cmd/server/routes.go`, add to `Server` struct (near the other cache fields):

```go
// Scope stats cache (30s TTL)
scopeStatsMu      sync.Mutex
scopeStatsCache   map[string]*ScopeStatsResponse // keyed by window param
scopeStatsCachedAt map[string]time.Time
```

- [ ] **Step 4: Register the route**

In the route registration block in `cmd/server/routes.go` (near `/api/stats`):

```go
r.HandleFunc("/api/scope-stats", s.handleScopeStats).Methods("GET")
```

- [ ] **Step 5: Add `handleScopeStats`**

Add to `cmd/server/routes.go`:

```go
func (s *Server) handleScopeStats(w http.ResponseWriter, r *http.Request) {
	const scopeStatsTTL = 30 * time.Second

	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	if window != "1h" && window != "24h" && window != "7d" {
		writeError(w, 400, "window must be 1h, 24h, or 7d")
		return
	}

	s.scopeStatsMu.Lock()
	if s.scopeStatsCache != nil {
		if cached, ok := s.scopeStatsCache[window]; ok && time.Since(s.scopeStatsCachedAt[window]) < scopeStatsTTL {
			s.scopeStatsMu.Unlock()
			writeJSON(w, cached)
			return
		}
	}
	s.scopeStatsMu.Unlock()

	resp, err := s.db.GetScopeStats(window)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	s.scopeStatsMu.Lock()
	if s.scopeStatsCache == nil {
		s.scopeStatsCache = make(map[string]*ScopeStatsResponse)
		s.scopeStatsCachedAt = make(map[string]time.Time)
	}
	s.scopeStatsCache[window] = resp
	s.scopeStatsCachedAt[window] = time.Now()
	s.scopeStatsMu.Unlock()

	writeJSON(w, resp)
}
```

- [ ] **Step 6: Run tests**

```
cd cmd/server && go test -run TestHandleScopeStats -v
```
Expected: PASS

- [ ] **Step 7: Run all server tests**

```
cd cmd/server && go test ./... 2>&1 | tail -20
```
Expected: all PASS

- [ ] **Step 8: Commit**

```bash
git add cmd/server/routes.go cmd/server/routes_test.go
git commit -m "feat(server): add /api/scope-stats endpoint (#899)"
```

---

## Task 7: Update API spec docs

**Files:**
- Modify: `docs/api-spec.md`

- [ ] **Step 1: Add `GET /api/scope-stats` to `docs/api-spec.md`**

Open `docs/api-spec.md` and add an entry for the new endpoint following the existing format. Include: method, path, query params (`window`: `1h`/`24h`/`7d`), response shape with the `ScopeStatsResponse` JSON, and a note that it requires the ingestor migration to have run.

- [ ] **Step 2: Commit**

```bash
git add docs/api-spec.md
git commit -m "docs: add /api/scope-stats to api-spec (#899)"
```

---

## Task 8: Frontend — "Scopes" tab in Analytics

**Files:**
- Modify: `public/analytics.js` — tab button + `renderScopesTab` function
- Modify: `public/index.html` — bump `__BUST__` version for `analytics.js`

- [ ] **Step 1: Add the tab button**

In `public/analytics.js`, find the tab buttons list (around line 88). Add after `<button class="tab-btn" data-tab="prefix-tool">Prefix Tool</button>`:

```html
<button class="tab-btn" data-tab="scopes">Scopes</button>
```

- [ ] **Step 2: Wire the tab in `renderTab`**

In `renderTab` (around line 186), add before the closing `}`:

```js
case 'scopes': await renderScopesTab(el); break;
```

- [ ] **Step 3: Add `renderScopesTab` function**

Add just before the `registerPage('analytics', ...)` line at the end of `analytics.js`:

```js
// ===================== SCOPES =====================
async function renderScopesTab(el) {
  var window = 'scopes_window';
  var selectedWindow = (typeof sessionStorage !== 'undefined' && sessionStorage.getItem(window)) || '24h';

  async function load(w) {
    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading scope stats…</div>';
    try {
      var data = await (await fetch('/api/scope-stats?window=' + encodeURIComponent(w))).json();
      if (data.error) {
        el.innerHTML = '<div class="text-center text-muted" style="padding:40px">' + esc(data.error) + '</div>';
        return;
      }
      render(data, w);
    } catch (err) {
      el.innerHTML = '<div class="text-center" style="color:var(--status-red);padding:40px">Failed to load scope stats: ' + esc(String(err)) + '</div>';
    }
  }

  function pct(n, total) {
    if (!total) return '—';
    return (n / total * 100).toFixed(1) + '%';
  }

  function render(d, w) {
    var s = d.summary;
    var total = s.transportTotal || 0;

    // Window selector
    var winHtml = ['1h', '24h', '7d'].map(function(v) {
      return '<button class="tab-btn' + (w === v ? ' active' : '') + '" data-win="' + v + '">' + v + '</button>';
    }).join('');

    // Summary cards
    var cardsHtml = [
      { label: 'Transport Total', value: total.toLocaleString(), note: '' },
      { label: 'Scoped', value: s.scoped.toLocaleString(), note: pct(s.scoped, total) },
      { label: 'Unscoped', value: s.unscoped.toLocaleString(), note: pct(s.unscoped, total) },
      { label: 'Unknown Scope', value: s.unknownScope.toLocaleString(), note: pct(s.unknownScope, s.scoped) + ' of scoped' },
    ].map(function(c) {
      return '<div class="stat-card"><div class="stat-value">' + c.value + '</div>' +
        '<div class="stat-label">' + c.label + '</div>' +
        (c.note ? '<div class="stat-note text-muted" style="font-size:11px">' + c.note + '</div>' : '') +
        '</div>';
    }).join('');

    // Per-region table
    var tableBody = '';
    if (d.byRegion && d.byRegion.length) {
      tableBody = d.byRegion.map(function(r) {
        return '<tr><td><code>' + esc(r.name) + '</code></td>' +
          '<td>' + r.count.toLocaleString() + '</td>' +
          '<td>' + pct(r.count, s.scoped) + '</td></tr>';
      }).join('');
      if (s.unknownScope > 0) {
        tableBody += '<tr><td><em class="text-muted">Unknown scope</em></td>' +
          '<td>' + s.unknownScope.toLocaleString() + '</td>' +
          '<td>' + pct(s.unknownScope, s.scoped) + '</td></tr>';
      }
    } else if (s.scoped === 0) {
      tableBody = '<tr><td colspan="3" class="text-muted" style="text-align:center">No scoped messages in this window</td></tr>';
    } else {
      tableBody = '<tr><td colspan="3" class="text-muted" style="text-align:center">No regions configured — add <code>hashRegions</code> to your config</td></tr>';
    }

    // Time-series chart (two-line SVG)
    var chartHtml = '';
    if (d.timeSeries && d.timeSeries.length > 1) {
      var scopedVals = d.timeSeries.map(function(p) { return p.scoped; });
      var unscopedVals = d.timeSeries.map(function(p) { return p.unscoped; });
      var maxVal = Math.max(1, Math.max.apply(null, scopedVals.concat(unscopedVals)));
      var W = 800, H = 180, padL = 44, padB = 24, padT = 10, padR = 10;
      var plotW = W - padL - padR, plotH = H - padB - padT;
      var n = d.timeSeries.length;

      function pts(vals) {
        return vals.map(function(v, i) {
          var x = padL + i * plotW / Math.max(n - 1, 1);
          var y = padT + plotH - (v / maxVal) * plotH;
          return x.toFixed(1) + ',' + y.toFixed(1);
        }).join(' ');
      }

      // Grid lines
      var grid = '';
      for (var gi = 0; gi <= 4; gi++) {
        var gy = padT + plotH * gi / 4;
        var gv = Math.round(maxVal * (4 - gi) / 4);
        grid += '<line x1="' + padL + '" y1="' + gy.toFixed(1) + '" x2="' + (W - padR) + '" y2="' + gy.toFixed(1) + '" stroke="var(--border)" stroke-dasharray="2"/>';
        grid += '<text x="' + (padL - 4) + '" y="' + (gy + 4).toFixed(1) + '" text-anchor="end" font-size="9" fill="var(--text-muted)">' + gv + '</text>';
      }

      var legendX = padL + plotW - 120;
      chartHtml = '<div style="margin-top:16px">' +
        '<svg viewBox="0 0 ' + W + ' ' + H + '" style="width:100%;max-height:' + H + 'px" role="img" aria-label="Scope time series">' +
        grid +
        '<polyline points="' + pts(scopedVals) + '" fill="none" stroke="var(--accent)" stroke-width="2"/>' +
        '<polyline points="' + pts(unscopedVals) + '" fill="none" stroke="var(--text-muted)" stroke-width="1.5" stroke-dasharray="4"/>' +
        '<rect x="' + legendX + '" y="' + padT + '" width="10" height="10" fill="var(--accent)"/>' +
        '<text x="' + (legendX + 14) + '" y="' + (padT + 9) + '" font-size="10" fill="var(--text)">Scoped</text>' +
        '<rect x="' + legendX + '" y="' + (padT + 16) + '" width="10" height="10" fill="var(--text-muted)"/>' +
        '<text x="' + (legendX + 14) + '" y="' + (padT + 25) + '" font-size="10" fill="var(--text)">Unscoped</text>' +
        '</svg></div>';
    }

    el.innerHTML =
      '<h3 style="margin:0 0 12px">🔭 Scope Statistics</h3>' +
      '<div style="margin-bottom:12px">' + winHtml + '</div>' +
      '<div class="stats-grid" style="margin-bottom:16px">' + cardsHtml + '</div>' +
      '<table class="data-table analytics-table" style="margin-bottom:8px">' +
      '<thead><tr><th>Region</th><th>Messages</th><th>% of Scoped</th></tr></thead>' +
      '<tbody>' + tableBody + '</tbody></table>' +
      chartHtml;

    // Bind window selector
    el.querySelectorAll('[data-win]').forEach(function(btn) {
      btn.addEventListener('click', function() {
        selectedWindow = btn.dataset.win;
        if (typeof sessionStorage !== 'undefined') sessionStorage.setItem(window, selectedWindow);
        load(selectedWindow);
      });
    });
  }

  load(selectedWindow);
}
```

- [ ] **Step 4: Bump cache buster in `public/index.html`**

Find the line that loads `analytics.js` with a `?v=__BUST__` suffix and increment the bust value to match the other files changed in this PR. Follow the project convention for how `__BUST__` is managed (check the existing values in the file and the Makefile/build script if any).

- [ ] **Step 5: Manual smoke test**

Start the server pointing at a DB that has had the ingestor migration run:
```
cd cmd/server && go run . -db path/to/meshcore.db
```
Open the browser at `http://localhost:8080/#/analytics?tab=scopes`. Verify:
- The "Scopes" tab appears and is clickable
- Summary cards render with counts (may be zeros on a fresh DB)
- Window selector switches between 1h / 24h / 7d
- No JS errors in the browser console

- [ ] **Step 6: Commit**

```bash
git add public/analytics.js public/index.html
git commit -m "feat(frontend): add Scopes tab to Analytics page (#899)"
```

---

## Self-Review Notes

- **Spec coverage**: All items covered: Feature 1 (scoped/unscoped counts) ✅, Feature 2 (region matching via config) ✅, Feature 3 (excluded — firmware limitation) ✅ noted in spec
- **NULL semantics**: `scopeNameForDB` correctly encodes the three states (NULL / "" / "#name")
- **HMAC derivation**: `matchScope` mirrors firmware exactly — little-endian uint16, zero/FFFF adjustment, first 2 bytes of HMAC output
- **API spec doc**: Task 7 updates `docs/api-spec.md` as required by project convention
- **Cache buster**: Task 8 Step 4 bumps `analytics.js` bust value
- **Backfill goroutine**: Runs in background so ingestor startup is not blocked
- **Empty slices**: `GetScopeStats` always returns non-nil slices for `ByRegion` and `TimeSeries` to avoid `null` in JSON
