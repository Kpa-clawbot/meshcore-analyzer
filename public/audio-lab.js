/* === MeshCore Analyzer — audio-lab.js === */
/* Audio Lab: Packet Jukebox for sound debugging & understanding */
'use strict';

(function () {
  let styleEl = null;
  let loopTimer = null;
  let selectedPacket = null;
  let baseBPM = 120;
  let speedMult = 1;
  let highlightTimers = [];

  const TYPE_COLORS = window.TYPE_COLORS || {
    ADVERT: '#f59e0b', GRP_TXT: '#10b981', TXT_MSG: '#6366f1',
    TRACE: '#8b5cf6', REQ: '#ef4444', RESPONSE: '#3b82f6',
    ACK: '#6b7280', PATH: '#ec4899', ANON_REQ: '#f97316', UNKNOWN: '#6b7280'
  };

  const SCALE_NAMES = {
    ADVERT: 'C major pentatonic', GRP_TXT: 'A minor pentatonic',
    TXT_MSG: 'E natural minor', TRACE: 'D whole tone'
  };

  const SYNTH_TYPES = {
    ADVERT: 'triangle', GRP_TXT: 'sine', TXT_MSG: 'triangle', TRACE: 'sine'
  };

  const SCALE_INTERVALS = {
    ADVERT: { intervals: [0,2,4,7,9], root: 48 },
    GRP_TXT: { intervals: [0,3,5,7,10], root: 45 },
    TXT_MSG: { intervals: [0,2,3,5,7,8,10], root: 40 },
    TRACE: { intervals: [0,2,4,6,8,10], root: 50 },
  };

  function injectStyles() {
    if (styleEl) return;
    styleEl = document.createElement('style');
    styleEl.textContent = `
      .alab { display: flex; height: 100%; overflow: hidden; }
      .alab-sidebar { width: 280px; min-width: 200px; border-right: 1px solid var(--border);
        overflow-y: auto; padding: 12px; background: var(--surface-1); }
      .alab-main { flex: 1; overflow-y: auto; padding: 16px 24px; }
      .alab-type-hdr { font-weight: 700; font-size: 13px; padding: 6px 8px; margin-top: 8px;
        border-radius: 6px; cursor: pointer; display: flex; justify-content: space-between; align-items: center; }
      .alab-type-hdr:hover { opacity: 0.8; }
      .alab-type-list { padding: 0; }
      .alab-pkt { padding: 5px 8px 5px 16px; font-size: 12px; font-family: var(--mono);
        cursor: pointer; border-radius: 4px; color: var(--text-muted); }
      .alab-pkt:hover { background: var(--hover-bg); }
      .alab-pkt.selected { background: var(--selected-bg); color: var(--text); font-weight: 600; }
      .alab-controls { display: flex; flex-wrap: wrap; gap: 12px; align-items: center;
        padding: 12px 16px; background: var(--surface-1); border-radius: 8px; margin-bottom: 16px; border: 1px solid var(--border); }
      .alab-btn { padding: 6px 14px; border: 1px solid var(--border); border-radius: 6px;
        background: var(--surface-1); color: var(--text); cursor: pointer; font-size: 13px; }
      .alab-btn:hover { background: var(--hover-bg); }
      .alab-btn.active { background: var(--accent); color: #fff; border-color: var(--accent); }
      .alab-speed { padding: 4px 8px; font-size: 12px; border-radius: 4px; border: 1px solid var(--border);
        background: var(--surface-1); color: var(--text-muted); cursor: pointer; }
      .alab-speed.active { background: var(--accent); color: #fff; border-color: var(--accent); }
      .alab-section { background: var(--surface-1); border: 1px solid var(--border);
        border-radius: 8px; padding: 16px; margin-bottom: 16px; }
      .alab-section h3 { margin: 0 0 12px 0; font-size: 14px; color: var(--text-muted); font-weight: 600; }
      .alab-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 8px; }
      .alab-stat { font-size: 12px; }
      .alab-stat .label { color: var(--text-muted); }
      .alab-stat .value { font-weight: 600; font-family: var(--mono); }
      .alab-hex { font-family: var(--mono); font-size: 11px; word-break: break-all; line-height: 1.6;
        max-height: 80px; overflow: hidden; transition: max-height 0.3s; }
      .alab-hex.expanded { max-height: none; }
      .alab-hex .sampled { background: var(--accent); color: #fff; border-radius: 2px; padding: 0 1px; }
      .alab-note-table { width: 100%; font-size: 12px; border-collapse: collapse; }
      .alab-note-table th { text-align: left; font-weight: 600; color: var(--text-muted);
        padding: 4px 8px; border-bottom: 1px solid var(--border); font-size: 11px; }
      .alab-note-table td { padding: 4px 8px; border-bottom: 1px solid var(--border); font-family: var(--mono); }
      .alab-byte-viz { display: flex; align-items: flex-end; height: 60px; gap: 1px; margin-top: 8px; }
      .alab-byte-bar { flex: 1; min-width: 2px; border-radius: 1px 1px 0 0; transition: box-shadow 0.1s; }
      .alab-byte-bar.playing { box-shadow: 0 0 8px 2px currentColor; transform: scaleY(1.15); }
      .alab-hex .playing { background: #ff6b6b !important; color: #fff !important; border-radius: 2px; padding: 0 2px; transition: background 0.1s; }
      .alab-note-table tr.playing { background: var(--accent) !important; color: #fff; }
      .alab-note-table tr.playing td { color: #fff; }
      .alab-map-table { width: 100%; font-size: 13px; border-collapse: collapse; }
      .alab-map-table td { padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: top; }
      .alab-map-table .map-param { font-weight: 600; white-space: nowrap; width: 110px; }
      .alab-map-table .map-value { font-family: var(--mono); font-weight: 700; white-space: nowrap; width: 120px; }
      .alab-map-table .map-why { font-size: 11px; color: var(--text-muted); font-family: var(--mono); }
      .map-why-inline { display: block; font-size: 10px; color: var(--text-muted); font-family: var(--mono); margin-top: 2px; }
      .alab-note-play { background: none; border: 1px solid var(--border); border-radius: 4px; cursor: pointer;
        font-size: 10px; padding: 2px 6px; color: var(--text-muted); }
      .alab-note-play:hover { background: var(--accent); color: #fff; border-color: var(--accent); }
      .alab-note-clickable { cursor: pointer; }
      .alab-note-clickable:hover { background: var(--hover-bg); }
      .alab-empty { text-align: center; padding: 60px 20px; color: var(--text-muted); font-size: 15px; }
      .alab-slider-group { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-muted); }
      .alab-slider-group input[type=range] { width: 80px; }
      .alab-slider-group select { font-size: 12px; padding: 2px 4px; background: var(--input-bg); color: var(--text); border: 1px solid var(--border); border-radius: 4px; }
      @media (max-width: 768px) {
        .alab { flex-direction: column; }
        .alab-sidebar { width: 100%; max-height: 200px; border-right: none; border-bottom: 1px solid var(--border); }
        .alab-main { padding: 12px; }
      }
    `;
    document.head.appendChild(styleEl);
  }

  function parseHex(hex) {
    const bytes = [];
    for (let i = 0; i < hex.length; i += 2) {
      const b = parseInt(hex.slice(i, i + 2), 16);
      if (!isNaN(b)) bytes.push(b);
    }
    return bytes;
  }

  function computeMapping(pkt) {
    const { buildScale, midiToFreq, mapRange, quantizeToScale } = MeshAudio.helpers;
    const rawHex = pkt.raw_hex || '';
    const allBytes = parseHex(rawHex);
    if (allBytes.length < 3) return null;

    const payloadBytes = allBytes.slice(3);
    let typeName = 'UNKNOWN';
    try { const d = JSON.parse(pkt.decoded_json || '{}'); typeName = d.type || 'UNKNOWN'; } catch {}

    const hops = [];
    try { const p = JSON.parse(pkt.path_json || '[]'); if (Array.isArray(p)) hops.push(...p); } catch {}
    const hopCount = Math.max(1, hops.length);
    const obsCount = pkt.observation_count || 1;

    const si = SCALE_INTERVALS[typeName] || SCALE_INTERVALS.ADVERT;
    const scale = buildScale(si.intervals, si.root);
    const scaleName = SCALE_NAMES[typeName] || 'C major pentatonic';
    const oscType = SYNTH_TYPES[typeName] || 'triangle';

    const noteCount = Math.max(2, Math.min(10, Math.ceil(Math.sqrt(payloadBytes.length))));
    const sampledIndices = [];
    const sampledBytes = [];
    for (let i = 0; i < noteCount; i++) {
      const idx = Math.floor((i / noteCount) * payloadBytes.length);
      sampledIndices.push(idx);
      sampledBytes.push(payloadBytes[idx]);
    }

    const filterHz = Math.round(mapRange(Math.min(hopCount, 10), 1, 10, 8000, 800));
    const volume = Math.min(0.6, 0.15 + (obsCount - 1) * 0.02);
    const voiceCount = Math.min(Math.max(1, Math.ceil(Math.log2(obsCount + 1))), 8);
    let panValue = 0;
    let panSource = 'no location data → center';
    try {
      const d = JSON.parse(pkt.decoded_json || '{}');
      if (d.lon != null) {
        panValue = Math.max(-1, Math.min(1, mapRange(d.lon, -125, -65, -1, 1)));
        panSource = `lon ${d.lon.toFixed(1)}° → map(-125...-65) → ${panValue.toFixed(2)}`;
      }
    } catch {}

    // Detune description
    const detuneDesc = [];
    for (let v = 0; v < voiceCount; v++) {
      const d = v === 0 ? 0 : (v % 2 === 0 ? 1 : -1) * (v * 5 + 3);
      detuneDesc.push((d >= 0 ? '+' : '') + d + '¢');
    }

    const bpm = MeshAudio.getBPM ? MeshAudio.getBPM() : 120;
    const tm = 60 / bpm; // BPM already includes speed multiplier

    const notes = sampledBytes.map((byte, i) => {
      const midi = quantizeToScale(byte, scale);
      const freq = midiToFreq(midi);
      const duration = mapRange(byte, 0, 255, 0.05, 0.4) * tm * 1000;
      let gap = 0.05 * tm * 1000;
      if (i < sampledBytes.length - 1) {
        const delta = Math.abs(sampledBytes[i + 1] - byte);
        gap = mapRange(delta, 0, 255, 0.03, 0.3) * tm * 1000;
      }
      return { index: sampledIndices[i], byte, midi, freq: Math.round(freq), duration: Math.round(duration), gap: Math.round(gap) };
    });

    return {
      typeName, allBytes, payloadBytes, sampledIndices, sampledBytes, notes,
      noteCount, filterHz, volume: volume.toFixed(3), voiceCount, panValue: panValue.toFixed(2),
      oscType, scaleName, hopCount, obsCount,
      totalSize: allBytes.length, payloadSize: payloadBytes.length,
      color: TYPE_COLORS[typeName] || TYPE_COLORS.UNKNOWN,
      panSource, detuneDesc,
    };
  }

  function renderDetail(pkt, app) {
    const m = computeMapping(pkt);
    if (!m) { document.getElementById('alabDetail').innerHTML = '<div class="alab-empty">No raw hex data for this packet</div>'; return; }

    // Hex dump with sampled bytes highlighted
    const sampledSet = new Set(m.sampledIndices);
    let hexHtml = '';
    for (let i = 0; i < m.payloadBytes.length; i++) {
      const h = m.payloadBytes[i].toString(16).padStart(2, '0').toUpperCase();
      if (sampledSet.has(i)) hexHtml += `<span class="sampled" id="hexByte${i}">${h}</span> `;
      else hexHtml += `<span id="hexByte${i}">${h}</span> `;
    }

    document.getElementById('alabDetail').innerHTML = `
      <div class="alab-section">
        <h3>📦 Packet Data</h3>
        <div class="alab-grid">
          <div class="alab-stat"><span class="label">Type</span><br><span class="value" style="color:${m.color}">${m.typeName}</span></div>
          <div class="alab-stat"><span class="label">Total Size</span><br><span class="value">${m.totalSize} bytes</span></div>
          <div class="alab-stat"><span class="label">Payload Size</span><br><span class="value">${m.payloadSize} bytes</span></div>
          <div class="alab-stat"><span class="label">Hops</span><br><span class="value">${m.hopCount}</span></div>
          <div class="alab-stat"><span class="label">Observations</span><br><span class="value">${m.obsCount}</span></div>
          <div class="alab-stat"><span class="label">Hash</span><br><span class="value">${pkt.hash || '—'}</span></div>
        </div>
        <div style="margin-top:10px">
          <div class="alab-hex" id="alabHex" onclick="this.classList.toggle('expanded')" title="Click to expand">${hexHtml}</div>
        </div>
      </div>

      <div class="alab-section">
        <h3>🎵 Sound Mapping</h3>
        <table class="alab-map-table">
          <tr>
            <td class="map-param">Instrument</td>
            <td class="map-value">${m.oscType}</td>
            <td class="map-why">payload_type = ${m.typeName} → ${m.oscType} oscillator</td>
          </tr>
          <tr>
            <td class="map-param">Scale</td>
            <td class="map-value">${m.scaleName}</td>
            <td class="map-why">payload_type = ${m.typeName} → ${m.scaleName} (root MIDI ${SCALE_INTERVALS[m.typeName]?.root || 48})</td>
          </tr>
          <tr>
            <td class="map-param">Notes</td>
            <td class="map-value">${m.noteCount}</td>
            <td class="map-why">⌈√${m.payloadSize}⌉ = ⌈${Math.sqrt(m.payloadSize).toFixed(1)}⌉ = ${m.noteCount} bytes sampled evenly across payload</td>
          </tr>
          <tr>
            <td class="map-param">Filter Cutoff</td>
            <td class="map-value">${m.filterHz} Hz</td>
            <td class="map-why">${m.hopCount} hops → map(1...10 → 8000...800 Hz) = ${m.filterHz} Hz lowpass — more hops = more muffled</td>
          </tr>
          <tr>
            <td class="map-param">Volume</td>
            <td class="map-value">${m.volume}</td>
            <td class="map-why">min(0.6, 0.15 + (${m.obsCount} obs − 1) × 0.02) = ${m.volume} — more observers = louder</td>
          </tr>
          <tr>
            <td class="map-param">Voices</td>
            <td class="map-value">${m.voiceCount}</td>
            <td class="map-why">min(⌈log₂(${m.obsCount} + 1)⌉, 8) = ${m.voiceCount} — more observers = richer chord</td>
          </tr>
          <tr>
            <td class="map-param">Detune</td>
            <td class="map-value">${m.detuneDesc.join(', ')}</td>
            <td class="map-why">${m.voiceCount} voices detuned for shimmer — wider spread with more voices</td>
          </tr>
          <tr>
            <td class="map-param">Pan</td>
            <td class="map-value">${m.panValue}</td>
            <td class="map-why">${m.panSource}</td>
          </tr>
        </table>
      </div>

      <div class="alab-section">
        <h3>🎹 Note Sequence</h3>
        <table class="alab-note-table">
          <tr><th></th><th>#</th><th>Payload Index</th><th>Byte</th><th>→ MIDI</th><th>→ Freq</th><th>Duration (why)</th><th>Gap (why)</th></tr>
          ${m.notes.map((n, i) => {
            const durWhy = `byte ${n.byte} → map(0...255 → 50...400ms) × tempo`;
            const gapWhy = i < m.notes.length - 1
              ? `|${n.byte} − ${m.notes[i+1].byte}| = ${Math.abs(m.notes[i+1].byte - n.byte)} → map(0...255 → 30...300ms) × tempo`
              : '';
            return `<tr id="noteRow${i}" class="alab-note-clickable" data-note-idx="${i}">
            <td><button class="alab-note-play" data-note-idx="${i}" title="Play this note">▶</button></td>
            <td>${i + 1}</td>
            <td>[${n.index}]</td>
            <td>0x${n.byte.toString(16).padStart(2, '0').toUpperCase()} (${n.byte})</td>
            <td>${n.midi}</td>
            <td>${n.freq} Hz</td>
            <td>${n.duration} ms <span class="map-why-inline">${durWhy}</span></td>
            <td>${i < m.notes.length - 1 ? n.gap + ' ms <span class="map-why-inline">' + gapWhy + '</span>' : '—'}</td>
          </tr>`;}).join('')}
        </table>
      </div>

      <div class="alab-section">
        <h3>📊 Byte Visualizer</h3>
        <div class="alab-byte-viz" id="alabByteViz"></div>
      </div>
    `;

    // Render byte visualizer
    const viz = document.getElementById('alabByteViz');
    if (viz) {
      for (let i = 0; i < m.payloadBytes.length; i++) {
        const bar = document.createElement('div');
        bar.className = 'alab-byte-bar';
        bar.id = 'byteBar' + i;
        const h = Math.max(2, (m.payloadBytes[i] / 255) * 60);
        bar.style.height = h + 'px';
        bar.style.background = sampledSet.has(i) ? m.color : '#555';
        bar.style.opacity = sampledSet.has(i) ? '1' : '0.3';
        bar.title = `[${i}] 0x${m.payloadBytes[i].toString(16).padStart(2, '0')} = ${m.payloadBytes[i]}`;
        viz.appendChild(bar);
      }
    }

    // Wire up individual note play buttons
    document.querySelectorAll('.alab-note-play').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        playOneNote(parseInt(btn.dataset.noteIdx));
      });
    });
    // Also allow clicking anywhere on the row
    document.querySelectorAll('.alab-note-clickable').forEach(row => {
      row.addEventListener('click', () => playOneNote(parseInt(row.dataset.noteIdx)));
    });
  }

  function clearHighlights() {
    highlightTimers.forEach(t => clearTimeout(t));
    highlightTimers = [];
    document.querySelectorAll('.alab-hex .playing, .alab-note-table .playing, .alab-byte-bar.playing').forEach(el => el.classList.remove('playing'));
  }

  function highlightPlayback(mapping) {
    clearHighlights();
    let timeOffset = 0;
    mapping.notes.forEach((note, i) => {
      // Highlight ON
      highlightTimers.push(setTimeout(() => {
        // Clear previous note highlights
        document.querySelectorAll('.alab-hex .playing, .alab-note-table .playing, .alab-byte-bar.playing').forEach(el => el.classList.remove('playing'));
        // Hex byte
        const hexEl = document.getElementById('hexByte' + note.index);
        if (hexEl) { hexEl.classList.add('playing'); hexEl.scrollIntoView({ block: 'nearest', behavior: 'smooth' }); }
        // Note row
        const rowEl = document.getElementById('noteRow' + i);
        if (rowEl) { rowEl.classList.add('playing'); rowEl.scrollIntoView({ block: 'nearest', behavior: 'smooth' }); }
        // Byte bar
        const barEl = document.getElementById('byteBar' + note.index);
        if (barEl) barEl.classList.add('playing');
      }, timeOffset));
      timeOffset += note.duration + (i < mapping.notes.length - 1 ? note.gap : 0);
    });
    // Clear all at end
    highlightTimers.push(setTimeout(clearHighlights, timeOffset + 200));
  }

  function playOneNote(noteIdx) {
    if (!selectedPacket) return;
    const m = computeMapping(selectedPacket);
    if (!m || !m.notes[noteIdx]) return;

    if (window.MeshAudio && !MeshAudio.isEnabled()) MeshAudio.setEnabled(true);
    const audioCtx = MeshAudio.getContext();
    if (!audioCtx) return;
    if (audioCtx.state === 'suspended') audioCtx.resume();

    const note = m.notes[noteIdx];
    const oscType = SYNTH_TYPES[m.typeName] || 'triangle';
    const ADSR = { ADVERT: { a: 0.02, d: 0.3, s: 0.4, r: 0.5 }, GRP_TXT: { a: 0.005, d: 0.15, s: 0.1, r: 0.2 },
      TXT_MSG: { a: 0.01, d: 0.2, s: 0.3, r: 0.4 }, TRACE: { a: 0.05, d: 0.4, s: 0.5, r: 0.8 } };
    const env = ADSR[m.typeName] || ADSR.ADVERT;
    const vol = parseFloat(m.volume) || 0.3;
    const dur = note.duration / 1000;

    const osc = audioCtx.createOscillator();
    const gain = audioCtx.createGain();
    const filter = audioCtx.createBiquadFilter();
    filter.type = 'lowpass';
    filter.frequency.value = m.filterHz;

    osc.type = oscType;
    osc.frequency.value = note.freq;

    const now = audioCtx.currentTime + 0.02;
    const sustainVol = Math.max(vol * env.s, 0.0001);
    gain.gain.setValueAtTime(0.0001, now);
    gain.gain.exponentialRampToValueAtTime(Math.max(vol, 0.0001), now + env.a);
    gain.gain.exponentialRampToValueAtTime(sustainVol, now + env.a + env.d);
    gain.gain.setTargetAtTime(0.0001, now + dur, env.r / 5);

    osc.connect(gain);
    gain.connect(filter);
    filter.connect(audioCtx.destination);
    osc.start(now);
    osc.stop(now + dur + env.r + 0.1);
    osc.onended = () => { osc.disconnect(); gain.disconnect(); filter.disconnect(); };

    // Highlight this note
    clearHighlights();
    const hexEl = document.getElementById('hexByte' + note.index);
    const rowEl = document.getElementById('noteRow' + noteIdx);
    const barEl = document.getElementById('byteBar' + note.index);
    if (hexEl) hexEl.classList.add('playing');
    if (rowEl) rowEl.classList.add('playing');
    if (barEl) barEl.classList.add('playing');
    highlightTimers.push(setTimeout(clearHighlights, note.duration + 200));
  }

  function playSelected() {
    if (!selectedPacket) return;
    if (window.MeshAudio) {
      if (!MeshAudio.isEnabled()) MeshAudio.setEnabled(true);
      // Build a packet object that sonifyPacket expects
      const pkt = {
        raw_hex: selectedPacket.raw_hex,
        raw: selectedPacket.raw_hex,
        observation_count: selectedPacket.observation_count || 1,
        decoded: {}
      };
      try {
        const d = JSON.parse(selectedPacket.decoded_json || '{}');
        const typeName = d.type || 'UNKNOWN';
        pkt.decoded = {
          header: { payloadTypeName: typeName },
          payload: d,
          path: { hops: JSON.parse(selectedPacket.path_json || '[]') }
        };
      } catch {}
      MeshAudio.sonifyPacket(pkt);
      // Sync highlights with audio
      const m = computeMapping(selectedPacket);
      if (m) highlightPlayback(m);
    }
  }

  async function init(app) {
    injectStyles();
    baseBPM = (MeshAudio && MeshAudio.getBPM) ? MeshAudio.getBPM() : 120;
    speedMult = 1;

    app.innerHTML = `
      <div class="alab">
        <div class="alab-sidebar" id="alabSidebar"><div style="color:var(--text-muted);font-size:13px;padding:8px">Loading packets...</div></div>
        <div class="alab-main">
          <div class="alab-controls" id="alabControls">
            <button class="alab-btn" id="alabPlay" title="Play selected packet">▶ Play</button>
            <button class="alab-btn" id="alabLoop" title="Loop playback">🔁 Loop</button>
            <span style="font-size:12px;color:var(--text-muted)">Speed:</span>
            <button class="alab-speed" data-speed="0.25">0.25x</button>
            <button class="alab-speed active" data-speed="1">1x</button>
            <button class="alab-speed" data-speed="2">2x</button>
            <button class="alab-speed" data-speed="4">4x</button>
            <div class="alab-slider-group">
              <span>BPM</span>
              <input type="range" id="alabBPM" min="30" max="300" value="${baseBPM}">
              <span id="alabBPMVal">${baseBPM}</span>
            </div>
            <div class="alab-slider-group">
              <span>Vol</span>
              <input type="range" id="alabVol" min="0" max="100" value="${MeshAudio && MeshAudio.getVolume ? Math.round(MeshAudio.getVolume() * 100) : 30}">
              <span id="alabVolVal">${MeshAudio && MeshAudio.getVolume ? Math.round(MeshAudio.getVolume() * 100) : 30}%</span>
            </div>
            <div class="alab-slider-group">
              <span>Voice</span>
              <select id="alabVoice">${(MeshAudio && MeshAudio.getVoiceNames ? MeshAudio.getVoiceNames() : ['constellation']).map(v =>
                `<option value="${v}" ${(MeshAudio && MeshAudio.getVoiceName && MeshAudio.getVoiceName() === v) ? 'selected' : ''}>${v}</option>`
              ).join('')}</select>
            </div>
          </div>
          <div id="alabDetail"><div class="alab-empty">← Select a packet from the sidebar to explore its sound</div></div>
        </div>
      </div>
    `;

    // Controls
    document.getElementById('alabPlay').addEventListener('click', playSelected);

    document.getElementById('alabLoop').addEventListener('click', function () {
      if (loopTimer) { clearInterval(loopTimer); loopTimer = null; this.classList.remove('active'); return; }
      this.classList.add('active');
      playSelected();
      loopTimer = setInterval(playSelected, 3000);
    });

    document.querySelectorAll('.alab-speed').forEach(btn => {
      btn.addEventListener('click', function () {
        document.querySelectorAll('.alab-speed').forEach(b => b.classList.remove('active'));
        this.classList.add('active');
        speedMult = parseFloat(this.dataset.speed);
        if (MeshAudio && MeshAudio.setBPM) MeshAudio.setBPM(baseBPM * speedMult);
        if (selectedPacket) renderDetail(selectedPacket, app);
      });
    });

    document.getElementById('alabBPM').addEventListener('input', function () {
      baseBPM = parseInt(this.value);
      document.getElementById('alabBPMVal').textContent = baseBPM;
      if (MeshAudio && MeshAudio.setBPM) MeshAudio.setBPM(baseBPM * speedMult);
      if (selectedPacket) renderDetail(selectedPacket, app);
    });

    document.getElementById('alabVol').addEventListener('input', function () {
      const v = parseInt(this.value) / 100;
      document.getElementById('alabVolVal').textContent = Math.round(v * 100) + '%';
      if (MeshAudio && MeshAudio.setVolume) MeshAudio.setVolume(v);
    });

    document.getElementById('alabVoice').addEventListener('change', function () {
      if (MeshAudio && MeshAudio.setVoice) MeshAudio.setVoice(this.value);
    });

    // Load buckets
    try {
      const data = await api('/audio-lab/buckets');
      const sidebar = document.getElementById('alabSidebar');
      if (!data.buckets || Object.keys(data.buckets).length === 0) {
        sidebar.innerHTML = '<div style="color:var(--text-muted);font-size:13px;padding:8px">No packets in memory yet</div>';
        return;
      }

      let html = '';
      for (const [type, pkts] of Object.entries(data.buckets)) {
        const color = TYPE_COLORS[type] || TYPE_COLORS.UNKNOWN;
        html += `<div class="alab-type-hdr" style="background:${color}22;color:${color}" data-type="${type}">
          <span>${type}</span><span style="font-size:11px;opacity:0.7">${pkts.length}</span></div>`;
        html += `<div class="alab-type-list" data-type-list="${type}">`;
        pkts.forEach((p, i) => {
          const size = p.raw_hex ? p.raw_hex.length / 2 : 0;
          html += `<div class="alab-pkt" data-type="${type}" data-idx="${i}">#${i + 1} — ${size}B — ${p.observation_count || 1} obs</div>`;
        });
        html += '</div>';
      }
      sidebar.innerHTML = html;

      // Store buckets for selection
      sidebar._buckets = data.buckets;

      // Click handlers
      sidebar.addEventListener('click', function (e) {
        const typeHdr = e.target.closest('.alab-type-hdr');
        if (typeHdr) {
          const list = sidebar.querySelector(`[data-type-list="${typeHdr.dataset.type}"]`);
          if (list) list.style.display = list.style.display === 'none' ? '' : 'none';
          return;
        }
        const pktEl = e.target.closest('.alab-pkt');
        if (pktEl) {
          sidebar.querySelectorAll('.alab-pkt').forEach(el => el.classList.remove('selected'));
          pktEl.classList.add('selected');
          const type = pktEl.dataset.type;
          const idx = parseInt(pktEl.dataset.idx);
          selectedPacket = sidebar._buckets[type][idx];
          renderDetail(selectedPacket, app);
        }
      });
    } catch (err) {
      document.getElementById('alabSidebar').innerHTML = `<div style="color:var(--text-muted);padding:8px">Error loading packets: ${err.message}</div>`;
    }
  }

  function destroy() {
    clearHighlights();
    if (loopTimer) { clearInterval(loopTimer); loopTimer = null; }
    if (styleEl) { styleEl.remove(); styleEl = null; }
    selectedPacket = null;
  }

  registerPage('audio-lab', { init, destroy });
})();
