// Voice v1: "Constellation" — melodic packet sonification
// Original voice: type-based instruments, scale-quantized melody from payload bytes,
// byte-driven note duration and spacing, hop-based filter, observation chord voicing.

(function () {
  'use strict';

  const { buildScale, midiToFreq, mapRange, quantizeToScale } = MeshAudio.helpers;

  // Scales per payload type
  const SCALES = {
    ADVERT: buildScale([0, 2, 4, 7, 9], 48),       // C major pentatonic
    GRP_TXT: buildScale([0, 3, 5, 7, 10], 45),      // A minor pentatonic
    TXT_MSG: buildScale([0, 2, 3, 5, 7, 8, 10], 40),// E natural minor
    TRACE: buildScale([0, 2, 4, 6, 8, 10], 50),      // D whole tone
  };
  const DEFAULT_SCALE = SCALES.ADVERT;

  // Synth ADSR envelopes per type
  const SYNTHS = {
    ADVERT: { type: 'triangle', attack: 0.02, decay: 0.3, sustain: 0.4, release: 0.5 },
    GRP_TXT: { type: 'sine', attack: 0.005, decay: 0.15, sustain: 0.1, release: 0.2 },
    TXT_MSG: { type: 'triangle', attack: 0.01, decay: 0.2, sustain: 0.3, release: 0.4 },
    TRACE: { type: 'sine', attack: 0.05, decay: 0.4, sustain: 0.5, release: 0.8 },
  };
  const DEFAULT_SYNTH = SYNTHS.ADVERT;

  function play(audioCtx, masterGain, parsed, opts) {
    const { payloadBytes, typeName, hopCount, obsCount, payload, hops } = parsed;
    const tm = opts.tempoMultiplier;

    const scale = SCALES[typeName] || DEFAULT_SCALE;
    const synthConfig = SYNTHS[typeName] || DEFAULT_SYNTH;

    // Sample sqrt(len) bytes evenly
    const noteCount = Math.max(2, Math.min(10, Math.ceil(Math.sqrt(payloadBytes.length))));
    const sampledBytes = [];
    for (let i = 0; i < noteCount; i++) {
      const idx = Math.floor((i / noteCount) * payloadBytes.length);
      sampledBytes.push(payloadBytes[idx]);
    }

    // Pan from longitude
    let panValue = 0;
    if (payload.lat !== undefined && payload.lon !== undefined) {
      panValue = Math.max(-1, Math.min(1, mapRange(payload.lon, -125, -65, -1, 1)));
    } else if (hops.length > 0) {
      panValue = (Math.random() - 0.5) * 0.6;
    }

    // Filter from hops
    const filterFreq = mapRange(Math.min(hopCount, 10), 1, 10, 8000, 800);

    // Volume from observations
    const volume = Math.min(0.5, 0.15 + (obsCount - 1) * 0.03);
    const voiceCount = Math.min(obsCount, 4);

    // Audio nodes
    const filter = audioCtx.createBiquadFilter();
    filter.type = 'lowpass';
    filter.frequency.value = filterFreq;
    filter.Q.value = 1;

    const panner = audioCtx.createStereoPanner();
    panner.pan.value = panValue;

    filter.connect(panner);
    panner.connect(masterGain);

    let timeOffset = audioCtx.currentTime + 0.01;
    let lastNoteEnd = timeOffset;

    for (let i = 0; i < sampledBytes.length; i++) {
      const byte = sampledBytes[i];
      const freq = midiToFreq(quantizeToScale(byte, scale));
      const duration = mapRange(byte, 0, 255, 0.05, 0.4) * tm;

      let gap = 0.05 * tm;
      if (i < sampledBytes.length - 1) {
        const delta = Math.abs(sampledBytes[i + 1] - byte);
        gap = mapRange(delta, 0, 255, 0.03, 0.3) * tm;
      }

      const noteStart = timeOffset;
      const noteEnd = noteStart + duration;

      for (let v = 0; v < voiceCount; v++) {
        const detune = v === 0 ? 0 : (v % 2 === 0 ? 1 : -1) * (v * 7);
        const osc = audioCtx.createOscillator();
        const envGain = audioCtx.createGain();

        osc.type = synthConfig.type;
        osc.frequency.value = freq;
        osc.detune.value = detune;

        const { attack: a, decay: d, sustain: s, release: r } = synthConfig;
        const voiceVol = volume / voiceCount;

        envGain.gain.setValueAtTime(0, noteStart);
        envGain.gain.linearRampToValueAtTime(voiceVol, noteStart + a);
        envGain.gain.exponentialRampToValueAtTime(Math.max(voiceVol * s, 0.0001), noteStart + a + d);
        envGain.gain.setValueAtTime(Math.max(voiceVol * s, 0.0001), noteEnd);
        envGain.gain.exponentialRampToValueAtTime(0.0001, noteEnd + r);

        osc.connect(envGain);
        envGain.connect(filter);
        osc.start(noteStart);
        osc.stop(noteEnd + r + 0.05);
        osc.onended = () => { osc.disconnect(); envGain.disconnect(); };
      }

      timeOffset = noteEnd + gap;
      lastNoteEnd = noteEnd + (synthConfig.release || 0.2);
    }

    // Cleanup shared nodes
    const cleanupMs = (lastNoteEnd - audioCtx.currentTime + 0.5) * 1000;
    setTimeout(() => {
      try { filter.disconnect(); panner.disconnect(); } catch (e) {}
    }, cleanupMs);

    return lastNoteEnd - audioCtx.currentTime;
  }

  MeshAudio.registerVoice('constellation', { name: 'constellation', play });
})();
