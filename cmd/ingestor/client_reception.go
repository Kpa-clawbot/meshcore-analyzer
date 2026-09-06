package main

import (
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// clientPubkeyRe validates the companion pubkey taken from the MQTT topic
// (meshcore/client/<PUBLIC_KEY>/packets). A no-ACL broker would let a client
// publish under an arbitrary topic segment (e.g. "!@#$"), so we reject anything
// that is not lowercase hex before it reaches client_receptions/client_observers.
// Mirrors the server-side hexPrefixRe (cmd/server/node_resolve.go).
var clientPubkeyRe = regexp.MustCompile(`^[0-9a-f]{2,64}$`)

// handleClientPacket processes a packet from the mobile client RX topic
// (meshcore/client/{PUBLIC_KEY}/packets). Unlike observer packets, a roaming
// companion reports WHERE it directly heard a node, so we write a
// client_receptions row and never touch the observers/observations tables.
// rxPubkey is the companion pubkey from the topic (ACL-bound by the broker).
func handleClientPacket(store *Store, cfg *Config, tag, rxPubkey string, msg map[string]interface{}, channelKeys map[string]string, regionKeys map[string][]byte) {
	// The companion identity IS the (ACL-bound) topic pubkey. Reject non-hex
	// topic segments so a no-ACL broker can't pollute the coverage tables, and
	// never fall back to a payload-supplied id (that would defeat the ACL trust
	// model — see docs/client-rx-coverage.md).
	rxPubkey = strings.ToLower(strings.TrimSpace(rxPubkey))
	if !clientPubkeyRe.MatchString(rxPubkey) {
		log.Printf("MQTT [%s] client: invalid pubkey %.8q, dropping", tag, rxPubkey)
		return
	}
	rawHex, _ := msg["raw"].(string)
	if rawHex == "" {
		return
	}
	gps, ok := msg["gps"].(map[string]interface{})
	if !ok {
		return // a client packet without a GPS fix is not coverage; drop
	}
	lat, latOK := toFloat64(gps["lat"])
	lon, lonOK := toFloat64(gps["lon"])
	if !latOK || !lonOK {
		return
	}
	var accPtr *float64
	if acc, ok := toFloat64(gps["acc_m"]); ok {
		accPtr = &acc
	}

	decoded, err := DecodePacket(rawHex, channelKeys, false)
	if err != nil {
		log.Printf("MQTT [%s] client decode error: %v", tag, err)
		return
	}

	direction := ""
	if v, ok := msg["direction"].(string); ok {
		direction = v
	} else if v, ok := msg["Direction"].(string); ok {
		direction = v
	}

	var snrPtr *float64
	if f, ok := toFloat64(firstPresent(msg, "SNR", "snr")); ok {
		snrPtr = &f
	}
	var rssiPtr *int
	if f, ok := toFloat64(firstPresent(msg, "RSSI", "rssi")); ok {
		v := int(f)
		rssiPtr = &v
	}

	// Resolved once via resolveRxTimeCore and formatted twice (RFC3339 for
	// coverage, rxTimeMillisLayout for observations) rather than reparsing the
	// envelope timestamp per format — a second parse would re-validate/re-log
	// the same timestamp and take its own independent time.Now() reading, so
	// the coverage row and the observation for the SAME packet could straddle
	// a second boundary on a fallback path.
	rxTime, _ := resolveRxTimeCore(msg, tag)
	rxAt := rxTime.Format(time.RFC3339)
	ingestedAt := time.Now().UTC().Format(time.RFC3339)
	isAdvert := decoded.Header.PayloadTypeName == "ADVERT"

	if cfg.ClientRxObservationsEnabled() {
		// rxAtMillis: UNIQUE(rx_pubkey, pkt_hash, rx_at) needs sub-second
		// resolution to keep distinct forwarder copies of the same flood as
		// separate rows — resolveRxTime's second-resolution RFC3339 would
		// collapse them and ON CONFLICT DO NOTHING would silently drop all but
		// the first.
		rxAtMillis := rxTime.Format(rxTimeMillisLayout)
		if obs := buildClientRxObservation(direction, rxPubkey, rawHex, rxAtMillis, ingestedAt, decoded, regionKeys, snrPtr, rssiPtr, lat, lon, accPtr); obs != nil {
			if _, err := store.InsertClientRxObservation(obs); err != nil {
				log.Printf("MQTT [%s] client observation insert: %v", tag, err)
			}
		}
	}

	rec, ok := buildClientReception(
		rxPubkey,
		direction, decoded.Header.RouteType, decoded.Header.PayloadType, decoded.Path.Hops, decoded.Payload.PubKey, isAdvert,
		snrPtr, rssiPtr, lat, lon, accPtr, rxAt, ingestedAt,
	)
	if !ok {
		return
	}
	if _, err := store.InsertClientReception(rec); err != nil {
		log.Printf("MQTT [%s] client_reception insert: %v", tag, err)
	}
	// Remember the companion's self-reported name (sent as "origin") so the
	// leaderboard can show a name even if this companion never advertised.
	if name := stringField(msg, "origin"); name != "" {
		if err := store.UpsertClientObserver(rec.RxPubkey, name, ingestedAt); err != nil {
			log.Printf("MQTT [%s] client_observer upsert: %v", tag, err)
		}
	}
}

// UpsertClientObserver records/updates a mobile client's self-reported name.
// All writes live in the ingestor (read/write invariant #1283).
func (s *Store) UpsertClientObserver(pubkey, name, ts string) error {
	if pubkey == "" || name == "" {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO client_observers (pubkey, name, last_seen) VALUES (?,?,?)
		ON CONFLICT(pubkey) DO UPDATE SET name = excluded.name, last_seen = excluded.last_seen`,
		strings.ToLower(pubkey), name, ts)
	return err
}

// firstPresent returns the first present value among the given keys.
func firstPresent(msg map[string]interface{}, keys ...string) interface{} {
	for _, k := range keys {
		if v, ok := msg[k]; ok {
			return v
		}
	}
	return nil
}

// stringField returns msg[key] as a string, or "" if absent/not a string.
func stringField(msg map[string]interface{}, key string) string {
	if v, ok := msg[key].(string); ok {
		return v
	}
	return ""
}

// ClientReception is one mobile RX coverage point: a companion (RxPubkey)
// directly heard a node (HeardKey) at a GPS position. Hex binning is done
// server-side from Lat/Lon at query time, so no cell id is stored here.
type ClientReception struct {
	RxPubkey    string
	HeardKey    string
	HeardKeyLen int
	RSSI        *int
	SNR         *float64
	Lat         float64
	Lon         float64
	PosAccM     *float64
	RxAt        string
	IngestedAt  string
	Src         string
}

// deriveHeardKey applies the RX capture HARD RULE: record only what the
// companion heard itself and directly.
//   - direction must be "rx".
//   - payload type must not repurpose the header path bytes (TRACE stores
//     per-hop SNR there, not node hashes — packetpath.PathBytesAreHops).
//   - hops present AND a FLOOD route → the directly-heard node is the LAST hop
//     (path[len-1] = the forwarder that just transmitted; each FLOOD forwarder
//     appends its hash to the end). 1-byte (2 hex char) prefixes are rejected.
//   - hops present on a DIRECT route → NOT attributable: direct forwarders
//     consume the next hop from the FRONT (firmware Mesh.cpp removeSelfFromPath),
//     so path[len-1] is the route's destination-side end, not who was heard.
//   - hops empty + isAdvert → the 0-hop advertiser, by its full pubkey.
//   - otherwise → not attributable (ok=false).
//
// Returns (heardKey lowercased, keylenBytes, src, ok).
func deriveHeardKey(direction string, routeType, payloadType int, hops []string, advertPubkey string, isAdvert bool) (string, int, string, bool) {
	if !strings.EqualFold(direction, "rx") {
		return "", 0, "", false
	}
	if !packetpath.PathBytesAreHops(byte(payloadType)) {
		return "", 0, "", false
	}
	if len(hops) > 0 {
		// FLOOD routes (TRANSPORT_FLOOD 0, FLOOD 1) APPEND each forwarder's hash to
		// the END of the path, so path[last] is the immediate RF transmitter. DIRECT
		// routes (2, 3) consume the next hop from the FRONT, so path[last] is the
		// route's destination-side end, NOT who was heard.
		if routeType != packetpath.RouteTransportFlood && routeType != packetpath.RouteFlood { // direct route: path[last] is not the transmitter
			return "", 0, "", false
		}
		last := strings.ToLower(strings.TrimSpace(hops[len(hops)-1]))
		keylen := len(last) / 2
		if keylen < 2 { // exclude 1-byte (collision-prone), matching Reach
			return "", 0, "", false
		}
		return last, keylen, "rxlog", true
	}
	if isAdvert && advertPubkey != "" {
		pk := strings.ToLower(strings.TrimSpace(advertPubkey))
		return pk, len(pk) / 2, "advert", true
	}
	return "", 0, "", false
}

// buildClientReception validates inputs and assembles a ClientReception, or
// returns ok=false when the packet is not attributable / out of range.
func buildClientReception(
	rxPubkey, direction string, routeType, payloadType int, hops []string, advertPubkey string, isAdvert bool,
	snr *float64, rssi *int, lat, lon float64, posAccM *float64, rxAt, ingestedAt string,
) (*ClientReception, bool) {
	if rxPubkey == "" || rxAt == "" {
		return nil, false
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil, false
	}
	heardKey, keylen, src, ok := deriveHeardKey(direction, routeType, payloadType, hops, advertPubkey, isAdvert)
	if !ok {
		return nil, false
	}
	return &ClientReception{
		RxPubkey: strings.ToLower(rxPubkey), HeardKey: heardKey, HeardKeyLen: keylen,
		RSSI: rssi, SNR: snr, Lat: lat, Lon: lon, PosAccM: posAccM,
		RxAt: rxAt, IngestedAt: ingestedAt, Src: src,
	}, true
}

// InsertClientReception writes one coverage row. Idempotent via the
// UNIQUE(rx_pubkey, heard_key, rx_at) constraint; returns ins=false when the
// row already existed. All writes live in the ingestor (read/write invariant #1283).
func (s *Store) InsertClientReception(r *ClientReception) (bool, error) {
	res, err := s.db.Exec(`
		INSERT INTO client_receptions
			(rx_pubkey, heard_key, heard_keylen, rssi, snr, lat, lon, pos_acc_m, rx_at, ingested_at, src)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(rx_pubkey, heard_key, rx_at) DO NOTHING`,
		r.RxPubkey, r.HeardKey, r.HeardKeyLen, r.RSSI, r.SNR, r.Lat, r.Lon, r.PosAccM, r.RxAt, r.IngestedAt, r.Src)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClientRfSample is one RF environment sample from a mobile client: the
// attached LoRa radio's own counters (noise floor, RX/TX airtime, CRC errors,
// packet totals), paired with the GPS point where the sample was taken. Every
// counter is an absolute cumulative value the radio reported; Task 5 derives
// deltas at query time. RecvErrors stays nil on firmware that cannot count
// CRC errors — never storing 0, which would read as a perfectly clean channel.
// Errors is the exception: per the firmware stats frame, it is an error-flags
// bitmask, not a counter, so it is deliberately excluded from ClientRfDeltas.
type ClientRfSample struct {
	RxPubkey, SampledAt, IngestedAt                                                    string
	Lat, Lon                                                                           float64
	PosAccM                                                                            *float64
	Stationary                                                                         bool
	UptimeSecs                                                                         int64
	BatteryMV, QueueLen, Errors                                                        *int
	NoiseFloor, LastRSSI                                                               *int
	LastSNR                                                                            *float64
	TxAirSecs, RxAirSecs, Recv, Sent, FloodRx, DirectRx, FloodTx, DirectTx, RecvErrors *int64
}

// InsertClientRfSample writes one RF environment sample. Idempotent via
// UNIQUE(rx_pubkey, sampled_at). Every counter is stored as an absolute; the
// server derives deltas, so a lost or reordered sample costs one interval
// rather than corrupting a running total.
func (s *Store) InsertClientRfSample(o *ClientRfSample) (bool, error) {
	res, err := s.db.Exec(`
		INSERT INTO client_rf_samples
			(rx_pubkey, sampled_at, ingested_at, lat, lon, pos_acc_m, stationary,
			 uptime_secs, battery_mv, queue_len, errors, noise_floor, last_rssi,
			 last_snr, tx_air_secs, rx_air_secs, recv, sent, flood_rx, direct_rx,
			 flood_tx, direct_tx, recv_errors)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(rx_pubkey, sampled_at) DO NOTHING`,
		o.RxPubkey, o.SampledAt, o.IngestedAt, o.Lat, o.Lon, o.PosAccM, boolToInt(o.Stationary),
		o.UptimeSecs, o.BatteryMV, o.QueueLen, o.Errors, o.NoiseFloor, o.LastRSSI,
		o.LastSNR, o.TxAirSecs, o.RxAirSecs, o.Recv, o.Sent, o.FloodRx, o.DirectRx,
		o.FloodTx, o.DirectTx, o.RecvErrors)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClientRxObservation is one decodable packet a mobile client heard, whether or
// not it was attributable to a directly-heard node. Diagnostic only: this table
// is never a coverage source (see client_receptions for that).
type ClientRxObservation struct {
	RxPubkey    string
	RxAt        string
	IngestedAt  string
	PktHash     string
	RouteType   int
	PayloadType int
	HashSize    int
	HopCount    int
	Code1       *string
	Code2       *string
	ScopeName   *string
	PathJSON    *string
	Forwarder   *string
	SNR         *float64
	RSSI        *int
	Lat         float64
	Lon         float64
	PosAccM     *float64
}

// buildClientRxObservation assembles a ClientRxObservation from a decoded
// client packet, regardless of whether it is attributable to a directly-heard
// node (deriveHeardKey/buildClientReception decide that separately, and this
// function has no opinion on it). It does, however, require direction "rx"
// (case-insensitively, matching deriveHeardKey's own check on the coverage
// path) and returns nil otherwise: a companion's own outgoing transmission is
// not RF it observed, and recording it would inflate this table's headline
// signal — per-flood row multiplicity meant to measure forwarder
// amplification of traffic actually heard over the air.
func buildClientRxObservation(
	direction, rxPubkey, rawHex, rxAt, ingestedAt string, decoded *DecodedPacket, regionKeys map[string][]byte,
	snr *float64, rssi *int, lat, lon float64, posAccM *float64,
) *ClientRxObservation {
	if !strings.EqualFold(direction, "rx") {
		return nil
	}
	obs := &ClientRxObservation{
		RxPubkey:    rxPubkey,
		RxAt:        rxAt,
		IngestedAt:  ingestedAt,
		PktHash:     ComputeContentHash(rawHex),
		RouteType:   decoded.Header.RouteType,
		PayloadType: decoded.Header.PayloadType,
		HashSize:    decoded.Path.HashSize,
		HopCount:    decoded.Path.HashCount, // declared count; len(Hops) can be short on a truncated path
		SNR:         snr,
		RSSI:        rssi,
		Lat:         lat,
		Lon:         lon,
		PosAccM:     posAccM,
	}
	if decoded.TransportCodes != nil {
		obs.Code1 = &decoded.TransportCodes.Code1
		obs.Code2 = &decoded.TransportCodes.Code2
		if decoded.TransportCodes.Code1 != "0000" {
			sn := matchScope(regionKeys, byte(decoded.Header.PayloadType), decoded.payloadRaw, decoded.TransportCodes.Code1)
			obs.ScopeName = &sn
		}
	}
	// TRACE repurposes the header path bytes as per-hop SNR values, not node
	// hashes (decoder.go), so they must not be recorded as a forwarder or a hop
	// chain — same guard as deriveHeardKey. The row is still written (route
	// type, transport codes and signal are all still valid diagnostics); only
	// forwarder/path_json stay NULL.
	if len(decoded.Path.Hops) > 0 && packetpath.PathBytesAreHops(byte(decoded.Header.PayloadType)) {
		// The decoder emits hops as uppercase hex (decoder.go strings.ToUpper),
		// but every other identifier in this schema is lowercase (rxPubkey,
		// heard_key, ComputeContentHash output). Lowercase every hop here so
		// path_json and forwarder agree in case within this table and stay
		// joinable against them — otherwise a case-sensitive comparison (e.g.
		// json_extract(path_json,'$[n]') = forwarder) silently matches zero
		// rows instead of erroring.
		lowerHops := make([]string, len(decoded.Path.Hops))
		for i, h := range decoded.Path.Hops {
			lowerHops[i] = strings.ToLower(h)
		}
		if b, err := json.Marshal(lowerHops); err == nil {
			j := string(b)
			obs.PathJSON = &j
		}
		// FLOOD routes accumulate forwarders at the END of the path, so
		// path[last] is the node that actually transmitted. DIRECT routes
		// consume from the FRONT, so their path[last] is the route's far
		// end — never the transmitter. Same rule as deriveHeardKey.
		if decoded.Header.RouteType == packetpath.RouteTransportFlood || decoded.Header.RouteType == packetpath.RouteFlood {
			f := lowerHops[len(lowerHops)-1]
			obs.Forwarder = &f
		}
	}
	return obs
}

// InsertClientRxObservation writes one diagnostic observation. Idempotent via
// UNIQUE(rx_pubkey, pkt_hash, rx_at); returns ins=false when the row existed.
// Several rows per pkt_hash are EXPECTED — each is one forwarder's copy of the
// same flood, and that multiplicity is the flood-amplification signal.
func (s *Store) InsertClientRxObservation(o *ClientRxObservation) (bool, error) {
	res, err := s.db.Exec(`
		INSERT INTO client_rx_observations
			(rx_pubkey, rx_at, ingested_at, pkt_hash, route_type, payload_type,
			 code1, code2, scope_name, hash_size, hop_count, path_json, forwarder,
			 snr, rssi, lat, lon, pos_acc_m)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(rx_pubkey, pkt_hash, rx_at) DO NOTHING`,
		o.RxPubkey, o.RxAt, o.IngestedAt, o.PktHash, o.RouteType, o.PayloadType,
		o.Code1, o.Code2, o.ScopeName, o.HashSize, o.HopCount, o.PathJSON, o.Forwarder,
		o.SNR, o.RSSI, o.Lat, o.Lon, o.PosAccM)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
