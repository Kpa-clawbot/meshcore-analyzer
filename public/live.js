(function() {
  'use strict';

  // Status color helpers (read from CSS variables for theme support)
  function cssVar(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
  function statusGreen() { return cssVar('--status-green') || '#22c55e'; }

  let map, ws, nodesLayer, pathsLayer, animLayer, heatLayer, geoFilterLayer;
  let nodeMarkers = {};
  let nodeData = {};
  let packetCount = 0;
  let activeAnims = 0;
  let nodeActivity = {};
  let recentPaths = [];
  let showGhostHops = localStorage.getItem('live-ghost-hops') !== 'false';
  let realisticPropagation = localStorage.getItem('live-realistic-propagation') === 'true';
  let showOnlyFavorites = localStorage.getItem('live-favorites-only') === 'true';
  let matrixMode = localStorage.getItem('live-matrix-mode') === 'true';
  let matrixRain = localStorage.getItem('live-matrix-rain') === 'true';
  let rainCanvas = null, rainCtx = null, rainDrops = [], rainRAF = null;
  const propagationBuffer = new Map(); // hash -> {timer, packets[]}
  let _onResize = null;
  let _navCleanup = null;
  let _timelineRefreshInterval = null;
  let _lcdClockInterval = null;
  let _rateCounterInterval = null;
  let _pruneInterval = null;

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

  // ROLE_COLORS loaded from shared roles.js (includes 'unknown')

  const TYPE_COLORS = window.TYPE_COLORS || {
    ADVERT: '#22c55e', GRP_TXT: '#3b82f6', TXT_MSG: '#f59e0b', ACK: '#6b7280',
    REQUEST: '#a855f7', RESPONSE: '#06b6d4', TRACE: '#ec4899', PATH: '#14b8a6'
  };

  const PAYLOAD_ICONS = {
    ADVERT: '📡', GRP_TXT: '💬', TXT_MSG: '✉️', ACK: '✓',
    REQUEST: '❓', RESPONSE: '📨', TRACE: '🔍', PATH: '🛤️'
  };

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
    fetch(`/api/packets?limit=10000&grouped=false&expand=observations&since=${encodeURIComponent(fetchFrom)}&order=asc`)
      .then(r => r.json())
      .then(data => {
        const pkts = data.packets || [];
        const replayEntries = expandToBufferEntries(pkts);
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
    fetch(`/api/packets?limit=2000&grouped=false&expand=observations&since=${encodeURIComponent(from)}`)
      .then(r => r.json())
      .then(data => {
        const pkts = (data.packets || []).reverse(); // oldest first
        // Prepend to buffer (avoid duplicates by ID)
        const existingIds = new Set(VCR.buffer.map(b => b.pkt.id).filter(Boolean));
        const filtered = pkts.filter(p => !existingIds.has(p.id));
        const newEntries = expandToBufferEntries(filtered);
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

    // Pre-aggregate VCR buffer by hash so each tick renders a full tree
    const hashGroups = new Map();
    for (const entry of VCR.buffer) {
      const hash = entry.pkt.hash || ('nohash-' + hashGroups.size);
      if (hashGroups.has(hash)) {
        hashGroups.get(hash).packets.push(entry.pkt);
        if (entry.ts > hashGroups.get(hash).ts) hashGroups.get(hash).ts = entry.ts;
      } else {
        hashGroups.set(hash, { packets: [entry.pkt], ts: entry.ts });
      }
    }
    const replayGroups = [...hashGroups.values()].sort((a, b) => a.ts - b.ts);
    console.log('[vcr] ' + replayGroups.length + ' groups from ' + VCR.buffer.length + ' buffer entries. Top 3:', replayGroups.slice(0,3).map(g => g.packets.length + ' obs'));
    let groupIdx = 0;

    function tick() {
      if (VCR.mode !== 'REPLAY') return;
      if (groupIdx >= replayGroups.length) {
        fetchNextReplayPage().then(hasMore => {
          if (hasMore) vcrResumeLive();
          else vcrResumeLive();
        });
        return;
      }
      const group = replayGroups[groupIdx];
      renderPacketTree(group.packets);
      updateVCRClock(group.ts);
      updateVCRLcd();
      VCR.playhead = Math.min(VCR.buffer.length, VCR.playhead + group.packets.length);
      updateVCRUI();
      updateTimelinePlayhead();
      groupIdx++;

      let delay = 300;
      if (groupIdx < replayGroups.length) {
        const nextGroup = replayGroups[groupIdx];
        const realGap = nextGroup.ts - group.ts;
        delay = Math.min(2000, Math.max(100, realGap)) / VCR.speed;
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
    return fetch(`/api/packets?limit=10000&grouped=false&expand=observations&since=${encodeURIComponent(since)}&order=asc`)
      .then(r => r.json())
      .then(data => {
        const pkts = data.packets || [];
        if (pkts.length === 0) return false;
        const newEntries = expandToBufferEntries(pkts);
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
    drawLcdText(`${hh}:${mm}:${ss}`, statusGreen());
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
      raw: pkt.raw_hex,
      path_json: pkt.path_json,
      _ts: new Date(pkt.timestamp || pkt.created_at).getTime(),
      decoded: { header: { payloadTypeName: typeName }, payload: raw, path: { hops } },
      snr: pkt.snr, rssi: pkt.rssi, observer: pkt.observer_name
    };
  }

  // Expand a DB packet (with optional observations[]) into VCR buffer entries
  function expandToBufferEntries(pkts) {
    const entries = [];
    for (const p of pkts) {
      if (p.observations && p.observations.length > 0) {
        for (const obs of p.observations) {
          entries.push({
            ts: new Date(obs.timestamp || p.timestamp || p.created_at).getTime(),
            pkt: dbPacketToLive(Object.assign({}, p, obs, { hash: p.hash, raw_hex: p.raw_hex, decoded_json: p.decoded_json }))
          });
        }
      } else {
        entries.push({
          ts: new Date(p.timestamp || p.created_at).getTime(),
          pkt: dbPacketToLive(p)
        });
      }
    }
    return entries;
  }

  // Buffer a packet from WS
  let _tabHidden = false;
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) {
      _tabHidden = true;
    } else {
      // Tab restored — skip animating anything that queued while away
      _tabHidden = false;
      // Clear any pending propagation buffers so they don't all fire at once
      for (const [hash, entry] of propagationBuffer) {
        clearTimeout(entry.timer);
      }
      propagationBuffer.clear();
    }
  });

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
      // Skip animations when tab is backgrounded — just buffer for VCR timeline
      if (_tabHidden) {
        updateTimeline();
        return;
      }
      if (realisticPropagation && pkt.hash) {
        const hash = pkt.hash;
        if (propagationBuffer.has(hash)) {
          propagationBuffer.get(hash).packets.push(pkt);
        } else {
          const entry = { packets: [pkt], timer: setTimeout(() => {
            const buffered = propagationBuffer.get(hash);
            propagationBuffer.delete(hash);
            if (buffered) renderPacketTree(buffered.packets);
          }, PROPAGATION_BUFFER_MS) };
          propagationBuffer.set(hash, entry);
        }
      } else {
        renderPacketTree([pkt]);
      }
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
          <div class="live-toggles">
            <label><input type="checkbox" id="liveHeatToggle" checked aria-describedby="heatDesc"> Heat</label>
            <span id="heatDesc" class="sr-only">Overlay a density heat map on the mesh nodes</span>
            <label><input type="checkbox" id="liveGhostToggle" checked aria-describedby="ghostDesc"> Ghosts</label>
            <span id="ghostDesc" class="sr-only">Show interpolated ghost markers for unknown hops</span>
            <label><input type="checkbox" id="liveRealisticToggle" aria-describedby="realisticDesc"> Realistic</label>
            <span id="realisticDesc" class="sr-only">Buffer packets by hash and animate all paths simultaneously</span>
            <label><input type="checkbox" id="liveMatrixToggle" aria-describedby="matrixDesc"> Matrix</label>
            <span id="matrixDesc" class="sr-only">Animate packet hex bytes flowing along paths like the Matrix</span>
            <label><input type="checkbox" id="liveMatrixRainToggle" aria-describedby="rainDesc"> Rain</label>
            <span id="rainDesc" class="sr-only">Matrix rain overlay — packets fall as hex columns</span>
            <label><input type="checkbox" id="liveAudioToggle" aria-describedby="audioDesc"> 🎵 Audio</label>
            <span id="audioDesc" class="sr-only">Sonify packets — turn raw bytes into generative music</span>
            <label><input type="checkbox" id="liveFavoritesToggle" aria-describedby="favDesc"> ⭐ Favorites</label>
            <span id="favDesc" class="sr-only">Show only favorited and claimed nodes</span>
            <label id="liveGeoFilterLabel" style="display:none"><input type="checkbox" id="liveGeoFilterToggle"> Mesh live area</label>
          </div>
          <div class="audio-controls hidden" id="audioControls">
            <label class="audio-slider-label">Voice <select id="audioVoiceSelect" class="audio-voice-select"></select></label>
            <label class="audio-slider-label">BPM <input type="range" id="audioBpmSlider" min="40" max="300" value="120" class="audio-slider"><span id="audioBpmVal">120</span></label>
            <label class="audio-slider-label">Vol <input type="range" id="audioVolSlider" min="0" max="100" value="30" class="audio-slider"><span id="audioVolVal">30</span></label>
          </div>
        </div>
        <div class="live-overlay live-feed" id="liveFeed">
          <button class="feed-hide-btn" id="feedHideBtn" title="Hide feed">✕</button>
        </div>
        <button class="feed-show-btn hidden" id="feedShowBtn" title="Show feed">📋</button>
        <div class="live-overlay live-node-detail hidden" id="liveNodeDetail">
          <button class="feed-hide-btn" id="nodeDetailClose" title="Close">✕</button>
          <div id="nodeDetailContent"></div>
        </div>
        <button class="legend-toggle-btn hidden" id="legendToggleBtn" aria-label="Show legend" title="Show legend">🎨</button>
        <div class="live-overlay live-legend" id="liveLegend" role="region" aria-label="Map legend">
          <h3 class="legend-title">PACKET TYPES</h3>
          <ul class="legend-list">
            <li><span class="live-dot" style="background:${TYPE_COLORS.ADVERT}" aria-hidden="true"></span> Advert — Node advertisement</li>
            <li><span class="live-dot" style="background:${TYPE_COLORS.GRP_TXT}" aria-hidden="true"></span> Message — Group text</li>
            <li><span class="live-dot" style="background:${TYPE_COLORS.TXT_MSG}" aria-hidden="true"></span> Direct — Direct message</li>
            <li><span class="live-dot" style="background:${TYPE_COLORS.REQUEST}" aria-hidden="true"></span> Request — Data request</li>
            <li><span class="live-dot" style="background:${TYPE_COLORS.TRACE}" aria-hidden="true"></span> Trace — Route trace</li>
          </ul>
          <h3 class="legend-title" style="margin-top:8px">NODE ROLES</h3>
          <ul class="legend-list" id="roleLegendList"></ul>
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

    // Fetch configurable map defaults (#115)
    let mapCenter = [37.45, -122.0];
    let mapZoom = 9;
    try {
      const mapCfg = await (await fetch('/api/config/map')).json();
      if (Array.isArray(mapCfg.center) && mapCfg.center.length === 2) mapCenter = mapCfg.center;
      if (typeof mapCfg.zoom === 'number') mapZoom = mapCfg.zoom;
    } catch {}

    map = L.map('liveMap', {
      zoomControl: false, attributionControl: false,
      zoomAnimation: true, markerZoomAnimation: true
    }).setView(mapCenter, mapZoom);

    const isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
    let tileLayer = L.tileLayer(isDark ? TILE_DARK : TILE_LIGHT, { maxZoom: 19 }).addTo(map);

    // Swap tiles when theme changes
    const _themeObs = new MutationObserver(function () {
      const dark = document.documentElement.getAttribute('data-theme') === 'dark' ||
        (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
      tileLayer.setUrl(dark ? TILE_DARK : TILE_LIGHT);
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

    // Check for packet replay from packets page (single or array of observations)
    const replayData = sessionStorage.getItem('replay-packet');
    if (replayData) {
      sessionStorage.removeItem('replay-packet');
      try {
        const parsed = JSON.parse(replayData);
        const packets = Array.isArray(parsed) ? parsed : [parsed];
        vcrPause(); // suppress live packets
        setTimeout(() => renderPacketTree(packets, true), 1500);
      } catch {}
    } else {
      // replayRecent(); // disabled — live page starts empty, fills from WS
    }

    map.on('zoomend', rescaleMarkers);

    // Heat map toggle — persist in localStorage
    const liveHeatEl = document.getElementById('liveHeatToggle');
    if (localStorage.getItem('meshcore-live-heatmap') === 'false') { liveHeatEl.checked = false; hideHeatMap(); }
    else if (localStorage.getItem('meshcore-live-heatmap') === 'true') { liveHeatEl.checked = true; }
    liveHeatEl.addEventListener('change', (e) => {
      localStorage.setItem('meshcore-live-heatmap', e.target.checked);
      if (e.target.checked) showHeatMap(); else hideHeatMap();
    });

    const ghostToggle = document.getElementById('liveGhostToggle');
    ghostToggle.checked = showGhostHops;
    ghostToggle.addEventListener('change', (e) => {
      showGhostHops = e.target.checked;
      localStorage.setItem('live-ghost-hops', showGhostHops);
    });

    const realisticToggle = document.getElementById('liveRealisticToggle');
    realisticToggle.checked = realisticPropagation;
    realisticToggle.addEventListener('change', (e) => {
      realisticPropagation = e.target.checked;
      localStorage.setItem('live-realistic-propagation', realisticPropagation);
    });

    const favoritesToggle = document.getElementById('liveFavoritesToggle');
    favoritesToggle.checked = showOnlyFavorites;
    favoritesToggle.addEventListener('change', (e) => {
      showOnlyFavorites = e.target.checked;
      localStorage.setItem('live-favorites-only', showOnlyFavorites);
      applyFavoritesFilter();
    });

    // Geo filter overlay
    (async function () {
      try {
        const gf = await api('/config/geo-filter', { ttl: 3600 });
        if (!gf || !gf.polygon || gf.polygon.length < 3) return;
        const geoColor = cssVar('--geo-filter-color') || '#3b82f6';
        const latlngs = gf.polygon.map(function (p) { return [p[0], p[1]]; });
        const innerPoly = L.polygon(latlngs, {
          color: geoColor, weight: 2, opacity: 0.8,
          fillColor: geoColor, fillOpacity: 0.08
        });
        const bufferPoly = gf.bufferKm > 0 ? (function () {
          let cLat = 0, cLon = 0;
          gf.polygon.forEach(function (p) { cLat += p[0]; cLon += p[1]; });
          cLat /= gf.polygon.length; cLon /= gf.polygon.length;
          const cosLat = Math.cos(cLat * Math.PI / 180);
          const outer = gf.polygon.map(function (p) {
            const dLatM = (p[0] - cLat) * 111000;
            const dLonM = (p[1] - cLon) * 111000 * cosLat;
            const dist = Math.sqrt(dLatM * dLatM + dLonM * dLonM);
            if (dist === 0) return [p[0], p[1]];
            const scale = (gf.bufferKm * 1000) / dist;
            return [p[0] + dLatM * scale / 111000, p[1] + dLonM * scale / (111000 * cosLat)];
          });
          return L.polygon(outer, {
            color: geoColor, weight: 1.5, opacity: 0.4, dashArray: '6 4',
            fillColor: geoColor, fillOpacity: 0.04
          });
        })() : null;
        geoFilterLayer = L.layerGroup(bufferPoly ? [bufferPoly, innerPoly] : [innerPoly]);
        const label = document.getElementById('liveGeoFilterLabel');
        if (label) label.style.display = '';
        const el = document.getElementById('liveGeoFilterToggle');
        if (el) {
          const saved = localStorage.getItem('meshcore-map-geo-filter');
          if (saved === 'true') { el.checked = true; geoFilterLayer.addTo(map); }
          el.addEventListener('change', function (e) {
            localStorage.setItem('meshcore-map-geo-filter', e.target.checked);
            if (e.target.checked) { geoFilterLayer.addTo(map); } else { map.removeLayer(geoFilterLayer); }
          });
        }
      } catch (e) { /* no geo filter configured */ }
    })();

    const matrixToggle = document.getElementById('liveMatrixToggle');
    matrixToggle.checked = matrixMode;
    matrixToggle.addEventListener('change', (e) => {
      matrixMode = e.target.checked;
      localStorage.setItem('live-matrix-mode', matrixMode);
      applyMatrixTheme(matrixMode);
      if (matrixMode) {
        hideHeatMap();
        const ht = document.getElementById('liveHeatToggle');
        if (ht) { ht.checked = false; ht.disabled = true; }
      } else {
        const ht = document.getElementById('liveHeatToggle');
        if (ht) { ht.disabled = false; }
      }
    });
    applyMatrixTheme(matrixMode);
    if (matrixMode) {
      hideHeatMap();
      const ht = document.getElementById('liveHeatToggle');
      if (ht) { ht.checked = false; ht.disabled = true; }
    } else {
      // Ensure heat toggle is enabled if matrix mode is off (recover from stale state)
      const ht = document.getElementById('liveHeatToggle');
      if (ht) { ht.disabled = false; }
    }

    const rainToggle = document.getElementById('liveMatrixRainToggle');
    rainToggle.checked = matrixRain;
    rainToggle.addEventListener('change', (e) => {
      matrixRain = e.target.checked;
      localStorage.setItem('live-matrix-rain', matrixRain);
      if (matrixRain) startMatrixRain(); else stopMatrixRain();
    });
    if (matrixRain) startMatrixRain();

    // Audio toggle
    const audioToggle = document.getElementById('liveAudioToggle');
    const audioControls = document.getElementById('audioControls');
    const bpmSlider = document.getElementById('audioBpmSlider');
    const bpmVal = document.getElementById('audioBpmVal');
    const volSlider = document.getElementById('audioVolSlider');
    const volVal = document.getElementById('audioVolVal');

    if (window.MeshAudio) {
      MeshAudio.restore();
      audioToggle.checked = MeshAudio.isEnabled();
      if (MeshAudio.isEnabled()) audioControls.classList.remove('hidden');
      bpmSlider.value = MeshAudio.getBPM();
      bpmVal.textContent = MeshAudio.getBPM();
      volSlider.value = Math.round(MeshAudio.getVolume() * 100);
      volVal.textContent = Math.round(MeshAudio.getVolume() * 100);

      // Populate voice selector
      const voiceSelect = document.getElementById('audioVoiceSelect');
      const voices = MeshAudio.getVoiceNames();
      voices.forEach(v => {
        const opt = document.createElement('option');
        opt.value = v; opt.textContent = v;
        voiceSelect.appendChild(opt);
      });
      voiceSelect.value = MeshAudio.getVoiceName() || voices[0] || '';
      voiceSelect.addEventListener('change', (e) => MeshAudio.setVoice(e.target.value));
    }

    audioToggle.addEventListener('change', (e) => {
      if (window.MeshAudio) {
        MeshAudio.setEnabled(e.target.checked);
        audioControls.classList.toggle('hidden', !e.target.checked);
      }
    });
    bpmSlider.addEventListener('input', (e) => {
      const v = parseInt(e.target.value, 10);
      bpmVal.textContent = v;
      if (window.MeshAudio) MeshAudio.setBPM(v);
    });
    volSlider.addEventListener('input', (e) => {
      const v = parseInt(e.target.value, 10);
      volVal.textContent = v;
      if (window.MeshAudio) MeshAudio.setVolume(v / 100);
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

    // Populate role legend from shared roles.js
    const roleLegendList = document.getElementById('roleLegendList');
    if (roleLegendList) {
      for (const role of (window.ROLE_SORT || ['repeater', 'companion', 'room', 'sensor', 'observer'])) {
        const li = document.createElement('li');
        li.innerHTML = `<span class="live-dot" style="background:${ROLE_COLORS[role] || '#6b7280'}" aria-hidden="true"></span> ${(ROLE_LABELS[role] || role).replace(/s$/, '')}`;
        roleLegendList.appendChild(li);
      }
    }

    // Node detail panel
    const nodeDetailPanel = document.getElementById('liveNodeDetail');
    const nodeDetailContent = document.getElementById('nodeDetailContent');
    document.getElementById('nodeDetailClose').addEventListener('click', () => {
      nodeDetailPanel.classList.add('hidden');
    });

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

    // Prune stale nodes every 60 seconds
    _pruneInterval = setInterval(pruneStaleNodes, 60000);

    // Auto-hide nav with pin toggle (#62)
    const topNav = document.querySelector('.top-nav');
    if (topNav) { topNav.style.position = 'fixed'; topNav.style.width = '100%'; topNav.style.zIndex = '1100'; }
    _navCleanup = { timeout: null, fn: null, pinned: false };
    // Add pin button to nav (guard against duplicate)
    if (topNav && !document.getElementById('navPinBtn')) {
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

  async function showNodeDetail(pubkey) {
    const panel = document.getElementById('liveNodeDetail');
    const content = document.getElementById('nodeDetailContent');
    panel.classList.remove('hidden');
    content.innerHTML = '<div style="padding:20px;color:var(--text-muted)">Loading…</div>';
    try {
      const [data, healthData] = await Promise.all([
        api('/nodes/' + encodeURIComponent(pubkey), { ttl: 30 }),
        api('/nodes/' + encodeURIComponent(pubkey) + '/health', { ttl: 30 }).catch(() => null)
      ]);
      const n = data.node;
      const h = healthData || {};
      const stats = h.stats || {};
      const observers = h.observers || [];
      const recent = h.recentPackets || [];
      const roleColor = ROLE_COLORS[n.role] || '#6b7280';
      const roleLabel = (ROLE_LABELS[n.role] || n.role || 'unknown').replace(/s$/, '');
      const hasLoc = n.lat != null && n.lon != null;
      const lastSeen = n.last_seen ? timeAgo(n.last_seen) : '—';
      const thresholds = window.getHealthThresholds ? getHealthThresholds(n.role) : { degradedMs: 3600000, silentMs: 86400000 };
      const ageMs = n.last_seen ? Date.now() - new Date(n.last_seen).getTime() : Infinity;
      const statusDot = ageMs < thresholds.degradedMs ? 'health-green' : ageMs < thresholds.silentMs ? 'health-yellow' : 'health-red';
      const statusLabel = ageMs < thresholds.degradedMs ? 'Online' : ageMs < thresholds.silentMs ? 'Degraded' : 'Offline';

      let html = `
        <div style="padding:16px;">
          <div style="display:flex;align-items:center;gap:8px;margin-bottom:12px;">
            <span class="${statusDot}" style="font-size:18px">●</span>
            <h3 style="margin:0;font-size:16px;font-weight:700;">${escapeHtml(n.name || 'Unknown')}</h3>
          </div>
          <div style="margin-bottom:12px;">
            <span style="display:inline-block;padding:2px 10px;border-radius:12px;font-size:11px;font-weight:600;background:${roleColor};color:#fff;">${roleLabel.toUpperCase()}</span>
            <span style="color:var(--text-muted);font-size:12px;margin-left:8px;">${statusLabel}</span>
          </div>
          <div style="font-size:12px;color:var(--text-muted);margin-bottom:8px;">
            <code style="font-size:10px;word-break:break-all;">${escapeHtml(n.public_key)}</code>
          </div>
          <table style="font-size:12px;width:100%;border-collapse:collapse;">
            <tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Last Seen</td><td>${lastSeen}</td></tr>
            <tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Adverts</td><td>${n.advert_count || 0}</td></tr>
            ${hasLoc ? `<tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Location</td><td>${n.lat.toFixed(5)}, ${n.lon.toFixed(5)}</td></tr>` : ''}
            ${stats.avgSnr != null ? `<tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Avg SNR</td><td>${stats.avgSnr.toFixed(1)} dB</td></tr>` : ''}
            ${stats.avgHops != null ? `<tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Avg Hops</td><td>${stats.avgHops.toFixed(1)}</td></tr>` : ''}
            ${stats.totalTransmissions || stats.totalPackets ? `<tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Total Packets</td><td>${stats.totalTransmissions || stats.totalPackets}</td></tr>` : ''}
          </table>`;

      if (observers.length) {
        const regions = [...new Set(observers.map(o => o.iata).filter(Boolean))];
        html += `<h4 style="font-size:12px;margin:12px 0 6px;color:var(--text-muted);">Heard By${regions.length ? ' — Regions: ' + regions.join(', ') : ''}</h4>
          <div style="font-size:11px;">` +
          observers.map(o => `<div style="padding:2px 0;"><a href="#/observers/${encodeURIComponent(o.observer_id)}" style="color:var(--accent);text-decoration:none;">${escapeHtml(o.observer_name || o.observer_id.slice(0, 12))}${o.iata ? ' (' + escapeHtml(o.iata) + ')' : ''}</a> — ${o.packetCount || o.count || 0} pkts</div>`).join('') +
          '</div>';
      }

      if (recent.length) {
        html += `<h4 style="font-size:12px;margin:12px 0 6px;color:var(--text-muted);">Recent Packets</h4>
          <div style="font-size:11px;max-height:200px;overflow-y:auto;">` +
          recent.slice(0, 10).map(p => `<div style="padding:2px 0;display:flex;justify-content:space-between;">
            <a href="#/packets/${encodeURIComponent(p.hash || '')}" style="color:var(--accent);text-decoration:none;">${escapeHtml(p.payload_type || '?')}${p.observation_count > 1 ? ' <span class="badge badge-obs" style="font-size:9px">👁 ' + p.observation_count + '</span>' : ''}</a>
            <span style="color:var(--text-muted)">${p.timestamp ? timeAgo(p.timestamp) : '—'}</span>
          </div>`).join('') +
          '</div>';
      }

      html += `<div id="liveNodePaths" style="margin-top:8px;"><div style="font-size:11px;color:var(--text-muted);padding:4px 0;"><span class="spinner" style="font-size:10px"></span> Loading paths…</div></div>`;

      html += `<div style="margin-top:12px;display:flex;gap:8px;">
        <a href="#/nodes/${encodeURIComponent(n.public_key)}" style="font-size:12px;color:var(--accent);">Full Detail →</a>
        <a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" style="font-size:12px;color:var(--accent);">📊 Analytics</a>
      </div></div>`;

      content.innerHTML = html;

      // Fetch paths asynchronously
      api('/nodes/' + encodeURIComponent(n.public_key) + '/paths', { ttl: 300 }).then(pathData => {
        const pathEl = document.getElementById('liveNodePaths');
        if (!pathEl) return;
        if (!pathData || !pathData.paths || !pathData.paths.length) {
          pathEl.innerHTML = '';
          return;
        }
        const COLLAPSE = 5;
        function renderPathList(paths) {
          return paths.map(p => {
            const chain = p.hops.map(h => {
              const isThis = h.pubkey === n.public_key || (h.prefix && n.public_key.toLowerCase().startsWith(h.prefix.toLowerCase()));
              const name = escapeHtml(h.name || h.prefix);
              if (isThis) return `<strong style="color:var(--accent)">${name}</strong>`;
              return h.pubkey ? `<a href="#/nodes/${h.pubkey}" style="color:var(--text);text-decoration:none">${name}</a>` : name;
            }).join(' → ');
            return `<div style="padding:3px 0;font-size:11px;line-height:1.4">${chain} <span style="color:var(--text-muted)">(${p.count}×)</span></div>`;
          }).join('');
        }
        pathEl.innerHTML = `<h4 style="font-size:12px;margin:8px 0 4px;color:var(--text-muted);">Paths Through (${pathData.totalPaths})</h4>` +
          `<div id="livePathsList" style="max-height:200px;overflow-y:auto;">` +
          renderPathList(pathData.paths.slice(0, COLLAPSE)) +
          (pathData.paths.length > COLLAPSE ? `<button id="showMorePaths" style="font-size:11px;color:var(--accent);background:none;border:none;cursor:pointer;padding:4px 0;">Show all ${pathData.paths.length} paths</button>` : '') +
          '</div>';
        const moreBtn = document.getElementById('showMorePaths');
        if (moreBtn) moreBtn.addEventListener('click', () => {
          document.getElementById('livePathsList').innerHTML = renderPathList(pathData.paths);
        });
      }).catch(() => {
        const pathEl = document.getElementById('liveNodePaths');
        if (pathEl) pathEl.innerHTML = '';
      });
    } catch (e) {
      content.innerHTML = `<div style="padding:20px;color:var(--text-muted);">Error: ${e.message}</div>`;
    }
  }

  async function loadNodes(beforeTs) {
    try {
      const url = beforeTs
        ? `/api/nodes?limit=2000&before=${encodeURIComponent(new Date(beforeTs).toISOString())}`
        : '/api/nodes?limit=2000';
      const resp = await fetch(url);
      const nodes = await resp.json();
      const list = Array.isArray(nodes) ? nodes : (nodes.nodes || []);
      var now = Date.now();
      list.forEach(n => {
        if (n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0)) {
          n._fromAPI = true;
          n._liveSeen = now;
          nodeData[n.public_key] = n;
          addNodeMarker(n);
        }
      });
      const _el2 = document.getElementById('liveNodeCount'); if (_el2) _el2.textContent = Object.keys(nodeMarkers).length;
      // Initialize shared HopResolver with loaded nodes
      if (window.HopResolver) HopResolver.init(list);
    } catch (e) { console.error('Failed to load nodes:', e); }
  }

  function clearNodeMarkers() {
    if (nodesLayer) nodesLayer.clearLayers();
    if (animLayer) animLayer.clearLayers();
    nodeMarkers = {};
    nodeData = {};
    nodeActivity = {};
    if (window.HopResolver) HopResolver.init([]);
    if (heatLayer) { map.removeLayer(heatLayer); heatLayer = null; }
  }

  function getFavoritePubkeys() {
    let favs = [];
    try { favs = favs.concat(JSON.parse(localStorage.getItem('meshcore-favorites') || '[]')); } catch {}
    try { favs = favs.concat(JSON.parse(localStorage.getItem('meshcore-my-nodes') || '[]').map(n => n.pubkey)); } catch {}
    return favs.filter(Boolean);
  }

  function packetInvolvesFavorite(pkt) {
    const favs = getFavoritePubkeys();
    if (favs.length === 0) return false;
    const decoded = pkt.decoded || {};
    const payload = decoded.payload || {};
    const hops = decoded.path?.hops || [];

    // Full pubkeys: sender
    if (payload.pubKey && favs.some(f => f === payload.pubKey)) return true;

    // Observer: may be name or pubkey
    const obs = pkt.observer_name || pkt.observer || '';
    if (obs) {
      if (favs.some(f => f === obs)) return true;
      for (const nd of Object.values(nodeData)) {
        if ((nd.name === obs || nd.public_key === obs) && favs.some(f => f === nd.public_key)) return true;
      }
    }

    // Hops are truncated hex prefixes — match by prefix in either direction
    for (const hop of hops) {
      const h = (hop.id || hop.public_key || hop).toString().toLowerCase();
      if (favs.some(f => f.toLowerCase().startsWith(h) || h.startsWith(f.toLowerCase()))) return true;
    }

    return false;
  }

  function isNodeFavorited(pubkey) {
    return getFavoritePubkeys().some(f => f === pubkey);
  }

  function rebuildFeedList() {
    const feed = document.getElementById('liveFeed');
    if (!feed) return;
    feed.querySelectorAll('.live-feed-item').forEach(el => el.remove());
    feedDedup.clear();

    // Aggregate VCR buffer by hash, then create one feed item per unique hash
    const byHash = new Map();
    for (const entry of VCR.buffer) {
      const pkt = entry.pkt;
      const hash = pkt.hash;
      if (hash && byHash.has(hash)) {
        const existing = byHash.get(hash);
        existing.packets.push(pkt);
        existing.count++;
        if (entry.ts > existing.latestTs) { existing.latestTs = entry.ts; existing.latestPkt = pkt; }
      } else {
        byHash.set(hash || ('nohash-' + byHash.size), { packets: [pkt], count: 1, latestTs: entry.ts, latestPkt: pkt, hash });
      }
    }

    // Sort by latest timestamp desc, take top 25
    const sorted = [...byHash.values()].sort((a, b) => b.latestTs - a.latestTs).slice(0, 25);

    for (const group of sorted) {
      const pkt = Object.assign({}, group.latestPkt, { observation_count: group.count });
      const decoded = pkt.decoded || {};
      const header = decoded.header || {};
      const payload = decoded.payload || {};
      const typeName = header.payloadTypeName || 'UNKNOWN';
      const icon = PAYLOAD_ICONS[typeName] || '📦';
      const color = TYPE_COLORS[typeName] || '#6b7280';

      // Find longest path across all observations for display
      let longestHops = decoded.path?.hops || [];
      for (const op of group.packets) {
        let opHops = [];
        if (op.path_json) {
          try { opHops = typeof op.path_json === 'string' ? JSON.parse(op.path_json) : op.path_json; } catch {}
        } else if (op.decoded?.path?.hops) {
          opHops = op.decoded.path.hops;
        }
        if (opHops.length > longestHops.length) longestHops = opHops;
      }

      // Create feed item directly with correct count
      const text = payload.text || payload.name || '';
      const preview = text ? ' ' + (text.length > 35 ? text.slice(0, 35) + '…' : text) : '';
      const hopStr = longestHops.length ? `<span class="feed-hops">${longestHops.length}⇢</span>` : '';
      const obsBadge = group.count > 1 ? `<span class="badge badge-obs" style="font-size:10px;margin-left:4px">👁 ${group.count}</span>` : '';

      const item = document.createElement('div');
      item.className = 'live-feed-item';
      item.setAttribute('tabindex', '0');
      item.setAttribute('role', 'button');
      if (group.hash) item.setAttribute('data-hash', group.hash);
      item.style.cursor = 'pointer';
      item.innerHTML = `
        <span class="feed-icon" style="color:${color}">${icon}</span>
        <span class="feed-type" style="color:${color}">${typeName}</span>
        ${hopStr}${obsBadge}
        <span class="feed-text">${escapeHtml(preview)}</span>
        <span class="feed-time">${new Date(group.latestTs || Date.now()).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit',second:'2-digit'})}</span>
      `;
      item.addEventListener('click', () => showFeedCard(item, pkt, color));
      feed.appendChild(item);

      // Register in dedup map so replay and live updates work
      if (group.hash) {
        feedDedup.set(group.hash, { element: item, count: group.count, pkt, packets: group.packets, createdAt: Date.now() });
      }
    }
  }

  function applyFavoritesFilter() {
    // Node markers always stay visible — only rebuild the feed list
    rebuildFeedList();
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

    marker.on('click', () => showNodeDetail(n.public_key));

    marker._glowMarker = glow;
    marker._baseColor = color;
    marker._baseSize = size;
    nodeMarkers[n.public_key] = marker;

    // Apply matrix tint if active
    if (matrixMode) {
      marker._matrixPrevColor = color;
      marker._baseColor = '#008a22';
      marker.setStyle({ fillColor: '#008a22', color: '#008a22', fillOpacity: 0.5, opacity: 0.5 });
      glow.setStyle({ fillColor: '#008a22', fillOpacity: 0.15 });
    }

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

  // Prune nodes not seen within their role's health threshold.
  // API-loaded nodes (_fromAPI) are dimmed instead of removed — matches static map behavior.
  // WS-only nodes (dynamically added from ADVERTs) are removed to prevent memory leaks.
  function pruneStaleNodes() {
    var now = Date.now();
    var pruned = false;
    for (var key in nodeMarkers) {
      var n = nodeData[key];
      if (!n) continue;
      var lastSeen = n._liveSeen || (n.last_heard ? new Date(n.last_heard).getTime() : null) || (n.last_seen ? new Date(n.last_seen).getTime() : null);
      if (lastSeen == null) continue;
      var status = window.getNodeStatus ? getNodeStatus(n.role || 'unknown', lastSeen) : 'active';
      var marker = nodeMarkers[key];
      if (status === 'stale') {
        if (n._fromAPI) {
          // API-loaded nodes: dim instead of removing (consistent with static map)
          if (marker && !marker._staleDimmed) {
            marker._staleDimmed = true;
            marker.setStyle({ fillOpacity: 0.25, opacity: 0.15 });
            if (marker._glowMarker) marker._glowMarker.setStyle({ fillOpacity: 0.04 });
          }
        } else {
          // WS-only nodes: remove to prevent unbounded memory growth
          if (marker) {
            if (nodesLayer) {
              try { nodesLayer.removeLayer(marker); } catch (e) {}
              if (marker._glowMarker) try { nodesLayer.removeLayer(marker._glowMarker); } catch (e) {}
            }
          }
          delete nodeMarkers[key];
          delete nodeData[key];
          pruned = true;
        }
      } else if (marker && marker._staleDimmed) {
        // Node became active again — restore full opacity
        marker._staleDimmed = false;
        var isRepeater = n.role === 'repeater';
        marker.setStyle({ fillOpacity: 0.85, opacity: isRepeater ? 0.6 : 0.3 });
        if (marker._glowMarker) marker._glowMarker.setStyle({ fillOpacity: 0.12 });
      }
    }
    if (pruned) {
      var _el2 = document.getElementById('liveNodeCount');
      if (_el2) _el2.textContent = Object.keys(nodeMarkers).length;
      if (window.HopResolver) HopResolver.init(Object.values(nodeData));
    }
  }

  // Expose for testing
  window._livePruneStaleNodes = pruneStaleNodes;
  window._liveNodeMarkers = function() { return nodeMarkers; };
  window._liveNodeData = function() { return nodeData; };

  async function replayRecent() {
    try {
      const resp = await fetch('/api/packets?limit=8&groupByHash=true');
      const data = await resp.json();
      const groups = (data.packets || []).reverse();

      // Fetch all observations first, then stagger rendering
      const allGroups = [];
      for (let i = 0; i < groups.length; i++) {
        const group = groups[i];
        let observations = [];
        try {
          const detail = await fetch('/api/packets/' + encodeURIComponent(group.hash));
          const detailData = await detail.json();
          observations = detailData.observations || [];
        } catch {}

        const livePackets = observations.map(obs => {
          const livePkt = dbPacketToLive(Object.assign({}, group, obs, {
            hash: group.hash,
            raw_hex: group.raw_hex,
            decoded_json: group.decoded_json,
          }));
          livePkt._ts = new Date(obs.timestamp || group.first_seen || Date.now()).getTime();
          return livePkt;
        });

        if (livePackets.length === 0) {
          const livePkt = dbPacketToLive(group);
          livePkt._ts = new Date(group.first_seen || group.latest || Date.now()).getTime();
          livePackets.push(livePkt);
        }

        livePackets.forEach(lp => VCR.buffer.push({ ts: lp._ts, pkt: lp }));
        allGroups.push(livePackets);
      }

      // Render with real timing gaps between packets
      // Sort by earliest timestamp
      allGroups.sort((a, b) => (a[0]?._ts || 0) - (b[0]?._ts || 0));
      let lastTs = allGroups[0]?.[0]?._ts || Date.now();
      for (let i = 0; i < allGroups.length; i++) {
        const groupTs = allGroups[i][0]?._ts || lastTs;
        // Real gap between this packet and the previous, capped at 3s for UX
        const gap = i === 0 ? 0 : Math.min(3000, Math.max(200, groupTs - lastTs));
        await new Promise(resolve => setTimeout(resolve, gap));
        renderPacketTree(allGroups[i]);
        lastTs = groupTs;
      }
      updateTimeline();
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
    ws.onclose = () => setTimeout(connectWS, WS_RECONNECT_MS);
    ws.onerror = () => {};
  }

  // === UNIFIED PACKET RENDERER ===
  // ONE function for all rendering: WS arrival, DB load, replay button, VCR playback.
  // Takes an array of observations (same hash) and renders the complete path tree.
  function renderPacketTree(packets, isReplay) {
    if (!packets || !packets.length) return;
    const first = packets[0];
    const decoded = first.decoded || {};
    const header = decoded.header || {};
    const payload = decoded.payload || {};
    const typeName = header.payloadTypeName || 'UNKNOWN';
    const icon = PAYLOAD_ICONS[typeName] || '📦';
    const color = TYPE_COLORS[typeName] || '#6b7280';
    const obsCount = packets.length;

    // --- Counters ---
    if (!isReplay) {
      packetCount += obsCount;
      pktTimestamps.push(Date.now());
      const _el = document.getElementById('livePktCount'); if (_el) _el.textContent = packetCount;
    }

    // --- Favorites filter ---
    if (showOnlyFavorites && !packets.some(function(p) { return packetInvolvesFavorite(p); })) return;

    // --- Ensure ADVERT nodes appear on map ---
    for (var pi = 0; pi < packets.length; pi++) {
      var pkt = packets[pi];
      var d = pkt.decoded || {};
      var h = d.header || {};
      var p = d.payload || {};
      if (h.payloadTypeName === 'ADVERT' && p.pubKey) {
        var key = p.pubKey;
        if (!nodeMarkers[key] && p.lat != null && p.lon != null && !(p.lat === 0 && p.lon === 0)) {
          var n = { public_key: key, name: p.name || key.slice(0,8), role: p.role || 'unknown', lat: p.lat, lon: p.lon, _liveSeen: Date.now() };
          nodeData[key] = n;
          addNodeMarker(n);
          if (window.HopResolver) HopResolver.init(Object.values(nodeData));
        } else if (nodeData[key]) {
          nodeData[key]._liveSeen = Date.now();
        }
      }
    }
    const _el2 = document.getElementById('liveNodeCount'); if (_el2) _el2.textContent = Object.keys(nodeMarkers).length;

    // --- Build consolidated packet for feed + audio ---
    const consolidated = Object.assign({}, first, { observation_count: obsCount });

    // --- Audio: sonify with correct observation count for multi-voice ---
    if (window.MeshAudio) MeshAudio.sonifyPacket(consolidated);

    // --- Feed item (one per hash group) ---
    if (!isReplay) {
      // Find longest path across all observations for display
      let feedHops = decoded.path?.hops || [];
      for (const fp of packets) {
        let fpHops = [];
        if (fp.path_json) {
          try { fpHops = typeof fp.path_json === 'string' ? JSON.parse(fp.path_json) : fp.path_json; } catch {}
        } else if (fp.decoded?.path?.hops) {
          fpHops = fp.decoded.path.hops;
        }
        if (fpHops.length > feedHops.length) feedHops = fpHops;
      }
      addFeedItem(icon, typeName, payload, feedHops, color, consolidated);
      // Store all observation packets in dedup entry for replay tree
      if (consolidated.hash && feedDedup.has(consolidated.hash)) {
        const entry = feedDedup.get(consolidated.hash);
        // Append observations — don't overwrite (each renderPacketTree call may have 1 or many)
        for (const p of packets) {
          if (!entry.packets.some(ep => ep.path_json === p.path_json && ep.observer === p.observer)) {
            entry.packets.push(p);
          }
        }
      }
    }

    // --- Rain drops: one per observation ---
    var baseHops = (decoded.path?.hops || []).length || 1;
    packets.forEach(function(rp, i) {
      if (i === 0) { addRainDrop(rp); return; }
      var variedHops = Math.max(1, baseHops + Math.floor(Math.random() * 3) - 1);
      setTimeout(function() { addRainDrop(rp, variedHops); }, i * 150);
    });

    // --- Extract all unique paths from observations ---
    // Prefer path_json (per-observer unique path) over decoded.path.hops (same for all)
    var allPaths = [];
    var seenPathKeys = new Set();
    for (var qi = 0; qi < packets.length; qi++) {
      var qpkt = packets[qi];
      var qd = qpkt.decoded || {};
      var qp = qd.payload || {};
      var hops;
      if (qpkt.path_json) {
        try { hops = typeof qpkt.path_json === 'string' ? JSON.parse(qpkt.path_json) : qpkt.path_json; } catch (e) { hops = qd.path?.hops || []; }
      } else {
        hops = qd.path?.hops || [];
      }
      var pathKey = hops.join(',');
      if (seenPathKeys.has(pathKey)) continue;
      seenPathKeys.add(pathKey);
      var hopPositions = resolveHopPositions(hops, qp);
      if (hopPositions.length >= 2) {
        allPaths.push({ hopPositions: hopPositions, raw: qpkt.raw || first.raw });
      } else if (hopPositions.length === 1) {
        pulseNode(hopPositions[0].key, hopPositions[0].pos, typeName);
      }
    }

    // If no multi-hop paths found, try the decoded path as fallback
    if (allPaths.length === 0) {
      var fallbackHops = decoded.path?.hops || [];
      var fallbackPositions = resolveHopPositions(fallbackHops, payload);
      if (fallbackPositions.length >= 2) {
        allPaths.push({ hopPositions: fallbackPositions, raw: first.raw });
      } else if (fallbackPositions.length === 1) {
        pulseNode(fallbackPositions[0].key, fallbackPositions[0].pos, typeName);
      }
    }

    // --- Animate all unique paths simultaneously ---
    // First path gets audio sync hook, rest are visual-only
    var firstPathDone = false;
    for (var ai = 0; ai < allPaths.length; ai++) {
      var onHop = null;
      if (!firstPathDone && obsCount === 1 && window.MeshAudio) {
        // For single observation, try sync voice on the first path
        var voice = window._meshAudioVoices && window._meshAudioVoices[MeshAudio.getVoiceName()];
        if (voice && voice.createSync && MeshAudio.isEnabled()) {
          var audioSync = voice.createSync(consolidated);
          if (audioSync) onHop = audioSync.playHop;
        }
      }
      firstPathDone = true;
      animatePath(allPaths[ai].hopPositions, typeName, color, allPaths[ai].raw, onHop);
    }
  }

  function resolveHopPositions(hops, payload) {
    // Delegate to shared HopResolver (from hop-resolver.js) instead of reimplementing
    const originLat = payload.lat != null && !(payload.lat === 0 && payload.lon === 0) ? payload.lat : null;
    const originLon = payload.lon != null && !(payload.lon === 0 && payload.lon === 0) ? payload.lon : null;

    // Use HopResolver if available and initialized, otherwise fall back to simple lookup
    const resolvedMap = (window.HopResolver && HopResolver.ready())
      ? HopResolver.resolve(hops, originLat, originLon, null, null, null)
      : {};

    // Convert HopResolver's map format to the array format live.js expects: {key, pos, name, known}
    const raw = hops.map(hop => {
      const r = resolvedMap[hop];
      if (r && r.name && r.pubkey && !r.unreliable) {
        // Look up coordinates from nodeData (HopResolver resolves name/pubkey but doesn't return lat/lon directly)
        const node = nodeData[r.pubkey];
        if (node && node.lat != null && node.lon != null && !(node.lat === 0 && node.lon === 0)) {
          return { key: r.pubkey, pos: [node.lat, node.lon], name: r.name, known: true };
        }
        return { key: r.pubkey, pos: null, name: r.name, known: false };
      }
      return { key: 'hop-' + hop, pos: null, name: hop, known: false };
    });

    // Add sender position as anchor if available
    if (payload.pubKey && originLat != null) {
      const existing = raw.find(p => p.key === payload.pubKey);
      if (!existing) {
        raw.unshift({ key: payload.pubKey, pos: [payload.lat, payload.lon], name: payload.name || payload.pubKey.slice(0, 8), known: true });
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

  function animatePath(hopPositions, typeName, color, rawHex, onHop) {
    if (!animLayer || !pathsLayer) return;
    activeAnims++;
    document.getElementById('liveAnimCount').textContent = activeAnims;
    let hopIndex = 0;

    function nextHop() {
      if (hopIndex >= hopPositions.length) {
        activeAnims = Math.max(0, activeAnims - 1);
        document.getElementById('liveAnimCount').textContent = activeAnims;
        return;
      }
      // Audio hook: notify per-hop callback
      if (onHop) try { onHop(hopIndex, hopPositions.length, hopPositions[hopIndex]); } catch (e) {}
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
        drawAnimatedLine(hp.pos, nextPos, lineColor, () => { hopIndex++; nextHop(); }, lineOpacity, rawHex);
      } else {
        if (!isGhost) pulseNode(hp.key, hp.pos, typeName);
        hopIndex++; nextHop();
      }
    }
    nextHop();
  }

  function pulseNode(key, pos, typeName) {
    if (!animLayer || !nodesLayer) return;
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

  // === Matrix Rain System ===
  function startMatrixRain() {
    const container = document.getElementById('liveMap');
    if (!container || rainCanvas) return;
    rainCanvas = document.createElement('canvas');
    rainCanvas.id = 'matrixRainCanvas';
    rainCanvas.style.cssText = 'position:absolute;inset:0;z-index:9998;pointer-events:none;';
    rainCanvas.width = container.clientWidth;
    rainCanvas.height = container.clientHeight;
    container.appendChild(rainCanvas);
    rainCtx = rainCanvas.getContext('2d');
    rainDrops = [];

    // Resize handler
    rainCanvas._resizeHandler = () => {
      if (rainCanvas) {
        rainCanvas.width = container.clientWidth;
        rainCanvas.height = container.clientHeight;
      }
    };
    window.addEventListener('resize', rainCanvas._resizeHandler);

    function renderRain(now) {
      if (!rainCanvas || !rainCtx) return;
      const W = rainCanvas.width, H = rainCanvas.height;
      rainCtx.clearRect(0, 0, W, H);

      for (let i = rainDrops.length - 1; i >= 0; i--) {
        const drop = rainDrops[i];
        const elapsed = now - drop.startTime;
        const progress = Math.min(1, elapsed / drop.duration);

        // Head position
        const headY = progress * drop.maxY;
        // Trail shows all packet bytes, scrolling through them
        const CHAR_H = 18;
        const VISIBLE_CHARS = drop.bytes.length; // show all bytes
        const trailPx = VISIBLE_CHARS * CHAR_H;

        // Scroll offset — cycles through all bytes over the drop lifetime
        const scrollOffset = Math.floor(progress * drop.bytes.length);

        for (let c = 0; c < VISIBLE_CHARS; c++) {
          const charY = headY - c * CHAR_H;
          if (charY < -CHAR_H || charY > H) continue;

          const byteIdx = (scrollOffset + c) % drop.bytes.length;

          // Fade: head is bright, tail fades
          const fadeFactor = 1 - (c / VISIBLE_CHARS);
          // Also fade entire drop near end of life
          const lifeFade = progress > 0.7 ? 1 - (progress - 0.7) / 0.3 : 1;
          const alpha = Math.max(0, fadeFactor * lifeFade);

          if (c === 0) {
            rainCtx.font = 'bold 16px "Courier New", monospace';
            rainCtx.fillStyle = `rgba(255, 255, 255, ${alpha})`;
            rainCtx.shadowColor = '#00ff41';
            rainCtx.shadowBlur = 12;
          } else {
            rainCtx.font = '14px "Courier New", monospace';
            rainCtx.fillStyle = `rgba(0, 255, 65, ${alpha * 0.8})`;
            rainCtx.shadowColor = '#00ff41';
            rainCtx.shadowBlur = 4;
          }

          rainCtx.fillText(drop.bytes[byteIdx], drop.x, charY);
        }

        // Remove finished drops
        if (progress >= 1) {
          rainDrops.splice(i, 1);
        }
      }

      rainCtx.shadowBlur = 0; // reset
      rainRAF = requestAnimationFrame(renderRain);
    }
    rainRAF = requestAnimationFrame(renderRain);
  }

  function stopMatrixRain() {
    if (rainRAF) { cancelAnimationFrame(rainRAF); rainRAF = null; }
    if (rainCanvas) {
      window.removeEventListener('resize', rainCanvas._resizeHandler);
      rainCanvas.remove();
      rainCanvas = null;
      rainCtx = null;
    }
    rainDrops = [];
  }

  function addRainDrop(pkt, hopOverride) {
    if (!rainCanvas || !matrixRain) return;
    const rawHex = pkt.raw || pkt.raw_hex || (pkt.packet && pkt.packet.raw_hex) || '';
    if (!rawHex) return;
    const decoded = pkt.decoded || {};
    const hops = decoded.path?.hops || [];
    const hopCount = hopOverride || Math.max(1, hops.length);
    const bytes = [];
    for (let i = 0; i < rawHex.length; i += 2) {
      bytes.push(rawHex.slice(i, i + 2).toUpperCase());
    }
    if (bytes.length === 0) return;

    const W = rainCanvas.width;
    const H = rainCanvas.height;
    // Fall distance proportional to hops: 8+ hops = full height
    const maxY = H * Math.min(1, hopCount / 4);
    // Duration: 5s for full height, proportional for shorter
    const duration = 5000 * (maxY / H);

    // Random x position, avoid edges
    const x = 20 + Math.random() * (W - 40);

    rainDrops.push({
      x,
      maxY,
      duration,
      bytes,
      hops: hopCount,
      startTime: performance.now()
    });
  }

  function applyMatrixTheme(on) {
    const container = document.getElementById('liveMap');
    if (!container) return;
    if (on) {
      // Force dark mode, save previous theme to restore later
      const currentTheme = document.documentElement.getAttribute('data-theme');
      if (currentTheme !== 'dark') {
        container.dataset.matrixPrevTheme = currentTheme || 'light';
        document.documentElement.setAttribute('data-theme', 'dark');
        const dt = document.getElementById('darkModeToggle');
        if (dt) { dt.textContent = '🌙'; dt.disabled = true; }
      } else {
        const dt = document.getElementById('darkModeToggle');
        if (dt) dt.disabled = true;
      }
      container.classList.add('matrix-theme');
      if (!document.getElementById('matrixScanlines')) {
        const scanlines = document.createElement('div');
        scanlines.id = 'matrixScanlines';
        scanlines.className = 'matrix-scanlines';
        container.appendChild(scanlines);
      }
      for (const [key, marker] of Object.entries(nodeMarkers)) {
        marker._matrixPrevColor = marker._baseColor;
        marker._baseColor = '#008a22';
        marker.setStyle({ fillColor: '#008a22', color: '#008a22', fillOpacity: 0.5, opacity: 0.5 });
        if (marker._glowMarker) marker._glowMarker.setStyle({ fillColor: '#008a22', fillOpacity: 0.15 });
      }
    } else {
      container.classList.remove('matrix-theme');
      const scanlines = document.getElementById('matrixScanlines');
      if (scanlines) scanlines.remove();
      // Restore previous theme
      const prevTheme = container.dataset.matrixPrevTheme;
      if (prevTheme) {
        document.documentElement.setAttribute('data-theme', prevTheme);
        localStorage.setItem('meshcore-theme', prevTheme);
        const dt = document.getElementById('darkModeToggle');
        if (dt) { dt.textContent = prevTheme === 'dark' ? '🌙' : '☀️'; dt.disabled = false; }
        delete container.dataset.matrixPrevTheme;
      } else {
        const dt = document.getElementById('darkModeToggle');
        if (dt) dt.disabled = false;
      }
      for (const [key, marker] of Object.entries(nodeMarkers)) {
        if (marker._matrixPrevColor) {
          marker._baseColor = marker._matrixPrevColor;
          marker.setStyle({ fillColor: marker._matrixPrevColor, color: '#fff', fillOpacity: 0.85, opacity: 1 });
          if (marker._glowMarker) marker._glowMarker.setStyle({ fillColor: marker._matrixPrevColor });
          delete marker._matrixPrevColor;
        }
      }
    }
  }

  function drawMatrixLine(from, to, color, onComplete, rawHex) {
    if (!animLayer || !pathsLayer) { if (onComplete) onComplete(); return; }
    const hexStr = rawHex || '';
    const bytes = [];
    for (let i = 0; i < hexStr.length; i += 2) {
      bytes.push(hexStr.slice(i, i + 2).toUpperCase());
    }
    if (bytes.length === 0) {
      for (let i = 0; i < 16; i++) bytes.push(((Math.random() * 256) | 0).toString(16).padStart(2, '0').toUpperCase());
    }

    const matrixGreen = '#00ff41';
    const TRAIL_LEN = Math.min(6, bytes.length);
    const DURATION_MS = 1100; // total hop duration
    const CHAR_INTERVAL = 0.06; // spawn a char every 6% of progress
    const charMarkers = [];
    let nextCharAt = CHAR_INTERVAL;
    let byteIdx = 0;

    const trail = L.polyline([from], {
      color: matrixGreen, weight: 1.5, opacity: 0.2, lineCap: 'round'
    }).addTo(pathsLayer);

    const trailCoords = [from];
    const startTime = performance.now();

    function tick(now) {
      const elapsed = now - startTime;
      const t = Math.min(1, elapsed / DURATION_MS);
      const lat = from[0] + (to[0] - from[0]) * t;
      const lon = from[1] + (to[1] - from[1]) * t;
      trailCoords.push([lat, lon]);
      trail.setLatLngs(trailCoords);

      // Remove old chars beyond trail length
      while (charMarkers.length > TRAIL_LEN) {
        const old = charMarkers.shift();
        try { animLayer.removeLayer(old.marker); } catch {}
      }

      // Fade existing chars
      for (let i = 0; i < charMarkers.length; i++) {
        const age = charMarkers.length - i;
        const op = Math.max(0.15, 1 - (age / TRAIL_LEN) * 0.7);
        const size = Math.max(10, 16 - age * 1.5);
        const el = charMarkers[i].marker.getElement();
        if (el) { el.style.opacity = op; el.style.fontSize = size + 'px'; }
      }

      // Spawn new char at intervals
      if (t >= nextCharAt && t < 1) {
        nextCharAt += CHAR_INTERVAL;
        const charEl = L.marker([lat, lon], {
          icon: L.divIcon({
            className: 'matrix-char',
            html: `<span style="color:#fff;font-family:'Courier New',monospace;font-size:16px;font-weight:bold;text-shadow:0 0 8px ${matrixGreen},0 0 16px ${matrixGreen},0 0 24px ${matrixGreen}60;pointer-events:none">${bytes[byteIdx % bytes.length]}</span>`,
            iconSize: [24, 18],
            iconAnchor: [12, 9]
          }),
          interactive: false
        }).addTo(animLayer);
        charMarkers.push({ marker: charEl });
        byteIdx++;
      }

      if (t < 1) {
        requestAnimationFrame(tick);
      } else {
        // Fade out
        const fadeStart = performance.now();
        function fadeOut(now) {
          const ft = Math.min(1, (now - fadeStart) / 300);
          if (ft >= 1) {
            for (const cm of charMarkers) try { animLayer.removeLayer(cm.marker); } catch {}
            try { pathsLayer.removeLayer(trail); } catch {}
            charMarkers.length = 0;
          } else {
            const op = 1 - ft;
            for (const cm of charMarkers) {
              const el = cm.marker.getElement(); if (el) el.style.opacity = op * 0.5;
            }
            trail.setStyle({ opacity: op * 0.15 });
            requestAnimationFrame(fadeOut);
          }
        }
        setTimeout(() => requestAnimationFrame(fadeOut), 150);
        if (onComplete) onComplete();
      }
    }
    requestAnimationFrame(tick);
  }

  function drawAnimatedLine(from, to, color, onComplete, overrideOpacity, rawHex) {
    if (!animLayer || !pathsLayer) { if (onComplete) onComplete(); return; }
    if (matrixMode) return drawMatrixLine(from, to, color, onComplete, rawHex);
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
        if (animLayer) animLayer.removeLayer(dot);

        recentPaths.push({ line, glowLine: contrail, time: Date.now() });
        while (recentPaths.length > 5) {
          const old = recentPaths.shift();
          if (pathsLayer) { pathsLayer.removeLayer(old.line); pathsLayer.removeLayer(old.glowLine); }
        }

        setTimeout(() => {
          let fadeOp = mainOpacity;
          const fi = setInterval(() => {
            fadeOp -= 0.1;
            if (fadeOp <= 0) {
              clearInterval(fi);
              if (pathsLayer) { pathsLayer.removeLayer(line); pathsLayer.removeLayer(contrail); }
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
      var savedOpacity = parseFloat(localStorage.getItem('meshcore-live-heatmap-opacity'));
      if (isNaN(savedOpacity)) savedOpacity = 0.3;
      heatLayer = L.heatLayer(points, {
        radius: 25, blur: 15, maxZoom: 14, minOpacity: 0.05,
        gradient: { 0.2: '#0d47a1', 0.4: '#1565c0', 0.6: '#42a5f5', 0.8: '#ffca28', 1.0: '#ff5722' }
      }).addTo(map);
      // Set overall layer opacity via canvas element
      if (heatLayer._canvas) { heatLayer._canvas.style.opacity = savedOpacity; }
      else { setTimeout(function() { if (heatLayer && heatLayer._canvas) heatLayer._canvas.style.opacity = savedOpacity; }, 100); }
      window._meshcoreLiveHeatLayer = heatLayer;
    }
  }

  function hideHeatMap() {
    if (heatLayer) { map.removeLayer(heatLayer); heatLayer = null; }
  }

  function addFeedItemDOM(icon, typeName, payload, hops, color, pkt, feed) {
    const text = payload.text || payload.name || '';
    const preview = text ? ' ' + (text.length > 35 ? text.slice(0, 35) + '…' : text) : '';
    const hopStr = hops.length ? `<span class="feed-hops">${hops.length}⇢</span>` : '';
    const obsBadge = pkt.observation_count > 1 ? `<span class="badge badge-obs" style="font-size:10px;margin-left:4px">👁 ${pkt.observation_count}</span>` : '';
    const item = document.createElement('div');
    item.className = 'live-feed-item';
    item.setAttribute('tabindex', '0');
    item.setAttribute('role', 'button');
    item.style.cursor = 'pointer';
    item.innerHTML = `
      <span class="feed-icon" style="color:${color}">${icon}</span>
      <span class="feed-type" style="color:${color}">${typeName}</span>
      ${hopStr}${obsBadge}
      <span class="feed-text">${escapeHtml(preview)}</span>
      <span class="feed-time">${new Date(pkt._ts || Date.now()).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit',second:'2-digit'})}</span>
    `;
    item.addEventListener('click', () => showFeedCard(item, pkt, color));
    feed.appendChild(item);
  }

  // Dedup: hash → {element, count, pkt, packets[], createdAt}
  // First packet with hash A creates a feed item.
  // Any packet with hash A arriving within 30s updates that item's count.
  // packets[] stores all observations for replay.
  const feedDedup = new Map();
  const DEDUP_WINDOW = 30000;

  function addFeedItem(icon, typeName, payload, hops, color, pkt) {
    const feed = document.getElementById('liveFeed');
    if (!feed) return;
    if (showOnlyFavorites && !packetInvolvesFavorite(pkt)) return;

    const hash = pkt.hash;
    const incomingObs = pkt.observation_count || 1;

    // Dedup: same hash within window → update existing entry
    if (hash && feedDedup.has(hash)) {
      const entry = feedDedup.get(hash);
      if ((Date.now() - entry.createdAt) < DEDUP_WINDOW) {
        entry.count += incomingObs;
        entry.packets.push(pkt);
        // Ensure badge exists
        let badge = entry.element.querySelector('.badge-obs');
        if (!badge) {
          badge = document.createElement('span');
          badge.className = 'badge badge-obs';
          badge.style.cssText = 'font-size:10px;margin-left:4px';
          const ref = entry.element.querySelector('.feed-hops') || entry.element.querySelector('.feed-type');
          if (ref) ref.after(badge); else entry.element.appendChild(badge);
        }
        badge.textContent = '👁 ' + entry.count;
        // Flash + move to top
        entry.element.classList.remove('live-feed-enter');
        void entry.element.offsetWidth; // force reflow
        entry.element.classList.add('live-feed-enter');
        requestAnimationFrame(() => requestAnimationFrame(() => entry.element.classList.remove('live-feed-enter')));
        // Re-add to DOM top (works even if it was trimmed out)
        feed.prepend(entry.element);
        entry.pkt.observation_count = entry.count;
        return;
      }
      // Window expired — fall through to create new entry
      feedDedup.delete(hash);
    }

    // Create new feed item
    const text = payload.text || payload.name || '';
    const preview = text ? ' ' + (text.length > 35 ? text.slice(0, 35) + '…' : text) : '';
    const hopStr = hops.length ? `<span class="feed-hops">${hops.length}⇢</span>` : '';
    const obsBadge = incomingObs > 1 ? `<span class="badge badge-obs" style="font-size:10px;margin-left:4px">👁 ${incomingObs}</span>` : '';

    const item = document.createElement('div');
    item.className = 'live-feed-item live-feed-enter';
    item.setAttribute('tabindex', '0');
    item.setAttribute('role', 'button');
    if (hash) item.setAttribute('data-hash', hash);
    item.style.cursor = 'pointer';
    item.innerHTML = `
      <span class="feed-icon" style="color:${color}">${icon}</span>
      <span class="feed-type" style="color:${color}">${typeName}</span>
      ${hopStr}${obsBadge}
      <span class="feed-text">${escapeHtml(preview)}</span>
      <span class="feed-time">${new Date(pkt._ts || Date.now()).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit',second:'2-digit'})}</span>
    `;
    item.addEventListener('click', () => showFeedCard(item, pkt, color));
    feed.prepend(item);
    requestAnimationFrame(() => requestAnimationFrame(() => item.classList.remove('live-feed-enter')));
    while (feed.children.length > 25) feed.removeChild(feed.lastChild);

    // Register
    if (hash) {
      feedDedup.set(hash, { element: item, count: incomingObs, pkt, packets: [pkt], createdAt: Date.now() });
      // Prune stale entries
      if (feedDedup.size > 100) {
        const cutoff = Date.now() - DEDUP_WINDOW;
        for (const [k, v] of feedDedup) {
          if (v.createdAt < cutoff) feedDedup.delete(k);
        }
      }
    }
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
      ${pkt.hash ? `<a class="fdc-link" href="#/packets/${pkt.hash.toLowerCase()}">View in packets →</a>` : ''}
      <button class="fdc-replay">↻ Replay</button>
    `;
    card.querySelector('.fdc-close').addEventListener('click', (e) => { e.stopPropagation(); card.remove(); });
    card.querySelector('.fdc-replay').addEventListener('click', (e) => {
      e.stopPropagation();
      const dedupEntry = pkt.hash && feedDedup.get(pkt.hash);
      const replayPkts = (dedupEntry && dedupEntry.packets.length > 1) ? dedupEntry.packets : [pkt];
      const uniquePaths = new Set(replayPkts.map(p => p.path_json || JSON.stringify(p.decoded?.path?.hops || [])));
      console.log('[replay] hash=' + pkt.hash + ' pkts=' + replayPkts.length + ' uniquePaths=' + uniquePaths.size, [...uniquePaths].slice(0, 3));
      renderPacketTree(replayPkts, true);
    });
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
    if (_pruneInterval) { clearInterval(_pruneInterval); _pruneInterval = null; }
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
    const existingPin = document.getElementById('navPinBtn');
    if (existingPin) existingPin.remove();
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
    nodesLayer = pathsLayer = animLayer = heatLayer = geoFilterLayer = null;
    stopMatrixRain();
    nodeMarkers = {}; nodeData = {};
    recentPaths = [];
    packetCount = 0; activeAnims = 0;
    nodeActivity = {}; pktTimestamps = [];
    feedDedup.clear();
    VCR.buffer = []; VCR.playhead = -1; VCR.mode = 'LIVE'; VCR.missedCount = 0; VCR.speed = 1;
  }

  let _themeRefreshHandler = null;

  registerPage('live', {
    init: function(app, routeParam) {
      _themeRefreshHandler = () => { /* live map rebuilds on next packet */ };
      window.addEventListener('theme-refresh', _themeRefreshHandler);
      return init(app, routeParam);
    },
    destroy: function() {
      if (_themeRefreshHandler) { window.removeEventListener('theme-refresh', _themeRefreshHandler); _themeRefreshHandler = null; }
      return destroy();
    }
  });
})();
