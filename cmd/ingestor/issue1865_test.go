package main

// Tests for #1865: ingest the observer /neighbors report as concrete evidence
// of configured region scopes. Covers handleNeighborsReport dispatch semantics
// and the UpdateNodeConfiguredScope store method (status gating, case folding,
// last-write-wins, and the "absence/timeout is not a signal" contract).

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// seedNode inserts a node into both nodes and inactive_nodes (lowercase key).
func seedNode(t *testing.T, store *Store, pubkey string) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO nodes (public_key, name) VALUES (?, ?)`, pubkey, "n_"+pubkey[:4]); err != nil {
		t.Fatalf("seed node %s: %v", pubkey, err)
	}
	if _, err := store.db.Exec(`INSERT INTO inactive_nodes (public_key, name) VALUES (?, ?)`, pubkey, "n_"+pubkey[:4]); err != nil {
		t.Fatalf("seed inactive node %s: %v", pubkey, err)
	}
}

func configuredScope(t *testing.T, store *Store, pubkey string) (sql.NullString, sql.NullString) {
	t.Helper()
	var sc, at sql.NullString
	if err := store.db.QueryRow(
		`SELECT configured_scope, configured_scope_at FROM nodes WHERE public_key = ?`, pubkey,
	).Scan(&sc, &at); err != nil {
		t.Fatalf("read configured_scope for %s: %v", pubkey, err)
	}
	return sc, at
}

func openNeighborsStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestHandleNeighborsReportWritesSelfAndResponded(t *testing.T) {
	store := openNeighborsStore(t)

	// Report keys are UPPERCASE; nodes.public_key is lowercase hex.
	const originUpper = "FEEDCA4AD4E2AE615AAAB3CB73FAEC6EF0C7AF4D410F5C58A70FC0F724B7C933"
	const respUpper = "B0D17C59FCF580592F8FB78B67D2F0CE9E9187EF3483A765BDFF1D7947A5109C"
	const timeoutUpper = "0CE5EA7CFA3AB01D11810EF56B73DD899CD6C58644D6A6832A5C1AE89AFC5E25"
	originLower := "feedca4ad4e2ae615aaab3cb73faec6ef0c7af4d410f5c58a70fc0f724b7c933"
	respLower := "b0d17c59fcf580592f8fb78b67d2f0ce9e9187ef3483a765bdff1d7947a5109c"
	timeoutLower := "0ce5ea7cfa3ab01d11810ef56b73dd899cd6c58644d6a6832a5c1ae89afc5e25"

	seedNode(t, store, originLower)
	seedNode(t, store, respLower)
	seedNode(t, store, timeoutLower)
	// Pre-existing confirmed scope on the timeout node — a timeout must NOT clobber it.
	if err := store.UpdateNodeConfiguredScope(timeoutLower, "eu", "2026-07-24T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	report := map[string]interface{}{
		"timestamp": "2026-07-25T10:46:14.000000+00:00",
		"origin_id": originUpper,
		"self":      map[string]interface{}{"scopes": "*"},
		"neighbors": []interface{}{
			map[string]interface{}{"pubkey": timeoutUpper, "scopes": "", "status": "timeout"},
			map[string]interface{}{"pubkey": respUpper, "scopes": "de,eu", "status": "responded"},
		},
	}
	handleNeighborsReport(store, "test", "obs-topic-id", report)

	// self scopes written under the lowercased origin_id.
	if sc, _ := configuredScope(t, store, originLower); !sc.Valid || sc.String != "*" {
		t.Errorf("self configured_scope = %v, want '*'", sc)
	}
	// responded neighbor written, lowercased.
	if sc, _ := configuredScope(t, store, respLower); !sc.Valid || sc.String != "de,eu" {
		t.Errorf("responded configured_scope = %v, want 'de,eu'", sc)
	}
	// timeout neighbor untouched — prior confirmed value survives.
	if sc, _ := configuredScope(t, store, timeoutLower); !sc.Valid || sc.String != "eu" {
		t.Errorf("timeout node configured_scope = %v, want prior 'eu' (must not be cleared)", sc)
	}
}

func TestHandleNeighborsReportUnknownNeighborIsNoop(t *testing.T) {
	store := openNeighborsStore(t)
	// No node seeded for this pubkey — UPDATE must match no row (no insert, no error).
	report := map[string]interface{}{
		"timestamp": "2026-07-25T10:46:14Z",
		"neighbors": []interface{}{
			map[string]interface{}{"pubkey": "aa" + "00000000000000000000000000000000000000000000000000000000000000"[2:], "scopes": "de", "status": "responded"},
		},
	}
	handleNeighborsReport(store, "test", "obs", report)
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("nodes count = %d, want 0 (report must not create nodes)", n)
	}
}

func TestHandleNeighborsReportRespondedEmptyScopeIsStored(t *testing.T) {
	store := openNeighborsStore(t)
	pk := "cc00000000000000000000000000000000000000000000000000000000000001"
	seedNode(t, store, pk)
	// A responded query with empty scopes is a valid "no scopes configured".
	report := map[string]interface{}{
		"timestamp": "2026-07-25T10:46:14Z",
		"neighbors": []interface{}{
			map[string]interface{}{"pubkey": pk, "scopes": "", "status": "responded"},
		},
	}
	handleNeighborsReport(store, "test", "obs", report)
	sc, at := configuredScope(t, store, pk)
	if !sc.Valid || sc.String != "" {
		t.Errorf("responded-empty configured_scope = %v, want stored empty string", sc)
	}
	if !at.Valid || at.String == "" {
		t.Errorf("configured_scope_at = %v, want the report timestamp", at)
	}
}

func TestUpdateNodeConfiguredScopeLastWriteWins(t *testing.T) {
	store := openNeighborsStore(t)
	pk := "dd00000000000000000000000000000000000000000000000000000000000001"
	seedNode(t, store, pk)

	if err := store.UpdateNodeConfiguredScope(pk, "eu", "2026-07-25T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	// Older report must NOT clobber the newer confirmed value.
	if err := store.UpdateNodeConfiguredScope(pk, "stale", "2026-07-24T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := configuredScope(t, store, pk); sc.String != "eu" {
		t.Errorf("configured_scope = %q, want 'eu' (older report must not overwrite)", sc.String)
	}
	// Newer report updates.
	if err := store.UpdateNodeConfiguredScope(pk, "de", "2026-07-26T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := configuredScope(t, store, pk); sc.String != "de" {
		t.Errorf("configured_scope = %q, want 'de' (newer report should update)", sc.String)
	}
	// inactive_nodes mirrored.
	var inactive sql.NullString
	if err := store.db.QueryRow(`SELECT configured_scope FROM inactive_nodes WHERE public_key = ?`, pk).Scan(&inactive); err != nil {
		t.Fatal(err)
	}
	if inactive.String != "de" {
		t.Errorf("inactive_nodes.configured_scope = %q, want 'de'", inactive.String)
	}
}

// TestUpdateNodeConfiguredScopeNormalizesAndOrdersByInstant proves the
// last-write-wins guard orders by chronological instant, not by raw string.
// A "+02:00" report that is lexicographically "greater" but chronologically
// EARLIER than the stored UTC value must not win, and stored timestamps are
// canonicalized to UTC "Z" form regardless of the incoming offset/precision.
func TestUpdateNodeConfiguredScopeNormalizesAndOrdersByInstant(t *testing.T) {
	store := openNeighborsStore(t)
	pk := "ee00000000000000000000000000000000000000000000000000000000000001"
	seedNode(t, store, pk)

	// Baseline: noon UTC.
	if err := store.UpdateNodeConfiguredScope(pk, "eu", "2026-07-25T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, at := configuredScope(t, store, pk); at.String != "2026-07-25T12:00:00Z" {
		t.Fatalf("stored at = %q, want canonical 'Z' form", at.String)
	}

	// "2026-07-25T13:30:00+02:00" == 11:30Z, chronologically EARLIER than 12:00Z,
	// but lexicographically GREATER ("13:30..." > "12:00...Z"). Must be skipped.
	if err := store.UpdateNodeConfiguredScope(pk, "stale", "2026-07-25T13:30:00+02:00"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := configuredScope(t, store, pk); sc.String != "eu" {
		t.Errorf("configured_scope = %q, want 'eu' (earlier +02:00 report must not win)", sc.String)
	}

	// "2026-07-25T15:00:00+02:00" == 13:00Z, chronologically LATER. Must update,
	// and be stored canonicalized to UTC.
	if err := store.UpdateNodeConfiguredScope(pk, "de", "2026-07-25T15:00:00+02:00"); err != nil {
		t.Fatal(err)
	}
	sc, at := configuredScope(t, store, pk)
	if sc.String != "de" {
		t.Errorf("configured_scope = %q, want 'de' (later +02:00 report should win)", sc.String)
	}
	if at.String != "2026-07-25T13:00:00Z" {
		t.Errorf("stored at = %q, want canonical '2026-07-25T13:00:00Z'", at.String)
	}

	// Firmware's real format (microseconds + "+00:00") canonicalizes to "Z".
	if err := store.UpdateNodeConfiguredScope(pk, "dk", "2026-07-26T09:43:48.000000+00:00"); err != nil {
		t.Fatal(err)
	}
	if _, at := configuredScope(t, store, pk); at.String != "2026-07-26T09:43:48Z" {
		t.Errorf("stored at = %q, want canonical '2026-07-26T09:43:48Z'", at.String)
	}
}
