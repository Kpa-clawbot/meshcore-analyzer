package main

import "math"

// NodePassesGeoFilter returns true if the node should be kept.
// Nodes with no GPS coordinates are always allowed.
func NodePassesGeoFilter(lat, lon *float64, gf *GeoFilterConfig) bool {
	if gf == nil {
		return true
	}
	if lat == nil || lon == nil {
		return true
	}
	if *lat == 0 && *lon == 0 {
		return true
	}
	if len(gf.Polygon) >= 3 {
		if pointInPolygon(*lat, *lon, gf.Polygon) {
			return true
		}
		if gf.BufferKm > 0 {
			n := len(gf.Polygon)
			for i := 0; i < n; i++ {
				j := (i + 1) % n
				if distToSegmentKm(*lat, *lon, gf.Polygon[i], gf.Polygon[j]) <= gf.BufferKm {
					return true
				}
			}
		}
		return false
	}
	// Legacy bounding box
	if gf.LatMin != nil && gf.LatMax != nil && gf.LonMin != nil && gf.LonMax != nil {
		return *lat >= *gf.LatMin && *lat <= *gf.LatMax && *lon >= *gf.LonMin && *lon <= *gf.LonMax
	}
	return true
}

func pointInPolygon(lat, lon float64, polygon [][2]float64) bool {
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

func distToSegmentKm(lat, lon float64, a, b [2]float64) float64 {
	lat1, lon1 := a[0], a[1]
	lat2, lon2 := b[0], b[1]
	cosLat := math.Cos((lat1 + lat2) / 2.0 * math.Pi / 180.0)
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
