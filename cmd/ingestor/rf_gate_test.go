package main

import "testing"

// clientRfMsg builds a valid RF environment sample message on the dedicated
// topic meshcore/client/<pubkey>/rf.
func clientRfMsg() *mockMessage {
	payload := []byte(`{"timestamp":"2026-06-09T12:00:00Z","uptime_secs":100.0,"noise_floor":-119.0,"gps":{"lat":51.05,"lon":3.72,"acc_m":8.0}}`)
	return &mockMessage{topic: "meshcore/client/" + testCompanionPK + "/rf", payload: payload}
}

// clientRfMsgWithRaw is clientRfMsg plus a "raw" field (same advert hex as
// clientCoverageMsg). The real app never sends "raw" on /rf, but the
// fall-through guard must be structural — independent of payload shape — so
// this proves the dispatch itself (not payload luck) is what keeps a /rf
// message with an incidental "raw" field from being decoded as an ordinary
// observer packet were the gate ever to fall through.
func clientRfMsgWithRaw() *mockMessage {
	advertHex := "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
	payload := []byte(`{"raw":"` + advertHex + `","direction":"rx","timestamp":"2026-06-09T12:00:00Z","origin":"MyMob","uptime_secs":100.0,"gps":{"lat":51.05,"lon":3.72,"acc_m":8.0}}`)
	return &mockMessage{topic: "meshcore/client/" + testCompanionPK + "/rf", payload: payload}
}

func clientRfSampleCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM client_rf_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestClientRfSamplesEnabledDefault verifies the gate helper defaults OFF for
// nil/absent config and is only true when explicitly enabled.
func TestClientRfSamplesEnabledDefault(t *testing.T) {
	if (&Config{}).ClientRfSamplesEnabled() {
		t.Fatal("nil ClientRfSamples must report disabled")
	}
	if (&Config{ClientRfSamples: &ClientRfSamplesConfig{Enabled: false}}).ClientRfSamplesEnabled() {
		t.Fatal("Enabled:false must report disabled")
	}
	if !(&Config{ClientRfSamples: &ClientRfSamplesConfig{Enabled: true}}).ClientRfSamplesEnabled() {
		t.Fatal("Enabled:true must report enabled")
	}
}

// TestClientRfSamplesGateOn drives handleMessage with the feature ON: the
// /rf topic message must be dispatched and write exactly one row.
func TestClientRfSamplesGateOn(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}
	cfg := &Config{ClientRfSamples: &ClientRfSamplesConfig{Enabled: true}}

	handleMessage(store, "test", source, clientRfMsg(), nil, nil, cfg)

	if n := clientRfSampleCount(t, store); n != 1 {
		t.Fatalf("feature ON: expected 1 client_rf_samples row, got %d", n)
	}
}

// TestClientRfSamplesGateOffDoesNotFallThroughToObserverPath is the
// dispatch-shape regression test for the /rf topic, mirroring
// TestClientRxCoverageGateOffDoesNotFallThroughToObserverPath. With the
// feature OFF, a meshcore/client/<pubkey>/rf message must be dropped
// outright — never fall through to the observer packet path, which would
// take parts[1] ("client") as a region and parts[2] (the companion pubkey)
// as an observer id.
func TestClientRfSamplesGateOffDoesNotFallThroughToObserverPath(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}
	cfg := &Config{} // ClientRfSamples nil ⇒ disabled

	handleMessage(store, "test", source, clientRfMsgWithRaw(), nil, nil, cfg)

	if n := clientRfSampleCount(t, store); n != 0 {
		t.Fatalf("feature OFF: expected 0 client_rf_samples rows, got %d", n)
	}
	var observerRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observers WHERE id = ?`, testCompanionPK).Scan(&observerRows); err != nil {
		t.Fatal(err)
	}
	if observerRows != 0 {
		t.Fatalf("feature OFF: the companion pubkey must not be registered as an observer, got %d rows", observerRows)
	}
	var clientRegionRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observers WHERE iata = 'client'`).Scan(&clientRegionRows); err != nil {
		t.Fatal(err)
	}
	if clientRegionRows != 0 {
		t.Fatalf("feature OFF: no observer should be registered under a bogus 'client' region, got %d rows", clientRegionRows)
	}
	var txRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&txRows); err != nil {
		t.Fatal(err)
	}
	if txRows != 0 {
		t.Fatalf("feature OFF: the client-topic sample must not be ingested as an ordinary observer packet, got %d transmissions rows", txRows)
	}
}

// TestClientRfSamplesBlacklistedDropped verifies a blacklisted operator
// cannot skirt the observer blacklist via the /rf topic, mirroring
// TestClientRxCoverageBlacklistedDropped.
func TestClientRfSamplesBlacklistedDropped(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}
	cfg := &Config{
		ClientRfSamples:   &ClientRfSamplesConfig{Enabled: true},
		ObserverBlacklist: []string{testCompanionPK},
	}

	handleMessage(store, "test", source, clientRfMsg(), nil, nil, cfg)

	if n := clientRfSampleCount(t, store); n != 0 {
		t.Fatalf("blacklisted companion: expected 0 client_rf_samples rows, got %d", n)
	}
}

// TestClientPacketsGateUnaffectedByRfGate verifies the two client sub-topic
// gates are independent: /rf disabled must not block /packets when
// clientRxCoverage is enabled (and vice versa is covered by the existing
// coverage_gate_test.go suite).
func TestClientPacketsGateUnaffectedByRfGate(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}
	cfg := &Config{
		ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true},
		ClientRfSamples:  &ClientRfSamplesConfig{Enabled: false},
	}

	handleMessage(store, "test", source, clientCoverageMsg(), nil, nil, cfg)

	if n := clientReceptionCount(t, store); n != 1 {
		t.Fatalf("expected 1 client_receptions row with clientRxCoverage on / clientRfSamples off, got %d", n)
	}
}
