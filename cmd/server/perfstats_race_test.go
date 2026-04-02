package main

import (
	"sync"
	"testing"
	"time"
)

// TestPerfStatsConcurrentAccess verifies that concurrent writes and reads
// to PerfStats do not trigger data races. Run with: go test -race
func TestPerfStatsConcurrentAccess(t *testing.T) {
	ps := NewPerfStats()

	var wg sync.WaitGroup
	const goroutines = 50
	const iterations = 200

	// Concurrent writers (simulating perfMiddleware)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				ms := float64(j) * 0.5
				key := "/api/test"
				if id%2 == 0 {
					key = "/api/other"
				}

				ps.mu.Lock()
				ps.Requests++
				ps.TotalMs += ms
				if _, ok := ps.Endpoints[key]; !ok {
					ps.Endpoints[key] = &EndpointPerf{Recent: make([]float64, 0, 100)}
				}
				ep := ps.Endpoints[key]
				ep.Count++
				ep.TotalMs += ms
				if ms > ep.MaxMs {
					ep.MaxMs = ms
				}
				ep.Recent = append(ep.Recent, ms)
				if len(ep.Recent) > 100 {
					ep.Recent = ep.Recent[1:]
				}
				if ms > 50 {
					ps.SlowQueries = append(ps.SlowQueries, SlowQuery{
						Path: key,
						Ms:   ms,
						Time: time.Now().UTC().Format(time.RFC3339),
					})
					if len(ps.SlowQueries) > 50 {
						ps.SlowQueries = ps.SlowQueries[1:]
					}
				}
				ps.mu.Unlock()
			}
		}(i)
	}

	// Concurrent readers (simulating handlePerf / handleHealth)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				ps.mu.Lock()
				_ = ps.Requests
				_ = ps.TotalMs
				for _, ep := range ps.Endpoints {
					_ = ep.Count
					_ = ep.MaxMs
					c := make([]float64, len(ep.Recent))
					copy(c, ep.Recent)
				}
				s := make([]SlowQuery, len(ps.SlowQueries))
				copy(s, ps.SlowQueries)
				ps.mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Verify consistency
	ps.mu.Lock()
	defer ps.mu.Unlock()
	expectedRequests := int64(goroutines * iterations)
	if ps.Requests != expectedRequests {
		t.Errorf("expected %d requests, got %d", expectedRequests, ps.Requests)
	}
	if len(ps.Endpoints) == 0 {
		t.Error("expected endpoints to be populated")
	}
}
