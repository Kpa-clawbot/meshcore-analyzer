package main

import (
	"context"
	"database/sql"
	"strconv"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newReachScanTestDB builds a minimal observer_idx-schema DB with two rows whose
// path contains "01FA" and one that does not, for scanReachRows coverage.
func newReachScanTestDB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, from_pubkey TEXT, payload_type INTEGER)`,
		`CREATE TABLE observers (id TEXT, inactive INTEGER)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_idx INTEGER, snr REAL, path_json TEXT, timestamp INTEGER)`,
		`INSERT INTO observers (id) VALUES ('OBS1')`, // rowid 1
		`INSERT INTO transmissions (id, from_pubkey, payload_type) VALUES (1,'FF00',4),(2,'',5),(3,'',5)`,
		`INSERT INTO observations (id, transmission_id, observer_idx, snr, path_json, timestamp) VALUES
			(1,1,1,-7.0,'["AA","01FA","BB"]',1000),
			(2,2,1,NULL,'["01FA","CC"]',1000),
			(3,3,1,-5.0,'["AA","CC"]',1000)`, // no 01FA → excluded
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return &DB{conn: conn}
}

// resolver that only resolves the exact tokens it's told are unique.
func testResolver(unique map[string]string) func(string) string {
	return func(tok string) string {
		if pk, ok := unique[tok]; ok {
			return pk
		}
		return "" // ambiguous / unknown → skip
	}
}

func TestParsePathTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`["AA","01FA","BB"]`, []string{"AA", "01FA", "BB"}},
		{`["aa","01fa"]`, []string{"AA", "01FA"}}, // uppercased
		{`["EFEF"]`, []string{"EFEF"}},
		{`[]`, nil},
		{``, nil},
		{`null`, nil},
		{`["49A985"]`, []string{"49A985"}}, // 3-byte hop preserved
	}
	for _, c := range cases {
		got := parsePathTokens(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("parsePathTokens(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parsePathTokens(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestAttributeDirections_PredecessorAndSuccessor(t *testing.T) {
	// path A(aa) -> N(01fa) -> B(bb): we hear A, B hears us.
	unique := map[string]string{"AA": "aa00", "BB": "bb00"}
	rows := []pathRow{{
		observerPK: "obs1", payloadType: 5,
		path: []string{"AA", "01FA", "BB"},
	}}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b", testResolver(unique))
	if d.we["aa00"] != 1 {
		t.Fatalf("we_hear[aa00]=%d want 1", d.we["aa00"])
	}
	if d.they["bb00"] != 1 {
		t.Fatalf("they_hear[bb00]=%d want 1", d.they["bb00"])
	}
	if d.relay != 1 {
		t.Fatalf("relay=%d want 1", d.relay)
	}
}

func TestAttributeDirections_LastHopObserverAndAdvertFirstHop(t *testing.T) {
	rows := []pathRow{
		// N is last hop → observer heard us directly (+snr).
		{observerPK: "obsx", payloadType: 5, path: []string{"AA", "01FA"}, snr: 4.0, snrValid: true},
		// N is first hop of an ADVERT (type 4) → we heard the originator.
		{observerPK: "obsy", payloadType: 4, fromPubkey: "origin1", path: []string{"01FA", "CC"}},
	}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b",
		testResolver(map[string]string{"CC": "cc00"}))
	if a, ok := d.obs["obsx"]; !ok || a.count != 1 {
		t.Fatalf("observer obsx not counted")
	}
	if a := d.obs["obsx"]; a.snrN != 1 || a.snrSum != 4.0 {
		t.Fatalf("observer snr not aggregated")
	}
	if d.they["obsx"] != 1 {
		t.Fatalf("they_hear[obsx]=%d want 1", d.they["obsx"])
	}
	if d.we["origin1"] != 1 {
		t.Fatalf("we_hear[origin1]=%d want 1 (advert first-hop)", d.we["origin1"])
	}
	if d.they["cc00"] != 1 {
		t.Fatalf("they_hear[cc00]=%d want 1 (successor)", d.they["cc00"])
	}
}

func TestAttributeDirections_AmbiguousSkippedAndSelfIgnored(t *testing.T) {
	// No observer, so the last-hop observer branch can't fire — this isolates
	// the resolve logic. ZZ is unresolved (ambiguous → skipped); the trailing
	// 01FA resolves to self (ourPK) and must be ignored as a successor.
	rows := []pathRow{{observerPK: "", payloadType: 5, path: []string{"ZZ", "01FA", "01FA"}}}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b",
		testResolver(map[string]string{"01FA": "01fa326b"}))
	if len(d.we) != 0 || len(d.they) != 0 {
		t.Fatalf("ambiguous/self should yield no edges, got we=%v they=%v", d.we, d.they)
	}
}

func TestAttributeDirections_LastHopWithObserverCountsObserver(t *testing.T) {
	// Guards the case the previous test deliberately excludes: when our token is
	// the last hop AND an observer is present, that observer heard us directly.
	rows := []pathRow{{observerPK: "obs1", payloadType: 5, path: []string{"ZZ", "01FA"}}}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b",
		testResolver(map[string]string{}))
	if a, ok := d.obs["obs1"]; d.they["obs1"] != 1 || !ok || a.count != 1 {
		t.Fatalf("last-hop observer should be counted, got they=%v", d.they)
	}
}

func TestReliableTokens(t *testing.T) {
	// pm where "01fa" is unique but "01" is shared (collision).
	nodes := []nodeInfo{
		{PublicKey: "01fa326b0000", Role: "repeater"},
		{PublicKey: "0188aaaa0000", Role: "repeater"},
	}
	pm := buildPrefixMap(nodes)
	toks := reliableTokens("01fa326b0000", pm)
	if !toks["01FA"] {
		t.Fatalf("expected 01FA reliable, got %v", toks)
	}
	if toks["01"] {
		t.Fatalf("1-byte 01 must be excluded (collision), got %v", toks)
	}
}

func TestReliableTokens_CompanionNotMisattributed(t *testing.T) {
	// pm holds only path-capable relays. A companion target (not in pm) whose
	// prefix uniquely matches an UNRELATED relay must yield NO reliable tokens —
	// otherwise that relay's traffic would be credited to the companion.
	relay := nodeInfo{PublicKey: "aa11000000000000", Role: "repeater"}
	pm := buildPrefixMap([]nodeInfo{relay})
	companion := "aa11ffff00000000" // shares 2-byte "aa11" with the relay, differs at byte 3
	toks := reliableTokens(companion, pm)
	if len(toks) != 0 {
		t.Fatalf("companion must get no reliable tokens (prefix points at a relay), got %v", toks)
	}
	// Sanity: the relay itself still resolves to its own prefix.
	if !reliableTokens(relay.PublicKey, pm)["AA11"] {
		t.Fatalf("relay should keep its own AA11 token")
	}
}

func TestScanReachRows_CapTruncates(t *testing.T) {
	defer func(orig int) { reachScanRowLimit = orig }(reachScanRowLimit)
	reachScanRowLimit = 1 // newReachScanTestDB has 2 matching rows
	db := newReachScanTestDB(t)
	defer db.conn.Close()
	srv := &Server{db: db}
	rows, _ := srv.scanReachRows(context.Background(), map[string]bool{"01FA": true}, 0)
	if len(rows) != 1 {
		t.Fatalf("scan must hard-cap at reachScanRowLimit (1), got %d rows", len(rows))
	}
}

func TestReachCacheEviction_BoundedNotWiped(t *testing.T) {
	srv := &Server{}
	resetReachState(t, srv)
	for i := 0; i < reachCacheMax+50; i++ {
		srv.reachCachePut("k"+strconv.Itoa(i), []byte("x"))
	}
	srv.reach.cacheMu.RLock()
	n := len(srv.reach.cache)
	srv.reach.cacheMu.RUnlock()
	// Bounded at the cap and NOT a full wipe (the old crude reset would leave 1).
	if n != reachCacheMax {
		t.Fatalf("cache size after overflow = %d, want %d (bounded, evict-oldest not full-wipe)", n, reachCacheMax)
	}
}

func TestReliableTokens_ThreeByteBranch(t *testing.T) {
	// Two nodes share the 2-byte prefix "01fa" but diverge at byte 3, so the
	// 3-byte (6-hex) prefix is the shortest unique token. Exercises the l=6
	// branch that the 1-/2-byte test does not.
	nodes := []nodeInfo{
		{PublicKey: "01fa32000000", Role: "repeater"},
		{PublicKey: "01fa99000000", Role: "repeater"},
	}
	pm := buildPrefixMap(nodes)
	toks := reliableTokens("01fa32000000", pm)
	if toks["01FA"] {
		t.Fatalf("2-byte 01FA collides here and must be excluded, got %v", toks)
	}
	if !toks["01FA32"] {
		t.Fatalf("expected 3-byte 01FA32 reliable token, got %v", toks)
	}
}

func TestAttributeDirections_NonAdvertFirstHopNotCredited(t *testing.T) {
	// Our token is the FIRST hop but payloadType is NOT an advert. The
	// fromPubkey must NOT be credited as we_hear (only adverts carry a
	// trustworthy originator → first-hop relationship). Guards the
	// `payloadType == PayloadADVERT` condition on the first-hop branch.
	rows := []pathRow{{
		observerPK: "obs1", payloadType: 5, fromPubkey: "origin1",
		path: []string{"01FA", "BB"},
	}}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b",
		testResolver(map[string]string{"BB": "bb00"}))
	if d.we["origin1"] != 0 {
		t.Fatalf("non-advert first hop must not credit we_hear[origin1], got %d", d.we["origin1"])
	}
	if len(d.we) != 0 {
		t.Fatalf("expected no we_hear edges, got %v", d.we)
	}
	if d.they["bb00"] != 1 { // successor still counts
		t.Fatalf("they_hear[bb00]=%d want 1", d.they["bb00"])
	}
}

func TestAttributeDirections_ObserverAggregatesAcrossRows(t *testing.T) {
	// Same observer on the last hop across multiple rows: count and SNR must
	// accumulate, not overwrite.
	rows := []pathRow{
		{observerPK: "obs1", payloadType: 5, path: []string{"AA", "01FA"}, snr: 2.0, snrValid: true},
		{observerPK: "obs1", payloadType: 5, path: []string{"BB", "01FA"}, snr: 6.0, snrValid: true},
	}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b", testResolver(nil))
	a, ok := d.obs["obs1"]
	if !ok || a.count != 2 {
		t.Fatalf("observer count should aggregate to 2, got %+v", a)
	}
	if a.snrN != 2 || a.snrSum != 8.0 {
		t.Fatalf("snr should aggregate (n=2,sum=8), got n=%d sum=%v", a.snrN, a.snrSum)
	}
	if d.they["obs1"] != 2 {
		t.Fatalf("they_hear[obs1]=%d want 2", d.they["obs1"])
	}
}

func TestScanReachRows_DecodesRows(t *testing.T) {
	db := newReachScanTestDB(t)
	defer db.conn.Close()
	srv := &Server{db: db}
	rows, _ := srv.scanReachRows(context.Background(), map[string]bool{"01FA": true}, 0)
	if len(rows) != 2 {
		t.Fatalf("expected 2 matching rows (non-matching path excluded), got %d", len(rows))
	}
	// Find the advert row (order is not guaranteed without ORDER BY).
	var got *pathRow
	for i := range rows {
		if rows[i].payloadType == 4 {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatalf("advert row not returned: %+v", rows)
	}
	// Fields are decoded + normalized: lowercase observer/from, uppercase path.
	if got.observerPK != "obs1" || got.fromPubkey != "ff00" {
		t.Fatalf("decoded fields wrong: %+v", *got)
	}
	if len(got.path) != 3 || got.path[1] != "01FA" {
		t.Fatalf("path not parsed/uppercased: %v", got.path)
	}
	if !got.snrValid || got.snr != -7.0 {
		t.Fatalf("snr not decoded: valid=%v val=%v", got.snrValid, got.snr)
	}
}
