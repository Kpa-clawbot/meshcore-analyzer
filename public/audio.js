// Mesh Audio Sonification — public/audio.js
// Turns raw packet bytes into generative music per AUDIO-PLAN.md

(function () {
  'use strict';

  // === State ===
  let audioEnabled = false;
  let audioCtx = null;
  let masterGain = null;
  let bpm = 120; // default BPM
  let activeVoices = 0;
  const MAX_VOICES = 12;

  // === Scales (MIDI note offsets from root) ===
  // Pentatonic / modal scales across 2-3 octaves
  const SCALES = {
    // C major pentatonic: C D E G A (repeated across octaves)
    ADVERT: buildScale([0, 2, 4, 7, 9], 48), // root C3
    // A minor pentatonic: A C D E G
    GRP_TXT: buildScale([0, 3, 5, 7, 10], 45), // root A2
    // E natural minor: E F# G A B C D
    TXT_MSG: buildScale([0, 2, 3, 5, 7, 8, 10], 40), // root E2
    // D whole tone: D E F# G# A# C
    TRACE: buildScale([0, 2, 4, 6, 8, 10], 50), // root D3
  };

  // Fallback scale for unknown types
  const DEFAULT_SCALE = SCALES.ADVERT;

  // === Synth configs per type ===
  const SYNTH_CONFIGS = {
    ADVERT: { type: 'triangle', attack: 0.02, decay: 0.3, sustain: 0.4, release: 0.5 },   // bell/pad
    GRP_TXT: { type: 'sine', attack: 0.005, decay: 0.15, sustain: 0.1, release: 0.2 },     // marimba/pluck
    TXT_MSG: { type: 'triangle', attack: 0.01, decay: 0.2, sustain: 0.3, release: 0.4 },   // piano-like
    TRACE: { type: 'sine', attack: 0.05, decay: 0.4, sustain: 0.5, release: 0.8 },          // ethereal
  };

  const DEFAULT_SYNTH = SYNTH_CONFIGS.ADVERT;

  // === Helpers ===

  function buildScale(intervals, rootMidi) {
    // Build scale across 3 octaves
    const notes = [];
    for (let oct = 0; oct < 3; oct++) {
      for (const interval of intervals) {
        notes.push(rootMidi + oct * 12 + interval);
      }
    }
    return notes;
  }

  function midiToFreq(midi) {
    return 440 * Math.pow(2, (midi - 69) / 12);
  }

  function mapRange(value, inMin, inMax, outMin, outMax) {
    return outMin + ((value - inMin) / (inMax - inMin)) * (outMax - outMin);
  }

  function quantizeToScale(byteVal, scale) {
    // Map 0-255 to scale index
    const idx = Math.floor((byteVal / 256) * scale.length);
    return scale[Math.min(idx, scale.length - 1)];
  }

  function tempoMultiplier() {
    // 120 BPM = 1.0x, higher = faster (shorter durations)
    return 120 / bpm;
  }

  // === Core: Initialize audio context ===

  function initAudio() {
    if (audioCtx) return;
    audioCtx = new (window.AudioContext || window.webkitAudioContext)();
    masterGain = audioCtx.createGain();
    masterGain.gain.value = 0.3;
    masterGain.connect(audioCtx.destination);
  }

  // === Core: Sonify a single packet ===

  function sonifyPacket(pkt) {
    if (!audioEnabled || !audioCtx) return;
    if (activeVoices >= MAX_VOICES) return; // voice stealing: just drop

    const rawHex = pkt.raw || pkt.raw_hex || (pkt.packet && pkt.packet.raw_hex) || '';
    if (!rawHex || rawHex.length < 6) return; // need at least 3 bytes

    // Parse raw hex to byte array
    const allBytes = [];
    for (let i = 0; i < rawHex.length; i += 2) {
      const b = parseInt(rawHex.slice(i, i + 2), 16);
      if (!isNaN(b)) allBytes.push(b);
    }
    if (allBytes.length < 3) return;

    // Header = first 3 bytes (configure voice), payload = rest
    const payloadBytes = allBytes.slice(3);
    if (payloadBytes.length === 0) return;

    // Extract musical parameters from pkt
    const decoded = pkt.decoded || {};
    const header = decoded.header || {};
    const typeName = header.payloadTypeName || 'UNKNOWN';
    const hops = decoded.path?.hops || [];
    const hopCount = Math.max(1, hops.length);
    const obsCount = pkt.observation_count || (pkt.packet && pkt.packet.observation_count) || 1;

    // Select scale and synth config
    const scale = SCALES[typeName] || DEFAULT_SCALE;
    const synthConfig = SYNTH_CONFIGS[typeName] || DEFAULT_SYNTH;

    // Sample sqrt(payload_length) bytes evenly across payload
    const noteCount = Math.max(2, Math.min(10, Math.ceil(Math.sqrt(payloadBytes.length))));
    const sampledBytes = [];
    for (let i = 0; i < noteCount; i++) {
      const idx = Math.floor((i / noteCount) * payloadBytes.length);
      sampledBytes.push(payloadBytes[idx]);
    }

    // Compute pan from origin longitude if available
    const payload = decoded.payload || {};
    let panValue = 0; // center default
    if (payload.lat !== undefined && payload.lon !== undefined) {
      // Map typical mesh longitude range (-125 to -65 for US) to -1..1
      panValue = mapRange(payload.lon, -125, -65, -1, 1);
      panValue = Math.max(-1, Math.min(1, panValue));
    } else if (hops.length > 0) {
      // Try first hop's position if available (node markers have lat/lon)
      // Fall back to slight random pan for spatial interest
      panValue = (Math.random() - 0.5) * 0.6;
    }

    // Filter cutoff from hop count: few hops = bright (8000Hz), many = muffled (800Hz)
    const filterFreq = mapRange(Math.min(hopCount, 10), 1, 10, 8000, 800);

    // Volume from observation count: 1 obs = base, more = louder (capped)
    const baseVolume = 0.15;
    const volume = Math.min(0.5, baseVolume + (obsCount - 1) * 0.03);

    // Detune cents for chord voicing (observation > 1)
    const voiceCount = Math.min(obsCount, 4); // max 4 stacked voices

    // Schedule the note sequence
    const tm = tempoMultiplier();
    let timeOffset = audioCtx.currentTime + 0.01; // tiny offset to avoid clicks

    activeVoices++;

    // Create shared filter
    const filter = audioCtx.createBiquadFilter();
    filter.type = 'lowpass';
    filter.frequency.value = filterFreq;
    filter.Q.value = 1;

    // Create panner
    const panner = audioCtx.createStereoPanner();
    panner.pan.value = panValue;

    // Chain: voices → filter → panner → master
    filter.connect(panner);
    panner.connect(masterGain);

    let lastNoteEnd = timeOffset;

    for (let i = 0; i < sampledBytes.length; i++) {
      const byte = sampledBytes[i];
      const midiNote = quantizeToScale(byte, scale);
      const freq = midiToFreq(midiNote);

      // Duration from byte value: low = staccato (50ms), high = sustained (400ms)
      const duration = mapRange(byte, 0, 255, 0.05, 0.4) * tm;

      // Spacing from delta to next byte
      let gap = 0.05 * tm; // minimum gap
      if (i < sampledBytes.length - 1) {
        const delta = Math.abs(sampledBytes[i + 1] - byte);
        gap = mapRange(delta, 0, 255, 0.03, 0.3) * tm;
      }

      const noteStart = timeOffset;
      const noteEnd = noteStart + duration;

      // Play note (with optional chord voicing)
      for (let v = 0; v < voiceCount; v++) {
        const detune = v === 0 ? 0 : (v % 2 === 0 ? 1 : -1) * (v * 7); // ±7, ±14 cents

        const osc = audioCtx.createOscillator();
        const envGain = audioCtx.createGain();

        osc.type = synthConfig.type;
        osc.frequency.value = freq;
        osc.detune.value = detune;

        // ADSR envelope
        const a = synthConfig.attack;
        const d = synthConfig.decay;
        const s = synthConfig.sustain;
        const r = synthConfig.release;
        const voiceVol = volume / voiceCount; // split volume across voices

        envGain.gain.setValueAtTime(0, noteStart);
        envGain.gain.linearRampToValueAtTime(voiceVol, noteStart + a);
        envGain.gain.linearRampToValueAtTime(voiceVol * s, noteStart + a + d);
        envGain.gain.setValueAtTime(voiceVol * s, noteEnd);
        envGain.gain.linearRampToValueAtTime(0.001, noteEnd + r);

        osc.connect(envGain);
        envGain.connect(filter);

        osc.start(noteStart);
        osc.stop(noteEnd + r + 0.01);

        // Cleanup
        osc.onended = () => {
          osc.disconnect();
          envGain.disconnect();
        };
      }

      timeOffset = noteEnd + gap;
      lastNoteEnd = noteEnd + (synthConfig.release || 0.2);
    }

    // Release voice slot after all notes finish
    const totalDuration = (lastNoteEnd - audioCtx.currentTime + 0.5) * 1000;
    setTimeout(() => {
      activeVoices = Math.max(0, activeVoices - 1);
      try {
        filter.disconnect();
        panner.disconnect();
      } catch (e) {}
    }, totalDuration);
  }

  // === Public API ===

  function setEnabled(on) {
    audioEnabled = on;
    if (on) initAudio();
    localStorage.setItem('live-audio-enabled', on);
  }

  function isEnabled() {
    return audioEnabled;
  }

  function setBPM(val) {
    bpm = Math.max(40, Math.min(300, val));
    localStorage.setItem('live-audio-bpm', bpm);
  }

  function getBPM() {
    return bpm;
  }

  function setVolume(val) {
    if (masterGain) masterGain.gain.value = Math.max(0, Math.min(1, val));
    localStorage.setItem('live-audio-volume', val);
  }

  function getVolume() {
    return masterGain ? masterGain.gain.value : 0.3;
  }

  // Restore from localStorage
  function restore() {
    const saved = localStorage.getItem('live-audio-enabled');
    if (saved === 'true') audioEnabled = true;
    const savedBpm = localStorage.getItem('live-audio-bpm');
    if (savedBpm) bpm = parseInt(savedBpm, 10) || 120;
    const savedVol = localStorage.getItem('live-audio-volume');
    if (savedVol) {
      initAudio();
      if (masterGain) masterGain.gain.value = parseFloat(savedVol) || 0.3;
    }
  }

  // Export
  window.MeshAudio = {
    sonifyPacket,
    setEnabled,
    isEnabled,
    setBPM,
    getBPM,
    setVolume,
    getVolume,
    restore,
  };
})();
