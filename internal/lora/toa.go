// Package lora implements closed-form LoRa Time-on-Air calculations.
//
// Issue #1768 — replaces the bytes-only proxy in
// cmd/server/relay_airtime_share.go with a true ToA estimate so the
// "Airtime %" headline metric is no longer biased ~3-4× against small
// frames by the preamble + fixed-symbol intercept.
//
// Reference: Semtech AN1200.13 / SX126x datasheet v1.1 §6.1.4.
// Cross-checked against RadioLib calculateTimeOnAir() (issue #1768
// discussion); MeshCore-specific constants:
//
//   - CRC = 1   (setCRC(1) in MeshCore drivers)
//   - IH  = 0   (explicit-header default, never overridden)
//   - DE  = 1 iff T_sym ≥ 16 ms (per SX126x_commands.cpp:224)
//
// Preamble follows MeshCore's preambleLengthForSF (firmware
// RadioLibWrappers.h:47, MeshCore PR #1954): 32 symbols for SF≤8, 16
// otherwise. Callers should pass PreambleForSF(sf) when modeling the
// MeshCore default; if Preset.Preamble is zero TimeOnAir falls back to
// the LoRa-protocol default 8.
package lora

import (
	"math"
	"time"
)

// Preset captures the LoRa PHY parameters needed to compute ToA.
// FreqHz is informational only (recorded for the analytics caption)
// and does not enter the ToA formula.
type Preset struct {
	FreqHz   float64 // e.g. 869.6e6 — informational only
	BWkHz    float64 // bandwidth in kHz (e.g. 62.5, 125, 250)
	SF       int     // spreading factor (6..12)
	CR       int     // coding-rate denominator suffix: 5 ⇒ 4/5 … 8 ⇒ 4/8
	Preamble int     // preamble symbols; 0 ⇒ LoRa default 8
}

// PreambleForSF returns MeshCore's SF-dependent preamble length:
// 32 symbols for SF≤8, 16 otherwise. Mirrors firmware
// preambleLengthForSF (RadioLibWrappers.h:47, PR #1954).
func PreambleForSF(sf int) int {
	if sf <= 8 {
		return 32
	}
	return 16
}

// TimeOnAir returns the LoRa time-on-air for a payload of payloadBytes
// transmitted with the given preset.
//
// Closed form (Semtech AN1200.13 / SX126x §6.1.4) with MeshCore
// constants CRC=1, IH=0:
//
//	T_sym       = 2^SF / BW_Hz
//	DE          = 1 if T_sym ≥ 16 ms else 0
//	n_payload   = 8 + max(ceil((8·PL − 4·SF + 28 + 16·CRC − 20·IH) /
//	                           (4·(SF − 2·DE))) · coding_coeff, 0)
//
// coding_coeff = preset.CR directly (we encode the denominator 5..8
// so coefficient = denominator; Semtech notation uses CR ∈ 1..4 with
// coefficient = CR+4, which is the same arithmetic).
//
//	n_preamble  = preamble + 4.25
//	ToA         = (n_preamble + n_payload) · T_sym
//
// Invalid presets (SF outside 6..12, BW≤0, CR outside 5..8, negative
// payload) return 0 so callers can guard cheaply on a zero result.
func TimeOnAir(payloadBytes int, preset Preset) time.Duration {
	if payloadBytes < 0 {
		return 0
	}
	if preset.SF < 6 || preset.SF > 12 {
		return 0
	}
	if preset.BWkHz <= 0 {
		return 0
	}
	if preset.CR < 5 || preset.CR > 8 {
		return 0
	}
	preamble := preset.Preamble
	if preamble <= 0 {
		preamble = 8
	}

	bwHz := preset.BWkHz * 1000.0
	tSym := math.Exp2(float64(preset.SF)) / bwHz // seconds per symbol
	de := 0
	if tSym*1000.0 >= 16.0 {
		de = 1
	}

	// CRC=1, IH=0 → constant +28 + 16*1 − 20*0 = +44.
	num := 8*payloadBytes - 4*preset.SF + 28 + 16
	den := 4 * (preset.SF - 2*de)
	if den <= 0 {
		return 0
	}
	var symbolsPayload float64
	if num <= 0 {
		// AN1200.13 §4.1.1.6: the inner ceil() argument can go negative
		// for very small payloads (the −4·SF + 28 + 16·CRC terms dominate
		// 8·PL). The reference formula clamps that ceil() to ≥ 0, so
		// symbolsPayload collapses to the fixed 8-symbol header term
		// when num ≤ 0. This is a max(., 0) clamp on the payload-symbol
		// count, NOT a separate header-floor rule.
		symbolsPayload = 8
	} else {
		ceilTerm := math.Ceil(float64(num) / float64(den))
		symbolsPayload = 8 + ceilTerm*float64(preset.CR)
	}

	preambleSymbols := float64(preamble) + 4.25
	totalSymbols := preambleSymbols + symbolsPayload
	secs := totalSymbols * tSym
	return time.Duration(secs * float64(time.Second))
}
