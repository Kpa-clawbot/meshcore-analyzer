# Mesh Audio — Sonification Plan

*Turn raw packet bytes into generative music.*

## Available Data Per Packet

| Field | Range | Musical Mapping |
|-------|-------|----------------|
| `raw_hex` | 20-60+ bytes | Melody — each byte = a note |
| `payload_type` | ADVERT, GRP_TXT, TXT_MSG, TRACE | Instrument/scale selection |
| `hop_count` | 1-12+ | Note duration |
| `path` | node sequence | Arpeggio pattern |
| `SNR` | dB value | Volume/velocity |
| `RSSI` | dBm value | Filter cutoff (brightness) |
| `hash_size` | 1-4 bytes | — |
| `observation_count` | 1-40+ | Chord voicing / polyphony |
| `channel_hash` | 0-255 | Root bass note / key |
| `arrival_rate` | packets/min | Tempo (BPM) |
| `node_lat/lon` | coordinates | Stereo pan |

## Mapping Design

### Melody from Raw Bytes
- Each byte (0-255) quantized to a musical scale across 2-3 octaves
- NOT chromatic (too chaotic) — use pentatonic or modal scales
- First N bytes of the packet = a short melodic phrase
- Different payload types get different scales:
  - **ADVERT** → major pentatonic (bright, announcing, beacon-like)
  - **GRP_TXT** → minor pentatonic (conversational, human)
  - **TXT_MSG** → natural minor (direct, intentional)
  - **TRACE** → whole tone (mysterious, searching, probing)

### Rhythm from Network Activity
- Packet arrival rate sets effective BPM
  - Busy network = fast tempo, quiet = ambient drone
- Hop count = note duration:
  - 1 hop = sixteenth note (short staccato)
  - 3 hops = quarter note
  - 8+ hops = whole note (sustained, traveled far)

### Timbre from Payload Type
- **ADVERT** = soft pad or bell (constant background beacons)
- **GRP_TXT** = plucked string or marimba (conversation, human-initiated)
- **TXT_MSG** = piano (direct, personal)
- **TRACE** = reversed cymbal or ethereal whoosh (probing)

### Spatial from Geography
- Node longitude → stereo pan (west = left, east = right)
- Or: packet origin's X position on current map viewport = pan
- Distance between hops → reverb amount (long hops = more reverb, signal traveled far)

### Dynamics from Signal Quality
- **SNR → velocity/volume**: strong signal = loud, weak = quiet whisper
- **RSSI → filter cutoff**: strong = bright and clear, weak = muffled/low-pass filtered
- Low SNR could add subtle bitcrushing/distortion (degraded signal = degraded sound)

### Harmony from Observations
- 1 observation = single note
- Multiple observers = chord voicing (each adds a voice)
- Each observer slightly detuned (chorus effect) — more observers = richer, more "present"
- Alternative: observation count triggers arpeggiation speed

### Bass from Channel Hash
- Channel hash (0-255) sets root bass note
- Different channels naturally play in different keys
- Creates harmonic separation between channel traffic

### Ambient Layer from ADVERTs
- ADVERTs are constant background heartbeats → generative ambient drone
- Active node count modulates drone complexity (more nodes = richer harmonics)
- Could use ADVERT battery level to modulate brightness of the drone

## Implementation

### Library: Tone.js
- Built on Web Audio API, ~150KB
- `Tone.Synth` / `Tone.PolySynth` for melody + chords
- `Tone.Sampler` if we want realistic instrument sounds
- `Tone.Transport` for tempo-synced playback tied to packet rate
- `Tone.Reverb`, `Tone.Filter`, `Tone.Chorus` for signal-quality effects
- `Tone.Panner` for geographic stereo positioning

### Integration Points
- New **"Audio"** toggle on live map controls (next to Matrix / Rain)
- `animatePacket(pkt)` also calls `sonifyPacket(pkt)`
- Optional: **"Sonify"** button on packet detail page — hear one packet's melody
- Master volume slider in controls
- Mute button (separate from toggle — lets you pause audio without losing state)

### Core Function Sketch
```
sonifyPacket(pkt):
  1. Extract raw_hex → byte array
  2. Select scale based on payload_type
  3. Map first 8-16 bytes to notes in scale (quantized)
  4. Set note duration from hop_count
  5. Set velocity from SNR
  6. Set filter cutoff from RSSI  
  7. Set pan from origin longitude
  8. Set reverb from max hop distance
  9. If observation_count > 1:
     - Play as chord (stack observation_count voices)
     - Each voice slightly detuned (+/- cents)
  10. Trigger synth.triggerAttackRelease()
```

### Performance Considerations
- Web Audio runs on separate thread — won't block UI/animations
- Limit polyphony (max 8-12 simultaneous voices) to prevent audio mudding
- Use note pooling / voice stealing when busy
- ADVERT drone should be a single oscillator modulated, not per-packet

### The Full Experience
Matrix mode + Rain + Audio = watching green hex bytes flow across the map, columns of raw data raining down, and each packet plays its own melody from its actual bytes. Quiet periods are sparse atmospheric ambience; traffic bursts become dense polyrhythmic cascades.

### Future Ideas
- "Record" button → export as MIDI or WAV
- Packet type mute toggles (silence ADVERTs, only hear messages)
- "DJ mode" — crossfade between regions
- Historical playback at accelerated speed = mesh network symphony
- Different "presets" (ambient, techno, classical mapping)
