package main

import "testing"

// Simple square polygon: lat 51–52, lon 3–5
var testPoly = [][2]float64{
	{51.0, 3.0},
	{52.0, 3.0},
	{52.0, 5.0},
	{51.0, 5.0},
}

func fptr(f float64) *float64 { return &f }

func TestNodePassesGeoFilter_NilConfig(t *testing.T) {
	if !NodePassesGeoFilter(fptr(51.5), fptr(4.0), nil) {
		t.Error("nil config should allow all nodes")
	}
}

func TestNodePassesGeoFilter_NilCoords(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: testPoly}
	if !NodePassesGeoFilter(nil, nil, gf) {
		t.Error("nil coords (no GPS fix) should always pass")
	}
}

func TestNodePassesGeoFilter_ZeroCoords(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: testPoly}
	if !NodePassesGeoFilter(fptr(0), fptr(0), gf) {
		t.Error("0,0 coords (no GPS fix) should always pass")
	}
}

func TestNodePassesGeoFilter_InsidePolygon(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: testPoly}
	if !NodePassesGeoFilter(fptr(51.5), fptr(4.0), gf) {
		t.Error("point inside polygon should pass")
	}
}

func TestNodePassesGeoFilter_OutsideNoBuffer(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: testPoly, BufferKm: 0}
	if NodePassesGeoFilter(fptr(50.0), fptr(4.0), gf) {
		t.Error("point outside polygon with no buffer should fail")
	}
}

func TestNodePassesGeoFilter_InBufferZone(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: testPoly, BufferKm: 50}
	// ~20 km south of the polygon's southern edge (lat=51.0)
	if !NodePassesGeoFilter(fptr(50.82), fptr(4.0), gf) {
		t.Error("point within buffer zone should pass")
	}
}

func TestNodePassesGeoFilter_BeyondBuffer(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: testPoly, BufferKm: 10}
	// ~200 km south — well beyond any buffer
	if NodePassesGeoFilter(fptr(49.0), fptr(4.0), gf) {
		t.Error("point far beyond buffer should fail")
	}
}

func TestNodePassesGeoFilter_LegacyBBox(t *testing.T) {
	latMin, latMax := 51.0, 52.0
	lonMin, lonMax := 3.0, 5.0
	gf := &GeoFilterConfig{LatMin: &latMin, LatMax: &latMax, LonMin: &lonMin, LonMax: &lonMax}

	if !NodePassesGeoFilter(fptr(51.5), fptr(4.0), gf) {
		t.Error("point inside bbox should pass")
	}
	if NodePassesGeoFilter(fptr(50.0), fptr(4.0), gf) {
		t.Error("point outside bbox should fail")
	}
}
