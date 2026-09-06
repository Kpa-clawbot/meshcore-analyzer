package lora

import (
	"math"
	"testing"
	"time"
)

// Reference values cross-checked against the closed-form computation
// shown in issue #1768 and against RadioLib calculateTimeOnAir():
//
//	freq=869.6 MHz, BW=62.5 kHz, SF=8, CR=4/5, preamble=32 (SF<=8)
//	T_sym = 256/62500 = 4.096 ms
//	preamble_symbols = 32 + 4.25 = 36.25
//
//	PL=8:  num = 64 - 32 + 28 + 16 = 76; den = 4*(8 - 0) = 32 (DE=0 since T_sym<16ms)
//	       ceil(76/32) = 3 → symbols_payload = 8 + 3*5 = 23
//	       total = (36.25 + 23) * 4.096 = 242.688 ms
//	PL=16: total = 283.648 ms
//	PL=32: total = 365.568 ms
//	PL=64: total = 529.408 ms
//
// These match the table in the issue #1768 body to the microsecond.
func TestTimeOnAir_DefaultEUPreset(t *testing.T) {
	preset := Preset{
		FreqHz:   869.6e6,
		BWkHz:    62.5,
		SF:       8,
		CR:       5,
		Preamble: PreambleForSF(8), // 32
	}

	cases := []struct {
		pl     int
		wantMs float64
	}{
		{8, 242.688},
		{16, 283.648},
		{32, 365.568},
		{64, 529.408},
	}
	for _, c := range cases {
		got := TimeOnAir(c.pl, preset)
		gotMs := float64(got) / float64(time.Millisecond)
		if math.Abs(gotMs-c.wantMs) > 0.01 {
			t.Errorf("TimeOnAir(%d B) = %.3f ms, want %.3f ms", c.pl, gotMs, c.wantMs)
		}
	}
}

// SF11/BW125 → T_sym = 2048/125000 = 16.384 ms ≥ 16ms → DE = 1; preamble = 16.
// PL=8:
//
//	num = 8*8 - 4*11 + 28 + 16 = 64 - 44 + 44 = 64
//	den = 4*(11 - 2*1) = 36; ceil(64/36) = 2
//	symbols_payload = 8 + 2*5 = 18
//	preamble_symbols = 16 + 4.25 = 20.25
//	total = (20.25 + 18) * 16.384 = 38.25 * 16.384 ≈ 626.688 ms
func TestTimeOnAir_DERangeSF11(t *testing.T) {
	preset := Preset{
		BWkHz:    125,
		SF:       11,
		CR:       5,
		Preamble: PreambleForSF(11),
	}
	got := TimeOnAir(8, preset)
	wantMs := 626.688
	gotMs := float64(got) / float64(time.Millisecond)
	if math.Abs(gotMs-wantMs) > 0.05 {
		t.Errorf("TimeOnAir(SF11/BW125, PL=8) = %.3f ms, want ~%.3f ms", gotMs, wantMs)
	}
}

func TestTimeOnAir_InvalidPresetReturnsZero(t *testing.T) {
	cases := []Preset{
		{BWkHz: 125, SF: 5, CR: 5},  // SF too low
		{BWkHz: 125, SF: 13, CR: 5}, // SF too high
		{BWkHz: 0, SF: 8, CR: 5},    // BW zero
		{BWkHz: 125, SF: 8, CR: 4},  // CR too low
		{BWkHz: 125, SF: 8, CR: 9},  // CR too high
	}
	for _, p := range cases {
		if got := TimeOnAir(16, p); got != 0 {
			t.Errorf("TimeOnAir(invalid preset %+v) = %v, want 0", p, got)
		}
	}
}

func TestPreambleForSF(t *testing.T) {
	if PreambleForSF(7) != 32 || PreambleForSF(8) != 32 {
		t.Errorf("PreambleForSF(<=8) must be 32 (firmware preambleLengthForSF)")
	}
	if PreambleForSF(9) != 16 || PreambleForSF(12) != 16 {
		t.Errorf("PreambleForSF(>8) must be 16")
	}
}
