package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
)

func serveReach(srv *Server, path string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/api/nodes/{pubkey}/reach", srv.handleNodeReach).Methods("GET")
	req := httptest.NewRequest("GET", path, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// pk64 pads a short hex stem to a full 64-char lowercase pubkey.
func pk64(stem string) string { return stem + strings.Repeat("0", 64-len(stem)) }

// resetReachState clears the per-server reach caches so test order cannot
// leak observable state between handler tests (and restores after the test).
// Now operates on *Server (was package globals — Independent r2 #2); accepts
// a variadic *Server so existing call sites that didn't pass one still
// compile but the reset is a no-op (used by tests that build the Server
// fresh and don't need state cleared).
func resetReachState(t *testing.T, servers ...*Server) {
	t.Helper()
	clear := func() {
		for _, s := range servers {
			if s == nil {
				continue
			}
			s.reach.cacheMu.Lock()
			s.reach.cache = map[string]reachCacheEntry{}
			s.reach.cacheMu.Unlock()
			s.reach.degreeMu.Lock()
			s.reach.degreeSnap = nil
			s.reach.degreeMu.Unlock()
		}
	}
	clear()
	t.Cleanup(clear)
}

// newReachIntegrationDB builds a complete observer_idx-schema DB with a target
// node N, two neighbours A/B, and one observation on obsPath so the HTTP handler
// exercises real directional attribution. Pass a path that omits N's token to
// build the zero-reach case (identifiable node, no matching observations).
func newReachIntegrationDB(t *testing.T, obsPath string) (*DB, string) {
	t.Helper()
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	n := pk64("01fa") // target — unique 2-byte token "01fa"
	a := pk64("aabb") // predecessor → we hear A
	b := pk64("ccdd") // successor → B hears us
	now := time.Now().Unix()
	stmts := []string{
		`CREATE TABLE nodes (public_key TEXT, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, advert_count INTEGER)`,
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, from_pubkey TEXT, payload_type INTEGER)`,
		`CREATE TABLE observers (id TEXT, inactive INTEGER)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_idx INTEGER, snr REAL, path_json TEXT, timestamp INTEGER)`,
		`CREATE TABLE neighbor_edges (node_a TEXT, node_b TEXT, count INTEGER)`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	ins := []struct {
		q    string
		args []interface{}
	}{
		{`INSERT INTO nodes VALUES (?, 'N', 'repeater', 50.9, 5.4, ?, '2026-06-01T00:00:00Z', 3)`, []interface{}{n, "2026-06-07T00:00:00Z"}},
		{`INSERT INTO nodes VALUES (?, 'A', 'repeater', 51.0, 5.5, ?, '2026-06-01T00:00:00Z', 1)`, []interface{}{a, "2026-06-07T00:00:00Z"}},
		{`INSERT INTO nodes VALUES (?, 'B', 'repeater', 51.1, 5.6, ?, '2026-06-01T00:00:00Z', 1)`, []interface{}{b, "2026-06-07T00:00:00Z"}},
		{`INSERT INTO observers (id) VALUES ('OBS1')`, nil},
		{`INSERT INTO transmissions (id, from_pubkey, payload_type) VALUES (1, '', 5)`, nil},
		{`INSERT INTO observations (id, transmission_id, observer_idx, snr, path_json, timestamp) VALUES (1,1,1,-7.0,?,?)`, []interface{}{obsPath, now}},
	}
	for _, in := range ins {
		if _, err := conn.Exec(in.q, in.args...); err != nil {
			t.Fatal(err)
		}
	}
	return &DB{conn: conn, isV3: true}, n
}

func TestClampDays(t *testing.T) {
	cases := []struct{ in, want int }{{0, 1}, {-5, 1}, {1, 1}, {7, 7}, {30, 30}, {31, 30}, {999, 30}}
	for _, c := range cases {
		if got := clampDays(c.in); got != c.want {
			t.Errorf("clampDays(%d)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestNodeReach_UnknownNode(t *testing.T) {
	srv := makeTestServer(makeTestGraph()) // no store/db wired → 404
	rr := serveReach(srv, "/api/nodes/"+pk64("deadbeef")+"/reach")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rr.Code)
	}
}

func TestNodeReach_InvalidPubkey(t *testing.T) {
	srv := makeTestServer(makeTestGraph())
	for _, bad := range []string{"deadbeef", "xyz", pk64("01") + "zz"} {
		rr := serveReach(srv, "/api/nodes/"+bad+"/reach")
		if rr.Code != http.StatusBadRequest {
			t.Errorf("pubkey %q: status=%d want 400", bad, rr.Code)
		}
	}
}

func TestNodeReach_ValidPubkeyNotInNodes(t *testing.T) {
	resetReachState(t)
	db := setupTestDBv2(t)
	cfg := &Config{}
	srv := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}
	// Syntactically valid pubkey that was never inserted → real 404 path.
	rr := serveReach(srv, "/api/nodes/"+pk64("beef")+"/reach")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestNodeReach_BlacklistedReturns404(t *testing.T) {
	pk := pk64("01fa")
	cfg := &Config{NodeBlacklist: []string{pk}}
	srv := &Server{cfg: cfg}
	rr := serveReach(srv, "/api/nodes/"+pk+"/reach")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("blacklisted pubkey: status=%d want 404", rr.Code)
	}
}

func TestNodeReach_AttributionAndCacheHit(t *testing.T) {
	resetReachState(t)
	db, n := newReachIntegrationDB(t, `["AABB","01FA","CCDD"]`)
	defer db.conn.Close()
	cfg := &Config{}
	srv := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}

	rr := serveReach(srv, "/api/nodes/"+n+"/reach?days=30")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	var resp NodeReachResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.Importance.RelayObservations < 1 {
		t.Fatalf("expected ≥1 relay observation, got %d", resp.Importance.RelayObservations)
	}
	var weHearA, theyHearB bool
	for _, l := range resp.Links {
		if l.Name == "A" && l.WeHear >= 1 {
			weHearA = true
		}
		if l.Name == "B" && l.TheyHear >= 1 {
			theyHearB = true
		}
	}
	if !weHearA {
		t.Errorf("expected we_hear≥1 for neighbour A, links=%+v", resp.Links)
	}
	if !theyHearB {
		t.Errorf("expected they_hear≥1 for neighbour B, links=%+v", resp.Links)
	}

	// Cache hit: the key (now generation-suffixed, #1629) must be populated
	// and a second request must 200.
	wantKey := n + "|30|g" + strconv.FormatUint(srv.cfg.BlacklistGeneration(), 10)
	if _, ok := srv.reachCacheGet(wantKey); !ok {
		t.Fatalf("expected reach response to be cached under %q", wantKey)
	}
	rr2 := serveReach(srv, "/api/nodes/"+n+"/reach?days=30")
	if rr2.Code != http.StatusOK || rr2.Body.String() != rr.Body.String() {
		t.Fatalf("cache-hit response differs: code=%d", rr2.Code)
	}
}

// Zero-reach happy path: a node that IS identifiable (has reliable tokens) but
// whose observations contain none of its tokens must return 200 with empty
// arrays — NOT 404. A wrong implementation that 404s here passes every other
// test. (docs/api-spec.md contract.)
func TestNodeReach_ZeroReach(t *testing.T) {
	resetReachState(t)
	db, n := newReachIntegrationDB(t, `["AABB","CCDD"]`) // path omits N's "01FA" token
	defer db.conn.Close()
	cfg := &Config{}
	srv := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}

	rr := serveReach(srv, "/api/nodes/"+n+"/reach?days=30")
	if rr.Code != http.StatusOK {
		t.Fatalf("zero-reach must be 200 not 404, got %d (body=%s)", rr.Code, rr.Body.String())
	}
	var resp NodeReachResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.ReliableTokens) == 0 {
		t.Fatalf("node should still be identifiable (reliable tokens present)")
	}
	if len(resp.Links) != 0 || len(resp.DirectObservers) != 0 || resp.Importance.RelayObservations != 0 {
		t.Fatalf("expected empty reach, got links=%d obs=%d relay=%d",
			len(resp.Links), len(resp.DirectObservers), resp.Importance.RelayObservations)
	}
}

func TestNodeReach_ShapeAndClamp(t *testing.T) {
	resetReachState(t)
	db := setupTestDBv2(t)
	const pk = "01fa326b475800a31105abcb9e4cac000b3e5d9e2b5ba0739981ce8d5f3a6754"
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('`+pk+`', 'BE-Test', 'repeater', 50.9, 5.4, '2026-06-07T00:00:00Z', '2026-06-01T00:00:00Z', 3)`)
	// scanReachRows joins observations on observer_idx; the v2 schema's
	// observations table lacks that column. Previously the scan error was
	// swallowed (issue #1631) and the test still saw empty arrays. With the
	// fix that returns 500, we rebuild observations to the observer_idx
	// shape (empty — no rows needed for shape/clamp assertions).
	mustExecDB(t, db, `DROP TABLE observations`)
	// PREFLIGHT: async=true reason="test-only in-memory schema rebuild; not a production migration"
	mustExecDB(t, db, `CREATE TABLE observations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transmission_id INTEGER,
		observer_idx INTEGER,
		snr REAL,
		path_json TEXT,
		timestamp INTEGER
	)`)

	cfg := &Config{}
	srv := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}

	rr := serveReach(srv, "/api/nodes/"+pk+"/reach?days=999")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	var resp NodeReachResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.Window.Days != 30 {
		t.Fatalf("days not clamped to 30: %d", resp.Window.Days)
	}
	if resp.Links == nil || resp.DirectObservers == nil || resp.ReliableTokens == nil {
		t.Fatalf("array fields must be non-nil (never null)")
	}
	if !contains(resp.ReliableTokens, "01FA") {
		t.Fatalf("expected 01FA reliable token, got %v", resp.ReliableTokens)
	}
	if resp.Node.FirstSeen != "2026-06-01T00:00:00Z" {
		t.Fatalf("first_seen not sourced from nodes table: %q", resp.Node.FirstSeen)
	}
}

// Issue #1631: a DB failure inside scanReachRows must surface as 500, not
// as a misleading "no reach" 200 or 404. We warm the integration DB, drop
// the observations table so the next reach scan query fails inside
// QueryContext, then assert the handler returns 500 (not 200 with empty
// arrays, which is the buggy current behavior — scanReachRows swallows the
// error and returns nil).
func TestNodeReach_ScanDBErrorReturns500(t *testing.T) {
	resetReachState(t)
	db, n := newReachIntegrationDB(t, `["AABB","01FA","CCDD"]`)
	defer db.conn.Close()
	cfg := &Config{}
	srv := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}

	// Warm the store's node cache (so buildNodeInfoMap on the failing call
	// still finds the target node). One healthy call also primes the
	// reach response cache — clear it below so the next call recomputes.
	if rr := serveReach(srv, "/api/nodes/"+n+"/reach?days=30"); rr.Code != http.StatusOK {
		t.Fatalf("warm-up call: status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	srv.reach.cacheMu.Lock()
	srv.reach.cache = map[string]reachCacheEntry{}
	srv.reach.cacheMu.Unlock()

	// Break the table that scanReachRows reads from. nodes / observers /
	// neighbor_edges remain intact so the failure is isolated to the
	// scanReachRows QueryContext path.
	if _, err := db.conn.Exec("DROP TABLE observations"); err != nil {
		t.Fatalf("drop observations: %v", err)
	}

	rr := serveReach(srv, "/api/nodes/"+n+"/reach?days=30")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on DB error inside scanReachRows, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
