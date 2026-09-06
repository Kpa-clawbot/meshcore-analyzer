package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// SourceStatusSnapshot is the per-MQTT-source connection state and counter
// view written to the ingestor stats file (under "source_statuses") and
// consumed by cmd/server's /api/mqtt/status handler (#1043).
//
// All fields are unix seconds (0 = "never"). PacketsLast5m is a sliding
// 5-minute count derived from a per-second ring buffer.
type SourceStatusSnapshot struct {
	Name               string `json:"name"`
	Broker             string `json:"broker"`
	Connected          bool   `json:"connected"`
	LastConnectUnix    int64  `json:"lastConnectUnix"`
	LastDisconnectUnix int64  `json:"lastDisconnectUnix"`
	LastPacketUnix     int64  `json:"lastPacketUnix"`
	ConnectCount       int64  `json:"connectCount"`
	DisconnectCount    int64  `json:"disconnectCount"`
	PacketsTotal       int64  `json:"packetsTotal"`
	PacketsLast5m      int64  `json:"packetsLast5m"`
	LastError          string `json:"lastError,omitempty"`
}

// sourceStatusState is the in-memory per-source counter set. All scalar
// fields are accessed via sync/atomic so the hot-path MarkPacket /
// MarkConnect / MarkDisconnect callsites stay lock-free. The 5-minute
// sliding window uses a 300-element per-second ring (one slot per
// second), guarded by ringMu only when we slide the cursor — the common
// path increments the current second with a single atomic.AddInt64.
//
// Memory: one state per source (typically 1-5 in production). 300 int64
// slots = 2.4KB/source — fine.
type sourceStatusState struct {
	name   string
	broker string // raw broker URL — server-side handler masks the password

	connected          atomic.Bool
	lastConnectUnix    atomic.Int64
	lastDisconnectUnix atomic.Int64
	lastPacketUnix     atomic.Int64
	connectCount       atomic.Int64
	disconnectCount    atomic.Int64
	packetsTotal       atomic.Int64

	// 5-minute sliding window: per-second buckets keyed by unix second.
	// Stored as parallel arrays so we can both zero-out a stale slot AND
	// know whether a slot's contents are still inside the window.
	ringMu    sync.Mutex
	ringSec   [300]int64 // unix second this slot represents (0 = unused)
	ringCount [300]int64 // packets received in that second

	// lastError is rare-write/rare-read so a plain mutex is fine.
	errMu     sync.RWMutex
	lastError string
}

// MarkConnect records a successful (re)connection to the broker.
// Clears any stale lastError from a prior disconnect — otherwise the UI
// shows "connected=true, lastError='connection refused'" after a successful
// reconnect, which is a lie (#1682 munger review r1).
func (s *sourceStatusState) MarkConnect(now time.Time) {
	s.connected.Store(true)
	s.lastConnectUnix.Store(now.Unix())
	s.connectCount.Add(1)
	s.errMu.Lock()
	s.lastError = ""
	s.errMu.Unlock()
}

// MarkDisconnect records the broker dropping the connection.
func (s *sourceStatusState) MarkDisconnect(now time.Time, err error) {
	s.connected.Store(false)
	s.lastDisconnectUnix.Store(now.Unix())
	s.disconnectCount.Add(1)
	if err != nil {
		s.errMu.Lock()
		s.lastError = err.Error()
		s.errMu.Unlock()
	}
}

// MarkPacket records receipt of an MQTT message. Hot path.
func (s *sourceStatusState) MarkPacket(now time.Time) {
	nowSec := now.Unix()
	s.lastPacketUnix.Store(nowSec)
	s.packetsTotal.Add(1)

	slot := nowSec % int64(len(s.ringSec))
	s.ringMu.Lock()
	if s.ringSec[slot] != nowSec {
		s.ringSec[slot] = nowSec
		s.ringCount[slot] = 0
	}
	s.ringCount[slot]++
	s.ringMu.Unlock()
}

// sumLast5m returns the count of MarkPacket calls in the last 300s. Slots
// whose stored second falls outside the window are ignored (no stale leak).
func (s *sourceStatusState) sumLast5m(now time.Time) int64 {
	nowSec := now.Unix()
	cutoff := nowSec - int64(len(s.ringSec)) + 1
	var total int64
	s.ringMu.Lock()
	for i := 0; i < len(s.ringSec); i++ {
		if s.ringSec[i] >= cutoff && s.ringSec[i] <= nowSec {
			total += s.ringCount[i]
		}
	}
	s.ringMu.Unlock()
	return total
}

// snapshot copies the state into a serializable view.
func (s *sourceStatusState) snapshot(now time.Time) SourceStatusSnapshot {
	s.errMu.RLock()
	errStr := s.lastError
	s.errMu.RUnlock()
	return SourceStatusSnapshot{
		Name:               s.name,
		Broker:             s.broker,
		Connected:          s.connected.Load(),
		LastConnectUnix:    s.lastConnectUnix.Load(),
		LastDisconnectUnix: s.lastDisconnectUnix.Load(),
		LastPacketUnix:     s.lastPacketUnix.Load(),
		ConnectCount:       s.connectCount.Load(),
		DisconnectCount:    s.disconnectCount.Load(),
		PacketsTotal:       s.packetsTotal.Load(),
		PacketsLast5m:      s.sumLast5m(now),
		LastError:          errStr,
	}
}

// sourceStatusRegistry holds one sourceStatusState per source. Keyed by
// tag (which is the source Name, or the Broker URL if the operator left
// the name blank).
var (
	sourceStatusRegistryMu sync.RWMutex
	sourceStatusRegistry   = map[string]*sourceStatusState{}
)

// RegisterSourceStatus creates (or returns the existing) state for the
// given source. Safe for cold-start use; idempotent — re-registering the
// same tag returns the existing state so counters aren't reset across
// reconnects.
func RegisterSourceStatus(tag, broker string) *sourceStatusState {
	sourceStatusRegistryMu.Lock()
	defer sourceStatusRegistryMu.Unlock()
	if s, ok := sourceStatusRegistry[tag]; ok {
		return s
	}
	s := &sourceStatusState{name: tag, broker: broker}
	sourceStatusRegistry[tag] = s
	return s
}

// lookupSourceStatus returns the state for tag, or nil if unregistered.
func lookupSourceStatus(tag string) *sourceStatusState {
	sourceStatusRegistryMu.RLock()
	defer sourceStatusRegistryMu.RUnlock()
	return sourceStatusRegistry[tag]
}

// SnapshotSourceStatuses returns a slice of every registered source's
// current snapshot. Surfaced via the ingestor stats file under
// "source_statuses" so /api/mqtt/status can serve it (#1043).
func SnapshotSourceStatuses(now time.Time) []SourceStatusSnapshot {
	sourceStatusRegistryMu.RLock()
	defer sourceStatusRegistryMu.RUnlock()
	out := make([]SourceStatusSnapshot, 0, len(sourceStatusRegistry))
	for _, s := range sourceStatusRegistry {
		out = append(out, s.snapshot(now))
	}
	return out
}

// resetSourceStatusRegistry clears the registry. Test-only helper.
func resetSourceStatusRegistry() {
	sourceStatusRegistryMu.Lock()
	defer sourceStatusRegistryMu.Unlock()
	sourceStatusRegistry = map[string]*sourceStatusState{}
}
