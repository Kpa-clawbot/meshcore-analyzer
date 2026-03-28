package main

import "testing"

// Simple square polygon: lat 51–52, lon 3–5
var serverTestPoly = [][2]float64{
	{51.0, 3.0},
	{52.0, 3.0},
	{52.0, 5.0},
	{51.0, 5.0},
}

func TestServerNodePassesGeoFilter_NilConfig(t *testing.T) {
	if !NodePassesGeoFilter(51.5, 4.0, nil) {
		t.Error("nil config should allow all nodes")
	}
}

func TestServerNodePassesGeoFilter_NilCoords(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: serverTestPoly}
	if !NodePassesGeoFilter(nil, nil, gf) {
		t.Error("nil coords (no GPS fix) should always pass")
	}
}

func TestServerNodePassesGeoFilter_ZeroCoords(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: serverTestPoly}
	if !NodePassesGeoFilter(0.0, 0.0, gf) {
		t.Error("0,0 coords (no GPS fix) should always pass")
	}
}

func TestServerNodePassesGeoFilter_InsidePolygon(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: serverTestPoly}
	if !NodePassesGeoFilter(51.5, 4.0, gf) {
		t.Error("point inside polygon should pass")
	}
}

func TestServerNodePassesGeoFilter_OutsideNoBuffer(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: serverTestPoly, BufferKm: 0}
	if NodePassesGeoFilter(50.0, 4.0, gf) {
		t.Error("point outside polygon with no buffer should fail")
	}
}

func TestServerNodePassesGeoFilter_InBufferZone(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: serverTestPoly, BufferKm: 50}
	// ~20 km south of the polygon's southern edge (lat=51.0)
	if !NodePassesGeoFilter(50.82, 4.0, gf) {
		t.Error("point within buffer zone should pass")
	}
}

func TestServerNodePassesGeoFilter_BeyondBuffer(t *testing.T) {
	gf := &GeoFilterConfig{Polygon: serverTestPoly, BufferKm: 10}
	if NodePassesGeoFilter(49.0, 4.0, gf) {
		t.Error("point far beyond buffer should fail")
	}
}

func TestServerNodePassesGeoFilter_LegacyBBox(t *testing.T) {
	latMin, latMax := 51.0, 52.0
	lonMin, lonMax := 3.0, 5.0
	gf := &GeoFilterConfig{LatMin: &latMin, LatMax: &latMax, LonMin: &lonMin, LonMax: &lonMax}

	if !NodePassesGeoFilter(51.5, 4.0, gf) {
		t.Error("point inside bbox should pass")
	}
	if NodePassesGeoFilter(50.0, 4.0, gf) {
		t.Error("point outside bbox should fail")
	}
}

func TestToFloat64Types(t *testing.T) {
	cases := []struct {
		input interface{}
		want  float64
		ok    bool
	}{
		{float64(1.5), 1.5, true},
		{float32(2.5), 2.5, true},
		{int(3), 3.0, true},
		{int64(4), 4.0, true},
		{nil, 0, false},
		{"nope", 0, false},
	}
	for _, tc := range cases {
		got, ok := toFloat64(tc.input)
		if ok != tc.ok {
			t.Errorf("toFloat64(%v): ok=%v, want %v", tc.input, ok, tc.ok)
		}
		if ok && got != tc.want {
			t.Errorf("toFloat64(%v): got %v, want %v", tc.input, got, tc.want)
		}
	}
}
