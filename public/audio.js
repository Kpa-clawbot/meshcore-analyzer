// Mesh Audio Engine — public/audio.js
// Core audio infrastructure + swappable voice modules
// Each voice module is a separate file (audio-v1.js, audio-v2.js, etc.)

(function () {
  'use strict';

  // === Engine State ===
  let audioEnabled = false;
  let audioCtx = null;
  let masterGain = null;
  let bpm = 120;
  let activeVoices = 0;
  const MAX_VOICES = 12;
  let currentVoice = null; // active voice module

  // === Shared Helpers (available to voice modules) ===

  function buildScale(intervals, rootMidi) {
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
    const idx = Math.floor((byteVal / 256) * scale.length);
    return scale[Math.min(idx, scale.length - 1)];
  }

  function tempoMultiplier() {
    return 120 / bpm;
  }

  function parsePacketBytes(pkt) {
    const rawHex = pkt.raw || pkt.raw_hex || (pkt.packet && pkt.packet.raw_hex) || '';
    if (!rawHex || rawHex.length < 6) return null;
    const allBytes = [];
    for (let i = 0; i < rawHex.length; i += 2) {
      const b = parseInt(rawHex.slice(i, i + 2), 16);
      if (!isNaN(b)) allBytes.push(b);
    }
    if (allBytes.length < 3) return null;

    const decoded = pkt.decoded || {};
    const header = decoded.header || {};
    const payload = decoded.payload || {};
    const hops = decoded.path?.hops || [];

    return {
      allBytes,
      headerBytes: allBytes.slice(0, 3),
      payloadBytes: allBytes.slice(3),
      typeName: header.payloadTypeName || 'UNKNOWN',
      hopCount: Math.max(1, hops.length),
      obsCount: pkt.observation_count || (pkt.packet && pkt.packet.observation_count) || 1,
      payload,
      hops,
    };
  }

  // === Engine: Init ===

  function initAudio() {
    if (audioCtx) {
      if (audioCtx.state === 'suspended') audioCtx.resume();
      return;
    }
    audioCtx = new (window.AudioContext || window.webkitAudioContext)();
    masterGain = audioCtx.createGain();
    masterGain.gain.value = 0.3;
    masterGain.connect(audioCtx.destination);
  }

  // === Engine: Sonify ===

  function sonifyPacket(pkt) {
    if (!audioEnabled || !audioCtx || !currentVoice) return;
    if (audioCtx.state === 'suspended') audioCtx.resume();
    if (activeVoices >= MAX_VOICES) return;

    const parsed = parsePacketBytes(pkt);
    if (!parsed || parsed.payloadBytes.length === 0) return;

    activeVoices++;

    try {
      const duration = currentVoice.play(audioCtx, masterGain, parsed, {
        bpm, tempoMultiplier: tempoMultiplier(),
      });

      // Release voice slot after estimated duration
      const releaseMs = (duration || 3) * 1000 + 500;
      setTimeout(() => { activeVoices = Math.max(0, activeVoices - 1); }, releaseMs);
    } catch (e) {
      activeVoices = Math.max(0, activeVoices - 1);
      console.error('[audio] voice error:', e);
    }
  }

  // === Voice Registration ===

  function registerVoice(name, voiceModule) {
    // voiceModule must have: { name, play(audioCtx, masterGain, parsed, opts) → durationSec }
    if (!window._meshAudioVoices) window._meshAudioVoices = {};
    window._meshAudioVoices[name] = voiceModule;
    // Auto-select first registered voice if none active
    if (!currentVoice) currentVoice = voiceModule;
  }

  function setVoice(name) {
    if (window._meshAudioVoices && window._meshAudioVoices[name]) {
      currentVoice = window._meshAudioVoices[name];
      localStorage.setItem('live-audio-voice', name);
      return true;
    }
    return false;
  }

  function getVoiceName() {
    return currentVoice ? currentVoice.name : null;
  }

  function getVoiceNames() {
    return Object.keys(window._meshAudioVoices || {});
  }

  // === Public API ===

  function setEnabled(on) {
    audioEnabled = on;
    if (on) initAudio();
    localStorage.setItem('live-audio-enabled', on);
  }

  function isEnabled() { return audioEnabled; }

  function setBPM(val) {
    bpm = Math.max(40, Math.min(300, val));
    localStorage.setItem('live-audio-bpm', bpm);
  }

  function getBPM() { return bpm; }

  function setVolume(val) {
    if (masterGain) masterGain.gain.value = Math.max(0, Math.min(1, val));
    localStorage.setItem('live-audio-volume', val);
  }

  function getVolume() { return masterGain ? masterGain.gain.value : 0.3; }

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
    const savedVoice = localStorage.getItem('live-audio-voice');
    if (savedVoice) setVoice(savedVoice);
  }

  // Export engine + helpers for voice modules
  window.MeshAudio = {
    sonifyPacket,
    setEnabled, isEnabled,
    setBPM, getBPM,
    setVolume, getVolume,
    registerVoice, setVoice, getVoiceName, getVoiceNames,
    restore,
    // Helpers for voice modules
    helpers: { buildScale, midiToFreq, mapRange, quantizeToScale },
  };
})();
