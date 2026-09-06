/* === CoreScope — observers.js === */
'use strict';

// Issue #1478 — naive-clock warning chip.
// Exposed as a window global so the renderer (and a jsdom-style test) can
// call it without depending on the IIFE-scoped helpers below. Returns the
// chip HTML when the observer's clock is currently flagged naive, or "" when
// it's clean — never throws.
window.ObserversNaiveChip = {
  render: function (o) {
    if (!o || o.clock_naive !== true) return '';
    var sec = Number(o.clock_skew_seconds || 0);
    var absSec = Math.abs(sec);
    var magnitude;
    if (absSec >= 3600) {
      magnitude = (absSec / 3600).toFixed(absSec >= 36000 ? 0 : 1) + 'h';
    } else if (absSec >= 60) {
      magnitude = Math.round(absSec / 60) + 'm';
    } else {
      magnitude = absSec + 's';
    }
    var dir = sec < 0 ? 'behind' : 'ahead of';
    var count = Number(o.clock_skew_count_24h || 0);
    var tip = 'Observer\u2019s local clock is ' + magnitude + ' ' + dir
      + ' UTC — per-packet timing for this observer is being clamped to ingest time'
      + (count > 0 ? ' (' + count + ' events in last 24h)' : '')
      + '. Fix: set the host clock to UTC, or emit Z-suffixed/offset-aware timestamps.';
    return '<span class="obs-clock-naive-chip" title="' + tip.replace(/"/g, '&quot;')
      + '" aria-label="' + tip.replace(/"/g, '&quot;')
      + '" style="margin-left:6px;cursor:help">'
      + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>'
      + '</span>';
  },
};

// #1562 — Pure helper for computing + rendering the observers-page aggregate
// header. Split out as a window global so it's unit-testable in a vm sandbox
// without needing a DOM, AND so the render path has one obvious place to wire
// the "Last updated: X ago" freshness label.
//
// Why this exists (per #1562): the header used to derive Online/Stale/Offline
// counts inline from a possibly-stale cached /api/observers payload, with no
// UI signal that the data could be stale. After #1551 added Cache-Control:
// no-store on the server response, the in-memory client cache (api() ttl)
// could still serve old data. The "Last updated" label makes that visible;
// manual refresh now also bypasses the cache (bust: true).
window.ObserversSummary = (function () {
  // #1563 — Single source of truth: aggregate counts MUST come from the
  // same classifier used to render the per-row dots. Previously this helper
  // had its own hardcoded thresholds parallel to healthStatus(); operators
  // would see "5 Online" in the header but count 12 green rows by hand
  // (regression of #1562). We now delegate to window.observerHealthStatus
  // (exposed below by the IIFE) and map its returned .cls to the bucket.
  //
  // A default fallback classifier is kept for the case where this module
  // is loaded BEFORE observers.js wires up window.observerHealthStatus
  // (e.g. legacy test paths). It mirrors the canonical thresholds, but
  // production code paths always hit window.observerHealthStatus.
  function defaultClassify(lastSeen) {
    if (!lastSeen) return { cls: 'health-red' };
    var ago = Date.now() - new Date(lastSeen).getTime();
    var tolerance = 30000;
    if (ago < 600000 + tolerance) return { cls: 'health-green' };
    if (ago < 3600000 + tolerance) return { cls: 'health-yellow' };
    return { cls: 'health-red' };
  }

  function computeCounts(observers) {
    var online = 0, stale = 0, offline = 0;
    var list = Array.isArray(observers) ? observers : [];
    var classifier = (typeof window !== 'undefined' && typeof window.observerHealthStatus === 'function')
      ? window.observerHealthStatus
      : defaultClassify;
    for (var i = 0; i < list.length; i++) {
      var h = classifier(list[i] && list[i].last_seen) || { cls: 'health-red' };
      if (h.cls === 'health-green') online++;
      else if (h.cls === 'health-yellow') stale++;
      else offline++;
    }
    return { online: online, stale: stale, offline: offline, total: list.length };
  }

  // Renders the obs-summary block as a string. fetchedAt is a ms epoch
  // timestamp (or null/0 for "unknown" — first render before any successful
  // fetch). When fetchedAt is older than 60s, the timestamp gets the
  // obs-updated-stale class so operators see the data is going stale.
  function renderHeader(counts, fetchedAt) {
    var c = counts || { online: 0, stale: 0, offline: 0, total: 0 };
    var updatedHtml = '';
    if (fetchedAt) {
      var ageMs = Date.now() - fetchedAt;
      var staleCls = ageMs > 60000 ? ' obs-updated-stale' : '';
      var iso = new Date(fetchedAt).toISOString();
      updatedHtml = '<span class="obs-stat obs-updated' + staleCls
        + '" data-action="obs-refresh" role="button" tabindex="0"'
        + ' title="Click to force a fresh fetch (bypass client cache)"'
        + ' aria-label="Last updated ' + timeAgo(iso) + ' — click to refresh">'
        + 'Last updated: ' + timeAgo(iso) + '</span>';
    }
    return ''
      + '<div class="obs-summary">'
      +   '<span class="obs-stat"><span class="health-dot health-green"><svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"></use></svg></span> ' + c.online + ' Online</span>'
      +   '<span class="obs-stat"><span class="health-dot health-yellow"><svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-triangle"></use></svg></span> ' + c.stale + ' Stale</span>'
      +   '<span class="obs-stat"><span class="health-dot health-red"><svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-x"></use></svg></span> ' + c.offline + ' Offline</span>'
      +   '<span class="obs-stat"><svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-broadcast"></use></svg> ' + c.total + ' Total</span>'
      +   updatedHtml
      + '</div>';
  }

  return { computeCounts: computeCounts, renderHeader: renderHeader };
})();

// #1644 — preserveCompareSelection
//
// Why: renderObservers() rewrites <tbody>.innerHTML on every 30s
// refresh (and on every WS-driven repaint), which destroys every
// `<input data-compare-select>` node along with its checked state.
// That manifested as "checkboxes randomly uncheck each other" — it
// wasn't sibling interference, it was the whole tbody being replaced
// underneath an active selection.
//
// The fix is surgical: snapshot the set of ids that were checked
// BEFORE we touch the DOM, then walk the new boxes after `innerHTML=`
// and re-set .checked on any whose `value` (observer id) was in the
// snapshot. O(n) over visible rows.
//
// Pure helper so it's testable in jsdom-less Node (see
// test-issue-1644-redesign.js). DO NOT inline this in render() —
// the unit test introspects the global by name.
window.preserveCompareSelection = function preserveCompareSelection(prevIds, tbody) {
  if (!tbody || !prevIds || typeof tbody.querySelectorAll !== 'function') return;
  var boxes = tbody.querySelectorAll('input[data-compare-select]');
  for (var i = 0; i < boxes.length; i++) {
    if (prevIds.has(boxes[i].value)) boxes[i].checked = true;
  }
};

(function () {
  let observers = [];
  let _fetchedAt = 0; // #1562: ms epoch when the current `observers` payload was received
  let _loadObserversReqId = 0; // #1563: monotonic id; resolutions older than the latest are discarded
  let obsSkewMap = {}; // observerID → {offsetSec, samples}
  let wsHandler = null;
  let refreshTimer = null;
  let regionChangeHandler = null;
  let _obsSortCtl = null; // #1641 r1 finding #6: tracked TableSort controller for destroy-before-reinit
  let _mqttPanelTeardown = null; // #1043: teardown fn for the MQTT status panel timer

  function init(app) {
    app.innerHTML = `
      <div class="observers-page">
        <div class="page-header">
          <h2>Observer Status</h2>
          <button type="button" class="btn-secondary" data-action="compare-observers"
                  title="Compare two observers side-by-side"
                  aria-label="Compare observers">
            <svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"></use></svg><span>Compare observers</span>
          </button>
          <button type="button" class="btn-secondary" data-action="compare-selected"
                  title="Select exactly two rows to compare"
                  aria-label="Compare selected observers"
                  aria-disabled="true" disabled>
            <svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-scales"></use></svg><span>Compare selected (<span data-role="compare-count">0</span>)</span>
          </button>
          <span class="obs-refresh-spacer"></span>
          <button class="btn-icon" data-action="obs-refresh" title="Refresh" aria-label="Refresh observers"><svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-arrow-clockwise"></use></svg></button>
        </div>
        <div id="obsRegionFilter" class="region-filter-container"></div>
        <div id="mqttStatusPanel" class="mqtt-status-panel-container"></div>
        <div id="obsContent"><div class="text-center text-muted" style="padding:40px">Loading…</div></div>
      </div>`;
    RegionFilter.init(document.getElementById('obsRegionFilter'));
    // #1043: mount the MQTT source status panel above the observers
    // table. Self-refreshes every 10s; tear down in destroy() so the
    // SPA route change doesn't leak the timer.
    if (typeof window !== 'undefined' && window.MqttStatusPanel) {
      _mqttPanelTeardown = window.MqttStatusPanel.mount(document.getElementById('mqttStatusPanel'));
    }
    regionChangeHandler = RegionFilter.onChange(function () { render(); });
    loadObservers();
    // Event delegation for data-action buttons
    app.addEventListener('click', function (e) {
      var btn = e.target.closest('[data-action]');
      if (btn && btn.dataset.action === 'obs-refresh') loadObservers({ bust: true });
      if (btn && btn.dataset.action === 'compare-observers') {
        location.hash = '#/compare';
        return;
      }
      if (btn && btn.dataset.action === 'compare-selected') {
        var picked = collectSelectedIds();
        if (picked.length === 2) {
          location.hash = '#/compare?a=' + encodeURIComponent(picked[0]) + '&b=' + encodeURIComponent(picked[1]);
        }
        return;
      }
      // #1640 — per-row checkbox: toggle, update compare-selected button state.
      var cb = e.target.closest('input[data-compare-select]');
      if (cb) {
        updateCompareSelectedState();
        return;
      }
      var row = e.target.closest('tr[data-action="navigate"]');
      if (row) {
        // #1056 AC#4: at narrow widths, open detail in slide-over instead of
        // navigating to a separate page.
        if (window.SlideOver && window.SlideOver.shouldUse()) {
          e.preventDefault();
          openObserverSlideOver(row.dataset.value);
          return;
        }
        location.hash = row.dataset.value;
      }
    });
    // #209 — Keyboard accessibility for observer rows
    app.addEventListener('keydown', function (e) {
      // #1562 — Last-updated pill (role=button) supports Enter/Space to
      // force a fresh fetch, matching click behavior on data-action="obs-refresh".
      var refreshBtn = e.target.closest('[data-action="obs-refresh"]');
      if (refreshBtn && (e.key === 'Enter' || e.key === ' ')) {
        e.preventDefault();
        loadObservers({ bust: true });
        return;
      }
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      if (window.SlideOver && window.SlideOver.shouldUse()) {
        openObserverSlideOver(row.dataset.value);
        return;
      }
      location.hash = row.dataset.value;
    });
    // Auto-refresh every 30s
    refreshTimer = setInterval(loadObservers, 30000);
    wsHandler = debouncedOnWS(function (msgs) {
      if (msgs.some(function (m) { return m.type === 'packet'; })) loadObservers();
    });
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (refreshTimer) clearInterval(refreshTimer);
    refreshTimer = null;
    if (regionChangeHandler) RegionFilter.offChange(regionChangeHandler);
    regionChangeHandler = null;
    if (_obsSortCtl && typeof _obsSortCtl.destroy === 'function') {
      try { _obsSortCtl.destroy(); } catch (_) { /* ignore */ }
    }
    _obsSortCtl = null;
    if (typeof _mqttPanelTeardown === 'function') {
      try { _mqttPanelTeardown(); } catch (_) { /* ignore */ }
    }
    _mqttPanelTeardown = null;
    observers = [];
    obsSkewMap = {};
  }

  async function loadObservers(opts) {
    var bust = !!(opts && opts.bust);
    // #1563 — in-flight guard: every call gets a monotonic id; when we
    // resolve, if a newer call has started, drop this result silently.
    // Prevents a slow auto-refresh from clobbering a fresh manual bust
    // (or vice versa) with stale data + a misleading "0s ago" pill.
    var myId = ++_loadObserversReqId;
    try {
      const [data, skewData] = await Promise.all([
        api('/observers', { ttl: CLIENT_TTL.observers, bust: bust }),
        api('/observers/clock-skew', { ttl: 30000 }).catch(function() { return []; })
      ]);
      if (myId !== _loadObserversReqId) return; // stale resolve, newer in-flight
      observers = data.observers || [];
      _fetchedAt = Date.now(); // #1562: stamp freshness for the header label
      obsSkewMap = {};
      (Array.isArray(skewData) ? skewData : []).forEach(function(s) {
        if (s && s.observerID) obsSkewMap[s.observerID] = s;
      });
      render();
    } catch (e) {
      if (myId !== _loadObserversReqId) return; // discard stale error too
      document.getElementById('obsContent').innerHTML =
        `<div class="text-muted" role="alert" aria-live="polite" style="padding:40px">Error loading observers: ${e.message}</div>`;
    }
  }

  // NOTE: Comparing server timestamps to Date.now() can skew if client/server
  // clocks differ. We add ±30s tolerance to thresholds to reduce false positives.
  function healthStatus(lastSeen) {
    if (!lastSeen) return { cls: 'health-red', label: 'Unknown' };
    const ago = Date.now() - new Date(lastSeen).getTime();
    const tolerance = 30000; // 30s tolerance for clock skew
    // Issue #1552 — thresholds are operator-configurable via config.json
    // healthThresholds.observerOnlineMinutes / observerStaleMinutes, surfaced
    // to the client through window.HEALTH_THRESHOLDS. Defaults are 60 min
    // Online / 1440 min (24h) Stale, matching node thresholds (#1552).
    const th = (typeof window !== 'undefined' && window.HEALTH_THRESHOLDS) || {};
    const onlineMs = th.observerOnlineMs || 3600000;
    const staleMs = th.observerStaleMs || 86400000;
    if (ago < onlineMs + tolerance) return { cls: 'health-green', label: 'Online' };
    if (ago < staleMs + tolerance) return { cls: 'health-yellow', label: 'Stale' };
    return { cls: 'health-red', label: 'Offline' };
  }
  // Issue #1552 — exposed for tests and external callers.
  // #1563 — Expose for ObserversSummary so aggregate counts and per-row dots
  // share ONE classifier (single source of truth). If anything reintroduces
  // parallel thresholds, the new ObserversSummary regression test breaks.
  window.observerHealthStatus = healthStatus;

  function packetBadge(o) {
    const warnIcon = '<svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-warning"></use></svg>';
    const broadcastIcon = '<svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-broadcast"></use></svg>';
    if (!o.last_packet_at) return '<span title="No packets ever observed">' + broadcastIcon + warnIcon + ' never</span>';
    const pktAgo = Date.now() - new Date(o.last_packet_at).getTime();
    const statusAgo = o.last_seen ? Date.now() - new Date(o.last_seen).getTime() : Infinity;
    const gap = pktAgo - statusAgo;
    if (gap > 600000) {
      return `<span title="Last packet ${timeAgo(o.last_packet_at)} — status is newer by ${Math.round(gap/60000)}min. Observer may be alive but not forwarding packets.">${broadcastIcon}${warnIcon} ${timeAgo(o.last_packet_at)}</span>`;
    }
    return timeAgo(o.last_packet_at);
  }

  // #1789 — Firmware strings can be long, e.g.
  // "v1.16.0-07a3ca9 Build: 2025-..."; truncate the "Build:" suffix for the
  // display string, keeping the full value in the cell's title= attr.
  function truncateBuildSuffix(s) {
    if (!s) return '';
    const idx = s.indexOf('Build:');
    if (idx < 0) return s;
    return s.slice(0, idx).replace(/[\s,;-]+$/, '');
  }

  function uptimeStr(firstSeen) {
    if (!firstSeen) return '—';
    const ms = Date.now() - new Date(firstSeen).getTime();
    const d = Math.floor(ms / 86400000);
    const h = Math.floor((ms % 86400000) / 3600000);
    if (d > 0) return `${d}d ${h}h`;
    const m = Math.floor((ms % 3600000) / 60000);
    return h > 0 ? `${h}h ${m}m` : `${m}m`;
  }

  function sparkBar(count, max) {
    if (max === 0) return `<span class="text-muted">0/hr</span>`;
    const pct = Math.min(100, Math.round((count / max) * 100));
    return `<span style="display:inline-flex;align-items:center;gap:6px;white-space:nowrap"><span style="display:inline-block;width:60px;height:12px;background:var(--border);border-radius:3px;overflow:hidden;vertical-align:middle"><span style="display:block;height:100%;width:${pct}%;background:linear-gradient(90deg,#3b82f6,#60a5fa);border-radius:3px"></span></span><span style="font-size:11px">${count}/hr</span></span>`;
  }

  function render() {
    const el = document.getElementById('obsContent');
    if (!el) return;

    // #1644 — snapshot compare-selection BEFORE we rewrite tbody.innerHTML.
    // The 30s auto-refresh (and any WS-driven repaint) destroys every
    // checkbox node; without this snapshot the user's selection silently
    // vanished. See window.preserveCompareSelection above.
    var _prevSelected = new Set();
    var _prevBoxes = document.querySelectorAll(
      '#obsTable tbody input[data-compare-select]:checked'
    );
    for (var _i = 0; _i < _prevBoxes.length; _i++) _prevSelected.add(_prevBoxes[_i].value);

    // Apply region filter
    const selectedRegions = RegionFilter.getSelected();
    const filtered = selectedRegions
      ? observers.filter(o => o.iata && selectedRegions.includes(o.iata))
      : observers;

    if (filtered.length === 0) {
      el.innerHTML = '<div class="text-center text-muted" style="padding:40px">No observers found.</div>';
      return;
    }

    const maxPktsHr = Math.max(1, ...filtered.map(o => o.packetsLastHour || 0));

    // #1562 — Aggregate counts + "Last updated" freshness label come from the
    // pure ObserversSummary helper (unit-tested in test-issue-1562-*).
    const summaryCounts = window.ObserversSummary.computeCounts(filtered);
    const summaryHtml = window.ObserversSummary.renderHeader(summaryCounts, _fetchedAt);

    el.innerHTML = `
      ${summaryHtml}
      <div class="obs-table-scroll table-fluid-wrap"><table class="data-table obs-table" id="obsTable">
        <caption class="sr-only">Observer status and statistics</caption>
        <thead><tr>
          <th scope="col" data-priority="1" data-sort-key="status" data-type="numeric">Status</th><th scope="col" data-priority="1" data-sort-key="name">Name</th><th scope="col" data-priority="3" data-sort-key="region">Region</th><th scope="col" data-priority="2" data-sort-key="last_seen" data-type="numeric">Last Status</th><th scope="col" data-priority="2" data-sort-key="last_packet_at" data-type="numeric">Last Packet</th>
          <th scope="col" data-priority="3" data-sort-key="packet_health" data-type="numeric">Packet Health</th><th scope="col" data-priority="4" data-sort-key="packet_count" data-type="numeric">Total Packets</th><th scope="col" data-priority="3" data-sort-key="packets_hour" data-type="numeric">Packets/Hour</th><th scope="col" data-priority="4" data-sort-key="clock_offset" data-type="numeric">Clock Offset</th><th scope="col" data-priority="4" data-sort-key="uptime" data-type="numeric">Uptime</th><th scope="col" data-priority="4" data-sort-key="firmware">Firmware</th><th scope="col" data-priority="4" data-sort-key="client_version">Client</th>
          <th scope="col" data-priority="1" class="col-compare-select" style="width:32px"><span class="sr-only">Select for compare</span></th>
        </tr></thead>
        <tbody>${filtered.map(o => {
          const h = healthStatus(o.last_seen);
          const shapeIcon = h.cls === 'health-green'
            ? '<svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"></use></svg>'
            : h.cls === 'health-yellow'
            ? '<svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-triangle"></use></svg>'
            : '<svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-x"></use></svg>';
          // TableSort reads NaN-sortable raw values from each cell's
          // data-value attr (table-sort.js comparators.numeric/time sort
          // NaN last). Empty string ⇒ NaN ⇒ missing-sorts-last.
          const _lastSeenMs  = o.last_seen      ? new Date(o.last_seen).getTime()      : '';
          const _lastPktMs   = o.last_packet_at ? new Date(o.last_packet_at).getTime() : '';
          const _firstSeenMs = o.first_seen     ? new Date(o.first_seen).getTime()     : 0;
          const _uptimeMs    = _firstSeenMs ? (Date.now() - _firstSeenMs) : '';
          const _skewSec = (obsSkewMap[o.id] && obsSkewMap[o.id].samples)
            ? Math.abs(Number(obsSkewMap[o.id].offsetSec) || 0)
            : '';
          // #1641 r1 finding #1: Packet Health sortable rank must differ
          // from Last Packet — use the health bucket, not the timestamp.
          // Higher rank = healthier; matches the desc-by-default semantics.
          const _healthRank = h.cls === 'health-green' ? 2 : (h.cls === 'health-yellow' ? 1 : 0);
          const _packetCount = (o.packet_count != null) ? o.packet_count : '';
          const _packetsHour = (o.packetsLastHour != null) ? o.packetsLastHour : '';
          return `<tr style="cursor:pointer" tabindex="0" role="row" data-action="navigate" data-value="#/observers/${encodeURIComponent(o.id)}" data-observer-id="${escapeHtml(o.id)}" onclick="location.hash='#/observers/${encodeURIComponent(o.id)}'">
            <td data-value="${_healthRank}"><span class="health-dot ${h.cls}" title="${h.label}">${shapeIcon}</span> ${h.label}</td>
            <td data-testid="obs-cell-name" data-value="${escapeHtml(String(o.name || o.id))}" class="mono">${escapeHtml(o.name || o.id)}${window.ObserversNaiveChip.render(o)}${o.can_relay === false ? ' <span class="badge-listener" title="Firmware reported repeat:off — listener-only; excluded from path-hop disambiguator (issue #1290)">listener</span>' : (o.can_relay === true ? ' <span class="badge-repeater" title="Firmware reported repeat:on — eligible as a path hop">repeater</span>' : '')}</td>
            <td data-value="${escapeHtml(o.iata || '')}">${o.iata ? `<span class="badge-region">${o.iata}</span>` : '—'}</td>
            <td data-value="${_lastSeenMs}">${timeAgo(o.last_seen)}</td>
            <td data-testid="obs-cell-last-packet" data-value="${_lastPktMs}">${o.last_packet_at ? timeAgo(o.last_packet_at) : '<span class="text-muted">—</span>'}</td>
            <td data-value="${_healthRank}">${packetBadge(o)}</td>
            <td data-testid="obs-cell-packet-count" data-value="${_packetCount}">${(o.packet_count || 0).toLocaleString()}</td>
            <td data-value="${_packetsHour}">${sparkBar(o.packetsLastHour || 0, maxPktsHr)}</td>
            <td data-value="${_skewSec}">${(function() {
              var sk = obsSkewMap[o.id];
              if (!sk || sk.samples == null || sk.samples === 0) return '<span class="text-muted">—</span>';
              var sev = observerSkewSeverity(sk.offsetSec);
              return renderSkewBadge(sev, sk.offsetSec) + ' <span class="text-muted" title="Computed from ' + sk.samples + ' multi-observer packets. Positive = observer ahead of consensus.">(' + sk.samples + ')</span>';
            })()}</td>
            <td data-value="${_uptimeMs}">${uptimeStr(o.first_seen)}</td>
            <td data-testid="obs-cell-firmware" data-value="${escapeHtml(String(o.firmware || ''))}" class="mono"${o.firmware ? ` title="${escapeHtml(String(o.firmware))}"` : ''}>${o.firmware ? escapeHtml(truncateBuildSuffix(String(o.firmware))) : '<span class="text-muted">—</span>'}</td>
            <td data-testid="obs-cell-client-version" data-value="${escapeHtml(String(o.client_version || ''))}" class="mono"${o.client_version ? ` title="${escapeHtml(String(o.client_version))}"` : ''}>${o.client_version ? escapeHtml(String(o.client_version)) : '<span class="text-muted">—</span>'}</td>
            <td class="col-compare-select" onclick="event.stopPropagation()" style="text-align:center">
              <input type="checkbox" data-compare-select value="${escapeHtml(o.id)}"
                     aria-label="Select ${escapeHtml(o.name || o.id)} for comparison"
                     onclick="event.stopPropagation()" />
            </td>
          </tr>`;
        }).join('')}</tbody>
      </table></div>`;
    makeColumnsResizable('#obsTable', 'meshcore-obs-col-widths');
    const obsTbl = document.getElementById('obsTable');
    // #1644 — restore previously-checked compare-select boxes.
    if (obsTbl && _prevSelected.size > 0) {
      var _tbody = obsTbl.querySelector('tbody');
      if (_tbody) window.preserveCompareSelection(_prevSelected, _tbody);
    }
    // Refresh the disabled/enabled state of "Compare selected (N)" against
    // the post-restore reality.
    updateCompareSelectedState();
    // #1056: fluid columns + +N hidden pill
    if (obsTbl && window.TableResponsive) {
      window.TableResponsive.register(obsTbl);
    }
    // #1639 — wire TableSort AFTER tbody is in the DOM (avoid the #679 init
    // race). Re-init on every render so the controller binds to the new
    // header instance, and persist sort state under meshcore-observers-sort.
    if (obsTbl && window.TableSort) {
      // #1641 r1 finding #6: destroy the prior controller before re-init so
      // we don't leak header click listeners across the 30s render cycle.
      if (_obsSortCtl && typeof _obsSortCtl.destroy === 'function') {
        try { _obsSortCtl.destroy(); } catch (_) { /* ignore */ }
      }
      _obsSortCtl = TableSort.init(obsTbl, {
        defaultColumn: 'last_seen',
        defaultDirection: 'desc',
        storageKey: 'meshcore-observers-sort'
      });
    } else if (obsTbl && !window.TableSort) {
      // #1641 r1 finding #14: surface bundler/loading regressions instead of
      // silently rendering an unsortable table.
      console.warn('[observers] window.TableSort missing — table will not be sortable');
    }
  }


  registerPage('observers', { init, destroy });

  // #1640 — multi-select compare wiring.
  function collectSelectedIds() {
    var boxes = document.querySelectorAll('#obsTable tbody input[data-compare-select]:checked');
    var ids = [];
    for (var i = 0; i < boxes.length; i++) ids.push(boxes[i].value);
    return ids;
  }
  function updateCompareSelectedState() {
    var btn = document.querySelector('[data-action="compare-selected"]');
    var ids = collectSelectedIds();
    // #1644 — toggle a table-level marker so CSS can highlight the
    // checkbox column only when something is actually selected. Avoids
    // the column dominating the eye when nothing is picked.
    var tbl = document.getElementById('obsTable');
    if (tbl) tbl.classList.toggle('has-compare-selection', ids.length > 0);
    if (!btn) return;
    var countEl = btn.querySelector('[data-role="compare-count"]');
    if (countEl) countEl.textContent = String(ids.length);
    var enabled = ids.length === 2;
    btn.disabled = !enabled;
    btn.setAttribute('aria-disabled', enabled ? 'false' : 'true');
    btn.title = enabled
      ? 'Compare the two selected observers'
      : 'Select exactly two observers to enable (currently ' + ids.length + ')';
  }
  // Wire change events globally so the state updates even when checkbox
  // toggles by keyboard (space) rather than mouse click.
  document.addEventListener('change', function (e) {
    if (e.target && e.target.matches && e.target.matches('input[data-compare-select]')) {
      updateCompareSelectedState();
    }
  });

  // #1056 AC#4: row-detail slide-over (narrow viewports). Renders a compact
  // summary from the in-memory observer + a link to the full page.
  function openObserverSlideOver(hashHref) {
    if (!window.SlideOver) return;
    var m = String(hashHref || '').match(/#\/observers\/(.+)$/);
    if (!m) return;
    var id = decodeURIComponent(m[1]);
    var o = (observers || []).find(function (x) { return String(x.id) === id; });
    if (!o) return;
    var h = healthStatus(o.last_seen);
    var sk = obsSkewMap[o.id];
    var skewLine = (sk && sk.samples) ? renderSkewBadge(observerSkewSeverity(sk.offsetSec), sk.offsetSec) + ' (' + sk.samples + ' samples)' : '—';
    var pkts = sparkBar(o.packetsLastHour || 0, Math.max(1, o.packetsLastHour || 1));
    var content = window.SlideOver.open({ title: (o.name || o.id) });
    var naiveChipHTML = window.ObserversNaiveChip.render(o);
    content.innerHTML =
      (naiveChipHTML ? '<div style="margin-bottom:10px">' + naiveChipHTML + ' <span class="text-muted">Clock is naive — per-packet timing clamped to ingest time. Clears itself 24h after the last skew event.</span></div>' : '') +
      '<dl class="slide-over-dl" style="margin:0;display:grid;grid-template-columns:auto 1fr;gap:6px 12px;font-size:13px">' +
        '<dt>Status</dt><dd><span class="health-dot ' + h.cls + '"><svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"></use></svg></span> ' + h.label + '</dd>' +
        '<dt>Region</dt><dd>' + (o.iata ? '<span class="badge-region">' + o.iata + '</span>' : '—') + '</dd>' +
        '<dt>Last status</dt><dd>' + timeAgo(o.last_seen) + '</dd>' +
        '<dt>Last packet</dt><dd>' + (o.last_packet_at ? timeAgo(o.last_packet_at) : '—') + '</dd>' +
        '<dt>Total packets</dt><dd>' + (o.packet_count || 0).toLocaleString() + '</dd>' +
        '<dt>Packets/hr</dt><dd>' + pkts + '</dd>' +
        '<dt>Clock offset</dt><dd>' + skewLine + '</dd>' +
        '<dt>Uptime</dt><dd>' + uptimeStr(o.first_seen) + '</dd>' +
      '</dl>' +
      '<p style="margin-top:14px"><a class="btn-primary" href="' + hashHref + '">Open full detail →</a></p>';
  }
})();
