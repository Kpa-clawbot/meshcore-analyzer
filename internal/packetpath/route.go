package packetpath

// Route type constants (header bits 1-0).
const (
	RouteTransportFlood  = 0
	RouteFlood           = 1
	RouteDirect          = 2
	RouteTransportDirect = 3
)

// PayloadTRACE is the payload type constant for TRACE packets.
const PayloadTRACE = 0x09

// IsTransportRoute returns true for TRANSPORT_FLOOD (0) and TRANSPORT_DIRECT (3).
func IsTransportRoute(routeType int) bool {
	return routeType == RouteTransportFlood || routeType == RouteTransportDirect
}

// PathBytesAreHops returns true when the raw_hex header path bytes represent
// route hop hashes (the normal case). Returns false for packet types where
// header path bytes are repurposed (e.g. TRACE uses them for SNR values).
func PathBytesAreHops(payloadType byte) bool {
	return payloadType != PayloadTRACE
}
