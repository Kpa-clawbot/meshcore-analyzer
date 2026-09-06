package main

// Regression test for #1094: the bimodal-clock warning currently exposes only
// RecentBadSampleCount, leaving the UI to render "⚠️ N of M adverts had
// nonsense timestamps" without telling the operator WHICH packets were bad.
//
// This test pins the additive API contract: alongside the count, the response
// must expose RecentBadSamples — a slice of (hash, advertTS, skewSec) — so the
// frontend can render each offending hash as a clickable link with its bad
// timestamp.

import (
	"testing"
	"time"
)

// Seeds 5 recent adverts: 3 healthy (~-20s skew) and 2 with a "nonsense"
// bimodal-bad timestamp (|skew| in (1h, 24h]). The recent window is exactly
// 5 samples, so all five are inside it.
func seedIssue1094Repro(t *testing.T) (*PacketStore, []string, []int64) {
	t.Helper()
	ps := NewPacketStore(nil, nil)
	pt := 4 // ADVERT

	const pubkey = "BADTS1094"
	baseObs := int64(1779000000)

	var txs []*StoreTx
	var badHashes []string
	var badAdvertTSs []int64

	// 3 healthy adverts (skew = -20s).
	for i := 0; i < 3; i++ {
		obsTS := baseObs + int64(i)*60
		advTS := obsTS - 20
		txs = append(txs, &StoreTx{
			Hash:        "healthy-1094-" + formatInt64(int64(i)),
			PayloadType: &pt,
			DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `},"pubKey":"BADTS1094"}`,
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		})
	}

	// 2 nonsense-timestamp adverts (skew = -7200s = -2h — bimodal-bad,
	// below the 24h RTC-reset exclusion so they DO count in recentBadCount).
	for i := 0; i < 2; i++ {
		obsTS := baseObs + int64(3+i)*60
		advTS := obsTS - 7200
		hash := "bad-1094-" + formatInt64(int64(i))
		txs = append(txs, &StoreTx{
			Hash:        hash,
			PayloadType: &pt,
			DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `},"pubKey":"BADTS1094"}`,
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		})
		badHashes = append(badHashes, hash)
		badAdvertTSs = append(badAdvertTSs, advTS)
	}

	ps.mu.Lock()
	ps.byNode[pubkey] = txs
	for _, tx := range txs {
		ps.byPayloadType[4] = append(ps.byPayloadType[4], tx)
	}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()
	return ps, badHashes, badAdvertTSs
}

func TestIssue1094_RecentBadSamples_ExposesHashAndTimestamp(t *testing.T) {
	ps, wantHashes, wantAdvertTSs := seedIssue1094Repro(t)
	r := ps.GetNodeClockSkew("BADTS1094")
	if r == nil {
		t.Fatal("expected clock skew result")
	}

	// Pre-condition: count must already be 2 (gates the test against the
	// existing field — if this drops we'd be measuring the wrong thing).
	if r.RecentBadSampleCount != 2 {
		t.Fatalf("RecentBadSampleCount = %d, want 2 (seed bug, not the field-under-test)",
			r.RecentBadSampleCount)
	}

	if len(r.RecentBadSamples) != 2 {
		t.Fatalf("RecentBadSamples len = %d, want 2 — operators need to see which "+
			"adverts had nonsense timestamps, not just the count",
			len(r.RecentBadSamples))
	}

	gotByHash := map[string]int64{}
	for _, bs := range r.RecentBadSamples {
		gotByHash[bs.Hash] = bs.AdvertTS
	}
	for i, h := range wantHashes {
		ts, ok := gotByHash[h]
		if !ok {
			t.Errorf("RecentBadSamples missing hash %q", h)
			continue
		}
		if ts != wantAdvertTSs[i] {
			t.Errorf("RecentBadSamples[%q].AdvertTS = %d, want %d (the bad advertTS)",
				h, ts, wantAdvertTSs[i])
		}
	}
}
