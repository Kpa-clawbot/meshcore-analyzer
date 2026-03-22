# Mesh Audio — Sonification Plan

*Turn raw packet bytes into generative music.*

## What Every Packet Has (guaranteed)
- `raw_hex` — melody source
- `hop_count` — note duration + filter cutoff
- `observation_count` — volume + chord voicing
- `payload_type` — instrument + scale + root key
- `node_lat/lon` — stereo pan
- `timestamp` — arrival timing

## Final Mapping

| Data | Musical Role |
|------|-------------|
| **payload_type** | Instrument + scale + root key |
| **payload bytes** (evenly sampled, sqrt(len) count) | Melody notes (pitch) |
| **byte value** | Note length (higher = longer sustain, lower = staccato) |
| **byte-to-byte delta** | Note spacing (big jump = longer gap, small = rapid) |
| **hop_count** | Low-pass filter cutoff (more hops = more muffled) |
| **observation_count** | Volume + chord voicing (more observers = louder + stacked detuned voices) |
| **node longitude** | Stereo pan (west = left, east = right) |
| **BPM tempo** (user control) | Master time multiplier on all durations |

## Instruments & Scales by Type

| Type | Instrument | Scale | Root |
|------|-----------|-------|------|
| ADVERT | Bell / pad | C major pentatonic | C |
| GRP_TXT | Marimba / pluck | A minor pentatonic | A |
| TXT_MSG | Piano | E natural minor | E |
| TRACE | Ethereal synth | D whole tone | D |

## How a Packet Plays

1. **Header configures the voice** — payload type selects instrument, scale, root key. Flags/transport codes select envelope shape. Header bytes are NOT played as notes.
2. **Sample payload bytes** — pick `sqrt(payload_length)` bytes, evenly spaced across payload:
   - 16-byte payload → 4 notes
   - 36-byte payload → 6 notes
   - 64-byte payload → 8 notes
3. **Each sampled byte → a note:**
   - **Pitch**: byte value (0-255) quantized to selected scale across 2-3 octaves
   - **Length**: byte value maps to sustain duration (low byte = short staccato ~50ms, high byte = sustained ~400ms)
   - **Spacing**: delta between current and next sampled byte determines gap to next note (small delta = rapid fire, large delta = pause). Scaled by BPM tempo multiplier.
4. **Filter**: low-pass cutoff from hop_count — few hops = bright/clear, many hops = muffled (signal traveled far)
5. **Volume**: observation_count — more observers = louder
6. **Chord voicing**: if observations > 1, stack slightly detuned voices (±5-15 cents per voice, chorus effect)
7. **Pan**: origin node longitude mapped to stereo field
8. **All timings scaled by BPM tempo control**

## UI Controls

- **Audio toggle** — on/off (next to Matrix / Rain)
- **BPM tempo slider** — master time multiplier (slow = ambient, fast = techno)
- **Volume slider** — master gain
- **Mute button** — pause audio without losing toggle state

## Implementation

### Library: Tone.js (~150KB)
- `Tone.Synth` / `Tone.PolySynth` for melody + chords
- `Tone.Sampler` for realistic instruments
- `Tone.Filter` for hop-based cutoff
- `Tone.Chorus` for observation detuning
- `Tone.Panner` for geographic stereo
- `Tone.Reverb` for spatial depth

### Integration
- `animatePacket(pkt)` also calls `sonifyPacket(pkt)`
- Optional "Sonify" button on packet detail page
- Web Audio runs on separate thread — won't block UI/animations
- Polyphony capped at 8-12 voices to prevent mudding
- Voice stealing when busy

### Core Function
```
sonifyPacket(pkt):
  1. Extract raw_hex → byte array
  2. Separate header (first ~3 bytes) from payload
  3. Header → select instrument, scale, root key, envelope
  4. Sample sqrt(payload.length) bytes evenly across payload
  5. For each sampled byte:
     - pitch = quantize(byte, scale, rootKey)
     - duration = map(byte, 50ms, 400ms) × tempoMultiplier
     - gap to next = map(abs(nextByte - byte), 30ms, 300ms) × tempoMultiplier
  6. Set filter cutoff from hop_count
  7. Set gain from observation_count
  8. Set pan from origin longitude
  9. If observation_count > 1: detune +/- cents per voice
  10. Schedule note sequence via Tone.js
```

## The Full Experience

Matrix mode + Rain + Audio: green hex bytes flow across the map, columns of raw data rain down, and each packet plays its own unique melody derived from its actual bytes. Quiet periods are sparse atmospheric ambience; traffic bursts become dense polyrhythmic cascades. Crank the BPM for techno, slow it down for ambient.

## Future Ideas

- "Record" button → export MIDI or WAV
- Per-type mute toggles (silence ADVERTs, only hear messages)
- "DJ mode" — crossfade between regions
- Historical playback at accelerated speed = mesh network symphony
- Presets (ambient, techno, classical, minimal)
- ADVERT ambient drone layer (single modulated oscillator, not per-packet)
