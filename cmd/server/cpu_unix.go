//go:build !windows

package main

import (
	"syscall"
	"time"
)

// getCPUPercent returns the process CPU usage percentage since the last call.
// It uses getrusage(2) (user+sys time) divided by wall-clock elapsed time.
// The first call always returns 0 because there is no previous sample to diff against.
// Values can exceed 100 % on multi-core systems if multiple goroutines are saturating CPUs.
func (s *Server) getCPUPercent() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	// Explicit int64 casts — Usec is int32 on Darwin, int64 on Linux; both are safe here.
	cpuNs := int64(ru.Utime.Sec)*1_000_000_000 + int64(ru.Utime.Usec)*1_000 +
		int64(ru.Stime.Sec)*1_000_000_000 + int64(ru.Stime.Usec)*1_000

	now := time.Now()

	s.cpuMu.Lock()
	defer s.cpuMu.Unlock()

	if s.cpuLastWall.IsZero() {
		// First sample — store baseline and return 0 (no delta to compute yet).
		s.cpuLastWall = now
		s.cpuLastCPUNs = cpuNs
		return 0
	}

	wallNs := float64(now.Sub(s.cpuLastWall).Nanoseconds())
	cpuDeltaNs := float64(cpuNs - s.cpuLastCPUNs)

	s.cpuLastWall = now
	s.cpuLastCPUNs = cpuNs

	if wallNs <= 0 {
		return 0
	}
	pct := cpuDeltaNs / wallNs * 100.0
	if pct < 0 {
		return 0
	}
	return pct
}
