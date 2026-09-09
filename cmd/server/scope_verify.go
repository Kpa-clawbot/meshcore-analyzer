package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// scopeVerifyMaxPacketsPerTarget bounds the per-target evidence list. AGENTS.md
// rule 0 forbids unbounded structures, and the corroboration threshold is 2 —
// past a few hundred packets more evidence changes no verdict, it only costs
// memory. scopeAuditTargetAgg.unmatchedPackets keeps counting past this: the
// count is the honest total, the list is the working set.
const scopeVerifyMaxPacketsPerTarget = 512

// scopeVerifyMaxWindowPackets bounds the OTHER axis: how many unnameable
// packets one refresh will hold and derive over.
//
// The "~400 packets in a 7 day window" this feature was sized against is a
// property of THIS instance's configuration, not of the feature. The ingestor
// stores an EMPTY scope_name for every transport-scoped packet no configured
// key names (scopeNameForDB) — spelled out in words for the reason the query
// below gives — so the fewer hashRegions an instance has, the more
// rows land here — and an instance with none at all puts every scoped packet
// in this set. That is the stock state, and it is the state this feature was
// built to help.
//
// The cost is one HMAC per distinct region name per packet, measured at ~0.7µs
// (BenchmarkScopeVerifierAudit: 124 names over 400 packets in 36ms). At this
// cap and 124 distinct declared names that is ~500k HMACs, roughly 0.35s of
// worst case on an endpoint that already costs seconds and caches for minutes.
// Ten times this cap would not: it would be seconds of HMAC on every refresh.
const scopeVerifyMaxWindowPackets = 4096

// scopeVerifyMaxRegionsPerTarget bounds how many of one repeater's declared
// names are put to the verifier. Every distinct name costs a full pass over
// the packet set, and the list is client-supplied: handleClientRegions
// (cmd/ingestor/client_regions.go) validates the target pubkey and each entry's
// shape, but nothing limits how many entries one companion may report. The
// longest genuine list on this network declares 21 regions, and the firmware
// exports a short label set by construction.
const scopeVerifyMaxRegionsPerTarget = 32

// capVerifyRegions applies scopeVerifyMaxRegionsPerTarget, keeping the first
// entries in the order the caller supplied. It does not sort or rank: a
// declared list has no priority order to respect, and reordering here would
// make which regions get verified depend on something the reader cannot see.
func capVerifyRegions(regions []string) []string {
	if len(regions) <= scopeVerifyMaxRegionsPerTarget {
		return regions
	}
	return regions[:scopeVerifyMaxRegionsPerTarget]
}

// scopeHMACInputs pulls the three values needed to test a region hypothesis
// against one packet: the payload type and raw payload bytes the sender HMACed,
// and the resulting two-byte code it put on the wire.
//
// It deliberately does NOT call DecodePacket. That runs decodePayload, which
// attempts channel decryption and signature validation — work this has no use
// for, repeated over every unmatched packet on every audit refresh. Walking the
// offsets is all that is needed, and it reuses decodeHeader/isTransportRoute/
// decodePath so the offset arithmetic is not duplicated from DecodePacket.
//
// ok is false for anything that cannot carry a region scope: malformed hex, a
// truncated header, an invalid path byte, or a non-transport route. A plain
// FLOOD packet has no transport codes at all, so there is no code1 to compare
// against and HMACing it could only waste time.
func scopeHMACInputs(rawHex string) (payloadType byte, payload []byte, code1 string, ok bool) {
	buf, err := hex.DecodeString(strings.TrimSpace(rawHex))
	if err != nil || len(buf) < 2 {
		return 0, nil, "", false
	}
	header := decodeHeader(buf[0])
	if !isTransportRoute(header.RouteType) {
		return 0, nil, "", false
	}
	offset := 1
	if len(buf) < offset+4 {
		return 0, nil, "", false
	}
	code1 = strings.ToUpper(hex.EncodeToString(buf[offset : offset+2]))
	offset += 4 // code1 and code2

	if offset >= len(buf) {
		return 0, nil, "", false
	}
	pathByte := buf[offset]
	offset++
	_, consumed, decodeErr := decodePath(pathByte, buf, offset)
	if decodeErr != nil {
		return 0, nil, "", false
	}
	offset += consumed
	if offset > len(buf) {
		return 0, nil, "", false
	}
	rest := buf[offset:]
	if len(rest) == 0 || len(rest) > maxPacketPayload {
		// The upper bound mirrors DecodePacket, which rejects a payload past
		// the firmware's MAX_PACKET_PAYLOAD. Unreachable in practice, because
		// such a packet never reaches the database with an empty scope_name in
		// the first place, but the comment above claims this walks the same
		// offsets as the decoder and that should be true rather than nearly
		// true. A verifier that accepts what the decoder rejects is a small
		// divergence today and a confusing one to debug later.
		return 0, nil, "", false
	}
	return byte(header.PayloadType), rest, code1, true
}

// regionCode derives the on-wire code1 a sender in region name would emit for
// this payload — the forward direction of what matchingRegions inverts in the
// ingestor (cmd/ingestor/main.go). The two must stay in step: key is
// SHA256("#name")[:16], the MAC covers payloadType followed by the payload, the
// code is the first two MAC bytes little-endian, and 0x0000/0xFFFF are reserved
// and nudged. Any divergence here silently produces regions that never verify.
//
// The leading '#' is optional because callers hold normScope'd names (the audit
// strips it) while the key is over the '#'-prefixed form.
//
// Case is significant and must stay so: the key is a hash over the raw bytes of
// "#name", so "#BEHSS" and "#behss" are different regions on the wire.
func regionCode(name string, payloadType byte, payload []byte) string {
	if !strings.HasPrefix(name, "#") {
		name = "#" + name
	}
	sum := sha256.Sum256([]byte(name))
	mac := hmac.New(sha256.New, sum[:16])
	mac.Write([]byte{payloadType})
	mac.Write(payload)
	h := mac.Sum(nil)
	code := uint16(h[0]) | uint16(h[1])<<8
	if code == 0 {
		code = 1
	} else if code == 0xFFFF {
		code = 0xFFFE
	}
	return strings.ToUpper(hex.EncodeToString([]byte{byte(code & 0xFF), byte(code >> 8)}))
}

// unmatchedTransmissionRow is one transmission that carried a transport scope
// no configured region key matched, with the raw bytes needed to test a region
// hypothesis against it.
type unmatchedTransmissionRow struct {
	txID   int64
	rawHex string
}

// unmatchedTransmissionsInWindow is the SECOND, narrow query behind the audit -
// deliberately not a widening of scopeAuditForwarderScanQuery.
//
// That scan returns one row per hop per flood packet: on a 2,000-packet sample
// after M0 that is 19,049 rows, and carrying raw_hex on every one of them would
// load the hot path to serve a few hundred packets. This selects only the
// transmissions that are actually candidates - an empty scope_name inside the
// window, ~400 over 7 days on the reference deployment - and the main scan is
// left exactly as it is.
//
// An empty scope_name is the "transport-scoped but unnameable" state; NULL
// means the packet carried no scope at all and can never verify against a
// region. The route filter matches the forwarder scan's, so the two agree on
// which packets count as forwarded.
//
// "Empty" is spelled out above rather than written as the two-single-quote
// literal on purpose: gofmt applies the old godoc typographic substitution
// inside doc comments and rewrites that digraph into a closing curly quote,
// which silently misstates the one value this query keys on — and puts it back
// on every gofmt run.
//
// Selection only: a row whose raw_hex cannot be walked is still returned, and
// dropped by newScopeVerifier. Filtering that in SQL is not possible and
// filtering it here would hide how many candidates the window actually held.
// The second return value reports that the window held more than
// scopeVerifyMaxWindowPackets rows and the answer is a sample of the most
// recent ones. Newest-first because a partial answer drawn from the most recent
// traffic is the one that matches what the window claims to describe: a
// repeater still forwarding a region is likelier to have done so recently, and
// verification only ever needs two corroborating packets.
func (s *PacketStore) unmatchedTransmissionsInWindow(sinceISO string) ([]unmatchedTransmissionRow, bool, error) {
	rows, err := s.db.conn.Query(`
		SELECT t.id, t.raw_hex
		FROM transmissions t
		WHERE t.first_seen >= ?
		  AND t.scope_name = ''
		  AND `+scopeConformanceForwarderRouteTypesSQL+`
		ORDER BY t.first_seen DESC
		LIMIT ?`, sinceISO, scopeVerifyMaxWindowPackets)
	if err != nil {
		return nil, false, fmt.Errorf("unmatched transmissions scan: %w", err)
	}
	defer rows.Close()

	var out []unmatchedTransmissionRow
	for rows.Next() {
		var r unmatchedTransmissionRow
		if err := rows.Scan(&r.txID, &r.rawHex); err != nil {
			return nil, false, fmt.Errorf("unmatched transmissions scan row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("unmatched transmissions rows: %w", err)
	}
	return out, len(out) == scopeVerifyMaxWindowPackets, nil
}

// scopeVerifyMinCorroboration is how many of a repeater's own unmatched packets
// must derive to a declared region before that region counts as observed.
//
// One is not enough, and the arithmetic is the whole argument: code1 is two
// bytes, so an unrelated name matches a given packet with probability 1/65536.
// Across ~400 unmatched packets and ~124 distinct declared names, chance alone
// produces roughly one false match per refresh. Two matches on the same region
// for the same repeater is (1/65536)^2 - about one in four billion. Raising
// this costs recall on quiet regions; lowering it to 1 makes the feature
// unsound, not merely noisy.
const scopeVerifyMinCorroboration = 2

// scopeVerifier answers "how many of these transmissions are region X" while
// deriving each region's code over each packet at most once.
//
// The cache is not a nicety, and where it has to sit was measured rather than
// guessed. Naively the audit does targets x declaredNames x unmatchedPackets
// HMACs — 205 x 124 x 400 is roughly 10,000,000. Caching per
// (region, transmission) pair cuts the HMACs to names x packets, ~50,000, but
// leaves the ITERATION cubic: a benchmark of that shape spent 501ms on 10.2M
// map lookups at ~49ns each, with the HMACs a rounding error beside it.
//
// So the cache is keyed per REGION, holding the set of transmissions that
// derive to it. A region is HMACed over every packet once, and a target then
// asks one question per declared region instead of one per (region, packet).
// The overwhelmingly common answer is an empty set — most declared regions
// match nothing — which costs a single lookup and no packet loop at all.
//
// Not safe for concurrent use: one verifier is built per audit computation,
// which handleScopeAudit already serialises behind its cache.
type scopeVerifier struct {
	packets map[int64]scopeVerifyInputs
	// matchesByRegion caches, per region, the transmissions that derive to it.
	// Computed once over every packet, never per target.
	matchesByRegion map[string]map[int64]bool
	// hmacCount is incremented per actual derivation, asserted by the cache
	// test so a future refactor cannot quietly reintroduce the naive cost.
	hmacCount int
}

type scopeVerifyInputs struct {
	payloadType byte
	payload     []byte
	code1       string
	ok          bool
}

// newScopeVerifier parses each row once. A row whose raw_hex cannot be walked
// is kept with ok=false rather than dropped, so its id still resolves and a
// caller asking about it gets "no evidence" instead of a miss.
func newScopeVerifier(rows []unmatchedTransmissionRow) *scopeVerifier {
	v := &scopeVerifier{
		packets:         make(map[int64]scopeVerifyInputs, len(rows)),
		matchesByRegion: map[string]map[int64]bool{},
	}
	for _, r := range rows {
		pt, payload, code1, ok := scopeHMACInputs(r.rawHex)
		v.packets[r.txID] = scopeVerifyInputs{payloadType: pt, payload: payload, code1: code1, ok: ok}
	}
	return v
}

// regionMatches returns the transmissions deriving to region, computing the
// whole set on first ask. Unparseable packets are skipped rather than counted
// as misses, so one malformed row in the window cannot blank a region.
func (v *scopeVerifier) regionMatches(region string) map[int64]bool {
	if m, ok := v.matchesByRegion[region]; ok {
		return m
	}
	m := map[int64]bool{}
	for txID, in := range v.packets {
		if !in.ok {
			continue
		}
		v.hmacCount++
		if regionCode(region, in.payloadType, in.payload) == in.code1 {
			m[txID] = true
		}
	}
	v.matchesByRegion[region] = m
	return m
}

// evidence counts, for each declared region, how many of txIDs derive to it.
// Regions with zero matches are absent from the result rather than present
// with 0, so the map is directly the "we found something" set.
func (v *scopeVerifier) evidence(txIDs []int64, declaredRegions []string) map[string]int {
	out := map[string]int{}
	for _, region := range declaredRegions {
		m := v.regionMatches(region)
		if len(m) == 0 {
			continue // the common case: one lookup, no packet loop
		}
		n := 0
		for _, txID := range txIDs {
			if m[txID] {
				n++
			}
		}
		if n > 0 {
			out[region] = n
		}
	}
	return out
}

// verified returns the regions in an evidence map that clear the corroboration
// threshold, sorted so the response is stable across refreshes.
func (v *scopeVerifier) verified(evidence map[string]int) []string {
	var out []string
	for region, n := range evidence {
		if n >= scopeVerifyMinCorroboration {
			out = append(out, region)
		}
	}
	sort.Strings(out)
	return out
}
