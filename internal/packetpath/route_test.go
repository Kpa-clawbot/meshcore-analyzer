package packetpath

import "testing"

func TestIsTransportRoute(t *testing.T) {
	if !IsTransportRoute(RouteTransportFlood) {
		t.Error("RouteTransportFlood should be transport")
	}
	if !IsTransportRoute(RouteTransportDirect) {
		t.Error("RouteTransportDirect should be transport")
	}
	if IsTransportRoute(RouteFlood) {
		t.Error("RouteFlood should not be transport")
	}
	if IsTransportRoute(RouteDirect) {
		t.Error("RouteDirect should not be transport")
	}
}

func TestPathBytesAreHops(t *testing.T) {
	if PathBytesAreHops(PayloadTRACE) {
		t.Error("PathBytesAreHops(PayloadTRACE) should be false")
	}
	// All other known payload types should return true.
	otherTypes := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F}
	for _, pt := range otherTypes {
		if !PathBytesAreHops(pt) {
			t.Errorf("PathBytesAreHops(0x%02X) should be true", pt)
		}
	}
}
