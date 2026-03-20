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
  let _timelineRefreshInterval = null;
  let _lcdClockInterval = null;
  let _rateCounterInterval = null;

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
      resizeTimer = setTimeout(() => {
        // Set live-page height from JS — most reliable across all mobile browsers
        const page = document.querySelector('.live-page');
        const appEl = document.getElementById('app');
        const h = window.innerHeight;
        if (page) page.style.height = h + 'px';
        if (appEl) appEl.style.height = h + 'px';
        if (map) {
          map.invalidateSize({ animate: false, pan: false });
        }
      }, 50);
    };
    // Run immediately to set correct initial height
    _onResize();
    window.addEventListener('resize', _onResize);
    window.addEventListener('orientationchange', () => {
      // Orientation change is async — viewport dimensions settle late
      [50, 200, 500, 1000, 2000].forEach(ms => setTimeout(_onResize, ms));
    });
    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', _onResize);
    }
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
    VCR.scrubTs = null;
    vcrSetMode('LIVE');
    // Reload all nodes (no time filter)
    clearNodeMarkers();
    loadNodes();
    const prompt = document.getElementById('vcrPrompt');
    if (prompt) prompt.classList.add('hidden');
  }

  function vcrUnpause() {
    if (VCR.mode !== 'PAUSED') return;
    if (VCR.scrubTs != null) {
      vcrReplayFromTs(VCR.scrubTs);
    } else {
      vcrResumeLive();
    }
  }

  function vcrReplayFromTs(targetTs) {
    const fetchFrom = new Date(targetTs).toISOString();
    stopReplay();
    vcrSetMode('REPLAY');

    // Reload map nodes to match the replay time
    clearNodeMarkers();
    loadNodes(targetTs);

    // Fetch packets from scrub point forward (ASC order, no limit clipping from the wrong end)
    fetch(`/api/packets?limit=10000&grouped=false&since=${encodeURIComponent(fetchFrom)}&order=asc`)
      .then(r => r.json())
      .then(data => {
        const pkts = data.packets || []; // already ASC from server
        const replayEntries = pkts.map(p => ({
          ts: new Date(p.timestamp || p.created_at).getTime(),
          pkt: dbPacketToLive(p)
        }));
        if (replayEntries.length === 0) {
          vcrSetMode('PAUSED');
          return;
        }
        VCR.buffer = replayEntries;
        VCR.playhead = 0;
        VCR.scrubEnd = null;
        VCR.scrubTs = null;
        VCR.dragPct = null;
        startReplay();
      })
      .catch(() => { vcrResumeLive(); });
  }

  function showVCRPrompt(count) {
    const prompt = document.getElementById('vcrPrompt');
    if (!prompt) return;
    prompt.setAttribute('role', 'alertdialog');
    prompt.setAttribute('aria-label', 'Missed packets prompt');
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
    // Focus first button for keyboard users (#59)
    document.getElementById('vcrPromptReplay').focus();
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
    fetch(`/api/packets?limit=2000&grouped=false&since=${encodeURIComponent(from)}`)
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
      if (VCR.playhead >= VCR.buffer.length) {
        // Try to fetch the next page before going live
        fetchNextReplayPage().then(hasMore => {
          if (hasMore) tick();
          else vcrResumeLive();
        });
        return;
      }
      const entry = VCR.buffer[VCR.playhead];
      animatePacket(entry.pkt);
      updateVCRClock(entry.ts);
      updateVCRLcd();
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

  function fetchNextReplayPage() {
    // Get timestamp of last packet in buffer to fetch the next page
    const last = VCR.buffer[VCR.buffer.length - 1];
    if (!last) return Promise.resolve(false);
    const since = new Date(last.ts + 1).toISOString(); // +1ms to avoid dupe
    return fetch(`/api/packets?limit=10000&grouped=false&since=${encodeURIComponent(since)}&order=asc`)
      .then(r => r.json())
      .then(data => {
        const pkts = data.packets || [];
        if (pkts.length === 0) return false;
        const newEntries = pkts.map(p => ({
          ts: new Date(p.timestamp || p.created_at).getTime(),
          pkt: dbPacketToLive(p)
        }));
        // Append to buffer, playhead stays where it is (at the end, about to read new entries)
        VCR.buffer = VCR.buffer.concat(newEntries);
        return true;
      })
      .catch(() => false);
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

  // 7-segment LCD renderer
  const SEG_MAP = {
    '0':0x7E,'1':0x30,'2':0x6D,'3':0x79,'4':0x33,'5':0x5B,'6':0x5F,'7':0x70,
    '8':0x7F,'9':0x7B,'-':0x01,':':0x80,' ':0x00,'P':0x67,'A':0x77,'U':0x3E,
    'S':0x5B,'E':0x4F,'L':0x0E,'I':0x30,'V':0x3E,'+':0x01
  };
  function drawSegDigit(ctx, x, y, w, h, bits, color) {
    const t = Math.max(2, h * 0.12); // segment thickness
    const g = 1; // gap
    const hw = w - 2*g, hh = (h - 3*g) / 2;
    ctx.fillStyle = color;
    // a=top, b=top-right, c=bot-right, d=bot, e=bot-left, f=top-left, g=mid
    if (bits & 0x40) ctx.fillRect(x+g+t/2, y, hw-t, t);           // a
    if (bits & 0x20) ctx.fillRect(x+w-t, y+g+t/2, t, hh-t/2);     // b
    if (bits & 0x10) ctx.fillRect(x+w-t, y+hh+2*g+t/2, t, hh-t/2);// c
    if (bits & 0x08) ctx.fillRect(x+g+t/2, y+h-t, hw-t, t);       // d
    if (bits & 0x04) ctx.fillRect(x, y+hh+2*g+t/2, t, hh-t/2);    // e
    if (bits & 0x02) ctx.fillRect(x, y+g+t/2, t, hh-t/2);         // f
    if (bits & 0x01) ctx.fillRect(x+g+t/2, y+hh+g-t/2, hw-t, t);  // g
    // colon
    if (bits & 0x80) {
      const r = t * 0.6;
      ctx.beginPath(); ctx.arc(x+w/2, y+h*0.33, r, 0, Math.PI*2); ctx.fill();
      ctx.beginPath(); ctx.arc(x+w/2, y+h*0.67, r, 0, Math.PI*2); ctx.fill();
    }
  }
  function drawLcdText(text, color) {
    const canvas = document.getElementById('vcrLcdCanvas');
    if (!canvas) return;
    canvas.setAttribute('aria-label', 'VCR time: ' + text);
    const ctx = canvas.getContext('2d');
    const dpr = window.devicePixelRatio || 1;
    const cw = canvas.offsetWidth, ch = canvas.offsetHeight;
    canvas.width = cw * dpr; canvas.height = ch * dpr;
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, cw, ch);
    
    const digitW = Math.min(16, (cw - 10) / text.length);
    const digitH = ch - 4;
    const totalW = digitW * text.length;
    let x = (cw - totalW) / 2;
    const y = 2;
    
    // Draw ghost segments (dim background) — hardcoded to match LCD green
    const ghostColor = 'rgba(74,222,128,0.07)';
    for (let i = 0; i < text.length; i++) {
      const ch2 = text[i];
      if (ch2 === ':') {
        drawSegDigit(ctx, x, y, digitW * 0.5, digitH, 0x80, ghostColor);
        x += digitW * 0.5;
      } else {
        drawSegDigit(ctx, x, y, digitW, digitH, 0x7F, ghostColor);
        x += digitW + 1;
      }
    }
    // Draw active segments
    x = (cw - totalW) / 2;
    for (let i = 0; i < text.length; i++) {
      const ch2 = text[i];
      const bits = SEG_MAP[ch2] || 0;
      if (ch2 === ':') {
        drawSegDigit(ctx, x, y, digitW * 0.5, digitH, bits, color);
        x += digitW * 0.5;
      } else {
        drawSegDigit(ctx, x, y, digitW, digitH, bits, color);
        x += digitW + 1;
      }
    }
  }

  function updateVCRClock(tsMs) {
    const d = new Date(tsMs);
    const hh = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    const ss = String(d.getSeconds()).padStart(2, '0');
    drawLcdText(`${hh}:${mm}:${ss}`, '#4ade80');
  }

  function updateVCRLcd() {
    const modeEl = document.getElementById('vcrLcdMode');
    const pktsEl = document.getElementById('vcrLcdPkts');
    if (modeEl) {
      if (VCR.mode === 'LIVE') modeEl.textContent = 'LIVE';
      else if (VCR.mode === 'PAUSED') modeEl.textContent = 'PAUSE';
      else if (VCR.mode === 'REPLAY') modeEl.textContent = `PLAY ${VCR.speed}x`;
    }
    if (pktsEl) {
      if (VCR.mode === 'PAUSED' && VCR.missedCount > 0) {
        pktsEl.textContent = `+${VCR.missedCount} PKTS`;
      } else {
        pktsEl.textContent = '';
      }
    }
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
      if (pauseBtn) { pauseBtn.textContent = '⏸'; pauseBtn.setAttribute('aria-label', 'Pause'); }
      if (missedEl) missedEl.classList.add('hidden');
      updateVCRClock(Date.now());
    } else if (VCR.mode === 'PAUSED') {
      modeEl.textContent = '⏸ PAUSED';
      modeEl.className = 'vcr-mode vcr-mode-paused';
      if (pauseBtn) { pauseBtn.textContent = '▶'; pauseBtn.setAttribute('aria-label', 'Play'); }
      if (missedEl && VCR.missedCount > 0) {
        missedEl.textContent = `+${VCR.missedCount}`;
        missedEl.classList.remove('hidden');
      }
    } else if (VCR.mode === 'REPLAY') {
      modeEl.textContent = `⏪ REPLAY`;
      modeEl.className = 'vcr-mode vcr-mode-replay';
      if (pauseBtn) { pauseBtn.textContent = '⏸'; pauseBtn.setAttribute('aria-label', 'Pause'); }
      if (missedEl) missedEl.classList.add('hidden');
    }
    if (speedBtn) { speedBtn.textContent = VCR.speed + 'x'; speedBtn.setAttribute('aria-label', 'Speed ' + VCR.speed + 'x'); }
    updateVCRLcd();
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
    // Keep buffer capped at ~2000 — adjust playhead to avoid stale indices (#63)
    if (VCR.buffer.length > 2000) {
      const trimCount = 500;
      VCR.buffer.splice(0, trimCount);
      if (VCR.playhead >= 0) {
        VCR.playhead = Math.max(0, VCR.playhead - trimCount);
      }
    }

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
    } else if (VCR.scrubTs != null) {
      // Scrubbed to a specific time — hold there
      x = ((VCR.scrubTs - startTs) / scopeMs) * cw;
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
            <label><input type="checkbox" id="liveHeatToggle" checked aria-describedby="heatDesc"> Heat</label>
            <span id="heatDesc" class="sr-only">Overlay a density heat map on the mesh nodes</span>
            <label><input type="checkbox" id="liveGhostToggle" checked aria-describedby="ghostDesc"> Ghosts</label>
            <span id="ghostDesc" class="sr-only">Show interpolated ghost markers for unknown hops</span>
          </div>
        </div>
        <div class="live-overlay live-feed" id="liveFeed">
          <button class="feed-hide-btn" id="feedHideBtn" title="Hide feed">✕</button>
        </div>
        <button class="feed-show-btn hidden" id="feedShowBtn" title="Show feed">📋</button>
        <button class="legend-toggle-btn hidden" id="legendToggleBtn" aria-label="Show legend" title="Show legend">🎨</button>
        <div class="live-overlay live-legend" id="liveLegend" role="region" aria-label="Map legend">
          <h3 class="legend-title">PACKET TYPES</h3>
          <ul class="legend-list">
            <li><span class="live-dot" style="background:#22c55e" aria-hidden="true"></span> Advert — Node advertisement</li>
            <li><span class="live-dot" style="background:#3b82f6" aria-hidden="true"></span> Message — Group text</li>
            <li><span class="live-dot" style="background:#f59e0b" aria-hidden="true"></span> Direct — Direct message</li>
            <li><span class="live-dot" style="background:#a855f7" aria-hidden="true"></span> Request — Data request</li>
            <li><span class="live-dot" style="background:#ec4899" aria-hidden="true"></span> Trace — Route trace</li>
          </ul>
          <h3 class="legend-title" style="margin-top:8px">NODE ROLES</h3>
          <ul class="legend-list">
            <li><span class="live-dot" style="background:#3b82f6" aria-hidden="true"></span> Repeater</li>
            <li><span class="live-dot" style="background:#06b6d4" aria-hidden="true"></span> Companion</li>
            <li><span class="live-dot" style="background:#a855f7" aria-hidden="true"></span> Room</li>
            <li><span class="live-dot" style="background:#f59e0b" aria-hidden="true"></span> Sensor</li>
          </ul>
        </div>

        <!-- VCR Bar -->
        <div class="vcr-bar" id="vcrBar">
          <div class="vcr-controls">
            <button id="vcrRewindBtn" class="vcr-btn" title="Rewind" aria-label="Rewind">⏪</button>
            <button id="vcrPauseBtn" class="vcr-btn" title="Pause/Play" aria-label="Pause">⏸</button>
            <button id="vcrLiveBtn" class="vcr-btn vcr-live-btn" title="Jump to live" aria-label="Snap to Live">LIVE</button>
            <button id="vcrSpeedBtn" class="vcr-btn" title="Playback speed" aria-label="Speed 1x">1x</button>
            <div id="vcrMode" class="vcr-mode vcr-mode-live"><span class="vcr-live-dot"></span> LIVE</div>
          </div>
          <div class="vcr-scope-btns" role="radiogroup" aria-label="Timeline scope">
            <button class="vcr-scope-btn active" data-scope="3600000" role="radio" aria-checked="true" aria-label="Scope 1 hour">1h</button>
            <button class="vcr-scope-btn" data-scope="21600000" role="radio" aria-checked="false" aria-label="Scope 6 hours">6h</button>
            <button class="vcr-scope-btn" data-scope="43200000" role="radio" aria-checked="false" aria-label="Scope 12 hours">12h</button>
            <button class="vcr-scope-btn" data-scope="86400000" role="radio" aria-checked="false" aria-label="Scope 24 hours">24h</button>
          </div>
          <div class="vcr-timeline-container">
            <canvas id="vcrTimeline" class="vcr-timeline"></canvas>
            <div id="vcrPlayhead" class="vcr-playhead"></div>
            <div id="vcrTimeTooltip" class="vcr-time-tooltip hidden"></div>
          </div>
          <div class="vcr-lcd">
            <div class="vcr-lcd-row vcr-lcd-mode" id="vcrLcdMode">LIVE</div>
            <canvas id="vcrLcdCanvas" class="vcr-lcd-canvas" width="200" height="32" role="img" aria-label="VCR time display"></canvas>
            <div class="vcr-lcd-row vcr-lcd-pkts" id="vcrLcdPkts"></div>
          </div>
          <div id="vcrPrompt" class="vcr-prompt hidden"></div>
        </div>
      </div>`;

    map = L.map('liveMap', {
      zoomControl: false, attributionControl: false,
      zoomAnimation: true, markerZoomAnimation: true
    }).setView([37.45, -122.0], 9);

    const isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
    const DARK_TILES = 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png';
    const LIGHT_TILES = 'https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png';
    let tileLayer = L.tileLayer(isDark ? DARK_TILES : LIGHT_TILES, { maxZoom: 19 }).addTo(map);

    // Swap tiles when theme changes
    const _themeObs = new MutationObserver(function () {
      const dark = document.documentElement.getAttribute('data-theme') === 'dark' ||
        (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
      tileLayer.setUrl(dark ? DARK_TILES : LIGHT_TILES);
    });
    _themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
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
    // Keyboard support for feed items (event delegation)
    feedEl.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        const item = e.target.closest('.live-feed-item');
        if (item) { e.preventDefault(); item.click(); }
      }
    });
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

    // Legend toggle for mobile (#60)
    const legendEl = document.getElementById('liveLegend');
    const legendToggleBtn = document.getElementById('legendToggleBtn');
    if (legendToggleBtn && legendEl) {
      legendToggleBtn.addEventListener('click', () => {
        const isVisible = legendEl.classList.toggle('legend-mobile-visible');
        legendToggleBtn.setAttribute('aria-label', isVisible ? 'Hide legend' : 'Show legend');
        legendToggleBtn.textContent = isVisible ? '✕' : '🎨';
      });
    }

    // Feed panel resize handle (#27)
    const savedFeedWidth = localStorage.getItem('live-feed-width');
    if (savedFeedWidth) feedEl.style.width = savedFeedWidth + 'px';
    const resizeHandle = document.createElement('div');
    resizeHandle.className = 'feed-resize-handle';
    resizeHandle.setAttribute('aria-label', 'Resize feed panel');
    feedEl.appendChild(resizeHandle);
    let feedResizing = false;
    resizeHandle.addEventListener('mousedown', (e) => {
      feedResizing = true; e.preventDefault();
    });
    document.addEventListener('mousemove', (e) => {
      if (!feedResizing) return;
      const newWidth = Math.max(200, Math.min(800, e.clientX - feedEl.getBoundingClientRect().left));
      feedEl.style.width = newWidth + 'px';
    });
    document.addEventListener('mouseup', () => {
      if (!feedResizing) return;
      feedResizing = false;
      localStorage.setItem('live-feed-width', parseInt(feedEl.style.width));
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
        document.querySelectorAll('.vcr-scope-btn').forEach(b => { b.classList.remove('active'); b.setAttribute('aria-checked', 'false'); });
        btn.classList.add('active');
        btn.setAttribute('aria-checked', 'true');
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

    // Touch tooltip for timeline (#19)
    timelineEl.addEventListener('touchmove', (e) => {
      if (!VCR.dragging) return;
      const touch = e.touches[0];
      const rect = timelineEl.getBoundingClientRect();
      const pct = Math.max(0, Math.min(1, (touch.clientX - rect.left) / rect.width));
      const ts = Date.now() - VCR.timelineScope + pct * VCR.timelineScope;
      const d = new Date(ts);
      timeTooltip.textContent = d.toLocaleTimeString([], {hour:'2-digit',minute:'2-digit',second:'2-digit'});
      timeTooltip.style.left = (touch.clientX - rect.left) + 'px';
      timeTooltip.classList.remove('hidden');
    });
    timelineEl.addEventListener('touchend', () => { timeTooltip.classList.add('hidden'); });

    // Drag scrubbing on timeline
    VCR.dragging = false;
    VCR.dragPct = 0;

    function scrubVisual(clientX) {
      const rect = timelineEl.getBoundingClientRect();
      VCR.dragPct = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
      const playheadEl = document.getElementById('vcrPlayhead');
      if (playheadEl) playheadEl.style.left = (VCR.dragPct * rect.width) + 'px';
      const now = VCR.frozenNow || Date.now();
      const targetTs = now - VCR.timelineScope + VCR.dragPct * VCR.timelineScope;
      updateVCRClock(targetTs);
    }

    function scrubRelease() {
      VCR.dragging = false;
      VCR.frozenNow = Date.now();
      const targetTs = VCR.frozenNow - VCR.timelineScope + VCR.dragPct * VCR.timelineScope;
      VCR.scrubTs = targetTs;
      updateVCRClock(targetTs);
      vcrReplayFromTs(targetTs);
    }

    timelineEl.addEventListener('mousedown', (e) => {
      VCR.dragging = true;
      VCR.scrubTs = null;
      stopReplay();
      if (!VCR.frozenNow) VCR.frozenNow = Date.now();
      scrubVisual(e.clientX);
      e.preventDefault();
    });
    document.addEventListener('mousemove', (e) => {
      if (!VCR.dragging) return;
      scrubVisual(e.clientX);
    });
    document.addEventListener('mouseup', () => {
      if (!VCR.dragging) return;
      scrubRelease();
    });
    timelineEl.addEventListener('touchstart', (e) => {
      VCR.dragging = true;
      VCR.scrubTs = null;
      stopReplay();
      if (!VCR.frozenNow) VCR.frozenNow = Date.now();
      scrubVisual(e.touches[0].clientX);
      e.preventDefault();
    }, { passive: false });
    timelineEl.addEventListener('touchmove', (e) => {
      if (!VCR.dragging) return;
      scrubVisual(e.touches[0].clientX);
    });
    timelineEl.addEventListener('touchend', () => {
      if (!VCR.dragging) return;
      scrubRelease();
    });

    // Fetch historical timestamps for timeline, then start refresh
    fetchTimelineTimestamps().then(() => updateTimeline());
    _timelineRefreshInterval = setInterval(() => {
      VCR.timelineFetchedScope = 0; // force refetch
      fetchTimelineTimestamps().then(() => updateTimeline());
    }, 30000);

    // Live clock tick — update LCD every second when in LIVE mode
    _lcdClockInterval = setInterval(() => {
      if (VCR.mode === 'LIVE') updateVCRClock(Date.now());
    }, 1000);

    // Auto-hide nav with pin toggle (#62)
    const topNav = document.querySelector('.top-nav');
    if (topNav) { topNav.style.position = 'fixed'; topNav.style.width = '100%'; topNav.style.zIndex = '1100'; }
    _navCleanup = { timeout: null, fn: null, pinned: false };
    // Add pin button to nav
    if (topNav) {
      const pinBtn = document.createElement('button');
      pinBtn.id = 'navPinBtn';
      pinBtn.className = 'nav-pin-btn';
      pinBtn.setAttribute('aria-label', 'Pin navigation open');
      pinBtn.setAttribute('title', 'Pin navigation open');
      pinBtn.textContent = '📌';
      pinBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        _navCleanup.pinned = !_navCleanup.pinned;
        pinBtn.classList.toggle('pinned', _navCleanup.pinned);
        pinBtn.setAttribute('aria-pressed', _navCleanup.pinned);
        if (_navCleanup.pinned) {
          clearTimeout(_navCleanup.timeout);
          topNav.classList.remove('nav-autohide');
        } else {
          _navCleanup.timeout = setTimeout(() => { topNav.classList.add('nav-autohide'); }, 4000);
        }
      });
      topNav.appendChild(pinBtn);
    }
    function showNav() {
      if (topNav) topNav.classList.remove('nav-autohide');
      clearTimeout(_navCleanup.timeout);
      if (!_navCleanup.pinned) {
        _navCleanup.timeout = setTimeout(() => { if (topNav) topNav.classList.add('nav-autohide'); }, 4000);
      }
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
    _rateCounterInterval = setInterval(() => {
      const now = Date.now();
      pktTimestamps = pktTimestamps.filter(t => now - t < 60000);
      const el = document.getElementById('livePktRate');
      if (el) el.textContent = pktTimestamps.length;
    }, 2000);
  }

  async function loadNodes(beforeTs) {
    try {
      const url = beforeTs
        ? `/api/nodes?limit=2000&before=${encodeURIComponent(new Date(beforeTs).toISOString())}`
        : '/api/nodes?limit=2000';
      const resp = await fetch(url);
      const nodes = await resp.json();
      const list = Array.isArray(nodes) ? nodes : (nodes.nodes || []);
      list.forEach(n => {
        if (n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0)) {
          nodeData[n.public_key] = n;
          addNodeMarker(n);
        }
      });
      const _el2 = document.getElementById('liveNodeCount'); if (_el2) _el2.textContent = Object.keys(nodeMarkers).length;
    } catch (e) { console.error('Failed to load nodes:', e); }
  }

  function clearNodeMarkers() {
    if (nodesLayer) nodesLayer.clearLayers();
    if (animLayer) animLayer.clearLayers();
    nodeMarkers = {};
    nodeData = {};
    nodeActivity = {};
    if (heatLayer) { map.removeLayer(heatLayer); heatLayer = null; }
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
    const _el = document.getElementById('livePktCount'); if (_el) _el.textContent = packetCount;

    const decoded = pkt.decoded || {};
    const header = decoded.header || {};
    const payload = decoded.payload || {};
    const typeName = header.payloadTypeName || 'UNKNOWN';
    const icon = PAYLOAD_ICONS[typeName] || '📦';
    const hops = decoded.path?.hops || [];
    const color = TYPE_COLORS[typeName] || '#6b7280';

    playSound(typeName);
    addFeedItem(icon, typeName, payload, hops, color, pkt);

    // If ADVERT, ensure node appears on map
    if (typeName === 'ADVERT' && payload.pubKey) {
      const key = payload.pubKey;
      if (!nodeMarkers[key] && payload.lat != null && payload.lon != null && !(payload.lat === 0 && payload.lon === 0)) {
        const n = { public_key: key, name: payload.name || key.slice(0,8), role: payload.role || 'unknown', lat: payload.lat, lon: payload.lon };
        nodeData[key] = n;
        addNodeMarker(n);
        const _el2 = document.getElementById('liveNodeCount'); if (_el2) _el2.textContent = Object.keys(nodeMarkers).length;
      }
    }

    const hopPositions = resolveHopPositions(hops, payload);
    if (hopPositions.length === 0) return;
    if (hopPositions.length === 1) { pulseNode(hopPositions[0].key, hopPositions[0].pos, typeName); return; }
    animatePath(hopPositions, typeName, color);
  }

  function resolveHopPositions(hops, payload) {
    const known = Object.values(nodeData);
    
    // First pass: find all candidates per hop
    const raw = hops.map(hop => {
      const hopLower = hop.toLowerCase();
      const candidates = known.filter(n => 
        n.public_key.toLowerCase().startsWith(hopLower) &&
        n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0)
      );
      if (candidates.length === 1) {
        return { key: candidates[0].public_key, pos: [candidates[0].lat, candidates[0].lon], name: candidates[0].name || hop, known: true };
      } else if (candidates.length > 1) {
        return { key: 'ambig-' + hop, pos: null, name: hop, known: false, candidates };
      }
      return { key: 'hop-' + hop, pos: null, name: hop, known: false };
    });

    // Add sender position if available
    if (payload.pubKey && payload.lat != null && !(payload.lat === 0 && payload.lon === 0)) {
      const existing = raw.find(p => p.key === payload.pubKey);
      if (!existing) {
        raw.unshift({ key: payload.pubKey, pos: [payload.lat, payload.lon], name: payload.name || payload.pubKey.slice(0, 8), known: true });
      }
    }

    // Sequential disambiguation: each hop nearest to previous (like server-side)
    const dist = (lat1, lon1, lat2, lon2) => Math.sqrt((lat1 - lat2) ** 2 + (lon1 - lon2) ** 2);

    // Forward pass: resolve ambiguous hops using previous hop's position
    let lastPos = null;
    for (const hop of raw) {
      if (hop.known && hop.pos) { lastPos = hop.pos; continue; }
      if (!hop.candidates) continue;
      if (lastPos) {
        hop.candidates.sort((a, b) => dist(a.lat, a.lon, lastPos[0], lastPos[1]) - dist(b.lat, b.lon, lastPos[0], lastPos[1]));
      }
      const best = hop.candidates[0];
      hop.key = best.public_key; hop.pos = [best.lat, best.lon];
      hop.name = best.name || best.public_key.slice(0, 8);
      hop.known = true; lastPos = hop.pos;
    }

    // Backward pass: catch any remaining from the tail
    let nextPos = null;
    for (let i = raw.length - 1; i >= 0; i--) {
      const hop = raw[i];
      if (hop.known && hop.pos) { nextPos = hop.pos; continue; }
      if (!hop.candidates || !nextPos) continue;
      hop.candidates.sort((a, b) => dist(a.lat, a.lon, nextPos[0], nextPos[1]) - dist(b.lat, b.lon, nextPos[0], nextPos[1]));
      const best = hop.candidates[0];
      hop.key = best.public_key; hop.pos = [best.lat, best.lon];
      hop.name = best.name || best.public_key.slice(0, 8);
      hop.known = true; nextPos = hop.pos;
    }

    // Sanity check: drop hops that are impossibly far from both neighbors (>200km ≈ 1.8°)
    // These are almost certainly 1-byte prefix collisions with distant nodes
    const MAX_HOP_DIST = 1.8;
    for (let i = 0; i < raw.length; i++) {
      if (!raw[i].known || !raw[i].pos) continue;
      const prev = i > 0 && raw[i-1].known && raw[i-1].pos ? raw[i-1].pos : null;
      const next = i < raw.length-1 && raw[i+1].known && raw[i+1].pos ? raw[i+1].pos : null;
      if (!prev && !next) continue; // lone hop, keep it
      const dPrev = prev ? dist(raw[i].pos[0], raw[i].pos[1], prev[0], prev[1]) : 0;
      const dNext = next ? dist(raw[i].pos[0], raw[i].pos[1], next[0], next[1]) : 0;
      if ((prev && dPrev > MAX_HOP_DIST) && (next && dNext > MAX_HOP_DIST)) {
        raw[i].known = false; raw[i].pos = null; // too far from both neighbors
      } else if (prev && !next && dPrev > MAX_HOP_DIST) {
        raw[i].known = false; raw[i].pos = null; // first/last with only one neighbor, too far
      } else if (!prev && next && dNext > MAX_HOP_DIST) {
        raw[i].known = false; raw[i].pos = null;
      }
    }

    if (!showGhostHops) return raw.filter(h => h.known);

    const knownPos2 = raw.filter(h => h.known);
    if (knownPos2.length < 2) return raw.filter(h => h.known);

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
    item.setAttribute('tabindex', '0');
    item.setAttribute('role', 'button');
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

  function destroy() {
    stopReplay();
    if (_timelineRefreshInterval) { clearInterval(_timelineRefreshInterval); _timelineRefreshInterval = null; }
    if (_lcdClockInterval) { clearInterval(_lcdClockInterval); _lcdClockInterval = null; }
    if (_rateCounterInterval) { clearInterval(_rateCounterInterval); _rateCounterInterval = null; }
    if (ws) { ws.onclose = null; ws.close(); ws = null; }
    if (map) { map.remove(); map = null; }
    if (_onResize) {
      window.removeEventListener('resize', _onResize);
      window.removeEventListener('orientationchange', _onResize);
      if (window.visualViewport) window.visualViewport.removeEventListener('resize', _onResize);
    }
    // Restore #app height to CSS default
    const appEl = document.getElementById('app');
    if (appEl) appEl.style.height = '';
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
