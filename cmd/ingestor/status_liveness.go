package main

import (
	"strings"
	"time"
)

// statusLivenessMaxAge is how far a status payload's own timestamp may lag
// before the message stops counting as evidence the observer is alive now.
//
// Deliberately generous: observer clocks are naive (#1478) and can sit a whole
// timezone off, so anything under a day has to be accepted. The replays this
// guards against are months stale, so the margin costs nothing.
const statusLivenessMaxAge = 24 * time.Hour

// statusIsLiveness reports whether a status message proves the observer was
// alive when we received it.
//
// The retain flag alone cannot answer that. Between EMQX and the ingestor sits
// a mosquitto bridge, and MQTT only sets RETAIN on messages delivered in
// response to a *new* subscription. The bridge runs cleansession, so every
// reconnect resubscribes and pulls EMQX's entire retained set — which it then
// republishes into its own broker as ordinary traffic. The ingestor is already
// subscribed there, so those months-old snapshots arrive with RETAIN cleared,
// indistinguishable from live status. On 2026-08-13 an EMQX restart replayed
// them and recreated 17 long-dead observers in two seconds.
//
// So judge the payload instead of the transport:
//   - a status that is not "online" is the observer's own offline notice (its
//     LWT, or a replay of it) and is never proof of life;
//   - a timestamp older than statusLivenessMaxAge describes a past that may be
//     months old.
//
// A payload with no timestamp, or one we cannot parse, is unjudgeable and
// keeps the previous behaviour — this guard removes false liveness, it does
// not invent new grounds to discard observers.
func statusIsLiveness(msg map[string]interface{}, now time.Time) bool {
	if s, ok := msg["status"].(string); ok && s != "" && !strings.EqualFold(s, "online") {
		return false
	}
	raw, _ := msg["timestamp"].(string)
	if raw == "" {
		return true
	}
	t, _, err := parseEnvelopeTime(raw)
	if err != nil {
		return true
	}
	return !t.Before(now.Add(-statusLivenessMaxAge))
}
