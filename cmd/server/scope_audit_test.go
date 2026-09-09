package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// Full 64-hex pubkeys for fixtures. path_json hops are truncated hashes of
// 1 to 4 bytes, so the forwarder join matches a hop as a PREFIX of the full
// pubkey rather than by equality; the tests need a real full-length key to
// exercise that.
var (
	testFullPubkeyA = "1a2b" + strings.Repeat("11", 30) // 64 hex chars
	testFullPubkeyB = "bbbb" + strings.Repeat("22", 30) // 64 hex chars
)

// setupScopeConformanceDB builds an in-memory transmissions/observations pair
// carrying exactly the columns ScopeConformance reads: code1/scope_name/
// first_seen/route_type on transmissions, path_json on observations. Mirrors
// the live ingestor schema (cmd/ingestor/db.go) rather than a convenient
// fiction, so the join behaves the way it does against a real database.
func setupScopeConformanceDB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	schema := `
		CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			code1 TEXT,
			code2 TEXT,
			scope_name TEXT
		);
		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
			path_json TEXT,
			timestamp INTEGER NOT NULL
		);
	`
	if _, err := conn.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return &DB{conn: conn}
}

// newScopeTestStore wires a *PacketStore to a freshly seeded scope-conformance
// schema. ScopeConformance is a pure SQL read against s.db, so no other
// PacketStore field needs to be populated.
func newScopeTestStore(t *testing.T) *PacketStore {
	t.Helper()
	db := setupScopeConformanceDB(t)
	return newTestStoreWithDB(t, db, &Config{})
}

// scopeSeed carries the code1/scope_name pair for one of the three states a
// seeded transmission can be in. scopeName mirrors scopeNameForDB's own
// encoding: nil means the packet carried no scope at all, a non-nil pointer
// to an empty string means transport-scoped but unmatched, and a non-nil
// pointer to a name means matched.
type scopeSeed struct {
	code1     string
	scopeName *string
}

func scopeMatched(name string) scopeSeed {
	n := name
	return scopeSeed{code1: "1234", scopeName: &n}
}

func scopeUnmatched() scopeSeed {
	empty := ""
	return scopeSeed{code1: "1234", scopeName: &empty}
}

func scopeUnscoped() scopeSeed {
	return scopeSeed{code1: "0000", scopeName: nil}
}

var scopeSeedCounter int

// seedTransmissionRoute inserts one transmission plus a single observation
// attributing it to forwarder, built the way the ingestor would build it: the
// path_json hop is uppercase (packetpath.DecodePathFromRawHex does
// strings.ToUpper on every hop), so the seed exercises the same case the
// live join has to cope with rather than a lowercase convenience fiction.
func seedTransmissionRoute(t *testing.T, s *PacketStore, forwarder string, seed scopeSeed, routeType int) {
	t.Helper()
	seedTransmissionRouteAt(t, s, forwarder, seed, routeType, "2026-01-15T12:00:00Z")
}

// seedTransmissionRouteAt is seedTransmissionRoute with an explicit
// first_seen, for tests (e.g. the handler tests below) that need a
// transmission to fall inside a real-wall-clock ?window= lookback rather
// than the fixed date the ScopeConformance unit tests above use.
func seedTransmissionRouteAt(t *testing.T, s *PacketStore, forwarder string, seed scopeSeed, routeType int, firstSeen string) {
	t.Helper()
	scopeSeedCounter++
	hash := fmt.Sprintf("scopehash%d", scopeSeedCounter)

	res, err := s.db.conn.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, code1, code2, scope_name)
		 VALUES ('AA', ?, ?, ?, 1, ?, '00', ?)`,
		hash, firstSeen, routeType, seed.code1, seed.scopeName,
	)
	if err != nil {
		t.Fatalf("seed transmission: %v", err)
	}
	txID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed transmission id: %v", err)
	}

	pathJSON := fmt.Sprintf(`["%s"]`, strings.ToUpper(forwarder))
	if _, err := s.db.conn.Exec(
		`INSERT INTO observations (transmission_id, path_json, timestamp) VALUES (?, ?, ?)`,
		txID, pathJSON, time.Now().Unix(),
	); err != nil {
		t.Fatalf("seed observation: %v", err)
	}
}

// seedTransmission seeds a FLOOD packet (route_type=1) — path[last] is the
// actual transmitter, so forwarder is attributable.
func seedTransmission(t *testing.T, s *PacketStore, forwarder string, seed scopeSeed) {
	t.Helper()
	seedTransmissionRoute(t, s, forwarder, seed, RouteFlood)
}

// seedDirectTransmission seeds a DIRECT packet (route_type=2) — path[last] is
// the route's far end, never the transmitter, so forwarder must NOT be
// attributed even though it appears in path_json.
func seedDirectTransmission(t *testing.T, s *PacketStore, forwarder string, seed scopeSeed) {
	t.Helper()
	seedTransmissionRoute(t, s, forwarder, seed, RouteDirect)
}

func TestScopeAuditForwardingAttributesUnambiguousHop(t *testing.T) {
	s := newScopeTestStore(t)
	hop := testFullPubkeyA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, s, hop, scopeMatched("#be"), RouteFlood, recent)

	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", []string{testFullPubkeyA})
	if err != nil {
		t.Fatal(err)
	}
	agg := got[testFullPubkeyA]
	if agg == nil || agg.scopes["be"] == nil || agg.scopes["be"].Packets != 1 {
		t.Fatalf("want the hop attributed to the sole matching target, got %+v", got)
	}
	if agg.ambiguousHops != 0 {
		t.Errorf("ambiguousHops = %d, want 0 — nothing here is ambiguous", agg.ambiguousHops)
	}
}

// TestScopeAuditForwardingAmbiguousHopCreditsNeitherTarget is FIX 1's core
// case: two declared targets share a 2-byte prefix. A hop at that prefix
// must be attributed to NEITHER of them (crediting both would let a
// colliding neighbour's traffic silently paper over a real notObserved
// finding), and BOTH candidates' ambiguousHops counters must increment (so
// the row can surface the caveat regardless of which candidate is being
// looked at).
func TestScopeAuditForwardingAmbiguousHopCreditsNeitherTarget(t *testing.T) {
	s := newScopeTestStore(t)
	pkA := "1a2b" + strings.Repeat("11", 30)
	pkB := "1a2b" + strings.Repeat("22", 30) // shares pkA's first 4 hex chars
	hop := pkA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, s, hop, scopeMatched("#be"), RouteFlood, recent)

	targets := []string{pkA, pkB}
	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", targets)
	if err != nil {
		t.Fatal(err)
	}
	for _, pk := range targets {
		agg := got[pk]
		if agg == nil {
			t.Fatalf("%s: want an agg present to carry the ambiguity counter, got none (result = %+v)", pk, got)
		}
		if len(agg.scopes) != 0 {
			t.Errorf("%s: scopes = %+v, want empty — an ambiguous hop must not be attributed to either candidate", pk, agg.scopes)
		}
		if agg.unscopedPackets != 0 {
			t.Errorf("%s: unscopedPackets = %d, want 0", pk, agg.unscopedPackets)
		}
		if agg.ambiguousHops != 1 {
			t.Errorf("%s: ambiguousHops = %d, want 1", pk, agg.ambiguousHops)
		}
	}
}

// --- GET /api/scope-audit handler tests ---

// setupScopeAuditServer builds a *Server over the ScopeConformance schema
// plus a nodes table carrying the confirmed-scope columns from #1865/#1971,
// which is where this endpoint reads its declared side. detectSchema must run
// after the columns exist so hasConfiguredScope is picked up; without it the
// handler correctly serves an empty audit and every assertion below would pass
// vacuously.
func setupScopeAuditServer(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	db := setupScopeConformanceDB(t)
	if _, err := db.conn.Exec(`CREATE TABLE nodes (
		public_key TEXT PRIMARY KEY,
		name TEXT,
		role TEXT,
		configured_scope TEXT,
		configured_scope_at TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if err := db.detectSchema(context.Background(), db.conn); err != nil {
		t.Fatal(err)
	}
	if !db.hasConfiguredScope {
		t.Fatal("hasConfiguredScope is false after creating the column: the fixture would test nothing")
	}
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = newTestStoreWithDB(t, db, cfg)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return srv, router
}

// insertDeclared records a node's confirmed region list. Keeps the original
// signature so the ported cases read unchanged, but writes to the upstream
// source: nodes.configured_scope, which an observer /neighbors report fills
// (#1865/#1971).
//
// truncated is accepted and ignored. The old source recorded whether an answer
// was cut short; /neighbors has its own size cap but does not report whether it
// fired, so the audit cannot claim either way and the page omits the caveat
// rather than showing a wrong one. Kept in the signature so the ported tests
// still document which fixtures meant "this answer was incomplete".
func insertDeclared(t *testing.T, srv *Server, target, observedAt, regionsCSV string, truncated int) {
	t.Helper()
	_ = truncated
	if _, err := srv.db.conn.Exec(
		`INSERT INTO nodes (public_key, configured_scope, configured_scope_at) VALUES (?, ?, ?)
		 ON CONFLICT(public_key) DO UPDATE SET configured_scope = excluded.configured_scope,
		                                       configured_scope_at = excluded.configured_scope_at`,
		target, regionsCSV, observedAt); err != nil {
		t.Fatal(err)
	}
}

func getScopeAudit(t *testing.T, router *mux.Router, query string) ScopeAuditResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/scope-audit"+query, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got ScopeAuditResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// TestHandleScopeAuditNormalisesHashPrefix pins trap 1: transmissions.scope_name
// keeps the '#' (hashRegions config), regions_csv arrives from the firmware
// with it already stripped. Declared "be-van" and observed "#be-van" must be
// recognised as the same scope, not reported as both missing and undeclared.
func TestHandleScopeAuditNormalisesHashPrefix(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	hop := pk[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be-van", 0)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("#be-van"), RouteFlood, recent)

	// FIX 2: the DECLARED side must be normalised too, not just observed.
	// regions_csv is not guaranteed to arrive with '#' already stripped (a
	// firmware variant, an operator-seeded row, or a future collector could
	// leave it on) — declaring "#be-van" and observing "#be-van" (which the
	// observed side always normalises to "be-van") must still match. Without
	// the fix, declaredNamed/declaredSet keep the raw "#be-van", so this
	// second repeater's row would show BOTH "#be-van" in notObserved (the
	// declared value never matches the normalised agg.scopes key) AND
	// "be-van" in undeclaredObserved (the normalised observed name isn't in
	// declaredSet) — the exact trap normScope exists to prevent, reappearing
	// on the other side of the comparison. Seeded alongside pk (not as a
	// separate getScopeAudit call) so both land in one response and neither
	// is masked by the 30s response cache.
	pk2 := testFullPubkeyB
	hop2 := pk2[:4]
	insertDeclared(t, srv, pk2, time.Now().UTC().Format(time.RFC3339), "#be-van", 0)
	seedTransmissionRouteAt(t, srv.store, hop2, scopeMatched("#be-van"), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 2 {
		t.Fatalf("repeaters = %+v, want exactly 2", got.Repeaters)
	}

	row := findScopeAuditRow(t, got.Repeaters, pk)
	if len(row.NotObserved) != 0 {
		t.Errorf("notObserved = %v, want empty — declared \"be-van\" observed as \"#be-van\" must match after normalisation", row.NotObserved)
	}
	if len(row.UndeclaredObserved) != 0 {
		t.Errorf("undeclaredObserved = %+v, want empty", row.UndeclaredObserved)
	}

	row2 := findScopeAuditRow(t, got.Repeaters, pk2)
	if len(row2.DeclaredRegions) != 1 || row2.DeclaredRegions[0] != "be-van" {
		t.Errorf("declaredRegions = %v, want [\"be-van\"] — the leading '#' must be stripped from the declared side too", row2.DeclaredRegions)
	}
	if len(row2.NotObserved) != 0 {
		t.Errorf("notObserved = %v, want empty — declared \"#be-van\" observed as \"#be-van\" must match after normalising BOTH sides", row2.NotObserved)
	}
	if len(row2.UndeclaredObserved) != 0 {
		t.Errorf("undeclaredObserved = %+v, want empty", row2.UndeclaredObserved)
	}
}

// findScopeAuditRow locates the row for pk, failing the test if absent —
// used wherever more than one repeater is seeded in the same response, since
// sort order is not by pubkey and cannot be relied on to pick the right row.
func findScopeAuditRow(t *testing.T, rows []ScopeAuditRow, pk string) ScopeAuditRow {
	t.Helper()
	for _, r := range rows {
		if r.PublicKey == pk {
			return r
		}
	}
	t.Fatalf("no row for pubkey %s in %+v", pk, rows)
	return ScopeAuditRow{}
}

// TestHandleScopeAuditExcludesWildcardFromComparison pins trap 2: '*' is the
// root of the region tree (governs plain FLOOD), not a scope. It must never
// appear in declaredRegions (or its count) or in notObserved/undeclaredObserved.
func TestHandleScopeAuditExcludesWildcardFromComparison(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "*,be", 0)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 {
		t.Fatalf("repeaters = %+v, want 1", got.Repeaters)
	}
	row := got.Repeaters[0]
	if !row.DeclaredWildcard {
		t.Error("declaredWildcard = false, want true")
	}
	if len(row.DeclaredRegions) != 1 || row.DeclaredRegions[0] != "be" {
		t.Errorf("declaredRegions = %v, want [\"be\"] — '*' must be excluded from the region list and its count", row.DeclaredRegions)
	}
	for _, r := range row.NotObserved {
		if r == "*" {
			t.Error("notObserved contains '*' — it must never be treated as a scope")
		}
	}
}

// TestHandleScopeAuditOmitsRepeaterWithNoDeclaredRow pins trap 3: a repeater
// that was never successfully asked is absent from the response entirely —
// not shown as a row declaring nothing, which is a distinct, meaningful fact.
func TestHandleScopeAuditOmitsRepeaterWithNoDeclaredRow(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	declaredPk := testFullPubkeyA
	neverAskedPk := testFullPubkeyB
	insertDeclared(t, srv, declaredPk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, 'never-asked', 'repeater')`, neverAskedPk); err != nil {
		t.Fatal(err)
	}

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 || got.Repeaters[0].PublicKey != declaredPk {
		t.Fatalf("repeaters = %+v, want exactly the one repeater that has declared", got.Repeaters)
	}
}

// TestHandleScopeAuditUnknownNodeNameIsNullNotEmpty proves a declared target
// this instance holds no nodes row for serialises name/role as null, not "".
// A declared-regions answer can name a repeater the network has never recorded,
// and "" would make that indistinguishable from a node we DO know that simply
// has no name — the same absent-is-not-empty rule the declared side follows.
func TestHandleScopeAuditUnknownNodeNameIsNullNotEmpty(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	// deliberately NO nodes row for pk

	req := httptest.NewRequest("GET", "/api/scope-audit", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"name":null`) {
		t.Errorf(`body must carry "name":null for a target with no nodes row, got: %s`, body)
	}
	if strings.Contains(body, `"name":""`) {
		t.Error(`"name":"" collapses "unknown node" into "node with no name"`)
	}

	var got ScopeAuditResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Repeaters) != 1 {
		t.Fatalf("repeaters = %d, want 1", len(got.Repeaters))
	}
	if got.Repeaters[0].Name != nil {
		t.Errorf("Name = %q, want nil", *got.Repeaters[0].Name)
	}
}

// TestHandleScopeAuditWildcardContradiction: observed forwarding unscoped
// (plain-FLOOD) traffic while the declared list omits '*' is the wildcard
// contradiction this endpoint must flag — the repeater says it does NOT
// forward those packets, and the traffic says otherwise.
func TestHandleScopeAuditWildcardContradiction(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	hop := pk[:4]
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be", 0) // no '*'
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeUnscoped(), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 {
		t.Fatalf("repeaters = %+v, want 1", got.Repeaters)
	}
	row := got.Repeaters[0]
	if !row.WildcardContradiction {
		t.Error("wildcardContradiction = false, want true — observed unscoped forwarding but '*' not declared")
	}
	if row.ObservedUnscopedPackets != 1 {
		t.Errorf("observedUnscopedPackets = %d, want 1", row.ObservedUnscopedPackets)
	}
}

// TestHandleScopeAuditWildcardDeclaredIsNotAContradiction is the other half:
// the same observed unscoped traffic is expected, not a contradiction, once
// '*' is declared.
func TestHandleScopeAuditWildcardDeclaredIsNotAContradiction(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	hop := pk[:4]
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "*,be", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeUnscoped(), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	if got.Repeaters[0].WildcardContradiction {
		t.Error("wildcardContradiction = true, want false — '*' IS declared, so unscoped forwarding is expected")
	}
}

// TestHandleScopeAuditNotObservedAndUndeclaredObserved exercises both
// comparison directions in one repeater: a declared region with zero
// observed forwarding, and an observed scope absent from the declared list.
func TestHandleScopeAuditNotObservedAndUndeclaredObserved(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	hop := pk[:4]
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be,be-vlg", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("#be"), RouteFlood, recent)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("#de-nw"), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	row := got.Repeaters[0]
	if len(row.NotObserved) != 1 || row.NotObserved[0] != "be-vlg" {
		t.Errorf("notObserved = %v, want [\"be-vlg\"]", row.NotObserved)
	}
	if len(row.UndeclaredObserved) != 1 || row.UndeclaredObserved[0].Scope != "de-nw" {
		t.Errorf("undeclaredObserved = %+v, want exactly \"de-nw\"", row.UndeclaredObserved)
	}
}

// TestHandleScopeAuditSurfacesAmbiguousHops is FIX 1's handler-level case:
// two repeaters both declare "be" and share a 2-byte pubkey prefix. The one
// hop seen in the window can't be attributed to either, so both rows must
// still show "be" as notObserved (an ambiguous hop invents no attribution,
// so it cannot silently satisfy the declared region) AND both rows must
// carry ambiguousHops=1, the caveat that the notObserved finding might be a
// prefix collision rather than a real gap.
func TestHandleScopeAuditSurfacesAmbiguousHops(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pkA := "1a2b" + strings.Repeat("11", 30)
	pkB := "1a2b" + strings.Repeat("22", 30)
	hop := pkA[:4]
	insertDeclared(t, srv, pkA, time.Now().UTC().Format(time.RFC3339), "be", 0)
	insertDeclared(t, srv, pkB, time.Now().UTC().Format(time.RFC3339), "be", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("#be"), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 2 {
		t.Fatalf("repeaters = %+v, want 2", got.Repeaters)
	}
	for _, row := range got.Repeaters {
		if row.AmbiguousHops != 1 {
			t.Errorf("%s: ambiguousHops = %d, want 1", row.PublicKey, row.AmbiguousHops)
		}
		if len(row.NotObserved) != 1 || row.NotObserved[0] != "be" {
			t.Errorf("%s: notObserved = %v, want [\"be\"] — an ambiguous hop must not silently satisfy the declared region", row.PublicKey, row.NotObserved)
		}
	}
}

// TestHandleScopeAuditSortsMissingRegionsFirst: the repeater with a declared
// region it is not forwarding must rank above a repeater in full agreement —
// that's the headline this endpoint exists to surface, not the boring majority.
func TestHandleScopeAuditSortsMissingRegionsFirst(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	agreePk := testFullPubkeyA
	missingPk := testFullPubkeyB
	agreeHop := agreePk[:4]
	missingHop := missingPk[:4]
	insertDeclared(t, srv, agreePk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	insertDeclared(t, srv, missingPk, time.Now().UTC().Format(time.RFC3339), "be,be-vlg", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, agreeHop, scopeMatched("#be"), RouteFlood, recent)
	seedTransmissionRouteAt(t, srv.store, missingHop, scopeMatched("#be"), RouteFlood, recent)
	// missingPk never forwards be-vlg -> 1 missing declared region; agreePk has 0.

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 2 {
		t.Fatalf("repeaters = %+v, want 2", got.Repeaters)
	}
	if got.Repeaters[0].PublicKey != missingPk {
		t.Errorf("repeaters[0] = %s, want %s (the row missing a declared region) ranked first", got.Repeaters[0].PublicKey, missingPk)
	}
}

// TestHandleScopeAuditWindowVocabulary confirms this endpoint matches the
// vocabulary used by the sibling /api/scope-stats and /api/nodes/{pubkey}/scopes
// endpoints (1h, 24h, 7d), not the broader ParseTimeWindow alias set.
func TestHandleScopeAuditWindowVocabulary(t *testing.T) {
	_, router := setupScopeAuditServer(t)
	req := httptest.NewRequest("GET", "/api/scope-audit?window=30d", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("window=30d: status = %d, want 400 (not part of this endpoint's vocabulary)", w.Code)
	}
}

// TestHandleScopeAuditFiltersBlacklistedNode confirms a blacklisted repeater
// is dropped from the audit even though it has declared a region list,
// mirroring the blacklist filtering applied by other multi-node endpoints.
func TestHandleScopeAuditFiltersBlacklistedNode(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	srv.cfg.NodeBlacklist = []string{pk}

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 0 {
		t.Errorf("repeaters = %+v, want empty — blacklisted node must be excluded", got.Repeaters)
	}
}

// TestHandleScopeAuditFiltersHiddenNamePrefix is the scope-audit twin of
// TestHandleScopeAuditFiltersBlacklistedNode, covering FIX 5: a repeater
// whose known name matches an operator-configured hidden-name prefix is
// excluded. It also pins the subtler half of the `id.Name != nil` guard in
// handleScopeAudit — a declared target this instance holds NO nodes row for
// has no name to test a hidden-prefix rule against, so it must NOT be
// filtered merely for lacking a name; only a KNOWN, matching name is ever
// grounds for hiding.
func TestHandleScopeAuditFiltersHiddenNamePrefix(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	hiddenPk := testFullPubkeyA
	unknownPk := testFullPubkeyB
	insertDeclared(t, srv, hiddenPk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	insertDeclared(t, srv, unknownPk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	// Upsert, not insert: upstream the declared list lives ON the nodes row
	// (configured_scope), so insertDeclared has already created it. The fork
	// kept them in separate tables and a plain INSERT was safe there.
	if _, err := srv.db.conn.Exec(
		`INSERT INTO nodes (public_key, name, role) VALUES (?, ?, 'repeater')
		 ON CONFLICT(public_key) DO UPDATE SET name = excluded.name, role = excluded.role`,
		hiddenPk, "🚫 ban me"); err != nil {
		t.Fatal(err)
	}
	// unknownPk deliberately gets NO name. On the fork that meant no nodes row
	// at all; here the row exists (configured_scope lives on it) but name stays
	// NULL. The property under test is unchanged: a node whose name we do not
	// know must never be hidden by a name-prefix rule.
	srv.cfg.SetHiddenNamePrefixes([]string{"🚫"})

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 || got.Repeaters[0].PublicKey != unknownPk {
		t.Fatalf("repeaters = %+v, want exactly the unknown-name target — the hidden-name-prefixed repeater must be excluded, and the nameless one must NOT be excluded merely for having no name", got.Repeaters)
	}
}

// TestHandleScopeAuditNoDeclaredRegionsTable covers the missing-table
// degrade path (mirrors TestHandleNodeScopesNoDeclaredRegionsTable): an
// older database predating the configured_scope column must not fail the
// request: the audit simply has no declared side to report.
func TestHandleScopeAuditWithoutConfiguredScopeColumn(t *testing.T) {
	// setupScopeConformanceDB creates no nodes table, so detectSchema leaves
	// hasConfiguredScope false: exactly an instance that has not yet ingested a
	// /neighbors report or is on a schema predating #1971.
	db := setupScopeConformanceDB(t)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = newTestStoreWithDB(t, db, cfg)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 0 {
		t.Errorf("repeaters = %+v, want empty when configured_scope does not exist", got.Repeaters)
	}
}

// --- Scope audit config-state classification ---

// TestScopeAuditConfigStateClassification is a table-driven test of the pure
// classification function scopeAuditConfigState — the derivation is a plain
// switch over (namedRegions, wildcard), so each of the four cells is worth
// pinning directly rather than only indirectly via the handler.
func TestScopeAuditConfigStateClassification(t *testing.T) {
	tests := []struct {
		name         string
		namedRegions []string
		wildcard     bool
		want         string
	}{
		{"named and wildcard", []string{"be"}, true, ScopeConfigFull},
		{"wildcard only", nil, true, ScopeConfigNoScopes},
		{"named only", []string{"be"}, false, ScopeConfigNoUnscoped},
		{"neither", nil, false, ScopeConfigNoFlood},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scopeAuditConfigState(tt.namedRegions, tt.wildcard)
			if got != tt.want {
				t.Errorf("scopeAuditConfigState(%v, %v) = %q, want %q", tt.namedRegions, tt.wildcard, got, tt.want)
			}
		})
	}
}

// TestHandleScopeAuditConfigStateAllFourShapes seeds one repeater per shape
// of the (declaredRegions, declaredWildcard) pair — including the fourth,
// "answered but empty" shape (no named regions AND no '*') that sits outside
// the three named states — and confirms both that each row's ConfigState
// lands correctly AND that tallying ConfigState across the response (the
// same arithmetic the frontend summary line performs over d.repeaters)
// reproduces the exact per-state counts seeded here. That second check is
// what pins "the counts in the summary match the rows": if
// scopeAuditConfigState mis-tags even one row, the tally computed from the
// response no longer matches what was seeded here, and the test fails.
func TestHandleScopeAuditConfigStateAllFourShapes(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	now := time.Now().UTC().Format(time.RFC3339)

	fullPk := strings.Repeat("11", 32)
	noScopesPk := strings.Repeat("22", 32)
	noUnscopedPk := strings.Repeat("33", 32)
	noFloodPk := strings.Repeat("44", 32)

	insertDeclared(t, srv, fullPk, now, "*,be", 0)
	insertDeclared(t, srv, noScopesPk, now, "*", 0)
	insertDeclared(t, srv, noUnscopedPk, now, "be", 0)
	insertDeclared(t, srv, noFloodPk, now, "", 0)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 4 {
		t.Fatalf("repeaters = %+v, want exactly 4", got.Repeaters)
	}

	want := map[string]string{
		fullPk:       ScopeConfigFull,
		noScopesPk:   ScopeConfigNoScopes,
		noUnscopedPk: ScopeConfigNoUnscoped,
		noFloodPk:    ScopeConfigNoFlood,
	}
	for pk, wantState := range want {
		row := findScopeAuditRow(t, got.Repeaters, pk)
		if row.ConfigState != wantState {
			t.Errorf("pk %s: configState = %q, want %q", pk, row.ConfigState, wantState)
		}
	}

	counts := map[string]int{}
	for _, row := range got.Repeaters {
		counts[row.ConfigState]++
	}
	for _, state := range []string{ScopeConfigFull, ScopeConfigNoScopes, ScopeConfigNoUnscoped, ScopeConfigNoFlood} {
		if counts[state] != 1 {
			t.Errorf("count of state %q across repeaters = %d, want 1 (summary tally must match the seeded rows)", state, counts[state])
		}
	}
}

// TestHandleNodeScopesDifferentWindowIsSeparateCacheEntry confirms the cache
// key includes window: a request for a different window must recompute
// rather than reuse another window's cached entry.

// --- Merging confirmed-scope sources (#1975) ---

// createDeclaredRegionsTable adds the optional second source and re-probes the
// schema, so hasDeclaredRegionsTable flips. Without the re-probe the merge
// would skip the table and every assertion below would pass vacuously.
func createDeclaredRegionsTable(t *testing.T, srv *Server) {
	t.Helper()
	if _, err := srv.db.conn.Exec(`
		CREATE TABLE node_declared_regions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			target TEXT NOT NULL,
			rx_pubkey TEXT NOT NULL,
			observed_at TEXT NOT NULL,
			ingested_at TEXT NOT NULL,
			regions_csv TEXT NOT NULL,
			truncated INTEGER NOT NULL DEFAULT 0,
			UNIQUE(target, rx_pubkey, observed_at)
		)`); err != nil {
		t.Fatal(err)
	}
	if err := srv.db.detectSchema(context.Background(), srv.db.conn); err != nil {
		t.Fatal(err)
	}
	if !srv.db.hasDeclaredRegionsTable {
		t.Fatal("hasDeclaredRegionsTable is false after creating the table: the fixture would test nothing")
	}
}

func seedSecondSource(t *testing.T, srv *Server, target, observedAt, regionsCSV string, truncated int) {
	t.Helper()
	if _, err := srv.db.conn.Exec(
		`INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated)
		 VALUES (?, 'rxpubkeyhex', ?, ?, ?, ?)`,
		target, observedAt, observedAt, regionsCSV, truncated); err != nil {
		t.Fatal(err)
	}
}

func declaredFor(t *testing.T, srv *Server, target string) (DeclaredRegionsRow, bool) {
	t.Helper()
	rows, err := srv.db.AllCurrentDeclaredRegions()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Target == target {
			return r, true
		}
	}
	return DeclaredRegionsRow{}, false
}

func TestDeclaredRegionsMergeUsesConfiguredScopeAlone(t *testing.T) {
	srv, _ := setupScopeAuditServer(t)
	insertDeclared(t, srv, testFullPubkeyA, "2026-09-01T00:00:00Z", "#be,#eu", 0)
	got, ok := declaredFor(t, srv, testFullPubkeyA)
	if !ok || got.RegionsCSV != "#be,#eu" {
		t.Fatalf("got %+v (found=%v), want the configured_scope answer", got, ok)
	}
}

func TestDeclaredRegionsMergeUsesSecondSourceAlone(t *testing.T) {
	// The case that made this merge necessary: an instance collecting confirmed
	// scopes by another route, with no configured_scope written yet. Reading
	// only configured_scope would render an empty page on real data.
	srv, _ := setupScopeAuditServer(t)
	createDeclaredRegionsTable(t, srv)
	seedSecondSource(t, srv, testFullPubkeyA, "2026-09-01T00:00:00Z", "be,eu", 0)
	got, ok := declaredFor(t, srv, testFullPubkeyA)
	if !ok || got.RegionsCSV != "be,eu" {
		t.Fatalf("got %+v (found=%v), want the second source's answer", got, ok)
	}
}

func TestDeclaredRegionsMergeNewestAnswerWins(t *testing.T) {
	srv, _ := setupScopeAuditServer(t)
	createDeclaredRegionsTable(t, srv)
	insertDeclared(t, srv, testFullPubkeyA, "2026-09-01T00:00:00Z", "#old", 0)
	seedSecondSource(t, srv, testFullPubkeyA, "2026-09-02T00:00:00Z", "newer", 0)
	got, _ := declaredFor(t, srv, testFullPubkeyA)
	if got.RegionsCSV != "newer" {
		t.Errorf("regions = %q, want the later answer regardless of which source it came from", got.RegionsCSV)
	}

	// And the other way round, so the rule is "newest wins" and not "one source
	// always beats the other".
	insertDeclared(t, srv, testFullPubkeyB, "2026-09-03T00:00:00Z", "#newer", 0)
	seedSecondSource(t, srv, testFullPubkeyB, "2026-09-02T00:00:00Z", "old", 0)
	gotB, _ := declaredFor(t, srv, testFullPubkeyB)
	if gotB.RegionsCSV != "#newer" {
		t.Errorf("regions = %q, want the later answer from the other source", gotB.RegionsCSV)
	}
}

func TestDeclaredRegionsMergeAnswerWithAnInstantBeatsOneWithout(t *testing.T) {
	// An empty instant means "we do not know when this was answered". It must
	// not outrank a dated answer, but it must still be used when it is all we
	// have.
	srv, _ := setupScopeAuditServer(t)
	createDeclaredRegionsTable(t, srv)
	insertDeclared(t, srv, testFullPubkeyA, "", "#undated", 0)
	seedSecondSource(t, srv, testFullPubkeyA, "2026-09-01T00:00:00Z", "dated", 0)
	got, _ := declaredFor(t, srv, testFullPubkeyA)
	if got.RegionsCSV != "dated" {
		t.Errorf("regions = %q, want the dated answer to win", got.RegionsCSV)
	}

	insertDeclared(t, srv, testFullPubkeyB, "", "#undated", 0)
	gotB, ok := declaredFor(t, srv, testFullPubkeyB)
	if !ok || gotB.RegionsCSV != "#undated" {
		t.Errorf("got %+v (found=%v), want an undated answer to be used when it is the only one", gotB, ok)
	}
}

func TestDeclaredRegionsMergeCarriesTruncatedFromTheSourceThatHasIt(t *testing.T) {
	srv, _ := setupScopeAuditServer(t)
	createDeclaredRegionsTable(t, srv)
	seedSecondSource(t, srv, testFullPubkeyA, "2026-09-01T00:00:00Z", "be", 1)
	got, _ := declaredFor(t, srv, testFullPubkeyA)
	if !got.Truncated {
		t.Error("truncated must survive the merge from a source that records it")
	}
}

func TestDeclaredRegionsMergeWithNoSourcesIsEmptyNotAnError(t *testing.T) {
	db := setupScopeConformanceDB(t)
	rows, err := db.AllCurrentDeclaredRegions()
	if err != nil {
		t.Fatalf("no sources must not be an error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want empty", rows)
	}
}

// --- Every-hop forwarder attribution and the scan that pays for it ---
//
// These cover the change from crediting path[last] to crediting every hop of a
// flood-family route, and the two-column scan plus per-window TTL that keeps
// the wider scan affordable.

// seedTransmissionPathAt seeds one transmission whose single observation
// carries a MULTI-hop path. A one-hop seed cannot tell the two reasons a node
// gets attributed apart — it is simultaneously path[0] and path[last] — so the
// mid-path cases below need a path with something after the target on it.
//
// Hops are upper-cased for the same reason seedTransmissionRoute does it: the
// decoder writes them that way (packetpath.DecodePathFromRawHex), and the join
// has to cope with that rather than with a lowercase convenience fiction.
func seedTransmissionPathAt(t *testing.T, s *PacketStore, hops []string, seed scopeSeed, routeType int, firstSeen string) {
	t.Helper()
	scopeSeedCounter++
	hash := fmt.Sprintf("scopehash%d", scopeSeedCounter)

	res, err := s.db.conn.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, code1, code2, scope_name)
		 VALUES ('AA', ?, ?, ?, 1, ?, '00', ?)`,
		hash, firstSeen, routeType, seed.code1, seed.scopeName,
	)
	if err != nil {
		t.Fatalf("seed transmission: %v", err)
	}
	txID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed transmission id: %v", err)
	}

	quoted := make([]string, len(hops))
	for i, h := range hops {
		quoted[i] = `"` + strings.ToUpper(h) + `"`
	}
	pathJSON := "[" + strings.Join(quoted, ",") + "]"
	if _, err := s.db.conn.Exec(
		`INSERT INTO observations (transmission_id, path_json, timestamp) VALUES (?, ?, ?)`,
		txID, pathJSON, time.Now().Unix(),
	); err != nil {
		t.Fatalf("seed observation: %v", err)
	}
}

// seedTransmission seeds a FLOOD packet (route_type=1) — path[last] is the
// actual transmitter, so forwarder is attributable.

// TestScopeAuditForwardingAttributesMidPathHop is the fleet-wide half of the
// mid-path attribution fix. The audit runs a different query from
// ScopeConformance — one full-window scan instead of one EXISTS per pubkey — so
// the two share the rule but not the code, and both need pinning.
//
// This is the case behind the audit's 65% blind spot: a declared target that
// forwards steadily but is never the hop an observer hears directly had every
// region it declares reported as notObserved.
func TestScopeAuditForwardingAttributesMidPathHop(t *testing.T) {
	s := newScopeTestStore(t)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionPathAt(t, s, []string{testFullPubkeyA[:4], "AAAA", "BBBB"}, scopeMatched("#be"), RouteFlood, recent)

	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", []string{testFullPubkeyA})
	if err != nil {
		t.Fatal(err)
	}
	agg := got[testFullPubkeyA]
	if agg == nil || agg.scopes["be"] == nil || agg.scopes["be"].Packets != 1 {
		t.Fatalf("want the mid-path hop attributed to its sole matching target, got %+v", got)
	}
	if agg.ambiguousHops != 0 {
		t.Errorf("ambiguousHops = %d, want 0 — one target matches this hop", agg.ambiguousHops)
	}
}

// TestScopeAuditForwardingIgnoresDirectRoutes pins the route-type filter on the
// audit's own query. With the last-hop restriction gone it is the only guard
// against crediting a DIRECT route's remaining path plan as forwarding — and a
// DIRECT packet's hops are frequently the declared targets this audit judges.

// TestScopeAuditForwardingIgnoresDirectRoutes pins the route-type filter on the
// audit's own query. With the last-hop restriction gone it is the only guard
// against crediting a DIRECT route's remaining path plan as forwarding — and a
// DIRECT packet's hops are frequently the declared targets this audit judges.
func TestScopeAuditForwardingIgnoresDirectRoutes(t *testing.T) {
	s := newScopeTestStore(t)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionPathAt(t, s, []string{"AAAA", testFullPubkeyA[:4], "BBBB"}, scopeMatched("#be"), RouteDirect, recent)
	seedTransmissionPathAt(t, s, []string{"AAAA", "BBBB", testFullPubkeyA[:4]}, scopeMatched("#be"), RouteTransportDirect, recent)

	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", []string{testFullPubkeyA})
	if err != nil {
		t.Fatal(err)
	}
	if agg := got[testFullPubkeyA]; agg != nil && (len(agg.scopes) != 0 || agg.unscopedPackets != 0 || agg.ambiguousHops != 0) {
		t.Errorf("agg = %+v, want no attribution from DIRECT routes", agg)
	}
}

// TestScopeAuditForwardingCountsOneTransmissionOncePerTarget pins that the
// existing "<target>|<txID>" de-duplication also absorbs the same target
// matching several hops of ONE path — which could not happen while only
// path[last] was read, and now can (a routing loop, or two hops colliding on the
// same truncated prefix). Without it a looping packet would inflate a target's
// packet count and quietly make a quiet region look busy.

// TestScopeAuditForwardingCountsOneTransmissionOncePerTarget pins that the
// existing "<target>|<txID>" de-duplication also absorbs the same target
// matching several hops of ONE path — which could not happen while only
// path[last] was read, and now can (a routing loop, or two hops colliding on the
// same truncated prefix). Without it a looping packet would inflate a target's
// packet count and quietly make a quiet region look busy.
func TestScopeAuditForwardingCountsOneTransmissionOncePerTarget(t *testing.T) {
	s := newScopeTestStore(t)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionPathAt(t, s, []string{testFullPubkeyA[:4], "AAAA", testFullPubkeyA[:4]}, scopeMatched("#be"), RouteFlood, recent)

	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", []string{testFullPubkeyA})
	if err != nil {
		t.Fatal(err)
	}
	agg := got[testFullPubkeyA]
	if agg == nil || agg.scopes["be"] == nil {
		t.Fatalf("want #be attributed, got %+v", got)
	}
	if agg.scopes["be"].Packets != 1 {
		t.Errorf("Packets = %d, want 1 — one transmission, matched on two of its hops", agg.scopes["be"].Packets)
	}
}

// TestScopeAuditForwardingAttributesLongerHopByItsOwnLength pins the
// length-indexed half of scopeAuditPrefixIndex, which every other test in this
// file leaves untested: they all seed 4-char hops, so a lookup that ignored hop
// length entirely would still pass them.
//
// pkOther shares the first 4 hex chars with testFullPubkeyA and diverges after
// that, so an 8-char hop has exactly one candidate while a 4-char hop would
// have two. Attribution must therefore key on the hop's OWN length: at 8 chars
// this is an unambiguous attribution, not an ambiguousHops row.

// TestScopeAuditForwardingAttributesLongerHopByItsOwnLength pins the
// length-indexed half of scopeAuditPrefixIndex, which every other test in this
// file leaves untested: they all seed 4-char hops, so a lookup that ignored hop
// length entirely would still pass them.
//
// pkOther shares the first 4 hex chars with testFullPubkeyA and diverges after
// that, so an 8-char hop has exactly one candidate while a 4-char hop would
// have two. Attribution must therefore key on the hop's OWN length: at 8 chars
// this is an unambiguous attribution, not an ambiguousHops row.
func TestScopeAuditForwardingAttributesLongerHopByItsOwnLength(t *testing.T) {
	s := newScopeTestStore(t)
	pkOther := testFullPubkeyA[:4] + strings.Repeat("33", 30)
	hop := testFullPubkeyA[:8]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionPathAt(t, s, []string{hop, "AAAA"}, scopeMatched("#be"), RouteFlood, recent)

	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", []string{testFullPubkeyA, pkOther})
	if err != nil {
		t.Fatal(err)
	}
	agg := got[testFullPubkeyA]
	if agg == nil || agg.scopes["be"] == nil || agg.scopes["be"].Packets != 1 {
		t.Fatalf("want the 8-char hop attributed to its sole matching target, got %+v", got)
	}
	if agg.ambiguousHops != 0 {
		t.Errorf("ambiguousHops = %d, want 0 — the two targets diverge before hop length 8", agg.ambiguousHops)
	}
	if other := got[pkOther]; other != nil && (len(other.scopes) != 0 || other.ambiguousHops != 0) {
		t.Errorf("pkOther = %+v, want no attribution and no ambiguity — the hop is not its prefix", other)
	}
}

// TestScopeAuditForwardingCountsUnmatchedPackets: a transport-scoped packet
// whose code1 matched no configured region key is stored with scope_name = ""
// (scopeNameForDB's "transport-scoped but unnameable" state). It is not a
// named scope, so it must not enter agg.scopes, and it is not an unscoped
// plain flood either, so it must not enter unscopedPackets. It is its own
// fact: this instance saw the target forward traffic it holds no key for.
//
// Without this counter the audit reports the declared region as "not
// observed", which reads as a finding about the repeater when it is really a
// gap in this instance's own hashRegions.

// TestScopeAuditTTLForSevenDayWindow pins the per-window TTL. The 7d window
// costs a different order of magnitude than the others (16.7s against 4.0s and
// 0.15s, measured on the live-shaped staging database on 2026-09-07), so it is
// deliberately not on the 30s the other two share. A future edit that collapses
// this back to one constant should have to delete a test that says why.
func TestScopeAuditTTLForSevenDayWindow(t *testing.T) {
	if got := scopeAuditTTLFor("7d"); got != 5*time.Minute {
		t.Errorf("scopeAuditTTLFor(7d) = %s, want 5m", got)
	}
	for _, w := range []string{"1h", "24h", ""} {
		if got := scopeAuditTTLFor(w); got != 30*time.Second {
			t.Errorf("scopeAuditTTLFor(%q) = %s, want 30s", w, got)
		}
	}
}

// TestHandleScopeAuditServesSecondRequestFromCache pins the cache path itself,
// which the singleflight rewrite moved out of the handler and into
// scopeAuditCached/scopeAuditStore. A declared row inserted between two
// requests inside the TTL must NOT appear in the second response: if it does,
// the response was recomputed and the cache is not being consulted.

// TestHandleScopeAuditServesSecondRequestFromCache pins the cache path itself,
// which the singleflight rewrite moved out of the handler and into
// scopeAuditCached/scopeAuditStore. A declared row inserted between two
// requests inside the TTL must NOT appear in the second response: if it does,
// the response was recomputed and the cache is not being consulted.
func TestHandleScopeAuditServesSecondRequestFromCache(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	now := time.Now().UTC().Format(time.RFC3339)
	insertDeclared(t, srv, testFullPubkeyA, now, "be", 0)

	first := getScopeAudit(t, router, "")
	if len(first.Repeaters) != 1 {
		t.Fatalf("first call repeaters = %d, want 1", len(first.Repeaters))
	}

	insertDeclared(t, srv, testFullPubkeyB, now, "be", 0)
	second := getScopeAudit(t, router, "")
	if len(second.Repeaters) != 1 {
		t.Errorf("second call repeaters = %d, want 1 — the row added after the first call proves the cache was bypassed", len(second.Repeaters))
	}
}

// TestHandleScopeAuditNormalisesHashPrefix pins trap 1: transmissions.scope_name
// keeps the '#' (hashRegions config), regions_csv arrives from the firmware
// with it already stripped. Declared "be-van" and observed "#be-van" must be
// recognised as the same scope, not reported as both missing and undeclared.

// TestScopeAuditForwardingCountsUnmatchedPackets: a transport-scoped packet
// whose code1 matched no configured region key is stored with scope_name = ""
// (scopeNameForDB's "transport-scoped but unnameable" state). It is not a
// named scope, so it must not enter agg.scopes, and it is not an unscoped
// plain flood either, so it must not enter unscopedPackets. It is its own
// fact: this instance saw the target forward traffic it holds no key for.
//
// Without this counter the audit reports the declared region as "not
// observed", which reads as a finding about the repeater when it is really a
// gap in this instance's own hashRegions.
func TestScopeAuditForwardingCountsUnmatchedPackets(t *testing.T) {
	s := newScopeTestStore(t)
	hop := testFullPubkeyA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, s, hop, scopeUnmatched(), RouteFlood, recent)

	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", []string{testFullPubkeyA})
	if err != nil {
		t.Fatal(err)
	}
	agg := got[testFullPubkeyA]
	if agg == nil {
		t.Fatalf("want an agg for the target, got none (result = %+v)", got)
	}
	if agg.unmatchedPackets != 1 {
		t.Errorf("unmatchedPackets = %d, want 1", agg.unmatchedPackets)
	}
	if len(agg.scopes) != 0 {
		t.Errorf("scopes = %+v, want empty — an unmatched packet names no region", agg.scopes)
	}
	if agg.unscopedPackets != 0 {
		t.Errorf("unscopedPackets = %d, want 0 — unmatched is not the same as unscoped", agg.unscopedPackets)
	}
}

// TestScopeAuditForwardingCountsUnmatchedOnMidPathHop is the post-M0 case that
// carries almost all of this counter's real volume: before M0 only a last hop
// was attributed, so a repeater deep in a flood path contributed nothing at
// all. Now every hop counts, and the same de-duplication that protects the
// named-scope tally must protect this one — a target appearing twice in one
// path is still one packet, not two.
func TestScopeAuditForwardingCountsUnmatchedOnMidPathHop(t *testing.T) {
	s := newScopeTestStore(t)
	hop := testFullPubkeyA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionPathAt(t, s, []string{"AAAA", hop, "BBBB", hop}, scopeUnmatched(), RouteFlood, recent)

	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", []string{testFullPubkeyA})
	if err != nil {
		t.Fatal(err)
	}
	agg := got[testFullPubkeyA]
	if agg == nil {
		t.Fatalf("want an agg for the mid-path target, got none (result = %+v)", got)
	}
	if agg.unmatchedPackets != 1 {
		t.Errorf("unmatchedPackets = %d, want 1 — one transmission, matched on two of its hops", agg.unmatchedPackets)
	}
}

// TestHandleScopeAuditSurfacesUnmatchedPackets: a repeater declares "behss",
// and this instance sees it forward transport-scoped traffic it cannot name.
// The row must still list "behss" as notObserved — an unmatched packet names
// no region, so it cannot satisfy the declaration — but it must also carry
// observedUnmatchedPackets, so a client can say the finding might be a missing
// region key rather than a silent repeater.
func TestHandleScopeAuditSurfacesUnmatchedPackets(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "behss", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, pk[:4], scopeUnmatched(), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 {
		t.Fatalf("repeaters = %+v, want 1", got.Repeaters)
	}
	row := got.Repeaters[0]
	if row.ObservedUnmatchedPackets != 1 {
		t.Errorf("observedUnmatchedPackets = %d, want 1", row.ObservedUnmatchedPackets)
	}
	if len(row.NotObserved) != 1 || row.NotObserved[0] != "behss" {
		t.Errorf("notObserved = %v, want [\"behss\"] — an unmatched packet names no region and cannot satisfy a declaration", row.NotObserved)
	}
	if row.WildcardContradiction {
		t.Error("wildcardContradiction = true, want false — unmatched traffic is scoped, so it says nothing about '*'")
	}
}
