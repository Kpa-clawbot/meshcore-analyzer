(function() {
  'use strict';

  let map, ws, nodesLayer, pathsLayer, animLayer, heatLayer;
  let nodeMarkers = {};
  let nodeData = {};
  let packetCount = 0;
  let activeAnims = 0;
  let nodeActivity = {};
  let recentPaths = [];
  let audioCtx = null;
  let soundEnabled = false;
  let showGhostHops = localStorage.getItem('live-ghost-hops') !== 'false';
  let _onResize = null;
  let _navCleanup = null;

  // === VCR State Machine ===
  const VCR = {
    mode: 'LIVE',        // LIVE | PAUSED | REPLAY
    buffer: [],          // { ts: Date.now(), pkt } — all packets seen
    playhead: -1,        // index in buffer (-1 = live tail)
    missedCount: 0,      // packets arrived while paused
    speed: 1,            // replay speed: 1, 2, 4, 8
    replayTimer: null,
    timelineScope: 3600000, // 1h default ms
    timelineTimestamps: [], // historical timestamps from DB for sparkline
    timelineFetchedScope: 0, // last fetched scope to avoid redundant fetches
  };

  const ROLE_COLORS = {
    repeater: '#3b82f6', companion: '#06b6d4', room: '#a855f7',
    sensor: '#f59e0b', unknown: '#6b7280'
  };

  const TYPE_COLORS = {
    ADVERT: '#22c55e', GRP_TXT: '#3b82f6', TXT_MSG: '#f59e0b', ACK: '#6b7280',
    REQUEST: '#a855f7', RESPONSE: '#06b6d4', TRACE: '#ec4899', PATH: '#14b8a6'
  };

  const PAYLOAD_ICONS = {
    ADVERT: '📡', GRP_TXT: '💬', TXT_MSG: '✉️', ACK: '✓',
    REQUEST: '❓', RESPONSE: '📨', TRACE: '🔍', PATH: '🛤️'
  };

  function playSound(typeName) {
    if (!soundEnabled || !audioCtx) return;
    try {
      const osc = audioCtx.createOscillator();
      const gain = audioCtx.createGain();
      osc.connect(gain); gain.connect(audioCtx.destination);
      const freqs = { ADVERT: 880, GRP_TXT: 523, TXT_MSG: 659, ACK: 330, REQUEST: 740, TRACE: 987 };
      osc.frequency.value = freqs[typeName] || 440;
      osc.type = typeName === 'GRP_TXT' ? 'sine' : typeName === 'ADVERT' ? 'triangle' : 'square';
      gain.gain.setValueAtTime(0.03, audioCtx.currentTime);
      gain.gain.exponentialRampToValueAtTime(0.001, audioCtx.currentTime + 0.15);
      osc.start(audioCtx.currentTime); osc.stop(audioCtx.currentTime + 0.15);
    } catch {}
  }

  function initResizeHandler() {
    let resizeTimer = null;
    _onResize = function() {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => { if (map) map.invalidateSize({ animate: false }); }, 150);
    };
    window.addEventListener('resize', _onResize);
    window.addEventListener('orientationchange', () => setTimeout(_onResize, 200));
  }

  // === VCR Controls ===

  function vcrSetMode(mode) {
    VCR.mode = mode;
    if (mode !== 'LIVE' && !VCR.frozenNow) VCR.frozenNow = Date.now();
    if (mode === 'LIVE') VCR.frozenNow = null;
    updateVCRUI();
  }

  function vcrPause() {
    if (VCR.mode === 'PAUSED') return;
    stopReplay();
    VCR.missedCount = 0;
    vcrSetMode('PAUSED');
  }

  function vcrResumeLive() {
    stopReplay();
    VCR.playhead = -1;
    VCR.speed = 1;
    VCR.missedCount = 0;
    VCR.scrubEnd = null;
    VCR.dragPct = null;
    vcrSetMode('LIVE');
    const prompt = document.getElementById('vcrPrompt');
    if (prompt) prompt.classList.add('hidden');
  }

  function vcrUnpause() {
    if (VCR.mode !== 'PAUSED') return;
    if (VCR.missedCount > 3) {
      // Show Option C prompt
      showVCRPrompt(VCR.missedCount);
    } else if (VCR.missedCount > 0) {
      // Few packets — just replay them quickly
      vcrReplayMissed();
    } else {
      vcrResumeLive();
    }
  }

  function showVCRPrompt(count) {
    const prompt = document.getElementById('vcrPrompt');
    if (!prompt) return;
    prompt.innerHTML = `
      <span>You missed <strong>${count}</strong> packets.</span>
      <button id="vcrPromptReplay" class="vcr-prompt-btn">▶ Replay</button>
      <button id="vcrPromptSkip" class="vcr-prompt-btn">⏭ Skip to live</button>
    `;
    prompt.classList.remove('hidden');
    document.getElementById('vcrPromptReplay').addEventListener('click', () => {
      prompt.classList.add('hidden');
      vcrReplayMissed();
    });
    document.getElementById('vcrPromptSkip').addEventListener('click', () => {
      prompt.classList.add('hidden');
      vcrResumeLive();
    });
  }

  function vcrReplayMissed() {
    const startIdx = VCR.buffer.length - VCR.missedCount;
    VCR.playhead = Math.max(0, startIdx);
    VCR.missedCount = 0;
    VCR.speed = 2; // slightly fast
    vcrSetMode('REPLAY');
    startReplay();
  }

  function vcrRewind(ms) {
    stopReplay();
    // Fetch packets from DB for the time window
    const now = Date.now();
    const from = new Date(now - ms).toISOString();
    fetch(`/api/packets?limit=200&grouped=false&since=${encodeURIComponent(from)}`)
      .then(r => r.json())
      .then(data => {
        const pkts = (data.packets || []).reverse(); // oldest first
        // Prepend to buffer (avoid duplicates by ID)
        const existingIds = new Set(VCR.buffer.map(b => b.pkt.id).filter(Boolean));
        const newEntries = pkts.filter(p => !existingIds.has(p.id)).map(p => ({
          ts: new Date(p.timestamp || p.created_at).getTime(),
          pkt: dbPacketToLive(p)
        }));
        VCR.buffer = [...newEntries, ...VCR.buffer];
        VCR.playhead = 0;
        VCR.speed = 1;
        vcrSetMode('REPLAY');
        startReplay();
        updateTimeline();
      })
      .catch(() => {});
  }

  function startReplay() {
    stopReplay();
    function tick() {
      if (VCR.mode !== 'REPLAY') return;
      const endIdx = VCR.scrubEnd != null ? VCR.scrubEnd : VCR.buffer.length;
      if (VCR.playhead >= endIdx) {
        VCR.scrubEnd = null;
        vcrSetMode('PAUSED');
        return;
      }
      const entry = VCR.buffer[VCR.playhead];
      animatePacket(entry.pkt);
      updateVCRClock(entry.ts);
      VCR.playhead++;
      updateVCRUI();
      updateTimelinePlayhead();

      // Calculate delay to next packet
      let delay = 150; // default
      if (VCR.playhead < VCR.buffer.length) {
        const nextEntry = VCR.buffer[VCR.playhead];
        const realGap = nextEntry.ts - entry.ts;
        delay = Math.min(2000, Math.max(80, realGap)) / VCR.speed;
      }
      VCR.replayTimer = setTimeout(tick, delay);
    }
    tick();
  }

  function stopReplay() {
    if (VCR.replayTimer) { clearTimeout(VCR.replayTimer); VCR.replayTimer = null; }
  }

  function vcrSpeedCycle() {
    const speeds = [1, 2, 4, 8];
    const idx = speeds.indexOf(VCR.speed);
    VCR.speed = speeds[(idx + 1) % speeds.length];
    updateVCRUI();
    // If replaying, restart with new speed
    if (VCR.mode === 'REPLAY' && VCR.replayTimer) {
      stopReplay();
      startReplay();
    }
  }

  function updateVCRClock(tsMs) {
    const el = document.getElementById('vcrClock');
    if (!el) return;
    const d = new Date(tsMs);
    const hh = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    const ss = String(d.getSeconds()).padStart(2, '0');
    el.textContent = `${hh}:${mm}:${ss}`;
  }

  function updateVCRUI() {
    const modeEl = document.getElementById('vcrMode');
    const pauseBtn = document.getElementById('vcrPauseBtn');
    const speedBtn = document.getElementById('vcrSpeedBtn');
    const missedEl = document.getElementById('vcrMissed');
    if (!modeEl) return;

    if (VCR.mode === 'LIVE') {
      modeEl.innerHTML = '<span class="vcr-live-dot"></span> LIVE';
      modeEl.className = 'vcr-mode vcr-mode-live';
      if (pauseBtn) pauseBtn.textContent = '⏸';
      if (missedEl) missedEl.classList.add('hidden');
      updateVCRClock(Date.now());
    } else if (VCR.mode === 'PAUSED') {
      modeEl.textContent = '⏸ PAUSED';
      modeEl.className = 'vcr-mode vcr-mode-paused';
      if (pauseBtn) pauseBtn.textContent = '▶';
      if (missedEl && VCR.missedCount > 0) {
        missedEl.textContent = `+${VCR.missedCount}`;
        missedEl.classList.remove('hidden');
      }
    } else if (VCR.mode === 'REPLAY') {
      modeEl.textContent = `⏪ REPLAY`;
      modeEl.className = 'vcr-mode vcr-mode-replay';
      if (pauseBtn) pauseBtn.textContent = '⏸';
      if (missedEl) missedEl.classList.add('hidden');
    }
    if (speedBtn) speedBtn.textContent = VCR.speed + 'x';
  }

  function dbPacketToLive(pkt) {
    const raw = JSON.parse(pkt.decoded_json || '{}');
    const hops = JSON.parse(pkt.path_json || '[]');
    const typeName = raw.type || pkt.payload_type_name || 'UNKNOWN';
    return {
      id: pkt.id, hash: pkt.hash,
      _ts: new Date(pkt.timestamp || pkt.created_at).getTime(),
      decoded: { header: { payloadTypeName: typeName }, payload: raw, path: { hops } },
      snr: pkt.snr, rssi: pkt.rssi, observer: pkt.observer_name
    };
  }

  // Buffer a packet from WS
  function bufferPacket(pkt) {
    pkt._ts = Date.now();
    const entry = { ts: pkt._ts, pkt };
    VCR.buffer.push(entry);
    // Keep buffer capped at ~2000
    if (VCR.buffer.length > 2000) VCR.buffer.splice(0, 500);

    if (VCR.mode === 'LIVE') {
      animatePacket(pkt);
      updateTimeline();
    } else if (VCR.mode === 'PAUSED') {
      VCR.missedCount++;
      updateVCRUI();
      updateTimeline();
    }
    // In REPLAY mode, new packets just go to buffer, will be reached when playhead catches up
  }

  // === Timeline ===

  async function fetchTimelineTimestamps() {
    const scopeMs = VCR.timelineScope;
    if (scopeMs === VCR.timelineFetchedScope) return;
    const since = new Date(Date.now() - scopeMs).toISOString();
    try {
      const resp = await fetch(`/api/packets/timestamps?since=${encodeURIComponent(since)}`);
      if (resp.ok) {
        const timestamps = await resp.json(); // array of ISO strings
        VCR.timelineTimestamps = timestamps.map(t => new Date(t).getTime());
        VCR.timelineFetchedScope = scopeMs;
      }
    } catch(e) { /* ignore */ }
  }

  function updateTimeline() {
    const canvas = document.getElementById('vcrTimeline');
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    const w = canvas.width = canvas.offsetWidth * (window.devicePixelRatio || 1);
    const h = canvas.height = canvas.offsetHeight * (window.devicePixelRatio || 1);
    ctx.scale(window.devicePixelRatio || 1, window.devicePixelRatio || 1);
    const cw = canvas.offsetWidth;
    const ch = canvas.offsetHeight;

    ctx.clearRect(0, 0, cw, ch);

    const now = VCR.frozenNow || Date.now();
    const scopeMs = VCR.timelineScope;
    const startTs = now - scopeMs;

    // Merge historical DB timestamps with live buffer timestamps
    const allTimestamps = [];
    VCR.timelineTimestamps.forEach(ts => {
      if (ts >= startTs) allTimestamps.push(ts);
    });
    VCR.buffer.forEach(entry => {
      if (entry.ts >= startTs) allTimestamps.push(entry.ts);
    });

    if (allTimestamps.length === 0) return;

    // Draw density sparkline
    const buckets = 100;
    const counts = new Array(buckets).fill(0);
    let maxCount = 0;
    allTimestamps.forEach(ts => {
      const bucket = Math.floor((ts - startTs) / scopeMs * buckets);
      if (bucket >= 0 && bucket < buckets) {
        counts[bucket]++;
        if (counts[bucket] > maxCount) maxCount = counts[bucket];
      }
    });

    if (maxCount === 0) return;

    const barW = cw / buckets;
    ctx.fillStyle = 'rgba(59, 130, 246, 0.4)';
    counts.forEach((c, i) => {
      if (c === 0) return;
      const barH = (c / maxCount) * (ch - 4);
      ctx.fillRect(i * barW, ch - barH - 2, barW - 1, barH);
    });

    // Draw playhead
    updateTimelinePlayhead();
  }

  function updateTimelinePlayhead() {
    if (VCR.dragging) return;
    const playheadEl = document.getElementById('vcrPlayhead');
    if (!playheadEl) return;
    const canvas = document.getElementById('vcrTimeline');
    if (!canvas) return;
    const cw = canvas.offsetWidth;
    const now = VCR.frozenNow || Date.now();
    const scopeMs = VCR.timelineScope;
    const startTs = now - scopeMs;

    let x;
    if (VCR.mode === 'LIVE') {
      x = cw;
    } else if (VCR.playhead >= 0 && VCR.playhead < VCR.buffer.length) {
      const playTs = VCR.buffer[VCR.playhead].ts;
      x = ((playTs - startTs) / scopeMs) * cw;
    } else {
      x = cw;
    }
    playheadEl.style.left = Math.max(0, Math.min(cw - 2, x)) + 'px';
  }

  function handleTimelineClick(e) {
    const canvas = document.getElementById('vcrTimeline');
    if (!canvas) return;
    const rect = canvas.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const pct = x / rect.width;
    const now = Date.now();
    const targetTs = now - VCR.timelineScope + pct * VCR.timelineScope;

    // Find closest buffer entry
    let closest = 0;
    let minDist = Infinity;
    VCR.buffer.forEach((entry, i) => {
      const dist = Math.abs(entry.ts - targetTs);
      if (dist < minDist) { minDist = dist; closest = i; }
    });

    // If click is before our buffer, fetch from DB
    if (VCR.buffer.length === 0 || targetTs < VCR.buffer[0].ts - 5000) {
      vcrRewind(now - targetTs);
      return;
    }

    stopReplay();
    VCR.playhead = closest;
    vcrSetMode('REPLAY');
    startReplay();
  }

  async function init(app) {
    app.innerHTML = `
      <div class="live-page">
        <div id="liveMap" style="width:100%;height:100%;position:absolute;top:0;left:0;z-index:1"></div>
        <div class="live-overlay live-header" id="liveHeader">
          <div class="live-title">
            <span class="live-beacon"></span>
            MESH LIVE
          </div>
          <div class="live-stats-row">
            <div class="live-stat-pill"><span id="livePktCount">0</span> pkts</div>
            <div class="live-stat-pill"><span id="liveNodeCount">0</span> nodes</div>
            <div class="live-stat-pill anim-pill"><span id="liveAnimCount">0</span> active</div>
            <div class="live-stat-pill rate-pill"><span id="livePktRate">0</span>/min</div>
          </div>
          <button class="live-sound-btn" id="liveSoundBtn" title="Toggle sound">🔇</button>
          <div class="live-toggles">
            <label><input type="checkbox" id="liveHeatToggle" checked> Heat</label>
            <label><input type="checkbox" id="liveGhostToggle" checked> Ghosts</label>
          </div>
        </div>
        <div class="live-overlay live-feed" id="liveFeed">
          <button class="feed-hide-btn" id="feedHideBtn" title="Hide feed">✕</button>
        </div>
        <button class="feed-show-btn hidden" id="feedShowBtn" title="Show feed">📋</button>
        <div class="live-overlay live-legend">
          <div class="legend-title">PACKET TYPES</div>
          <div><span class="live-dot" style="background:#22c55e"></span> Advert</div>
          <div><span class="live-dot" style="background:#3b82f6"></span> Message</div>
          <div><span class="live-dot" style="background:#f59e0b"></span> Direct</div>
          <div><span class="live-dot" style="background:#a855f7"></span> Request</div>
          <div><span class="live-dot" style="background:#ec4899"></span> Trace</div>
          <div class="legend-title" style="margin-top:8px">NODE ROLES</div>
          <div><span class="live-dot" style="background:#3b82f6"></span> Repeater</div>
          <div><span class="live-dot" style="background:#06b6d4"></span> Companion</div>
          <div><span class="live-dot" style="background:#a855f7"></span> Room</div>
          <div><span class="live-dot" style="background:#f59e0b"></span> Sensor</div>
        </div>

        <!-- VCR Bar -->
        <div class="vcr-bar" id="vcrBar">
          <div class="vcr-controls">
            <button id="vcrRewindBtn" class="vcr-btn" title="Rewind">⏪</button>
            <button id="vcrPauseBtn" class="vcr-btn" title="Pause/Play">⏸</button>
            <button id="vcrLiveBtn" class="vcr-btn vcr-live-btn" title="Jump to live">LIVE</button>
            <button id="vcrSpeedBtn" class="vcr-btn" title="Playback speed">1x</button>
            <div id="vcrMode" class="vcr-mode vcr-mode-live"><span class="vcr-live-dot"></span> LIVE</div>
            <span id="vcrClock" class="vcr-clock">--:--:--</span>
            <span id="vcrMissed" class="vcr-missed hidden">+0</span>
          </div>
          <div class="vcr-timeline-wrap">
            <div class="vcr-scope-btns">
              <button class="vcr-scope-btn active" data-scope="3600000">1h</button>
              <button class="vcr-scope-btn" data-scope="21600000">6h</button>
              <button class="vcr-scope-btn" data-scope="43200000">12h</button>
              <button class="vcr-scope-btn" data-scope="86400000">24h</button>
            </div>
            <div class="vcr-timeline-container">
              <canvas id="vcrTimeline" class="vcr-timeline"></canvas>
              <div id="vcrPlayhead" class="vcr-playhead"></div>
              <div id="vcrTimeTooltip" class="vcr-time-tooltip hidden"></div>
            </div>
          </div>
          <div id="vcrPrompt" class="vcr-prompt hidden"></div>
        </div>
      </div>`;

    map = L.map('liveMap', {
      zoomControl: false, attributionControl: false,
      zoomAnimation: true, markerZoomAnimation: true
    }).setView([37.45, -122.0], 9);

    L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', { maxZoom: 19 }).addTo(map);
    L.control.zoom({ position: 'topright' }).addTo(map);

    nodesLayer = L.layerGroup().addTo(map);
    pathsLayer = L.layerGroup().addTo(map);
    animLayer = L.layerGroup().addTo(map);

    injectSVGFilters();
    await loadNodes();
    showHeatMap();
    connectWS();
    initResizeHandler();
    startRateCounter();

    // Check for single packet replay from packets page
    const replayData = sessionStorage.getItem('replay-packet');
    if (replayData) {
      sessionStorage.removeItem('replay-packet');
      try {
        const pkt = JSON.parse(replayData);
        vcrPause(); // suppress live packets
        setTimeout(() => animatePacket(pkt), 1500);
      } catch {}
    } else {
      replayRecent();
    }

    map.on('zoomend', rescaleMarkers);

    // Sound toggle
    document.getElementById('liveSoundBtn').addEventListener('click', () => {
      if (!audioCtx) audioCtx = new (window.AudioContext || window.webkitAudioContext)();
      soundEnabled = !soundEnabled;
      document.getElementById('liveSoundBtn').textContent = soundEnabled ? '🔊' : '🔇';
    });

    // Heat map toggle
    document.getElementById('liveHeatToggle').addEventListener('change', (e) => {
      if (e.target.checked) showHeatMap(); else hideHeatMap();
    });

    const ghostToggle = document.getElementById('liveGhostToggle');
    ghostToggle.checked = showGhostHops;
    ghostToggle.addEventListener('change', (e) => {
      showGhostHops = e.target.checked;
      localStorage.setItem('live-ghost-hops', showGhostHops);
    });

    // Feed show/hide
    const feedEl = document.getElementById('liveFeed');
    const feedHideBtn = document.getElementById('feedHideBtn');
    const feedShowBtn = document.getElementById('feedShowBtn');
    if (localStorage.getItem('live-feed-hidden') === 'true') {
      feedEl.classList.add('hidden');
      feedShowBtn.classList.remove('hidden');
    }
    feedHideBtn.addEventListener('click', () => {
      feedEl.classList.add('hidden'); feedShowBtn.classList.remove('hidden');
      localStorage.setItem('live-feed-hidden', 'true');
    });
    feedShowBtn.addEventListener('click', () => {
      feedEl.classList.remove('hidden'); feedShowBtn.classList.add('hidden');
      localStorage.setItem('live-feed-hidden', 'false');
    });

    // Save/restore map view
    const savedView = localStorage.getItem('live-map-view');
    if (savedView) {
      try { const v = JSON.parse(savedView); map.setView([v.lat, v.lng], v.zoom); } catch {}
    }
    map.on('moveend', () => {
      const c = map.getCenter();
      localStorage.setItem('live-map-view', JSON.stringify({ lat: c.lat, lng: c.lng, zoom: map.getZoom() }));
    });

    // === VCR event listeners ===
    document.getElementById('vcrPauseBtn').addEventListener('click', () => {
      if (VCR.mode === 'PAUSED') vcrUnpause();
      else if (VCR.mode === 'REPLAY') { stopReplay(); vcrSetMode('PAUSED'); }
      else vcrPause();
    });
    document.getElementById('vcrLiveBtn').addEventListener('click', vcrResumeLive);
    document.getElementById('vcrSpeedBtn').addEventListener('click', vcrSpeedCycle);
    document.getElementById('vcrRewindBtn').addEventListener('click', () => {
      // Rewind by current scope
      vcrRewind(VCR.timelineScope);
    });

    // Scope buttons
    document.querySelectorAll('.vcr-scope-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        document.querySelectorAll('.vcr-scope-btn').forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        VCR.timelineScope = parseInt(btn.dataset.scope);
        fetchTimelineTimestamps().then(() => updateTimeline());
      });
    });

    // Timeline click to scrub
    // Timeline click handled by drag (mousedown+mouseup)

    // Timeline hover — show time tooltip
    const timelineEl = document.getElementById('vcrTimeline');
    const timeTooltip = document.getElementById('vcrTimeTooltip');
    timelineEl.addEventListener('mousemove', (e) => {
      const rect = timelineEl.getBoundingClientRect();
      const pct = (e.clientX - rect.left) / rect.width;
      const ts = Date.now() - VCR.timelineScope + pct * VCR.timelineScope;
      const d = new Date(ts);
      timeTooltip.textContent = d.toLocaleTimeString([], {hour:'2-digit',minute:'2-digit',second:'2-digit'});
      timeTooltip.style.left = (e.clientX - rect.left) + 'px';
      timeTooltip.classList.remove('hidden');
    });
    timelineEl.addEventListener('mouseleave', () => { timeTooltip.classList.add('hidden'); });

    // Drag scrubbing on timeline
    VCR.dragging = false;
    VCR.dragPct = 0;

    function scrubVisual(clientX) {
      const rect = timelineEl.getBoundingClientRect();
      VCR.dragPct = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
      const playheadEl = document.getElementById('vcrPlayhead');
      if (playheadEl) playheadEl.style.left = (VCR.dragPct * rect.width) + 'px';
      // Update VCR clock to show drag target time
      const now = VCR.frozenNow || Date.now();
      const targetTs = now - VCR.timelineScope + VCR.dragPct * VCR.timelineScope;
      updateVCRClock(targetTs);
    }

    function scrubCommit() {
      VCR.frozenNow = Date.now(); // freeze timeline at scrub moment
      const targetTs = VCR.frozenNow - VCR.timelineScope + VCR.dragPct * VCR.timelineScope;

      stopReplay();
      vcrSetMode('REPLAY');
      const fetchFrom = new Date(targetTs - 30000).toISOString(); // 30s before target
      fetch(`/api/packets?limit=200&grouped=false&since=${encodeURIComponent(fetchFrom)}`)
        .then(r => r.json())
        .then(data => {
          const pkts = (data.packets || []).reverse(); // oldest first
          const existingIds = new Set(VCR.buffer.map(b => b.pkt.id).filter(Boolean));
          const newEntries = pkts.filter(p => !existingIds.has(p.id)).map(p => ({
            ts: new Date(p.timestamp || p.created_at).getTime(),
            pkt: dbPacketToLive(p)
          }));
          if (newEntries.length) {
            VCR.buffer = [...newEntries, ...VCR.buffer].sort((a,b) => a.ts - b.ts);
          }
          // Find closest entry to target timestamp
          let closest = 0, minDist = Infinity;
          VCR.buffer.forEach((entry, i) => {
            const dist = Math.abs(entry.ts - targetTs);
            if (dist < minDist) { minDist = dist; closest = i; }
          });
          VCR.playhead = closest;
          // Only replay ~50 packets from scrub point, not entire buffer to end
          VCR.scrubEnd = Math.min(closest + 50, VCR.buffer.length);
          VCR.dragging = false;
          startReplay();
        })
        .catch(() => { VCR.dragging = false; });
    }

    timelineEl.addEventListener('mousedown', (e) => {
      VCR.dragging = true;
      stopReplay();
      scrubVisual(e.clientX);
      e.preventDefault();
    });
    document.addEventListener('mousemove', (e) => {
      if (!VCR.dragging) return;
      scrubVisual(e.clientX);
    });
    document.addEventListener('mouseup', () => {
      if (!VCR.dragging) return;
      scrubCommit(); // keeps VCR.dragging=true until fetch completes
    });
    // Touch support
    timelineEl.addEventListener('touchstart', (e) => {
      VCR.dragging = true;
      stopReplay();
      scrubVisual(e.touches[0].clientX);
      e.preventDefault();
    }, { passive: false });
    timelineEl.addEventListener('touchmove', (e) => {
      if (!VCR.dragging) return;
      scrubVisual(e.touches[0].clientX);
    });
    timelineEl.addEventListener('touchend', () => {
      if (!VCR.dragging) return;
      scrubCommit(); // keeps VCR.dragging=true until fetch completes
    });

    // Fetch historical timestamps for timeline, then start refresh
    fetchTimelineTimestamps().then(() => updateTimeline());
    setInterval(() => {
      // Re-fetch if scope changed or periodically to pick up new data
      VCR.timelineFetchedScope = 0; // force refetch
      fetchTimelineTimestamps().then(() => updateTimeline());
    }, 30000);

    // Auto-hide nav
    const topNav = document.querySelector('.top-nav');
    if (topNav) { topNav.style.position = 'fixed'; topNav.style.width = '100%'; topNav.style.zIndex = '1100'; }
    _navCleanup = { timeout: null, fn: null };
    function showNav() {
      if (topNav) topNav.classList.remove('nav-autohide');
      clearTimeout(_navCleanup.timeout);
      _navCleanup.timeout = setTimeout(() => { if (topNav) topNav.classList.add('nav-autohide'); }, 4000);
    }
    _navCleanup.fn = showNav;
    const livePage = document.querySelector('.live-page');
    if (livePage) {
      livePage.addEventListener('mousemove', showNav);
      livePage.addEventListener('touchstart', showNav);
      livePage.addEventListener('click', showNav);
    }
    showNav();
  }

  function injectSVGFilters() {
    if (document.getElementById('live-svg-filters')) return;
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.id = 'live-svg-filters';
    svg.style.cssText = 'position:absolute;width:0;height:0;';
    svg.innerHTML = `<defs><filter id="glow"><feGaussianBlur stdDeviation="3" result="blur"/><feMerge><feMergeNode in="blur"/><feMergeNode in="SourceGraphic"/></feMerge></filter></defs>`;
    document.body.appendChild(svg);
  }

  let pktTimestamps = [];
  function startRateCounter() {
    setInterval(() => {
      const now = Date.now();
      pktTimestamps = pktTimestamps.filter(t => now - t < 60000);
      const el = document.getElementById('livePktRate');
      if (el) el.textContent = pktTimestamps.length;
    }, 2000);
  }

  async function loadNodes() {
    try {
      const resp = await fetch('/api/nodes?limit=500');
      const nodes = await resp.json();
      const list = Array.isArray(nodes) ? nodes : (nodes.nodes || []);
      list.forEach(n => {
        if (n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0)) {
          nodeData[n.public_key] = n;
          addNodeMarker(n);
        }
      });
      document.getElementById('liveNodeCount').textContent = Object.keys(nodeMarkers).length;
    } catch (e) { console.error('Failed to load nodes:', e); }
  }

  function addNodeMarker(n) {
    if (nodeMarkers[n.public_key]) return nodeMarkers[n.public_key];
    const color = ROLE_COLORS[n.role] || ROLE_COLORS.unknown;
    const isRepeater = n.role === 'repeater';
    const zoom = map ? map.getZoom() : 11;
    const zoomScale = Math.max(0.4, (zoom - 8) / 6);
    const size = Math.round((isRepeater ? 6 : 4) * zoomScale);

    const glow = L.circleMarker([n.lat, n.lon], {
      radius: size + 4, fillColor: color, fillOpacity: 0.12, stroke: false, interactive: false
    }).addTo(nodesLayer);

    const marker = L.circleMarker([n.lat, n.lon], {
      radius: size, fillColor: color, fillOpacity: 0.85,
      color: '#fff', weight: isRepeater ? 1.5 : 0.5, opacity: isRepeater ? 0.6 : 0.3
    }).addTo(nodesLayer);

    marker.bindTooltip(n.name || n.public_key.slice(0, 8), {
      permanent: false, direction: 'top', offset: [0, -10], className: 'live-tooltip'
    });

    marker._glowMarker = glow;
    marker._baseColor = color;
    marker._baseSize = size;
    nodeMarkers[n.public_key] = marker;
    return marker;
  }

  function rescaleMarkers() {
    const zoom = map.getZoom();
    const zoomScale = Math.max(0.4, (zoom - 8) / 6);
    for (const [key, marker] of Object.entries(nodeMarkers)) {
      const n = nodeData[key];
      const isRepeater = n && n.role === 'repeater';
      const size = Math.round((isRepeater ? 6 : 4) * zoomScale);
      marker.setRadius(size);
      marker._baseSize = size;
      if (marker._glowMarker) marker._glowMarker.setRadius(size + 4);
    }
  }

  async function replayRecent() {
    try {
      const resp = await fetch('/api/packets?limit=8&grouped=false');
      const data = await resp.json();
      const pkts = (data.packets || []).reverse();
      pkts.forEach((pkt, i) => {
        const livePkt = dbPacketToLive(pkt);
        livePkt._ts = new Date(pkt.timestamp || pkt.created_at).getTime();
        const ts = livePkt._ts;
        VCR.buffer.push({ ts, pkt: livePkt });
        setTimeout(() => animatePacket(livePkt), i * 400);
      });
      setTimeout(updateTimeline, pkts.length * 400 + 200);
    } catch {}
  }

  function connectWS() {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = new WebSocket(`${proto}://${location.host}`);
    ws.onmessage = (e) => {
      try {
        const msg = JSON.parse(e.data);
        if (msg.type === 'packet') bufferPacket(msg.data);
      } catch {}
    };
    ws.onclose = () => setTimeout(connectWS, 3000);
    ws.onerror = () => {};
  }

  function animatePacket(pkt) {
    packetCount++;
    pktTimestamps.push(Date.now());
    document.getElementById('livePktCount').textContent = packetCount;

    const decoded = pkt.decoded || {};
    const header = decoded.header || {};
    const payload = decoded.payload || {};
    const typeName = header.payloadTypeName || 'UNKNOWN';
    const icon = PAYLOAD_ICONS[typeName] || '📦';
    const hops = decoded.path?.hops || [];
    const color = TYPE_COLORS[typeName] || '#6b7280';

    playSound(typeName);
    addFeedItem(icon, typeName, payload, hops, color, pkt);

    const hopPositions = resolveHopPositions(hops, payload);
    if (hopPositions.length === 0) return;
    if (hopPositions.length === 1) { pulseNode(hopPositions[0].key, hopPositions[0].pos, typeName); return; }
    animatePath(hopPositions, typeName, color);
  }

  function resolveHopPositions(hops, payload) {
    const known = Object.values(nodeData);
    const raw = hops.map(hop => {
      let found = null;
      for (const n of known) {
        if (n.public_key.toLowerCase().startsWith(hop.toLowerCase())) { found = n; break; }
      }
      if (found && !(found.lat === 0 && found.lon === 0)) {
        return { key: found.public_key, pos: [found.lat, found.lon], name: found.name || hop, known: true };
      }
      return { key: 'hop-' + hop, pos: null, name: hop, known: false };
    });

    if (payload.pubKey && payload.lat != null && !(payload.lat === 0 && payload.lon === 0)) {
      const existing = raw.find(p => p.key === payload.pubKey);
      if (!existing) {
        raw.unshift({ key: payload.pubKey, pos: [payload.lat, payload.lon], name: payload.name || payload.pubKey.slice(0, 8), known: true });
      }
    }

    if (!showGhostHops) return raw.filter(h => h.known);

    const knownPositions = raw.filter(h => h.known);
    if (knownPositions.length < 2) return raw.filter(h => h.known);

    for (let i = 0; i < raw.length; i++) {
      if (raw[i].known) continue;
      let before = null, after = null;
      for (let j = i - 1; j >= 0; j--) { if (raw[j].known || raw[j].pos) { before = raw[j].pos; break; } }
      for (let j = i + 1; j < raw.length; j++) { if (raw[j].known) { after = raw[j].pos; break; } }
      if (before && after) {
        let gapStart = i, gapEnd = i;
        for (let j = i - 1; j >= 0 && !raw[j].known; j--) gapStart = j;
        for (let j = i + 1; j < raw.length && !raw[j].known; j++) gapEnd = j;
        const gapSize = gapEnd - gapStart + 1;
        const t = (i - gapStart + 1) / (gapSize + 1);
        raw[i].pos = [before[0] + (after[0] - before[0]) * t, before[1] + (after[1] - before[1]) * t];
        raw[i].ghost = true;
      }
    }
    return raw.filter(h => h.pos != null);
  }

  function animatePath(hopPositions, typeName, color) {
    activeAnims++;
    document.getElementById('liveAnimCount').textContent = activeAnims;
    let hopIndex = 0;

    function nextHop() {
      if (hopIndex >= hopPositions.length) {
        activeAnims = Math.max(0, activeAnims - 1);
        document.getElementById('liveAnimCount').textContent = activeAnims;
        return;
      }
      const hp = hopPositions[hopIndex];
      const isGhost = hp.ghost;

      if (isGhost) {
        if (!nodeMarkers[hp.key]) {
          const ghost = L.circleMarker(hp.pos, {
            radius: 3, fillColor: '#94a3b8', fillOpacity: 0.35, color: '#94a3b8', weight: 1, opacity: 0.5
          }).addTo(animLayer);
          let pulseUp = true;
          const pulseTimer = setInterval(() => {
            if (!animLayer.hasLayer(ghost)) { clearInterval(pulseTimer); return; }
            ghost.setStyle({ fillOpacity: pulseUp ? 0.6 : 0.25, opacity: pulseUp ? 0.7 : 0.4 });
            pulseUp = !pulseUp;
          }, 600);
          setTimeout(() => { clearInterval(pulseTimer); if (animLayer.hasLayer(ghost)) animLayer.removeLayer(ghost); }, 3000);
        }
      } else {
        pulseNode(hp.key, hp.pos, typeName);
      }

      if (hopIndex < hopPositions.length - 1) {
        const nextPos = hopPositions[hopIndex + 1].pos;
        const nextGhost = hopPositions[hopIndex + 1].ghost;
        const lineColor = (isGhost || nextGhost) ? '#94a3b8' : color;
        const lineOpacity = (isGhost || nextGhost) ? 0.3 : undefined;
        drawAnimatedLine(hp.pos, nextPos, lineColor, () => { hopIndex++; nextHop(); }, lineOpacity);
      } else {
        if (!isGhost) pulseNode(hp.key, hp.pos, typeName);
        hopIndex++; nextHop();
      }
    }
    nextHop();
  }

  function pulseNode(key, pos, typeName) {
    if (!nodeMarkers[key]) {
      const ghost = L.circleMarker(pos, {
        radius: 5, fillColor: '#6b7280', fillOpacity: 0.3, color: '#fff', weight: 0.5, opacity: 0.2
      }).addTo(nodesLayer);
      ghost._baseColor = '#6b7280'; ghost._baseSize = 5;
      nodeMarkers[key] = ghost;
      setTimeout(() => {
        nodesLayer.removeLayer(ghost);
        if (ghost._glowMarker) nodesLayer.removeLayer(ghost._glowMarker);
        delete nodeMarkers[key];
      }, 30000);
    }

    const marker = nodeMarkers[key];
    if (!marker) return;
    const color = TYPE_COLORS[typeName] || '#6b7280';

    const ring = L.circleMarker(pos, {
      radius: 2, fillColor: 'transparent', fillOpacity: 0, color: color, weight: 3, opacity: 0.9
    }).addTo(animLayer);

    let r = 2, op = 0.9;
    const iv = setInterval(() => {
      r += 1.5; op -= 0.03;
      if (op <= 0) {
        clearInterval(iv);
        try { animLayer.removeLayer(ring); } catch {}
        return;
      }
      try {
        ring.setRadius(r);
        ring.setStyle({ opacity: op, weight: Math.max(0.3, 3 - r * 0.04) });
      } catch { clearInterval(iv); }
    }, 26);
    // Safety cleanup — never let a ring live longer than 2s
    setTimeout(() => { clearInterval(iv); try { animLayer.removeLayer(ring); } catch {} }, 2000);

    const baseColor = marker._baseColor || '#6b7280';
    const baseSize = marker._baseSize || 6;
    marker.setStyle({ fillColor: '#fff', fillOpacity: 1, radius: baseSize + 2, color: color, weight: 2 });

    if (marker._glowMarker) {
      marker._glowMarker.setStyle({ fillColor: color, fillOpacity: 0.2, radius: baseSize + 6 });
      setTimeout(() => marker._glowMarker.setStyle({ fillColor: baseColor, fillOpacity: 0.08, radius: baseSize + 3 }), 500);
    }

    setTimeout(() => marker.setStyle({ fillColor: color, fillOpacity: 0.95, radius: baseSize + 1, weight: 1.5 }), 150);
    setTimeout(() => marker.setStyle({ fillColor: baseColor, fillOpacity: 0.85, radius: baseSize, color: '#fff', weight: marker._baseSize > 6 ? 1.5 : 0.5 }), 700);

    nodeActivity[key] = (nodeActivity[key] || 0) + 1;
  }

  function drawAnimatedLine(from, to, color, onComplete, overrideOpacity) {
    const steps = 20;
    const latStep = (to[0] - from[0]) / steps;
    const lonStep = (to[1] - from[1]) / steps;
    let step = 0;
    let currentCoords = [from];
    const mainOpacity = overrideOpacity ?? 0.8;
    const isDashed = overrideOpacity != null;

    const contrail = L.polyline([from], {
      color: color, weight: 6, opacity: mainOpacity * 0.2, lineCap: 'round'
    }).addTo(pathsLayer);

    const line = L.polyline([from], {
      color: color, weight: isDashed ? 1.5 : 2, opacity: mainOpacity, lineCap: 'round',
      dashArray: isDashed ? '4 6' : null
    }).addTo(pathsLayer);

    const dot = L.circleMarker(from, {
      radius: 3.5, fillColor: '#fff', fillOpacity: 1, color: color, weight: 1.5
    }).addTo(animLayer);

    const interval = setInterval(() => {
      step++;
      const lat = from[0] + latStep * step;
      const lon = from[1] + lonStep * step;
      currentCoords.push([lat, lon]);
      line.setLatLngs(currentCoords);
      contrail.setLatLngs(currentCoords);
      dot.setLatLng([lat, lon]);

      if (step >= steps) {
        clearInterval(interval);
        animLayer.removeLayer(dot);

        recentPaths.push({ line, glowLine: contrail, time: Date.now() });
        while (recentPaths.length > 5) {
          const old = recentPaths.shift();
          pathsLayer.removeLayer(old.line);
          pathsLayer.removeLayer(old.glowLine);
        }

        setTimeout(() => {
          let fadeOp = mainOpacity;
          const fi = setInterval(() => {
            fadeOp -= 0.1;
            if (fadeOp <= 0) {
              clearInterval(fi);
              pathsLayer.removeLayer(line);
              pathsLayer.removeLayer(contrail);
              recentPaths = recentPaths.filter(p => p.line !== line);
            } else {
              line.setStyle({ opacity: fadeOp });
              contrail.setStyle({ opacity: fadeOp * 0.15 });
            }
          }, 52);
        }, 800);

        if (onComplete) onComplete();
      }
    }, 33);
  }

  function showHeatMap() {
    if (heatLayer) { map.removeLayer(heatLayer); heatLayer = null; }
    const points = [];
    Object.values(nodeData).forEach(n => {
      points.push([n.lat, n.lon, nodeActivity[n.public_key] || 1]);
    });
    for (const [key, count] of Object.entries(nodeActivity)) {
      const marker = nodeMarkers[key];
      if (marker && !nodeData[key]) {
        const ll = marker.getLatLng();
        points.push([ll.lat, ll.lng, count]);
      }
    }
    if (points.length && typeof L.heatLayer === 'function') {
      heatLayer = L.heatLayer(points, {
        radius: 25, blur: 15, maxZoom: 14, minOpacity: 0.3,
        gradient: { 0.2: '#0d47a1', 0.4: '#1565c0', 0.6: '#42a5f5', 0.8: '#ffca28', 1.0: '#ff5722' }
      }).addTo(map);
    }
  }

  function hideHeatMap() {
    if (heatLayer) { map.removeLayer(heatLayer); heatLayer = null; }
  }

  function addFeedItem(icon, typeName, payload, hops, color, pkt) {
    const feed = document.getElementById('liveFeed');
    if (!feed) return;

    const text = payload.text || payload.name || '';
    const preview = text ? ' ' + (text.length > 35 ? text.slice(0, 35) + '…' : text) : '';
    const hopStr = hops.length ? `<span class="feed-hops">${hops.length}⇢</span>` : '';

    const item = document.createElement('div');
    item.className = 'live-feed-item live-feed-enter';
    item.style.cursor = 'pointer';
    item.innerHTML = `
      <span class="feed-icon" style="color:${color}">${icon}</span>
      <span class="feed-type" style="color:${color}">${typeName}</span>
      ${hopStr}
      <span class="feed-text">${escapeHtml(preview)}</span>
      <span class="feed-time">${new Date(pkt._ts || Date.now()).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit',second:'2-digit'})}</span>
    `;
    item.addEventListener('click', () => showFeedCard(item, pkt, color));
    feed.prepend(item);
    requestAnimationFrame(() => { requestAnimationFrame(() => item.classList.remove('live-feed-enter')); });
    while (feed.children.length > 25) feed.removeChild(feed.lastChild);
  }

  function showFeedCard(anchor, pkt, color) {
    document.querySelector('.feed-detail-card')?.remove();
    const decoded = pkt.decoded || {};
    const header = decoded.header || {};
    const payload = decoded.payload || {};
    const hops = decoded.path?.hops || [];
    const typeName = header.payloadTypeName || 'UNKNOWN';
    const text = payload.text || '';
    const sender = payload.name || payload.sender || payload.senderName || '';
    const channel = payload.channelName || (payload.channelHash != null ? 'Ch ' + payload.channelHash : '');
    const snr = pkt.SNR ?? pkt.snr ?? null;
    const rssi = pkt.RSSI ?? pkt.rssi ?? null;
    const observer = pkt.observer_name || pkt.observer || '';
    const pktId = pkt.id || '';

    const card = document.createElement('div');
    card.className = 'feed-detail-card';
    card.innerHTML = `
      <div class="fdc-header" style="border-left:3px solid ${color}">
        <strong>${typeName}</strong>
        ${sender ? `<span class="fdc-sender">${escapeHtml(sender)}</span>` : ''}
        <button class="fdc-close">✕</button>
      </div>
      ${text ? `<div class="fdc-text">${escapeHtml(text.length > 120 ? text.slice(0, 120) + '…' : text)}</div>` : ''}
      <div class="fdc-meta">
        ${channel ? `<span>📻 ${escapeHtml(channel)}</span>` : ''}
        ${hops.length ? `<span>🔀 ${hops.length} hops</span>` : ''}
        ${snr != null ? `<span>📶 ${Number(snr).toFixed(1)} dB</span>` : ''}
        ${rssi != null ? `<span>📡 ${rssi} dBm</span>` : ''}
        ${observer ? `<span>👁 ${escapeHtml(observer)}</span>` : ''}
      </div>
      ${pktId ? `<a class="fdc-link" href="#/packets/id/${pktId}">View in packets →</a>` : ''}
      <button class="fdc-replay">↻ Replay</button>
    `;
    card.querySelector('.fdc-close').addEventListener('click', (e) => { e.stopPropagation(); card.remove(); });
    card.querySelector('.fdc-replay').addEventListener('click', (e) => { e.stopPropagation(); animatePacket(pkt); });
    document.addEventListener('click', function dismiss(e) {
      if (!card.contains(e.target) && !anchor.contains(e.target)) { card.remove(); document.removeEventListener('click', dismiss); }
    });
    const feedEl = document.getElementById('liveFeed');
    if (feedEl) feedEl.parentElement.appendChild(card);
  }

  function escapeHtml(s) {
    return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
  }

  function destroy() {
    stopReplay();
    if (ws) { ws.onclose = null; ws.close(); ws = null; }
    if (map) { map.remove(); map = null; }
    if (_onResize) { window.removeEventListener('resize', _onResize); window.removeEventListener('orientationchange', _onResize); }
    const topNav = document.querySelector('.top-nav');
    if (topNav) { topNav.classList.remove('nav-autohide'); topNav.style.position = ''; topNav.style.width = ''; topNav.style.zIndex = ''; }
    if (_navCleanup) {
      clearTimeout(_navCleanup.timeout);
      const livePage = document.querySelector('.live-page');
      if (livePage && _navCleanup.fn) {
        livePage.removeEventListener('mousemove', _navCleanup.fn);
        livePage.removeEventListener('touchstart', _navCleanup.fn);
        livePage.removeEventListener('click', _navCleanup.fn);
      }
      _navCleanup = null;
    }
    nodesLayer = pathsLayer = animLayer = heatLayer = null;
    nodeMarkers = {}; nodeData = {};
    recentPaths = [];
    packetCount = 0; activeAnims = 0;
    nodeActivity = {}; pktTimestamps = [];
    VCR.buffer = []; VCR.playhead = -1; VCR.mode = 'LIVE'; VCR.missedCount = 0; VCR.speed = 1;
  }

  registerPage('live', { init, destroy });
})();
