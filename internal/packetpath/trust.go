package packetpath

// TrustConfig controls how much confidence a path-hash prefix observation
// must carry before it is used as topology/mapping evidence (issue #1784).
//
// MeshCore path hops are hashed pubkey prefixes of 1, 2, or 3 bytes
// (firmware/src/Packet.cpp:13-18, hash_size = (pathByte>>6)+1). Shorter
// prefixes collide more often — a 1-byte hash has only 256 possible values,
// so on denser meshes a "currently unique candidate" resolution can still
// be a false positive. TrustConfig lets operators require a longer prefix
// before a mapping consumer (neighbor graph, path resolver, neighbor
// builder, path inspector) treats a resolved hop as trustworthy evidence.
//
// This does not affect raw storage: packets and paths are always stored
// exactly as received. It only gates *derived* trust — which observations
// are eligible to become neighbor-graph edges, resolved-path hops, or
// capability inferences. See the #1784 call-site audit for the full list
// of consumers this is meant to gate.
type TrustConfig struct {
	MinHashBytesForMapping int `json:"minHashBytesForMapping,omitempty"`
}

// DefaultMinHashBytesForMapping is the backward-compatible default: every
// prefix length counts as mapping evidence, exactly as before #1784.
//
// It is deliberately 1 rather than the stricter 2. There is no UI control for
// this threshold, it can only be changed in config.json and that needs a
// restart, so shipping 2 would tighten every instance on upgrade with nothing
// in the UI explaining why the neighbour graph shrank. Measured on a live
// network, 56% of path-hop observations carry a 1-byte prefix and 41% of
// repeaters use a 1-byte hash, so that is not a marginal change. Issue #1784's
// own first acceptance criterion is that the default stays backward compatible.
//
// Operators who want the stricter behaviour set minHashBytesForMapping: 2 or 3.
const DefaultMinHashBytesForMapping = 1

const MaxHashBytes = 3

func (c *TrustConfig) MinHashBytesOrDefault() int {
	if c == nil || c.MinHashBytesForMapping <= 0 {
		return DefaultMinHashBytesForMapping
	}
	if c.MinHashBytesForMapping > MaxHashBytes {
		return MaxHashBytes
	}
	return c.MinHashBytesForMapping
}

// MeetsPathTrust reports whether a path-hash observation of the given
// prefix length (in bytes) should be trusted as mapping/topology evidence
// under cfg's threshold.
//
// prefixBytes == 0 denotes an unknown-length/legacy observation — e.g.
// pre-#1638 persisted neighbor edges with no per-mode breakdown
// (NeighborEdge.CountsByMode[0] in cmd/server/neighbor_graph.go). Per
// #1784's bucket-0 policy: once the threshold requires more than the
// wire-format minimum of 1 byte (minBytes >= 2), an unknown-length
// observation cannot be proven to meet it and is excluded. At the
// trust-all threshold (minBytes <= 1) bucket 0 passes, matching pre-#1784
// behavior exactly.
func MeetsPathTrust(prefixBytes int, cfg *TrustConfig) bool {
	minBytes := cfg.MinHashBytesOrDefault()
	if minBytes <= 1 {
		return true
	}
	if prefixBytes <= 0 {
		return false
	}
	return prefixBytes >= minBytes
}
