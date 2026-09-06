package main

import (
	"log"
	"strings"
	"time"
)

// handleClientRfSample processes one RF environment sample from
// meshcore/client/{PUBLIC_KEY}/rf. The topic pubkey is the identity (ACL-bound
// by the broker); origin_id in the payload is informational and MUST NOT be
// used as a fallback — that would defeat the trust model.
func handleClientRfSample(store *Store, tag, rxPubkey string, msg map[string]interface{}) {
	rxPubkey = strings.ToLower(strings.TrimSpace(rxPubkey))
	if !clientPubkeyRe.MatchString(rxPubkey) {
		log.Printf("MQTT [%s] rf: invalid pubkey %.8q, dropping", tag, rxPubkey)
		return
	}
	gps, ok := msg["gps"].(map[string]interface{})
	if !ok {
		return // a sample without a position is not a map point
	}
	lat, latOK := toFloat64(gps["lat"])
	lon, lonOK := toFloat64(gps["lon"])
	if !latOK || !lonOK || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return
	}
	uptime, uptimeOK := toFloat64(msg["uptime_secs"])
	if !uptimeOK {
		// uptime_secs is the only reboot/wrap detector the delta query has. A
		// missing value would silently default to 0, making every future sample
		// look like uptime never increases — ClientRfDeltas would then return
		// nothing forever while the table looks full. The app always sends this
		// field, so absence means a malformed publisher; reject it the same way
		// a missing GPS fix is rejected.
		log.Printf("MQTT [%s] rf: missing uptime_secs, dropping", tag)
		return
	}

	rxTime, _ := resolveRxTimeCore(msg, tag)
	sampledAt := rxTime.Format(rxTimeMillisLayout)

	o := &ClientRfSample{
		RxPubkey:   rxPubkey,
		SampledAt:  sampledAt,
		IngestedAt: time.Now().UTC().Format(time.RFC3339),
		Lat:        lat, Lon: lon,
		UptimeSecs: int64(uptime),
	}
	if acc, ok := toFloat64(gps["acc_m"]); ok {
		o.PosAccM = &acc
	}
	if v, ok := msg["stationary"].(bool); ok {
		o.Stationary = v
	}
	// Optional numeric fields: absent stays NULL. recv_errors in particular must
	// never default to 0 — that would read as a clean channel on firmware that
	// does not report it.
	o.BatteryMV = optInt(msg, "battery_mv")
	o.QueueLen = optInt(msg, "queue_len")
	o.Errors = optInt(msg, "errors")
	o.NoiseFloor = optInt(msg, "noise_floor")
	o.LastRSSI = optInt(msg, "last_rssi")
	o.LastSNR = optFloat(msg, "last_snr")
	o.TxAirSecs = optInt64(msg, "tx_air_secs")
	o.RxAirSecs = optInt64(msg, "rx_air_secs")
	o.Recv = optInt64(msg, "recv")
	o.Sent = optInt64(msg, "sent")
	o.FloodRx = optInt64(msg, "flood_rx")
	o.DirectRx = optInt64(msg, "direct_rx")
	o.FloodTx = optInt64(msg, "flood_tx")
	o.DirectTx = optInt64(msg, "direct_tx")
	o.RecvErrors = optInt64(msg, "recv_errors")

	if _, err := store.InsertClientRfSample(o); err != nil {
		log.Printf("MQTT [%s] rf sample insert: %v", tag, err)
	}
}

// optInt / optInt64 / optFloat return nil when the key is absent, so a missing
// counter becomes SQL NULL rather than a misleading zero.
func optInt(m map[string]interface{}, k string) *int {
	if f, ok := toFloat64(m[k]); ok {
		v := int(f)
		return &v
	}
	return nil
}

func optInt64(m map[string]interface{}, k string) *int64 {
	if f, ok := toFloat64(m[k]); ok {
		v := int64(f)
		return &v
	}
	return nil
}

func optFloat(m map[string]interface{}, k string) *float64 {
	if f, ok := toFloat64(m[k]); ok {
		return &f
	}
	return nil
}

// ClientRfDelta is one consecutive pair of samples from the same radio. Each
// *Delta field is nil when either endpoint of the pair is NULL (the counter
// is unsupported on that firmware) — a plain int64 defaulting to 0 there
// would be indistinguishable from a measured zero, the exact conflation the
// NULL-vs-zero rule on the underlying columns exists to prevent.
type ClientRfDelta struct {
	At           string
	Lat, Lon     float64
	Stationary   bool
	WallMillis   int64
	RxAirDelta   *int64
	TxAirDelta   *int64
	RecvDelta    *int64
	RecvErrDelta *int64
}

// ClientRfDeltas returns consecutive-sample deltas for one radio. Pairs where
// uptime_secs did not increase are skipped: that is a reboot (or a counter
// wrap), and subtracting across it would produce a large negative or a bogus
// spike. Absolutes stay in the table; only this view does arithmetic.
func (s *Store) ClientRfDeltas(rxPubkey, from, to string) ([]ClientRfDelta, error) {
	rxPubkey = strings.ToLower(strings.TrimSpace(rxPubkey))
	rows, err := s.db.Query(`
		SELECT sampled_at, lat, lon, stationary, uptime_secs,
		       rx_air_secs, tx_air_secs, recv, recv_errors,
		       LAG(sampled_at)  OVER w AS prev_at,
		       LAG(uptime_secs) OVER w AS prev_uptime,
		       LAG(rx_air_secs) OVER w AS prev_rx,
		       LAG(tx_air_secs) OVER w AS prev_tx,
		       LAG(recv)        OVER w AS prev_recv,
		       LAG(recv_errors) OVER w AS prev_errs
		FROM client_rf_samples
		WHERE rx_pubkey = ? AND sampled_at >= ? AND sampled_at < ?
		WINDOW w AS (ORDER BY sampled_at)
		ORDER BY sampled_at`, rxPubkey, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ClientRfDelta
	for rows.Next() {
		var at string
		var lat, lon float64
		var stationary int
		var uptime int64
		var rx, tx, recv, errs *int64
		var prevAt *string
		var prevUptime, prevRx, prevTx, prevRecv, prevErrs *int64
		if err := rows.Scan(&at, &lat, &lon, &stationary, &uptime, &rx, &tx, &recv, &errs,
			&prevAt, &prevUptime, &prevRx, &prevTx, &prevRecv, &prevErrs); err != nil {
			log.Printf("client_rf_samples delta scan: %v", err)
			continue
		}
		if prevAt == nil || prevUptime == nil || uptime <= *prevUptime {
			continue // first row of the window, or a reboot/wrap — no delta
		}
		t1, err1 := time.Parse(time.RFC3339, *prevAt)
		t2, err2 := time.Parse(time.RFC3339, at)
		if err1 != nil || err2 != nil {
			continue
		}
		d := ClientRfDelta{
			At: at, Lat: lat, Lon: lon, Stationary: stationary == 1,
			WallMillis: t2.Sub(t1).Milliseconds(),
		}
		d.RxAirDelta = deltaOf(rx, prevRx)
		d.TxAirDelta = deltaOf(tx, prevTx)
		d.RecvDelta = deltaOf(recv, prevRecv)
		d.RecvErrDelta = deltaOf(errs, prevErrs)
		out = append(out, d)
	}
	return out, rows.Err()
}

func deltaOf(cur, prev *int64) *int64 {
	if cur == nil || prev == nil {
		return nil
	}
	d := *cur - *prev
	if d < 0 { // a wrapped individual counter even though uptime_secs looked monotonic
		d = 0
	}
	return &d
}
