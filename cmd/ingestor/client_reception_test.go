package main

import (
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// TestPruneOldClientReceptions verifies the retention reaper bounds the coverage
// tables: rows older than the window (and stale companion names) are deleted,
// recent ones kept, and days=0 disables it.
func TestPruneOldClientReceptions(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	recent := now.AddDate(0, 0, -1).Format(time.RFC3339)
	old := now.AddDate(0, 0, -40).Format(time.RFC3339)
	const companion2 = "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3"

	s.InsertClientReception(&ClientReception{RxPubkey: testCompanionPK, HeardKey: "aabbcc", HeardKeyLen: 3, Lat: 51, Lon: 3.7, RxAt: recent, IngestedAt: "x", Src: "rxlog"})
	s.InsertClientReception(&ClientReception{RxPubkey: testCompanionPK, HeardKey: "aabbcc", HeardKeyLen: 3, Lat: 51, Lon: 3.7, RxAt: old, IngestedAt: "x", Src: "rxlog"})
	s.UpsertClientObserver(testCompanionPK, "Fresh", recent)
	s.UpsertClientObserver(companion2, "Stale", old)

	if n, _ := s.PruneOldClientReceptions(0); n != 0 {
		t.Fatalf("days=0 must be a no-op, got %d", n)
	}
	n, err := s.PruneOldClientReceptions(7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 old reception pruned, got %d", n)
	}
	var recN, obsN int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&recN)
	s.db.QueryRow(`SELECT COUNT(*) FROM client_observers`).Scan(&obsN)
	if recN != 1 {
		t.Fatalf("expected 1 reception remaining (recent), got %d", recN)
	}
	if obsN != 1 {
		t.Fatalf("expected 1 observer remaining (fresh), got %d", obsN)
	}
}

// TestPruneClientRxObservationsUsesIndex verifies the diagnostic observations
// reaper's DELETE ... WHERE rx_at < ? seeks idx_cro_prune rather than
// full-scanning under the writer lock, mirroring
// TestClientReceptionsRetentionUsesRxAtIndex for client_receptions below. May
// pass immediately since idx_cro_prune already exists (Task 3) — in that case
// this is a regression guard.
func TestPruneClientRxObservationsUsesIndex(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN DELETE FROM client_rx_observations WHERE rx_at < ?`, "2026-01-01T00:00:00Z")
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
	if !strings.Contains(plan, "idx_cro_prune") {
		t.Fatalf("retention DELETE should use idx_cro_prune, plan was:\n%s", plan)
	}
}

// TestPruneOldClientRxObservations verifies the diagnostic observations reaper
// deletes rows older than the window and keeps recent ones, and that days=0
// disables it — mirroring TestPruneOldClientReceptions for the sibling table.
// It also covers the millisecond-boundary case an RFC3339 (no-millisecond)
// cutoff would get wrong, mirroring TestPruneOldClientRfSamples: rx_at is
// stored via rxTimeMillisLayout, and a row 500ms after the cutoff instant —
// chronologically newer, so it must survive — has a string form like
// "...T10:00:00.500Z" that sorts lexicographically BEFORE a bare
// "...T10:00:00Z" cutoff (since '.' is 0x2E and 'Z' is 0x5A). An
// RFC3339-formatted cutoff would therefore wrongly delete it; a
// millisecond-formatted cutoff (what PruneOldClientRxObservations uses today)
// correctly keeps it.
func TestPruneOldClientRxObservations(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	recent := now.AddDate(0, 0, -1).Format(rxTimeMillisLayout)
	old := now.AddDate(0, 0, -40).Format(rxTimeMillisLayout)
	cutoffInstant := now.AddDate(0, 0, -7)
	boundary := cutoffInstant.Add(500 * time.Millisecond).Format(rxTimeMillisLayout)

	mk := func(rxAt, pktHash string) *ClientRxObservation {
		return &ClientRxObservation{
			RxPubkey: testCompanionPK, RxAt: rxAt, IngestedAt: rxAt, PktHash: pktHash,
			RouteType: 1, PayloadType: 5, HashSize: 1, HopCount: 0, Lat: 51.0, Lon: 3.7,
		}
	}
	if _, err := s.InsertClientRxObservation(mk(recent, "hash-recent")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertClientRxObservation(mk(old, "hash-old")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertClientRxObservation(mk(boundary, "hash-boundary")); err != nil {
		t.Fatal(err)
	}

	if n, _ := s.PruneOldClientRxObservations(0); n != 0 {
		t.Fatalf("days=0 must be a no-op, got %d", n)
	}
	n, err := s.PruneOldClientRxObservations(7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 old observation pruned, got %d", n)
	}
	var remaining int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rx_observations`).Scan(&remaining)
	if remaining != 2 {
		t.Fatalf("expected 2 observations remaining (recent + boundary), got %d", remaining)
	}
	var recentSurvived int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rx_observations WHERE pkt_hash = ?`, "hash-recent").Scan(&recentSurvived)
	if recentSurvived != 1 {
		t.Fatalf("the recent row must survive the prune; got %d", recentSurvived)
	}
	var boundarySurvived int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rx_observations WHERE pkt_hash = ?`, "hash-boundary").Scan(&boundarySurvived)
	if boundarySurvived != 1 {
		t.Fatalf("boundary row (500ms after cutoff instant) must survive; an RFC3339 (no-ms) cutoff would wrongly delete it because '.' < 'Z' lexicographically — got %d", boundarySurvived)
	}
}

func TestClientReceptionsTableExists(t *testing.T) {
	s := newTestStore(t)
	cols := map[string]bool{}
	rows, err := s.db.Query(`PRAGMA table_info(client_receptions)`)
	if err != nil {
		t.Fatalf("PRAGMA failed: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, want := range []string{"id", "rx_pubkey", "heard_key", "heard_keylen", "rssi", "snr", "lat", "lon", "pos_acc_m", "rx_at", "ingested_at", "src"} {
		if !cols[want] {
			t.Errorf("missing column %q in client_receptions", want)
		}
	}
}

func crF(f float64) *float64 { return &f }
func crI(i int) *int         { return &i }

// TestClientReceptionsCoverageQueryUsesIndex verifies #5/#18: the dominant
// per-node coverage query (sargable heard_key IN-list + bbox, mirroring
// cmd/server coverageHeardKeyCandidates) seeks the heard_key composite index
// rather than scanning the table. Without idx_client_recept_heard_geo the plan
// is "SCAN client_receptions".
func TestClientReceptionsCoverageQueryUsesIndex(t *testing.T) {
	s := newTestStore(t)
	q := `EXPLAIN QUERY PLAN SELECT lat, lon, snr, rssi, heard_key, rx_at
		FROM client_receptions
		WHERE heard_key IN (?,?,?) AND lat BETWEEN ? AND ? AND lon BETWEEN ? AND ?`
	rows, err := s.db.Query(q, "aabbccddeeff00112233", "aabbcc", "aabb", 50.0, 52.0, 3.0, 4.0)
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
	if !strings.Contains(plan, "USING INDEX idx_client_recept") {
		t.Fatalf("coverage query should use a client_recept index, plan was:\n%s", plan)
	}
	if strings.Contains(plan, "SCAN client_receptions") {
		t.Fatalf("coverage query should not full-scan, plan was:\n%s", plan)
	}
}

// TestClientReceptionsRetentionUsesRxAtIndex verifies the retention reaper's
// DELETE ... WHERE rx_at < ? (and the leaderboard's rx_at window) seek the rx_at
// index rather than full-scanning under the writer lock (polish review).
func TestClientReceptionsRetentionUsesRxAtIndex(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN DELETE FROM client_receptions WHERE rx_at < ?`, "2026-01-01T00:00:00Z")
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
	if !strings.Contains(plan, "idx_client_recept_rxat") {
		t.Fatalf("retention DELETE should use idx_client_recept_rxat, plan was:\n%s", plan)
	}
}

// TestRxLeaderboardQueryIsIndexBacked pins the planner choice for the leaderboard
// SELECT (the rx_at-windowed, rx_pubkey-grouped query in cmd/server/rx_dashboard.go).
// SQLite serves it from the UNIQUE(rx_pubkey,heard_key,rx_at) constraint index as a
// COVERING scan (not idx_client_recept_rxat, and not a table-heap scan). The table
// is retention-bounded, so a covering scan is acceptable; this test guards against a
// silent regression to a bare table scan under the writer lock when the schema is
// next tweaked. Representative form (no JOINs — they don't change whether `cr` is
// index-backed).
func TestRxLeaderboardQueryIsIndexBacked(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN
		SELECT cr.rx_pubkey, COUNT(*), COUNT(DISTINCT cr.heard_key)
		FROM client_receptions cr
		WHERE cr.rx_at >= ?
		GROUP BY cr.rx_pubkey
		ORDER BY COUNT(*) DESC
		LIMIT ?`, "2026-01-01T00:00:00Z", 100)
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
	t.Logf("leaderboard plan:\n%s", plan)
	// The concern is a bare table-heap scan, not which specific index wins. The
	// plan must stay index-backed (covering or search) — a regression to a bare
	// "SCAN cr" without an index fails here.
	if !strings.Contains(plan, "INDEX") {
		t.Fatalf("leaderboard SELECT must stay index-backed (no full table-heap scan), plan was:\n%s", plan)
	}
}

func TestDeriveHeardKey(t *testing.T) {
	full := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	k, l, src, ok := deriveHeardKey("rx", packetpath.RouteFlood, PayloadADVERT, nil, strings.ToUpper(full), true)
	if !ok || l != 32 || src != "advert" || k != full {
		t.Fatalf("0-hop advert: got k=%q l=%d src=%q ok=%v", k, l, src, ok)
	}
	k, l, src, ok = deriveHeardKey("rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"aa", "bbccdd"}, "", false)
	if !ok || k != "bbccdd" || l != 3 || src != "rxlog" {
		t.Fatalf("flood path: got k=%q l=%d src=%q ok=%v", k, l, src, ok)
	}
	// DIRECT route: path[last] is the route's far end, not the transmitter — must be rejected.
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteDirect, PayloadGRP_TXT, []string{"aa", "bbccdd"}, "", false); ok {
		t.Fatalf("direct-route path must be rejected")
	}
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteTransportDirect, PayloadGRP_TXT, []string{"aa", "bbccdd"}, "", false); ok {
		t.Fatalf("transport-direct-route path must be rejected")
	}
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"aa", "bb"}, "", false); ok {
		t.Fatalf("1-byte last hop should be rejected")
	}
	if _, _, _, ok = deriveHeardKey("tx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"aabbcc"}, "", false); ok {
		t.Fatalf("tx must be rejected")
	}
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteFlood, PayloadGRP_TXT, nil, "", false); ok {
		t.Fatalf("no hops + non-advert must be rejected")
	}
	// TRACE repurposes the header path bytes as per-hop SNR values, not node
	// hashes — a FLOOD-routed TRACE must never be attributable, even though the
	// route type and hop shape are otherwise identical to the accepted case above.
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteFlood, PayloadTRACE, []string{"aa", "bbccdd"}, "", false); ok {
		t.Fatalf("FLOOD-routed TRACE must be rejected (path bytes are SNR values, not node hashes)")
	}
}

func TestBuildClientReception(t *testing.T) {
	acc := 8.0
	rec, ok := buildClientReception("companionpk", "rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"aa", "bbccdd"}, "", false,
		crF(-7.5), crI(-92), 51.05, 3.72, &acc, "2026-06-09T12:00:00Z", "2026-06-09T12:00:01Z")
	if !ok || rec.HeardKey != "bbccdd" || rec.HeardKeyLen != 3 || rec.Src != "rxlog" {
		t.Fatalf("bad reception: %+v ok=%v", rec, ok)
	}
	if _, ok := buildClientReception("c", "rx", packetpath.RouteDirect, PayloadGRP_TXT, []string{"bbccdd"}, "", false,
		crF(-7.5), crI(-92), 51.05, 3.72, nil, "t", "t"); ok {
		t.Fatal("direct-route path must be rejected (not the transmitter)")
	}
	if _, ok := buildClientReception("c", "rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"bbccdd"}, "", false, nil, nil, 99.0, 3.72, nil, "t", "t"); ok {
		t.Fatal("out-of-range lat must be rejected")
	}
	if _, ok := buildClientReception("c", "rx", packetpath.RouteFlood, PayloadTRACE, []string{"aa", "bbccdd"}, "", false,
		crF(-7.5), crI(-92), 51.05, 3.72, nil, "t", "t"); ok {
		t.Fatal("FLOOD-routed TRACE must be rejected (path bytes are SNR values, not node hashes)")
	}
}

func TestInsertClientReceptionRoundTripAndIdempotent(t *testing.T) {
	s := newTestStore(t)
	rec := &ClientReception{
		RxPubkey: "companionpk", HeardKey: "bbccdd", HeardKeyLen: 3, RSSI: crI(-92),
		Lat: 51.05, Lon: 3.72, RxAt: "2026-06-09T12:00:00Z", IngestedAt: "2026-06-09T12:00:01Z", Src: "rxlog",
	}
	if ins, err := s.InsertClientReception(rec); err != nil || !ins {
		t.Fatalf("first insert: ins=%v err=%v", ins, err)
	}
	if ins, err := s.InsertClientReception(rec); err != nil || ins {
		t.Fatalf("second insert should be a no-op: ins=%v err=%v", ins, err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 row, got %d", n)
	}
}

func TestHandleClientPacketRelayedAdvertWritesReception(t *testing.T) {
	s := newTestStore(t)
	advertHex := "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
	msg := map[string]interface{}{
		"raw":       advertHex,
		"direction": "rx",
		"timestamp": "2026-06-09T12:00:00Z",
		"origin":    "MyMob",
		"SNR":       -7.0,
		"RSSI":      -92.0,
		"gps":       map[string]interface{}{"lat": 51.05, "lon": 3.72, "acc_m": 8.0},
	}
	handleClientPacket(s, &Config{}, "test", testCompanionPK, msg, nil, nil)

	var obsName string
	s.db.QueryRow(`SELECT name FROM client_observers WHERE pubkey=?`, testCompanionPK).Scan(&obsName)
	if obsName != "MyMob" {
		t.Fatalf("expected client_observers name 'MyMob', got %q", obsName)
	}

	// This fixture is a relayed advert (non-empty path), so by the capture HARD
	// RULE we record the directly-heard LAST hop (multibyte), not the originator.
	// The 0-hop advert→full-pubkey branch is covered by TestDeriveHeardKey.
	var n, keylen int
	var src string
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(heard_keylen),0), COALESCE(MAX(src),'') FROM client_receptions WHERE rx_pubkey=?`, testCompanionPK).Scan(&n, &keylen, &src); err != nil {
		t.Fatal(err)
	}
	if n != 1 || keylen < 2 || src != "rxlog" {
		t.Fatalf("expected 1 rxlog reception (multibyte last hop), got n=%d keylen=%d src=%q", n, keylen, src)
	}

	// No GPS → no row.
	const companion2 = "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3"
	handleClientPacket(s, &Config{}, "test", companion2, map[string]interface{}{"raw": advertHex, "direction": "rx"}, nil, nil)
	var n2 int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions WHERE rx_pubkey=?`, companion2).Scan(&n2)
	if n2 != 0 {
		t.Fatalf("packet without gps must be dropped, got %d rows", n2)
	}
}

// TestHandleClientPacketZeroHopAdvertWritesReception covers the #9 gap: the
// advert fixture used above is a RELAYED advert (non-empty path), so it exercises
// the rxlog last-hop branch, not the 0-hop src='advert' branch. Here we rebuild
// the same advert with zero hops — header (FLOOD ADVERT) + "00" (0 hops) + the
// same advert payload — so handleClientPacket stores the advertiser by its full
// pubkey with src='advert', and we assert gps/snr were captured too.
func TestHandleClientPacketZeroHopAdvertWritesReception(t *testing.T) {
	s := newTestStore(t)
	relayed := "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
	// relayed = header(2) + path-descriptor(2) + 5*2-byte hops(20) + payload.
	payload := relayed[24:]
	zeroHop := "1100" + payload
	advertPubkey := strings.ToLower(payload[:64]) // advert payload starts with the 32-byte pubkey

	msg := map[string]interface{}{
		"raw": zeroHop, "direction": "rx", "timestamp": "2026-06-09T12:00:00Z",
		"origin": "MyMob", "SNR": -7.0, "RSSI": -92.0,
		"gps": map[string]interface{}{"lat": 51.05, "lon": 3.72, "acc_m": 8.0},
	}
	handleClientPacket(s, &Config{}, "test", testCompanionPK, msg, nil, nil)

	var heardKey, src string
	var keylen int
	var snr sql.NullFloat64
	var lat, lon float64
	if err := s.db.QueryRow(`SELECT heard_key, heard_keylen, src, snr, lat, lon FROM client_receptions WHERE rx_pubkey=?`, testCompanionPK).
		Scan(&heardKey, &keylen, &src, &snr, &lat, &lon); err != nil {
		t.Fatalf("expected a 0-hop advert reception: %v", err)
	}
	if src != "advert" || keylen != 32 || heardKey != advertPubkey {
		t.Fatalf("0-hop advert: want advert/32/%s, got %s/%d/%s", advertPubkey, src, keylen, heardKey)
	}
	if !snr.Valid || snr.Float64 != -7 || lat != 51.05 || lon != 3.72 {
		t.Fatalf("gps/snr not captured: snr=%v lat=%f lon=%f", snr, lat, lon)
	}
}

// TestHandleClientPacketRejectsNonHexPubkey verifies the #2 fix: a companion
// pubkey from the topic that isn't lowercase hex (a no-ACL broker could publish
// meshcore/client/!@#$/packets) writes nothing to either coverage table. Without
// the clientPubkeyRe guard this fixture would insert a polluting row.
// cfgWithObservations returns a Config with the observations gate on.
func cfgWithObservations() *Config {
	return &Config{ClientRxObservations: &ClientRxObservationsConfig{Enabled: true}}
}

// TestClientPacketWritesObservationNotCoverage verifies a DIRECT-route packet
// (not attributable per deriveHeardKey) still produces one diagnostic
// observation row while writing ZERO coverage rows — the two paths are
// independent.
func TestClientPacketWritesObservationNotCoverage(t *testing.T) {
	s := newTestStore(t)
	// header 0x16 = route_type 2 (DIRECT), payload_type 5 (GRP_TXT). DIRECT
	// carries no transport codes. path byte 0x41 = hash_size 2, hop_count 1,
	// then the 2-byte hop, then the payload.
	// deriveHeardKey refuses a DIRECT route, so this must produce an
	// observation row and ZERO coverage rows.
	raw := "16" + "41" + "1a2b" + strings.Repeat("33", 16)
	msg := map[string]interface{}{
		"raw": raw, "direction": "rx", "SNR": 4.5, "RSSI": -101.0,
		"timestamp": "2026-08-17T10:00:00.123Z",
		"gps":       map[string]interface{}{"lat": 51.2, "lon": 4.4, "acc_m": 8.0},
	}
	handleClientPacket(s, cfgWithObservations(), "test", "aa11", msg, nil, nil)

	var obs, cov int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rx_observations`).Scan(&obs)
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&cov)
	if obs != 1 {
		t.Errorf("observations = %d, want 1", obs)
	}
	if cov != 0 {
		t.Errorf("coverage rows = %d, want 0 — the invariant must hold", cov)
	}

	var hash string
	var hopCount, hashSize int
	var fwd sql.NullString
	s.db.QueryRow(`SELECT pkt_hash, hop_count, hash_size, forwarder FROM client_rx_observations`).
		Scan(&hash, &hopCount, &hashSize, &fwd)
	if hash != ComputeContentHash(raw) {
		t.Errorf("pkt_hash = %q, want ComputeContentHash value %q", hash, ComputeContentHash(raw))
	}
	if hopCount != 1 || hashSize != 2 {
		t.Errorf("hop_count/hash_size = %d/%d, want 1/2", hopCount, hashSize)
	}
	// path[last] on a DIRECT route is the route's far end, not who transmitted
	// (deriveHeardKey's "path[last] is flood-only" rule) — a production bug
	// once got this wrong, so pin it here: forwarder must stay NULL, not the
	// path's last hop.
	if fwd.Valid {
		t.Errorf("forwarder = %q, want NULL for a DIRECT route (path[last] is flood-only)", fwd.String)
	}
}

// TestClientPacketFloodWritesBoth verifies a FLOOD-route packet (attributable)
// writes both an observation row AND a coverage row, and that the observation's
// forwarder is path[last] — the immediate RF transmitter.
func TestClientPacketFloodWritesBoth(t *testing.T) {
	s := newTestStore(t)
	// header 0x15 = route_type 1 (FLOOD), payload_type 5. 2-byte hash, one hop
	// → path[last] is the transmitter, so this IS attributable.
	raw := "15" + "41" + "1a2b" + strings.Repeat("33", 16)
	msg := map[string]interface{}{
		"raw": raw, "direction": "rx", "SNR": 4.5, "RSSI": -101.0,
		"timestamp": "2026-08-17T10:00:00.123Z",
		"gps":       map[string]interface{}{"lat": 51.2, "lon": 4.4},
	}
	handleClientPacket(s, cfgWithObservations(), "test", "aa11", msg, nil, nil)

	var obs, cov int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rx_observations`).Scan(&obs)
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&cov)
	if obs != 1 || cov != 1 {
		t.Errorf("observations/coverage = %d/%d, want 1/1", obs, cov)
	}
	var fwd, pathJSON string
	s.db.QueryRow(`SELECT forwarder, path_json FROM client_rx_observations`).Scan(&fwd, &pathJSON)
	if fwd != "1a2b" {
		t.Errorf("forwarder = %q, want 1a2b", fwd)
	}
	// The decoder emits hops uppercase; path_json must be lowercased to match
	// forwarder (and every other identifier in this table), or a
	// json_extract(path_json,'$[...]') = forwarder join silently returns zero
	// rows instead of erroring.
	if pathJSON != `["1a2b"]` {
		t.Errorf("path_json = %q, want [\"1a2b\"] (lowercase, matching forwarder's case)", pathJSON)
	}
}

// TestClientObservationsGateOff verifies that with clientRxObservations
// disabled, a client packet writes zero observation rows.
func TestClientObservationsGateOff(t *testing.T) {
	s := newTestStore(t)
	raw := "16" + "41" + "1a2b" + strings.Repeat("33", 16)
	msg := map[string]interface{}{
		"raw": raw, "direction": "rx",
		"timestamp": "2026-08-17T10:00:00.123Z",
		"gps":       map[string]interface{}{"lat": 51.2, "lon": 4.4},
	}
	handleClientPacket(s, &Config{}, "test", "aa11", msg, nil, nil)

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rx_observations`).Scan(&n)
	if n != 0 {
		t.Errorf("rows = %d, want 0 when the gate is off", n)
	}
}

// TestClientObservationsTxDirectionSkipped verifies a companion's own
// outgoing transmission (direction "tx") writes zero observation rows, even
// though this FLOOD packet would otherwise be attributable (as
// TestClientPacketFloodWritesBoth proves for the same shape with "rx"). The
// observations gate sits above deriveHeardKey's own "rx" check, which runs
// only on the coverage path — without a matching check here, a companion's
// own transmissions would be recorded as observed RF and inflate the table's
// headline per-flood forwarder-multiplicity signal.
func TestClientObservationsTxDirectionSkipped(t *testing.T) {
	s := newTestStore(t)
	raw := "15" + "41" + "1a2b" + strings.Repeat("33", 16)
	msg := map[string]interface{}{
		"raw": raw, "direction": "tx",
		"timestamp": "2026-08-17T10:00:00.123Z",
		"gps":       map[string]interface{}{"lat": 51.2, "lon": 4.4},
	}
	handleClientPacket(s, cfgWithObservations(), "test", "aa11", msg, nil, nil)

	var obs, cov int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rx_observations`).Scan(&obs)
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&cov)
	if obs != 0 {
		t.Errorf("observations = %d, want 0 for a tx (self-transmission) packet", obs)
	}
	if cov != 0 {
		t.Errorf("coverage rows = %d, want 0 for a tx packet", cov)
	}
}

// TestClientObservationsMillisecondPrecisionAllowsDistinctRows is the
// regression test for the second-resolution rx_at bug: two client packets
// with the same raw and rx_pubkey, timestamps 40ms apart in the same second,
// must produce TWO observation rows, not one collapsed by
// UNIQUE(rx_pubkey, pkt_hash, rx_at) + ON CONFLICT DO NOTHING. resolveRxTime's
// RFC3339 (second-resolution) formatting would collapse these; the dedicated
// millisecond-precision rx_at for this table must not.
func TestClientObservationsMillisecondPrecisionAllowsDistinctRows(t *testing.T) {
	s := newTestStore(t)
	raw := "16" + "41" + "1a2b" + strings.Repeat("33", 16)
	// Derived from time.Now() (not hardcoded) so this test never crosses
	// resolveRxTimeCore's >30d-stale reject, which would replace rx_at with
	// ingest time and silently stop exercising the millisecond-distinctness
	// this test exists to pin.
	base := time.Now().UTC().Add(-time.Minute)
	ts1 := base.Format(rxTimeMillisLayout)
	ts2 := base.Add(40 * time.Millisecond).Format(rxTimeMillisLayout)
	baseMsg := map[string]interface{}{
		"raw": raw, "direction": "rx",
		"gps": map[string]interface{}{"lat": 51.2, "lon": 4.4},
	}
	msg1 := map[string]interface{}{"timestamp": ts1}
	msg2 := map[string]interface{}{"timestamp": ts2}
	for k, v := range baseMsg {
		msg1[k] = v
		msg2[k] = v
	}
	handleClientPacket(s, cfgWithObservations(), "test", "aa11", msg1, nil, nil)
	handleClientPacket(s, cfgWithObservations(), "test", "aa11", msg2, nil, nil)

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rx_observations`).Scan(&n)
	if n != 2 {
		t.Fatalf("observations = %d, want 2 — sub-second rx_at must keep both forwarder copies distinct", n)
	}
	var rxAts []string
	rows, err := s.db.Query(`SELECT rx_at FROM client_rx_observations ORDER BY rx_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var rxAt string
		if err := rows.Scan(&rxAt); err != nil {
			t.Fatal(err)
		}
		rxAts = append(rxAts, rxAt)
	}
	if rxAts[0] != ts1 || rxAts[1] != ts2 {
		t.Errorf("rx_at values = %v, want millisecond-distinct timestamps %v/%v", rxAts, ts1, ts2)
	}
}

// TestClientObservationScopeNameFromTransportCode covers code1/code2/scope_name
// — the field this whole table exists for ("which repeater forwards which
// scope"). Uses the same precomputed matchScope vector as main_test.go's
// TestMatchScope (key = SHA256("#test")[:16], payloadType=5, payload="hello"
// -> code1 2AB5) rather than inventing a vector, so a wrong HMAC algorithm
// cannot pass.
func TestClientObservationScopeNameFromTransportCode(t *testing.T) {
	s := newTestStore(t)
	testKey, err := hex.DecodeString("9cd8fcf22a47333b591d96a2b848b73f") // SHA256("#test")[:16]
	if err != nil {
		t.Fatal(err)
	}
	regionKeys := map[string][]byte{"#test": testKey}
	helloHex := hex.EncodeToString([]byte("hello"))

	// Derived from time.Now() (not hardcoded) so this test never crosses
	// resolveRxTimeCore's >30d-stale reject, which would replace rx_at with
	// ingest time and break the WHERE rx_at = ? lookups below.
	base := time.Now().UTC().Add(-time.Minute)
	ts1 := base.Format(rxTimeMillisLayout)
	ts2 := base.Add(time.Second).Format(rxTimeMillisLayout)

	// header 0x14 = route_type 0 (TRANSPORT_FLOOD), payload_type 5 (GRP_TXT).
	// Transport routes (0,3) carry code1/code2 right after the header, raw
	// wire hex uppercased with no byte swap: bytes 2A B5 -> "2AB5", AA BB ->
	// "AABB". path byte 0x00 = hash_size 1, hop_count 0 (no hops). Payload
	// bytes are "hello" to match the precomputed matchScope vector.
	raw := "14" + "2ab5" + "aabb" + "00" + helloHex
	msg := map[string]interface{}{
		"raw": raw, "direction": "rx",
		"timestamp": ts1,
		"gps":       map[string]interface{}{"lat": 51.2, "lon": 4.4},
	}
	handleClientPacket(s, cfgWithObservations(), "test", "aa11", msg, nil, regionKeys)

	// ComputeContentHash deliberately excludes the transport-code bytes (so the
	// same content dedups across scopes), so raw and raw2 below share one
	// pkt_hash — distinguish the two inserted rows by rx_at, not pkt_hash.
	var code1, code2, scopeName sql.NullString
	if err := s.db.QueryRow(`SELECT code1, code2, scope_name FROM client_rx_observations WHERE rx_at = ?`, ts1).
		Scan(&code1, &code2, &scopeName); err != nil {
		t.Fatal(err)
	}
	if !code1.Valid || code1.String != "2AB5" {
		t.Errorf("code1 = %v, want raw wire hex uppercased 2AB5 (bytes 2A B5, no byte swap)", code1)
	}
	if !code2.Valid || code2.String != "AABB" {
		t.Errorf("code2 = %v, want raw wire hex uppercased AABB (bytes AA BB, no byte swap)", code2)
	}
	if !scopeName.Valid || scopeName.String != "#test" {
		t.Errorf("scope_name = %v, want matched region #test", scopeName)
	}

	// Second packet: code1 "0000" is the real, non-scoped value — stored
	// literally, and the != "0000" guard must leave scope_name NULL rather
	// than attempt (and fail) a match.
	raw2 := "14" + "0000" + "aabb" + "00" + helloHex
	msg2 := map[string]interface{}{
		"raw": raw2, "direction": "rx",
		"timestamp": ts2,
		"gps":       map[string]interface{}{"lat": 51.2, "lon": 4.4},
	}
	handleClientPacket(s, cfgWithObservations(), "test", "aa11", msg2, nil, regionKeys)

	var code1b, scopeNameB sql.NullString
	if err := s.db.QueryRow(`SELECT code1, scope_name FROM client_rx_observations WHERE rx_at = ?`, ts2).
		Scan(&code1b, &scopeNameB); err != nil {
		t.Fatal(err)
	}
	if !code1b.Valid || code1b.String != "0000" {
		t.Errorf("code1 = %v, want literal 0000 (a real value, not NULL)", code1b)
	}
	if scopeNameB.Valid {
		t.Errorf("scope_name = %q, want NULL when code1=0000", scopeNameB.String)
	}
}

// TestHandleClientPacketFloodTraceWritesNoCoverage is the CRITICAL-1
// regression test: a FLOOD-routed TRACE (header 0x25 = payload_type 9 << 2 |
// route_type 1) repurposes the header path bytes as per-hop SNR values, not
// node hashes (decoder.go). Before the packetpath.PathBytesAreHops guard was
// wired into the client path, this shape yielded heardKey=<SNR bytes>,
// src="rxlog" and a phantom client_receptions row. It must now produce ZERO
// coverage rows, and the surviving diagnostic observation must carry NULL
// forwarder/path_json (the route type, codes and signal are still valid
// diagnostics, so the row itself is kept).
func TestHandleClientPacketFloodTraceWritesNoCoverage(t *testing.T) {
	s := newTestStore(t)
	// header 0x25 = route_type 1 (FLOOD), payload_type 9 (TRACE).
	// pathByte 0x41 = hash_size 2, hash_count 1 -> path bytes "1a2b" (SNR, not a hop hash).
	// payload: tag(4) + authCode(4) + flags(1) = 9 bytes, so decodeTrace succeeds cleanly.
	raw := "25" + "41" + "1a2b" + "aabbccdd1122334400"
	decoded, err := DecodePacket(raw, nil, false)
	if err != nil {
		t.Fatalf("fixture must decode: %v", err)
	}
	if decoded.Header.PayloadTypeName != "TRACE" || decoded.Header.RouteType != packetpath.RouteFlood {
		t.Fatalf("fixture sanity check failed: payloadType=%s routeType=%d", decoded.Header.PayloadTypeName, decoded.Header.RouteType)
	}
	if len(decoded.Path.Hops) != 1 || decoded.Path.Hops[0] != "1A2B" {
		t.Fatalf("fixture sanity check failed: hops=%v, want a 2-byte SNR entry decoded as a hop", decoded.Path.Hops)
	}

	msg := map[string]interface{}{
		"raw": raw, "direction": "rx", "SNR": 4.5, "RSSI": -101.0,
		"timestamp": "2026-08-17T10:00:00.123Z",
		"gps":       map[string]interface{}{"lat": 51.2, "lon": 4.4, "acc_m": 8.0},
	}
	handleClientPacket(s, cfgWithObservations(), "test", "aa11", msg, nil, nil)

	var cov int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&cov)
	if cov != 0 {
		t.Fatalf("coverage rows = %d, want 0 — a FLOOD-routed TRACE must never be attributable", cov)
	}

	var obs int
	var fwd, pathJSON sql.NullString
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM client_rx_observations`).Scan(&obs); err != nil {
		t.Fatal(err)
	}
	if obs != 1 {
		t.Fatalf("observations = %d, want 1 (the packet was genuinely heard; only forwarder/path_json must be NULL)", obs)
	}
	if err := s.db.QueryRow(`SELECT forwarder, path_json FROM client_rx_observations`).Scan(&fwd, &pathJSON); err != nil {
		t.Fatal(err)
	}
	if fwd.Valid {
		t.Errorf("forwarder = %q, want NULL for a TRACE packet (path bytes are SNR values, not a node hash)", fwd.String)
	}
	if pathJSON.Valid {
		t.Errorf("path_json = %q, want NULL for a TRACE packet (path bytes are SNR values, not a hop chain)", pathJSON.String)
	}
}

func TestHandleClientPacketRejectsNonHexPubkey(t *testing.T) {
	s := newTestStore(t)
	advertHex := "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
	for _, bad := range []string{"!@#$", "companionpk", "", "g0g0", "xyz"} {
		msg := map[string]interface{}{
			"raw": advertHex, "direction": "rx", "timestamp": "2026-06-09T12:00:00Z",
			"origin": "Spoof", "SNR": -7.0, "RSSI": -92.0,
			"gps": map[string]interface{}{"lat": 51.05, "lon": 3.72, "acc_m": 8.0},
		}
		handleClientPacket(s, &Config{}, "test", bad, msg, nil, nil)
	}
	var nRecept, nObs int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&nRecept)
	s.db.QueryRow(`SELECT COUNT(*) FROM client_observers`).Scan(&nObs)
	if nRecept != 0 || nObs != 0 {
		t.Fatalf("non-hex pubkey must write nothing, got %d receptions, %d observers", nRecept, nObs)
	}
}
