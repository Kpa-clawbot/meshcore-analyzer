// Package packetpath provides shared helpers for extracting path hops from
// raw MeshCore packet hex bytes.
package packetpath

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// isTransportRoute returns true for TRANSPORT_FLOOD (0) and TRANSPORT_DIRECT (3).
func isTransportRoute(routeType int) bool {
	return routeType == 0 || routeType == 3
}

// DecodePathFromRawHex extracts the header path hops directly from raw hex bytes.
// This is the authoritative path that matches what's in raw_hex, as opposed to
// decoded.Path.Hops which may be overwritten for TRACE packets (issue #886).
func DecodePathFromRawHex(rawHex string) ([]string, error) {
	buf, err := hex.DecodeString(rawHex)
	if err != nil || len(buf) < 2 {
		return nil, fmt.Errorf("invalid or too-short hex")
	}

	headerByte := buf[0]
	offset := 1
	if isTransportRoute(int(headerByte & 0x03)) {
		if len(buf) < offset+4 {
			return nil, fmt.Errorf("too short for transport codes")
		}
		offset += 4
	}
	if offset >= len(buf) {
		return nil, fmt.Errorf("too short for path byte")
	}

	pathByte := buf[offset]
	offset++

	hashSize := int(pathByte>>6) + 1
	hashCount := int(pathByte & 0x3F)

	hops := make([]string, 0, hashCount)
	for i := 0; i < hashCount; i++ {
		start := offset + i*hashSize
		end := start + hashSize
		if end > len(buf) {
			break
		}
		hops = append(hops, strings.ToUpper(hex.EncodeToString(buf[start:end])))
	}
	return hops, nil
}
