package main

import (
	"testing"
	"time"
)

func TestHandleClientRfSample(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"type": "RF_SAMPLE", "timestamp": "2026-08-17T10:00:00.000Z",
		"gps":         map[string]interface{}{"lat": 51.2, "lon": 4.4, "acc_m": 8.0},
		"stationary":  false,
		"uptime_secs": 84213.0, "noise_floor": -119.0, "rx_air_secs": 20877.0,
	}
	handleClientRfSample(s, "test", "aa11", msg)

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM client_rf_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}

	var sampledAt string
	var recvErrorsNull bool
	if err := s.db.QueryRow(`SELECT sampled_at, recv_errors IS NULL FROM client_rf_samples`).Scan(&sampledAt, &recvErrorsNull); err != nil {
		t.Fatal(err)
	}
	// sampled_at must be stored at rxTimeMillisLayout precision, not
	// resolveRxTime's second-resolution RFC3339 — Task 6's prune cutoff
	// compares lexicographically against this string.
	if _, err := time.Parse(rxTimeMillisLayout, sampledAt); err != nil {
		t.Errorf("sampled_at = %q, want rxTimeMillisLayout format: %v", sampledAt, err)
	}
	// The payload omits recv_errors; it must stay NULL, never default to 0
	// (which would read as a clean channel on firmware that can't count CRC
	// errors).
	if !recvErrorsNull {
		t.Error("recv_errors should be NULL when omitted from the payload")
	}
}

func TestHandleClientRfSampleRejects(t *testing.T) {
	s := newTestStore(t)
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"timestamp":   "2026-08-17T10:00:00.000Z",
			"gps":         map[string]interface{}{"lat": 51.2, "lon": 4.4},
			"uptime_secs": 1.0,
		}
	}

	handleClientRfSample(s, "test", "NOT-HEX!", base()) // bad topic pubkey

	noGPS := base()
	delete(noGPS, "gps")
	handleClientRfSample(s, "test", "aa11", noGPS)

	badGPS := base()
	badGPS["gps"] = map[string]interface{}{"lat": 999.0, "lon": 4.4}
	handleClientRfSample(s, "test", "aa11", badGPS)

	noUptime := base()
	delete(noUptime, "uptime_secs")
	handleClientRfSample(s, "test", "aa11", noUptime)

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rf_samples`).Scan(&n)
	if n != 0 {
		t.Errorf("rows = %d, want 0 — all four must be rejected", n)
	}
}

// TestRfDeltaBreaksOnReboot seeds the first two samples through
// handleClientRfSample (not a direct InsertClientRfSample) so the
// handler-written rxTimeMillisLayout format is what ClientRfDeltas actually
// parses back — a regression that reverted the handler to second-resolution
// resolveRxTime would leave a direct-insert-seeded test green while breaking
// this round trip for real.
func TestRfDeltaBreaksOnReboot(t *testing.T) {
	s := newTestStore(t)
	seed := func(ts string, uptime, rxAir float64) {
		handleClientRfSample(s, "test", "aa11", map[string]interface{}{
			"timestamp":   ts,
			"gps":         map[string]interface{}{"lat": 51.2, "lon": 4.4},
			"uptime_secs": uptime,
			"rx_air_secs": rxAir,
		})
	}
	seed("2026-08-17T10:00:00.000Z", 1000, 500)
	seed("2026-08-17T10:00:15.000Z", 1015, 512) // +12 s of RX air over 15 s
	seed("2026-08-17T10:00:30.000Z", 10, 3)     // rebooted: uptime dropped

	// Bounds are compared lexicographically against millisecond-precision
	// sampled_at values, so they must themselves be in the millisecond layout
	// — "10:00:00Z" would lexicographically exclude "10:00:00.000Z" (`.` < `Z`).
	deltas, err := s.ClientRfDeltas("aa11", "2026-08-17T00:00:00.000Z", "2026-08-18T00:00:00.000Z")
	if err != nil {
		t.Fatalf("deltas: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %d, want 1 (the reboot pair must be skipped)", len(deltas))
	}
	if deltas[0].RxAirDelta == nil || *deltas[0].RxAirDelta != 12 || deltas[0].WallMillis != 15000 {
		t.Errorf("delta = %v over %dms, want 12 over 15000ms", deltas[0].RxAirDelta, deltas[0].WallMillis)
	}
}

// TestRfDeltaNilWhenEitherEndpointMissingCounter proves the NULL-vs-zero
// distinction survives into the delta view: a counter unsupported by
// firmware (NULL on either endpoint) must produce a nil *int64 delta, never
// a 0 that a CRC-error-rate map would render as "clean channel".
func TestRfDeltaNilWhenEitherEndpointMissingCounter(t *testing.T) {
	s := newTestStore(t)
	seed := func(ts string, uptime float64, recvErrors *int64) {
		msg := map[string]interface{}{
			"timestamp":   ts,
			"gps":         map[string]interface{}{"lat": 51.2, "lon": 4.4},
			"uptime_secs": uptime,
		}
		if recvErrors != nil {
			msg["recv_errors"] = float64(*recvErrors)
		}
		handleClientRfSample(s, "test", "aa11", msg)
	}
	five := int64(5)
	seed("2026-08-17T10:00:00.000Z", 1000, nil) // firmware without the counter
	seed("2026-08-17T10:00:15.000Z", 1015, &five)

	deltas, err := s.ClientRfDeltas("aa11", "2026-08-17T00:00:00.000Z", "2026-08-18T00:00:00.000Z")
	if err != nil {
		t.Fatalf("deltas: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %d, want 1", len(deltas))
	}
	if deltas[0].RecvErrDelta != nil {
		t.Errorf("RecvErrDelta = %v, want nil (prev recv_errors was NULL)", *deltas[0].RecvErrDelta)
	}
}

// TestClientRfDeltasNormalizesPubkeyCase proves ClientRfDeltas matches the
// stored lowercase rx_pubkey even when a caller passes uppercase hex —
// mirroring the strings.ToLower(rx) normalization in
// cmd/server/rx_dashboard.go's queryCoverageFiltered. Without it, a URL path
// pubkey pasted in uppercase silently returns zero rows.
func TestClientRfDeltasNormalizesPubkeyCase(t *testing.T) {
	s := newTestStore(t)
	seed := func(ts string, uptime, rxAir float64) {
		handleClientRfSample(s, "test", "aa11", map[string]interface{}{
			"timestamp":   ts,
			"gps":         map[string]interface{}{"lat": 51.2, "lon": 4.4},
			"uptime_secs": uptime,
			"rx_air_secs": rxAir,
		})
	}
	seed("2026-08-17T10:00:00.000Z", 1000, 500)
	seed("2026-08-17T10:00:15.000Z", 1015, 512)

	deltas, err := s.ClientRfDeltas("AA11", "2026-08-17T00:00:00.000Z", "2026-08-18T00:00:00.000Z")
	if err != nil {
		t.Fatalf("deltas: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %d, want 1 for uppercase pubkey lookup", len(deltas))
	}
}
