(function() {
  'use strict';

  // #1799 PR #1804 r1 item 9 (adv6): payload-labels.js is loaded
  // synchronously before this script in index.html. Missing module is
  // a packaging bug — crash loud rather than render with stale inline
  // fallback data.
  if (!window.PayloadLabels) {
    throw new Error('live.js: window.PayloadLabels missing — payload-labels.js failed to load');
  }

  // getParsedPath / getParsedDecoded are in shared packet-helpers.js (loaded before this file)
  var getParsedPath = window.getParsedPath;
  var getParsedDecoded = window.getParsedDecoded;

  // Status color helpers (read from CSS variables for theme support)
  function cssVar(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
  function statusGreen() { return cssVar('--status-green') || '#22c55e'; }

  let map, ws, nodesLayer, pathsLayer, animLayer, heatLayer, geoFilterLayer, clickablePathsLayer;
  // New animation canvas
  let animCanvas, animCtx;
  let _dprMedia = null;
  let _dprChangeHandler = null;
  let activeAnimations = [];
  let activePulses = [];
  let activeGhosts = [];
  let isAnimating = false;
  let activeFades = [];
  let isFading = false;
  let canvasTopLeft;
  // #1514 S2 — scratch points reused per-frame in renderAnimations() to avoid
  // 2 object allocations per anim per frame (50 anims × 60fps = ~6000/sec).
  const _scratchFrom = { x: 0, y: 0 };
  const _scratchTo = { x: 0, y: 0 };
  let clickablePaths = [];
  const CLICKABLE_PATH_TTL_MS = 30000;
  const CLICKABLE_PATH_MAX = 50;
  const CLICKABLE_POPUP_DISMISS_MS = 20000;
  let nodeMarkers = {};
  let nodeData = {};
  let packetCount = 0;
  let activeAnims = 0;
  const MAX_CONCURRENT_ANIMS = 20;
  // Perf caps for the animation overlay (see updateAnimCanvas / renderAnimations).
  const ANIM_MAX_DPR = 1.5;       // cap backing-store pixels on hi-DPI displays
  const ANIM_MIN_FRAME_MS = 15;   // don't redraw faster than ~60fps (high-refresh guard)
  let _lastAnimFrame = 0;
  let nodeActivity = {};
  let recentPaths = [];
  // Heat is a `let` mirror like the seven below it, NOT an inline
  // localStorage read: wireLiveControls() restores from it and its onChange
  // writes it back, so it stays fresh across SPA remounts the same way the
  // others do (destroy() never resets these; the onChange assignment is what
  // keeps them honest). Nothing else writes 'meshcore-live-heatmap' -- checked
  // across the tree, the only writer is the heat onChange below.
  let heatEnabled = localStorage.getItem('meshcore-live-heatmap') !== 'false';
  let showGhostHops = localStorage.getItem('live-ghost-hops') !== 'false';
  let realisticPropagation = localStorage.getItem('live-realistic-propagation') === 'true';
  let showOnlyFavorites = localStorage.getItem('live-favorites-only') === 'true';
  let multibyteOnly = localStorage.getItem('live-multibyte-only') === 'true';
  let matrixMode = localStorage.getItem('live-matrix-mode') === 'true';
  let matrixRain = localStorage.getItem('live-matrix-rain') === 'true';
  let colorByHash = localStorage.getItem('meshcore-color-packets-by-hash') !== 'false';
  /** Current theme string for hash-color functions. */
  function _liveTheme() { return document.documentElement.dataset.theme || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'); }
  let nodeFilterKeys = (localStorage.getItem('live-node-filter') || '').split(',').map(s => s.trim()).filter(Boolean);
  let nodeFilterTotal = 0;
  let nodeFilterShown = 0;
  // Region filter (#1045): observer_id → IATA code, populated from /api/observers
  let observerIataMap = {};
  let regionFilterChangeHandler = null;

  /**
   * Returns true if the packet group matches the selected regions.
   * - selected null/empty → no filter active, always true.
   * - Match if ANY observation's observer maps to an IATA in selected (case-insensitive).
   * Pure helper exposed for unit tests.
   */
  function packetMatchesRegion(packets, obsMap, selected) {
    if (!selected || !selected.length) return true;
    if (!packets || !packets.length) return false;
    const sel = selected.map(function(s) { return String(s).toUpperCase(); });
    for (var i = 0; i < packets.length; i++) {
      var oid = packets[i] && packets[i].observer_id;
      if (oid == null) continue;
      var iata = obsMap && obsMap[oid];
      if (!iata) continue;
      if (sel.indexOf(String(iata).toUpperCase()) !== -1) return true;
    }
    return false;
  }
  function setObserverIataMap(m) { observerIataMap = m || {}; }

  // #1189 R2 mesh-operator fix: live feed must show the observer's IATA pill
  // alongside the existing observation-count badge so operators on /live can tell SAME-
  // region from CROSS-region reception at a glance (same affordance as the
  // /packets table). Mirrors `obsIataBadge` in public/packets.js — kept as a
  // local helper for now (live.js and packets.js are separate IIFEs with no
  // shared module). TODO: extract `obsIataBadge` into shared packet-helpers.js
  // and have both surfaces import it.
  function obsIataBadgeHtml(pkt) {
    if (!pkt) return '';
    var iata = pkt.observer_iata;
    if (!iata && pkt.observer_id) iata = observerIataMap && observerIataMap[pkt.observer_id];
    if (!iata) return '';
    var esc = (typeof escapeHtml === 'function')
      ? escapeHtml(iata)
      : String(iata).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    return '<span class="badge-iata" style="font-size:10px;margin-left:4px">' + esc + '</span>';
  }

  /**
   * Build observer_id → IATA map from the /api/observers response.
   * The endpoint returns `{ observers: [...], server_time: "..." }`
   * (cmd/server/types.go ObserverListResponse). Defensive: also accepts
   * a bare array in case the API shape ever changes back, and ignores
   * observers without an IATA. Returns a plain object (used as a hash).
   * Exported for tests via window._liveBuildObserverIataMap.
   * Fixes #1136 (regression introduced in #1080 which assumed array shape).
   */
  function buildObserverIataMap(data) {
    var list = null;
    if (Array.isArray(data)) list = data;
    else if (data && Array.isArray(data.observers)) list = data.observers;
    var m = {};
    if (!list) return m;
    for (var i = 0; i < list.length; i++) {
      var o = list[i];
      if (o && o.id != null && o.iata) m[o.id] = o.iata;
    }
    return m;
  }
  const _savedSpeed = parseFloat(localStorage.getItem('live-vcr-speed'));
  const _initialSpeed = [0.25, 0.5, 1, 2, 4, 8].includes(_savedSpeed) ? _savedSpeed : 1;
  let rainCanvas = null, rainCtx = null, rainDrops = [], rainRAF = null;
  const propagationBuffer = new Map(); // hash -> {timer, packets[]}
  let _onResize = null;
  let _navCleanup = null;
  let _timelineRefreshInterval = null;
  let _lcdClockInterval = null;
  let _rateCounterInterval = null;
  let _pruneInterval = null;
  let _feedTimestampInterval = null;
  let activeNodeDetailKey = null;

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
    replayGen: 0,            // generation counter — incremented on each replay/rewind to discard stale async results
  };
  // #1599 — drop live WS packets during a manual replay handoff without
  // entering VCR PAUSED (which freezes the canvas engine and kills the very
  // animation we want to play). The handoff sets this true, then clears it
  // after the replay window has elapsed.
  let suppressLive = false;

  // ROLE_COLORS loaded from shared roles.js (includes 'unknown')

  const TYPE_COLORS = window.TYPE_COLORS || {
    ADVERT: '#22c55e', GRP_TXT: '#3b82f6', TXT_MSG: '#f59e0b', ACK: '#6b7280',
    REQ: '#a855f7', RESPONSE: '#06b6d4', TRACE: '#ec4899', PATH: '#14b8a6',
    ANON_REQ: '#f43f5e', GRP_DATA: '#8b5cf6', MULTIPART: '#0d9488',
    CONTROL: '#b45309', RAW_CUSTOM: '#c026d3'
  };

  // #1804 r1 item 7 (adv3): legend builder extracted from the live-overlay
  // template IIFE so it is testable in isolation. See
  // test-live-legend-helper.js. Emits one <li data-enum="<ENUM>">…</li>
  // per entry in ORDER, each row formatted as `SHORT — LONG`.
  //
  // #1804 r1 item 9 (adv6): inline fallback dropped. The top-of-file
  // guard already crashed if PayloadLabels was missing, so by the time
  // we reach here the canonical map is guaranteed to be there.
  function buildLegendHtml(PL) {
    var src = (PL && PL.api) ? PL.api : PL;
    var enums = (PL && PL.enums) ? PL.enums : PL;
    var order = src.ORDER;
    return order.map(function (k) {
      var e = enums[k];
      if (!e) return '';
      var color = TYPE_COLORS[k] || '#888';
      var label = e.short + ' \u2014 ' + e.long;
      return '<li data-enum="' + k + '"><span class="live-dot" style="background:' + color + '" aria-hidden="true"></span> ' + label + '</li>';
    }).join('');
  }
  // Expose for tests + downstream consumers. The unit test reads this
  // via vm; in-page consumers can pull it off window for debugging.
  if (typeof window !== 'undefined') { window.buildLegendHtml = buildLegendHtml; }

  const PAYLOAD_ICONS = {
    ADVERT: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg>', GRP_TXT: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chat-circle"/></svg>', TXT_MSG: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-envelope"/></svg>', ACK: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check"/></svg>',
    REQ: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg>', RESPONSE: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-envelope"/></svg>', TRACE: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg>', PATH: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-path"/></svg>'
  };

  /* ---- Panel Corner Positioning (#608 M0) ---- */
  var PANEL_DEFAULTS = { liveFeed: 'bl', liveLegend: 'br', liveNodeDetail: 'tr' };
  var CORNER_CYCLE = ['tl', 'tr', 'br', 'bl'];
  var CORNER_ARROWS = { tl: '↘', tr: '↙', bl: '↗', br: '↖' };
  var CORNER_LABELS = { tl: 'top-left', tr: 'top-right', bl: 'bottom-left', br: 'bottom-right' };
  var PANEL_NAMES = { liveFeed: 'Feed', liveLegend: 'Legend', liveNodeDetail: 'Node detail' };

  function getPanelPositions() {
    var pos = {};
    for (var id in PANEL_DEFAULTS) {
      try { pos[id] = localStorage.getItem('panel-corner-' + id) || PANEL_DEFAULTS[id]; }
      catch (_) { pos[id] = PANEL_DEFAULTS[id]; }
    }
    return pos;
  }

  function nextAvailableCorner(panelId, desired, allPositions) {
    var idx = CORNER_CYCLE.indexOf(desired);
    for (var i = 0; i < 4; i++) {
      var candidate = CORNER_CYCLE[(idx + i) % 4];
      var occupied = false;
      for (var otherId in allPositions) {
        if (otherId !== panelId && allPositions[otherId] === candidate) { occupied = true; break; }
      }
      if (!occupied) return candidate;
    }
    return desired; // all occupied (impossible with 3 panels, 4 corners)
  }

  function applyPanelPosition(id, corner) {
    var el = document.getElementById(id);
    if (!el) return;
    el.setAttribute('data-position', corner);
    var btn = el.querySelector('.panel-corner-btn');
    if (btn) {
      btn.textContent = CORNER_ARROWS[corner];
      btn.setAttribute('aria-label',
        'Move ' + (PANEL_NAMES[id] || 'panel') + ' to next corner (currently ' + CORNER_LABELS[corner] + ')');
    }
  }

  function initPanelPositions() {
    var positions = getPanelPositions();
    for (var id in positions) {
      applyPanelPosition(id, positions[id]);
    }
    // Wire up click handlers on corner buttons
    var btns = document.querySelectorAll('.panel-corner-btn[data-panel]');
    for (var i = 0; i < btns.length; i++) {
      btns[i].addEventListener('click', function(e) {
        e.stopPropagation();
        var panelId = this.getAttribute('data-panel');
        onCornerClick(panelId);
      });
    }
  }

  function onCornerClick(panelId) {
    var positions = getPanelPositions();
    var current = positions[panelId];
    var nextIdx = (CORNER_CYCLE.indexOf(current) + 1) % 4;
    var next = nextAvailableCorner(panelId, CORNER_CYCLE[nextIdx], positions);
    try { localStorage.setItem('panel-corner-' + panelId, next); } catch (_) { /* quota */ }
    // #1567: corner button must clear any prior free-form drag state, or
    // the inline top/left from drag-manager.js wins the cascade over the
    // corner anchors and the panel silently no-ops on click.
    // #1568 round-1 MAJOR 2: shared cleaner — keeps Escape revert,
    // responsive gate, corner-click, and reset paths in sync.
    if (window.DragManager && DragManager.clearPanel) {
      DragManager.clearPanel(document.getElementById(panelId), panelId);
    }
    applyPanelPosition(panelId, next);
    // Announce for screen readers
    var announce = document.getElementById('panelPositionAnnounce');
    if (announce) announce.textContent = (PANEL_NAMES[panelId] || 'Panel') + ' moved to ' + CORNER_LABELS[next];
  }

  function resetPanelPositions() {
    for (var id in PANEL_DEFAULTS) {
      try { localStorage.removeItem('panel-corner-' + id); } catch (_) { /* ignore */ }
      // #1568 round-1 MAJOR 1: clear drag state before applying defaults,
      // otherwise a dragged panel's inline coords win the cascade.
      if (window.DragManager && DragManager.clearPanel) {
        DragManager.clearPanel(document.getElementById(id), id);
      }
      applyPanelPosition(id, PANEL_DEFAULTS[id]);
    }
  }

  // Export for testing
  if (typeof window !== 'undefined') {
    window._panelCorner = {
      PANEL_DEFAULTS: PANEL_DEFAULTS, CORNER_CYCLE: CORNER_CYCLE,
      getPanelPositions: getPanelPositions, nextAvailableCorner: nextAvailableCorner,
      applyPanelPosition: applyPanelPosition, onCornerClick: onCornerClick,
      resetPanelPositions: resetPanelPositions
    };
  }

  function formatLiveTimestampHtml(isoLike) {
    if (typeof formatTimestampWithTooltip !== 'function' || typeof getTimestampMode !== 'function') {
      return escapeHtml(typeof timeAgo === 'function' ? timeAgo(isoLike) : '—');
    }
    const d = isoLike ? new Date(isoLike) : null;
    const iso = d && isFinite(d.getTime()) ? d.toISOString() : null;
    const f = formatTimestampWithTooltip(iso, getTimestampMode());
    const warn = f.isFuture
      ? ' <span class="timestamp-future-icon" title="Timestamp is in the future — node clock may be skewed"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg></span>'
      : '';
    return `<span class="timestamp-text" title="${escapeHtml(f.tooltip)}">${escapeHtml(f.text)}</span>${warn}`;
  }

  function initResizeHandler() {
    let resizeTimer = null;
    _onResize = function() {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        // Set live-page height from JS — most reliable across all mobile browsers
        const page = document.querySelector('.live-page');
        const appEl = document.getElementById('app');
        // #1267: the CSS rule for .live-page subtracts --bottom-nav-reserve
        // (0px desktop, 56px+safe-area at ≤768px) so the fixed .bottom-nav
        // (z-index 1200) does not occlude the VCR bar (position:absolute;
        // bottom:0; z-index 1000). Mirror that subtraction here — otherwise
        // this JS override clobbers the CSS height with raw window.innerHeight
        // and the VCR bar slides under the bottom-nav (issue #1267).
        // Prefer the bottom-nav's measured rendered height so we also cover
        // the 1px top border and any visual chrome the --bottom-nav-reserve
        // token doesn't account for; fall back to the token-resolved value.
        const reserve = (() => {
          const bn = document.querySelector('.bottom-nav');
          if (bn) {
            const cs = getComputedStyle(bn);
            if (cs.display !== 'none') {
              const r = bn.getBoundingClientRect().height;
              if (r > 0) return r;
            }
          }
          const probe = document.createElement('div');
          probe.style.cssText = 'position:absolute;visibility:hidden;height:var(--bottom-nav-reserve,0px);pointer-events:none;';
          (document.body || document.documentElement).appendChild(probe);
          const px = probe.getBoundingClientRect().height || 0;
          probe.remove();
          return px;
        })();
        const h = Math.max(0, window.innerHeight - reserve);
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

  // #1206: publish the VCR bar's measured height as --vcr-bar-height on the
  // .live-page root so bottom-pinned overlays (feed, legend, corner panels)
  // can reserve the right amount of space and never get occluded by the bar.
  // Cleanup state is captured in module-scoped _vcrHeightCleanup so destroy()
  // can disconnect the ResizeObserver + remove the resize/visualViewport
  // listeners on SPA page navigation (otherwise re-mounts of /live would
  // accumulate observers forever — same leak class as #1180).
  var _vcrHeightCleanup = null;
  function initVCRHeightTracker() {
    // #1206 r1 (adversarial should-fix): guard against double-init —
    // if a prior tracker is still active (re-mount race, dev hot-reload),
    // tear it down BEFORE overwriting _vcrHeightCleanup so the previous
    // ResizeObserver/listeners aren't orphaned.
    if (_vcrHeightCleanup) { try { _vcrHeightCleanup(); } catch (_) {} _vcrHeightCleanup = null; }
    var bar = document.getElementById('vcrBar');
    var page = document.querySelector('.live-page');
    if (!bar || !page) return;
    function publish() {
      var h = Math.ceil(bar.getBoundingClientRect().height) || 58;
      page.style.setProperty('--vcr-bar-height', h + 'px');
      // #1568 round-2: also publish on :root so JS reading
      // getComputedStyle(document.documentElement).getPropertyValue(
      // '--vcr-bar-height') sees the measured value (E2E assertions,
      // future global consumers). CSS resolution unchanged — the
      // .live-page-scoped var still wins for live-overlay rules.
      try { document.documentElement.style.setProperty('--vcr-bar-height', h + 'px'); } catch (_) {}
    }
    publish();
    var ro = null;
    if (typeof ResizeObserver === 'function') {
      try { ro = new ResizeObserver(publish); ro.observe(bar); } catch (_) { ro = null; }
    }
    window.addEventListener('resize', publish);
    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', publish);
    }
    _vcrHeightCleanup = function() {
      if (ro) { try { ro.disconnect(); } catch (_) {} ro = null; }
      window.removeEventListener('resize', publish);
      if (window.visualViewport) {
        window.visualViewport.removeEventListener('resize', publish);
      }
    };
  }

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
    VCR.replayGen++; // invalidate any in-flight async chunk processing
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
    VCR.replayGen++;
    var gen = VCR.replayGen;
    vcrSetMode('REPLAY');

    // Refresh map nodes to match the replay time. Don't tear every dot down
    // (clearNodeMarkers) — that re-renders the whole node layer and flickers on
    // each scrub. Clear only the transient animation/path layers; loadNodes()
    // reconciles the node set in place (keeps shared dots, removes only nodes
    // absent at the target time, adds genuinely new ones).
    if (animLayer) animLayer.clearLayers();
    if (pathsLayer) pathsLayer.clearLayers();
    loadNodes(targetTs);

    // Fetch packets from scrub point forward (ASC order, no limit clipping from the wrong end)
    fetch(`/api/packets?limit=10000&grouped=false&expand=observations&since=${encodeURIComponent(fetchFrom)}&order=asc`)
      .then(r => r.json())
      .then(data => {
        const pkts = data.packets || [];
        return expandToBufferEntriesAsync(pkts);
      })
      .then(function(replayEntries) {
        if (gen !== VCR.replayGen) return; // stale async result — user changed mode
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
    VCR.replayGen++;
    var gen = VCR.replayGen;
    // Fetch packets from DB for the time window
    const now = Date.now();
    const from = new Date(now - ms).toISOString();
    fetch(`/api/packets?limit=${window.LIVE_MAP_MAX_NODES}&grouped=false&expand=observations&since=${encodeURIComponent(from)}`)
      .then(r => r.json())
      .then(data => {
        const pkts = (data.packets || []).reverse(); // oldest first
        // Prepend to buffer (avoid duplicates by ID)
        const existingIds = new Set(VCR.buffer.map(b => b.pkt.id).filter(Boolean));
        const filtered = pkts.filter(p => !existingIds.has(p.id));
        return expandToBufferEntriesAsync(filtered);
      })
      .then(function(newEntries) {
        if (gen !== VCR.replayGen) return; // stale async result
        VCR.buffer = [].concat(newEntries, VCR.buffer);
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
    var gen = VCR.replayGen;
    const since = new Date(last.ts + 1).toISOString(); // +1ms to avoid dupe
    return fetch(`/api/packets?limit=10000&grouped=false&expand=observations&since=${encodeURIComponent(since)}&order=asc`)
      .then(r => r.json())
      .then(data => {
        const pkts = data.packets || [];
        if (pkts.length === 0) return false;
        return expandToBufferEntriesAsync(pkts).then(function(newEntries) {
          if (gen !== VCR.replayGen) return false; // stale
          VCR.buffer = VCR.buffer.concat(newEntries);
          return true;
        });
      })
      .catch(() => false);
  }

  function stopReplay() {
    if (VCR.replayTimer) { clearTimeout(VCR.replayTimer); VCR.replayTimer = null; }
  }

  function buildClickablePathPopupHtml(typeName, color, hopNames, tsMs, hash) {
    // tsMs is packet receive time — "ago" is relative to when the packet arrived, not when the animation ended
    const secsAgo = Math.round((Date.now() - tsMs) / 1000);
    const timeStr = secsAgo < 60 ? secsAgo + 's ago' : Math.round(secsAgo / 60) + 'm ago';
    // hopNames originate from adv_name (~line 3199) — attacker-controlled. Escape
    // each before joining into the popup HTML. (#1536)
    const chain = (hopNames || []).map(escapeHtml).join(' → ');
    const link = hash ? `<a class="lc-path-link" href="#/packets/${hash}" style="color:${color}">full detail →</a>` : '';
    return `<div class="lc-path-popup">
      <span class="lc-path-badge" style="background:${color}">${typeName}</span>
      <div class="lc-path-time">${timeStr}</div>
      <div class="lc-path-chain">${chain}</div>
      ${link ? '<div class="lc-path-link-wrap">' + link + '</div>' : ''}
    </div>`;
  }

  function pruneClickablePaths(now) {
    const cutoff = now - CLICKABLE_PATH_TTL_MS;
    for (let i = clickablePaths.length - 1; i >= 0; i--) {
      if (clickablePaths[i].addedAt < cutoff) {
        try { clickablePaths[i].poly.remove(); } catch (_) {}
        clickablePaths.splice(i, 1);
      }
    }
    while (clickablePaths.length > CLICKABLE_PATH_MAX) {
      try { clickablePaths[0].poly.remove(); } catch (_) {}
      clickablePaths.shift();
    }
  }

  function registerClickablePath(latLngs, typeName, color, hopNames, tsMs, hash) {
    if (!clickablePathsLayer) return;
    const poly = L.polyline(latLngs, { weight: 12, opacity: 0, interactive: true }).addTo(clickablePathsLayer);
    const entry = { addedAt: Date.now(), poly };
    clickablePaths.push(entry);
    pruneClickablePaths(Date.now());
    let dismissTimer = null;
    poly.on('click', function(e) {
      if (dismissTimer) clearTimeout(dismissTimer);
      const html = buildClickablePathPopupHtml(typeName, color, hopNames, tsMs, hash);
      L.popup({ maxWidth: 280, className: 'path-info-popup' })
        .setLatLng(e.latlng)
        .setContent(html)
        .openOn(map);
      dismissTimer = setTimeout(() => { if (map) map.closePopup(); }, CLICKABLE_POPUP_DISMISS_MS);
    });
  }

  function speedLabel(s) {
    if (s === 0.25) return '¼x';
    if (s === 0.5) return '½x';
    return s + 'x';
  }

  function vcrSpeedCycle() {
    const speeds = [0.25, 0.5, 1, 2, 4, 8];
    const idx = speeds.indexOf(VCR.speed);
    VCR.speed = speeds[(idx + 1) % speeds.length];
    localStorage.setItem('live-vcr-speed', VCR.speed);
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

  function wakeCanvasEngine() {
    if (!isAnimating && activeAnimations.length > 0) {
      const isPaused = VCR.mode === 'PAUSED' || VCR.speed === 0;
      if (!isPaused) {
        isAnimating = true;
        requestAnimationFrame(renderAnimations);
      }
    }
  }

  function vcrFormatTime(tsMs) {
    const d = new Date(tsMs);
    const utc = typeof getTimestampTimezone === 'function' && getTimestampTimezone() === 'utc';
    const hh = String(utc ? d.getUTCHours() : d.getHours()).padStart(2, '0');
    const mm = String(utc ? d.getUTCMinutes() : d.getMinutes()).padStart(2, '0');
    const ss = String(utc ? d.getUTCSeconds() : d.getSeconds()).padStart(2, '0');
    return `${hh}:${mm}:${ss}`;
  }

  function updateVCRClock(tsMs) {
    drawLcdText(vcrFormatTime(tsMs), statusGreen());
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
    if (speedBtn) { speedBtn.textContent = speedLabel(VCR.speed); speedBtn.setAttribute('aria-label', 'Speed ' + speedLabel(VCR.speed)); }
    updateVCRLcd();

    // WAKE THE ENGINE: If we unpaused or changed speed above 0, kickstart the canvas
    wakeCanvasEngine();
  }

  function dbPacketToLive(pkt) {
    const raw = getParsedDecoded(pkt);
    const hops = getParsedPath(pkt);
    const typeName = raw.type || pkt.payload_type_name || 'UNKNOWN';
    return {
      id: pkt.id, hash: pkt.hash,
      raw: pkt.raw_hex,
      path_json: pkt.path_json,
      resolved_path: pkt.resolved_path,
      _ts: new Date(pkt.timestamp || pkt.created_at).getTime(),
      decoded: { header: { payloadTypeName: typeName }, payload: raw, path: { hops } },
      snr: pkt.snr, rssi: pkt.rssi, observer: pkt.observer_name,
      // #1898: the region filter matches on observer_id (packetMatchesRegion,
      // line ~85). Without it every replayed packet has observer_id undefined,
      // so the filter skips them all and drops the whole group. observer_iata
      // is carried too so obsIataBadgeHtml does not have to fall back to the
      // roster map for replayed packets.
      observer_id: pkt.observer_id, observer_iata: pkt.observer_iata
    };
  }

  // Expand a DB packet (with optional observations[]) into VCR buffer entries
  /**
   * Process packets into buffer entries in chunks to avoid blocking the main thread.
   * Returns a Promise that resolves with the entries array.
   * Each chunk processes CHUNK_SIZE packets, then yields to the event loop via setTimeout(0).
   */
  var VCR_CHUNK_SIZE = 200;
  function expandToBufferEntriesAsync(pkts) {
    return new Promise(function(resolve) {
      var entries = [];
      var i = 0;
      function processChunk() {
        var end = Math.min(i + VCR_CHUNK_SIZE, pkts.length);
        for (; i < end; i++) {
          var p = pkts[i];
          if (p.observations && p.observations.length > 0) {
            for (var j = 0; j < p.observations.length; j++) {
              var obs = p.observations[j];
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
        if (i < pkts.length) {
          setTimeout(processChunk, 0);
        } else {
          resolve(entries);
        }
      }
      processChunk();
    });
  }

  // Synchronous version kept for small datasets and backward compat (tests)
  function expandToBufferEntries(pkts) {
    var entries = [];
    for (var k = 0; k < pkts.length; k++) {
      var p = pkts[k];
      if (p.observations && p.observations.length > 0) {
        for (var j = 0; j < p.observations.length; j++) {
          var obs = p.observations[j];
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
      // Batch-update timeline once on restore instead of per-packet while hidden
      updateTimeline();
    }
  });

  function packetTimestamp(pkt) {
    return new Date(pkt.timestamp || pkt.created_at || Date.now()).getTime();
  }
  if (typeof window !== 'undefined') window._live_packetTimestamp = packetTimestamp;

  function bufferPacket(pkt) {
    pkt._ts = packetTimestamp(pkt);
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
        return;
      }
      // #1599 — manual replay handoff sets suppressLive=true so incoming live
      // WS packets don't clutter the replay animation. We still want the
      // timeline to tick so the UI shows traffic continuing.
      if (suppressLive) { updateTimeline(); return; }
      if (realisticPropagation && pkt.hash) {
        const hash = pkt.hash;
        if (propagationBuffer.has(hash)) {
          propagationBuffer.get(hash).packets.push(pkt);
        } else {
          const entry = {
            packets: [pkt], timer: setTimeout(() => {
            const buffered = propagationBuffer.get(hash);
            propagationBuffer.delete(hash);
            if (buffered) renderPacketTree(buffered.packets);
            }, PROPAGATION_BUFFER_MS)
          };
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


  /** Restore persisted state and attach listeners for the eight persisted
   * live-view toggles: heat, inferred hops, realistic, colour-by-hash,
   * favourites, multibyte-only, matrix and rain.
   *
   * MUST be called synchronously right after `app.innerHTML`, before init()
   * awaits anything. Until it runs, those checkboxes are in the DOM, clickable
   * and inert: a click flips `.checked` and no handler fires, so nothing
   * reaches localStorage. For the seven toggles whose state is assigned
   * unconditionally the click is then also undone by `.checked = <pref>`; for
   * the heat toggle, whose old code only assigned on an explicit stored value,
   * a first-time visitor's click survived visually but still did nothing.
   *
   * Restoring state and attaching listeners must therefore never sit behind an
   * await. Applying the visible EFFECT of a restored setting may -- that is
   * applyLiveControlEffects(), called once the map and markers exist.
   *
   * NOT covered here, deliberately: #liveAudioToggle still has the same
   * dead window (MeshAudio persists 'live-audio-enabled' to localStorage and
   * the toggle is wired after the awaits), but its restore path runs through
   * MeshAudio.restore() and a whole slider panel, so moving it is its own
   * change. #liveGeoFilterToggle stays hidden until its own config fetch
   * resolves, so its window is not user-reachable. The fullscreen control is
   * not in this template at all -- Leaflet creates it after the map exists.
   */
  function wireLiveControls() {
    // One table instead of eight near-identical blocks, so the lookup is
    // guarded in exactly one place and the restore/persist halves of each
    // toggle sit next to each other instead of hundreds of lines apart.
    const TOGGLES = [
      { id: 'liveHeatToggle',
        restore: () => heatEnabled,
        onChange: (v) => {
          heatEnabled = v;
          localStorage.setItem('meshcore-live-heatmap', heatEnabled);
          if (v) showHeatMap(); else hideHeatMap();
        } },
      { id: 'liveGhostToggle',
        restore: () => showGhostHops,
        onChange: (v) => {
          showGhostHops = v;
          localStorage.setItem('live-ghost-hops', showGhostHops);
        } },
      { id: 'liveRealisticToggle',
        restore: () => realisticPropagation,
        onChange: (v) => {
          realisticPropagation = v;
          localStorage.setItem('live-realistic-propagation', realisticPropagation);
        } },
      { id: 'liveColorHashToggle',
        restore: () => colorByHash,
        onChange: (v) => {
          colorByHash = v;
          localStorage.setItem('meshcore-color-packets-by-hash', colorByHash);
          window.dispatchEvent(new Event('storage'));
        } },
      { id: 'liveFavoritesToggle',
        restore: () => showOnlyFavorites,
        onChange: (v) => {
          showOnlyFavorites = v;
          localStorage.setItem('live-favorites-only', showOnlyFavorites);
          applyFavoritesFilter();
        } },
      { id: 'liveMultibyteToggle',
        restore: () => multibyteOnly,
        onChange: (v) => {
          multibyteOnly = v;
          localStorage.setItem('live-multibyte-only', multibyteOnly);
          rebuildFeedList();
        } },
      { id: 'liveMatrixToggle',
        restore: () => matrixMode,
        onChange: (v) => {
          matrixMode = v;
          localStorage.setItem('live-matrix-mode', matrixMode);
          applyMatrixTheme(matrixMode);
          syncHeatToggleToMatrix(matrixMode);
        } },
      { id: 'liveMatrixRainToggle',
        restore: () => matrixRain,
        onChange: (v) => {
          matrixRain = v;
          localStorage.setItem('live-matrix-rain', matrixRain);
          if (matrixRain) startMatrixRain(); else stopMatrixRain();
        } },
    ];
    for (const t of TOGGLES) {
      // Guarded: this now runs before `L.map()`, so an unguarded throw would
      // reject init() and leave the whole route blank. (The old inline blocks
      // were unguarded too, but sat late in init(): a missing id cost the rest
      // of the wiring from that point on, while the map, feed and WS survived.)
      const el = document.getElementById(t.id);
      if (!el) { console.warn('[live] control not found: ' + t.id); continue; }
      el.checked = t.restore();
      el.addEventListener('change', (e) => t.onChange(e.target.checked));
    }
    // The matrix<->heat interlock applies from the first paint too: with matrix
    // stored ON the heat box would otherwise render checked and enabled until
    // applyLiveControlEffects() runs after the awaits. Safe before the map
    // exists -- hideHeatMap() guards on heatLayer, which is unset here.
    syncHeatToggleToMatrix(matrixMode);
  }

  /** Matrix mode owns the heat map: while it is on, heat is off and its toggle
   * is disabled. Extracted because the same block ran both in the matrix change
   * handler and at load; two copies of an interlock drift.
   */
  function syncHeatToggleToMatrix(on) {
    const ht = document.getElementById('liveHeatToggle');
    if (on) {
      hideHeatMap();
      if (ht) { ht.checked = false; ht.disabled = true; }
    } else if (ht) {
      // Recover from stale state: heat stays usable whenever matrix is off.
      ht.disabled = false;
    }
  }

  /** Apply the effects of the restored control state.
   *
   * Split out of wireLiveControls() because these need state that init() builds
   * behind its awaits: applyMatrixTheme() recolours the existing `nodeMarkers`,
   * and the heat-map calls act on `heatLayer`/`map`. Deliberately NOT a
   * prerequisite for the controls being usable.
   */
  function applyLiveControlEffects() {
    applyMatrixTheme(matrixMode);
    syncHeatToggleToMatrix(matrixMode);
    if (matrixRain) startMatrixRain();
  }

  async function init(app) {
    app.innerHTML = `
      <div class="live-page">
        <div id="liveMap" style="width:100%;height:100%;position:absolute;top:0;left:0;z-index:1"></div>
        <div class="live-overlay live-header" id="liveHeader">
          <div class="live-header-body" data-live-header-body id="liveHeaderBody">
            <div class="live-title">
              MESH LIVE
            </div>
          </div>
          <div class="live-header-critical" data-live-header-critical>
            <span class="live-beacon" aria-label="WebSocket connection beacon"></span>
            <div class="live-stat-pill live-stat-pill--critical"><span id="livePktCount">0</span> pkts</div>
          </div>
          <!-- #1234: stats row promoted to a direct child of .live-header so
               the counters are always visible inline on mobile (single-row
               header, no MESH LIVE label, no chart toggle). At desktop
               this also flows inline next to the title via flex. -->
          <div class="live-stats-row" data-live-stats-row>
            <div class="live-stat-pill"><span id="liveNodeCount">0</span> nodes</div>
            <div class="live-stat-pill anim-pill"><span id="liveAnimCount">0</span> active</div>
            <div class="live-stat-pill rate-pill"><span id="livePktRate">0</span>/min</div>
          </div>
          <button class="live-header-toggle" data-live-header-toggle id="liveHeaderToggle"
                  aria-expanded="false" aria-controls="liveHeaderBody"
                  aria-label="Show live stats"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg></button>

        <!-- #1205: settings toggles are children of the MESH LIVE panel
             (#liveHeader), not a free-floating .live-overlay. PR #1180
             detached them; this restores the pre-regression structure. -->
        <div class="live-controls" id="liveControls">
          <div class="live-controls-body" data-live-controls-body id="liveControlsBody">
            <div class="live-toggles">
            <label><input type="checkbox" id="liveHeatToggle" checked aria-describedby="heatDesc"> Heat</label>
            <span id="heatDesc" class="sr-only">Overlay a density heat map on the mesh nodes</span>
            <label><input type="checkbox" id="liveGhostToggle" checked aria-describedby="ghostDesc"> Inferred Hops</label>
            <span id="ghostDesc" class="sr-only">Show inferred hop markers for unknown hops</span>
            <label><input type="checkbox" id="liveRealisticToggle" aria-describedby="realisticDesc"> Realistic</label>
            <span id="realisticDesc" class="sr-only">Buffer packets by hash and animate all paths simultaneously</span>
            <label><input type="checkbox" id="liveColorHashToggle" aria-describedby="colorHashDesc"> Color by hash</label>
            <span id="colorHashDesc" class="sr-only">Color flying-packet dots and contrails by packet hash for propagation tracing</span>
            <label><input type="checkbox" id="liveMatrixToggle" aria-describedby="matrixDesc"> Matrix</label>
            <span id="matrixDesc" class="sr-only">Animate packet hex bytes flowing along paths like the Matrix</span>
            <label><input type="checkbox" id="liveMatrixRainToggle" aria-describedby="rainDesc"> Rain</label>
            <span id="rainDesc" class="sr-only">Matrix rain overlay — packets fall as hex columns</span>
            <label><input type="checkbox" id="liveAudioToggle" aria-describedby="audioDesc"> <svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-music-note"/></svg> Audio</label>
            <span id="audioDesc" class="sr-only">Sonify packets — turn raw bytes into generative music</span>
            <label><input type="checkbox" id="liveFavoritesToggle" aria-describedby="favDesc"> ⭐ Favorites</label>
            <span id="favDesc" class="sr-only">Show only favorited and claimed nodes</span>
            <label><input type="checkbox" id="liveMultibyteToggle" aria-describedby="multibyteDesc"> Multibyte only</label>
            <span id="multibyteDesc" class="sr-only">Show only multibyte (≥2-byte path-hash) packets; hide unreliable single-byte traffic</span>
            <label id="liveGeoFilterLabel" style="display:none"><input type="checkbox" id="liveGeoFilterToggle"> Mesh live area</label>
            </div>
            <div class="live-toggles">
              <div class="live-node-filter-wrap" style="position:relative">
                <label class="live-node-filter-hitarea" style="display:inline-flex; align-items:center; min-height:44px; cursor:text;">
                  <input type="text" id="liveNodeFilterInput" placeholder="Filter by node…" autocomplete="off" class="live-node-filter-input" role="combobox" aria-expanded="false" aria-owns="liveNodeFilterDropdown" aria-autocomplete="list" aria-activedescendant="">
                </label>
                <div id="liveNodeFilterDropdown" class="live-node-filter-dropdown hidden" role="listbox"></div>
                <button id="liveNodeFilterClear" class="vcr-btn" title="Clear node filter" style="display:none">×</button>
              </div>
              <div id="liveNodeFilterCount" class="live-filter-count hidden"></div>
            </div>
            <div id="liveRegionFilter" class="region-filter-container live-region-filter-container" aria-label="Filter live packets by IATA region"></div>
            <div id="liveAreaFilter" class="live-area-filter-container"></div>
            <div class="audio-controls hidden" id="audioControls">
              <label class="audio-slider-label">Voice <select id="audioVoiceSelect" class="audio-voice-select"></select></label>
              <label class="audio-slider-label">BPM <input type="range" id="audioBpmSlider" min="40" max="300" value="120" class="audio-slider"><span id="audioBpmVal">120</span></label>
              <label class="audio-slider-label">Vol <input type="range" id="audioVolSlider" min="0" max="100" value="30" class="audio-slider"><span id="audioVolVal">30</span></label>
            </div>
          </div>
        </div>
        </div><!-- /#liveHeader -->
        <div class="live-overlay live-feed" id="liveFeed">
          <div class="panel-header">
            <button class="panel-corner-btn" data-panel="liveFeed" title="Move panel to next corner" aria-label="Move panel to next corner">◫</button>
            <button class="feed-hide-btn" id="feedHideBtn" title="Hide feed" aria-label="Hide feed"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></button>
          </div>
          <div class="panel-content" aria-live="polite" aria-relevant="additions" role="log">
            <div class="live-feed-empty" aria-hidden="true">Waiting for packets…</div>
          </div>
        </div>
        <button class="feed-show-btn hidden" id="feedShowBtn" title="Show feed" aria-label="Show feed"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clipboard-text"/></svg></button>
        <div id="nodeDetailBackdrop" class="node-detail-backdrop"></div>
        <div class="live-overlay live-node-detail hidden" id="liveNodeDetail">
          <div class="panel-header">
            <button class="panel-corner-btn" data-panel="liveNodeDetail" title="Move panel to next corner" aria-label="Move panel to next corner">◫</button>
            <button class="feed-hide-btn" id="nodeDetailClose" title="Close" aria-label="Close"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></button>
          </div>
          <div class="panel-content" id="nodeDetailContent"></div>
        </div>
        <button class="legend-toggle-btn" id="legendToggleBtn" aria-label="Show legend" title="Show legend"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-palette"/></svg></button>
        <div class="live-overlay live-legend" id="liveLegend" role="region" aria-label="Map legend">
          <div class="panel-header">
            <button class="panel-corner-btn" data-panel="liveLegend" title="Move panel to next corner" aria-label="Move panel to next corner">◫</button>
          </div>
          <div class="panel-content">
          <h3 class="legend-title">PACKET TYPES</h3>
          <ul class="legend-list">
            ${buildLegendHtml(window.PayloadLabels || null)}
          </ul>
          <h3 class="legend-title" style="margin-top:8px">NODE ROLES</h3>
          <ul class="legend-list" id="roleLegendList"></ul>
          <h3 class="legend-title" style="margin-top:8px">MARKER STYLES</h3>
          <ul class="legend-list">
            <li><span class="live-ring live-ring--repeater" aria-hidden="true"></span> Bright white ring — repeater</li>
            <li><span class="live-ring live-ring--other" aria-hidden="true"></span> Faded ring — companion / sensor / room</li>
          </ul>
          </div>
        </div>

        <!-- VCR Bar -->
        <div class="sr-only" id="panelPositionAnnounce" aria-live="polite"></div>
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
            <button class="vcr-scope-btn vcr-scope-btn--overflow" data-scope="43200000" role="radio" aria-checked="false" aria-label="Scope 12 hours">12h</button>
            <button class="vcr-scope-btn vcr-scope-btn--overflow" data-scope="86400000" role="radio" aria-checked="false" aria-label="Scope 24 hours">24h</button>
            <!-- #1234: at ≤640px buttons marked .vcr-scope-btn--overflow are
                 hidden and surfaced via this More dropdown instead. -->
            <div class="vcr-scope-more-wrap" data-vcr-scope-more-wrap>
              <button type="button" class="vcr-scope-btn vcr-scope-more" data-vcr-scope-more
                      aria-haspopup="true" aria-expanded="false" aria-controls="vcrScopeMoreMenu"
                      aria-label="More timeline scopes">More ▾</button>
              <div class="vcr-scope-more-menu" id="vcrScopeMoreMenu" role="menu" hidden></div>
            </div>
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

    // Controls are wired BEFORE any await below -- see wireLiveControls().
    wireLiveControls();

    // Fetch configurable map defaults (#115)
    let mapCenter = [37.45, -122.0];
    let mapZoom = 9;
    try {
      const mapCfg = await (await fetch('/api/config/map')).json();
      if (Array.isArray(mapCfg.center) && mapCfg.center.length === 2) mapCenter = mapCfg.center;
      if (typeof mapCfg.zoom === 'number') mapZoom = mapCfg.zoom;
    } catch { }

    // #1709: URL hash lat/lon/zoom is the highest-precedence viewport source.
    // Applied here so the very first setView() lands at the requested viewport
    // (avoids a visible recenter from default → URL after init).
    const initVp = (typeof parseViewportHash === 'function')
      ? parseViewportHash(location.hash) : null;
    if (initVp) {
      mapCenter = [initVp.lat, initVp.lon];
      mapZoom = initVp.zoom;
    }

    map = L.map('liveMap', {
      zoomControl: false,
      attributionControl: false,
      zoomAnimation: true,
      markerZoomAnimation: true,
      preferCanvas: true
    }).setView(mapCenter, mapZoom);

    // 1. Create a custom pane for high-performance canvas animations
    map.createPane('animationsPane');

    // ARCHITECTURE NOTE - dual animation panes (#1514 S7):
    // Leaflet's default pane z-indexes:
    //   overlayPane: 400 (vector paths)
    //   markerPane:  600 (static node dots)
    //   tooltipPane: 650 (hover labels)
    //   popupPane:   700 (click details)
    //
    // We sandwich TWO panes between markerPane and tooltipPane on purpose:
    //   - 'animationsPane' (z=625): the canvas engine for in-flight packet
    //     animations and the post-flight fading polylines (#1514 M2). Sits
    //     ABOVE static node markers (so packets visibly fly over nodes) but
    //     UNDER tooltips/popups so hover/click affordances always win.
    //   - 'liveAnimPane' (z=650, created below): legacy SVG layer used by
    //     drawMatrixLine/animatePath/pulseNode/ghostMarkers (everything that
    //     hasn't been ported to the canvas engine yet). Kept just above
    //     animationsPane so SVG-based effects stack on top of canvas trails.
    //
    // Future work (#1514 out-of-scope): port the remaining SVG paths to the
    // canvas engine and collapse to a single pane.
    map.getPane('animationsPane').style.zIndex = 625;

    // Ensure mouse events pass through to the markers/map below
    map.getPane('animationsPane').style.pointerEvents = 'none';

    // 2. Create the canvas and inject into the pane
    animCanvas = document.createElement('canvas');
    // Leaflet uses translate3d for positioning, so we use absolute positioning but rely on L.DomUtil
    animCanvas.style.cssText = 'position:absolute; pointer-events:none;';

    map.getPane('animationsPane').appendChild(animCanvas);
    animCtx = animCanvas.getContext('2d');

    // 3. The Leaflet-native positioning function
    function updateAnimCanvas() {
      if (!animCanvas || !map) return;

      // Add a 20% buffer around the visible screen to prevent clipping during short pans
      const size = map.getSize();
      const padX = Math.round(size.x * 0.2);
      const padY = Math.round(size.y * 0.2);

      const w = size.x + padX * 2;
      const h = size.y + padY * 2;

    // Cap the backing-store DPR. The animation canvas is ~1.4x the screen area
    // (20% pad per side); at native devicePixelRatio 2-3 that means clearing and
    // redrawing 5-12x the screen's pixels every frame. Capping at 1.5 keeps lines
    // crisp while cutting per-frame fill cost by up to ~4x on hi-DPI displays.
    const dpr = Math.min(window.devicePixelRatio || 1, ANIM_MAX_DPR);

      // Updating width/height automatically clears the canvas
      animCanvas.width = w * dpr;
      animCanvas.height = h * dpr;
      animCanvas.style.width = w + 'px';
      animCanvas.style.height = h + 'px';

      animCtx.setTransform(dpr, 0, 0, dpr, 0, 0);

      // Find the absolute pixel bounds of the current viewport
      const pixelBounds = map.getPixelBounds();
      // Pad the bounds by 20% on all sides
      const min = pixelBounds.min.subtract([padX, padY]);

      // Convert absolute pixel coordinates to Layer points (relative to the pane)
      canvasTopLeft = min.subtract(map.getPixelOrigin());

      // Let Leaflet position the canvas inside the pane using CSS transforms
      L.DomUtil.setPosition(animCanvas, canvasTopLeft);
    }

    let _cullRAF = null;
    function cullMarkers() {
      if (!map || !nodesLayer) return;
      if (_cullRAF) return; // Skip if a cull is already queued for this frame

      _cullRAF = requestAnimationFrame(() => {
        _cullRAF = null;
        if (!map || !nodesLayer) return;

        let bounds;
        try { bounds = map.getBounds().pad(0.5); } catch (e) { return; }

        for (const key in nodeMarkers) {
          const marker = nodeMarkers[key];
          const isVisible = bounds.contains(marker.getLatLng());
          const hasLayer = nodesLayer.hasLayer(marker);

          if (isVisible && !hasLayer) nodesLayer.addLayer(marker);
          else if (!isVisible && hasLayer) nodesLayer.removeLayer(marker);
        }
      });
    }

    // 4. Hook into Leaflet's transition-end events
    map.on('moveend zoomend resize', () => {
      updateAnimCanvas();
      cullMarkers();
    });
    updateAnimCanvas();
    cullMarkers();

    // #1514 M1+S10 — DPR change handling.
    //
    // matchMedia(`(resolution: ${dpr}dppx)`) is a STRICT-MATCH query: it only
    // fires when the current DPR stops matching. After it fires, we must rebind
    // a new MQL keyed to the new DPR. Older code did remove → re-add inside a
    // try/finally which was fragile (a throw in updateAnimCanvas would still
    // re-bind, and a synchronous re-entry between remove and add could lose
    // the handler). The {once: true} pattern below removes the listener
    // atomically before our handler runs, so re-binding is race-free.
    function _rebindDPRListener() {
      if (_dprMedia && _dprChangeHandler) {
        try { _dprMedia.removeEventListener('change', _dprChangeHandler); } catch (_) {}
      }
      _dprMedia = window.matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
      _dprChangeHandler = () => {
        updateAnimCanvas();
        _rebindDPRListener();
      };
      _dprMedia.addEventListener('change', _dprChangeHandler, { once: true });
    }
    _rebindDPRListener();

    const isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
    // #1420 — multi-provider dark-tile picker. Light mode unchanged.
    let _liveDarkRefLayer = null;
    function _liveResolveTile(dark) {
      const reg = window.MC_TILE_PROVIDERS || {};
      if (!dark) {
        const lightId = (typeof window.MC_getLightTileProvider === 'function') ? window.MC_getLightTileProvider() : null;
        const lp = lightId ? (reg[lightId] || null) : null;
        if (lp && lp.url) {
          return { url: (typeof lp.url === 'function' ? lp.url() : lp.url), attribution: lp.attribution || '© OpenStreetMap © CartoDB', refUrl: null };
        }
        return { url: TILE_LIGHT, attribution: '© OpenStreetMap © CartoDB', refUrl: null };
      }
      const id  = (typeof window.MC_getDarkTileProvider === 'function') ? window.MC_getDarkTileProvider() : 'carto-dark';
      const p   = reg[id] || reg['carto-dark'] || {};
      return {
        url: (typeof p.url === 'function' ? p.url() : p.url) || (typeof p.baseUrl === 'function' ? p.baseUrl() : p.baseUrl) || TILE_DARK,
        attribution: p.attribution || '© OpenStreetMap © CartoDB',
        refUrl: p.refUrl || null
      };
    }
    const liveAutoLayerGroup = L.layerGroup().addTo(map);

    function _liveSyncDarkTiles(dark) {
      const r = _liveResolveTile(dark);
      tileLayer.setUrl(r.url);
      if (tileLayer.options) tileLayer.options.attribution = r.attribution;
      if (dark && r.refUrl) {
        if (!_liveDarkRefLayer) {
          _liveDarkRefLayer = L.tileLayer(r.refUrl, { maxZoom: 19, attribution: r.attribution }).addTo(liveAutoLayerGroup);
        } else {
          _liveDarkRefLayer.setUrl(r.refUrl);
          if (!liveAutoLayerGroup.hasLayer(_liveDarkRefLayer)) _liveDarkRefLayer.addTo(liveAutoLayerGroup);
        }
      } else if (_liveDarkRefLayer) {
        liveAutoLayerGroup.removeLayer(_liveDarkRefLayer);
        _liveDarkRefLayer = null;
      }
      if (typeof window.MC_applyTileFilter === 'function') window.MC_applyTileFilter();
      // #1420 parity with map.js — refresh visible attribution credit after provider swap.
      // Make sure the map is loaded before trying to update the ui
      if (map && map.attributionControl) {
        try { map.attributionControl._update && map.attributionControl._update(); } catch (_) {}
      }
    }
    const _liveInitTile = _liveResolveTile(isDark);
    let tileLayer = L.tileLayer(_liveInitTile.url, { maxZoom: 19, attribution: _liveInitTile.attribution }).addTo(liveAutoLayerGroup);
    if (isDark && _liveInitTile.refUrl) {
      _liveDarkRefLayer = L.tileLayer(_liveInitTile.refUrl, { maxZoom: 19, attribution: _liveInitTile.attribution }).addTo(liveAutoLayerGroup);
    }
    if (typeof window.MC_applyTileFilter === 'function') window.MC_applyTileFilter();
  
    // Add Zoom Control
    L.control.zoom({ position: 'topright' }).addTo(map);

    // Add Layer Control, passing 'topright' to put it on the right
    if (typeof window.MC_createLayerControl === 'function') {
      window.MC_createLayerControl(map, liveAutoLayerGroup, 'topright');
    }

    // Add custom Leaflet Control for Fullscreen
    const LiveFullscreenControl = L.Control.extend({
      options: { position: 'topright' },
      onAdd: function() {
        const container = L.DomUtil.create('div', 'leaflet-bar leaflet-control live-leaflet-toggle');
        const btn = L.DomUtil.create('a', '', container);
        btn.href = 'javascript:void(0)';
        btn.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-arrows-out"/></svg>';
        btn.id = 'liveFullscreenToggle';
        btn.title = 'Fullscreen (F)';
        btn.setAttribute('aria-label', 'Toggle fullscreen');
        btn.setAttribute('role', 'button');
        btn.setAttribute('aria-pressed', 'false');
        L.DomEvent.disableClickPropagation(btn);
        return container;
      }
    });
    map.addControl(new LiveFullscreenControl());

    // Add custom Leaflet Control for Settings
    const LiveSettingsControl = L.Control.extend({
      options: { position: 'topright' },
      onAdd: function() {
        const container = L.DomUtil.create('div', 'leaflet-bar leaflet-control live-leaflet-toggle');
        const btn = L.DomUtil.create('a', 'live-controls-toggle', container);
        btn.href = 'javascript:void(0)';
        btn.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-gear"/></svg>';
        btn.id = 'liveControlsToggle';
        btn.setAttribute('data-live-controls-toggle', '');
        btn.title = 'Settings';
        btn.setAttribute('aria-label', 'Show live controls');
        btn.setAttribute('role', 'button');
        btn.setAttribute('aria-expanded', 'false');
        btn.setAttribute('aria-controls', 'liveControlsBody');
        L.DomEvent.disableClickPropagation(btn);
        return container;
      }
    });
    map.addControl(new LiveSettingsControl());



    // Swap tiles when theme changes
    const _themeObs = new MutationObserver(function () {
      const dark = document.documentElement.getAttribute('data-theme') === 'dark' ||
        (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
      _liveSyncDarkTiles(dark);
    });
    _themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    // #1420 — re-render on customizer change.
    window.addEventListener('mc-tile-provider-changed', function (e) {
      const dark = document.documentElement.getAttribute('data-theme') === 'dark' ||
        (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
      if (e && e.detail && e.detail.type && e.detail.type !== (dark ? 'dark' : 'light')) return;
      _liveSyncDarkTiles(dark);
    });

    // #1485 — animations + trails need their own pane above markerPane.
    // PR #1334 moved node markers from L.circleMarker (overlayPane @ 400)
    // to L.marker+divIcon (markerPane @ 600); animations stayed in the
    // default overlayPane and were occluded by every node marker. Custom
    // pane @ 650 puts them strictly above all nodes. (tooltipPane shares
    // 650 — tooltips are user-triggered, harmless to share.)
    map.createPane('liveAnimPane');
    map.getPane('liveAnimPane').style.zIndex = 650;
    // Pointer-events default to none so the pane doesn't steal clicks
    // from the marker pane underneath (clickablePathsLayer handles that).
    map.getPane('liveAnimPane').style.pointerEvents = 'none';

    nodesLayer = L.layerGroup().addTo(map);
    pathsLayer = L.layerGroup({ pane: 'liveAnimPane' }).addTo(map);
    animLayer = L.layerGroup({ pane: 'liveAnimPane' }).addTo(map);
    clickablePathsLayer = L.layerGroup().addTo(map);

    injectSVGFilters();
    AreaFilter.init(document.getElementById('liveAreaFilter'));
    AreaFilter.onChange(function () { loadNodes(); });
    await loadNodes();
    // Build the heat layer only when the user wants it. It used to be built
    // unconditionally and torn down ~270 lines later, which was invisible
    // (no await in between, so no frame composited) but meant that any throw
    // between the two calls left the layer visible AGAINST the stored
    // preference. Matrix-forced hiding is deliberately not repeated here --
    // that interlock lives in syncHeatToggleToMatrix() and only there.
    if (heatEnabled) showHeatMap();
    connectWS();
    initResizeHandler();
    initVCRHeightTracker();
    startRateCounter();

    // Check for packet replay from packets page (single or array of observations)
    const replayData = sessionStorage.getItem('replay-packet');
    if (replayData) {
      sessionStorage.removeItem('replay-packet');
      try {
        const parsed = JSON.parse(replayData);
        const packets = Array.isArray(parsed) ? parsed : [parsed];
        // #1599 — mute live WS traffic but keep VCR in LIVE so the canvas
        // engine keeps advancing animation progress. Using vcrPause() here
        // set VCR.mode='PAUSED' which froze anim.progress in
        // renderAnimations(), turning the replay into a blank, motionless map.
        suppressLive = true;
        setTimeout(() => renderPacketTree(packets, true), 1500);
        // Clear the suppression after the replay window has elapsed (the
        // longest animation duration plus a margin) so live traffic resumes.
        setTimeout(() => { suppressLive = false; }, 12000);
      } catch { }
    } else {
      // replayRecent(); // disabled — live page starts empty, fills from WS
    }

    map.on('zoomend', rescaleMarkers);

    // Region filter (#1045): dropdown of observer IATA regions
    (function initLiveRegionFilter() {
      var rfEl = document.getElementById('liveRegionFilter');
      if (!rfEl || !window.RegionFilter) return;
      // Fetch observer roster to build observer_id → IATA map.
      // /api/observers returns `{observers:[...], server_time:"..."}`
      // (cmd/server/types.go ObserverListResponse) — NOT a top-level array.
      // Bug #1136: previously parsed as array → map empty → region filter
      // dropped every packet.
      fetch('/api/observers').then(function(r) { return r.json(); }).then(function(data) {
        setObserverIataMap(buildObserverIataMap(data));
      }).catch(function() { /* leave map empty; filter will hide all when active */ });
      RegionFilter.init(rfEl, { dropdown: true });
      regionFilterChangeHandler = RegionFilter.onChange(function() {
        // #1108 — when the region selection changes, reload visible map
        // nodes so non-region nodes disappear (or reappear) immediately.
        // The packet feed already filters live via packetMatchesRegion.
        try { loadNodes(); } catch (e) { /* loadNodes not yet defined during init order edge cases */ }
      });
      // #1108 — "Show all nodes (faded)" sub-toggle, sibling to the region
      // dropdown. Off by default = hide non-region nodes; on = legacy
      // show-everything behavior.
      (function initShowAllNodesToggle() {
        if (!window.RegionShowAll) return;
        var wrap = document.createElement('label');
        wrap.className = 'live-show-all-region-nodes';
        wrap.title = 'When a region is selected, show every node on the map (legacy behavior). Off = hide non-region nodes.';
        var cb = document.createElement('input');
        cb.type = 'checkbox';
        cb.id = 'liveShowAllRegionNodes';
        cb.checked = RegionShowAll.get();
        wrap.appendChild(cb);
        wrap.appendChild(document.createTextNode(' Show all nodes'));
        rfEl.parentNode.insertBefore(wrap, rfEl.nextSibling);
        cb.addEventListener('change', function() {
          RegionShowAll.set(cb.checked);
          try { loadNodes(); } catch (e) { }
        });
      })();
    })();

    // Node filter input — autocomplete-as-you-type (#1110)
    const nodeFilterInput = document.getElementById('liveNodeFilterInput');
    const nodeFilterClear = document.getElementById('liveNodeFilterClear');
    const nodeFilterDropdown = document.getElementById('liveNodeFilterDropdown');
    if (nodeFilterInput) {
      // Restore from URL param or localStorage
      const urlNode = getHashParams && getHashParams().get('node');
      if (urlNode) setNodeFilter(urlNode.split(',').map(s => s.trim()).filter(Boolean));
      else if (nodeFilterKeys.length) updateNodeFilterUI();

      let activeIdx = -1;

      function hideDropdown() {
        if (!nodeFilterDropdown) return;
        nodeFilterDropdown.classList.add('hidden');
        nodeFilterDropdown.innerHTML = '';
        nodeFilterInput.setAttribute('aria-expanded', 'false');
        nodeFilterInput.setAttribute('aria-activedescendant', '');
        activeIdx = -1;
      }

      function applyFilterFromInput(rawValue) {
        // Treat input as a single substring query rather than a list of pubkeys.
        // setNodeFilter accepts pubkeys/prefixes/names; commit raw for live filtering.
        const val = (rawValue || '').trim();
        setNodeFilter(val ? [val] : []);
        // Update URL without triggering hashchange (which would re-init the page).
        const params = getHashParams ? getHashParams() : new URLSearchParams();
        if (val) params.set('node', val);
        else params.delete('node');
        const base = location.hash.split('?')[0] || '#/live';
        const qs = params.toString();
        const newHash = base + (qs ? '?' + qs : '');
        const newUrl = location.pathname + location.search + newHash;
        try { history.replaceState(null, '', newUrl); } catch (_) {}
      }

      function selectSuggestion(opt) {
        const key = opt.getAttribute('data-key') || '';
        const name = opt.getAttribute('data-name') || key;
        nodeFilterInput.value = name;
        // Filter by pubkey prefix when available — most precise.
        setNodeFilter(key ? [key] : (name ? [name] : []));
        const params = getHashParams ? getHashParams() : new URLSearchParams();
        if (key) params.set('node', key);
        else params.delete('node');
        const base = location.hash.split('?')[0] || '#/live';
        const qs = params.toString();
        const newUrl = location.pathname + location.search + base + (qs ? '?' + qs : '');
        try { history.replaceState(null, '', newUrl); } catch (_) {}
        hideDropdown();
      }

      const escapeHtmlLocal = (typeof escapeHtml === 'function') ? escapeHtml : function (s) {
        return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
          return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c];
        });
      };

      async function fetchSuggestions(q) {
        if (!nodeFilterDropdown) return;
        if (!q || q.length < 1) { hideDropdown(); return; }
        try {
          const resp = await fetch('/api/nodes/search?q=' + encodeURIComponent(q));
          if (!resp.ok) { hideDropdown(); return; }
          const data = await resp.json();
          const nodes = (data && data.nodes) || [];
          if (!nodes.length) { hideDropdown(); return; }
          nodeFilterDropdown.innerHTML = nodes.map(function (n, i) {
            const name = n.name || (n.public_key ? n.public_key.slice(0, 8) : '?');
            const pkShort = n.public_key ? n.public_key.slice(0, 8) : '';
            return '<div class="live-node-filter-option" id="liveNodeFilterOpt-' + i +
              '" role="option" data-key="' + escapeHtmlLocal(n.public_key || '') +
              '" data-name="' + escapeHtmlLocal(name) + '">' +
              escapeHtmlLocal(name) +
              ' <span style="color:var(--text-muted);font-size:0.8em">' + escapeHtmlLocal(pkShort) + '</span></div>';
          }).join('');
          nodeFilterDropdown.classList.remove('hidden');
          nodeFilterInput.setAttribute('aria-expanded', 'true');
          nodeFilterDropdown.querySelectorAll('.live-node-filter-option').forEach(function (opt) {
            opt.addEventListener('mousedown', function (ev) {
              // Use mousedown so we run before blur hides the dropdown.
              ev.preventDefault();
              selectSuggestion(opt);
            });
          });
        } catch (_) { hideDropdown(); }
      }

      const debouncedInput = debounce(function (e) {
        const v = e.target.value.trim();
        // Apply live filter immediately as user types (no Enter required).
        applyFilterFromInput(v);
        fetchSuggestions(v);
      }, 200);

      nodeFilterInput.addEventListener('input', debouncedInput);

      nodeFilterInput.addEventListener('keydown', function (e) {
        const opts = nodeFilterDropdown ? nodeFilterDropdown.querySelectorAll('.live-node-filter-option') : [];
        if (e.key === 'Enter') {
          // Critical: prevent any default form submission / navigation behavior.
          e.preventDefault();
          if (opts.length && activeIdx >= 0 && opts[activeIdx]) {
            selectSuggestion(opts[activeIdx]);
          } else {
            // Just commit current text as a filter and close the dropdown.
            applyFilterFromInput(nodeFilterInput.value);
            hideDropdown();
          }
          return;
        }
        if (!opts.length || (nodeFilterDropdown && nodeFilterDropdown.classList.contains('hidden'))) return;
        if (e.key === 'ArrowDown') {
          e.preventDefault();
          activeIdx = Math.min(activeIdx + 1, opts.length - 1);
        } else if (e.key === 'ArrowUp') {
          e.preventDefault();
          activeIdx = Math.max(activeIdx - 1, 0);
        } else if (e.key === 'Escape') {
          hideDropdown();
          return;
        } else {
          return;
        }
        opts.forEach(function (o, i) {
          o.classList.toggle('live-node-filter-active', i === activeIdx);
          o.setAttribute('aria-selected', i === activeIdx ? 'true' : 'false');
        });
        if (activeIdx >= 0 && opts[activeIdx]) {
          nodeFilterInput.setAttribute('aria-activedescendant', opts[activeIdx].id);
          opts[activeIdx].scrollIntoView({ block: 'nearest' });
        }
      });

      nodeFilterInput.addEventListener('blur', function () {
        // Slight delay so click on a suggestion can register first.
        setTimeout(hideDropdown, 150);
      });
    }
    if (nodeFilterClear) {
      nodeFilterClear.addEventListener('click', () => {
        if (nodeFilterInput) nodeFilterInput.value = '';
        setNodeFilter([]);
        // Drop the ?node param without re-running the SPA route handler.
        const params = getHashParams ? getHashParams() : new URLSearchParams();
        params.delete('node');
        const base = location.hash.split('?')[0] || '#/live';
        const qs = params.toString();
        const newUrl = location.pathname + location.search + base + (qs ? '?' + qs : '');
        try { history.replaceState(null, '', newUrl); } catch (_) {}
      });
    }

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

    applyLiveControlEffects();

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
      
      if (voices.length <= 1) {
        voiceSelect.parentElement.style.display = 'none';
      }
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

    // ── Live header / controls toggles (#1178, #1179) ──────────────────────
    // At narrow viewports (≤768px) the header collapses to a single
    // toggle button revealing the stats body, and the controls collapse
    // to a single toggle button revealing the toggles list. CSS gates
    // visibility of the toggle buttons; JS only flips classes and the
    // hidden attribute. At wide viewports the bodies are always shown.
    (function wireLiveCollapseToggles() {
      var pairs = [
        {
          rootId: 'liveHeader', togId: 'liveHeaderToggle', bodyId: 'liveHeaderBody',
          showLabel: 'Show live stats', hideLabel: 'Hide live stats'
        },
        {
          rootId: 'liveControls', togId: 'liveControlsToggle', bodyId: 'liveControlsBody',
          showLabel: 'Show live controls', hideLabel: 'Hide live controls'
        },
      ];
      var narrowMql = window.matchMedia('(max-width: 768px)');
      function setExpanded(p, expanded) {
        var root = document.getElementById(p.rootId);
        var tog = document.getElementById(p.togId);
        var body = document.getElementById(p.bodyId);
        if (!root || !tog || !body) return;
        if (expanded) {
          root.classList.add('is-expanded'); root.classList.remove('is-collapsed');
          body.removeAttribute('hidden');
          tog.setAttribute('aria-expanded', 'true');
          tog.setAttribute('aria-label', p.hideLabel);
        } else {
          root.classList.add('is-collapsed'); root.classList.remove('is-expanded');
          body.setAttribute('hidden', '');
          tog.setAttribute('aria-expanded', 'false');
          tog.setAttribute('aria-label', p.showLabel);
        }
      }
      function applyForViewport() {
        for (var i = 0; i < pairs.length; i++) {
          var p = pairs[i];
          var root = document.getElementById(p.rootId);
          if (!root) continue;
          
          if (p.rootId === 'liveControls') {
            // #1532 - liveControls is an accordion on all viewports,
            // persisting state across reloads via localStorage.
            if (!root.classList.contains('is-expanded') && !root.classList.contains('is-collapsed')) {
              var startExpanded = false;
              try {
                if (localStorage.getItem('live-controls-expanded') === 'true') {
                  startExpanded = true;
                }
              } catch (_) { /* private browsing */ }
              setExpanded(p, startExpanded);
            }
          } else {
            // liveHeader is collapsible on narrow screens, permanently open on wide
            if (narrowMql.matches) {
              if (!root.classList.contains('is-expanded')) setExpanded(p, false);
            } else {
              var body = document.getElementById(p.bodyId);
              var tog = document.getElementById(p.togId);
              if (body) body.removeAttribute('hidden');
              root.classList.remove('is-collapsed'); 
              root.classList.remove('is-expanded');
              if (tog) tog.setAttribute('aria-expanded', 'true');
            }
          }
        }
      }
      pairs.forEach(function (p) {
        var tog = document.getElementById(p.togId);
        if (!tog) return;
        tog.addEventListener('click', function () {
          var root = document.getElementById(p.rootId);
          var nowExpanded = !(root && root.classList.contains('is-expanded'));
          setExpanded(p, nowExpanded);
          // #1532 — persist controls pin state across reloads.
          if (p.rootId === 'liveControls') {
            try { localStorage.setItem('live-controls-expanded', nowExpanded ? 'true' : 'false'); }
            catch (_) { /* private browsing */ }
          }
        });
      });
      applyForViewport();
      // #1180 — bind once across SPA re-mounts. MQL is process-global per
      // query string; per-init binds accumulate handlers without bound.
      if (!_liveNarrowMqlBound) {
        if (narrowMql.addEventListener) narrowMql.addEventListener('change', applyForViewport);
        else if (narrowMql.addListener) narrowMql.addListener(applyForViewport);
        _liveNarrowMqlBound = true;
        try {
          window.__liveMQLBindCount = (window.__liveMQLBindCount || 0) + 1;
        } catch (_) { /* sealed window */ }
      }
    })();
    // ───────────────────────────────────────────────────────────────────────

    // ── #1532 — Live fullscreen toggle ─────────────────────────────────────
    // Adds `body.live-fullscreen` which CSS uses to hide header body,
    // controls body, VCR controls, and bottom-nav while leaving
    // .live-stats-row pinned top-right. Triggered by:
    //   * clicking #liveFullscreenToggle (fullscreen button next to gear)
    //   • pressing the `F` key (when focus is not in an input/textarea)
    // State persists across reloads via localStorage('live-fullscreen').
    (function wireLiveFullscreenToggle() {
      var STORAGE_KEY = 'live-fullscreen';
      var btn = document.getElementById('liveFullscreenToggle');
      if (btn) btn.addEventListener('click', function () { toggleFullscreen(); });
      function setFullscreen(on) {
        document.body.classList.toggle('live-fullscreen', !!on);
        if (btn) {
          btn.setAttribute('aria-pressed', on ? 'true' : 'false');
          btn.innerHTML = on ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-arrows-out"/></svg>' : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-arrows-out"/></svg>';
          btn.setAttribute('aria-label', on
            ? 'Exit fullscreen (F)'
            : 'Toggle fullscreen (F) — hide chrome, keep stats');
        }
        try { localStorage.setItem(STORAGE_KEY, on ? 'true' : 'false'); }
        catch (_) { /* private browsing */ }
      }
      function toggleFullscreen() {
        setFullscreen(!document.body.classList.contains('live-fullscreen'));
      }
      // Restore prior choice on mount.
      try {
        if (localStorage.getItem(STORAGE_KEY) === 'true') setFullscreen(true);
      } catch (_) { /* ignore */ }

      // `F` keypress toggles fullscreen — but only when focus is NOT in
      // a typing surface (node-filter input, audio sliders, etc.).
      // Escape exits fullscreen (only when currently in fullscreen so we
      // don't shadow other Escape handlers, e.g. dropdown close on the
      // node-filter input).
      if (!window.__liveFullscreenKeyBound) {
        window.addEventListener('keydown', function (e) {
          if (e.defaultPrevented) return;
          if (typeof e.key !== 'string') return;
          // Only act when on the live page.
          if (!document.querySelector('.live-page')) return;
          // Escape: exit fullscreen if currently in fullscreen. Don't
          // gate on focus-in-input here — exiting fullscreen via Escape
          // should always work when chrome is hidden. Do NOT fire when
          // not currently in fullscreen so other handlers see the key.
          if (e.key === 'Escape') {
            if (document.body.classList.contains('live-fullscreen')) {
              e.preventDefault();
              setFullscreen(false);
            }
            return;
          }
          if (e.altKey || e.ctrlKey || e.metaKey) return;
          var isF = (e.key === 'f' || e.key === 'F' || e.key.toLowerCase() === 'f');
          if (!isF) return;
          var t = e.target;
          if (t) {
            var tag = (t.tagName || '').toUpperCase();
            if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
            if (t.isContentEditable) return;
          }
          e.preventDefault();
          toggleFullscreen();
        });
        window.__liveFullscreenKeyBound = true;
      }
    })();
    // ───────────────────────────────────────────────────────────────────────

    if (legendToggleBtn && legendEl) {
      // Restore legend collapsed state from localStorage (#279)
      try {
        if (localStorage.getItem('live-legend-hidden') === 'true') {
          legendEl.classList.add('hidden');
          legendToggleBtn.setAttribute('aria-label', 'Show legend');
          legendToggleBtn.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-palette"/></svg>';
        }
      } catch (_) { /* private browsing / storage disabled */ }
      legendToggleBtn.addEventListener('click', () => {
        const nowHidden = legendEl.classList.toggle('hidden');
        legendToggleBtn.setAttribute('aria-label', nowHidden ? 'Show legend' : 'Hide legend');
        legendToggleBtn.innerHTML = nowHidden ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-palette"/></svg>' : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg>';
        try { localStorage.setItem('live-legend-hidden', String(nowHidden)); } catch (_) { /* ignore */ }
      });
    }

    // Populate role legend from shared roles.js
    // Initialize panel corner positions (#608 M0)
    initPanelPositions();

    // Initialize DragManager for free-form panel dragging (#608 M1)
    if (window.DragManager) {
      var dragMgr = new DragManager();
      // #1619: expose so the feed-detail-card popup (constructed in a
      // different scope) can register itself as draggable.
      window._liveDragMgr = dragMgr;
      var dragPanels = ['liveFeed', 'liveLegend', 'liveNodeDetail'];
      for (var di = 0; di < dragPanels.length; di++) {
        dragMgr.register(document.getElementById(dragPanels[di]));
      }
      dragMgr.restorePositions();

      // Responsive gate: disable drag below medium breakpoint or on touch
      var dragMql = window.matchMedia('(pointer: fine) and (min-width: 768px)');
      function onDragMediaChange(e) {
        if (!e.matches) {
          // Revert dragged panels to corner positions. Preserve the
          // localStorage drag key so widening the viewport restores
          // the dragged position via dragMgr.restorePositions().
          // #1568 round-1 MAJOR 2: shared cleaner with clearStorage:false.
          document.querySelectorAll('.live-overlay[data-dragged="true"]').forEach(function (p) {
            DragManager.clearPanel(p, p.id, { clearStorage: false });
          });
          initPanelPositions();
          dragMgr.disable();
        } else {
          dragMgr.enable();
          dragMgr.restorePositions();
        }
      }
      dragMql.addEventListener('change', onDragMediaChange);
      // Initial check
      if (!dragMql.matches) dragMgr.disable();

      // Resize clamping (debounced)
      var resizeTimer = null;
      window.addEventListener('resize', function () {
        clearTimeout(resizeTimer);
        resizeTimer = setTimeout(function () { dragMgr.handleResize(); }, 200);
      });
    }

    const roleLegendList = document.getElementById('roleLegendList');
    if (roleLegendList) {
      for (const role of (window.ROLE_SORT || ['repeater', 'companion', 'room', 'sensor', 'observer'])) {
        const li = document.createElement('li');
        // #1293 — SVG swatch shows SHAPE + colour so colourblind ops can
        // distinguish roles without relying on hue alone (WCAG 1.4.1).
        const color = ROLE_COLORS[role] || '#6b7280';
        const swatch = window.makeRoleMarkerSVG
          ? window.makeRoleMarkerSVG(role, color, 14)
          : `<span class="live-dot" style="background:${color}" aria-hidden="true"></span>`;
        li.innerHTML = `<span class="live-shape-swatch" aria-hidden="true">${swatch}</span> ${(ROLE_LABELS[role] || role).replace(/s$/, '')}`;
        roleLegendList.appendChild(li);
      }
    }

    // Node detail panel
    const nodeDetailPanel = document.getElementById('liveNodeDetail');
    const nodeDetailContent = document.getElementById('nodeDetailContent');
    const nodeDetailBackdrop = document.getElementById('nodeDetailBackdrop');
    function closeNodeDetail() {
      activeNodeDetailKey = null;
      nodeDetailPanel.classList.add('hidden');
      nodeDetailBackdrop.classList.remove('active');
    }
    document.getElementById('nodeDetailClose').addEventListener('click', closeNodeDetail);
    nodeDetailBackdrop.addEventListener('click', closeNodeDetail);

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

    // Save/restore map view. #1709: URL hash lat/lon/zoom overrides
    // localStorage. Validated via shared parseViewportHash helper.
    const urlVp = (typeof parseViewportHash === 'function')
      ? parseViewportHash(location.hash) : null;
    if (urlVp) {
      map.setView([urlVp.lat, urlVp.lon], urlVp.zoom);
    } else {
      const savedView = localStorage.getItem('live-map-view');
      if (savedView) {
        try { const v = JSON.parse(savedView); map.setView([v.lat, v.lng], v.zoom); } catch {}
      }
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

    // Scope buttons (#1234: exclude .vcr-scope-more which is the dropdown trigger)
    document.querySelectorAll('.vcr-scope-btn[data-scope]').forEach(btn => {
      btn.addEventListener('click', () => {
        document.querySelectorAll('.vcr-scope-btn[data-scope]').forEach(b => { b.classList.remove('active'); b.setAttribute('aria-checked', 'false'); });
        btn.classList.add('active');
        btn.setAttribute('aria-checked', 'true');
        VCR.timelineScope = parseInt(btn.dataset.scope);
        fetchTimelineTimestamps().then(() => updateTimeline());
      });
    });

    // #1234: VCR scope "More ▾" overflow dropdown. At ≤640px, scope buttons
    // tagged .vcr-scope-btn--overflow (12h, 24h) are hidden via CSS and
    // surfaced through this menu. Clicking a menu item proxies the click
    // to the underlying hidden button so the existing scope handler runs.
    (function wireVcrScopeMore() {
      var moreBtn = document.querySelector('[data-vcr-scope-more]');
      var menu = document.getElementById('vcrScopeMoreMenu');
      if (!moreBtn || !menu) return;
      var overflowBtns = Array.from(document.querySelectorAll('.vcr-scope-btn--overflow'));
      // Populate menu from overflow buttons (preserves label + scope).
      menu.innerHTML = '';
      overflowBtns.forEach(function (src) {
        var item = document.createElement('button');
        item.type = 'button';
        item.className = 'vcr-scope-more-item';
        item.setAttribute('role', 'menuitem');
        item.setAttribute('data-scope', src.dataset.scope);
        item.textContent = src.textContent;
        item.addEventListener('click', function () {
          src.click(); // delegate to original handler — keeps single source of truth
          menu.setAttribute('hidden', '');
          moreBtn.setAttribute('aria-expanded', 'false');
          // Reflect the chosen scope on the More button label so the user sees feedback.
          moreBtn.textContent = item.textContent + ' ▾';
        });
        menu.appendChild(item);
      });
      moreBtn.addEventListener('click', function (e) {
        e.stopPropagation();
        var open = !menu.hasAttribute('hidden');
        if (open) { menu.setAttribute('hidden', ''); moreBtn.setAttribute('aria-expanded', 'false'); }
        else      { menu.removeAttribute('hidden');  moreBtn.setAttribute('aria-expanded', 'true');  }
      });
      // Click outside closes the menu.
      document.addEventListener('click', function (e) {
        if (menu.hasAttribute('hidden')) return;
        if (e.target === moreBtn || moreBtn.contains(e.target) ||
            e.target === menu   || menu.contains(e.target)) return;
        menu.setAttribute('hidden', '');
        moreBtn.setAttribute('aria-expanded', 'false');
      });
    })();

    // Timeline click to scrub
    // Timeline click handled by drag (mousedown+mouseup)

    // Timeline hover — show time tooltip
    const timelineEl = document.getElementById('vcrTimeline');
    const timeTooltip = document.getElementById('vcrTimeTooltip');
    timelineEl.addEventListener('mousemove', (e) => {
      const rect = timelineEl.getBoundingClientRect();
      const pct = (e.clientX - rect.left) / rect.width;
      const ts = Date.now() - VCR.timelineScope + pct * VCR.timelineScope;
      timeTooltip.textContent = vcrFormatTime(ts);
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
      timeTooltip.textContent = vcrFormatTime(ts);
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

    // Refresh relative timestamps in feed every 10 seconds (#701)
    _feedTimestampInterval = setInterval(function() {
      document.querySelectorAll('.feed-time[data-ts]').forEach(function(el) {
        el.innerHTML = formatLiveTimestampHtml(Number(el.dataset.ts));
      });
    }, 10000);

    // Auto-hide nav with pin toggle (#62)
    const topNav = document.querySelector('.top-nav');
    if (topNav) { topNav.style.position = 'fixed'; topNav.style.width = '100%'; topNav.style.zIndex = '1100'; }
    const _savedPin = localStorage.getItem('live-nav-pinned') === 'true';
    _navCleanup = { timeout: null, fn: null, pinned: _savedPin };
    // Add pin button to nav (guard against duplicate)
    if (topNav && !document.getElementById('navPinBtn')) {
      const pinBtn = document.createElement('button');
      pinBtn.id = 'navPinBtn';
      pinBtn.className = 'nav-pin-btn';
      pinBtn.setAttribute('aria-label', 'Pin navigation open');
      pinBtn.setAttribute('title', 'Pin navigation open');
      pinBtn.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-push-pin"/></svg>';
      pinBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        _navCleanup.pinned = !_navCleanup.pinned;
        pinBtn.classList.toggle('pinned', _navCleanup.pinned);
        pinBtn.setAttribute('aria-pressed', _navCleanup.pinned);
        document.body.classList.toggle('nav-pinned', _navCleanup.pinned);
        try { localStorage.setItem('live-nav-pinned', _navCleanup.pinned); } catch (_) {}
        if (_navCleanup.pinned) {
          clearTimeout(_navCleanup.timeout);
          topNav.classList.remove('nav-autohide');
        } else {
          _navCleanup.timeout = setTimeout(() => { topNav.classList.add('nav-autohide'); }, 4000);
        }
      });
        if (_navCleanup.pinned) {
        pinBtn.classList.add('pinned');
        pinBtn.setAttribute('aria-pressed', 'true');
        document.body.classList.add('nav-pinned');
        topNav.classList.remove('nav-autohide');
      }
      const navRight = topNav.querySelector('.nav-right');
      if (navRight) {
        navRight.appendChild(pinBtn);
      } else {
        topNav.appendChild(pinBtn);
      }
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
    activeNodeDetailKey = pubkey;
    const panel = document.getElementById('liveNodeDetail');
    const content = document.getElementById('nodeDetailContent');
    panel.classList.remove('hidden');
    document.getElementById('nodeDetailBackdrop').classList.add('active');
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
      const lastSeen = formatLiveTimestampHtml(n.last_seen);
      const thresholds = window.getHealthThresholds ? getHealthThresholds(n.role) : { degradedMs: 3600000, silentMs: 86400000 };
      const ageMs = n.last_seen ? Date.now() - new Date(n.last_seen).getTime() : Infinity;
      const statusDot = ageMs < thresholds.degradedMs ? 'health-green' : ageMs < thresholds.silentMs ? 'health-yellow' : 'health-red';
      const statusLabel = ageMs < thresholds.degradedMs ? 'Online' : ageMs < thresholds.silentMs ? 'Degraded' : 'Offline';

      let html = `
        <div style="padding:16px;">
          <div style="display:flex;align-items:center;gap:8px;margin-bottom:12px;">
            <span class="${statusDot}" style="font-size:18px" aria-hidden="true"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span>
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
            ${'configured_scope' in n && n.configured_scope !== null ? `<tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;" title="Region scopes this node has configured, confirmed via an observer /neighbors report (status=responded) — concrete evidence (#1865).${n.configured_scope_at ? ' Last confirmed ' + escapeHtml(String(n.configured_scope_at)) + '.' : ''}">Configured scope <span style="color:var(--status-green,#2ecc71)" aria-label="confirmed">✓</span></td><td>${n.configured_scope === '' ? '<span style="color:var(--text-muted)">none configured</span>' : `<code style="color:var(--link-color)">${escapeHtml(n.configured_scope)}</code>`}</td></tr>` : ''}
            ${'default_scope' in n ? `<tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Scope</td><td>${n.default_scope === null ? '<span style="color:var(--text-muted)">—</span>'
  : n.default_scope === '' ? '<span style="color:var(--text-muted)">unknown scope</span>'
  : `<code style="color:var(--link-color)">${escapeHtml(n.default_scope)}</code>`
}</td></tr>` : ''}
            ${hasLoc ? `<tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Location</td><td>${n.lat.toFixed(5)}, ${n.lon.toFixed(5)}</td></tr>` : ''}
            ${stats.avgSnr != null ? `<tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Avg SNR</td><td>${stats.avgSnr.toFixed(1)} dB</td></tr>` : ''}
            ${stats.avgHops != null ? `<tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Avg Hops</td><td>${stats.avgHops.toFixed(1)}</td></tr>` : ''}
            ${stats.totalTransmissions || stats.totalPackets ? `<tr><td style="color:var(--text-muted);padding:4px 8px 4px 0;">Total Packets</td><td>${stats.totalTransmissions || stats.totalPackets}</td></tr>` : ''}
          </table>`;

      if (observers.length) {
        const regions = [...new Set(observers.map(o => o.iata).filter(Boolean))];
        html += `<h4 style="font-size:12px;margin:12px 0 6px;color:var(--text-muted);">Heard By${regions.length ? ' — Regions: ' + regions.join(', ') : ''}</h4>
          <div style="font-size:11px;">` +
          observers.map(o => `<div style="padding:2px 0;"><a href="#/observers/${encodeURIComponent(o.observer_id)}" style="color:var(--link-color);text-decoration:none;">${escapeHtml(o.observer_name || o.observer_id.slice(0, 12))}${o.iata ? ' (' + escapeHtml(o.iata) + ')' : ''}</a> — ${o.packetCount || o.count || 0} pkts</div>`).join('') +
          '</div>';
      }

      if (recent.length) {
        html += `<h4 style="font-size:12px;margin:12px 0 6px;color:var(--text-muted);">Recent Packets</h4>
          <div style="font-size:11px;max-height:200px;overflow-y:auto;">` +
          recent.slice(0, 10).map(p => `<div style="padding:2px 0;display:flex;justify-content:space-between;">
            <a href="#/packets/${encodeURIComponent(p.hash || '')}" style="color:var(--link-color);text-decoration:none;">${escapeHtml(p.payload_type || '?')}${transportBadge(p.route_type)}${p.observation_count > 1 ? ' <span class="badge badge-obs" style="font-size:9px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> ' + p.observation_count + '</span>' : ''}</a>
            <span style="color:var(--text-muted)">${formatLiveTimestampHtml(p.timestamp)}</span>
          </div>`).join('') +
          '</div>';
      }

      html += `<div id="liveNodePaths" style="margin-top:8px;"><div style="font-size:11px;color:var(--text-muted);padding:4px 0;"><span class="spinner" style="font-size:10px"></span> Loading paths…</div></div>`;

      html += `<div style="margin-top:12px;display:flex;gap:8px;">
        <a href="#/nodes/${encodeURIComponent(n.public_key)}" style="font-size:12px;color:var(--link-color);">Full Detail →</a>
        <a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" style="font-size:12px;color:var(--link-color);"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> Analytics</a>
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
        // #1689 r1 (adv #3): respect the customizer "hide 1-byte path hops"
        // toggle in the live-pane "Paths Through" widget. /api/.../paths
        // returns hops as objects {prefix,pubkey,name}; the byte-length is
        // derived from the hex prefix.
        function _filterHopObjs(hopObjs) {
          if (typeof window === 'undefined' || !window.MC_isVisibleHop) return hopObjs;
          if (!window.MC_getHide1ByteHops || !window.MC_getHide1ByteHops()) return hopObjs;
          return (hopObjs || []).filter(function (h) {
            return window.MC_isVisibleHop(h && h.prefix ? String(h.prefix) : '');
          });
        }
        // #1784 — path trust threshold: check whether ALL hops in a path
        // are below the configured minimum hash bytes for mapping.
        function _pathHopsBelowTrust(hops) {
          if (typeof window === 'undefined' || !window.MC_meetsPathTrust) return false;
          if (!hops || !hops.length) return false;
          for (var i = 0; i < hops.length; i++) {
            if (window.MC_meetsPathTrust(hops[i] && hops[i].prefix ? String(hops[i].prefix) : '')) return false;
          }
          return true;
        }
        function renderPathList(paths) {
          return paths.map(p => {
            const filteredHops = _filterHopObjs(p.hops || []);
            if (!filteredHops.length) {
              // #1784 — distinguish between "all 1-byte filtered" and
              // "all below trust threshold" for the fallback message.
              var _msg = '1-byte filtered';
              if (_pathHopsBelowTrust(p.hops || [])) {
                var _tt = (window.MC_getPathTrustThreshold ? window.MC_getPathTrustThreshold() : 1);
                _msg = _tt + '-byte trust threshold (pathTrust.minHashBytesForMapping: ' + _tt + ')';
              }
              return `<div style="padding:3px 0;font-size:11px;line-height:1.4;color:var(--text-muted)">— (${_msg}) <span style="color:var(--text-muted)">(${p.count}×)</span></div>`;
            }
            const chain = filteredHops.map(h => {
              const isThis = h.pubkey === n.public_key || (h.prefix && n.public_key.toLowerCase().startsWith(h.prefix.toLowerCase()));
              const name = escapeHtml(h.name || h.prefix);
              if (isThis) return `<strong style="color:var(--link-color)">${name}</strong>`;
              return h.pubkey ? `<a href="#/nodes/${h.pubkey}" style="color:var(--text);text-decoration:none">${name}</a>` : name;
            }).join(' → ');
            return `<div style="padding:3px 0;font-size:11px;line-height:1.4">${chain} <span style="color:var(--text-muted)">(${p.count}×)</span></div>`;
          }).join('');
        }
        pathEl.innerHTML = `<h4 style="font-size:12px;margin:8px 0 4px;color:var(--text-muted);">Paths Through (${pathData.totalPaths})</h4>` +
          `<div id="livePathsList" style="max-height:200px;overflow-y:auto;">` +
          renderPathList(pathData.paths.slice(0, COLLAPSE)) +
          (pathData.paths.length > COLLAPSE ? `<button id="showMorePaths" style="font-size:11px;color:var(--link-color);background:none;border:none;cursor:pointer;padding:4px 0;">Show all ${pathData.paths.length} paths</button>` : '') +
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
      const aqs = AreaFilter.areaQueryString();
      // #1108 — honor region selector for visible map nodes unless
      // "Show all nodes" is enabled. Empty string when no region set.
      const rqs = (window.RegionFilter && typeof RegionFilter.nodesRegionQueryString === 'function')
        ? RegionFilter.nodesRegionQueryString() : '';
      const beforeQs = beforeTs
        ? `&before=${encodeURIComponent(new Date(beforeTs).toISOString())}`
        : '';
      // Full reload (no beforeTs): clear existing markers so switching areas
      // removes nodes that no longer belong to the selected area.
      if (!beforeTs) {
        if (nodesLayer) nodesLayer.clearLayers();
        nodeMarkers = {};
        nodeData = {};
      }
      // Paginate past the server's per-request node cap (listLimits.nodesMax)
      // so the live map isn't truncated to the top-N by advert. LIVE_MAP_MAX_NODES
      // is passed as safetyCap, which fetchAllNodes enforces as a hard ceiling on
      // returned nodes — so the live map never renders more than the configured max.
      const { nodes: list } = await fetchAllNodes(`${beforeQs}${aqs}${rqs}`, {
        safetyCap: window.LIVE_MAP_MAX_NODES || 10000,
      });
      var now = Date.now();
      // Time-scoped reload (VCR scrub/replay): reconcile against the existing
      // markers instead of tearing the layer down. addNodeMarker() already
      // no-ops keys that are still present, so unchanged dots stay put (no
      // flicker) — we only rebuild what actually differs at the target time.
      if (beforeTs) {
        const valid = list.filter(n => n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0));
        const nextKeys = new Set(valid.map(n => n.public_key));
        // Drop markers for nodes that didn't exist at the target time.
        for (const key in nodeMarkers) {
          if (!nextKeys.has(key)) {
            const stale = nodeMarkers[key];
            if (stale && nodesLayer) { try { nodesLayer.removeLayer(stale); } catch (e) {} }
            delete nodeMarkers[key];
            delete nodeData[key];
            delete nodeActivity[key];
          }
        }
        // Refresh survivors whose geometry/role/name differs at the target
        // time. addNodeMarker() no-ops existing keys, so without this a kept
        // marker would keep showing current values rather than the time-scoped
        // ones — drop the changed marker here so it's rebuilt by the loop below
        // (nodeData still holds the prior values until that loop overwrites it).
        for (const n of valid) {
          const existing = nodeMarkers[n.public_key];
          const prev = nodeData[n.public_key];
          if (existing && prev && (prev.lat !== n.lat || prev.lon !== n.lon ||
              prev.role !== n.role || (prev.name || '') !== (n.name || ''))) {
            if (nodesLayer) { try { nodesLayer.removeLayer(existing); } catch (e) {} }
            delete nodeMarkers[n.public_key];
          }
        }
      }
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
      // Fetch affinity data for hop disambiguation
      fetchAffinityData();
      startAffinityRefresh();
    } catch (e) { console.error('Failed to load nodes:', e); }
  }

  let _affinityInterval = null;

  async function fetchAffinityData() {
    try {
      const resp = await fetch('/api/analytics/neighbor-graph');
      const graph = await resp.json();
      if (window.HopResolver && HopResolver.setAffinity) {
        HopResolver.setAffinity(graph);
      }
    } catch (e) { console.warn('Failed to fetch affinity data:', e); }
  }

  function startAffinityRefresh() {
    if (_affinityInterval) clearInterval(_affinityInterval);
    _affinityInterval = setInterval(fetchAffinityData, 60000);
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

  function packetInvolvesFilterNode(pkt, filterKeys) {
    if (!filterKeys.length) return true;
    // #1689 r2 MAJOR: node-filter search semantics MUST be independent of
    // the customizer's hide-1-byte-hops display preference. Letting the
    // display toggle change search results silently fails the principle
    // of least astonishment (operator searches for a node, hides 1-byte
    // hops for chart readability, suddenly matches disappear). Chip
    // rendering filters hops separately via MC_filterPathHops at the
    // render boundary — see feedHops below.
    const hops = (pkt.decoded?.path?.hops) || [];
    for (const hop of hops) {
      const h = (hop.id || hop.public_key || hop).toString().toLowerCase();
      if (filterKeys.some(f => f.toLowerCase().startsWith(h) || h.startsWith(f.toLowerCase()))) return true;
    }
    return false;
  }

  function setNodeFilter(keys) {
    nodeFilterKeys = keys;
    nodeFilterTotal = 0;
    nodeFilterShown = 0;
    localStorage.setItem('live-node-filter', keys.join(','));
    updateNodeFilterUI();
  }

  function updateNodeFilterUI() {
    const countEl = document.getElementById('liveNodeFilterCount');
    const clearBtn = document.getElementById('liveNodeFilterClear');
    const input = document.getElementById('liveNodeFilterInput');
    if (nodeFilterKeys.length > 0) {
      if (clearBtn) clearBtn.style.display = '';
      if (countEl) { countEl.textContent = `Showing ${nodeFilterShown} of ${nodeFilterTotal}`; countEl.classList.remove('hidden'); }
      if (input && input.value !== nodeFilterKeys.join(', ')) input.value = nodeFilterKeys.join(', ');
    } else {
      if (clearBtn) clearBtn.style.display = 'none';
      if (countEl) countEl.classList.add('hidden');
    }
    updateNodeFilterDatalist();
  }

  function updateNodeFilterDatalist() {
    const dl = document.getElementById('liveNodeFilterList');
    if (!dl) return;
    dl.innerHTML = Object.values(nodeData).map(n =>
      `<option value="${escapeHtml(n.public_key)}">${escapeHtml(n.name || n.public_key.slice(0, 8))}</option>`
    ).join('');
  }

  function rebuildFeedList() {
    const feed = document.getElementById('liveFeed');
    if (!feed) return;
    const feedContent = feed.querySelector('.panel-content');
    if (!feedContent) return;
    feedContent.querySelectorAll('.live-feed-item').forEach(el => el.remove());
    feedDedup.clear();
    // #1207: ensure empty-state placeholder is present (re-add if a prior
    // rebuild wiped everything). CSS hides it when items exist.
    if (!feedContent.querySelector('.live-feed-empty')) {
      var _ph = document.createElement('div');
      _ph.className = 'live-feed-empty';
      _ph.setAttribute('aria-hidden', 'true');
      _ph.textContent = 'Waiting for packets…';
      feedContent.appendChild(_ph);
    }

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
      if (multibyteOnly && !groupIsMultibyte(group.packets)) continue;
      const pkt = Object.assign({}, group.latestPkt, { observation_count: group.count });
      const decoded = pkt.decoded || {};
      const header = decoded.header || {};
      const payload = decoded.payload || {};
      const typeName = header.payloadTypeName || 'UNKNOWN';
      const icon = PAYLOAD_ICONS[typeName] || '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-package"/></svg>';
      const color = TYPE_COLORS[typeName] || '#6b7280';

      // Find longest path across all observations for display
      let longestHops = decoded.path?.hops || [];
      for (const op of group.packets) {
        let opHops = [];
        if (op.path_json) {
          try { opHops = getParsedPath(op); } catch {}
        } else if (op.decoded?.path?.hops) {
          opHops = op.decoded.path.hops;
        }
        if (opHops.length > longestHops.length) longestHops = opHops;
      }

      // Create feed item directly with correct count
      const text = payload.text || payload.name || '';
      const preview = text ? ' ' + (text.length > 35 ? text.slice(0, 35) + '…' : text) : '';
      const hopStr = longestHops.length ? `<span class="feed-hops">${longestHops.length}⇢</span>` : '';
      const obsBadge = group.count > 1 ? `<span class="badge badge-obs" style="font-size:10px;margin-left:4px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> ${group.count}</span>` : '';
      const iataBadge = obsIataBadgeHtml(pkt);

      var _ccPayload = (pkt.decoded || {}).payload || {};
      var _ccChan1 = (typeName === 'GRP_TXT' || typeName === 'CHAN') ? (_ccPayload.channel || null) : null;
      var dotHtml1 = _ccChan1 ? _feedColorDot(_ccChan1) : '';

      const item = document.createElement('div');
      item.className = 'live-feed-item';
      item.setAttribute('tabindex', '0');
      item.setAttribute('role', 'button');
      if (group.hash) item.setAttribute('data-hash', group.hash);
      item.style.cursor = 'pointer';
      item.innerHTML = `
        <span class="feed-icon" style="color:${color}">${icon}</span>
        <span class="feed-type" style="color:${color}">${typeName}</span>
        ${dotHtml1}${transportBadge(pkt.route_type)}${hopStr}${obsBadge}${iataBadge}
        <span class="feed-text">${escapeHtml(preview)}</span>
        <span class="feed-time" data-ts="${group.latestTs || Date.now()}">${formatLiveTimestampHtml(group.latestTs || Date.now())}</span>
      `;
      if (_ccChan1) item._ccChannel = _ccChan1; // channel color picker (#674)
      item.addEventListener('click', () => showFeedCard(item, pkt, color));
      feedContent.appendChild(item);

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

  /**
   * Round to the next even integer to prevent browser sub-pixel snapping on
   * SVG node markers. Cross-link: live.css (~line 1300) — "Eliminate SVG
   * baseline drift" — relies on icon size being even so the half-size
   * iconAnchor lands on a whole pixel.
   * @param {number} n size in CSS pixels
   * @returns {number} `n` if even, else `n + 1`
   */
  function evenSize(n) { return n % 2 ? n + 1 : n; }

  function addNodeMarker(n) {
    if (nodeMarkers[n.public_key]) return nodeMarkers[n.public_key];
    const color = ROLE_COLORS[n.role] || ROLE_COLORS.unknown;
    // #1438: SVG fill expression — use the live CSS var so existing
    // markers recolor when cb-preset switches or the operator overrides.
    // `color` (hex from ROLE_COLORS) is still tracked as `_baseColor`
    // for matrix mode / pulse animations that need an explicit value.
    const fillExpr = 'var(--mc-role-' + (n.role || 'companion') + ')';
    const isRepeater = n.role === 'repeater';
    const zoom = map ? map.getZoom() : 11;
    const zoomScale = Math.max(0.4, (zoom - 8) / 6);
    // Shape-aware sizing: keep prior visual weight (~6/4 base) but
    // route through divIcon so colourblind ops get distinct silhouettes
    // (#1293). Size is the SVG box; circleMarker radius ~= size/3.
    let sizePx = evenSize(Math.max(10, Math.round((isRepeater ? 18 : 14) * zoomScale)));

    const svgHtml = (window.makeRoleMarkerSVG
      ? window.makeRoleMarkerSVG(n.role, null, sizePx)
      : '<svg width="' + sizePx + '" height="' + sizePx + '" viewBox="0 0 ' + sizePx + ' ' + sizePx +
        '"><circle cx="' + (sizePx/2) + '" cy="' + (sizePx/2) + '" r="' + (sizePx/2 - 2) +
        '" fill="' + fillExpr + '" stroke="var(--mc-marker-stroke-color)" stroke-width="var(--mc-marker-stroke-width)" stroke-opacity="var(--mc-marker-stroke-opacity)"/></svg>');

    const icon = L.divIcon({
      html: svgHtml,
      className: 'live-node-marker live-node-' + (n.role || 'unknown'),
      iconSize: [sizePx, sizePx],
      iconAnchor: [sizePx / 2, sizePx / 2],
      popupAnchor: [0, -sizePx / 2]
    });
    const marker = L.marker([n.lat, n.lon], { icon: icon, interactive: true });
    if (nodesLayer) {
      try {
        // If the marker is outside the current map bounds, it is intentionally
        // deferred to save DOM nodes. cullMarkers() is the only path that will
        // re-attach it later when the user pans or zooms.
        if (!map || map.getBounds().pad(0.5).contains(marker.getLatLng())) {
          marker.addTo(nodesLayer);
        }
      } catch (e) {
        // Fallback: map.getBounds() can throw during early Leaflet initialization
        marker.addTo(nodesLayer);
      }
    }

    marker.bindTooltip(escapeHtml(n.name || n.public_key.slice(0, 8)), {
      permanent: false, direction: 'top', offset: [0, -sizePx / 2], className: 'live-tooltip'
    });

    marker.on('click', () => showNodeDetail(n.public_key));

    marker._baseColor = color;
    marker._baseSize = sizePx;
    marker._role = n.role || 'unknown';
    nodeMarkers[n.public_key] = marker;

    // Apply matrix tint if active — re-render the SVG with matrix colour
    if (matrixMode) {
      marker._matrixPrevColor = color;
      marker._baseColor = '#008a22';
      const mxHtml = window.makeRoleMarkerSVG
        ? window.makeRoleMarkerSVG(marker._role, '#008a22', sizePx)
        : svgHtml;
      const el = marker.getElement();
      if (el) el.innerHTML = mxHtml;
    }

    return marker;
  }

  // #1293 — divIcon helpers. The live-map node marker is now an
  // L.marker (divIcon SVG), not an L.circleMarker, so setStyle /
  // setRadius are no-ops. These helpers update the DOM element
  // directly so existing call-sites (rescale, stale-dim, matrix mode,
  // highlight pulse) keep working without same-colour fill stacking.
  function _liveMarkerEl(marker) {
    if (!marker || typeof marker.getElement !== 'function') return null;
    return marker.getElement();
  }
  function _liveSetMarkerOpacity(marker, opacity) {
    var el = _liveMarkerEl(marker);
    if (el) el.style.opacity = String(opacity);
  }
  function _liveSetMarkerSize(marker, sizePx) {
    var el = _liveMarkerEl(marker);
    if (!el) return;

    // Update the DOM container styles manually so the anchor remains centered
    // without having to destroy and recreate the Leaflet marker object.
    el.style.width = sizePx + 'px';
    el.style.height = sizePx + 'px';
    el.style.marginLeft = -(sizePx / 2) + 'px';
    el.style.marginTop = -(sizePx / 2) + 'px';

    var svg = el.querySelector('svg');
    if (svg) {
      svg.setAttribute('width', sizePx);
      svg.setAttribute('height', sizePx);
    }
    marker._baseSize = sizePx;
  }
  function _liveSetMarkerColor(marker, color) {
    var el = _liveMarkerEl(marker);
    if (!el) return;
    if (window.makeRoleMarkerSVG) {
      el.innerHTML = window.makeRoleMarkerSVG(marker._role || 'unknown', color, marker._baseSize || 14);
    } else {
      // Fallback: tweak fill on first shape
      var shape = el.querySelector('svg > *');
      if (shape) shape.setAttribute('fill', color);
    }
  }
  window._liveSetMarkerSize = _liveSetMarkerSize;
  window._liveSetMarkerColor = _liveSetMarkerColor;

  function rescaleMarkers() {
    const zoom = map.getZoom();
    const zoomScale = Math.max(0.4, (zoom - 8) / 6);
    for (const [key, marker] of Object.entries(nodeMarkers)) {
      const n = nodeData[key];
      const isRepeater = n && n.role === 'repeater';
      let sizePx = evenSize(Math.max(10, Math.round((isRepeater ? 18 : 14) * zoomScale)));
      _liveSetMarkerSize(marker, sizePx);
    }
  }

  // Prune nodes not seen within their role's health threshold.
  // API-loaded nodes (_fromAPI) are dimmed instead of removed — matches static map behavior.
  // #1598: infra nodes (repeater/room) are ALWAYS dimmed, never deleted —
  // a dimmed marker still tells operators "infrastructure exists here,
  // advert stale", whereas deletion silently rewrites the map. Staleness
  // itself is relay-aware via getNodeStatus(node) (max of advert-based
  // freshness and last_relayed for infra).
  // Remaining WS-only non-infra nodes are removed to prevent memory leaks.
  function pruneStaleNodes() {
    var now = Date.now();
    var pruned = false;
    for (var key in nodeMarkers) {
      var n = nodeData[key];
      if (!n) continue;
      var lastSeen = n._liveSeen || (n.last_heard ? new Date(n.last_heard).getTime() : null) || (n.last_seen ? new Date(n.last_seen).getTime() : null);
      if (lastSeen == null) continue;
      var status = window.getNodeStatus ? getNodeStatus(n) : 'active';
      var isInfra = n.role === 'repeater' || n.role === 'room';
      var marker = nodeMarkers[key];
      if (status === 'stale') {
        if (n._fromAPI || isInfra) {
          // API-loaded nodes: dim instead of removing (consistent with static map)
          if (marker && !marker._staleDimmed) {
            marker._staleDimmed = true;
            _liveSetMarkerOpacity(marker, 0.35);
          }
        } else {
          // WS-only non-infra nodes: remove to prevent unbounded memory growth
          if (marker) {
            if (nodesLayer) {
              try { nodesLayer.removeLayer(marker); } catch (e) { }
            }
          }
          delete nodeMarkers[key];
          delete nodeData[key];
          delete nodeActivity[key];
          pruned = true;
        }
      } else if (marker && marker._staleDimmed) {
        // Node became active again — restore full opacity
        marker._staleDimmed = false;
        _liveSetMarkerOpacity(marker, 1);
      }
    }
    if (pruned) {
      var _el2 = document.getElementById('liveNodeCount');
      if (_el2) _el2.textContent = Object.keys(nodeMarkers).length;
      if (window.HopResolver) HopResolver.init(Object.values(nodeData));
    }
    // Prune orphaned nodeActivity entries (nodes removed above or never tracked)
    for (var aKey in nodeActivity) {
      if (!(aKey in nodeData)) delete nodeActivity[aKey];
    }
    pruneClickablePaths(Date.now());
  }

  // Expose for testing
  window._livePruneStaleNodes = pruneStaleNodes;
  window._liveBuildClickablePathPopupHtml = buildClickablePathPopupHtml;
  window._livePruneClickablePaths = pruneClickablePaths;
  window._liveClickablePaths = clickablePaths;
  window._liveNodeMarkers = function() { return nodeMarkers; };
  window._liveNodeData = function() { return nodeData; };
  window._liveNodeActivity = function() { return nodeActivity; };
  window._vcrFormatTime = vcrFormatTime;
  window._liveDbPacketToLive = dbPacketToLive;
  window._liveStepPulse = stepPulse;
  window._liveExpandToBufferEntries = expandToBufferEntries;
  window._liveExpandToBufferEntriesAsync = expandToBufferEntriesAsync;
  window._liveSEG_MAP = SEG_MAP;
  window._liveBufferPacket = bufferPacket;
  window._liveVCR = function() { return VCR; };
  window._liveGetFavoritePubkeys = getFavoritePubkeys;
  window._livePacketInvolvesFavorite = packetInvolvesFavorite;
  window._liveIsNodeFavorited = isNodeFavorited;
  window._livePacketInvolvesFilterNode = packetInvolvesFilterNode;
  window._liveGetNodeFilterKeys = function() { return nodeFilterKeys; };
  window._livePacketMatchesRegion = packetMatchesRegion;
  window._liveSetObserverIataMap = setObserverIataMap;
  window._liveBuildObserverIataMap = buildObserverIataMap;
  window._liveGetObserverIataMap = function() { return observerIataMap; };
  window._liveSetNodeFilter = setNodeFilter;
  window._liveFormatLiveTimestampHtml = formatLiveTimestampHtml;
  window._liveResolveHopPositions = resolveHopPositions;
  window._liveVcrSpeedCycle = vcrSpeedCycle;
  window._liveSpeedLabel = speedLabel;
  window._liveVcrPause = vcrPause;
  window._liveVcrResumeLive = vcrResumeLive;
  window._liveVcrSetMode = vcrSetMode;
  // #1207 test seams: expose production feed mutators so E2E can exercise
  // the real eviction guard / placeholder re-add path (not a test-local copy).
  window._liveAddFeedItem = function(icon, typeName, payload, hops, color, pkt) {
    return addFeedItem(icon, typeName, payload, hops, color, pkt);
  };
  window._liveRebuildFeedList = function() { return rebuildFeedList(); };

  // PR #1490 test seams: Expose internal state for Playwright assertions
  window._liveDrawAnimatedLine = drawAnimatedLine;
  window._liveTestSeams = {
    getAnimCount: () => activeAnimations.length,
    isAnimating: () => isAnimating,
    getPathCount: () => (typeof recentPaths !== 'undefined' ? recentPaths.length : 0),
    wake: wakeCanvasEngine,
    getPulses: () => activePulses,
    triggerPulse: pulseNode
  };

  async function replayRecent() {
    try {
      // Single bulk fetch with expand=observations — no N+1 calls
      const resp = await fetch('/api/packets?limit=8&expand=observations');
      const data = await resp.json();
      const groups = (data.packets || []).reverse();

      const allGroups = groups.map((group) => {
        const observations = group.observations || [];

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
        return livePackets;
      });

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
    } catch { }
  }

  function connectWS() {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = new WebSocket(`${proto}://${location.host}`);
    ws.onmessage = (e) => {
      try {
        const msg = JSON.parse(e.data);
        if (msg.type === 'packet') bufferPacket(msg.data);
      } catch { }
    };
    ws.onclose = () => setTimeout(connectWS, WS_RECONNECT_MS);
    ws.onerror = () => {};
  }

  // A packet group is multibyte when its path hash size is >= 2 bytes.
  // All observations of one packet share the same hash size; use the first
  // observation with a resolvable raw_hex. Unresolvable => treated as single
  // (excluded when the "Multibyte only" filter is on).
  function groupIsMultibyte(packets) {
    if (!packets || !packets.length) return false;
    for (var i = 0; i < packets.length; i++) {
      var p = packets[i];
      var size = window.MC_packetHashSize
        ? window.MC_packetHashSize(p.raw_hex, p.route_type)
        : 0;
      if (size > 0) return size >= 2;
    }
    return false;
  }

  // === UNIFIED PACKET RENDERER ===
  // ONE function for all rendering: WS arrival, DB load, replay button, VCR playback.
  // Takes an array of observations (same hash) and renders the complete path tree.
  function renderPacketTree(packets, isReplay) {
    if (!packets || !packets.length) return;
    // "Multibyte only" filter: drop single-byte / unresolvable packets from the
    // entire live view (feed, map, rain, counter — live AND replay). Placed
    // above the counter so livePktCount reflects multibyte-only traffic.
    if (multibyteOnly && !groupIsMultibyte(packets)) return;
    const first = packets[0];
    const decoded = first.decoded || {};
    const header = decoded.header || {};
    const payload = decoded.payload || {};
    const typeName = header.payloadTypeName || 'UNKNOWN';
    const icon = PAYLOAD_ICONS[typeName] || '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-package"/></svg>';
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

    // --- Node filter ---
    if (nodeFilterKeys.length) {
      nodeFilterTotal++;
      if (!packets.some(function(p) { return packetInvolvesFilterNode(p, nodeFilterKeys); })) return;
      nodeFilterShown++;
      updateNodeFilterUI();
    }

    // --- Region filter (#1045): drop packet if no observation matches selected IATA ---
    if (window.RegionFilter && typeof RegionFilter.getSelected === 'function') {
      var _regionSel = RegionFilter.getSelected();
      if (_regionSel && _regionSel.length && !packetMatchesRegion(packets, observerIataMap, _regionSel)) return;
    }

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
          try { fpHops = getParsedPath(fp); } catch {}
        } else if (fp.decoded?.path?.hops) {
          fpHops = fp.decoded.path.hops;
        }
        if (fpHops.length > feedHops.length) feedHops = fpHops;
      }
      // #1689 r1 (adv #3): apply the customizer "hide 1-byte path hops"
      // toggle to the feed-item hop count + chip rendering. feedHops here
      // is an array of raw hex tokens (from path_json) or hop-objects (from
      // decoded.path.hops) — MC_filterPathHops only knows about hex strings
      // so we branch on shape.
      if (typeof window !== 'undefined' && window.MC_getHide1ByteHops && window.MC_getHide1ByteHops()) {
        if (feedHops.length && typeof feedHops[0] === 'string') {
          feedHops = window.MC_filterPathHops(feedHops);
        } else if (feedHops.length && window.MC_isVisibleHop) {
          feedHops = feedHops.filter(function (h) {
            return window.MC_isVisibleHop(h && h.prefix ? String(h.prefix) : (h && h.id ? String(h.id) : ''));
          });
        }
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
        try { hops = getParsedPath(qpkt); } catch (e) { hops = qd.path?.hops || []; }
      } else {
        hops = qd.path?.hops || [];
      }
      var pathKey = hops.join(',');
      if (seenPathKeys.has(pathKey)) continue;
      seenPathKeys.add(pathKey);
      var hopPositions = resolveHopPositions(hops, qp, window.getResolvedPath ? getResolvedPath(qpkt) : null);
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
    var pktMeta = { hash: first.hash, ts: first._ts || Date.now() };
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
      // For TRACE packets, split at hopsCompleted: solid for completed, dashed for remaining
      var hopsCompleted = decoded.path && decoded.path.hopsCompleted;
      if (typeName === 'TRACE' && hopsCompleted != null && hopsCompleted < allPaths[ai].hopPositions.length) {
        var completedPositions = allPaths[ai].hopPositions.slice(0, hopsCompleted + 1);
        var remainingPositions = allPaths[ai].hopPositions.slice(hopsCompleted);
        if (completedPositions.length >= 2) {
          animatePath(completedPositions, typeName, color, allPaths[ai].raw, onHop, pktMeta);
        } else if (completedPositions.length === 1) {
          pulseNode(completedPositions[0].key, completedPositions[0].pos, typeName);
        }
        if (remainingPositions.length >= 2) {
          drawDashedPath(remainingPositions, color);
        }
      } else {
        animatePath(allPaths[ai].hopPositions, typeName, color, allPaths[ai].raw, onHop, pktMeta);
      }
    }
  }

  // Draw a static dashed/ghosted line for unreached TRACE hops
  function drawDashedPath(hopPositions, color) {
    var GHOST_TIMEOUT_MS = 10000;
    var ghostColor = getComputedStyle(document.documentElement).getPropertyValue('--trace-ghost-color').trim() || '#94a3b8';
    if (!pathsLayer) return;
    for (var i = 0; i < hopPositions.length - 1; i++) {
      var from = hopPositions[i].pos;
      var to = hopPositions[i + 1].pos;
      var line = L.polyline([from, to], {
        color: color, weight: 2, opacity: 0.25, dashArray: '6, 8'
      }).addTo(pathsLayer);
      // Pulse the unreached hop nodes as ghost markers
      if (i > 0) {
        var hp = hopPositions[i];
        if (!nodeMarkers[hp.key]) {
          var ghost = L.circleMarker(hp.pos, {
            radius: 3, fillColor: ghostColor, fillOpacity: 0.2, color: color, weight: 1, opacity: 0.3
          }).addTo(pathsLayer);
          setTimeout((function(g) { return function() { if (pathsLayer.hasLayer(g)) pathsLayer.removeLayer(g); }; })(ghost), GHOST_TIMEOUT_MS);
        }
      }
      // Remove dashed line after timeout
      setTimeout((function(l) { return function() { if (pathsLayer.hasLayer(l)) pathsLayer.removeLayer(l); }; })(line), GHOST_TIMEOUT_MS);
    }
    // Ghost marker for the final unreached hop
    var last = hopPositions[hopPositions.length - 1];
    if (!nodeMarkers[last.key]) {
      var ghostEnd = L.circleMarker(last.pos, {
        radius: 4, fillColor: ghostColor, fillOpacity: 0.25, color: color, weight: 1, opacity: 0.35
      }).addTo(pathsLayer);
      setTimeout(function() { if (pathsLayer.hasLayer(ghostEnd)) pathsLayer.removeLayer(ghostEnd); }, GHOST_TIMEOUT_MS);
    }
  }

  function resolveHopPositions(hops, payload, resolvedPath) {
    // Hoist sender GPS guard once — reject (0,0) as "no GPS"
    const hasValidGps = payload.lat != null && payload.lon != null
      && !(payload.lat === 0 && payload.lon === 0);
    const senderLat = hasValidGps ? payload.lat : null;
    const senderLon = hasValidGps ? payload.lon : null;

    // Prefer server-side resolved_path when available
    var resolvedMap;
    if (resolvedPath && resolvedPath.length === hops.length && window.HopResolver && HopResolver.ready()) {
      resolvedMap = HopResolver.resolveFromServer(hops, resolvedPath);
      // Fill in any null entries from client-side fallback, preserving sender GPS context
      var nullHops = hops.filter(function(h, i) { return !resolvedPath[i] && !resolvedMap[h]; });
      if (nullHops.length) {
        var fallback = HopResolver.resolve(nullHops, senderLat, senderLon, null, null, null);
        for (var k in fallback) resolvedMap[k] = fallback[k];
      }
    } else {
      // Delegate to shared HopResolver (from hop-resolver.js) instead of reimplementing
      // Use HopResolver if available and initialized, otherwise fall back to simple lookup
      resolvedMap = (window.HopResolver && HopResolver.ready())
        ? HopResolver.resolve(hops, senderLat, senderLon, null, null, null)
        : {};
    }

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
    if (payload.pubKey && senderLat != null) {
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

  function animatePath(hopPositions, typeName, color, rawHex, onHop, pktMeta) {
    if (!animLayer || !pathsLayer) return;
    if (activeAnims >= MAX_CONCURRENT_ANIMS) return;
    activeAnims++;
    document.getElementById('liveAnimCount').textContent = activeAnims;
    let hopIndex = 0;

    function nextHop() {
      if (hopIndex >= hopPositions.length) {
        activeAnims = Math.max(0, activeAnims - 1);
        const countEl = document.getElementById('liveAnimCount');
        if (countEl) countEl.textContent = activeAnims;
        if (pktMeta && hopPositions.length >= 2) {
          const latLngs = [], hopNames = [];
          for (const hp of hopPositions) {
            latLngs.push(hp.pos);
            hopNames.push(hp.name || (hp.key ? hp.key.slice(0, 8) : '?'));
          }
          registerClickablePath(latLngs, typeName, color, hopNames, pktMeta.ts, pktMeta.hash);
        }
        return;
      }
      if (!animLayer) return;

      const hp = hopPositions[hopIndex];

      // Audio hook: notify per-hop callback
      if (onHop) {
        try {
          onHop(hopIndex, hopPositions.length, hp);
        } catch (e) { }
      }

      const isGhost = hp.ghost;

      if (isGhost) {
        if (!nodeMarkers[hp.key]) {
          const ghost = L.circleMarker(hp.pos, {
            radius: 3, fillColor: '#94a3b8', fillOpacity: 0.35, color: '#94a3b8', weight: 1, opacity: 0.5,
          }).addTo(animLayer);

          activeGhosts.push({ marker: ghost, timeLeft: 3000, lastTick: null });
          if (!isAnimating) {
            isAnimating = true;
            requestAnimationFrame(renderAnimations);
          }
        }
      } else {
        pulseNode(hp.key, hp.pos, typeName);
      }

      if (hopIndex < hopPositions.length - 1) {
        const nextPos = hopPositions[hopIndex + 1].pos;
        const nextGhost = hopPositions[hopIndex + 1].ghost;
        const lineColor = (isGhost || nextGhost) ? '#94a3b8' : color;
        const lineOpacity = (isGhost || nextGhost) ? 0.3 : undefined;
        drawAnimatedLine(hp.pos, nextPos, lineColor, () => { hopIndex++; nextHop(); }, lineOpacity, rawHex, pktMeta?.hash);
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

    const baseColor = marker._baseColor || '#6b7280';
    const baseSize = marker._baseSize || 14;

    activePulses.push({
      pos: pos,
      color: color,
      r: 2,
      op: 0.9,
      hl_r: baseSize / 2 + 4,
      hl_op: 0.95,
      hl_weight: 3,
      startTime: performance.now(),
      lastPulse: null
    });
    if (!isAnimating) {
      isAnimating = true;
      requestAnimationFrame(renderAnimations);
    }

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
        if (dt) { dt.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-moon"/></svg>'; dt.disabled = true; }
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
        _liveSetMarkerColor(marker, '#008a22');
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
        if (dt) { dt.innerHTML = prevTheme === 'dark' ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-moon"/></svg>' : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-sun"/></svg>'; dt.disabled = false; }
        delete container.dataset.matrixPrevTheme;
      } else {
        const dt = document.getElementById('darkModeToggle');
        if (dt) dt.disabled = false;
      }
      for (const [key, marker] of Object.entries(nodeMarkers)) {
        if (marker._matrixPrevColor) {
          marker._baseColor = marker._matrixPrevColor;
          _liveSetMarkerColor(marker, marker._matrixPrevColor);
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
    const DURATION_MS = VCR.mode === 'REPLAY' ? 1100 / VCR.speed : 1100;
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
      if (!animLayer || !pathsLayer) {
        if (onComplete) onComplete();
        return;
      }
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
          if (!animLayer || !pathsLayer) {
            charMarkers.length = 0;
            if (onComplete) onComplete();
            return;
          }
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

  function tickDt(obj, fieldName, now) {
    if (obj[fieldName] === null) obj[fieldName] = now;
    const rawDt = now - obj[fieldName];
    const dt = Math.min(rawDt, 32);
    obj[fieldName] = now;
    const speed = VCR.mode === 'REPLAY' ? (VCR.speed || 1) : 1;
    return dt * speed;
  }

  function stepPulse(pulse, now, isPaused) {
    const scaledDt = tickDt(pulse, 'lastPulse', now);

    if (!isPaused) {
      const dtSec = scaledDt / 1000;
      // Inner pulse (58px/sec, fade out 1.15/sec)
      pulse.r += 58 * dtSec;
      pulse.op -= 1.15 * dtSec;
      // Outer highlight ring (grow 20px/sec, fade out 1.35/sec)
      pulse.hl_r += 20 * dtSec;
      pulse.hl_op -= 1.35 * dtSec;
      pulse.hl_weight = pulse.hl_op > 0.4 ? 3 : 2;
    }
  }

  function renderAnimations(now) {
    if (!animCtx) return;

    if (activeAnimations.length === 0 && activePulses.length === 0 && activeGhosts.length === 0) {
      isAnimating = false;
      animCtx.clearRect(0, 0, animCanvas.clientWidth, animCanvas.clientHeight);
      return;
    }

    const isPaused = VCR.mode === 'PAUSED' || VCR.speed === 0;

    // High-refresh guard: cap redraw at ~60fps. Progress is time-based
    // (tickDt reads each object's own elapsed delta, itself capped at 32ms),
    // so skipping a frame preserves motion exactly — this only stops 120/144Hz
    // displays from doing 2-2.4x the per-frame work for no visible benefit.
    // Skip only while running; paused frames fall through to the sleep path below.
    if (!isPaused && now - _lastAnimFrame < ANIM_MIN_FRAME_MS) {
      requestAnimationFrame(renderAnimations);
      return;
    }
    _lastAnimFrame = now;

    // Clear the canvas for this frame
    animCtx.clearRect(0, 0, animCanvas.clientWidth, animCanvas.clientHeight);

    // Render Ghosts (VCR-aware timeout)
    for (let i = activeGhosts.length - 1; i >= 0; i--) {
      const g = activeGhosts[i];
      if (g.lastTick === null) g.lastTick = now;
      const dt = now - g.lastTick;
      g.lastTick = now;

      if (!isPaused) {
        g.timeLeft -= dt * (VCR.speed || 1);
      }

      if (g.timeLeft <= 0) {
        if (animLayer && animLayer.hasLayer(g.marker)) {
          try { animLayer.removeLayer(g.marker); } catch { }
        }
        activeGhosts.splice(i, 1);
      }
    }

    // Render Pulses
    for (let i = activePulses.length - 1; i >= 0; i--) {
      const pulse = activePulses[i];
      stepPulse(pulse, now, isPaused);

      // Natural completion based purely on scaled simulation time
      // with a 30-second wall-clock safety backstop for engine stalls.
      if (pulse.op <= 0 || now - pulse.startTime > 30000) {
        activePulses.splice(i, 1);
        continue;
      }

      const pulseLayerPt = map.latLngToLayerPoint(pulse.pos);
      const pulsePt = {
        x: pulseLayerPt.x - canvasTopLeft.x,
        y: pulseLayerPt.y - canvasTopLeft.y
      };

      const W = animCanvas.clientWidth;
      const H = animCanvas.clientHeight;
      if (pulsePt.x >= -pulse.r && pulsePt.x <= W + pulse.r && pulsePt.y >= -pulse.r && pulsePt.y <= H + pulse.r) {
        // Inner expanding pulse
        animCtx.beginPath();
        animCtx.arc(pulsePt.x, pulsePt.y, pulse.r, 0, Math.PI * 2);
        animCtx.lineWidth = Math.max(0.3, 3 - pulse.r * 0.04);
        animCtx.strokeStyle = pulse.color;
        animCtx.globalAlpha = Math.max(0, pulse.op);
        animCtx.stroke();

        // Outer highlight ring (previously marker._highlightRing)
        if (pulse.hl_op > 0) {
          animCtx.beginPath();
          animCtx.arc(pulsePt.x, pulsePt.y, pulse.hl_r, 0, Math.PI * 2);
          animCtx.lineWidth = pulse.hl_weight;
          animCtx.strokeStyle = pulse.color;
          animCtx.globalAlpha = Math.max(0, pulse.hl_op);
          animCtx.stroke();
        }
      }
    }
    animCtx.globalAlpha = 1.0;

    for (let i = activeAnimations.length - 1; i >= 0; i--) {
      const anim = activeAnimations[i];
      const scaledDt = tickDt(anim, 'lastTick', now);

      // Advance progress only if we are not paused
      if (!isPaused) {
        anim.progress += scaledDt / 660;
      }

      const t = Math.min(1, anim.progress);

      // Use LayerPoint math so coordinates lock to the moving pane
      const fromLayerPt = map.latLngToLayerPoint(anim.from);
      const toLayerPt = map.latLngToLayerPoint(anim.to);

      // Offset by the canvas's position within the pane to get drawable pixels.
      // #1514 S2 — reuse module-scoped scratch objects instead of allocating per frame.
      const fromPt = _scratchFrom;
      fromPt.x = fromLayerPt.x - canvasTopLeft.x;
      fromPt.y = fromLayerPt.y - canvasTopLeft.y;
      const toPt = _scratchTo;
      toPt.x = toLayerPt.x - canvasTopLeft.x;
      toPt.y = toLayerPt.y - canvasTopLeft.y;

      const W = animCanvas.clientWidth;
      const H = animCanvas.clientHeight;
      const cull = (fromPt.x < 0 && toPt.x < 0) || (fromPt.x > W && toPt.x > W) ||
        (fromPt.y < 0 && toPt.y < 0) || (fromPt.y > H && toPt.y > H);

      if (!cull) {
        const currentX = fromPt.x + (toPt.x - fromPt.x) * t;
        const currentY = fromPt.y + (toPt.y - fromPt.y) * t;

        // Draw Contrail (glow)
        animCtx.beginPath();
        animCtx.moveTo(fromPt.x, fromPt.y);
        animCtx.lineTo(currentX, currentY);
        animCtx.strokeStyle = anim.contrailColor;
        animCtx.lineWidth = 6;
        animCtx.globalAlpha = anim.opacity * 0.2;
        animCtx.lineCap = 'round';
        animCtx.stroke();

        // Draw Core Line
        animCtx.beginPath();
        animCtx.moveTo(fromPt.x, fromPt.y);
        animCtx.lineTo(currentX, currentY);
        if (anim.isDashed) {
          animCtx.setLineDash([4, 6]);
          animCtx.lineWidth = 1.5;
        } else {
          animCtx.lineWidth = 2;
        }
        animCtx.strokeStyle = anim.lineColor;
        animCtx.globalAlpha = anim.opacity;
        animCtx.stroke();
        animCtx.setLineDash([]); // Reset for next draw

        // Draw Leading Dot
        animCtx.beginPath();
        animCtx.arc(currentX, currentY, 3.5, 0, Math.PI * 2);
        animCtx.fillStyle = anim.hashFill;
        animCtx.fill();
        animCtx.lineWidth = 1.5;
        animCtx.strokeStyle = anim.hashOutline;
        animCtx.stroke();
        animCtx.globalAlpha = 1.0; // Reset
      }

      // Handle completion
      if (t >= 1) {
        createFadingLeafletLine(anim);
        if (anim.onComplete) anim.onComplete();
        activeAnimations.splice(i, 1);
      }
    }

    // SLEEP LOGIC: If paused, halt the loop and prepare all animations for a clean wake
    if (isPaused) {
      isAnimating = false;
      for (let i = 0; i < activeAnimations.length; i++) {
        activeAnimations[i].lastTick = null;
      }
      for (let i = 0; i < activeGhosts.length; i++) {
        activeGhosts[i].lastTick = null;
      }
      return; // Stop requesting frames. GPU goes to sleep.
    }

    requestAnimationFrame(renderAnimations);
  }

  function renderFades(now) {
    if (activeFades.length === 0) {
      isFading = false;
      return;
    }
    for (let i = activeFades.length - 1; i >= 0; i--) {
      const f = activeFades[i];
      if (!pathsLayer) continue;
      const fadeElapsed = now - f.lastFade;
              if (fadeElapsed >= 52) {
                const fadeTicks = Math.min(Math.floor(fadeElapsed / 52), 4);
        f.lastFade = now;
        f.opacity -= 0.1 * fadeTicks;
        if (f.opacity <= 0) {
          if (pathsLayer) { pathsLayer.removeLayer(f.line); pathsLayer.removeLayer(f.contrail); }
          recentPaths = recentPaths.filter(p => p.line !== f.line);
          activeFades.splice(i, 1);
        } else {
          f.line.setStyle({ opacity: f.opacity });
          f.contrail.setStyle({ opacity: f.opacity * 0.15 });
        }
      }
    }
    if (activeFades.length > 0) {
      requestAnimationFrame(renderFades);
    } else {
      isFading = false;
    }
  }

  function createFadingLeafletLine(anim) {
    if (!pathsLayer) return;

    const contrail = L.polyline([anim.from, anim.to], {
      pane: 'animationsPane', // #1514 M2 — fades stack with the moving phase (z=625).
      color: anim.contrailColor, weight: 6, opacity: anim.opacity * 0.2, lineCap: 'round'
    }).addTo(pathsLayer);

    const line = L.polyline([anim.from, anim.to], {
      pane: 'animationsPane', // #1514 M2 — fades stack with the moving phase (z=625).
      color: anim.lineColor, weight: anim.isDashed ? 1.5 : 2, opacity: anim.opacity,
      lineCap: 'round', dashArray: anim.isDashed ? '4 6' : null
    }).addTo(pathsLayer);

          recentPaths.push({ line, glowLine: contrail, time: Date.now() });
          while (recentPaths.length > 5) {
            const old = recentPaths.shift();
            if (pathsLayer) { pathsLayer.removeLayer(old.line); pathsLayer.removeLayer(old.glowLine); }
      activeFades = activeFades.filter(f => f.line !== old.line);
    }

    activeFades.push({
      line: line,
      contrail: contrail,
      opacity: anim.opacity,
      lastFade: performance.now()
    });

    if (!isFading) {
      isFading = true;
      requestAnimationFrame(renderFades);
    }
  }

  function drawAnimatedLine(from, to, color, onComplete, overrideOpacity, rawHex, hash) {
    // GUARD: Prevent stale callbacks from pushing to a destroyed map
    if (!map || !animCtx) {
      if (onComplete) onComplete();
      return;
    }

    if (matrixMode) return drawMatrixLine(from, to, color, onComplete, rawHex);

    const mainOpacity = overrideOpacity ?? 0.8;
    const isDashed = overrideOpacity != null;

    var hashFill = '#fff';
    var hashOutline = color;
    var contrailColor = color;

    if (colorByHash && hash && !isDashed && window.HashColor) {
      var hsl = HashColor.hashToHsl(hash, _liveTheme());
      hashFill = hsl;
      hashOutline = HashColor.hashToOutline(hash, _liveTheme());
      contrailColor = hsl;
    }

    // Push to the hardware-accelerated canvas engine
    activeAnimations.push({
      from: from,
      to: to,
      progress: 0, // Start at 0%
      lastTick: null,
      opacity: mainOpacity,
      isDashed: isDashed,
      lineColor: (colorByHash && hash && !isDashed && window.HashColor) ? hashFill : color,
      contrailColor: contrailColor,
      hashFill: hashFill,
      hashOutline: hashOutline,
      onComplete: onComplete
    });

    // WAKE LOGIC: Kickstart the loop if it is currently sleeping
    if (!isAnimating) {
      isAnimating = true;
      requestAnimationFrame(renderAnimations);
    }
  }

  function showHeatMap() {
    // The change handlers are attached before L.map() exists (wireLiveControls
    // runs ahead of the awaits), so a click during page load can land here with
    // `map` still unset. Today that is saved by nodeData being empty at that
    // point; make the guard explicit instead of accidental.
    if (!map) return;
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

  /** Extract channel row style from a packet (shared by feed item builders). */
  function _getChannelStyle(pkt) {
    if (!window.ChannelColors) return '';
    var d = pkt.decoded || {};
    var p = d.payload || {};
    var typeName = p.type || (d.header || {}).payloadTypeName || '';
    var ch = p.channel || null;
    return window.ChannelColors.getRowStyle(typeName, ch);
  }

  /** Build a clickable 12×12 color dot for a channel feed item (#674). */
  function _feedColorDot(channel) {
    if (!channel || !window.ChannelColors) return '';
    var c = window.ChannelColors.get(channel);
    var bg = c || 'transparent';
    var border = c ? c : 'var(--border-color, #555)';
    var style = c
      ? 'background:' + bg + ';border:1px solid ' + border
      : 'background:transparent;border:1px dashed ' + border;
    return '<span class="feed-color-dot" data-channel="' + escapeHtml(channel) + '" style="display:inline-block;width:18px;height:18px;border-radius:50%;' + style + ';cursor:pointer;vertical-align:middle;margin-left:4px;flex-shrink:0" title="Set color for ' + escapeHtml(channel) + '"></span>';
  }

  function addFeedItemDOM(icon, typeName, payload, hops, color, pkt, feed) {
    const text = payload.text || payload.name || '';
    const preview = text ? ' ' + (text.length > 35 ? text.slice(0, 35) + '…' : text) : '';
    const hopStr = hops.length ? `<span class="feed-hops">${hops.length}⇢</span>` : '';
    const obsBadge = pkt.observation_count > 1 ? `<span class="badge badge-obs" style="font-size:10px;margin-left:4px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> ${pkt.observation_count}</span>` : '';
    const iataBadge = obsIataBadgeHtml(pkt);
    const anomalyIcon = (pkt.decoded && pkt.decoded.anomaly) ? '<span title="Anomaly detected" style="margin-left:4px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg></span>' : '';
    var _ccPayload2 = (pkt.decoded || {}).payload || {};
    var _ccChan = (typeName === 'GRP_TXT' || typeName === 'CHAN') ? (_ccPayload2.channel || null) : null;
    var dotHtml = _ccChan ? _feedColorDot(_ccChan) : '';
    const item = document.createElement('div');
    item.className = 'live-feed-item';
    item.setAttribute('tabindex', '0');
    item.setAttribute('role', 'button');
    item.style.cursor = 'pointer';
    // Hash-color stripe for feed items (mirrors packets table border-left)
    if (colorByHash && pkt.hash && window.HashColor) {
      item.style.borderLeft = '4px solid ' + HashColor.hashToHsl(pkt.hash, _liveTheme());
    }
    // Channel color highlighting for GRP_TXT packets (#271)
    var _cs = _getChannelStyle(pkt);
    if (_cs) item.style.cssText += _cs;
    item.innerHTML = `
      <span class="feed-icon" style="color:${color}">${icon}</span>
      <span class="feed-type" style="color:${color}">${typeName}</span>
      ${dotHtml}${transportBadge(pkt.route_type)}${hopStr}${obsBadge}${iataBadge}${anomalyIcon}
      <span class="feed-text">${escapeHtml(preview)}</span>
      <span class="feed-time" data-ts="${pkt._ts || Date.now()}">${formatLiveTimestampHtml(pkt._ts || Date.now())}</span>
    `;
    if (_ccChan) item._ccChannel = _ccChan; // channel color picker (#674)
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
    const feedPanel = document.getElementById('liveFeed');
    const feed = feedPanel ? feedPanel.querySelector('.panel-content') : null;
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
        badge.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> ' + entry.count;
        // Flash + move to top
        entry.element.classList.remove('live-feed-enter');
        void entry.element.offsetWidth; // force reflow
        entry.element.classList.add('live-feed-enter');
        requestAnimationFrame(() => requestAnimationFrame(() => entry.element.classList.remove('live-feed-enter')));
        // Re-add to DOM top (works even if it was trimmed out)
        feed.prepend(entry.element);
        // Update timestamp to latest observation (#701)
        var _dedupTimeSpan = entry.element.querySelector('.feed-time');
        if (_dedupTimeSpan) {
          var _dedupNow = pkt._ts || Date.now();
          _dedupTimeSpan.setAttribute('data-ts', _dedupNow);
          _dedupTimeSpan.innerHTML = formatLiveTimestampHtml(_dedupNow);
        }
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
    const obsBadge = incomingObs > 1 ? `<span class="badge badge-obs" style="font-size:10px;margin-left:4px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> ${incomingObs}</span>` : '';
    const iataBadge = obsIataBadgeHtml(pkt);
    var _ccPayload3 = (pkt.decoded || {}).payload || {};
    var _ccChan3 = (typeName === 'GRP_TXT' || typeName === 'CHAN') ? (_ccPayload3.channel || null) : null;
    var dotHtml3 = _ccChan3 ? _feedColorDot(_ccChan3) : '';

    const item = document.createElement('div');
    item.className = 'live-feed-item live-feed-enter';
    item.setAttribute('tabindex', '0');
    item.setAttribute('role', 'button');
    if (hash) item.setAttribute('data-hash', hash);
    item.style.cursor = 'pointer';
    // Hash-color stripe for feed items (mirrors packets table border-left)
    if (colorByHash && hash && window.HashColor) {
      item.style.borderLeft = '4px solid ' + HashColor.hashToHsl(hash, _liveTheme());
    }
    // Channel color highlighting for GRP_TXT packets (#271)
    var _chanStyle = _getChannelStyle(pkt);
    if (_chanStyle) item.style.cssText += _chanStyle;
    item.innerHTML = `
      <span class="feed-icon" style="color:${color}">${icon}</span>
      <span class="feed-type" style="color:${color}">${typeName}</span>
      ${dotHtml3}${transportBadge(pkt.route_type)}${hopStr}${obsBadge}${iataBadge}
      <span class="feed-text">${escapeHtml(preview)}</span>
      <span class="feed-time" data-ts="${pkt._ts || Date.now()}">${formatLiveTimestampHtml(pkt._ts || Date.now())}</span>
    `;
    if (_ccChan3) item._ccChannel = _ccChan3; // channel color picker (#674)
    item.addEventListener('click', () => showFeedCard(item, pkt, color));
    feed.prepend(item);
    requestAnimationFrame(() => requestAnimationFrame(() => item.classList.remove('live-feed-enter')));
    // #1207: trim to 25 items, but never evict the empty-state placeholder.
    while (feed.querySelectorAll('.live-feed-item').length > 25) {
      var _items = feed.querySelectorAll('.live-feed-item');
      feed.removeChild(_items[_items.length - 1]);
    }

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
      <div class="panel-header" style="border-left:3px solid ${color}">
        <strong>${typeName}</strong>
        ${sender ? `<span class="fdc-sender">${escapeHtml(sender)}</span>` : ''}
        <button class="fdc-close" aria-label="Close"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></button>
      </div>
      ${text ? `<div class="fdc-text">${escapeHtml(text.length > 120 ? text.slice(0, 120) + '…' : text)}</div>` : ''}
      <div class="fdc-meta">
        ${channel ? `<span><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-radio"/></svg> ${escapeHtml(channel)}</span>` : ''}
        ${hops.length ? `<span><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-shuffle"/></svg> ${hops.length} hops</span>` : ''}
        ${snr != null ? `<span><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-cell-signal-high"/></svg> ${Number(snr).toFixed(1)} dB</span>` : ''}
        ${rssi != null ? `<span><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> ${rssi} dBm</span>` : ''}
        ${observer ? `<span><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> ${escapeHtml(observer)}</span>` : ''}
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
    // #1619: register the popup with the live DragManager so users can move
    // it out from behind the legend (responsive gate is handled inside the
    // manager via its `enabled` flag — no extra wiring required here).
    if (window._liveDragMgr) {
      try { window._liveDragMgr.register(card); } catch (_) { /* ignore */ }
    }
  }

  function destroy() {
    // #1514 S3 — drain onComplete callbacks BEFORE clearing the array. Audio
    // `onHop` hooks rely on these firing exactly once per queued animation;
    // previously destroy() dropped them silently when navigating away with
    // packets in flight.
    for (let i = 0; i < activeAnimations.length; i++) {
      const a = activeAnimations[i];
      if (a && typeof a.onComplete === 'function') {
        try { a.onComplete(); } catch (_) {}
      }
    }
    activeAnimations.length = 0;
    activePulses.length = 0;
    activeFades.length = 0;
    activeGhosts.length = 0;
    isAnimating = false;
    isFading = false;
    // #1514 S8 — tear down animation canvas + DPR listener BEFORE map.remove()
    // (Leaflet pane is still attached). Doing this after map.remove() would
    // call clearRect on a context whose backing pane is gone, and a late DPR
    // change could still fire updateAnimCanvas() against a null map.
    if (animCtx && animCanvas) {
      try { animCtx.clearRect(0, 0, animCanvas.clientWidth, animCanvas.clientHeight); } catch (_) {}
      animCanvas.remove();
      animCanvas = null;
      animCtx = null;
    }
    if (_dprMedia && _dprChangeHandler) {
      try { _dprMedia.removeEventListener('change', _dprChangeHandler); } catch (_) {}
      _dprMedia = null;
      _dprChangeHandler = null;
    }
    stopReplay();
    if (_timelineRefreshInterval) { clearInterval(_timelineRefreshInterval); _timelineRefreshInterval = null; }
    if (_lcdClockInterval) { clearInterval(_lcdClockInterval); _lcdClockInterval = null; }
    if (_rateCounterInterval) { clearInterval(_rateCounterInterval); _rateCounterInterval = null; }
    if (_pruneInterval) { clearInterval(_pruneInterval); _pruneInterval = null; }
    if (_feedTimestampInterval) { clearInterval(_feedTimestampInterval); _feedTimestampInterval = null; }
    if (_affinityInterval) { clearInterval(_affinityInterval); _affinityInterval = null; }
    if (ws) { ws.onclose = null; ws.close(); ws = null; }
    if (regionFilterChangeHandler && window.RegionFilter && typeof RegionFilter.offChange === 'function') {
      RegionFilter.offChange(regionFilterChangeHandler);
      regionFilterChangeHandler = null;
    }
    if (map) { map.remove(); map = null; }
    if (_onResize) {
      window.removeEventListener('resize', _onResize);
      window.removeEventListener('orientationchange', _onResize);
      if (window.visualViewport) window.visualViewport.removeEventListener('resize', _onResize);
    }
    if (_vcrHeightCleanup) { try { _vcrHeightCleanup(); } catch (_) {} _vcrHeightCleanup = null; }
    // Restore #app height to CSS default
    const appEl = document.getElementById('app');
    if (appEl) appEl.style.height = '';
    const topNav = document.querySelector('.top-nav');
    if (topNav) { topNav.classList.remove('nav-autohide'); topNav.style.position = ''; topNav.style.width = ''; topNav.style.zIndex = ''; }
    const existingPin = document.getElementById('navPinBtn');
    if (existingPin) existingPin.remove();
    if (document.body) document.body.classList.remove('nav-pinned');
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
    nodesLayer = pathsLayer = animLayer = heatLayer = geoFilterLayer = clickablePathsLayer = null;
    clickablePaths = [];
    stopMatrixRain();
    // #1572 — clear body.live-fullscreen on route exit. The class hides
    // .bottom-nav (the only nav on mobile), so leaking it across SPA
    // routes strands the user. Reset state but DO NOT clear the
    // localStorage preference — restoring fullscreen on return to /live
    // is intentional.
    if (document.body) document.body.classList.remove('live-fullscreen');
    nodeMarkers = {}; nodeData = {};
    activeNodeDetailKey = null;
    recentPaths = [];
    packetCount = 0; activeAnims = 0;
    nodeActivity = {}; pktTimestamps = [];
    feedDedup.clear();
    VCR.buffer = []; VCR.playhead = -1; VCR.mode = 'LIVE'; VCR.missedCount = 0; VCR.speed = _initialSpeed; VCR.replayGen = 0;
  }

  let _themeRefreshHandler = null;

  // #1180 — singleton guard for the wireLiveCollapseToggles() narrow-viewport
  // MQL listener. MediaQueryList is process-global per query string; without
  // this gate, every SPA re-mount of /live registers a new 'change' handler.
  // The handler reads from current DOM each time, so a one-shot bind is safe
  // across re-mounts. window.__liveMQLBindCount is a debug seam consumed by
  // test-live-mql-leak-1180-e2e.js and otherwise unused.
  var _liveNarrowMqlBound = false;
  // #1514 S4 — single source of truth for window._liveTestSeams is at the
  // earlier exposure block (search for `window._liveTestSeams = {`). The
  // duplicate definition that lived here previously added a `_liveTestSeams.wake`
  // that bypassed pause/empty-queue guards; tests must use the production
  // `wakeCanvasEngine` exposed there.

  registerPage('live', {
    init: function(app, routeParam) {
      _themeRefreshHandler = () => {
        rebuildFeedList();
        if (activeNodeDetailKey) showNodeDetail(activeNodeDetailKey);
      };
      window.addEventListener('theme-refresh', _themeRefreshHandler);
      var result = init(app, routeParam);
      // Install channel color picker (M2, #271)
      if (window.ChannelColorPicker) window.ChannelColorPicker.installLiveFeed();
      return result;
    },
    destroy: function() {
      if (_themeRefreshHandler) { window.removeEventListener('theme-refresh', _themeRefreshHandler); _themeRefreshHandler = null; }
      return destroy();
    }
  });
})();
