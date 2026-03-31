package main

import dec "github.com/corescope/internal/decoder"

const (
	RouteTransportFlood  = dec.RouteTransportFlood
	RouteFlood           = dec.RouteFlood
	RouteDirect          = dec.RouteDirect
	RouteTransportDirect = dec.RouteTransportDirect

	PayloadREQ        = dec.PayloadREQ
	PayloadRESPONSE   = dec.PayloadRESPONSE
	PayloadTXT_MSG    = dec.PayloadTXT_MSG
	PayloadACK        = dec.PayloadACK
	PayloadADVERT     = dec.PayloadADVERT
	PayloadGRP_TXT    = dec.PayloadGRP_TXT
	PayloadGRP_DATA   = dec.PayloadGRP_DATA
	PayloadANON_REQ   = dec.PayloadANON_REQ
	PayloadPATH       = dec.PayloadPATH
	PayloadTRACE      = dec.PayloadTRACE
	PayloadMULTIPART  = dec.PayloadMULTIPART
	PayloadCONTROL    = dec.PayloadCONTROL
	PayloadRAW_CUSTOM = dec.PayloadRAW_CUSTOM
)

type Header = dec.Header
type TransportCodes = dec.TransportCodes
type Path = dec.Path
type AdvertFlags = dec.AdvertFlags
type Payload = dec.Payload
type DecodedPacket = dec.DecodedPacket

func DecodePacket(hexString string) (*DecodedPacket, error) {
	return dec.DecodePacket(hexString, nil)
}

func ComputeContentHash(rawHex string) string {
	return dec.ComputeContentHash(rawHex)
}

func PayloadJSON(p *Payload) string {
	return dec.PayloadJSON(p)
}

func ValidateAdvert(p *Payload) (bool, string) {
	return dec.ValidateAdvert(p)
}
