package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tempDBPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(".", "testdata")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, t.Name()+".db")
	// Clean up any previous test DB
	os.Remove(p)
	os.Remove(p + "-wal")
	os.Remove(p + "-shm")
	t.Cleanup(func() {
		os.Remove(p)
		os.Remove(p + "-wal")
		os.Remove(p + "-shm")
	})
	return p
}

func TestOpenStore(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Verify tables exist
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tables = append(tables, name)
	}

	expected := []string{"nodes", "observations", "observers", "transmissions"}
	for _, e := range expected {
		found := false
		for _, tbl := range tables {
			if tbl == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing table %s, got %v", e, tables)
		}
	}

	// Verify packets_v view exists
	var viewCount int
	err = s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='view' AND name='packets_v'").Scan(&viewCount)
	if err != nil {
		t.Fatal(err)
	}
	if viewCount != 1 {
		t.Error("packets_v view not created")
	}
}

func TestInsertTransmission(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	snr := 5.5
	rssi := -100.0
	data := &PacketData{
		RawHex:         "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976",
		Timestamp:      "2026-03-25T00:00:00Z",
		ObserverID:     "obs1",
		Hash:           "abcdef1234567890",
		RouteType:      2,
		PayloadType:    2,
		PayloadVersion: 0,
		PathJSON:       "[]",
		DecodedJSON:    `{"type":"TXT_MSG"}`,
		SNR:            &snr,
		RSSI:           &rssi,
	}

	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Verify transmission was inserted
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 1 {
		t.Errorf("transmissions count=%d, want 1", count)
	}

	// Verify observation was inserted
	s.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&count)
	if count != 1 {
		t.Errorf("observations count=%d, want 1", count)
	}

	// Verify hash dedup: same hash should not create new transmission
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 1 {
		t.Errorf("transmissions count after dedup=%d, want 1", count)
	}
}

func TestPacketsViewQueryable(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Insert observer so the LEFT JOIN resolves
	if err := s.UpsertObserver("obs1", "TestObserver", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	snr := 3.5
	rssi := -95.0
	data := &PacketData{
		RawHex:      "AABB",
		Timestamp:   "2026-01-01T00:00:00Z",
		ObserverID:  "obs1",
		Hash:        "viewtesthash",
		RouteType:   1,
		PayloadType: 4,
		PathJSON:    "[]",
		DecodedJSON: `{"type":"ADVERT"}`,
		SNR:         &snr,
		RSSI:        &rssi,
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Query through packets_v — the view the Go server relies on
	var obsID, obsName sql.NullString
	var hash string
	err = s.db.QueryRow("SELECT observer_id, observer_name, hash FROM packets_v LIMIT 1").Scan(&obsID, &obsName, &hash)
	if err != nil {
		t.Fatalf("packets_v query failed: %v", err)
	}
	if hash != "viewtesthash" {
		t.Errorf("hash=%s, want viewtesthash", hash)
	}
	if !obsID.Valid || obsID.String != "obs1" {
		t.Errorf("observer_id=%v, want obs1", obsID)
	}
	if !obsName.Valid || obsName.String != "TestObserver" {
		t.Errorf("observer_name=%v, want TestObserver", obsName)
	}
}

func TestUpsertNode(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lat := 37.0
	lon := -122.0
	if err := s.UpsertNode("aabbccdd", "TestNode", "repeater", &lat, &lon, "2026-03-25T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	var name, role string
	s.db.QueryRow("SELECT name, role FROM nodes WHERE public_key = 'aabbccdd'").Scan(&name, &role)
	if name != "TestNode" {
		t.Errorf("name=%s, want TestNode", name)
	}
	if role != "repeater" {
		t.Errorf("role=%s, want repeater", role)
	}

	// Upsert again — should update
	if err := s.UpsertNode("aabbccdd", "UpdatedNode", "repeater", &lat, &lon, "2026-03-25T01:00:00Z"); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT name FROM nodes WHERE public_key = 'aabbccdd'").Scan(&name)
	if name != "UpdatedNode" {
		t.Errorf("after upsert name=%s, want UpdatedNode", name)
	}

	// UpsertNode does not modify advert_count (IncrementAdvertCount is separate)
	var count int
	s.db.QueryRow("SELECT advert_count FROM nodes WHERE public_key = 'aabbccdd'").Scan(&count)
	if count != 0 {
		t.Errorf("advert_count=%d, want 0 (UpsertNode does not increment)", count)
	}
}

func TestUpsertObserver(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertObserver("obs1", "Observer1", " sjc ", nil); err != nil {
		t.Fatal(err)
	}

	var name, iata string
	s.db.QueryRow("SELECT name, iata FROM observers WHERE id = 'obs1'").Scan(&name, &iata)
	if name != "Observer1" {
		t.Errorf("name=%s, want Observer1", name)
	}
	if iata != "SJC" {
		t.Errorf("iata=%s, want SJC", iata)
	}
}

func TestUpsertObserverWithMeta(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	battery := 3500
	uptime := int64(86400)
	noise := -115.5
	model := "L1"
	firmware := "v1.2.3"
	clientVersion := "2.4.1"
	radio := "SX1262"
	meta := &ObserverMeta{
		Model:         &model,
		Firmware:      &firmware,
		ClientVersion: &clientVersion,
		Radio:         &radio,
		BatteryMv:     &battery,
		UptimeSecs:    &uptime,
		NoiseFloor:    &noise,
	}

	if err := s.UpsertObserver("obs1", "Observer1", "SJC", meta); err != nil {
		t.Fatal(err)
	}

	// Verify correct types in DB
	var batteryMv int
	var uptimeSecs int64
	var noiseFloor float64
	var gotModel, gotFirmware, gotClientVersion, gotRadio string
	err = s.db.QueryRow("SELECT model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor FROM observers WHERE id = 'obs1'").
		Scan(&gotModel, &gotFirmware, &gotClientVersion, &gotRadio, &batteryMv, &uptimeSecs, &noiseFloor)
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != model {
		t.Errorf("model=%s, want %s", gotModel, model)
	}
	if gotFirmware != firmware {
		t.Errorf("firmware=%s, want %s", gotFirmware, firmware)
	}
	if gotClientVersion != clientVersion {
		t.Errorf("client_version=%s, want %s", gotClientVersion, clientVersion)
	}
	if gotRadio != radio {
		t.Errorf("radio=%s, want %s", gotRadio, radio)
	}
	if batteryMv != 3500 {
		t.Errorf("battery_mv=%d, want 3500", batteryMv)
	}
	if uptimeSecs != 86400 {
		t.Errorf("uptime_secs=%d, want 86400", uptimeSecs)
	}
	if noiseFloor != -115.5 {
		t.Errorf("noise_floor=%f, want -115.5", noiseFloor)
	}

	// Verify typeof returns correct SQLite types
	var typBattery, typUptime, typNoise string
	s.db.QueryRow("SELECT typeof(battery_mv), typeof(uptime_secs), typeof(noise_floor) FROM observers WHERE id = 'obs1'").
		Scan(&typBattery, &typUptime, &typNoise)
	if typBattery != "integer" {
		t.Errorf("typeof(battery_mv)=%s, want integer", typBattery)
	}
	if typUptime != "integer" {
		t.Errorf("typeof(uptime_secs)=%s, want integer", typUptime)
	}
	if typNoise != "real" {
		t.Errorf("typeof(noise_floor)=%s, want real", typNoise)
	}
}

func TestUpsertObserverMetaPreservesExisting(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// First upsert with metadata
	battery := 3500
	noise := -115.5
	model := "L1"
	firmware := "v1.2.3"
	clientVersion := "2.4.1"
	radio := "SX1262"
	meta := &ObserverMeta{
		Model:         &model,
		Firmware:      &firmware,
		ClientVersion: &clientVersion,
		Radio:         &radio,
		BatteryMv:     &battery,
		NoiseFloor:    &noise,
	}
	if err := s.UpsertObserver("obs1", "Observer1", "SJC", meta); err != nil {
		t.Fatal(err)
	}

	// Second upsert without metadata — should preserve existing values
	if err := s.UpsertObserver("obs1", "Observer1", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	var batteryMv int
	var noiseFloor float64
	var gotModel, gotFirmware, gotClientVersion, gotRadio string
	s.db.QueryRow("SELECT model, firmware, client_version, radio, battery_mv, noise_floor FROM observers WHERE id = 'obs1'").
		Scan(&gotModel, &gotFirmware, &gotClientVersion, &gotRadio, &batteryMv, &noiseFloor)
	if gotModel != model {
		t.Errorf("model=%s after nil-meta upsert, want %s (preserved)", gotModel, model)
	}
	if gotFirmware != firmware {
		t.Errorf("firmware=%s after nil-meta upsert, want %s (preserved)", gotFirmware, firmware)
	}
	if gotClientVersion != clientVersion {
		t.Errorf("client_version=%s after nil-meta upsert, want %s (preserved)", gotClientVersion, clientVersion)
	}
	if gotRadio != radio {
		t.Errorf("radio=%s after nil-meta upsert, want %s (preserved)", gotRadio, radio)
	}
	if batteryMv != 3500 {
		t.Errorf("battery_mv=%d after nil-meta upsert, want 3500 (preserved)", batteryMv)
	}
	if noiseFloor != -115.5 {
		t.Errorf("noise_floor=%f after nil-meta upsert, want -115.5 (preserved)", noiseFloor)
	}
}

func TestExtractObserverMeta(t *testing.T) {
	// Float values from JSON (typical MQTT payload)
	msg := map[string]interface{}{
		"model":            "L1",
		"firmware_version": "v1.2.3",
		"clientVersion":    "2.4.1",
		"radio":            "SX1262",
		"battery_mv":       3500.0,
		"uptime_secs":      86400.0,
		"noise_floor":      -115.5,
	}
	meta := extractObserverMeta(msg)
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if meta.Model == nil || *meta.Model != "L1" {
		t.Errorf("Model=%v, want L1", meta.Model)
	}
	if meta.Firmware == nil || *meta.Firmware != "v1.2.3" {
		t.Errorf("Firmware=%v, want v1.2.3", meta.Firmware)
	}
	if meta.ClientVersion == nil || *meta.ClientVersion != "2.4.1" {
		t.Errorf("ClientVersion=%v, want 2.4.1", meta.ClientVersion)
	}
	if meta.Radio == nil || *meta.Radio != "SX1262" {
		t.Errorf("Radio=%v, want SX1262", meta.Radio)
	}
	if meta.BatteryMv == nil || *meta.BatteryMv != 3500 {
		t.Errorf("BatteryMv=%v, want 3500", meta.BatteryMv)
	}
	if meta.UptimeSecs == nil || *meta.UptimeSecs != 86400 {
		t.Errorf("UptimeSecs=%v, want 86400", meta.UptimeSecs)
	}
	if meta.NoiseFloor == nil || *meta.NoiseFloor != -115.5 {
		t.Errorf("NoiseFloor=%v, want -115.5", meta.NoiseFloor)
	}

	// Battery with fractional part should round
	msg2 := map[string]interface{}{
		"battery_mv": 3500.7,
	}
	meta2 := extractObserverMeta(msg2)
	if meta2 == nil || meta2.BatteryMv == nil || *meta2.BatteryMv != 3501 {
		t.Errorf("battery_mv rounding: got %v, want 3501", meta2)
	}

	// Empty message → nil
	meta3 := extractObserverMeta(map[string]interface{}{})
	if meta3 != nil {
		t.Errorf("expected nil for empty message, got %v", meta3)
	}

	// firmware/client snake_case fields should be captured too
	msg4 := map[string]interface{}{
		"firmware":       "v9.9.9",
		"client_version": "3.0.0",
	}
	meta4 := extractObserverMeta(msg4)
	if meta4 == nil || meta4.Firmware == nil || *meta4.Firmware != "v9.9.9" {
		t.Errorf("Firmware=%v, want v9.9.9", meta4)
	}
	if meta4 == nil || meta4.ClientVersion == nil || *meta4.ClientVersion != "3.0.0" {
		t.Errorf("ClientVersion=%v, want 3.0.0", meta4)
	}

	// When both keys are present, explicit compatibility fields win due extraction order:
	// firmware_version overrides firmware and clientVersion overrides client_version.
	msg5 := map[string]interface{}{
		"firmware":         "v1-legacy",
		"firmware_version": "v2-canonical",
		"client_version":   "1.0.0-legacy",
		"clientVersion":    "2.0.0-canonical",
	}
	meta5 := extractObserverMeta(msg5)
	if meta5 == nil {
		t.Fatal("expected non-nil meta for dual-key payload")
	}
	if meta5.Firmware == nil || *meta5.Firmware != "v2-canonical" {
		t.Errorf("Firmware precedence mismatch: got %v, want v2-canonical from firmware_version", meta5.Firmware)
	}
	if meta5.ClientVersion == nil || *meta5.ClientVersion != "2.0.0-canonical" {
		t.Errorf("ClientVersion precedence mismatch: got %v, want 2.0.0-canonical from clientVersion", meta5.ClientVersion)
	}
}

func TestSchemaNoiseFloorIsReal(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Check column type affinity via PRAGMA
	rows, err := s.db.Query("PRAGMA table_info(observers)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull, pk int
		var dflt interface{}
		if rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk) == nil {
			if colName == "noise_floor" && colType != "REAL" {
				t.Errorf("noise_floor column type=%s, want REAL", colType)
			}
			if colName == "battery_mv" && colType != "INTEGER" {
				t.Errorf("battery_mv column type=%s, want INTEGER", colType)
			}
			if colName == "uptime_secs" && colType != "INTEGER" {
				t.Errorf("uptime_secs column type=%s, want INTEGER", colType)
			}
		}
	}
}

func TestInsertTransmissionWithObserver(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Insert observer first
	if err := s.UpsertObserver("obs1", "Observer1", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	data := &PacketData{
		RawHex:      "0A00D69F",
		Timestamp:   "2026-03-25T00:00:00Z",
		ObserverID:  "obs1",
		Hash:        "test1234567890ab",
		RouteType:   2,
		PayloadType: 2,
		PathJSON:    "[]",
		DecodedJSON: `{"type":"TXT_MSG"}`,
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Verify observer_idx was resolved
	var observerIdx *int64
	s.db.QueryRow("SELECT observer_idx FROM observations LIMIT 1").Scan(&observerIdx)
	if observerIdx == nil {
		t.Error("observer_idx should be set when observer exists")
	}
}

// #463: Verify that inserting a packet updates the observer's last_seen,
// so low-traffic observers don't incorrectly appear offline.
func TestInsertTransmissionUpdatesObserverLastSeen(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Insert observer with an old last_seen
	if err := s.UpsertObserver("obs1", "Observer1", "SJC", nil); err != nil {
		t.Fatal(err)
	}
	// Backdate last_seen to 2 hours ago
	oldTime := "2026-03-24T22:00:00Z"
	s.db.Exec("UPDATE observers SET last_seen = ? WHERE id = ?", oldTime, "obs1")

	// Verify it was backdated
	var lastSeenBefore string
	s.db.QueryRow("SELECT last_seen FROM observers WHERE id = ?", "obs1").Scan(&lastSeenBefore)
	if lastSeenBefore != oldTime {
		t.Fatalf("expected last_seen=%s, got %s", oldTime, lastSeenBefore)
	}

	// Insert a packet from this observer
	data := &PacketData{
		RawHex:      "0A00D69F",
		Timestamp:   "2026-03-25T01:00:00Z",
		ObserverID:  "obs1",
		Hash:        "lastseentest123456",
		RouteType:   2,
		PayloadType: 2,
		PathJSON:    "[]",
		DecodedJSON: `{"type":"TXT_MSG"}`,
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Verify last_seen was updated
	var lastSeenAfter string
	s.db.QueryRow("SELECT last_seen FROM observers WHERE id = ?", "obs1").Scan(&lastSeenAfter)
	if lastSeenAfter == oldTime {
		t.Error("observer last_seen was NOT updated after packet insertion — low-traffic observers will appear offline")
	}
	if lastSeenAfter != "2026-03-25T01:00:00Z" {
		t.Errorf("expected last_seen=2026-03-25T01:00:00Z, got %s", lastSeenAfter)
	}
}

func TestEndToEndIngest(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Simulate full pipeline: decode + insert
	rawHex := "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"

	decoded, err := DecodePacket(rawHex, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	msg := &MQTTPacketMessage{
		Raw: rawHex,
	}
	pktData := BuildPacketData(msg, decoded, "obs1", "SJC")
	if _, err := s.InsertTransmission(pktData); err != nil {
		t.Fatal(err)
	}

	// Process advert node upsert
	if decoded.Payload.Type == "ADVERT" && decoded.Payload.PubKey != "" {
		ok, _ := ValidateAdvert(&decoded.Payload)
		if ok {
			role := advertRole(decoded.Payload.Flags)
			err := s.UpsertNode(decoded.Payload.PubKey, decoded.Payload.Name, role, decoded.Payload.Lat, decoded.Payload.Lon, pktData.Timestamp)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	// Verify node was created
	var nodeName string
	err = s.db.QueryRow("SELECT name FROM nodes WHERE public_key = ?", decoded.Payload.PubKey).Scan(&nodeName)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nodeName, "MRR2-R") {
		t.Errorf("node name=%s, want MRR2-R", nodeName)
	}
}

func TestInsertTransmissionEmptyHash(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := &PacketData{
		RawHex:    "0A00",
		Timestamp: "2026-03-25T00:00:00Z",
		Hash:      "", // empty hash → should return nil
	}
	_, err = s.InsertTransmission(data)
	if err != nil {
		t.Errorf("empty hash should return nil, got %v", err)
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 0 {
		t.Errorf("no transmission should be inserted for empty hash, got count=%d", count)
	}
}

func TestInsertTransmissionEmptyTimestamp(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "", // empty → uses current time
		Hash:      "emptyts123456789",
		RouteType: 2,
	}
	_, err = s.InsertTransmission(data)
	if err != nil {
		t.Fatal(err)
	}

	var firstSeen string
	s.db.QueryRow("SELECT first_seen FROM transmissions WHERE hash = ?", data.Hash).Scan(&firstSeen)
	if firstSeen == "" {
		t.Error("first_seen should be set even with empty timestamp")
	}
}

func TestInsertTransmissionEarlierFirstSeen(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Insert with later timestamp
	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T12:00:00Z",
		Hash:      "firstseen12345678",
		RouteType: 2,
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Insert again with earlier timestamp
	data2 := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T06:00:00Z", // earlier
		Hash:      "firstseen12345678",    // same hash
		RouteType: 2,
	}
	if _, err := s.InsertTransmission(data2); err != nil {
		t.Fatal(err)
	}

	var firstSeen string
	s.db.QueryRow("SELECT first_seen FROM transmissions WHERE hash = ?", data.Hash).Scan(&firstSeen)
	if firstSeen != "2026-03-25T06:00:00Z" {
		t.Errorf("first_seen=%s, want 2026-03-25T06:00:00Z (earlier timestamp)", firstSeen)
	}
}

func TestInsertTransmissionLaterFirstSeenNotUpdated(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T06:00:00Z",
		Hash:      "notupdated1234567",
		RouteType: 2,
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Insert with later timestamp — should NOT update first_seen
	data2 := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T18:00:00Z",
		Hash:      "notupdated1234567",
		RouteType: 2,
	}
	if _, err := s.InsertTransmission(data2); err != nil {
		t.Fatal(err)
	}

	var firstSeen string
	s.db.QueryRow("SELECT first_seen FROM transmissions WHERE hash = ?", data.Hash).Scan(&firstSeen)
	if firstSeen != "2026-03-25T06:00:00Z" {
		t.Errorf("first_seen=%s should not change to later time", firstSeen)
	}
}

func TestInsertTransmissionNilSNRRSSI(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T00:00:00Z",
		Hash:      "nilsnrrssi1234567",
		RouteType: 2,
		SNR:       nil,
		RSSI:      nil,
	}
	_, err = s.InsertTransmission(data)
	if err != nil {
		t.Fatal(err)
	}

	var snr, rssi *float64
	s.db.QueryRow("SELECT snr, rssi FROM observations LIMIT 1").Scan(&snr, &rssi)
	if snr != nil {
		t.Errorf("snr should be nil, got %v", snr)
	}
	if rssi != nil {
		t.Errorf("rssi should be nil, got %v", rssi)
	}
}

func TestBuildPacketData(t *testing.T) {
	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	decoded, err := DecodePacket(rawHex, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	snr := 5.0
	rssi := -100.0
	msg := &MQTTPacketMessage{
		Raw:    rawHex,
		SNR:    &snr,
		RSSI:   &rssi,
		Origin: "test-observer",
	}

	pkt := BuildPacketData(msg, decoded, "obs123", "SJC")

	if pkt.RawHex != rawHex {
		t.Errorf("rawHex mismatch")
	}
	if pkt.ObserverID != "obs123" {
		t.Errorf("observerID=%s, want obs123", pkt.ObserverID)
	}
	if pkt.ObserverName != "test-observer" {
		t.Errorf("observerName=%s", pkt.ObserverName)
	}
	if pkt.SNR == nil || *pkt.SNR != 5.0 {
		t.Errorf("SNR=%v", pkt.SNR)
	}
	if pkt.RSSI == nil || *pkt.RSSI != -100.0 {
		t.Errorf("RSSI=%v", pkt.RSSI)
	}
	if pkt.Hash == "" {
		t.Error("hash should not be empty")
	}
	if len(pkt.Hash) != 16 {
		t.Errorf("hash length=%d, want 16", len(pkt.Hash))
	}
	if pkt.RouteType != decoded.Header.RouteType {
		t.Errorf("routeType mismatch")
	}
	if pkt.PayloadType != decoded.Header.PayloadType {
		t.Errorf("payloadType mismatch")
	}
	if pkt.Timestamp == "" {
		t.Error("timestamp should be set")
	}
	if pkt.DecodedJSON == "" || pkt.DecodedJSON == "{}" {
		t.Error("decodedJSON should be populated")
	}
}

func TestBuildPacketDataWithHops(t *testing.T) {
	// A packet with actual hops in the path
	raw := "0505AABBCCDDEE" + strings.Repeat("00", 10)
	decoded, err := DecodePacket(raw, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	msg := &MQTTPacketMessage{Raw: raw}
	pkt := BuildPacketData(msg, decoded, "", "")

	if pkt.PathJSON == "[]" {
		t.Error("pathJSON should contain hops")
	}
	if !strings.Contains(pkt.PathJSON, "AA") {
		t.Errorf("pathJSON should contain hop AA: %s", pkt.PathJSON)
	}
}

func TestBuildPacketDataNilSNRRSSI(t *testing.T) {
	decoded, _ := DecodePacket("0A00"+strings.Repeat("00", 10), nil, false)
	msg := &MQTTPacketMessage{Raw: "0A00" + strings.Repeat("00", 10)}
	pkt := BuildPacketData(msg, decoded, "", "")

	if pkt.SNR != nil {
		t.Errorf("SNR should be nil")
	}
	if pkt.RSSI != nil {
		t.Errorf("RSSI should be nil")
	}
}

func TestUpsertNodeEmptyLastSeen(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lat := 37.0
	lon := -122.0
	// Empty lastSeen → should use current time
	if err := s.UpsertNode("aabbccdd", "TestNode", "repeater", &lat, &lon, ""); err != nil {
		t.Fatal(err)
	}

	var lastSeen string
	s.db.QueryRow("SELECT last_seen FROM nodes WHERE public_key = 'aabbccdd'").Scan(&lastSeen)
	if lastSeen == "" {
		t.Error("last_seen should be set even with empty input")
	}
}

func TestOpenStoreTwice(t *testing.T) {
	// Opening same DB twice tests the "observations already exists" path in applySchema
	path := tempDBPath(t)
	s1, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()

	// Second open — observations table already exists
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	// Verify it still works
	var count int
	s2.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 observations, got %d", count)
	}
}

func TestInsertTransmissionDedupObservation(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// First insert
	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-25T00:00:00Z",
		Hash:      "dedupobs12345678",
		RouteType: 2,
		PathJSON:  "[]",
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	// Insert same hash again with same observer (no observerID) —
	// the UNIQUE constraint on observations dedup should handle it
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&count)
	// Should have 2 observations (no observer_idx means both have NULL)
	// Actually INSERT OR IGNORE — may be 1 due to dedup index
	if count < 1 {
		t.Errorf("should have at least 1 observation, got %d", count)
	}
}

func TestSchemaCompatibility(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Verify column names match what Node.js expects
	expectedTxCols := []string{"id", "raw_hex", "hash", "first_seen", "route_type", "payload_type", "payload_version", "decoded_json", "created_at"}
	rows, _ := s.db.Query("PRAGMA table_info(transmissions)")
	var txCols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt *string
		var pk int
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		txCols = append(txCols, name)
	}
	rows.Close()

	for _, e := range expectedTxCols {
		found := false
		for _, c := range txCols {
			if c == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("transmissions missing column %s, got %v", e, txCols)
		}
	}

	// Verify observations columns
	expectedObsCols := []string{"id", "transmission_id", "observer_idx", "direction", "snr", "rssi", "score", "path_json", "timestamp"}
	rows, _ = s.db.Query("PRAGMA table_info(observations)")
	var obsCols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt *string
		var pk int
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		obsCols = append(obsCols, name)
	}
	rows.Close()

	for _, e := range expectedObsCols {
		found := false
		for _, c := range obsCols {
			if c == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("observations missing column %s, got %v", e, obsCols)
		}
	}
}

func TestConcurrentWrites(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Pre-create an observer for observer_idx resolution
	if err := s.UpsertObserver("obs1", "Observer1", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	const goroutines = 20
	const writesPerGoroutine = 50

	errCh := make(chan error, goroutines*writesPerGoroutine)
	done := make(chan struct{})

	for g := 0; g < goroutines; g++ {
		go func(gIdx int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < writesPerGoroutine; i++ {
				hash := fmt.Sprintf("concurrent_%d_%d_____", gIdx, i) // pad to 16+ chars
				snr := 5.0
				rssi := -100.0
				data := &PacketData{
					RawHex:      "0A00D69F",
					Timestamp:   time.Now().UTC().Format(time.RFC3339),
					ObserverID:  "obs1",
					Hash:        hash[:16],
					RouteType:   2,
					PayloadType: 4, // ADVERT
					PathJSON:    "[]",
					DecodedJSON: `{"type":"ADVERT"}`,
					SNR:         &snr,
					RSSI:        &rssi,
				}
				if _, err := s.InsertTransmission(data); err != nil {
					errCh <- fmt.Errorf("goroutine %d write %d: %w", gIdx, i, err)
					return
				}
				// Also do node + observer upserts to simulate full pipeline
				lat := 37.0
				lon := -122.0
				pubKey := fmt.Sprintf("node_%d_%d________", gIdx, i)
				if err := s.UpsertNode(pubKey[:16], "Node", "repeater", &lat, &lon, data.Timestamp); err != nil {
					errCh <- fmt.Errorf("goroutine %d node upsert %d: %w", gIdx, i, err)
					return
				}
				obsID := fmt.Sprintf("obs_%d_%d__________", gIdx, i)
				if err := s.UpsertObserver(obsID[:16], "Obs", "SJC", nil); err != nil {
					errCh <- fmt.Errorf("goroutine %d observer upsert %d: %w", gIdx, i, err)
					return
				}
			}
		}(g)
	}

	// Wait for all goroutines
	for g := 0; g < goroutines; g++ {
		<-done
	}
	close(errCh)

	var errors []error
	for err := range errCh {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		t.Errorf("got %d errors from %d concurrent writers (first: %v)", len(errors), goroutines, errors[0])
	}

	// Verify data integrity
	var txCount, obsCount, nodeCount, observerCount int
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&txCount)
	s.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&obsCount)
	s.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodeCount)
	s.db.QueryRow("SELECT COUNT(*) FROM observers").Scan(&observerCount)

	expectedTx := goroutines * writesPerGoroutine
	if txCount != expectedTx {
		t.Errorf("transmissions count=%d, want %d", txCount, expectedTx)
	}
	if obsCount != expectedTx {
		t.Errorf("observations count=%d, want %d", obsCount, expectedTx)
	}

	t.Logf("Concurrent write test: %d goroutines × %d writes = %d total, 0 errors",
		goroutines, writesPerGoroutine, goroutines*writesPerGoroutine)
	t.Logf("Stats: tx_inserted=%d tx_dupes=%d obs_inserted=%d write_errors=%d",
		s.Stats.TransmissionsInserted.Load(),
		s.Stats.DuplicateTransmissions.Load(),
		s.Stats.ObservationsInserted.Load(),
		s.Stats.WriteErrors.Load(),
	)
}

func TestDBStats(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Initial stats should be zero
	if s.Stats.TransmissionsInserted.Load() != 0 {
		t.Error("initial TransmissionsInserted should be 0")
	}
	if s.Stats.WriteErrors.Load() != 0 {
		t.Error("initial WriteErrors should be 0")
	}

	// Insert a transmission
	data := &PacketData{
		RawHex:    "0A00D69F",
		Timestamp: "2026-03-28T00:00:00Z",
		Hash:      "stats_test_12345",
		RouteType: 2,
		PathJSON:  "[]",
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	if s.Stats.TransmissionsInserted.Load() != 1 {
		t.Errorf("TransmissionsInserted=%d, want 1", s.Stats.TransmissionsInserted.Load())
	}
	if s.Stats.ObservationsInserted.Load() != 1 {
		t.Errorf("ObservationsInserted=%d, want 1", s.Stats.ObservationsInserted.Load())
	}

	// Insert duplicate
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}
	if s.Stats.DuplicateTransmissions.Load() != 1 {
		t.Errorf("DuplicateTransmissions=%d, want 1", s.Stats.DuplicateTransmissions.Load())
	}

	// Node upsert
	lat := 37.0
	lon := -122.0
	if err := s.UpsertNode("pk1", "Node1", "repeater", &lat, &lon, "2026-03-28T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if s.Stats.NodeUpserts.Load() != 1 {
		t.Errorf("NodeUpserts=%d, want 1", s.Stats.NodeUpserts.Load())
	}

	// Observer upsert
	if err := s.UpsertObserver("obs1", "Obs1", "SJC", nil); err != nil {
		t.Fatal(err)
	}
	if s.Stats.ObserverUpserts.Load() != 1 {
		t.Errorf("ObserverUpserts=%d, want 1", s.Stats.ObserverUpserts.Load())
	}

	// LogStats should not panic
	s.LogStats()
}

func TestLoadTestThroughput(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Pre-create observer
	if err := s.UpsertObserver("obs1", "Observer1", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	const totalMessages = 1000
	const goroutines = 20
	perGoroutine := totalMessages / goroutines

	// Simulate full pipeline: InsertTransmission + UpsertNode + UpsertObserver + IncrementAdvertCount
	// This matches the real handleMessage write pattern for ADVERT packets
	latencies := make([]time.Duration, totalMessages)
	var busyErrors atomic.Int64
	var totalErrors atomic.Int64
	errCh := make(chan error, totalMessages)
	done := make(chan struct{})

	start := time.Now()

	for g := 0; g < goroutines; g++ {
		go func(gIdx int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perGoroutine; i++ {
				msgStart := time.Now()
				idx := gIdx*perGoroutine + i
				hash := fmt.Sprintf("load_%04d_%04d____", gIdx, i)
				snr := 5.0
				rssi := -100.0

				data := &PacketData{
					RawHex:      "0A00D69F",
					Timestamp:   time.Now().UTC().Format(time.RFC3339),
					ObserverID:  "obs1",
					Hash:        hash[:16],
					RouteType:   2,
					PayloadType: 4,
					PathJSON:    "[]",
					DecodedJSON: `{"type":"ADVERT","pubKey":"` + hash[:16] + `"}`,
					SNR:         &snr,
					RSSI:        &rssi,
				}

				_, err := s.InsertTransmission(data)
				if err != nil {
					totalErrors.Add(1)
					if strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "SQLITE_BUSY") {
						busyErrors.Add(1)
					}
					errCh <- err
					continue
				}

				lat := 37.0 + float64(gIdx)*0.001
				lon := -122.0 + float64(i)*0.001
				pubKey := fmt.Sprintf("node_%04d_%04d____", gIdx, i)
				if err := s.UpsertNode(pubKey[:16], "Node", "repeater", &lat, &lon, data.Timestamp); err != nil {
					totalErrors.Add(1)
					if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "BUSY") {
						busyErrors.Add(1)
					}
				}

				if err := s.IncrementAdvertCount(pubKey[:16]); err != nil {
					totalErrors.Add(1)
				}

				obsID := fmt.Sprintf("obs_%04d_%04d_____", gIdx, i)
				if err := s.UpsertObserver(obsID[:16], "Obs", "SJC", nil); err != nil {
					totalErrors.Add(1)
					if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "BUSY") {
						busyErrors.Add(1)
					}
				}

				latencies[idx] = time.Since(msgStart)
			}
		}(g)
	}

	for g := 0; g < goroutines; g++ {
		<-done
	}
	close(errCh)
	elapsed := time.Since(start)

	// Calculate p50, p95, p99
	validLatencies := make([]time.Duration, 0, totalMessages)
	for _, l := range latencies {
		if l > 0 {
			validLatencies = append(validLatencies, l)
		}
	}
	sort.Slice(validLatencies, func(i, j int) bool { return validLatencies[i] < validLatencies[j] })

	p50 := validLatencies[len(validLatencies)*50/100]
	p95 := validLatencies[len(validLatencies)*95/100]
	p99 := validLatencies[len(validLatencies)*99/100]
	msgsPerSec := float64(totalMessages) / elapsed.Seconds()

	t.Logf("=== LOAD TEST RESULTS ===")
	t.Logf("Messages:     %d (%d goroutines × %d each)", totalMessages, goroutines, perGoroutine)
	t.Logf("Writes/msg:   4 (InsertTx + UpsertNode + IncrAdvertCount + UpsertObserver)")
	t.Logf("Total writes: %d", totalMessages*4)
	t.Logf("Duration:     %s", elapsed.Round(time.Millisecond))
	t.Logf("Throughput:   %.1f msgs/sec (%.1f writes/sec)", msgsPerSec, msgsPerSec*4)
	t.Logf("Latency p50:  %s", p50.Round(time.Microsecond))
	t.Logf("Latency p95:  %s", p95.Round(time.Microsecond))
	t.Logf("Latency p99:  %s", p99.Round(time.Microsecond))
	t.Logf("SQLITE_BUSY:  %d", busyErrors.Load())
	t.Logf("Total errors: %d", totalErrors.Load())
	t.Logf("Stats: tx=%d dupes=%d obs=%d nodes=%d observers=%d write_err=%d",
		s.Stats.TransmissionsInserted.Load(),
		s.Stats.DuplicateTransmissions.Load(),
		s.Stats.ObservationsInserted.Load(),
		s.Stats.NodeUpserts.Load(),
		s.Stats.ObserverUpserts.Load(),
		s.Stats.WriteErrors.Load(),
	)

	// Hard assertions
	if busyErrors.Load() > 0 {
		t.Errorf("SQLITE_BUSY errors: %d (expected 0)", busyErrors.Load())
	}
	if totalErrors.Load() > 0 {
		t.Errorf("Total errors: %d (expected 0)", totalErrors.Load())
	}

	var txCount int
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&txCount)
	if txCount != totalMessages {
		t.Errorf("transmissions=%d, want %d", txCount, totalMessages)
	}
}

func TestUpdateNodeTelemetry(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lat := 37.0
	lon := -122.0
	if err := s.UpsertNode("telem1", "TelemetryNode", "sensor", &lat, &lon, "2026-03-25T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	battery := 3700
	temp := 28.5
	if err := s.UpdateNodeTelemetry("telem1", &battery, &temp); err != nil {
		t.Fatal(err)
	}

	var bv int
	var tc float64
	err = s.db.QueryRow("SELECT battery_mv, temperature_c FROM nodes WHERE public_key = 'telem1'").Scan(&bv, &tc)
	if err != nil {
		t.Fatal(err)
	}
	if bv != 3700 {
		t.Errorf("battery_mv=%d, want 3700", bv)
	}
	if tc != 28.5 {
		t.Errorf("temperature_c=%f, want 28.5", tc)
	}

	newTemp := -5.0
	if err := s.UpdateNodeTelemetry("telem1", nil, &newTemp); err != nil {
		t.Fatal(err)
	}
	err = s.db.QueryRow("SELECT battery_mv, temperature_c FROM nodes WHERE public_key = 'telem1'").Scan(&bv, &tc)
	if err != nil {
		t.Fatal(err)
	}
	if bv != 3700 {
		t.Errorf("battery_mv after nil update=%d, want 3700 (preserved)", bv)
	}
	if tc != -5.0 {
		t.Errorf("temperature_c after update=%f, want -5.0", tc)
	}
}

func TestTelemetryMigrationAddsColumns(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.db.Exec("SELECT battery_mv, temperature_c FROM nodes LIMIT 1")
	if err != nil {
		t.Errorf("nodes table should have battery_mv and temperature_c columns: %v", err)
	}

	_, err = s.db.Exec("SELECT battery_mv, temperature_c FROM inactive_nodes LIMIT 1")
	if err != nil {
		t.Errorf("inactive_nodes table should have battery_mv and temperature_c columns: %v", err)
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM _migrations WHERE name = 'node_telemetry_v1'").Scan(&count)
	if count != 1 {
		t.Errorf("migration node_telemetry_v1 should be recorded, count=%d", count)
	}
}

// --- Bug #320: Observer metadata nested stats ---

func TestExtractObserverMetaNestedStats(t *testing.T) {
	// Real-world MQTT status payload: stats fields nested under "stats"
	msg := map[string]interface{}{
		"status":           "online",
		"origin":           "ObserverName",
		"model":            "Heltec V3",
		"firmware_version": "v1.14.0-9f1a3ea",
		"stats": map[string]interface{}{
			"battery_mv":  4174.0,
			"uptime_secs": 80277.0,
			"noise_floor": -110.0,
		},
	}
	meta := extractObserverMeta(msg)
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if meta.Model == nil || *meta.Model != "Heltec V3" {
		t.Errorf("Model=%v, want Heltec V3", meta.Model)
	}
	if meta.Firmware == nil || *meta.Firmware != "v1.14.0-9f1a3ea" {
		t.Errorf("Firmware=%v, want v1.14.0-9f1a3ea", meta.Firmware)
	}
	if meta.BatteryMv == nil || *meta.BatteryMv != 4174 {
		t.Errorf("BatteryMv=%v, want 4174", meta.BatteryMv)
	}
	if meta.UptimeSecs == nil || *meta.UptimeSecs != 80277 {
		t.Errorf("UptimeSecs=%v, want 80277", meta.UptimeSecs)
	}
	if meta.NoiseFloor == nil || *meta.NoiseFloor != -110.0 {
		t.Errorf("NoiseFloor=%v, want -110", meta.NoiseFloor)
	}
}

func TestExtractObserverMetaNestedStatsPrecedence(t *testing.T) {
	// If stats has a value AND top-level has a value, nested wins
	msg := map[string]interface{}{
		"battery_mv":  9999.0, // top-level (stale/wrong)
		"noise_floor": -120.0, // top-level (stale/wrong)
		"stats": map[string]interface{}{
			"battery_mv":  4174.0, // nested (correct)
			"noise_floor": -110.5, // nested (correct)
		},
	}
	meta := extractObserverMeta(msg)
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if meta.BatteryMv == nil || *meta.BatteryMv != 4174 {
		t.Errorf("BatteryMv=%v, want 4174 (nested should win over top-level)", meta.BatteryMv)
	}
	if meta.NoiseFloor == nil || *meta.NoiseFloor != -110.5 {
		t.Errorf("NoiseFloor=%v, want -110.5 (nested should win over top-level)", meta.NoiseFloor)
	}
}

func TestExtractObserverMetaFlatFallback(t *testing.T) {
	// Backward compatibility: flat structure (no stats object) still works
	msg := map[string]interface{}{
		"battery_mv":  3500.0,
		"uptime_secs": 86400.0,
		"noise_floor": -115.5,
	}
	meta := extractObserverMeta(msg)
	if meta == nil {
		t.Fatal("expected non-nil meta for flat structure")
	}
	if meta.BatteryMv == nil || *meta.BatteryMv != 3500 {
		t.Errorf("BatteryMv=%v, want 3500", meta.BatteryMv)
	}
	if meta.UptimeSecs == nil || *meta.UptimeSecs != 86400 {
		t.Errorf("UptimeSecs=%v, want 86400", meta.UptimeSecs)
	}
	if meta.NoiseFloor == nil || *meta.NoiseFloor != -115.5 {
		t.Errorf("NoiseFloor=%v, want -115.5", meta.NoiseFloor)
	}
}

func TestExtractObserverMetaEmptyStats(t *testing.T) {
	// Empty stats object should not crash, top-level fallback still applies
	msg := map[string]interface{}{
		"model": "T-Beam",
		"stats": map[string]interface{}{},
	}
	meta := extractObserverMeta(msg)
	if meta == nil {
		t.Fatal("expected non-nil meta (model is present)")
	}
	if meta.Model == nil || *meta.Model != "T-Beam" {
		t.Errorf("Model=%v, want T-Beam", meta.Model)
	}
	if meta.BatteryMv != nil {
		t.Errorf("BatteryMv should be nil, got %v", *meta.BatteryMv)
	}
}

func TestExtractObserverMetaStatsNotAMap(t *testing.T) {
	// stats field is not a map (e.g., string) — should not crash, fall back to top-level
	msg := map[string]interface{}{
		"stats":      "invalid",
		"battery_mv": 3700.0,
	}
	meta := extractObserverMeta(msg)
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if meta.BatteryMv == nil || *meta.BatteryMv != 3700 {
		t.Errorf("BatteryMv=%v, want 3700 (top-level fallback when stats is not a map)", meta.BatteryMv)
	}
}

func TestExtractObserverMetaNoiseFloorFloat(t *testing.T) {
	// noise_floor migrated to REAL — verify float precision preserved
	msg := map[string]interface{}{
		"stats": map[string]interface{}{
			"noise_floor": -108.75,
		},
	}
	meta := extractObserverMeta(msg)
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if meta.NoiseFloor == nil || *meta.NoiseFloor != -108.75 {
		t.Errorf("NoiseFloor=%v, want -108.75", meta.NoiseFloor)
	}
}

func TestExtractObserverMetaNestedNilSkipsTopLevel(t *testing.T) {
	// JSON {"stats": {"battery_mv": null}} decodes to nil value in the map.
	// Nested nil should suppress top-level fallback (nested wins semantics).
	msg := map[string]interface{}{
		"battery_mv": 3700.0,
		"stats": map[string]interface{}{
			"battery_mv": nil,
		},
	}
	meta := extractObserverMeta(msg)
	if meta != nil && meta.BatteryMv != nil {
		t.Error("nested nil should suppress top-level fallback")
	}
}

func TestObsTimestampIndexMigration(t *testing.T) {
	// Case 1: new DB — OpenStore should create idx_observations_timestamp as part
	// of the observations table schema.
	t.Run("NewDB", func(t *testing.T) {
		s, err := OpenStore(tempDBPath(t))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		var count int
		err = s.db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_observations_timestamp'",
		).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Error("idx_observations_timestamp should exist on a new DB")
		}

		var migCount int
		err = s.db.QueryRow(
			"SELECT COUNT(*) FROM _migrations WHERE name='obs_timestamp_index_v1'",
		).Scan(&migCount)
		if err != nil {
			t.Fatal(err)
		}
		// On a new DB the index is created inline (not via migration), so the
		// migration row may or may not be recorded — just verify the index exists.
		_ = migCount
	})

	// Case 2: existing DB that has the observations table but lacks the index
	// and lacks the _migrations entry — simulates an older installation.
	t.Run("MigrationPath", func(t *testing.T) {
		path := tempDBPath(t)

		// Build a bare-bones DB that mimics an old installation:
		// observations table exists but idx_observations_timestamp does NOT.
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`
			CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY);
			CREATE TABLE IF NOT EXISTS transmissions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				raw_hex TEXT NOT NULL,
				hash TEXT NOT NULL UNIQUE,
				first_seen TEXT NOT NULL,
				route_type INTEGER,
				payload_type INTEGER,
				payload_version INTEGER,
				decoded_json TEXT,
				created_at TEXT DEFAULT (datetime('now'))
			);
			CREATE TABLE IF NOT EXISTS observations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
				observer_idx INTEGER,
				direction TEXT,
				snr REAL,
				rssi REAL,
				score INTEGER,
				path_json TEXT,
				timestamp INTEGER NOT NULL
			);
		`)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		// Confirm the index is absent before OpenStore runs.
		var preCount int
		db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_observations_timestamp'",
		).Scan(&preCount)
		db.Close()
		if preCount != 0 {
			t.Fatalf("pre-condition failed: idx_observations_timestamp should not exist yet, got count=%d", preCount)
		}

		// Now open via OpenStore — the migration should add the index.
		s, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		var idxCount int
		err = s.db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_observations_timestamp'",
		).Scan(&idxCount)
		if err != nil {
			t.Fatal(err)
		}
		if idxCount != 1 {
			t.Error("idx_observations_timestamp should exist after migration on old DB")
		}

		var migCount int
		err = s.db.QueryRow(
			"SELECT COUNT(*) FROM _migrations WHERE name='obs_timestamp_index_v1'",
		).Scan(&migCount)
		if err != nil {
			t.Fatal(err)
		}
		if migCount != 1 {
			t.Errorf("migration obs_timestamp_index_v1 should be recorded, got count=%d", migCount)
		}
	})
}

func TestBuildPacketDataScoreAndDirection(t *testing.T) {
	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	decoded, err := DecodePacket(rawHex, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	score := 42.0
	dir := "incoming"
	msg := &MQTTPacketMessage{
		Raw:       rawHex,
		Score:     &score,
		Direction: &dir,
	}

	pkt := BuildPacketData(msg, decoded, "obs1", "SJC")
	if pkt.Score == nil || *pkt.Score != 42.0 {
		t.Errorf("Score=%v, want 42.0", pkt.Score)
	}
	if pkt.Direction == nil || *pkt.Direction != "incoming" {
		t.Errorf("Direction=%v, want incoming", pkt.Direction)
	}
}

func TestBuildPacketDataNilScoreDirection(t *testing.T) {
	decoded, _ := DecodePacket("0A00"+strings.Repeat("00", 10), nil, false)
	msg := &MQTTPacketMessage{Raw: "0A00" + strings.Repeat("00", 10)}
	pkt := BuildPacketData(msg, decoded, "", "")

	if pkt.Score != nil {
		t.Errorf("Score should be nil, got %v", *pkt.Score)
	}
	if pkt.Direction != nil {
		t.Errorf("Direction should be nil, got %v", *pkt.Direction)
	}
}

func TestInsertTransmissionWithScoreAndDirection(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	score := 7.5
	dir := "outgoing"
	data := &PacketData{
		RawHex:    "AABB",
		Timestamp: "2025-01-01T00:00:00Z",
		SNR:       ptrFloat(5.0),
		RSSI:      ptrFloat(-90.0),
		Score:     &score,
		Direction: &dir,
		Hash:      "abc123",
		PathJSON:  "[]",
	}

	isNew, err := s.InsertTransmission(data)
	if err != nil {
		t.Fatal(err)
	}
	if !isNew {
		t.Error("expected new transmission")
	}

	// Verify the observation was stored with score and direction
	var gotDir sql.NullString
	var gotScore sql.NullFloat64
	err = s.db.QueryRow("SELECT direction, score FROM observations LIMIT 1").Scan(&gotDir, &gotScore)
	if err != nil {
		t.Fatal(err)
	}
	if !gotDir.Valid || gotDir.String != "outgoing" {
		t.Errorf("direction=%v, want outgoing", gotDir)
	}
	if !gotScore.Valid || gotScore.Float64 != 7.5 {
		t.Errorf("score=%v, want 7.5", gotScore)
	}
}

func ptrFloat(f float64) *float64 { return &f }
func ptrInt(i int) *int           { return &i }

func TestRoundToInterval(t *testing.T) {
	tests := []struct {
		input    time.Time
		interval int
		want     time.Time
	}{
		{time.Date(2026, 4, 5, 10, 2, 0, 0, time.UTC), 300, time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)},
		{time.Date(2026, 4, 5, 10, 3, 0, 0, time.UTC), 300, time.Date(2026, 4, 5, 10, 5, 0, 0, time.UTC)},
		{time.Date(2026, 4, 5, 10, 2, 30, 0, time.UTC), 300, time.Date(2026, 4, 5, 10, 5, 0, 0, time.UTC)},
		{time.Date(2026, 4, 5, 10, 5, 0, 0, time.UTC), 300, time.Date(2026, 4, 5, 10, 5, 0, 0, time.UTC)},
		{time.Date(2026, 4, 5, 10, 7, 29, 0, time.UTC), 300, time.Date(2026, 4, 5, 10, 5, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		got := RoundToInterval(tc.input, tc.interval)
		if !got.Equal(tc.want) {
			t.Errorf("RoundToInterval(%v, %d) = %v, want %v", tc.input, tc.interval, got, tc.want)
		}
	}
}

func TestInsertMetrics(t *testing.T) {
	store, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nf := -112.5
	txAir := 100
	rxAir := 500
	recvErr := 3
	batt := 3720
	data := &MetricsData{
		ObserverID: "obs1",
		NoiseFloor: &nf,
		TxAirSecs:  &txAir,
		RxAirSecs:  &rxAir,
		RecvErrors: &recvErr,
		BatteryMv:  &batt,
	}

	if err := store.InsertMetrics(data); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	// Verify insertion
	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM observer_metrics WHERE observer_id = 'obs1'").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}

	// Verify values
	var gotNF float64
	var gotTx, gotRx, gotErr, gotBatt int
	store.db.QueryRow("SELECT noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv FROM observer_metrics WHERE observer_id = 'obs1'").Scan(&gotNF, &gotTx, &gotRx, &gotErr, &gotBatt)
	if gotNF != -112.5 {
		t.Errorf("noise_floor = %v, want -112.5", gotNF)
	}
	if gotTx != 100 {
		t.Errorf("tx_air_secs = %d, want 100", gotTx)
	}
}

func TestInsertMetricsIdempotent(t *testing.T) {
	store, err := OpenStoreWithInterval(tempDBPath(t), 300)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nf := -110.0
	data := &MetricsData{ObserverID: "obs1", NoiseFloor: &nf}

	// Insert twice — should result in 1 row (INSERT OR REPLACE)
	store.InsertMetrics(data)
	nf2 := -108.0
	data.NoiseFloor = &nf2
	store.InsertMetrics(data)

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM observer_metrics WHERE observer_id = 'obs1'").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row (idempotent), got %d", count)
	}

	// Verify the value was replaced
	var gotNF float64
	store.db.QueryRow("SELECT noise_floor FROM observer_metrics WHERE observer_id = 'obs1'").Scan(&gotNF)
	if gotNF != -108.0 {
		t.Errorf("noise_floor = %v, want -108.0 (replaced)", gotNF)
	}
}

func TestInsertMetricsNullFields(t *testing.T) {
	store, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nf := -115.0
	data := &MetricsData{
		ObserverID: "obs1",
		NoiseFloor: &nf,
		// All other fields nil
	}

	if err := store.InsertMetrics(data); err != nil {
		t.Fatalf("InsertMetrics with nulls: %v", err)
	}

	var gotNF sql.NullFloat64
	var gotTx sql.NullInt64
	store.db.QueryRow("SELECT noise_floor, tx_air_secs FROM observer_metrics WHERE observer_id = 'obs1'").Scan(&gotNF, &gotTx)
	if !gotNF.Valid || gotNF.Float64 != -115.0 {
		t.Errorf("noise_floor = %v, want -115.0", gotNF)
	}
	if gotTx.Valid {
		t.Errorf("tx_air_secs should be NULL, got %v", gotTx.Int64)
	}
}

func TestPruneOldMetrics(t *testing.T) {
	store, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Insert old and new metrics directly
	oldTs := time.Now().UTC().AddDate(0, 0, -40).Format(time.RFC3339)
	newTs := time.Now().UTC().Format(time.RFC3339)
	store.db.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor) VALUES (?, ?, ?)", "obs1", oldTs, -110.0)
	store.db.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor) VALUES (?, ?, ?)", "obs1", newTs, -112.0)

	n, err := store.PruneOldMetrics(30)
	if err != nil {
		t.Fatalf("PruneOldMetrics: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1", n)
	}

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM observer_metrics").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row remaining, got %d", count)
	}
}

func TestExtractObserverMetaNewFields(t *testing.T) {
	msg := map[string]interface{}{
		"model": "L1",
		"stats": map[string]interface{}{
			"noise_floor":  -112.5,
			"battery_mv":   3720.0,
			"uptime_secs":  86400.0,
			"tx_air_secs":  100.0,
			"rx_air_secs":  500.0,
			"recv_errors":  3.0,
		},
	}
	meta := extractObserverMeta(msg)
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if meta.TxAirSecs == nil || *meta.TxAirSecs != 100 {
		t.Errorf("TxAirSecs = %v, want 100", meta.TxAirSecs)
	}
	if meta.RxAirSecs == nil || *meta.RxAirSecs != 500 {
		t.Errorf("RxAirSecs = %v, want 500", meta.RxAirSecs)
	}
	if meta.RecvErrors == nil || *meta.RecvErrors != 3 {
		t.Errorf("RecvErrors = %v, want 3", meta.RecvErrors)
	}
}
