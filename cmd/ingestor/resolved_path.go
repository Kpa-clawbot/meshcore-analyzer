package main

import (
	"encoding/json"
	"strings"
	"sync/atomic"
)

// Issue #1547 — resolved_path writer (ingestor-owned).
//
// Per the #1283 refactor (server is read-only; ingestor owns the
// neighbor graph + node directory), the writer that populated
// `observations.resolved_path` must live here in the ingestor. PR #1289
// removed the server-side writer without porting it — this restores it.
//
// Approach:
//   - `resolvePath` is a pure function: hop prefixes → full pubkeys
//     using the in-memory prefix index built from `nodes.public_key`.
//   - Unique-prefix hops resolve to the full pubkey; ambiguous or
//     unknown hops resolve to `nil`. The output shape is `[]*string`
//     (with nulls for unresolved positions) — the JSON serialization
//     matches what the server's `unmarshalResolvedPath` /
//     frontend `getResolvedPath` already consume.
//   - The prefix index is rebuilt on startup and once per neighbor-
//     builder tick (60s) so new nodes start resolving within a minute
//     without blocking the MQTT ingest path.

// resolvePath maps each hop prefix to a full pubkey when the index
// has exactly one candidate; returns nil at that position otherwise.
// Returns nil for empty/no hops.
func resolvePath(hops []string, idx prefixIndex) []*string {
	if len(hops) == 0 {
		return nil
	}
	out := make([]*string, len(hops))
	if idx == nil {
		return out
	}
	for i, hop := range hops {
		h := strings.ToLower(hop)
		candidates := idx[h]
		if len(candidates) == 1 {
			pk := candidates[0]
			out[i] = &pk
		}
	}
	return out
}

// marshalResolvedPath JSON-encodes a resolved path. Returns "" when
// the input is empty OR when every element is nil (writer treats "" as
// SQL NULL).
//
// The all-nil case matters because of the UPSERT in InsertTransmission:
//
//	resolved_path = COALESCE(excluded.resolved_path, resolved_path)
//
// If we emitted "[null,null]" here, nilIfEmpty() would let it through
// as a non-NULL string and the COALESCE would OVERWRITE a previously
// stored good resolved_path on re-ingest. Returning "" lets nilIfEmpty
// produce SQL NULL so the COALESCE falls through to the existing value.
// See issue #1547 / PR #1548 reviewer findings.
func marshalResolvedPath(rp []*string) string {
	if len(rp) == 0 {
		return ""
	}
	allNil := true
	for _, p := range rp {
		if p != nil {
			allNil = false
			break
		}
	}
	if allNil {
		return ""
	}
	b, err := json.Marshal(rp)
	if err != nil {
		return ""
	}
	return string(b)
}

// prefixIdxHolder caches the prefix index for the InsertTransmission
// hot path. atomic.Value lets the 60s rebuild happen without a lock on
// the read side.
type prefixIdxHolder struct {
	v atomic.Value // holds prefixIndex
}

func (h *prefixIdxHolder) load() prefixIndex {
	if v := h.v.Load(); v != nil {
		return v.(prefixIndex)
	}
	return nil
}

func (h *prefixIdxHolder) store(idx prefixIndex) {
	h.v.Store(idx)
}

// RefreshPrefixIndex rebuilds the in-memory prefix index from the
// nodes table and publishes it atomically. Called on startup and from
// the neighbor-edges builder tick (60s) so new nodes become resolvable
// without per-insert DB scans.
func (s *Store) RefreshPrefixIndex() error {
	idx, err := buildPrefixIndex(s.db)
	if err != nil {
		return err
	}
	s.prefixIdx.store(idx)
	return nil
}

// resolvedPubkeys flattens a resolved path to the non-nil pubkeys it
// contains, deduplicating repeats within the same path. Used by the
// relay-aware last_seen touch (#1598); nil entries are unresolved or
// ambiguous hops and are deliberately dropped.
func resolvedPubkeys(rp []*string) []string {
	if len(rp) == 0 {
		return nil
	}
	out := make([]string, 0, len(rp))
	seen := make(map[string]bool, len(rp))
	for _, p := range rp {
		if p == nil || *p == "" || seen[*p] {
			continue
		}
		seen[*p] = true
		out = append(out, *p)
	}
	return out
}
