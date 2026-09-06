package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// The packet detail panel (public/packets.js) renders a "Scope" row gated on
// `pkt.scope_name != null`, distinguishing three states that transmissions.scope_name
// encodes: SQL NULL (not transport-scoped, row hidden), "" (transport-scoped but the
// region did not match a configured key → "unknown scope") and "#name" (matched
// region). txToMap is the shape /api/packets and /api/packets/{id} serve from the
// in-memory store, so it must carry all three states through.

func TestTxToMapScopeNameMatchedRegion(t *testing.T) {
	scope := "#belgium"
	m := txToMap(&StoreTx{ID: 1, Hash: "aa", ScopeName: &scope})
	if m["scope_name"] != "#belgium" {
		t.Errorf("scope_name = %#v, want %q", m["scope_name"], "#belgium")
	}
}

func TestTxToMapScopeNameUnknownScope(t *testing.T) {
	scope := ""
	m := txToMap(&StoreTx{ID: 1, Hash: "aa", ScopeName: &scope})
	v, ok := m["scope_name"]
	if !ok {
		t.Fatal("scope_name key missing for a transport-scoped packet with an unmatched region")
	}
	if v != "" {
		t.Errorf("scope_name = %#v, want %q (frontend renders this as 'unknown scope')", v, "")
	}
}

func TestTxToMapScopeNameNotTransportScoped(t *testing.T) {
	m := txToMap(&StoreTx{ID: 1, Hash: "aa", ScopeName: nil})
	if m["scope_name"] != nil {
		t.Errorf("scope_name = %#v, want nil for a non-transport-scoped packet", m["scope_name"])
	}
	// A typed nil *string in the map would marshal as null but compare non-nil in
	// Go; assert the JSON the browser actually receives.
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["scope_name"] != nil {
		t.Errorf("JSON scope_name = %#v, want null", decoded["scope_name"])
	}
}

// TestPacketDetailExposesScopeName is the end-to-end guard: the packet-detail
// endpoint is served from the in-memory store, so scope_name must survive the
// SQL scan (nullStrPtr) and the map conversion (txToMap) with all three states
// intact. Before this test, txToMap dropped the field entirely and the Scope row
// only ever rendered for packets old enough to fall through to the DB.
func TestPacketDetailExposesScopeName(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.conn.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL`); err != nil {
		t.Fatalf("add scope_name column: %v", err)
	}
	db.hasScopeName = true

	now := time.Now().UTC().Format(time.RFC3339)
	// route_type 1 = FLOOD (never transport-scoped → NULL); 0 = TRANSPORT_FLOOD.
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
		VALUES ('AABB', 'aaaaaaaaaaaaaaa1', ?, 1, 4)`, now); err != nil {
		t.Fatalf("insert unscoped: %v", err)
	}
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES ('AABB', 'aaaaaaaaaaaaaaa2', ?, 0, 4, '')`, now); err != nil {
		t.Fatalf("insert unknown-scope: %v", err)
	}
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES ('AABB', 'aaaaaaaaaaaaaaa3', ?, 0, 4, '#belgium')`, now); err != nil {
		t.Fatalf("insert matched-scope: %v", err)
	}

	srv := NewServer(db, &Config{Port: 3000}, NewHub())
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("background indexes never became ready")
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	cases := []struct {
		name string
		hash string
		want interface{}
	}{
		{"not transport-scoped", "aaaaaaaaaaaaaaa1", nil},
		{"transport-scoped, region unmatched", "aaaaaaaaaaaaaaa2", ""},
		{"transport-scoped, region matched", "aaaaaaaaaaaaaaa3", "#belgium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if store.GetPacketByHash(tc.hash) == nil {
				t.Fatalf("precondition: %s not in store (would hit the DB fallback)", tc.hash)
			}
			req := httptest.NewRequest("GET", "/api/packets/"+tc.hash, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
			}
			var body map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			pkt, ok := body["packet"].(map[string]interface{})
			if !ok {
				t.Fatal("expected packet object")
			}
			if got := pkt["scope_name"]; got != tc.want {
				t.Errorf("scope_name = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// --- grouped view ---
//
// The Packets tab defaults to "Group by Hash", which is served by a separate
// mapper (groupedTxsToPage in the store, a dedicated query in the DB fallback).
// The Scope column reads scope_name off those rows, so both paths must carry it.

func TestGroupedTxsToPageCarriesScopeName(t *testing.T) {
	matched := "#belgium"
	unmatched := ""
	txs := []*StoreTx{
		{ID: 1, Hash: "aa", ScopeName: &matched},
		{ID: 2, Hash: "bb", ScopeName: &unmatched},
		{ID: 3, Hash: "cc", ScopeName: nil},
	}
	res := groupedTxsToPage(txs, len(txs), 0, len(txs))
	want := []interface{}{"#belgium", "", nil}
	for i, w := range want {
		if got := res.Packets[i]["scope_name"]; got != w {
			t.Errorf("packet %d: scope_name = %#v, want %#v", i, got, w)
		}
	}
}

func TestGroupedPacketsEndpointExposesScopeName(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.conn.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL`); err != nil {
		t.Fatalf("add scope_name column: %v", err)
	}
	db.hasScopeName = true
	if _, err := db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
		VALUES ('AABB', 'bbbbbbbbbbbbbbb1', ?, 1, 4)`, now); err != nil {
		t.Fatalf("insert unscoped: %v", err)
	}
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES ('AABB', 'bbbbbbbbbbbbbbb2', ?, 0, 4, '')`, now); err != nil {
		t.Fatalf("insert unknown-scope: %v", err)
	}
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES ('AABB', 'bbbbbbbbbbbbbbb3', ?, 0, 4, '#belgium')`, now); err != nil {
		t.Fatalf("insert matched-scope: %v", err)
	}

	want := map[string]interface{}{
		"bbbbbbbbbbbbbbb1": nil,
		"bbbbbbbbbbbbbbb2": "",
		"bbbbbbbbbbbbbbb3": "#belgium",
	}

	// Both the store-backed path and the DB-only fallback must agree.
	for _, withStore := range []bool{true, false} {
		name := "store"
		if !withStore {
			name = "db"
		}
		t.Run(name, func(t *testing.T) {
			srv := NewServer(db, &Config{Port: 3000}, NewHub())
			if withStore {
				store := NewPacketStore(db, nil)
				if err := store.Load(); err != nil {
					t.Fatalf("store.Load: %v", err)
				}
				if !store.WaitIndexesReady(5 * time.Second) {
					t.Fatal("background indexes never became ready")
				}
				srv.store = store
			}
			router := mux.NewRouter()
			srv.RegisterRoutes(router)

			req := httptest.NewRequest("GET", "/api/packets?groupByHash=true&limit=50", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
			}
			var body struct {
				Packets []map[string]interface{} `json:"packets"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(body.Packets) != 3 {
				t.Fatalf("expected 3 grouped packets, got %d", len(body.Packets))
			}
			for _, p := range body.Packets {
				h, _ := p["hash"].(string)
				if got := p["scope_name"]; got != want[h] {
					t.Errorf("%s: scope_name = %#v, want %#v", h, got, want[h])
				}
			}
		})
	}
}
