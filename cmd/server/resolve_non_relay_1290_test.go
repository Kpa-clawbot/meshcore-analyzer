package main

import (
	"testing"
)

// Issue #1290 — exclude observers that advertised `repeat:off` (listener-only)
// from the path-hop disambiguator candidate set. Three cases:
//  1. repeat:off pubkey → NOT a candidate
//  2. repeat:on pubkey  → IS a candidate (regression guard)
//  3. legacy / no field → IS a candidate (back-compat preserve current behavior)
func TestResolveWithContext_ExcludesNonRelayObservers_Issue1290(t *testing.T) {
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "a1aaaaaa", Name: "RealRepeater"},
		{Role: "repeater", PublicKey: "a1bbbbbb", Name: "ListenerOnly"},
	}

	// Case 1: marked non-relay → excluded from candidate set.
	pm := buildPrefixMap(nodes)
	pm.markNonRelay([]string{"a1bbbbbb"})
	ni, conf, _ := pm.resolveWithContext("a1bbbbbb", nil, nil)
	if ni != nil {
		t.Fatalf("case repeat:off — expected nil (listener-only excluded), got name=%q confidence=%q", ni.Name, conf)
	}
	if conf != "no_match" {
		t.Fatalf("case repeat:off — expected no_match confidence after exclusion, got %q", conf)
	}

	// Case 2: repeat:on (i.e. not in nonRelay set) → still resolves.
	pm2 := buildPrefixMap(nodes)
	pm2.markNonRelay([]string{"a1bbbbbb"})
	ni2, _, _ := pm2.resolveWithContext("a1aaaaaa", nil, nil)
	if ni2 == nil || ni2.Name != "RealRepeater" {
		t.Fatalf("case repeat:on — expected RealRepeater, got %+v", ni2)
	}

	// Case 3: legacy back-compat — no markNonRelay call → behavior unchanged.
	pm3 := buildPrefixMap(nodes)
	ni3, _, _ := pm3.resolveWithContext("a1bbbbbb", nil, nil)
	if ni3 == nil || ni3.Name != "ListenerOnly" {
		t.Fatalf("case legacy — expected ListenerOnly (back-compat), got %+v", ni3)
	}
}
