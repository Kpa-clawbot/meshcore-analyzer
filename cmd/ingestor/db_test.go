package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meshcore-analyzer/packetpath"
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
	before := time.Now().Unix()
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Unix()

	// Verify last_seen was updated to INGEST time, not envelope time (#1465).
	var lastSeenAfter string
	s.db.QueryRow("SELECT last_seen FROM observers WHERE id = ?", "obs1").Scan(&lastSeenAfter)
	if lastSeenAfter == oldTime {
		t.Error("observer last_seen was NOT updated after packet insertion — low-traffic observers will appear offline")
	}
	ls, err := time.Parse(time.RFC3339, lastSeenAfter)
	if err != nil {
		t.Fatalf("last_seen %q not RFC3339: %v", lastSeenAfter, err)
	}
	if ls.Unix() < before-5 || ls.Unix() > after+5 {
		t.Errorf("expected last_seen ≈ server now (in [%d, %d]), got %s (epoch %d). "+
			"observer.last_seen must use ingest time, not envelope time (#1465).",
			before, after, lastSeenAfter, ls.Unix())
	}
}

func TestLastPacketAtUpdatedOnPacketOnly(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Insert observer via status path — last_packet_at should be NULL
	if err := s.UpsertObserver("obs1", "Observer1", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	var lastPacketAt sql.NullString
	s.db.QueryRow("SELECT last_packet_at FROM observers WHERE id = ?", "obs1").Scan(&lastPacketAt)
	if lastPacketAt.Valid {
		t.Fatalf("expected last_packet_at to be NULL after UpsertObserver, got %s", lastPacketAt.String)
	}

	// Insert a packet from this observer — last_packet_at should be set
	data := &PacketData{
		RawHex:      "0A00D69F",
		Timestamp:   "2026-04-24T12:00:00Z",
		ObserverID:  "obs1",
		Hash:        "lastpackettest123456",
		RouteType:   2,
		PayloadType: 2,
		PathJSON:    "[]",
		DecodedJSON: `{"type":"TXT_MSG"}`,
	}
	before := time.Now().Unix()
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Unix()

	s.db.QueryRow("SELECT last_packet_at FROM observers WHERE id = ?", "obs1").Scan(&lastPacketAt)
	if !lastPacketAt.Valid {
		t.Fatal("expected last_packet_at to be non-NULL after InsertTransmission")
	}
	// last_packet_at, like last_seen, is "when did the analyzer last receive a
	// packet from this observer" — an ingest-time question, independent of the
	// envelope timestamp. See #1465.
	lp, err := time.Parse(time.RFC3339, lastPacketAt.String)
	if err != nil {
		t.Fatalf("last_packet_at %q not RFC3339: %v", lastPacketAt.String, err)
	}
	if lp.Unix() < before-5 || lp.Unix() > after+5 {
		t.Errorf("expected last_packet_at ≈ server now (in [%d, %d]), got %s (epoch %d)",
			before, after, lastPacketAt.String, lp.Unix())
	}

	// UpsertObserver again (status path) — last_packet_at should NOT change
	if err := s.UpsertObserver("obs1", "Observer1", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	var lastPacketAtAfterStatus sql.NullString
	s.db.QueryRow("SELECT last_packet_at FROM observers WHERE id = ?", "obs1").Scan(&lastPacketAtAfterStatus)
	if !lastPacketAtAfterStatus.Valid || lastPacketAtAfterStatus.String != lastPacketAt.String {
		t.Errorf("UpsertObserver should not change last_packet_at; expected %s, got %v", lastPacketAt.String, lastPacketAtAfterStatus)
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
	pktData := BuildPacketData(msg, decoded, "obs1", "SJC", nil)
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
		Raw:       rawHex,
		SNR:       &snr,
		RSSI:      &rssi,
		Origin:    "test-observer",
		Timestamp: "2026-05-16T10:00:00Z",
	}

	pkt := BuildPacketData(msg, decoded, "obs123", "SJC", nil)

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
		t.Errorf("timestamp must be populated (server ingest time, #1370 reverts #1233)")
	}
	if pkt.Timestamp == "2026-05-16T10:00:00Z" {
		t.Errorf("timestamp=%s; must NOT be the envelope value (#1370 reverts #1233's "+
			"premise that envelope timestamp is trustworthy — buggy client clocks poison ordering)", pkt.Timestamp)
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
	pkt := BuildPacketData(msg, decoded, "", "", nil)

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
	pkt := BuildPacketData(msg, decoded, "", "", nil)

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

	pkt := BuildPacketData(msg, decoded, "obs1", "SJC", nil)
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
	pkt := BuildPacketData(msg, decoded, "", "", nil)

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
			"noise_floor": -112.5,
			"battery_mv":  3720.0,
			"uptime_secs": 86400.0,
			"tx_air_secs": 100.0,
			"rx_air_secs": 500.0,
			"recv_errors": 3.0,
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

// TestInsertObservationSNRFillIn verifies that when the same observation is
// received twice — first without SNR, then with SNR — the SNR is filled in
// rather than silently discarded. The unique dedup index is
// (transmission_id, observer_idx, COALESCE(path_json, '')); observer_idx must
// be non-NULL for the conflict to fire (SQLite treats NULL != NULL).
//
// (Blank line above is deliberate: gofmt's Go-1.19+ doc-comment formatter
// rewrites a doubled straight-quote pair like '' into a single curly “ once
// this comment is attached as a doc comment to the func below — detaching it
// keeps the literal '' matching the real index expression in the
// idx_observations_dedup CREATE UNIQUE INDEX statements elsewhere in this file.)

func TestInsertObservationSNRFillIn(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Register the observer so observer_idx is non-NULL (required for dedup).
	if err := s.UpsertObserver("pymc-obs1", "PyMC Observer", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	// First arrival: same observer, no SNR/RSSI (e.g. broker replay without RF fields).
	data1 := &PacketData{
		RawHex:     "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976",
		Timestamp:  "2026-04-20T00:00:00Z",
		Hash:       "snrfillin0001hash",
		RouteType:  1,
		ObserverID: "pymc-obs1",
		SNR:        nil,
		RSSI:       nil,
	}
	if _, err := s.InsertTransmission(data1); err != nil {
		t.Fatal(err)
	}

	var snr1, rssi1 *float64
	s.db.QueryRow("SELECT snr, rssi FROM observations LIMIT 1").Scan(&snr1, &rssi1)
	if snr1 != nil || rssi1 != nil {
		t.Fatalf("precondition: first insert should have nil SNR/RSSI, got snr=%v rssi=%v", snr1, rssi1)
	}

	// Second arrival: same packet, same observer, now WITH SNR/RSSI.
	snr := 10.5
	rssi := -88.0
	data2 := &PacketData{
		RawHex:     data1.RawHex,
		Timestamp:  data1.Timestamp,
		Hash:       data1.Hash,
		RouteType:  data1.RouteType,
		ObserverID: "pymc-obs1",
		SNR:        &snr,
		RSSI:       &rssi,
	}
	if _, err := s.InsertTransmission(data2); err != nil {
		t.Fatal(err)
	}

	var snr2, rssi2 *float64
	s.db.QueryRow("SELECT snr, rssi FROM observations LIMIT 1").Scan(&snr2, &rssi2)
	if snr2 == nil || *snr2 != snr {
		t.Errorf("SNR not filled in by second arrival: got %v, want %v", snr2, snr)
	}
	if rssi2 == nil || *rssi2 != rssi {
		t.Errorf("RSSI not filled in by second arrival: got %v, want %v", rssi2, rssi)
	}

	// Third arrival: same packet again, SNR absent — must NOT overwrite existing SNR.
	data3 := &PacketData{
		RawHex:     data1.RawHex,
		Timestamp:  data1.Timestamp,
		Hash:       data1.Hash,
		RouteType:  data1.RouteType,
		ObserverID: "pymc-obs1",
		SNR:        nil,
		RSSI:       nil,
	}
	if _, err := s.InsertTransmission(data3); err != nil {
		t.Fatal(err)
	}

	var snr3, rssi3 *float64
	s.db.QueryRow("SELECT snr, rssi FROM observations LIMIT 1").Scan(&snr3, &rssi3)
	if snr3 == nil || *snr3 != snr {
		t.Errorf("SNR overwritten by null arrival: got %v, want %v", snr3, snr)
	}
	if rssi3 == nil || *rssi3 != rssi {
		t.Errorf("RSSI overwritten by null arrival: got %v, want %v", rssi3, rssi)
	}
}

// TestPerObservationRawHex verifies that two MQTT packets for the same hash
// from different observers store distinct raw_hex per observation (#881).
func TestPerObservationRawHex(t *testing.T) {
	store, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Register two observers
	store.UpsertObserver("obs-A", "Observer A", "", nil)
	store.UpsertObserver("obs-B", "Observer B", "", nil)

	hash := "abc123def456"
	rawA := "c0ffee01"
	rawB := "c0ffee0201aa"
	dir := "RX"

	// First observation from observer A
	pdA := &PacketData{
		RawHex:     rawA,
		Hash:       hash,
		Timestamp:  "2026-04-21T10:00:00Z",
		ObserverID: "obs-A",
		Direction:  &dir,
		PathJSON:   "[]",
	}
	isNew, err := store.InsertTransmission(pdA)
	if err != nil {
		t.Fatalf("insert A: %v", err)
	}
	if !isNew {
		t.Fatal("expected new transmission")
	}

	// Second observation from observer B (same hash, different raw bytes)
	pdB := &PacketData{
		RawHex:     rawB,
		Hash:       hash,
		Timestamp:  "2026-04-21T10:00:01Z",
		ObserverID: "obs-B",
		Direction:  &dir,
		PathJSON:   `["aabb"]`,
	}
	isNew2, err := store.InsertTransmission(pdB)
	if err != nil {
		t.Fatalf("insert B: %v", err)
	}
	if isNew2 {
		t.Fatal("expected duplicate transmission")
	}

	// Query observations and verify per-observation raw_hex
	rows, err := store.db.Query(`
		SELECT o.raw_hex, obs.id
		FROM observations o
		LEFT JOIN observers obs ON obs.rowid = o.observer_idx
		ORDER BY o.id ASC
	`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	type obsResult struct {
		rawHex     string
		observerID string
	}
	var results []obsResult
	for rows.Next() {
		var rh, oid sql.NullString
		if err := rows.Scan(&rh, &oid); err != nil {
			t.Fatal(err)
		}
		results = append(results, obsResult{
			rawHex:     rh.String,
			observerID: oid.String,
		})
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(results))
	}
	if results[0].rawHex != rawA {
		t.Errorf("obs A raw_hex: got %q, want %q", results[0].rawHex, rawA)
	}
	if results[1].rawHex != rawB {
		t.Errorf("obs B raw_hex: got %q, want %q", results[1].rawHex, rawB)
	}
	if results[0].rawHex == results[1].rawHex {
		t.Error("both observations have same raw_hex — should differ")
	}
}

// TestBuildPacketData_TraceUsesPayloadHops verifies that TRACE packets use
// payload-decoded route hops in path_json (NOT the raw_hex header SNR bytes).
// Issue #886 / #887.
func TestBuildPacketData_TraceUsesPayloadHops(t *testing.T) {
	// TRACE packet: header path has SNR bytes [30,2D,0D,23], but decoded.Path.Hops
	// is overwritten to payload hops [67,33,D6,33,67].
	rawHex := "2604302D0D2359FEE7B100000000006733D63367"
	decoded, err := DecodePacket(rawHex, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	// decoded.Path.Hops should be the TRACE-replaced hops (payload hops)
	if len(decoded.Path.Hops) != 5 {
		t.Fatalf("expected 5 decoded hops, got %d", len(decoded.Path.Hops))
	}

	msg := &MQTTPacketMessage{Raw: rawHex}
	pd := BuildPacketData(msg, decoded, "test-obs", "TST", nil)

	// For TRACE: path_json MUST be the payload-decoded route hops, NOT the SNR bytes
	expectedPathJSON := `["67","33","D6","33","67"]`
	if pd.PathJSON != expectedPathJSON {
		t.Errorf("path_json = %s, want %s (TRACE must use payload hops)", pd.PathJSON, expectedPathJSON)
	}

	// Verify that DecodePathFromRawHex returns the SNR bytes (header path) which differ
	headerHops, herr := packetpath.DecodePathFromRawHex(rawHex)
	if herr != nil {
		t.Fatal(herr)
	}
	headerJSON, _ := json.Marshal(headerHops)
	if string(headerJSON) == expectedPathJSON {
		t.Error("header path (SNR) should differ from payload hops for TRACE")
	}
}

// TestBuildPacketData_NonTracePathJSON verifies non-TRACE packets also derive path from raw_hex.
func TestBuildPacketData_NonTracePathJSON(t *testing.T) {
	// A simple ADVERT packet (payload type 0) with 2 hops, hash_size 1
	// Header 0x09 = FLOOD(1), ADVERT(2), version 0
	// Path byte 0x02 = hash_size 1, hash_count 2
	// Path bytes: AA BB
	rawHex := "0902AABB" + "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	decoded, err := DecodePacket(rawHex, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	msg := &MQTTPacketMessage{Raw: rawHex}
	pd := BuildPacketData(msg, decoded, "obs1", "TST", nil)

	expectedPathJSON := `["AA","BB"]`
	if pd.PathJSON != expectedPathJSON {
		t.Errorf("path_json = %s, want %s", pd.PathJSON, expectedPathJSON)
	}
}

func TestScopeNameMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Verify column exists
	rows, err := store.db.Query("PRAGMA table_info(transmissions)")
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	found := false
	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk); err == nil && colName == "scope_name" {
			found = true
		}
	}
	rows.Close()
	if !found {
		t.Fatal("scope_name column not found in transmissions")
	}

	// Verify column actually stores and retrieves values (NULL and non-NULL).
	_, err = store.db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES ('aabb', 'hash1', '2026-01-01T00:00:00Z', 0, 5, '#belgium')`)
	if err != nil {
		t.Fatalf("insert scoped row: %v", err)
	}
	_, err = store.db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES ('ccdd', 'hash2', '2026-01-01T00:00:01Z', 0, 5, NULL)`)
	if err != nil {
		t.Fatalf("insert unscoped row: %v", err)
	}

	var name string
	if err := store.db.QueryRow(`SELECT scope_name FROM transmissions WHERE hash = 'hash1'`).Scan(&name); err != nil {
		t.Fatalf("read scope_name: %v", err)
	}
	if name != "#belgium" {
		t.Errorf("scope_name = %q, want #belgium", name)
	}

	var nullScope interface{}
	if err := store.db.QueryRow(`SELECT scope_name FROM transmissions WHERE hash = 'hash2'`).Scan(&nullScope); err != nil {
		t.Fatalf("read null scope_name: %v", err)
	}
	if nullScope != nil {
		t.Errorf("scope_name for unscoped = %v, want nil", nullScope)
	}
}

func TestTransportCodesStored(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Byte order matters: Code1 is the RAW wire hex of the two bytes,
	// uppercased, with NO little-endian swap (decoder.go:954). Bytes AA BB
	// therefore read back as "AABB". Layout, per firmware packet_format.md:
	// header + transport_codes(4) + path_len + path + payload.
	// header 0x14 = route_type 0 (TRANSPORT_FLOOD), payload_type 5 (GRP_TXT).
	// payload_type 5 keeps the advert validation path out of this test.
	rawScoped := "14" + "AABB" + "CCDD" + "00" + strings.Repeat("11", 32)
	// header 0x15 = route_type 1 (FLOOD), payload_type 5 — no transport codes.
	rawUnscoped := "15" + "00" + strings.Repeat("22", 32)
	// header 0x14 = route_type 0 (TRANSPORT_FLOOD) with all-zero transport
	// codes. "0000" is stored literally (NOT NULL) — it is the meaningful
	// "explicitly unscoped transport packet" case and must not collapse
	// into the same NULL a non-transport packet gets.
	rawZeroCodes := "14" + "0000" + "0000" + "00" + strings.Repeat("33", 32)

	for _, raw := range []string{rawScoped, rawUnscoped, rawZeroCodes} {
		pd := &PacketData{
			RawHex: raw, Hash: ComputeContentHash(raw), Timestamp: "2026-08-17T10:00:00Z",
			ObserverID: "obs1", RouteType: 0, PayloadType: 5,
		}
		if raw == rawUnscoped {
			pd.RouteType = 1
		}
		if raw == rawScoped {
			pd.Code1, pd.Code2 = "AABB", "CCDD"
		}
		if raw == rawZeroCodes {
			pd.Code1, pd.Code2 = "0000", "0000"
		}
		if _, err := s.InsertTransmission(pd); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	var c1, c2 *string
	if err := s.db.QueryRow(`SELECT code1, code2 FROM transmissions WHERE hash = ?`,
		ComputeContentHash(rawScoped)).Scan(&c1, &c2); err != nil {
		t.Fatalf("read scoped: %v", err)
	}
	if c1 == nil || *c1 != "AABB" || c2 == nil || *c2 != "CCDD" {
		t.Errorf("scoped codes = %v/%v, want AABB/CCDD", c1, c2)
	}

	if err := s.db.QueryRow(`SELECT code1, code2 FROM transmissions WHERE hash = ?`,
		ComputeContentHash(rawUnscoped)).Scan(&c1, &c2); err != nil {
		t.Fatalf("read unscoped: %v", err)
	}
	if c1 != nil || c2 != nil {
		t.Errorf("unscoped codes = %v/%v, want NULL/NULL", c1, c2)
	}

	if err := s.db.QueryRow(`SELECT code1, code2 FROM transmissions WHERE hash = ?`,
		ComputeContentHash(rawZeroCodes)).Scan(&c1, &c2); err != nil {
		t.Fatalf("read zero-codes: %v", err)
	}
	if c1 == nil || *c1 != "0000" || c2 == nil || *c2 != "0000" {
		t.Errorf("zero-codes codes = %v/%v, want literal \"0000\"/\"0000\" (not NULL)", c1, c2)
	}
}

func TestBackfillTransportCodes(t *testing.T) {
	s := newTestStore(t)

	// Same layout and byte-order rules as TestTransportCodesStored: raw wire
	// hex, no swap, so bytes AA BB read back as "AABB".
	rawScoped := "14" + "AABB" + "CCDD" + "00" + strings.Repeat("11", 32)
	rawFlood := "15" + "00" + strings.Repeat("22", 32)

	// Simulate pre-migration rows: inserted directly, code1/code2 left NULL.
	// route_type must match the raw header, or the backfill's route filter
	// selects the wrong rows: 0 for the transport packet, 1 for the flood.
	for _, tc := range []struct {
		raw       string
		routeType int
	}{{rawScoped, 0}, {rawFlood, 1}} {
		if _, err := s.db.Exec(
			`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
			 VALUES (?,?,?,?,?)`,
			tc.raw, ComputeContentHash(tc.raw), "2026-08-01T00:00:00Z", tc.routeType, 5); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	s.BackfillTransportCodesAsync()
	s.backfillWg.Wait()

	var c1 *string
	if err := s.db.QueryRow(`SELECT code1 FROM transmissions WHERE hash = ?`,
		ComputeContentHash(rawScoped)).Scan(&c1); err != nil {
		t.Fatalf("read: %v", err)
	}
	if c1 == nil || *c1 != "AABB" {
		t.Errorf("backfilled code1 = %v, want AABB", c1)
	}

	if err := s.db.QueryRow(`SELECT code1 FROM transmissions WHERE hash = ?`,
		ComputeContentHash(rawFlood)).Scan(&c1); err != nil {
		t.Fatalf("read flood: %v", err)
	}
	if c1 != nil {
		t.Errorf("flood code1 = %v, want NULL", c1)
	}

	// Idempotent: a second run is a no-op guarded by _migrations.
	var done int
	if err := s.db.QueryRow(
		`SELECT 1 FROM _migrations WHERE name = 'backfill_transport_codes_v1'`).Scan(&done); err != nil {
		t.Errorf("migration not recorded: %v", err)
	}
}

// TestBackfillTransportCodes_MultiBatchWithUndecodableRow is a regression test for
// a defect in the original loop: it terminated on "items returned < batchSize"
// rather than "rows scanned < batchSize", so a full page containing even one
// undecodable row broke the loop early and the exported wrapper then recorded
// the completion migration — silently truncating the backfill and, because the
// guard blocks all future runs, leaving every later row permanently NULL.
// Seeds more rows than one batch holds, with an undecodable row in the first
// page, and asserts every decodable row across every page got backfilled.
func TestBackfillTransportCodes_MultiBatchWithUndecodableRow(t *testing.T) {
	s := newTestStore(t)

	const batchSize = 3

	// route_type 0 (TRANSPORT_FLOOD) header, but only 1 byte follows — too
	// short for the 4-byte transport codes field, so DecodePacket errors and
	// this row is skipped. It must not stop rows after it in later pages from
	// being backfilled.
	badRaw := "14" + "AA"

	// Payload byte varies per row so ComputeContentHash (which hashes only the
	// payload, skipping path and transport-code bytes) gives each row a
	// distinct hash despite the shared header/path/code shape.
	var goodRaws []string
	for i := 1; i <= 6; i++ {
		goodRaws = append(goodRaws, fmt.Sprintf("14%04X%04X00%s", i, i+100, strings.Repeat(fmt.Sprintf("%02X", i), 32)))
	}

	// Insertion order fixes id order: bad row first, then the 6 good rows,
	// giving 7 matching rows across 3 pages of 3 — batch 1 = {bad, good1,
	// good2}, batch 2 = {good3, good4, good5}, batch 3 = {good6}.
	if _, err := s.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
		 VALUES (?,?,?,?,?)`,
		badRaw, ComputeContentHash(badRaw), "2026-08-01T00:00:00Z", 0, 5); err != nil {
		t.Fatalf("seed bad row: %v", err)
	}
	for _, raw := range goodRaws {
		if _, err := s.db.Exec(
			`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
			 VALUES (?,?,?,?,?)`,
			raw, ComputeContentHash(raw), "2026-08-01T00:00:00Z", 0, 5); err != nil {
			t.Fatalf("seed good row: %v", err)
		}
	}

	total, err := s.backfillTransportCodes(batchSize)
	if err != nil {
		t.Fatalf("backfillTransportCodes: %v", err)
	}
	if total != len(goodRaws) {
		t.Errorf("backfillTransportCodes returned %d, want %d (one per good row)", total, len(goodRaws))
	}

	for i, raw := range goodRaws {
		wantCode1 := fmt.Sprintf("%04X", i+1)
		var c1 *string
		if err := s.db.QueryRow(`SELECT code1 FROM transmissions WHERE hash = ?`,
			ComputeContentHash(raw)).Scan(&c1); err != nil {
			t.Fatalf("read good row %d: %v", i, err)
		}
		if c1 == nil || *c1 != wantCode1 {
			t.Errorf("good row %d code1 = %v, want %s (row after the undecodable one in an earlier or later page must still be backfilled)", i, c1, wantCode1)
		}
	}

	var badC1 *string
	if err := s.db.QueryRow(`SELECT code1 FROM transmissions WHERE hash = ?`,
		ComputeContentHash(badRaw)).Scan(&badC1); err != nil {
		t.Fatalf("read bad row: %v", err)
	}
	if badC1 != nil {
		t.Errorf("undecodable row code1 = %v, want NULL", badC1)
	}
}

// TestBackfillTransportCodes_ErrorWithholdsGuardRow is a regression test for the
// invariant that backfill_transport_codes_v1 is recorded iff and only if the
// backfill actually ran to exhaustion of all matching rows. Forces the select
// branch of backfillTransportCodes to fail mid-run by closing the store's DB
// connection out from under it (simulating a transient DB error), then verifies
// (a) the guard row is absent afterward and (b) reopening a fresh Store on the
// same file and running the backfill again still processes the row the failed
// run never reached.
func TestBackfillTransportCodes_ErrorWithholdsGuardRow(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	raw := "14" + "AABB" + "CCDD" + "00" + strings.Repeat("11", 32)
	if _, err := store1.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
		 VALUES (?,?,?,?,?)`,
		raw, ComputeContentHash(raw), "2026-08-01T00:00:00Z", 0, 5); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := store1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := store1.backfillTransportCodes(5000); err == nil {
		t.Fatal("backfillTransportCodes on a closed store returned nil error, want non-nil")
	}

	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	var done int
	if err := store2.db.QueryRow(
		`SELECT 1 FROM _migrations WHERE name = 'backfill_transport_codes_v1'`).Scan(&done); err != sql.ErrNoRows {
		t.Fatalf("guard row query = (done=%d, err=%v), want sql.ErrNoRows — guard must be withheld after a failed run", done, err)
	}

	// Unobstructed, a subsequent run must still backfill the row the failed
	// run never reached.
	store2.BackfillTransportCodesAsync()
	store2.backfillWg.Wait()

	var c1 *string
	if err := store2.db.QueryRow(`SELECT code1 FROM transmissions WHERE hash = ?`,
		ComputeContentHash(raw)).Scan(&c1); err != nil {
		t.Fatalf("read: %v", err)
	}
	if c1 == nil || *c1 != "AABB" {
		t.Errorf("code1 = %v, want AABB — subsequent run must backfill what the failed run skipped", c1)
	}
	if err := store2.db.QueryRow(
		`SELECT 1 FROM _migrations WHERE name = 'backfill_transport_codes_v1'`).Scan(&done); err != nil {
		t.Errorf("guard row not recorded after a successful run: %v", err)
	}
}

// --- Feature 3: default_scope column on nodes (#899) ---

func TestUpdateNodeDefaultScope(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Insert a node into nodes and inactive_nodes so both tables can be updated.
	if _, err := store.db.Exec(`INSERT INTO nodes (public_key, name) VALUES ('pk1', 'Node1')`); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO inactive_nodes (public_key, name) VALUES ('pk1', 'Node1')`); err != nil {
		t.Fatalf("insert inactive node: %v", err)
	}

	// First call: writes scope to both tables.
	if err := store.UpdateNodeDefaultScope("pk1", "#belgium"); err != nil {
		t.Fatalf("UpdateNodeDefaultScope: %v", err)
	}
	var got string
	if err := store.db.QueryRow(`SELECT default_scope FROM nodes WHERE public_key = 'pk1'`).Scan(&got); err != nil {
		t.Fatalf("read nodes.default_scope: %v", err)
	}
	if got != "#belgium" {
		t.Errorf("nodes.default_scope = %q, want #belgium", got)
	}
	var gotInactive string
	if err := store.db.QueryRow(`SELECT default_scope FROM inactive_nodes WHERE public_key = 'pk1'`).Scan(&gotInactive); err != nil {
		t.Fatalf("read inactive_nodes.default_scope: %v", err)
	}
	if gotInactive != "#belgium" {
		t.Errorf("inactive_nodes.default_scope = %q, want #belgium", gotInactive)
	}

	// Second call with same value: short-circuit, no redundant UPDATE (verify no error and value stable).
	if err := store.UpdateNodeDefaultScope("pk1", "#belgium"); err != nil {
		t.Fatalf("UpdateNodeDefaultScope short-circuit: %v", err)
	}
	if err := store.db.QueryRow(`SELECT default_scope FROM nodes WHERE public_key = 'pk1'`).Scan(&got); err != nil {
		t.Fatalf("read after short-circuit: %v", err)
	}
	if got != "#belgium" {
		t.Errorf("after short-circuit nodes.default_scope = %q, want #belgium", got)
	}

	// Third call with different value: updates both tables.
	if err := store.UpdateNodeDefaultScope("pk1", "#eu"); err != nil {
		t.Fatalf("UpdateNodeDefaultScope update: %v", err)
	}
	if err := store.db.QueryRow(`SELECT default_scope FROM nodes WHERE public_key = 'pk1'`).Scan(&got); err != nil {
		t.Fatalf("read after update: %v", err)
	}
	if got != "#eu" {
		t.Errorf("after update nodes.default_scope = %q, want #eu", got)
	}
	if err := store.db.QueryRow(`SELECT default_scope FROM inactive_nodes WHERE public_key = 'pk1'`).Scan(&gotInactive); err != nil {
		t.Fatalf("read inactive after update: %v", err)
	}
	if gotInactive != "#eu" {
		t.Errorf("after update inactive_nodes.default_scope = %q, want #eu", gotInactive)
	}
}

// --- Issue #888: Backfill path_json from raw_hex ---

func TestBackfillPathJsonFromRawHex(t *testing.T) {
	dbPath := tempDBPath(t)
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Insert a transmission with payload_type != TRACE (e.g. 0x01)
	// raw_hex: header 0x05 (route FLOOD, payload 0x01), path byte 0x42 (hash_size=2, count=2),
	// hops: AABB, CCDD, then some payload bytes
	rawHex := "0542AABBCCDD0000000000000000000000000000"
	s.db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type) VALUES (?, 'h1', '2025-01-01T00:00:00Z', 1)`, rawHex)

	// Insert observation with raw_hex but empty path_json
	s.db.Exec(`INSERT INTO observations (transmission_id, timestamp, raw_hex, path_json) VALUES (1, 1000, ?, '[]')`, rawHex)
	// Insert observation with raw_hex and NULL path_json
	s.db.Exec(`INSERT INTO observations (transmission_id, timestamp, raw_hex, path_json) VALUES (1, 1001, ?, NULL)`, rawHex)
	// Insert observation with existing path_json (should NOT be overwritten)
	s.db.Exec(`INSERT INTO observations (transmission_id, timestamp, raw_hex, path_json) VALUES (1, 1002, ?, '["XX","YY"]')`, rawHex)

	// Insert a TRACE transmission (payload_type = 0x09) — should be skipped
	traceRaw := "2604302D0D2359FEE7B100000000006733D63367"
	s.db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type) VALUES (?, 'h2', '2025-01-01T00:00:00Z', 9)`, traceRaw)
	s.db.Exec(`INSERT INTO observations (transmission_id, timestamp, raw_hex, path_json) VALUES (2, 1003, ?, '[]')`, traceRaw)

	// Remove the migration marker so it runs again on reopen
	s.db.Exec(`DELETE FROM _migrations WHERE name = 'backfill_path_json_from_raw_hex_v1'`)
	s.Close()

	// Reopen — backfill is now async, must trigger explicitly
	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	// Trigger async backfill and wait for completion
	s2.BackfillPathJSONAsync()
	deadline := time.Now().Add(10 * time.Second)
	var migCount int
	for time.Now().Before(deadline) {
		s2.db.QueryRow("SELECT COUNT(*) FROM _migrations WHERE name = 'backfill_path_json_from_raw_hex_v1'").Scan(&migCount)
		if migCount == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if migCount != 1 {
		t.Fatalf("migration not recorded")
	}

	// Row 1 (was '[]') is NOT re-processed by the backfill — '[]' means
	// "already attempted, no hops" and is excluded by the WHERE to avoid the
	// infinite-loop bug fixed in #1119. It must remain '[]'.
	var pj1 string
	s2.db.QueryRow("SELECT path_json FROM observations WHERE id = 1").Scan(&pj1)
	if pj1 != "[]" {
		t.Errorf("row 1 path_json = %q, want %q (must not re-process '[]' rows after #1119)", pj1, "[]")
	}

	// Row 2 (was NULL) should now have decoded hops
	var pj2 string
	s2.db.QueryRow("SELECT path_json FROM observations WHERE id = 2").Scan(&pj2)
	if pj2 != `["AABB","CCDD"]` {
		t.Errorf("row 2 path_json = %q, want %q", pj2, `["AABB","CCDD"]`)
	}

	// Row 3 (had existing data) should NOT be overwritten
	var pj3 string
	s2.db.QueryRow("SELECT path_json FROM observations WHERE id = 3").Scan(&pj3)
	if pj3 != `["XX","YY"]` {
		t.Errorf("row 3 path_json = %q, want %q (should not be overwritten)", pj3, `["XX","YY"]`)
	}

	// Row 4 (TRACE) should NOT be updated
	var pj4 string
	s2.db.QueryRow("SELECT path_json FROM observations WHERE id = 4").Scan(&pj4)
	if pj4 != "[]" {
		t.Errorf("row 4 (TRACE) path_json = %q, want %q (should be skipped)", pj4, "[]")
	}
}

func TestCleanupLegacyNullHashTimestamp(t *testing.T) {
	path := tempDBPath(t)

	// Create a bare-bones DB with legacy bad data
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS transmissions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		raw_hex TEXT NOT NULL,
		hash TEXT NOT NULL,
		first_seen TEXT NOT NULL,
		route_type INTEGER,
		payload_type INTEGER,
		payload_version INTEGER,
		decoded_json TEXT,
		created_at TEXT DEFAULT (datetime('now')),
		channel_hash TEXT DEFAULT NULL
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS observations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
		observer_idx INTEGER,
		direction TEXT,
		snr REAL,
		rssi REAL,
		score INTEGER,
		path_json TEXT,
		timestamp INTEGER NOT NULL
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, advert_count INTEGER DEFAULT 0, battery_mv INTEGER, temperature_c REAL)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS observers (id TEXT PRIMARY KEY, name TEXT, iata TEXT, last_seen TEXT, first_seen TEXT, packet_count INTEGER DEFAULT 0, model TEXT, firmware TEXT, client_version TEXT, radio TEXT, battery_mv INTEGER, uptime_secs INTEGER, noise_floor REAL, inactive INTEGER DEFAULT 0, last_packet_at TEXT DEFAULT NULL)`)

	// Insert good transmission
	db.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, 'aabb', 'abc123', '2024-01-01T00:00:00Z')`)
	db.Exec(`INSERT INTO observations (transmission_id, observer_idx, timestamp) VALUES (1, 1, 1704067200)`)

	// Insert bad: empty hash
	db.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (2, 'ccdd', '', '2024-01-01T00:00:00Z')`)
	db.Exec(`INSERT INTO observations (transmission_id, observer_idx, timestamp) VALUES (2, 1, 1704067200)`)

	// Insert bad: empty first_seen
	db.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (3, 'eeff', 'def456', '')`)
	db.Exec(`INSERT INTO observations (transmission_id, observer_idx, timestamp) VALUES (3, 2, 1704067200)`)

	db.Close()

	// Now open via OpenStore which should run the migration
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Good transmission should remain
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions WHERE id = 1").Scan(&count)
	if count != 1 {
		t.Error("good transmission should not be deleted")
	}

	// Bad transmissions should be gone
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions WHERE id = 2").Scan(&count)
	if count != 0 {
		t.Errorf("transmission with empty hash should be deleted, got count=%d", count)
	}
	s.db.QueryRow("SELECT COUNT(*) FROM transmissions WHERE id = 3").Scan(&count)
	if count != 0 {
		t.Errorf("transmission with empty first_seen should be deleted, got count=%d", count)
	}

	// Observations for bad transmissions should be gone
	s.db.QueryRow("SELECT COUNT(*) FROM observations WHERE transmission_id IN (2, 3)").Scan(&count)
	if count != 0 {
		t.Errorf("observations for bad transmissions should be deleted, got count=%d", count)
	}

	// Observation for good transmission should remain
	s.db.QueryRow("SELECT COUNT(*) FROM observations WHERE transmission_id = 1").Scan(&count)
	if count != 1 {
		t.Error("observation for good transmission should remain")
	}

	// Migration marker should exist
	var migCount int
	s.db.QueryRow("SELECT COUNT(*) FROM _migrations WHERE name = 'cleanup_legacy_null_hash_ts'").Scan(&migCount)
	if migCount != 1 {
		t.Error("migration marker cleanup_legacy_null_hash_ts should be recorded")
	}

	// Idempotent: opening again should not error
	s.Close()
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal("second open should not fail:", err)
	}
	s2.Close()
}

func TestBuildPacketDataRegionFromPayload(t *testing.T) {
	msg := &MQTTPacketMessage{Raw: "0102030405060708", Region: "PDX"}
	decoded := &DecodedPacket{
		Header: Header{RouteType: 1, PayloadType: 3},
	}
	pkt := BuildPacketData(msg, decoded, "obs1", "SJC", nil)
	// When payload has region, it should override the topic-derived region
	if pkt.Region != "PDX" {
		t.Fatalf("expected region PDX from payload, got %q", pkt.Region)
	}
}

func TestBuildPacketDataRegionFallsBackToTopic(t *testing.T) {
	msg := &MQTTPacketMessage{Raw: "0102030405060708"}
	decoded := &DecodedPacket{
		Header: Header{RouteType: 1, PayloadType: 3},
	}
	pkt := BuildPacketData(msg, decoded, "obs1", "SJC", nil)
	if pkt.Region != "SJC" {
		t.Fatalf("expected region SJC from topic, got %q", pkt.Region)
	}
}

// TestBackfillPathJSONAsync verifies that the path_json backfill does NOT block
// OpenStore from returning. MQTT connect happens immediately after OpenStore;
// if the backfill is synchronous, MQTT would be delayed indefinitely on large DBs.
// This test creates pending backfill rows, opens the store, and asserts that
// OpenStore returns before the migration is recorded — proving async execution.
func TestBackfillPathJSONAsync(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "async_test.db")

	// Bootstrap schema manually so we can insert test data BEFORE OpenStore
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	// Create tables manually (minimal schema for this test)
	_, err = db.Exec(`
		CREATE TABLE _migrations (name TEXT PRIMARY KEY);
		CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT,
			created_at TEXT DEFAULT (datetime('now')),
			channel_hash TEXT
		);
		CREATE TABLE observers (
			id TEXT PRIMARY KEY,
			name TEXT,
			iata TEXT,
			last_seen TEXT,
			first_seen TEXT,
			packet_count INTEGER DEFAULT 0,
			model TEXT,
			firmware TEXT,
			client_version TEXT,
			radio TEXT,
			battery_mv INTEGER,
			uptime_secs INTEGER,
			noise_floor REAL,
			inactive INTEGER DEFAULT 0,
			last_packet_at TEXT
		);
		CREATE TABLE nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT, role TEXT, lat REAL, lon REAL,
			last_seen TEXT, first_seen TEXT, advert_count INTEGER DEFAULT 0,
			battery_mv INTEGER, temperature_c REAL
		);
		CREATE TABLE inactive_nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT, role TEXT, lat REAL, lon REAL,
			last_seen TEXT, first_seen TEXT, advert_count INTEGER DEFAULT 0,
			battery_mv INTEGER, temperature_c REAL
		);
		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
			observer_idx INTEGER,
			direction TEXT,
			snr REAL, rssi REAL, score INTEGER,
			path_json TEXT,
			timestamp INTEGER NOT NULL,
			raw_hex TEXT
		);
		CREATE UNIQUE INDEX idx_observations_dedup ON observations(transmission_id, observer_idx, COALESCE(path_json, ''));
		CREATE INDEX idx_observations_transmission_id ON observations(transmission_id);
		CREATE INDEX idx_observations_observer_idx ON observations(observer_idx);
		CREATE INDEX idx_observations_timestamp ON observations(timestamp);
		CREATE TABLE observer_metrics (
			observer_id TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			noise_floor REAL, tx_air_secs INTEGER, rx_air_secs INTEGER,
			recv_errors INTEGER, battery_mv INTEGER,
			packets_sent INTEGER, packets_recv INTEGER,
			PRIMARY KEY (observer_id, timestamp)
		);
		CREATE TABLE dropped_packets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			hash TEXT, raw_hex TEXT, reason TEXT NOT NULL,
			observer_id TEXT, observer_name TEXT,
			node_pubkey TEXT, node_name TEXT,
			dropped_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		t.Fatal("bootstrap schema:", err)
	}

	// Mark all migrations as done EXCEPT the path_json backfill
	for _, m := range []string{
		"advert_count_unique_v1", "noise_floor_real_v1", "node_telemetry_v1",
		"obs_timestamp_index_v1", "observer_metrics_v1", "observer_metrics_ts_idx",
		"observers_inactive_v1", "observer_metrics_packets_v1", "channel_hash_v1",
		"dropped_packets_v1", "observations_raw_hex_v1", "observers_last_packet_at_v1",
		"cleanup_legacy_null_hash_ts",
	} {
		db.Exec(`INSERT INTO _migrations (name) VALUES (?)`, m)
	}

	// Insert a transmission + observations with NULL path_json and valid raw_hex
	// raw_hex "0102AABBCCDD0000" has 2-hop path decodable by packetpath
	rawHex := "41020304AABBCCDD05060708"
	_, err = db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type) VALUES (?, 'hash1', '2025-01-01T00:00:00Z', 4)`, rawHex)
	if err != nil {
		t.Fatal("insert tx:", err)
	}
	// Insert 100 observations needing backfill
	for i := 0; i < 100; i++ {
		_, err = db.Exec(`INSERT INTO observations (transmission_id, observer_idx, timestamp, raw_hex, path_json) VALUES (1, ?, ?, ?, NULL)`,
			i+1, 1700000000+i, rawHex)
		if err != nil {
			// dedup index might fire — use unique observer_idx
			t.Fatalf("insert obs %d: %v", i, err)
		}
	}
	db.Close()

	// Now open store via OpenStore — this must return QUICKLY (non-blocking)
	start := time.Now()
	store, err := OpenStoreWithInterval(dbPath, 300)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal("OpenStore:", err)
	}
	defer store.Close()

	// OpenStore must return in under 2 seconds (backfill is no longer in applySchema)
	if elapsed > 2*time.Second {
		t.Fatalf("OpenStore blocked for %v — backfill must not run in applySchema", elapsed)
	}

	// Backfill must NOT be recorded yet — it hasn't been triggered
	var done int
	err = store.db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'backfill_path_json_from_raw_hex_v1'").Scan(&done)
	if err == nil {
		t.Fatal("migration recorded during OpenStore — backfill must be async via BackfillPathJSONAsync()")
	}

	// Now trigger the async backfill (simulates what main.go does after OpenStore)
	store.BackfillPathJSONAsync()

	// Wait for backfill to complete (should be very fast with 100 rows)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		err = store.db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'backfill_path_json_from_raw_hex_v1'").Scan(&done)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("backfill never completed within 10s")
	}

	// Verify backfill actually worked — observations should have non-NULL path_json
	var nullCount int
	store.db.QueryRow("SELECT COUNT(*) FROM observations WHERE path_json IS NULL").Scan(&nullCount)
	if nullCount > 0 {
		t.Errorf("backfill left %d observations with NULL path_json", nullCount)
	}
}

// TestBackfillPathJSONAsyncMethodExists verifies the async backfill API surface
// exists — BackfillPathJSONAsync must be callable independently from OpenStore.
func TestBackfillPathJSONAsyncMethodExists(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "method_test.db")
	store, err := OpenStoreWithInterval(dbPath, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// BackfillPathJSONAsync must exist as a method on *Store
	// This is a compile-time check — if the method doesn't exist, the test won't compile.
	store.BackfillPathJSONAsync()
}

// TestBackfillPathJSONAsync_BracketRowsTerminate exercises the infinite-loop bug
// from issue #1119. Observations whose path_json is already '[]' (meaning a prior
// backfill pass attempted to decode them and found no hops) must NOT be re-selected
// by the WHERE clause — otherwise the loop rewrites the same '[]' value forever
// and never records the migration marker.
//
// This test seeds N rows with path_json='[]' and a raw_hex that DecodePathFromRawHex
// resolves to zero hops. With the bug, the backfill loops infinitely re-UPDATEing
// the same rows back to '[]', batch is never empty, migration marker is never
// written. With the fix, no rows match → the very first batch is empty → migration
// is recorded immediately.
func TestBackfillPathJSONAsync_BracketRowsTerminate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bracket_terminate.db")

	// Bootstrap a minimal schema directly so we can seed pre-existing '[]' rows
	// before OpenStore runs.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE _migrations (name TEXT PRIMARY KEY);
		CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT,
			created_at TEXT DEFAULT (datetime('now')),
			channel_hash TEXT
		);
		CREATE TABLE observers (
			id TEXT PRIMARY KEY, name TEXT, iata TEXT,
			last_seen TEXT, first_seen TEXT, packet_count INTEGER DEFAULT 0,
			model TEXT, firmware TEXT, client_version TEXT, radio TEXT,
			battery_mv INTEGER, uptime_secs INTEGER, noise_floor REAL,
			inactive INTEGER DEFAULT 0, last_packet_at TEXT
		);
		CREATE TABLE nodes (
			public_key TEXT PRIMARY KEY, name TEXT, role TEXT,
			lat REAL, lon REAL, last_seen TEXT, first_seen TEXT,
			advert_count INTEGER DEFAULT 0, battery_mv INTEGER, temperature_c REAL
		);
		CREATE TABLE inactive_nodes (
			public_key TEXT PRIMARY KEY, name TEXT, role TEXT,
			lat REAL, lon REAL, last_seen TEXT, first_seen TEXT,
			advert_count INTEGER DEFAULT 0, battery_mv INTEGER, temperature_c REAL
		);
		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
			observer_idx INTEGER, direction TEXT,
			snr REAL, rssi REAL, score INTEGER,
			path_json TEXT,
			timestamp INTEGER NOT NULL,
			raw_hex TEXT
		);
		CREATE UNIQUE INDEX idx_observations_dedup ON observations(transmission_id, observer_idx, COALESCE(path_json, ''));
		CREATE INDEX idx_observations_transmission_id ON observations(transmission_id);
		CREATE INDEX idx_observations_observer_idx ON observations(observer_idx);
		CREATE INDEX idx_observations_timestamp ON observations(timestamp);
		CREATE TABLE observer_metrics (
			observer_id TEXT NOT NULL, timestamp TEXT NOT NULL,
			noise_floor REAL, tx_air_secs INTEGER, rx_air_secs INTEGER,
			recv_errors INTEGER, battery_mv INTEGER,
			packets_sent INTEGER, packets_recv INTEGER,
			PRIMARY KEY (observer_id, timestamp)
		);
		CREATE TABLE dropped_packets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			hash TEXT, raw_hex TEXT, reason TEXT NOT NULL,
			observer_id TEXT, observer_name TEXT,
			node_pubkey TEXT, node_name TEXT,
			dropped_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		t.Fatal("bootstrap schema:", err)
	}

	// Mark all migrations done EXCEPT backfill_path_json_from_raw_hex_v1.
	for _, m := range []string{
		"advert_count_unique_v1", "noise_floor_real_v1", "node_telemetry_v1",
		"obs_timestamp_index_v1", "observer_metrics_v1", "observer_metrics_ts_idx",
		"observers_inactive_v1", "observer_metrics_packets_v1", "channel_hash_v1",
		"dropped_packets_v1", "observations_raw_hex_v1", "observers_last_packet_at_v1",
		"cleanup_legacy_null_hash_ts",
	} {
		db.Exec(`INSERT INTO _migrations (name) VALUES (?)`, m)
	}

	// raw_hex producing ZERO hops via DecodePathFromRawHex:
	// DIRECT route (type=2), payload_type=2, version=0 → header 0x0A; path byte 0x00.
	// (See internal/packetpath/path_test.go: TestDecodePathFromRawHex_ZeroHops.)
	rawHex := "0A00DEADBEEF"
	_, err = db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type) VALUES (?, 'h_brackets', '2025-01-01T00:00:00Z', 2)`, rawHex)
	if err != nil {
		t.Fatal("insert tx:", err)
	}
	const seedCount = 100
	for i := 0; i < seedCount; i++ {
		_, err = db.Exec(`INSERT INTO observations (transmission_id, observer_idx, timestamp, raw_hex, path_json) VALUES (1, ?, ?, ?, '[]')`,
			i+1, 1700000000+i, rawHex)
		if err != nil {
			t.Fatalf("insert obs %d: %v", i, err)
		}
	}
	db.Close()

	store, err := OpenStoreWithInterval(dbPath, 300)
	if err != nil {
		t.Fatal("OpenStore:", err)
	}
	defer store.Close()

	// Trigger backfill. With the bug, every iteration re-fetches all 100 rows
	// (because '[]' matches the WHERE), rewrites them to '[]', sleeps 50ms, repeats.
	// The loop never terminates and the migration marker is never written.
	store.BackfillPathJSONAsync()

	// Generous deadline: with the fix the marker is written essentially immediately.
	// With the bug the marker is never written within any bounded time.
	deadline := time.Now().Add(5 * time.Second)
	var done int
	for time.Now().Before(deadline) {
		err = store.db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'backfill_path_json_from_raw_hex_v1'").Scan(&done)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("issue #1119: backfill never recorded migration marker within 5s — infinite loop on path_json='[]' rows")
	}

	// Verify the seeded '[]' rows still have '[]' (sanity — neither bug nor fix
	// should change their value), and that there are no NULL/empty path_json rows
	// the backfill should have processed.
	var bracketCount int
	store.db.QueryRow("SELECT COUNT(*) FROM observations WHERE path_json = '[]'").Scan(&bracketCount)
	if bracketCount != seedCount {
		t.Errorf("expected %d rows with path_json='[]', got %d", seedCount, bracketCount)
	}
}

// TestSchemaMultibyteSupColumns verifies that the multibyte_sup_v1 migration adds
// the expected columns and is idempotent across multiple OpenStore calls.
func TestSchemaMultibyteSupColumns(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	for _, table := range []string{"nodes", "inactive_nodes"} {
		rows, err := store.db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", table, err)
		}
		var foundSup, foundEvid bool
		for rows.Next() {
			var cid int
			var name, colType string
			var notNull, pk int
			var dflt interface{}
			if rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk) == nil {
				if name == "multibyte_sup" {
					foundSup = true
				}
				if name == "multibyte_evidence" {
					foundEvid = true
				}
			}
		}
		rows.Close()
		if !foundSup {
			t.Errorf("table %s: multibyte_sup column missing", table)
		}
		if !foundEvid {
			t.Errorf("table %s: multibyte_evidence column missing", table)
		}
	}

	// Verify migration is present. As of #1324 follow-up the migration
	// lives in internal/dbschema (column-probe + idempotent ALTER), not
	// in the legacy _migrations marker table — so we just re-assert the
	// columns exist and the second OpenStore is a no-op.
	store.Close()
	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore (second open): %v", err)
	}
	store2.Close()
}

// TestUpdateNodeDefaultScope_EmptyScopeIsNoop is the DB-layer defense-in-depth
// regression test for #1534. Even if the call-site guard at main.go:720 is
// later removed or refactored, the DB function MUST refuse to overwrite a
// previously-correct default_scope with the empty string. This is the
// belt-and-braces guard recommended by adversarial review (MAJOR-2) and
// dijkstra review (MINOR-2).
func TestUpdateNodeDefaultScope_EmptyScopeIsNoop(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(`INSERT INTO nodes (public_key, name, default_scope) VALUES ('pk1', 'Node1', '#belgium')`); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO inactive_nodes (public_key, name, default_scope) VALUES ('pk1', 'Node1', '#belgium')`); err != nil {
		t.Fatalf("insert inactive node: %v", err)
	}

	// Empty-scope call must be a silent no-op (return nil), NOT overwrite.
	if err := store.UpdateNodeDefaultScope("pk1", ""); err != nil {
		t.Fatalf("UpdateNodeDefaultScope(\"\") returned error: %v (want nil)", err)
	}

	var got string
	if err := store.db.QueryRow(`SELECT default_scope FROM nodes WHERE public_key = 'pk1'`).Scan(&got); err != nil {
		t.Fatalf("read nodes.default_scope: %v", err)
	}
	if got != "#belgium" {
		t.Errorf("nodes.default_scope after empty-scope call = %q, want #belgium (DB-layer guard missing — #1534)", got)
	}
	var gotInactive string
	if err := store.db.QueryRow(`SELECT default_scope FROM inactive_nodes WHERE public_key = 'pk1'`).Scan(&gotInactive); err != nil {
		t.Fatalf("read inactive_nodes.default_scope: %v", err)
	}
	if gotInactive != "#belgium" {
		t.Errorf("inactive_nodes.default_scope after empty-scope call = %q, want #belgium (DB-layer guard missing — #1534)", gotInactive)
	}
}

func TestInsertClientRxObservation(t *testing.T) {
	s := newTestStore(t)
	code1, fwd := "AABB", "1a2b"
	snr, rssi, posAccM := -7.5, -92, 8.5
	o := &ClientRxObservation{
		RxPubkey: "aa11", RxAt: "2026-08-17T10:00:00.123Z", IngestedAt: "2026-08-17T10:00:01Z",
		PktHash: "0123456789abcdef", RouteType: 0, PayloadType: 4,
		HashSize: 2, HopCount: 1, Code1: &code1, Forwarder: &fwd,
		Lat: 51.2, Lon: 4.4, SNR: &snr, RSSI: &rssi, PosAccM: &posAccM,
	}
	ins, err := s.InsertClientRxObservation(o)
	if err != nil || !ins {
		t.Fatalf("insert: ins=%v err=%v", ins, err)
	}
	// Geometry/signal round-trip: lat/lon/snr/rssi/pos_acc_m are never read back
	// anywhere else in the suite, so a field swap inside the 18-argument Exec
	// (e.g. lat<->lon) would pass green. lat and lon are deliberately unequal so
	// a swap is detectable.
	var gotLat, gotLon, gotSNR, gotPosAccM float64
	var gotRSSI int
	if err := s.db.QueryRow(`SELECT lat, lon, snr, rssi, pos_acc_m FROM client_rx_observations WHERE pkt_hash = ?`, o.PktHash).
		Scan(&gotLat, &gotLon, &gotSNR, &gotRSSI, &gotPosAccM); err != nil {
		t.Fatalf("read back geometry: %v", err)
	}
	if gotLat != 51.2 || gotLon != 4.4 {
		t.Errorf("lat/lon = %v/%v, want 51.2/4.4 (a lat<->lon swap would pass with symmetric values)", gotLat, gotLon)
	}
	if gotSNR != -7.5 {
		t.Errorf("snr = %v, want -7.5", gotSNR)
	}
	if gotRSSI != -92 {
		t.Errorf("rssi = %v, want -92", gotRSSI)
	}
	if gotPosAccM != 8.5 {
		t.Errorf("pos_acc_m = %v, want 8.5", gotPosAccM)
	}
	// Same (rx_pubkey, pkt_hash, rx_at) → idempotent, no second row.
	ins, err = s.InsertClientRxObservation(o)
	if err != nil || ins {
		t.Fatalf("re-insert: ins=%v err=%v (want false, nil)", ins, err)
	}
	// A second copy of the same flood from another forwarder, 40 ms later,
	// MUST create its own row — that multiplicity is the amplification signal.
	fwd2 := "3c4d"
	o2 := *o
	o2.RxAt, o2.Forwarder = "2026-08-17T10:00:00.163Z", &fwd2
	if ins, err = s.InsertClientRxObservation(&o2); err != nil || !ins {
		t.Fatalf("second copy: ins=%v err=%v", ins, err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rx_observations WHERE pkt_hash = ?`, o.PktHash).Scan(&n)
	if n != 2 {
		t.Errorf("rows for pkt_hash = %d, want 2", n)
	}
}

// NOTE: a TestTransportCodesV1MigrationFailsClosed regression test (forcing a
// genuine ALTER failure to prove the migration doesn't record its guard row
// on error) was attempted and removed — see final-fix-report.md for why the
// failure mode isn't reachable from a black-box test of applySchema without
// contorting production code. The fix itself (db.go, transport_codes_v1
// block) still stands, matching the observers_clock_naive_v1 pattern.

func TestInsertClientRfSample(t *testing.T) {
	s := newTestStore(t)
	posAcc := 12.5
	snr := -6.75
	errs := 7
	nf := -119
	rssi := -92
	battery := 4021
	queueLen := 3
	txAir := int64(341)
	rxAir := int64(20877)
	recv := int64(18422)
	sent := int64(290)
	floodRx := int64(17110)
	directRx := int64(1312)
	floodTx := int64(212)
	directTx := int64(78)
	o := &ClientRfSample{
		RxPubkey: "aa11", SampledAt: "2026-08-17T10:00:00.000Z", IngestedAt: "2026-08-17T10:00:01Z",
		Lat: 51.2, Lon: 4.4, PosAccM: &posAcc, Stationary: true, UptimeSecs: 84213,
		BatteryMV: &battery, QueueLen: &queueLen, Errors: &errs, NoiseFloor: &nf, LastRSSI: &rssi,
		LastSNR: &snr, TxAirSecs: &txAir, RxAirSecs: &rxAir, Recv: &recv, Sent: &sent,
		FloodRx: &floodRx, DirectRx: &directRx, FloodTx: &floodTx, DirectTx: &directTx,
	}
	ins, err := s.InsertClientRfSample(o)
	if err != nil || !ins {
		t.Fatalf("insert: ins=%v err=%v", ins, err)
	}
	ins, err = s.InsertClientRfSample(o)
	if err != nil || ins {
		t.Fatalf("duplicate must be idempotent: ins=%v err=%v", ins, err)
	}

	// Every column gets a distinct, non-symmetric value and is read back, so a
	// positional transposition between any two same-typed neighbours in the
	// 23-column INSERT (e.g. flood_rx/direct_rx, or recv/sent) is detectable —
	// row-counting alone would pass either way.
	var gotLat, gotLon, gotPosAccM, gotLastSNR float64
	var gotStationary, gotErrors, gotNoiseFloor, gotLastRSSI, gotBatteryMV, gotQueueLen int
	var gotUptimeSecs, gotTxAirSecs, gotRxAirSecs, gotRecv, gotSent, gotFloodRx, gotDirectRx, gotFloodTx, gotDirectTx int64
	var gotRecvErrors *int64
	if err := s.db.QueryRow(`
		SELECT lat, lon, pos_acc_m, stationary, uptime_secs, battery_mv, queue_len,
		       errors, noise_floor, last_rssi, last_snr, tx_air_secs, rx_air_secs,
		       recv, sent, flood_rx, direct_rx, flood_tx, direct_tx, recv_errors
		FROM client_rf_samples WHERE sampled_at = ?`, o.SampledAt).Scan(
		&gotLat, &gotLon, &gotPosAccM, &gotStationary, &gotUptimeSecs, &gotBatteryMV, &gotQueueLen,
		&gotErrors, &gotNoiseFloor, &gotLastRSSI, &gotLastSNR, &gotTxAirSecs, &gotRxAirSecs,
		&gotRecv, &gotSent, &gotFloodRx, &gotDirectRx, &gotFloodTx, &gotDirectTx, &gotRecvErrors); err != nil {
		t.Fatalf("read: %v", err)
	}
	if gotLat != 51.2 || gotLon != 4.4 {
		t.Errorf("lat/lon = %v/%v, want 51.2/4.4 (a lat<->lon swap would pass with symmetric values)", gotLat, gotLon)
	}
	if gotPosAccM != 12.5 {
		t.Errorf("pos_acc_m = %v, want 12.5", gotPosAccM)
	}
	if gotStationary != 1 {
		t.Errorf("stationary = %d, want 1", gotStationary)
	}
	if gotUptimeSecs != 84213 {
		t.Errorf("uptime_secs = %d, want 84213", gotUptimeSecs)
	}
	if gotBatteryMV != 4021 {
		t.Errorf("battery_mv = %d, want 4021", gotBatteryMV)
	}
	if gotQueueLen != 3 {
		t.Errorf("queue_len = %d, want 3", gotQueueLen)
	}
	if gotErrors != 7 {
		t.Errorf("errors = %d, want 7", gotErrors)
	}
	if gotNoiseFloor != -119 {
		t.Errorf("noise_floor = %d, want -119", gotNoiseFloor)
	}
	if gotLastRSSI != -92 {
		t.Errorf("last_rssi = %d, want -92", gotLastRSSI)
	}
	if gotLastSNR != -6.75 {
		t.Errorf("last_snr = %v, want -6.75", gotLastSNR)
	}
	if gotTxAirSecs != 341 {
		t.Errorf("tx_air_secs = %d, want 341", gotTxAirSecs)
	}
	if gotRxAirSecs != 20877 {
		t.Errorf("rx_air_secs = %d, want 20877", gotRxAirSecs)
	}
	if gotRecv != 18422 {
		t.Errorf("recv = %d, want 18422", gotRecv)
	}
	if gotSent != 290 {
		t.Errorf("sent = %d, want 290", gotSent)
	}
	if gotFloodRx != 17110 {
		t.Errorf("flood_rx = %d, want 17110", gotFloodRx)
	}
	if gotDirectRx != 1312 {
		t.Errorf("direct_rx = %d, want 1312", gotDirectRx)
	}
	if gotFloodTx != 212 {
		t.Errorf("flood_tx = %d, want 212", gotFloodTx)
	}
	if gotDirectTx != 78 {
		t.Errorf("direct_tx = %d, want 78", gotDirectTx)
	}
	if gotRecvErrors != nil {
		t.Errorf("recv_errors = %v, want NULL on firmware that cannot count them", gotRecvErrors)
	}

	// A second sample, at a different sampled_at, proves recv_errors round-trips
	// non-NULL too — the first sample only covers the NULL direction.
	re := int64(55)
	o2 := &ClientRfSample{
		RxPubkey: "aa11", SampledAt: "2026-08-17T10:00:15.000Z", IngestedAt: "2026-08-17T10:00:16Z",
		Lat: 51.2, Lon: 4.4, Stationary: true, UptimeSecs: 84228, RecvErrors: &re,
	}
	if ins, err = s.InsertClientRfSample(o2); err != nil || !ins {
		t.Fatalf("insert second sample: ins=%v err=%v", ins, err)
	}
	var gotRecvErrors2 *int64
	if err := s.db.QueryRow(`SELECT recv_errors FROM client_rf_samples WHERE sampled_at = ?`, o2.SampledAt).
		Scan(&gotRecvErrors2); err != nil {
		t.Fatalf("read second sample: %v", err)
	}
	if gotRecvErrors2 == nil || *gotRecvErrors2 != 55 {
		t.Errorf("recv_errors = %v, want 55", gotRecvErrors2)
	}
}

// TestPruneClientRfSamplesUsesIndex verifies the RF-sample reaper's
// DELETE ... WHERE sampled_at < ? seeks idx_crf_prune rather than
// full-scanning under the writer lock, mirroring
// TestPruneClientRxObservationsUsesIndex for the sibling table. May pass
// immediately since idx_crf_prune already exists (Task 4) — in that case
// this is a regression guard.
func TestPruneClientRfSamplesUsesIndex(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN DELETE FROM client_rf_samples WHERE sampled_at < ?`, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_crf_prune") {
		t.Fatalf("retention DELETE should use idx_crf_prune, plan was:\n%s", plan)
	}
}

// TestPruneOldClientRfSamples verifies the RF-sample reaper deletes rows
// older than the window and keeps recent ones, and that days=0 disables it —
// mirroring TestPruneOldClientRxObservations for the sibling table. It also
// covers the case an RFC3339 (no-millisecond) cutoff gets wrong: sampled_at
// is stored via rxTimeMillisLayout, and a row sampled 500ms after the cutoff
// instant — chronologically newer, so it must survive — has a string form
// like "...T10:00:00.500Z" that sorts lexicographically BEFORE a bare
// "...T10:00:00Z" cutoff (since '.' is 0x2E and 'Z' is 0x5A). An
// RFC3339-formatted cutoff would therefore wrongly delete it; a
// millisecond-formatted cutoff correctly keeps it.
func TestPruneOldClientRfSamples(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	recent := now.AddDate(0, 0, -1).Format(rxTimeMillisLayout)
	old := now.AddDate(0, 0, -40).Format(rxTimeMillisLayout)
	cutoffInstant := now.AddDate(0, 0, -7)
	boundary := cutoffInstant.Add(500 * time.Millisecond).Format(rxTimeMillisLayout)

	mk := func(sampledAt string) *ClientRfSample {
		return &ClientRfSample{
			RxPubkey: "aa11", SampledAt: sampledAt, IngestedAt: sampledAt,
			Lat: 51.2, Lon: 4.4, UptimeSecs: 100,
		}
	}
	if _, err := s.InsertClientRfSample(mk(recent)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertClientRfSample(mk(old)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertClientRfSample(mk(boundary)); err != nil {
		t.Fatal(err)
	}

	if n, _ := s.PruneOldClientRfSamples(0); n != 0 {
		t.Fatalf("days=0 must be a no-op, got %d", n)
	}
	n, err := s.PruneOldClientRfSamples(7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 old sample pruned, got %d", n)
	}
	var remaining int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rf_samples`).Scan(&remaining)
	if remaining != 2 {
		t.Fatalf("expected 2 samples remaining (recent + boundary), got %d", remaining)
	}
	var boundarySurvived int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rf_samples WHERE sampled_at = ?`, boundary).Scan(&boundarySurvived)
	if boundarySurvived != 1 {
		t.Fatalf("boundary row (500ms after cutoff instant) must survive; an RFC3339 (no-ms) cutoff would wrongly delete it because '.' < 'Z' lexicographically — got %d", boundarySurvived)
	}
}
