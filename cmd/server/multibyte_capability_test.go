package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// setupCapabilityTestDB creates a minimal in-memory DB with nodes table.
func setupCapabilityTestDB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	conn.Exec(`CREATE TABLE nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT,
		lat REAL, lon REAL, last_seen TEXT, first_seen TEXT,
		advert_count INTEGER DEFAULT 0, battery_mv INTEGER, temperature_c REAL
	)`)
	conn.Exec(`CREATE TABLE observers (
		id TEXT PRIMARY KEY, name TEXT, iata TEXT, last_seen TEXT,
		first_seen TEXT, packet_count INTEGER DEFAULT 0, model TEXT,
		firmware TEXT, client_version TEXT, radio TEXT, battery_mv INTEGER,
		uptime_secs INTEGER
	)`)
	return &DB{conn: conn}
}

// addTestPacket adds a StoreTx to the store's internal structures including
// the byPathHop index and byPayloadType index.
func addTestPacket(store *PacketStore, tx *StoreTx) {
	store.mu.Lock()
	defer store.mu.Unlock()
	tx.ID = len(store.packets) + 1
	if tx.Hash == "" {
		tx.Hash = fmt.Sprintf("test-hash-%d", tx.ID)
	}
	store.packets = append(store.packets, tx)
	store.byHash[tx.Hash] = tx
	store.byTxID[tx.ID] = tx
	if tx.PayloadType != nil {
		store.byPayloadType[*tx.PayloadType] = append(store.byPayloadType[*tx.PayloadType], tx)
	}
	addTxToPathHopIndex(store.byPathHop, tx)
}

// buildPathByte returns a 2-char hex string for the path byte with given
// hashSize (1-3) and hopCount.
func buildPathByte(hashSize, hopCount int) string {
	b := byte(((hashSize - 1) & 0x3) << 6) | byte(hopCount&0x3F)
	return fmt.Sprintf("%02x", b)
}

// makeTestAdvert creates a StoreTx representing a flood advert packet.
func makeTestAdvert(pubkey string, hashSize int) *StoreTx {
	decoded, _ := json.Marshal(map[string]interface{}{"pubKey": pubkey, "name": pubkey[:8]})
	pt := 4
	pathByte := buildPathByte(hashSize, 1)
	prefix := strings.ToLower(pubkey[:hashSize*2])
	rawHex := "01" + pathByte + prefix // flood header + path byte + hop prefix
	return &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		DecodedJSON: string(decoded),
		PathJSON:    `["` + prefix + `"]`,
		FirstSeen:   "2026-04-11T00:00:00.000Z",
	}
}

// TestMultiByteCapability_Confirmed tests that a repeater advertising
// with hash_size >= 2 is classified as "confirmed".
func TestMultiByteCapability_Confirmed(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepA", "repeater", "2026-04-11T00:00:00Z")

	store := NewPacketStore(db, nil)
	addTestPacket(store, makeTestAdvert("aabbccdd11223344", 2))

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "confirmed" {
		t.Errorf("expected confirmed, got %s", caps[0].Status)
	}
	if caps[0].Evidence != "advert" {
		t.Errorf("expected advert evidence, got %s", caps[0].Evidence)
	}
	if caps[0].MaxHashSize != 2 {
		t.Errorf("expected maxHashSize 2, got %d", caps[0].MaxHashSize)
	}
}

// TestMultiByteCapability_Suspected tests that a repeater whose prefix
// appears in a multi-byte path is classified as "suspected".
func TestMultiByteCapability_Suspected(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepB", "repeater", "2026-04-10T00:00:00Z")

	store := NewPacketStore(db, nil)

	// Non-advert packet with 2-byte hash in path, hop prefix matching node
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aabb"
	pt := 1
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aabb"]`,
		FirstSeen:   "2026-04-10T00:00:00.000Z",
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "suspected" {
		t.Errorf("expected suspected, got %s", caps[0].Status)
	}
	if caps[0].Evidence != "path" {
		t.Errorf("expected path evidence, got %s", caps[0].Evidence)
	}
	if caps[0].MaxHashSize != 2 {
		t.Errorf("expected maxHashSize 2, got %d", caps[0].MaxHashSize)
	}
}

// TestMultiByteCapability_Unknown tests that a repeater with only 1-byte
// adverts and no multi-byte path appearances is classified as "unknown".
func TestMultiByteCapability_Unknown(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepC", "repeater", "2026-04-08T00:00:00Z")

	store := NewPacketStore(db, nil)

	// Advert with 1-byte hash only
	addTestPacket(store, makeTestAdvert("aabbccdd11223344", 1))

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "unknown" {
		t.Errorf("expected unknown, got %s", caps[0].Status)
	}
	if caps[0].MaxHashSize != 1 {
		t.Errorf("expected maxHashSize 1, got %d", caps[0].MaxHashSize)
	}
}

// TestMultiByteCapability_PrefixCollision tests that when two repeaters
// share the same prefix, one confirmed via advert, the other gets
// suspected (not confirmed) from path data alone.
func TestMultiByteCapability_PrefixCollision(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	// Two repeaters sharing 1-byte prefix "aa"
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabb000000000001", "RepConfirmed", "repeater", "2026-04-11T00:00:00Z")
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aacc000000000002", "RepOther", "repeater", "2026-04-11T00:00:00Z")

	store := NewPacketStore(db, nil)

	// RepConfirmed has a 2-byte advert
	addTestPacket(store, makeTestAdvert("aabb000000000001", 2))

	// A packet with 2-byte path containing 1-byte hop "aa" — both share this prefix
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aa"
	pt := 1
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aa"]`,
		FirstSeen:   "2026-04-10T00:00:00.000Z",
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(caps))
	}

	capByName := map[string]MultiByteCapEntry{}
	for _, c := range caps {
		capByName[c.Name] = c
	}

	if capByName["RepConfirmed"].Status != "confirmed" {
		t.Errorf("RepConfirmed expected confirmed, got %s", capByName["RepConfirmed"].Status)
	}
	if capByName["RepOther"].Status != "suspected" {
		t.Errorf("RepOther expected suspected, got %s", capByName["RepOther"].Status)
	}
}

// TestMultiByteCapability_TraceExcluded tests that TRACE packets (payload_type 8)
// do NOT contribute to "suspected" multi-byte capability. TRACE packets carry
// hash size in their own flags, so pre-1.14 repeaters can forward multi-byte
// TRACEs without actually supporting multi-byte hashes. See #714.
func TestMultiByteCapability_TraceExcluded(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepTrace", "repeater", "2026-04-10T00:00:00Z")

	store := NewPacketStore(db, nil)

	// TRACE packet (payload_type 8) with 2-byte hash in path
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aabb"
	pt := 8
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aabb"]`,
		FirstSeen:   "2026-04-10T00:00:00.000Z",
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "unknown" {
		t.Errorf("expected unknown (TRACE excluded), got %s", caps[0].Status)
	}
}

// TestMultiByteCapability_NonTraceStillSuspected verifies that non-TRACE packets
// with 2-byte paths still correctly mark a repeater as "suspected".
func TestMultiByteCapability_NonTraceStillSuspected(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepNonTrace", "repeater", "2026-04-10T00:00:00Z")

	store := NewPacketStore(db, nil)

	// GRP_TXT packet (payload_type 1) with 2-byte hash in path
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aabb"
	pt := 1
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aabb"]`,
		FirstSeen:   "2026-04-10T00:00:00.000Z",
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "suspected" {
		t.Errorf("expected suspected, got %s", caps[0].Status)
	}
}

// TestMultiByteCapability_ConfirmedUnaffectedByTraceExclusion verifies that
// "confirmed" status from adverts is not affected by the TRACE exclusion.
func TestMultiByteCapability_ConfirmedUnaffectedByTraceExclusion(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepConfirmedTrace", "repeater", "2026-04-11T00:00:00Z")

	store := NewPacketStore(db, nil)

	// Advert with 2-byte hash (confirms capability)
	addTestPacket(store, makeTestAdvert("aabbccdd11223344", 2))

	// TRACE packet also present — should not downgrade confirmed status
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aabb"
	pt := 8
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aabb"]`,
		FirstSeen:   "2026-04-10T00:00:00.000Z",
	}
	addTestPacket(store, pkt)

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "confirmed" {
		t.Errorf("expected confirmed (unaffected by TRACE), got %s", caps[0].Status)
	}
}

// TestMultiByteCapability_CompanionConfirmed tests that a companion with
// multi-byte advert is classified as "confirmed", not "unknown" (Bug 1, #754).
func TestMultiByteCapability_CompanionConfirmed(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "CompA", "companion", "2026-04-11T00:00:00Z")

	store := NewPacketStore(db, nil)
	addTestPacket(store, makeTestAdvert("aabbccdd11223344", 2))

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(caps))
	}
	if caps[0].Status != "confirmed" {
		t.Errorf("expected confirmed for companion, got %s", caps[0].Status)
	}
	if caps[0].Role != "companion" {
		t.Errorf("expected role companion, got %s", caps[0].Role)
	}
	if caps[0].Evidence != "advert" {
		t.Errorf("expected advert evidence, got %s", caps[0].Evidence)
	}
}

// TestMultiByteCapability_RoleColumnPopulated tests that the Role field is
// populated for all node types (Bug 2, #754).
func TestMultiByteCapability_RoleColumnPopulated(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabb000000000001", "Rep1", "repeater", "2026-04-11T00:00:00Z")
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"ccdd000000000002", "Comp1", "companion", "2026-04-11T00:00:00Z")
	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"eeff000000000003", "Room1", "room_server", "2026-04-11T00:00:00Z")

	store := NewPacketStore(db, nil)
	addTestPacket(store, makeTestAdvert("aabb000000000001", 2))
	addTestPacket(store, makeTestAdvert("ccdd000000000002", 2))
	addTestPacket(store, makeTestAdvert("eeff000000000003", 1))

	caps := store.computeMultiByteCapability(nil)
	if len(caps) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(caps))
	}

	roleByName := map[string]string{}
	for _, c := range caps {
		roleByName[c.Name] = c.Role
	}
	if roleByName["Rep1"] != "repeater" {
		t.Errorf("Rep1 role: expected repeater, got %s", roleByName["Rep1"])
	}
	if roleByName["Comp1"] != "companion" {
		t.Errorf("Comp1 role: expected companion, got %s", roleByName["Comp1"])
	}
	if roleByName["Room1"] != "room_server" {
		t.Errorf("Room1 role: expected room_server, got %s", roleByName["Room1"])
	}
}

// TestMultiByteCapability_AdopterEvidenceTakesPrecedence tests that when
// adopter data shows hashSize >= 2 but path evidence says "suspected",
// the node is upgraded to "confirmed" (Bug 3, #754).
func TestMultiByteCapability_AdopterEvidenceTakesPrecedence(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"aabbccdd11223344", "RepAdopter", "repeater", "2026-04-11T00:00:00Z")

	store := NewPacketStore(db, nil)

	// Only a path-based packet (no advert) — would normally be "suspected"
	pathByte := buildPathByte(2, 1)
	rawHex := "01" + pathByte + "aabb"
	pt := 1
	pkt := &StoreTx{
		RawHex:      rawHex,
		PayloadType: &pt,
		PathJSON:    `["aabb"]`,
		FirstSeen:   "2026-04-10T00:00:00.000Z",
	}
	addTestPacket(store, pkt)

	// Without adopter data: should be suspected
	caps := store.computeMultiByteCapability(nil)
	capByName := map[string]MultiByteCapEntry{}
	for _, c := range caps {
		capByName[c.Name] = c
	}
	if capByName["RepAdopter"].Status != "suspected" {
		t.Errorf("without adopter data: expected suspected, got %s", capByName["RepAdopter"].Status)
	}

	// With adopter data showing hashSize 2: should be confirmed
	adopterHS := map[string]int{"aabbccdd11223344": 2}
	caps = store.computeMultiByteCapability(adopterHS)
	capByName = map[string]MultiByteCapEntry{}
	for _, c := range caps {
		capByName[c.Name] = c
	}
	if capByName["RepAdopter"].Status != "confirmed" {
		t.Errorf("with adopter data: expected confirmed, got %s", capByName["RepAdopter"].Status)
	}
	if capByName["RepAdopter"].Evidence != "advert" {
		t.Errorf("with adopter data: expected advert evidence, got %s", capByName["RepAdopter"].Evidence)
	}
}
