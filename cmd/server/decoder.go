package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/meshcore-analyzer/packetpath"
	"github.com/meshcore-analyzer/sigvalidate"
)

// Route type constants (header bits 1-0)
const (
	RouteTransportFlood  = 0
	RouteFlood           = 1
	RouteDirect          = 2
	RouteTransportDirect = 3
)

// Payload type constants (header bits 5-2)
const (
	PayloadREQ        = 0x00
	PayloadRESPONSE   = 0x01
	PayloadTXT_MSG    = 0x02
	PayloadACK        = 0x03
	PayloadADVERT     = 0x04
	PayloadGRP_TXT    = 0x05
	PayloadGRP_DATA   = 0x06
	PayloadANON_REQ   = 0x07
	PayloadPATH       = 0x08
	PayloadTRACE      = 0x09
	PayloadMULTIPART  = 0x0A
	PayloadCONTROL    = 0x0B
	PayloadRAW_CUSTOM = 0x0F
)

var routeTypeNames = map[int]string{
	0: "TRANSPORT_FLOOD",
	1: "FLOOD",
	2: "DIRECT",
	3: "TRANSPORT_DIRECT",
}

// Header is the decoded packet header.
type Header struct {
	RouteType       int    `json:"routeType"`
	RouteTypeName   string `json:"routeTypeName"`
	PayloadType     int    `json:"payloadType"`
	PayloadTypeName string `json:"payloadTypeName"`
	PayloadVersion  int    `json:"payloadVersion"`
}

// TransportCodes are present on TRANSPORT_FLOOD and TRANSPORT_DIRECT routes.
type TransportCodes struct {
	Code1 string `json:"code1"`
	Code2 string `json:"code2"`
}

// Path holds decoded path/hop information.
type Path struct {
	HashSize      int      `json:"hashSize"`
	HashCount     int      `json:"hashCount"`
	Hops          []string `json:"hops"`
	HopsCompleted *int     `json:"hopsCompleted,omitempty"`
}

// AdvertFlags holds decoded advert flag bits.
type AdvertFlags struct {
	Raw         int  `json:"raw"`
	Type        int  `json:"type"`
	Chat        bool `json:"chat"`
	Repeater    bool `json:"repeater"`
	Room        bool `json:"room"`
	Sensor      bool `json:"sensor"`
	HasLocation bool `json:"hasLocation"`
	HasFeat1    bool `json:"hasFeat1"`
	HasFeat2    bool `json:"hasFeat2"`
	HasName     bool `json:"hasName"`
}

// Payload is a generic decoded payload. Fields are populated depending on type.
type Payload struct {
	Type          string `json:"type"`
	DestHash      string `json:"destHash,omitempty"`
	SrcHash       string `json:"srcHash,omitempty"`
	MAC           string `json:"mac,omitempty"`
	EncryptedData string `json:"encryptedData,omitempty"`
	ExtraHash     string `json:"extraHash,omitempty"`
	// Extended ACK fields per firmware 1.16.0 (issue #1610) — populated by
	// decodeAck once the server-side re-decoder is upgraded (issue #1694).
	AckLen         *int         `json:"ackLen,omitempty"`
	AckAttempt     *int         `json:"ackAttempt,omitempty"`
	AckRand        *int         `json:"ackRand,omitempty"`
	PubKey         string       `json:"pubKey,omitempty"`
	Timestamp      uint32       `json:"timestamp,omitempty"`
	TimestampISO   string       `json:"timestampISO,omitempty"`
	Signature      string       `json:"signature,omitempty"`
	SignatureValid *bool        `json:"signatureValid,omitempty"`
	Flags          *AdvertFlags `json:"flags,omitempty"`
	Lat            *float64     `json:"lat,omitempty"`
	Lon            *float64     `json:"lon,omitempty"`
	Name           string       `json:"name,omitempty"`
	ChannelHash    int          `json:"channelHash,omitempty"`
	// ANON_REQ carries the sender's FULL 32-byte public key (not a 1-byte
	// srcHash like REQ) — MeshCore firmware/src/Mesh.cpp. Surfaced as
	// srcPubKey so store.go's node indexer picks it up and it resolves to a
	// node name; frontend also reads legacy ephemeralPubKey for pre-rename
	// packets (#1864).
	SrcPubKey  string    `json:"srcPubKey,omitempty"`
	PathData   string    `json:"pathData,omitempty"`
	Tag        uint32    `json:"tag,omitempty"`
	AuthCode   uint32    `json:"authCode,omitempty"`
	TraceFlags *int      `json:"traceFlags,omitempty"`
	SNRValues  []float64 `json:"snrValues,omitempty"`
	RawHex     string    `json:"raw,omitempty"`
	Error      string    `json:"error,omitempty"`
	// GRP_TXT/GRP_DATA channel envelope helpers — see
	// firmware/src/helpers/BaseChatMesh.cpp:376-391.
	ChannelHashHex   string `json:"channelHashHex,omitempty"`
	DecryptionStatus string `json:"decryptionStatus,omitempty"`
	// GRP_DATA (PAYLOAD_TYPE_GRP_DATA=0x06) inner fields, per
	// firmware/src/helpers/BaseChatMesh.cpp:382-385.
	DataType      *int   `json:"dataType,omitempty"`
	DataLen       *int   `json:"dataLen,omitempty"`
	DecryptedBlob string `json:"decryptedBlob,omitempty"`
	// MULTIPART (PAYLOAD_TYPE_MULTIPART=0x0A) inner fields, per
	// firmware/src/Mesh.cpp:289 — byte0 = (remaining<<4) | inner_type.
	Remaining     *int   `json:"remaining,omitempty"`
	InnerType     *int   `json:"innerType,omitempty"`
	InnerTypeName string `json:"innerTypeName,omitempty"`
	InnerAckCrc   string `json:"innerAckCrc,omitempty"`
	// Extended ACK inner fields (issue #1610 / #1694) — populated by
	// decodeMultipart once ACK parity is ported from the ingestor.
	InnerAckLen     *int   `json:"innerAckLen,omitempty"`
	InnerAckAttempt *int   `json:"innerAckAttempt,omitempty"`
	InnerAckRand    *int   `json:"innerAckRand,omitempty"`
	InnerPayload    string `json:"innerPayload,omitempty"`
	// CONTROL (PAYLOAD_TYPE_CONTROL=0x0B) byte0 flags, per
	// firmware/src/Mesh.cpp:69 — high-bit = zero-hop direct subset.
	CtrlFlags   string `json:"ctrlFlags,omitempty"`
	CtrlZeroHop *bool  `json:"ctrlZeroHop,omitempty"`
	CtrlLength  *int   `json:"ctrlLength,omitempty"`
	// RAW_CUSTOM (PAYLOAD_TYPE_RAW_CUSTOM=0x0F) — application-defined per
	// firmware/src/Mesh.cpp:577 (createRawData). We expose the bare envelope
	// shape so consumers can triage by length + leading tag byte.
	RawLength    *int   `json:"rawLength,omitempty"`
	FirstByteTag string `json:"firstByteTag,omitempty"`
}

// DecodedPacket is the full decoded result.
type DecodedPacket struct {
	Header         Header          `json:"header"`
	TransportCodes *TransportCodes `json:"transportCodes"`
	Path           Path            `json:"path"`
	Payload        Payload         `json:"payload"`
	Raw            string          `json:"raw"`
	Anomaly        string          `json:"anomaly,omitempty"`
}

func decodeHeader(b byte) Header {
	rt := int(b & 0x03)
	pt := int((b >> 2) & 0x0F)
	pv := int((b >> 6) & 0x03)

	rtName := routeTypeNames[rt]
	if rtName == "" {
		rtName = "UNKNOWN"
	}
	ptName := payloadTypeNames[pt]
	if ptName == "" {
		ptName = "UNKNOWN"
	}

	return Header{
		RouteType:       rt,
		RouteTypeName:   rtName,
		PayloadType:     pt,
		PayloadTypeName: ptName,
		PayloadVersion:  pv,
	}
}

// Firmware-derived limits — see firmware/src/MeshCore.h:19,21.
const (
	maxPathSize      = 64  // MAX_PATH_SIZE — total path bytes allowed
	maxPacketPayload = 184 // MAX_PACKET_PAYLOAD — max raw payload bytes
)

// isValidPathLen mirrors firmware Packet::isValidPathLen
// (firmware/src/Packet.cpp:13-18). hash_size==4 is reserved; total path bytes
// must fit within MAX_PATH_SIZE.
func isValidPathLen(pathByte byte) bool {
	hashCount := int(pathByte & 0x3F)
	hashSize := int(pathByte>>6) + 1
	if hashSize == 4 {
		return false // reserved
	}
	return hashCount*hashSize <= maxPathSize
}

func decodePath(pathByte byte, buf []byte, offset int) (Path, int, error) {
	hashSize := int(pathByte>>6) + 1
	hashCount := int(pathByte & 0x3F)
	// Exact mirror of firmware Packet::isValidPathLen (Packet.cpp:13-18).
	// hash_size==4 is reserved and is rejected by firmware regardless of
	// hash_count, so we must reject 0xC0 etc even on zero-hop packets —
	// firmware never emits them, so an on-wire pathByte with the upper
	// 2 bits set to 11 is by definition malformed/adversarial.
	if !isValidPathLen(pathByte) {
		return Path{}, 0, fmt.Errorf("invalid path encoding: pathByte 0x%02X (hash_size=%d hash_count=%d) violates firmware validity (Packet.cpp:13-18, MAX_PATH_SIZE=%d)", pathByte, hashSize, hashCount, maxPathSize)
	}
	totalBytes := hashSize * hashCount
	hops := make([]string, 0, hashCount)

	for i := 0; i < hashCount; i++ {
		start := offset + i*hashSize
		end := start + hashSize
		if end > len(buf) {
			break
		}
		hops = append(hops, strings.ToUpper(hex.EncodeToString(buf[start:end])))
	}

	return Path{
		HashSize:  hashSize,
		HashCount: hashCount,
		Hops:      hops,
	}, totalBytes, nil
}

// isTransportRoute delegates to packetpath.IsTransportRoute.
func isTransportRoute(routeType int) bool {
	return packetpath.IsTransportRoute(routeType)
}

func decodeEncryptedPayload(typeName string, buf []byte) Payload {
	if len(buf) < 4 {
		return Payload{Type: typeName, Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	return Payload{
		Type:          typeName,
		DestHash:      hex.EncodeToString(buf[0:1]),
		SrcHash:       hex.EncodeToString(buf[1:2]),
		MAC:           hex.EncodeToString(buf[2:4]),
		EncryptedData: hex.EncodeToString(buf[4:]),
	}
}

func decodeAck(buf []byte) Payload {
	if len(buf) < 4 {
		return Payload{Type: "ACK", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	checksum := binary.LittleEndian.Uint32(buf[0:4])
	ackLen := len(buf)
	if ackLen > 6 {
		ackLen = 6
	}
	p := Payload{
		Type:      "ACK",
		ExtraHash: fmt.Sprintf("%08x", checksum),
		AckLen:    &ackLen,
	}
	// Firmware 1.16.0 extended ACK (issue #1610): 5th byte is the attempt
	// counter (commit f6e6fdaa), 6th byte is a random byte added so identical
	// attempts still hash uniquely (commit a130a95a).
	if len(buf) >= 5 {
		attempt := int(buf[4])
		p.AckAttempt = &attempt
	}
	if len(buf) >= 6 {
		rnd := int(buf[5])
		p.AckRand = &rnd
	}
	return p
}

func decodeAdvert(buf []byte, validateSignatures bool) Payload {
	if len(buf) < 100 {
		return Payload{Type: "ADVERT", Error: "too short for advert", RawHex: hex.EncodeToString(buf)}
	}

	pubKey := hex.EncodeToString(buf[0:32])
	timestamp := binary.LittleEndian.Uint32(buf[32:36])
	signature := hex.EncodeToString(buf[36:100])
	appdata := buf[100:]

	p := Payload{
		Type:         "ADVERT",
		PubKey:       pubKey,
		Timestamp:    timestamp,
		TimestampISO: fmt.Sprintf("%s", epochToISO(timestamp)),
		Signature:    signature,
	}

	if validateSignatures {
		valid, err := sigvalidate.ValidateAdvert(buf[0:32], buf[36:100], timestamp, appdata)
		if err != nil {
			f := false
			p.SignatureValid = &f
		} else {
			p.SignatureValid = &valid
		}
	}

	if len(appdata) > 0 {
		flags := appdata[0]
		advType := int(flags & 0x0F)
		hasFeat1 := flags&0x20 != 0
		hasFeat2 := flags&0x40 != 0
		p.Flags = &AdvertFlags{
			Raw:         int(flags),
			Type:        advType,
			Chat:        advType == 1,
			Repeater:    advType == 2,
			Room:        advType == 3,
			Sensor:      advType == 4,
			HasLocation: flags&0x10 != 0,
			HasFeat1:    hasFeat1,
			HasFeat2:    hasFeat2,
			HasName:     flags&0x80 != 0,
		}

		off := 1
		if p.Flags.HasLocation && len(appdata) >= off+8 {
			latRaw := int32(binary.LittleEndian.Uint32(appdata[off : off+4]))
			lonRaw := int32(binary.LittleEndian.Uint32(appdata[off+4 : off+8]))
			lat := float64(latRaw) / 1e6
			lon := float64(lonRaw) / 1e6
			p.Lat = &lat
			p.Lon = &lon
			off += 8
		}
		if hasFeat1 && len(appdata) >= off+2 {
			off += 2 // skip feat1 bytes (reserved for future use)
		}
		if hasFeat2 && len(appdata) >= off+2 {
			off += 2 // skip feat2 bytes (reserved for future use)
		}
		if p.Flags.HasName {
			name := string(appdata[off:])
			name = strings.TrimRight(name, "\x00")
			name = sanitizeName(name)
			// Firmware writes the node name into a 32-byte buffer
			// (MAX_ADVERT_DATA_SIZE, firmware/src/MeshCore.h:11). Truncate
			// here so adversarial on-wire adverts can't pollute Payload.Name
			// with bytes firmware would never emit.
			if len(name) > 32 {
				name = name[:32]
			}
			p.Name = name
		}
	}

	return p
}

func decodeGrpTxt(buf []byte) Payload {
	if len(buf) < 3 {
		return Payload{Type: "GRP_TXT", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	return Payload{
		Type:          "GRP_TXT",
		ChannelHash:   int(buf[0]),
		MAC:           hex.EncodeToString(buf[1:3]),
		EncryptedData: hex.EncodeToString(buf[3:]),
	}
}

// decodeGrpData decodes PAYLOAD_TYPE_GRP_DATA (0x06). Outer envelope is the
// same shape as GRP_TXT (channel_hash(1)+MAC(2)+ciphertext) — see
// firmware/src/helpers/BaseChatMesh.cpp:476,500. This server-side decoder has
// no channel keys, so it surfaces the envelope only.
func decodeGrpData(buf []byte) Payload {
	if len(buf) < 3 {
		return Payload{Type: "GRP_DATA", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	return Payload{
		Type:           "GRP_DATA",
		ChannelHash:    int(buf[0]),
		ChannelHashHex: fmt.Sprintf("%02X", buf[0]),
		MAC:            hex.EncodeToString(buf[1:3]),
		EncryptedData:  hex.EncodeToString(buf[3:]),
	}
}

// decodeMultipart decodes PAYLOAD_TYPE_MULTIPART (0x0A) per
// firmware/src/Mesh.cpp:287-310. byte0 = (remaining<<4) | inner_type;
// when inner_type == PAYLOAD_TYPE_ACK the next 4 bytes are an ack_crc.
func decodeMultipart(buf []byte) Payload {
	if len(buf) < 1 {
		return Payload{Type: "MULTIPART", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	remaining := int(buf[0] >> 4)
	innerType := int(buf[0] & 0x0F)
	innerName := payloadTypeNames[innerType]
	if innerName == "" {
		innerName = "UNKNOWN"
	}
	p := Payload{
		Type:          "MULTIPART",
		Remaining:     &remaining,
		InnerType:     &innerType,
		InnerTypeName: innerName,
	}
	if innerType == PayloadACK && len(buf) >= 5 {
		crc := binary.LittleEndian.Uint32(buf[1:5])
		p.InnerAckCrc = fmt.Sprintf("%08x", crc)
		// Firmware 1.16.0 extended ACK (issue #1610): inner ACK blob may be
		// 5 or 6 bytes (payload_len = 1 + ack_len) instead of always 4.
		// Attempt counter added in commit f6e6fdaa, RNG byte in commit a130a95a.
		ackLen := len(buf) - 1
		if ackLen > 6 {
			ackLen = 6
		}
		p.InnerAckLen = &ackLen
		if len(buf) >= 6 {
			attempt := int(buf[5])
			p.InnerAckAttempt = &attempt
		}
		if len(buf) >= 7 {
			rnd := int(buf[6])
			p.InnerAckRand = &rnd
		}
	} else if len(buf) > 1 {
		p.InnerPayload = hex.EncodeToString(buf[1:])
	}
	return p
}

// decodeControl decodes PAYLOAD_TYPE_CONTROL (0x0B) byte0 flags per
// firmware/src/Mesh.cpp:69 (high-bit set ⇒ zero-hop direct subset).
func decodeControl(buf []byte) Payload {
	if len(buf) < 1 {
		return Payload{Type: "CONTROL", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	zeroHop := buf[0]&0x80 != 0
	length := len(buf)
	return Payload{
		Type:        "CONTROL",
		CtrlFlags:   fmt.Sprintf("%02x", buf[0]),
		CtrlZeroHop: &zeroHop,
		CtrlLength:  &length,
		RawHex:      hex.EncodeToString(buf),
	}
}

// decodeRawCustom decodes PAYLOAD_TYPE_RAW_CUSTOM (0x0F). The payload bytes
// are application-defined per firmware/src/Mesh.cpp:577 (createRawData), so
// we only surface the bare envelope shape: total length plus the leading
// byte, which apps commonly use as a tag/type discriminator.
func decodeRawCustom(buf []byte) Payload {
	length := len(buf)
	p := Payload{
		Type:      "RAW_CUSTOM",
		RawLength: &length,
		RawHex:    hex.EncodeToString(buf),
	}
	if length > 0 {
		p.FirstByteTag = fmt.Sprintf("%02X", buf[0])
	}
	return p
}

func decodeAnonReq(buf []byte) Payload {
	if len(buf) < 35 {
		return Payload{Type: "ANON_REQ", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	return Payload{
		Type:          "ANON_REQ",
		DestHash:      hex.EncodeToString(buf[0:1]),
		SrcPubKey:     hex.EncodeToString(buf[1:33]),
		MAC:           hex.EncodeToString(buf[33:35]),
		EncryptedData: hex.EncodeToString(buf[35:]),
	}
}

func decodePathPayload(buf []byte) Payload {
	if len(buf) < 4 {
		return Payload{Type: "PATH", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	return Payload{
		Type:     "PATH",
		DestHash: hex.EncodeToString(buf[0:1]),
		SrcHash:  hex.EncodeToString(buf[1:2]),
		MAC:      hex.EncodeToString(buf[2:4]),
		PathData: hex.EncodeToString(buf[4:]),
	}
}

func decodeTrace(buf []byte) Payload {
	if len(buf) < 9 {
		return Payload{Type: "TRACE", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	tag := binary.LittleEndian.Uint32(buf[0:4])
	authCode := binary.LittleEndian.Uint32(buf[4:8])
	flags := int(buf[8])
	p := Payload{
		Type:       "TRACE",
		Tag:        tag,
		AuthCode:   authCode,
		TraceFlags: &flags,
	}
	if len(buf) > 9 {
		p.PathData = hex.EncodeToString(buf[9:])
	}
	return p
}

func decodePayload(payloadType int, buf []byte, validateSignatures bool) Payload {
	switch payloadType {
	case PayloadREQ:
		return decodeEncryptedPayload("REQ", buf)
	case PayloadRESPONSE:
		return decodeEncryptedPayload("RESPONSE", buf)
	case PayloadTXT_MSG:
		return decodeEncryptedPayload("TXT_MSG", buf)
	case PayloadACK:
		return decodeAck(buf)
	case PayloadADVERT:
		return decodeAdvert(buf, validateSignatures)
	case PayloadGRP_TXT:
		return decodeGrpTxt(buf)
	case PayloadGRP_DATA:
		return decodeGrpData(buf)
	case PayloadANON_REQ:
		return decodeAnonReq(buf)
	case PayloadPATH:
		return decodePathPayload(buf)
	case PayloadTRACE:
		return decodeTrace(buf)
	case PayloadMULTIPART:
		return decodeMultipart(buf)
	case PayloadCONTROL:
		return decodeControl(buf)
	case PayloadRAW_CUSTOM:
		return decodeRawCustom(buf)
	default:
		return Payload{Type: "UNKNOWN", RawHex: hex.EncodeToString(buf)}
	}
}

// DecodePacket decodes a hex-encoded MeshCore packet.
func DecodePacket(hexString string, validateSignatures bool) (*DecodedPacket, error) {
	hexString = strings.ReplaceAll(hexString, " ", "")
	hexString = strings.ReplaceAll(hexString, "\n", "")
	hexString = strings.ReplaceAll(hexString, "\r", "")

	buf, err := hex.DecodeString(hexString)
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %w", err)
	}
	if len(buf) < 2 {
		return nil, fmt.Errorf("packet too short (need at least header + pathLength)")
	}

	header := decodeHeader(buf[0])
	offset := 1

	var tc *TransportCodes
	if isTransportRoute(header.RouteType) {
		if len(buf) < offset+4 {
			return nil, fmt.Errorf("packet too short for transport codes")
		}
		tc = &TransportCodes{
			Code1: strings.ToUpper(hex.EncodeToString(buf[offset : offset+2])),
			Code2: strings.ToUpper(hex.EncodeToString(buf[offset+2 : offset+4])),
		}
		offset += 4
	}

	if offset >= len(buf) {
		return nil, fmt.Errorf("packet too short (no path byte)")
	}
	pathByte := buf[offset]
	offset++

	path, bytesConsumed, decodeErr := decodePath(pathByte, buf, offset)
	if decodeErr != nil {
		return nil, decodeErr
	}
	offset += bytesConsumed

	// Bounds check — see cmd/ingestor/decoder.go for full rationale (#1211).
	if offset > len(buf) {
		return nil, fmt.Errorf("packet path length (%d bytes claimed by pathByte 0x%02X) exceeds buffer (%d bytes)", bytesConsumed, pathByte, len(buf))
	}

	payloadBuf := buf[offset:]
	// Firmware caps payload at MAX_PACKET_PAYLOAD=184 (firmware/src/MeshCore.h:19).
	// Anything larger cannot be a valid wire packet — drop it.
	if len(payloadBuf) > maxPacketPayload {
		return nil, fmt.Errorf("packet payload (%d bytes) exceeds firmware MAX_PACKET_PAYLOAD=%d (MeshCore.h:19)", len(payloadBuf), maxPacketPayload)
	}
	payload := decodePayload(header.PayloadType, payloadBuf, validateSignatures)

	// TRACE packets store hop IDs in the payload (buf[9:]) rather than the header
	// path field. Firmware always sends TRACE as DIRECT (route_type 2 or 3);
	// FLOOD-routed TRACEs are anomalous but handled gracefully (parsed, but
	// flagged). The TRACE flags byte (payload offset 8) encodes path_sz in
	// bits 0-1 as a power-of-two exponent: hash_bytes = 1 << path_sz.
	// NOT the header path byte's hash_size bits. The header path contains SNR
	// bytes — one per hop that actually forwarded.
	// We expose hopsCompleted (count of SNR bytes) so consumers can distinguish
	// how far the trace got vs the full intended route.
	var anomaly string
	if header.PayloadType == PayloadTRACE && payload.PathData != "" {
		// Flag anomalous routing — firmware only sends TRACE as DIRECT
		if header.RouteType != RouteDirect && header.RouteType != RouteTransportDirect {
			anomaly = "TRACE packet with non-DIRECT routing (expected DIRECT or TRANSPORT_DIRECT)"
		}
		// The header path hops count represents SNR entries = completed hops
		hopsCompleted := path.HashCount
		// Extract per-hop SNR from header path bytes (int8, quarter-dB encoding)
		if hopsCompleted > 0 && len(path.Hops) >= hopsCompleted {
			snrVals := make([]float64, 0, hopsCompleted)
			for i := 0; i < hopsCompleted; i++ {
				b, err := hex.DecodeString(path.Hops[i])
				if err == nil && len(b) == 1 {
					snrVals = append(snrVals, float64(int8(b[0]))/4.0)
				}
			}
			if len(snrVals) > 0 {
				payload.SNRValues = snrVals
			}
		}
		pathBytes, err := hex.DecodeString(payload.PathData)
		if err == nil && payload.TraceFlags != nil {
			// path_sz from flags byte is a power-of-two exponent per firmware:
			// hash_bytes = 1 << (flags & 0x03)
			pathSz := 1 << (*payload.TraceFlags & 0x03)
			hops := make([]string, 0, len(pathBytes)/pathSz)
			for i := 0; i+pathSz <= len(pathBytes); i += pathSz {
				hops = append(hops, strings.ToUpper(hex.EncodeToString(pathBytes[i:i+pathSz])))
			}
			path.Hops = hops
			path.HashCount = len(hops)
			path.HashSize = pathSz
			path.HopsCompleted = &hopsCompleted
		}
	}

	// Zero-hop direct packets have hash_count=0 (lower 6 bits of pathByte),
	// which makes the generic formula yield a bogus hashSize. Reset to 0
	// (unknown) so API consumers get correct data. We mask with 0x3F to check
	// only hash_count, matching the JS frontend approach — the upper hash_size
	// bits are meaningless when there are no hops. Skip TRACE packets — they
	// use hashSize to parse hops from the payload above.
	if (header.RouteType == RouteDirect || header.RouteType == RouteTransportDirect) && pathByte&0x3F == 0 && header.PayloadType != PayloadTRACE {
		path.HashSize = 0
	}

	return &DecodedPacket{
		Header:         header,
		TransportCodes: tc,
		Path:           path,
		Payload:        payload,
		Raw:            strings.ToUpper(hexString),
		Anomaly:        anomaly,
	}, nil
}

// ComputeContentHash computes the SHA-256-based content hash (first 16 hex chars).
// It hashes the payload-type nibble + payload (skipping path bytes) to produce a
// route-independent identifier for the same logical packet. For TRACE packets,
// path_len is included in the hash to match firmware behavior.
func ComputeContentHash(rawHex string) string {
	buf, err := hex.DecodeString(rawHex)
	if err != nil || len(buf) < 2 {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}

	headerByte := buf[0]
	offset := 1
	if isTransportRoute(int(headerByte & 0x03)) {
		offset += 4
	}
	if offset >= len(buf) {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}
	pathByte := buf[offset]
	offset++
	hashSize := int((pathByte>>6)&0x3) + 1
	hashCount := int(pathByte & 0x3F)
	pathBytes := hashSize * hashCount

	payloadStart := offset + pathBytes
	if payloadStart > len(buf) {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}

	payload := buf[payloadStart:]

	// Hash payload-type byte only (bits 2-5 of header), not the full header.
	// Firmware: SHA256(payload_type + [path_len for TRACE] + payload)
	// Using the full header caused different hashes for the same logical packet
	// when route type or version bits differed. See issue #786.
	payloadType := (headerByte >> 2) & 0x0F
	toHash := []byte{payloadType}
	if int(payloadType) == PayloadTRACE {
		// Firmware uses uint16_t path_len (2 bytes, little-endian)
		toHash = append(toHash, pathByte, 0x00)
	}
	toHash = append(toHash, payload...)

	h := sha256.Sum256(toHash)
	return hex.EncodeToString(h[:])[:16]
}

// PayloadJSON serializes the payload to JSON for DB storage.
func PayloadJSON(p *Payload) string {
	b, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ValidateAdvert checks decoded advert data before DB insertion.
func ValidateAdvert(p *Payload) (bool, string) {
	if p == nil || p.Error != "" {
		reason := "null advert"
		if p != nil {
			reason = p.Error
		}
		return false, reason
	}

	pk := p.PubKey
	if len(pk) < 16 {
		return false, fmt.Sprintf("pubkey too short (%d hex chars)", len(pk))
	}
	allZero := true
	for _, c := range pk {
		if c != '0' {
			allZero = false
			break
		}
	}
	if allZero {
		return false, "pubkey is all zeros"
	}

	if p.Lat != nil {
		if math.IsInf(*p.Lat, 0) || math.IsNaN(*p.Lat) || *p.Lat < -90 || *p.Lat > 90 {
			return false, fmt.Sprintf("invalid lat: %f", *p.Lat)
		}
	}
	if p.Lon != nil {
		if math.IsInf(*p.Lon, 0) || math.IsNaN(*p.Lon) || *p.Lon < -180 || *p.Lon > 180 {
			return false, fmt.Sprintf("invalid lon: %f", *p.Lon)
		}
	}

	if p.Name != "" {
		for _, c := range p.Name {
			if (c >= 0x00 && c <= 0x08) || c == 0x0b || c == 0x0c || (c >= 0x0e && c <= 0x1f) || c == 0x7f {
				return false, "name contains control characters"
			}
		}
		if len(p.Name) > 64 {
			return false, fmt.Sprintf("name too long (%d chars)", len(p.Name))
		}
	}

	if p.Flags != nil {
		role := advertRole(p.Flags)
		// Accept canonical labels plus "none" (ADV_TYPE_NONE=0) and "type-N"
		// placeholders for ADV_TYPE 5-15 (FUTURE) — see
		// firmware/src/helpers/AdvertDataHelpers.h:7-12.
		validRoles := map[string]bool{
			"repeater": true, "companion": true, "room": true, "sensor": true, "none": true,
		}
		if !validRoles[role] && !strings.HasPrefix(role, "type-") {
			return false, fmt.Sprintf("unknown role: %s", role)
		}
	}

	return true, ""
}

// sanitizeName strips non-printable characters (< 0x20 except tab/newline) and DEL.
func sanitizeName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		if c == '\t' || c == '\n' || (c >= 0x20 && c != 0x7f) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// advertRole returns a stable role label for an advert. Follows firmware
// ADV_TYPE_* constants in firmware/src/helpers/AdvertDataHelpers.h:7-12:
//
//	0 NONE, 1 CHAT, 2 REPEATER, 3 ROOM, 4 SENSOR, 5-15 FUTURE.
//
// Previously this coerced both 0 (NONE) and 5-15 (FUTURE) to "companion",
// silently relabelling unknown/reserved types — see issue #1279 P1 #3.
func advertRole(f *AdvertFlags) string {
	if f == nil {
		return "companion"
	}
	switch f.Type {
	case 0:
		return "none"
	case 1:
		return "companion"
	case 2:
		return "repeater"
	case 3:
		return "room"
	case 4:
		return "sensor"
	default:
		return fmt.Sprintf("type-%d", f.Type)
	}
}

func epochToISO(epoch uint32) string {
	t := time.Unix(int64(epoch), 0)
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
