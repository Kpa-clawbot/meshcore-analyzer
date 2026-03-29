package main

// parity_test.go — Golden fixture shape tests.
// Validates that Go API responses match the shape of Node.js API responses.
// Shapes were captured from the production Node.js server and stored in
// testdata/golden/shapes.json.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// shapeSpec describes the expected JSON structure from the Node.js server.
type shapeSpec struct {
	Type         string               `json:"type"`
	Keys         map[string]shapeSpec `json:"keys,omitempty"`
	ElementShape *shapeSpec           `json:"elementShape,omitempty"`
	DynamicKeys  bool                 `json:"dynamicKeys,omitempty"`
	ValueShape   *shapeSpec           `json:"valueShape,omitempty"`
	RequiredKeys map[string]shapeSpec `json:"requiredKeys,omitempty"`
}

// loadShapes reads testdata/golden/shapes.json relative to this source file.
func loadShapes(t *testing.T) map[string]shapeSpec {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	data, err := os.ReadFile(filepath.Join(dir, "testdata", "golden", "shapes.json"))
	if err != nil {
		t.Fatalf("cannot load shapes.json: %v", err)
	}
	var shapes map[string]shapeSpec
	if err := json.Unmarshal(data, &shapes); err != nil {
		t.Fatalf("cannot parse shapes.json: %v", err)
	}
	return shapes
}

// validateShape recursively checks that `actual` matches the expected `spec`.
// `path` tracks the JSON path for error messages.
// Returns a list of mismatch descriptions.
func validateShape(actual interface{}, spec shapeSpec, path string) []string {
	var errs []string

	switch spec.Type {
	case "null", "nullable":
		// nullable means: value can be null OR matching type. Accept anything.
		return nil
	case "nullable_number":
		// Can be null or number
		if actual != nil {
			if _, ok := actual.(float64); !ok {
				errs = append(errs, fmt.Sprintf("%s: expected number or null, got %T", path, actual))
			}
		}
		return errs
	case "string":
		if actual == nil {
			errs = append(errs, fmt.Sprintf("%s: expected string, got null", path))
		} else if _, ok := actual.(string); !ok {
			errs = append(errs, fmt.Sprintf("%s: expected string, got %T", path, actual))
		}
	case "number":
		if actual == nil {
			errs = append(errs, fmt.Sprintf("%s: expected number, got null", path))
		} else if _, ok := actual.(float64); !ok {
			errs = append(errs, fmt.Sprintf("%s: expected number, got %T (%v)", path, actual, actual))
		}
	case "boolean":
		if actual == nil {
			errs = append(errs, fmt.Sprintf("%s: expected boolean, got null", path))
		} else if _, ok := actual.(bool); !ok {
			errs = append(errs, fmt.Sprintf("%s: expected boolean, got %T", path, actual))
		}
	case "array":
		if actual == nil {
			errs = append(errs, fmt.Sprintf("%s: expected array, got null (arrays must be [] not null)", path))
			return errs
		}
		arr, ok := actual.([]interface{})
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: expected array, got %T", path, actual))
			return errs
		}
		if spec.ElementShape != nil && len(arr) > 0 {
			errs = append(errs, validateShape(arr[0], *spec.ElementShape, path+"[0]")...)
		}
	case "object":
		if actual == nil {
			errs = append(errs, fmt.Sprintf("%s: expected object, got null", path))
			return errs
		}
		obj, ok := actual.(map[string]interface{})
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: expected object, got %T", path, actual))
			return errs
		}

		if spec.DynamicKeys {
			// Object with dynamic keys — validate value shapes
			if spec.ValueShape != nil && len(obj) > 0 {
				for k, v := range obj {
					errs = append(errs, validateShape(v, *spec.ValueShape, path+"."+k)...)
					break // check just one sample
				}
			}
			if spec.RequiredKeys != nil {
				for rk, rs := range spec.RequiredKeys {
					v, exists := obj[rk]
					if !exists {
						errs = append(errs, fmt.Sprintf("%s: missing required key %q in dynamic-key object", path, rk))
					} else {
						errs = append(errs, validateShape(v, rs, path+"."+rk)...)
					}
				}
			}
		} else if spec.Keys != nil {
			// Object with known keys — check each expected key exists and has correct type
			for key, keySpec := range spec.Keys {
				val, exists := obj[key]
				if !exists {
					errs = append(errs, fmt.Sprintf("%s: missing field %q (expected %s)", path, key, keySpec.Type))
				} else {
					errs = append(errs, validateShape(val, keySpec, path+"."+key)...)
				}
			}
		}
	}

	return errs
}

// parityEndpoint defines one endpoint to test for parity.
type parityEndpoint struct {
	name string // key in shapes.json
	path string // HTTP path to request
}

func TestParityShapes(t *testing.T) {
	shapes := loadShapes(t)
	_, router := setupTestServer(t)

	endpoints := []parityEndpoint{
		{"stats", "/api/stats"},
		{"nodes", "/api/nodes?limit=5"},
		{"packets", "/api/packets?limit=5"},
		{"packets_grouped", "/api/packets?limit=5&groupByHash=true"},
		{"observers", "/api/observers"},
		{"channels", "/api/channels"},
		{"channel_messages", "/api/channels/0000000000000000/messages?limit=5"},
		{"analytics_rf", "/api/analytics/rf?days=7"},
		{"analytics_topology", "/api/analytics/topology?days=7"},
		{"analytics_hash_sizes", "/api/analytics/hash-sizes?days=7"},
		{"analytics_distance", "/api/analytics/distance?days=7"},
		{"analytics_subpaths", "/api/analytics/subpaths?days=7"},
		{"bulk_health", "/api/nodes/bulk-health"},
		{"health", "/api/health"},
		{"perf", "/api/perf"},
	}

	for _, ep := range endpoints {
		t.Run("Parity_"+ep.name, func(t *testing.T) {
			spec, ok := shapes[ep.name]
			if !ok {
				t.Fatalf("no shape spec found for %q in shapes.json", ep.name)
			}

			req := httptest.NewRequest("GET", ep.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != 200 {
				t.Fatalf("GET %s returned %d, expected 200. Body: %s",
					ep.path, w.Code, w.Body.String())
			}

			var body interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET %s returned invalid JSON: %v\nBody: %s",
					ep.path, err, w.Body.String())
			}

			mismatches := validateShape(body, spec, ep.path)
			if len(mismatches) > 0 {
				t.Errorf("Go %s has %d shape mismatches vs Node.js golden:\n  %s",
					ep.path, len(mismatches), strings.Join(mismatches, "\n  "))
			}
		})
	}
}

// TestParityNodeDetail tests node detail endpoint shape.
// Uses a known test node public key from seeded data.
func TestParityNodeDetail(t *testing.T) {
	shapes := loadShapes(t)
	_, router := setupTestServer(t)

	spec, ok := shapes["node_detail"]
	if !ok {
		t.Fatal("no shape spec for node_detail in shapes.json")
	}

	req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("node detail returned %d: %s", w.Code, w.Body.String())
	}

	var body interface{}
	json.Unmarshal(w.Body.Bytes(), &body)

	mismatches := validateShape(body, spec, "/api/nodes/{pubkey}")
	if len(mismatches) > 0 {
		t.Errorf("Go node detail has %d shape mismatches vs Node.js golden:\n  %s",
			len(mismatches), strings.Join(mismatches, "\n  "))
	}
}

// TestParityArraysNotNull verifies that array-typed fields in Go responses are
// [] (empty array) rather than null. This is a common Go/JSON pitfall where
// nil slices marshal as null instead of [].
// Uses shapes.json to know which fields SHOULD be arrays.
func TestParityArraysNotNull(t *testing.T) {
	shapes := loadShapes(t)
	_, router := setupTestServer(t)

	endpoints := []struct {
		name string
		path string
	}{
		{"stats", "/api/stats"},
		{"nodes", "/api/nodes?limit=5"},
		{"packets", "/api/packets?limit=5"},
		{"packets_grouped", "/api/packets?limit=5&groupByHash=true"},
		{"observers", "/api/observers"},
		{"channels", "/api/channels"},
		{"bulk_health", "/api/nodes/bulk-health"},
		{"analytics_rf", "/api/analytics/rf?days=7"},
		{"analytics_topology", "/api/analytics/topology?days=7"},
		{"analytics_hash_sizes", "/api/analytics/hash-sizes?days=7"},
		{"analytics_distance", "/api/analytics/distance?days=7"},
		{"analytics_subpaths", "/api/analytics/subpaths?days=7"},
	}

	for _, ep := range endpoints {
		t.Run("NullArrayCheck_"+ep.name, func(t *testing.T) {
			spec, ok := shapes[ep.name]
			if !ok {
				t.Skipf("no shape spec for %s", ep.name)
			}

			req := httptest.NewRequest("GET", ep.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != 200 {
				t.Skipf("GET %s returned %d, skipping null-array check", ep.path, w.Code)
			}

			var body interface{}
			json.Unmarshal(w.Body.Bytes(), &body)

			nullArrays := findNullArrays(body, spec, ep.path)
			if len(nullArrays) > 0 {
				t.Errorf("Go %s has null where [] expected:\n  %s\n"+
					"Go nil slices marshal as null — initialize with make() or literal",
					ep.path, strings.Join(nullArrays, "\n  "))
			}
		})
	}
}

// findNullArrays walks JSON data alongside a shape spec and returns paths
// where the spec says the field should be an array but Go returned null.
func findNullArrays(actual interface{}, spec shapeSpec, path string) []string {
	var nulls []string

	switch spec.Type {
	case "array":
		if actual == nil {
			nulls = append(nulls, fmt.Sprintf("%s: null (should be [])", path))
		} else if arr, ok := actual.([]interface{}); ok && spec.ElementShape != nil {
			for i, elem := range arr {
				nulls = append(nulls, findNullArrays(elem, *spec.ElementShape, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case "object":
		obj, ok := actual.(map[string]interface{})
		if !ok || obj == nil {
			return nulls
		}
		if spec.Keys != nil {
			for key, keySpec := range spec.Keys {
				if val, exists := obj[key]; exists {
					nulls = append(nulls, findNullArrays(val, keySpec, path+"."+key)...)
				} else if keySpec.Type == "array" {
					// Key missing entirely — also a null-array problem
					nulls = append(nulls, fmt.Sprintf("%s.%s: missing (should be [])", path, key))
				}
			}
		}
		if spec.DynamicKeys && spec.ValueShape != nil {
			for k, v := range obj {
				nulls = append(nulls, findNullArrays(v, *spec.ValueShape, path+"."+k)...)
				break // sample one
			}
		}
	}

	return nulls
}

// TestParityHealthEngine verifies Go health endpoint declares engine=go
// while Node declares engine=node (or omits it). The Go server must always
// identify itself.
func TestParityHealthEngine(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)

	engine, ok := body["engine"]
	if !ok {
		t.Error("health response missing 'engine' field (Go server must include engine=go)")
	} else if engine != "go" {
		t.Errorf("health engine=%v, expected 'go'", engine)
	}
}

// TestValidateShapeFunction directly tests the shape validator itself.
func TestValidateShapeFunction(t *testing.T) {
	t.Run("string match", func(t *testing.T) {
		errs := validateShape("hello", shapeSpec{Type: "string"}, "$.x")
		if len(errs) != 0 {
			t.Errorf("unexpected errors: %v", errs)
		}
	})

	t.Run("string mismatch", func(t *testing.T) {
		errs := validateShape(42.0, shapeSpec{Type: "string"}, "$.x")
		if len(errs) != 1 {
			t.Errorf("expected 1 error, got %d: %v", len(errs), errs)
		}
	})

	t.Run("null array rejected", func(t *testing.T) {
		errs := validateShape(nil, shapeSpec{Type: "array"}, "$.arr")
		if len(errs) != 1 || !strings.Contains(errs[0], "null") {
			t.Errorf("expected null-array error, got: %v", errs)
		}
	})

	t.Run("empty array OK", func(t *testing.T) {
		errs := validateShape([]interface{}{}, shapeSpec{Type: "array"}, "$.arr")
		if len(errs) != 0 {
			t.Errorf("unexpected errors for empty array: %v", errs)
		}
	})

	t.Run("missing object key", func(t *testing.T) {
		spec := shapeSpec{Type: "object", Keys: map[string]shapeSpec{
			"name": {Type: "string"},
			"age":  {Type: "number"},
		}}
		obj := map[string]interface{}{"name": "test"}
		errs := validateShape(obj, spec, "$.user")
		if len(errs) != 1 || !strings.Contains(errs[0], "age") {
			t.Errorf("expected missing age error, got: %v", errs)
		}
	})

	t.Run("nullable allows null", func(t *testing.T) {
		errs := validateShape(nil, shapeSpec{Type: "nullable"}, "$.x")
		if len(errs) != 0 {
			t.Errorf("nullable should accept null: %v", errs)
		}
	})

	t.Run("dynamic keys validates value shape", func(t *testing.T) {
		spec := shapeSpec{
			Type:        "object",
			DynamicKeys: true,
			ValueShape:  &shapeSpec{Type: "number"},
		}
		obj := map[string]interface{}{"a": 1.0, "b": 2.0}
		errs := validateShape(obj, spec, "$.dyn")
		if len(errs) != 0 {
			t.Errorf("unexpected errors: %v", errs)
		}
	})
}

func TestParityWSMultiObserverGolden(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	hub := NewHub()
	store := NewPacketStore(db)
	if err := store.Load(); err != nil {
		t.Fatalf("store load failed: %v", err)
	}

	poller := NewPoller(db, hub, 50*time.Millisecond)
	poller.store = store

	client := &Client{send: make(chan []byte, 256)}
	hub.Register(client)
	defer hub.Unregister(client)

	go poller.Start()
	defer poller.Stop()

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('BEEF', 'goldenstarburst237', ?, 1, 4, '{"pubKey":"aabbccdd11223344","type":"ADVERT"}')`, now); err != nil {
		t.Fatalf("insert tx failed: %v", err)
	}
	var txID int
	if err := db.conn.QueryRow(`SELECT id FROM transmissions WHERE hash='goldenstarburst237'`).Scan(&txID); err != nil {
		t.Fatalf("query tx id failed: %v", err)
	}
	ts := time.Now().Unix()
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, 1, 11.0, -88, '["p1"]', ?),
		       (?, 2, 9.0, -92, '["p1","p2"]', ?),
		       (?, 1, 7.0, -96, '["p1","p2","p3"]', ?)`,
		txID, ts, txID, ts+1, txID, ts+2); err != nil {
		t.Fatalf("insert obs failed: %v", err)
	}

	type golden struct {
		Hash        string
		Count       int
		Paths       []string
		ObserverIDs []string
	}
	expected := golden{
		Hash:        "goldenstarburst237",
		Count:       3,
		Paths:       []string{`["p1"]`, `["p1","p2"]`, `["p1","p2","p3"]`},
		ObserverIDs: []string{"obs1", "obs2"},
	}

	gotPaths := make([]string, 0, expected.Count)
	gotObservers := make(map[string]bool)
	deadline := time.After(2 * time.Second)
	for len(gotPaths) < expected.Count {
		select {
		case raw := <-client.send:
			var msg map[string]interface{}
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatalf("unmarshal ws message failed: %v", err)
			}
			if msg["type"] != "packet" {
				continue
			}
			data, _ := msg["data"].(map[string]interface{})
			if data == nil || data["hash"] != expected.Hash {
				continue
			}
			if path, ok := data["path_json"].(string); ok {
				gotPaths = append(gotPaths, path)
			}
			if oid, ok := data["observer_id"].(string); ok && oid != "" {
				gotObservers[oid] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %d ws messages, got %d", expected.Count, len(gotPaths))
		}
	}

	sort.Strings(gotPaths)
	sort.Strings(expected.Paths)
	if len(gotPaths) != len(expected.Paths) {
		t.Fatalf("path count mismatch: got %d want %d", len(gotPaths), len(expected.Paths))
	}
	for i := range expected.Paths {
		if gotPaths[i] != expected.Paths[i] {
			t.Fatalf("path mismatch at %d: got %q want %q", i, gotPaths[i], expected.Paths[i])
		}
	}
	for _, oid := range expected.ObserverIDs {
		if !gotObservers[oid] {
			t.Fatalf("missing expected observer %q in ws messages", oid)
		}
	}
}
