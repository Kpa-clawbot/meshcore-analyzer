package main

import "github.com/meshcore-analyzer/geofilter"

// NodePassesGeoFilter returns true if the node should be kept.
// Nodes with no GPS coordinates are always allowed.
func NodePassesGeoFilter(lat, lon *float64, gf *GeoFilterConfig) bool {
	if gf == nil {
		return true
	}
	if lat == nil || lon == nil {
		return true
	}
	return geofilter.PassesFilter(*lat, *lon, gf)
}
