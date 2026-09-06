package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// mqttBrokerSchemes is the set of broker URL schemes whose embedded
// `user:pass@host` credentials we want to redact. We URL-parse for these
// (defense vs. passwords containing `@`); other strings fall through to
// the legacy regex pass for embedded user:pass occurrences in free-form
// error strings.
var mqttBrokerSchemes = map[string]bool{
	"mqtt": true, "mqtts": true, "tcp": true, "ssl": true, "ws": true, "wss": true,
}

// mqttBrokerURLRe locates a broker URL (with credentials) embedded inside
// a larger free-form string — e.g. an error message that quotes the
// failing broker. Each match is fed through url.Parse + redaction. We
// match greedily up through the LAST `@` followed by a host-shaped token
// so passwords containing `@` are not truncated (#1682 adversarial r1).
//
// Go's RE2 has no lookahead; we capture the host tail and emit it
// unchanged in the replacement.
var mqttBrokerURLRe = regexp.MustCompile(`(?i)(?:mqtt|mqtts|tcp|ssl|ws|wss)://[^\s]*`)

// maskBrokerURL returns the broker URL with any inline password redacted.
// `mqtt://user:secret@host:1883` -> `mqtt://user:****@host:1883`.
// `mqtt://user:p@ss@host` -> `mqtt://user:****@host` (password with `@`).
// URLs without inline credentials are returned unchanged.
//
// Primary strategy: url.Parse — handles passwords with `@`, `:`, etc.
// Fallback: regex sweep for free-form strings (e.g. error messages that
// quote a URL fragment but aren't standalone-parseable).
func maskBrokerURL(s string) string {
	if s == "" {
		return s
	}
	// Fast path: the whole string is the broker URL.
	if masked, ok := redactBrokerURL(s); ok {
		return masked
	}
	// Fallback: free-form string (e.g. error message) containing a URL.
	// Find embedded broker URLs and redact each in-place.
	return mqttBrokerURLRe.ReplaceAllStringFunc(s, func(m string) string {
		if out, ok := redactBrokerURL(m); ok {
			return out
		}
		return m
	})
}

// redactBrokerURL parses s as a URL and, if it has an mqtt-family scheme
// with userinfo containing a password, returns the URL with the password
// replaced by `****`. Returns ok=false when s is not such a URL.
func redactBrokerURL(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.User == nil {
		return s, false
	}
	if !mqttBrokerSchemes[strings.ToLower(u.Scheme)] {
		return s, false
	}
	if _, hasPass := u.User.Password(); !hasPass {
		return s, false
	}
	// Re-assemble manually rather than via url.UserPassword + u.String()
	// because the latter percent-encodes the `*` mask token into `%2A`,
	// defeating the user-visible redaction marker. We only need to swap
	// the userinfo segment of the original string.
	hostAndAfter := s
	if idx := strings.LastIndex(s, "@"); idx >= 0 {
		hostAndAfter = s[idx+1:]
	}
	// Preserve original scheme casing (url.Parse lowercases u.Scheme).
	schemeEnd := strings.Index(s, "://")
	if schemeEnd < 0 {
		return s, false
	}
	return s[:schemeEnd] + "://" + u.User.Username() + ":****@" + hostAndAfter, true
}

// MqttSourceStatus is the per-MQTT-source status row surfaced via
// /api/mqtt/status. Mirrors the on-disk shape the ingestor publishes
// (cmd/ingestor SourceStatusSnapshot) but with the broker URL credentials
// redacted before serving — operators must not see the broker password
// in the API response (#1043 acceptance criterion).
type MqttSourceStatus struct {
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

// MqttStatusResponse is the JSON envelope returned by /api/mqtt/status.
type MqttStatusResponse struct {
	Sources  []MqttSourceStatus `json:"sources"`
	SampleAt string             `json:"sampleAt"`
	// WatchdogLastTickUnix (#1749) is the unix-seconds timestamp of the
	// most recent ingestor watchdog tick. Surfaced so external monitoring
	// can detect a wedged watchdog goroutine (value older than ~2× the
	// scan interval — typically 60s — means the watchdog itself died,
	// not just one source). 0 / omitted: ingestor has never ticked yet
	// or is running an older build that did not publish this field.
	WatchdogLastTickUnix int64 `json:"watchdogLastTickUnix,omitempty"`
	// WatchdogPanicCount (#1810 round-1) is the running total of
	// recovered panics inside the ingestor's watchdog per-source work
	// IIFE. Surfaced alongside WatchdogLastTickUnix because the tick
	// clock is stamped BEFORE per-source work — a loop panicking on
	// every source still advances the tick and looks healthy by tick
	// alone. A rapidly-growing value means the watchdog is alive but
	// per-source processing is broken. 0 / omitted: no recovered
	// panics OR older ingestor build.
	WatchdogPanicCount int64 `json:"watchdogPanicCount,omitempty"`
	// WatchdogLogDropCount (#1749 root-cause fix) is the running total
	// of watchdog log lines dropped because the async emit queue's
	// background writer could not keep up — almost always because its
	// underlying write() is itself stuck (Docker JSON-file log driver
	// backpressure, full stderr pipe, etc.). Distinguishes "watchdog
	// dead" (WatchdogLastTickUnix stale) from "watchdog alive, but its
	// log sink is stuck" (ticking normally, this climbing). 0 /
	// omitted: writer never fell behind OR older ingestor build.
	WatchdogLogDropCount int64 `json:"watchdogLogDropCount,omitempty"`
}

// ingestorMqttStatusEnvelope is the partial shape the server decodes from
// the ingestor stats file (additive — older ingestors omit the field).
type ingestorMqttStatusEnvelope struct {
	SampledAt            string             `json:"sampledAt"`
	SourceStatuses       []MqttSourceStatus `json:"source_statuses"`
	WatchdogLastTickUnix int64              `json:"watchdogLastTickUnix"`
	WatchdogPanicCount   int64              `json:"watchdogPanicCount"`
	WatchdogLogDropCount int64              `json:"watchdogLogDropCount"`
}

// handleMqttStatus serves GET /api/mqtt/status. Reads the ingestor stats
// file, masks broker-URL passwords, and returns the per-source status
// list. Returns an empty list (200 OK) when the stats file is missing
// or unparseable — the UI panel renders a "no data yet" state.
func (s *Server) handleMqttStatus(w http.ResponseWriter, r *http.Request) {
	resp := MqttStatusResponse{Sources: []MqttSourceStatus{}, SampleAt: ""}
	data, err := os.ReadFile(IngestorStatsPath())
	if err != nil {
		writeJSON(w, resp)
		return
	}
	var env ingestorMqttStatusEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		writeJSON(w, resp)
		return
	}
	resp.SampleAt = env.SampledAt
	resp.WatchdogLastTickUnix = env.WatchdogLastTickUnix
	resp.WatchdogPanicCount = env.WatchdogPanicCount
	resp.WatchdogLogDropCount = env.WatchdogLogDropCount
	for _, src := range env.SourceStatuses {
		src.Broker = maskBrokerURL(src.Broker)
		// Broker libraries occasionally quote the failing URL in the
		// error string — redact there too as defense-in-depth.
		src.LastError = maskBrokerURL(src.LastError)
		resp.Sources = append(resp.Sources, src)
	}
	writeJSON(w, resp)
}
