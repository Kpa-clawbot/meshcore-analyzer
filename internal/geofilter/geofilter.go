// Package geofilter provides the shared geographic filter configuration and
// geometry used by both the server and ingestor packages.
package geofilter

import "math"

// Config defines the geographic filter polygon or bounding box.
// Shared between the server and ingestor packages.
type Config struct {
	Polygon  [][2]float64 `json:"polygon,omitempty"`
	BufferKm float64      `json:"bufferKm,omitempty"`
	LatMin   *float64     `json:"latMin,omitempty"`
	LatMax   *float64     `json:"latMax,omitempty"`
	LonMin   *float64     `json:"lonMin,omitempty"`
	LonMax   *float64     `json:"lonMax,omitempty"`
}

// PassesFilter returns true if the coordinates fall within the filter area.
// Nodes with no GPS fix (0,0) are always allowed.
func PassesFilter(lat, lon float64, gf *Config) bool {
	if gf == nil {
		return true
	}
	if lat == 0 && lon == 0 {
		return true
	}
	if len(gf.Polygon) >= 3 {
		if PointInPolygon(lat, lon, gf.Polygon) {
			return true
		}
		if gf.BufferKm > 0 {
			n := len(gf.Polygon)
			for i := 0; i < n; i++ {
				j := (i + 1) % n
				if DistToSegmentKm(lat, lon, gf.Polygon[i], gf.Polygon[j]) <= gf.BufferKm {
					return true
				}
			}
		}
		return false
	}
	// Legacy bounding box fallback
	if gf.LatMin != nil && gf.LatMax != nil && gf.LonMin != nil && gf.LonMax != nil {
		return lat >= *gf.LatMin && lat <= *gf.LatMax && lon >= *gf.LonMin && lon <= *gf.LonMax
	}
	return true
}

// PointInPolygon uses the ray-casting algorithm.
func PointInPolygon(lat, lon float64, polygon [][2]float64) bool {
	inside := false
	n := len(polygon)
	j := n - 1
	for i := 0; i < n; i++ {
		yi, xi := polygon[i][0], polygon[i][1]
		yj, xj := polygon[j][0], polygon[j][1]
		if (yi > lat) != (yj > lat) {
			if lon < (xj-xi)*(lat-yi)/(yj-yi)+xi {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

// DistToSegmentKm returns the approximate distance in km from point (lat,lon)
// to line segment a→b using a flat-earth projection.
func DistToSegmentKm(lat, lon float64, a, b [2]float64) float64 {
	lat1, lon1 := a[0], a[1]
	lat2, lon2 := b[0], b[1]
	cosLat := math.Cos((lat1+lat2) / 2.0 * math.Pi / 180.0)
	ax := (lon1 - lon) * 111.0 * cosLat
	ay := (lat1 - lat) * 111.0
	bx := (lon2 - lon) * 111.0 * cosLat
	by := (lat2 - lat) * 111.0
	abx, aby := bx-ax, by-ay
	abSq := abx*abx + aby*aby
	if abSq == 0 {
		return math.Sqrt(ax*ax + ay*ay)
	}
	t := math.Max(0, math.Min(1, -(ax*abx+ay*aby)/abSq))
	px := ax + t*abx
	py := ay + t*aby
	return math.Sqrt(px*px + py*py)
}
