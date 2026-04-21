package main

import (
	"sort"
	"testing"
)

// dedupeHopsByPair mirrors the production logic in store.go for testability.
// It groups distHopRecords by unordered pair, keeps max-distance record per pair,
// and computes obsCount, bestSnr, medianSnr.
func dedupeHopsByPair(hops []distHopRecord) []map[string]interface{} {
	type pairAgg struct {
		best     *distHopRecord
		obsCount int
		maxSNR   *float64
		snrs     []float64
	}
	pairMap := make(map[string]*pairAgg)
	for i := range hops {
		h := &hops[i]
		pk1, pk2 := h.FromPk, h.ToPk
		if pk1 > pk2 {
			pk1, pk2 = pk2, pk1
		}
		key := pk1 + "|" + pk2
		agg, ok := pairMap[key]
		if !ok {
			agg = &pairAgg{}
			pairMap[key] = agg
		}
		agg.obsCount++
		if h.SNR != nil {
			agg.snrs = append(agg.snrs, *h.SNR)
			if agg.maxSNR == nil || *h.SNR > *agg.maxSNR {
				v := *h.SNR
				agg.maxSNR = &v
			}
		}
		if agg.best == nil || h.Dist > agg.best.Dist {
			agg.best = h
		}
	}
	type pairEntry struct {
		key string
		agg *pairAgg
	}
	pairs := make([]pairEntry, 0, len(pairMap))
	for k, v := range pairMap {
		pairs = append(pairs, pairEntry{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].agg.best.Dist > pairs[j].agg.best.Dist })
	result := make([]map[string]interface{}, 0)
	for i, pe := range pairs {
		if i >= 20 {
			break
		}
		h := pe.agg.best
		var medianSNR *float64
		if len(pe.agg.snrs) > 0 {
			sorted := make([]float64, len(pe.agg.snrs))
			copy(sorted, pe.agg.snrs)
			sort.Float64s(sorted)
			mid := len(sorted) / 2
			if len(sorted)%2 == 0 {
				v := (sorted[mid-1] + sorted[mid]) / 2
				medianSNR = &v
			} else {
				v := sorted[mid]
				medianSNR = &v
			}
		}
		result = append(result, map[string]interface{}{
			"fromName": h.FromName, "fromPk": h.FromPk,
			"toName": h.ToName, "toPk": h.ToPk,
			"dist": h.Dist, "type": h.Type,
			"bestSnr": floatPtrOrNil(pe.agg.maxSNR), "medianSnr": floatPtrOrNil(medianSNR),
			"obsCount": pe.agg.obsCount,
			"hash": h.Hash, "timestamp": h.Timestamp,
		})
	}
	return result
}

func f64(v float64) *float64 { return &v }

func TestDedupeTopHopsByPair(t *testing.T) {
	hops := []distHopRecord{
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 100, Type: "R↔R", SNR: f64(5.0), Hash: "h1", Timestamp: "t1"},
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 90, Type: "R↔R", SNR: f64(8.0), Hash: "h2", Timestamp: "t2"},
		{FromPk: "BBB", ToPk: "AAA", FromName: "B", ToName: "A", Dist: 80, Type: "R↔R", SNR: f64(3.0), Hash: "h3", Timestamp: "t3"},
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 70, Type: "R↔R", SNR: f64(6.0), Hash: "h4", Timestamp: "t4"},
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 60, Type: "R↔R", SNR: f64(4.0), Hash: "h5", Timestamp: "t5"},
		{FromPk: "CCC", ToPk: "DDD", FromName: "C", ToName: "D", Dist: 50, Type: "C↔R", SNR: f64(7.0), Hash: "h6", Timestamp: "t6"},
	}

	result := dedupeHopsByPair(hops)

	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}

	// First entry: A↔B pair, max distance = 100, obsCount = 5
	ab := result[0]
	if ab["dist"].(float64) != 100 {
		t.Errorf("expected dist 100, got %v", ab["dist"])
	}
	if ab["obsCount"].(int) != 5 {
		t.Errorf("expected obsCount 5, got %v", ab["obsCount"])
	}
	if ab["hash"].(string) != "h1" {
		t.Errorf("expected hash h1, got %v", ab["hash"])
	}
	// bestSnr should be 8.0
	if ab["bestSnr"].(float64) != 8.0 {
		t.Errorf("expected bestSnr 8.0, got %v", ab["bestSnr"])
	}
	// medianSnr of [3,4,5,6,8] = 5.0
	if ab["medianSnr"].(float64) != 5.0 {
		t.Errorf("expected medianSnr 5.0, got %v", ab["medianSnr"])
	}

	// Second entry: C↔D pair
	cd := result[1]
	if cd["dist"].(float64) != 50 {
		t.Errorf("expected dist 50, got %v", cd["dist"])
	}
	if cd["obsCount"].(int) != 1 {
		t.Errorf("expected obsCount 1, got %v", cd["obsCount"])
	}
}

func TestDedupeTopHopsReversePairMerges(t *testing.T) {
	// (B,A) and (A,B) should merge
	hops := []distHopRecord{
		{FromPk: "BBB", ToPk: "AAA", FromName: "B", ToName: "A", Dist: 50, Type: "R↔R", Hash: "h1"},
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 80, Type: "R↔R", Hash: "h2"},
	}
	result := dedupeHopsByPair(hops)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result[0]["obsCount"].(int) != 2 {
		t.Errorf("expected obsCount 2, got %v", result[0]["obsCount"])
	}
	if result[0]["dist"].(float64) != 80 {
		t.Errorf("expected dist 80, got %v", result[0]["dist"])
	}
}

func TestDedupeTopHopsNilSNR(t *testing.T) {
	hops := []distHopRecord{
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 100, Type: "R↔R", SNR: nil, Hash: "h1"},
		{FromPk: "AAA", ToPk: "BBB", FromName: "A", ToName: "B", Dist: 90, Type: "R↔R", SNR: nil, Hash: "h2"},
	}
	result := dedupeHopsByPair(hops)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result[0]["bestSnr"] != nil {
		t.Errorf("expected bestSnr nil, got %v", result[0]["bestSnr"])
	}
	if result[0]["medianSnr"] != nil {
		t.Errorf("expected medianSnr nil, got %v", result[0]["medianSnr"])
	}
}
