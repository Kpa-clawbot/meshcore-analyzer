package main

import "github.com/meshcore-analyzer/geofilter"

// NodePassesGeoFilter returns true if the node should be included in responses.
// Nodes with no GPS coordinates are always allowed.
// lat and lon are interface{} because they come from DB row maps.
func NodePassesGeoFilter(lat, lon interface{}, gf *GeoFilterConfig) bool {
	if gf == nil {
		return true
	}
	latF, ok1 := toFloat64(lat)
	lonF, ok2 := toFloat64(lon)
	if !ok1 || !ok2 {
		return true
	}
	return geofilter.PassesFilter(latF, lonF, gf)
}

func toFloat64(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case nil:
		return 0, false
	}
	return 0, false
}
