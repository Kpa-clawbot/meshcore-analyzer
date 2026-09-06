package main

import (
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Issue #1751: repeater/room nodes should expose the deduplicated, sorted
// set of region scope names (transmissions.scope_name) across every
// non-advert packet in which they appear as a path hop. Advert packets must
// be excluded (a self-advert is not transported traffic).
//
// These tests exercise BOTH computation paths that feed RepeaterRelayInfo:
//   - computeRepeaterRelayInfoMap (bulk, repeater_enrich_bulk.go)
//   - GetRepeaterRelayInfo        (per-node, repeater_liveness.go)
// so the field stays in parity for /api/nodes (bulk) and the single-node
// detail endpoint (per-node).

const scope1751Key = "aabbccdd11223344"

// scopeTx builds a path-hop StoreTx with the given payload type, scope name,
// and an in-window FirstSeen.
func scopeTx(id int, payloadType int, scope string) *StoreTx {
	pt := payloadType
	return &StoreTx{
		ID:          id,
		Hash:        "scope-tx-" + scope + "-" + strconv.Itoa(id),
		PayloadType: &pt,
		ScopeName:   &scope,
		FirstSeen:   time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano),
	}
}

func TestTransportedScopes_BulkDedupSortAndAdvertExcluded(t *testing.T) {
	// Three non-advert packets across two distinct scopes (one repeated to
	// prove dedup) PLUS one advert carrying a scope that must NOT appear.
	txMsgWest := scopeTx(1, 2, "region-west")  // TXT_MSG
	txGrpEast := scopeTx(2, 5, "region-east")  // GRP_TXT
	txMsgWest2 := scopeTx(3, 1, "region-west") // REQ, duplicate scope
	advertSecret := scopeTx(4, payloadTypeAdvert, "advert-only-scope")

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{
			scope1751Key: {txMsgWest, txGrpEast, txMsgWest2, advertSecret},
		},
		mu: sync.RWMutex{},
	}

	out := store.computeRepeaterRelayInfoMap(24)
	got := out[scope1751Key].TransportedScopes

	want := []string{"region-east", "region-west"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bulk TransportedScopes = %v, want %v (deduped, sorted, advert-excluded)", got, want)
	}
}

func TestTransportedScopes_PerNodeMatchesBulk(t *testing.T) {
	txMsgWest := scopeTx(1, 2, "region-west")
	txGrpEast := scopeTx(2, 5, "region-east")
	advertSecret := scopeTx(3, payloadTypeAdvert, "advert-only-scope")

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{
			scope1751Key: {txMsgWest, txGrpEast, advertSecret},
		},
		mu: sync.RWMutex{},
	}

	info := store.GetRepeaterRelayInfo(scope1751Key, 24)
	got := info.TransportedScopes

	want := []string{"region-east", "region-west"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("per-node TransportedScopes = %v, want %v (deduped, sorted, advert-excluded)", got, want)
	}
}

// TestTransportedScopes_EmptyWhenNoScope guards the "field absent" contract:
// a repeater whose path-hop packets carry no usable scope_name must yield a
// nil/empty slice so routes.go omits the JSON field entirely. Both non-values
// count: ScopeName nil (not transport-scoped, or older schema with
// hasScopeName=false) and a pointer to "" (transport-scoped, region unmatched).
func TestTransportedScopes_EmptyWhenNoScope(t *testing.T) {
	unmatchedScope := scopeTx(1, 2, "") // non-advert, transport-scoped, region unmatched
	noScope := scopeTx(2, 2, "")        // non-advert, not transport-scoped at all
	noScope.ScopeName = nil

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{scope1751Key: {unmatchedScope, noScope}},
		mu:        sync.RWMutex{},
	}

	if got := store.computeRepeaterRelayInfoMap(24)[scope1751Key].TransportedScopes; len(got) != 0 {
		t.Fatalf("bulk: expected no scopes when ScopeName empty/nil, got %v", got)
	}
	if got := store.GetRepeaterRelayInfo(scope1751Key, 24).TransportedScopes; len(got) != 0 {
		t.Fatalf("per-node: expected no scopes when ScopeName empty/nil, got %v", got)
	}
}

// TestTransportedScopes_PrefixBucketNotAttributed is the #1902 regression.
//
// A 1-byte hop prefix is shared by every node whose pubkey starts with that
// byte, so the prefix bucket holds other nodes' traffic. Scopes are a
// categorical claim about which regions a repeater serves — a 1-byte hop
// names one of N candidates and cannot substantiate it. On the live network
// this badged Belgian repeaters with #de/#de-nw purely because a German
// repeater shared their first pubkey byte.
//
// So TransportedScopes must come from the full-key bucket only, while the
// counters keep the #662 prefix fold (over-count beats false zeros).
// Asserted on BOTH computation paths so they stay in parity.
func TestTransportedScopes_PrefixBucketNotAttributed(t *testing.T) {
	direct := scopeTx(1, 2, "region-direct")   // only in the full-key bucket
	foreign := scopeTx(2, 2, "region-foreign") // only in the 1-byte bucket
	shared := scopeTx(3, 2, "region-shared")   // in BOTH buckets (dedup by ID)

	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{
			scope1751Key:     {direct, shared},
			scope1751Key[:2]: {foreign, shared},
		},
		mu: sync.RWMutex{},
	}

	// "region-foreign" is only evidenced by a 1-byte hop — it must not be
	// attributed. "region-shared" appears under the full key too, so it stays.
	want := []string{"region-direct", "region-shared"}

	bulk := store.computeRepeaterRelayInfoMap(24)[scope1751Key]
	if !reflect.DeepEqual(bulk.TransportedScopes, want) {
		t.Fatalf("bulk TransportedScopes = %v, want %v (prefix-bucket scopes excluded)", bulk.TransportedScopes, want)
	}

	perNode := store.GetRepeaterRelayInfo(scope1751Key, 24)
	if !reflect.DeepEqual(perNode.TransportedScopes, want) {
		t.Fatalf("per-node TransportedScopes = %v, want %v (prefix-bucket scopes excluded)", perNode.TransportedScopes, want)
	}

	// The #662 fold itself is untouched: all three in-window packets still
	// count, deduped by tx ID. Narrowing scopes must not narrow the counters.
	if bulk.RelayCount24h != 3 {
		t.Fatalf("bulk RelayCount24h = %d, want 3 (prefix fold must still feed the counters)", bulk.RelayCount24h)
	}
	if perNode.RelayCount24h != 3 {
		t.Fatalf("per-node RelayCount24h = %d, want 3 (prefix fold must still feed the counters)", perNode.RelayCount24h)
	}
}

// TestTransportedScopes_Capped pins the soft cap (#1751 review follow-up):
// more distinct scopes than maxTransportedScopes are bounded to the cap,
// keeping the lexicographically-first names so the output stays deterministic.
func TestTransportedScopes_Capped(t *testing.T) {
	var txs []*StoreTx
	for i := 0; i < maxTransportedScopes+10; i++ {
		txs = append(txs, scopeTx(i+1, 2, fmt.Sprintf("scope-%03d", i)))
	}
	store := &PacketStore{
		byPathHop: map[string][]*StoreTx{scope1751Key: txs},
		mu:        sync.RWMutex{},
	}

	got := store.computeRepeaterRelayInfoMap(24)[scope1751Key].TransportedScopes
	if len(got) != maxTransportedScopes {
		t.Fatalf("expected cap at %d scopes, got %d", maxTransportedScopes, len(got))
	}
	if got[0] != "scope-000" || got[len(got)-1] != fmt.Sprintf("scope-%03d", maxTransportedScopes-1) {
		t.Fatalf("cap should keep lexicographically-first names: first=%q last=%q", got[0], got[len(got)-1])
	}
}
