/* === CoreScope — observers.js === */
'use strict';

(function () {
  let observers = [];
  let wsHandler = null;
  let refreshTimer = null;
  let regionChangeHandler = null;

  function init(app) {
    app.innerHTML = `
      <div class="observers-page">
        <div class="page-header">
          <h2>Observer Status</h2>
          <a href="#/compare" class="btn-icon" title="Compare observers" aria-label="Compare observers" style="text-decoration:none">🔍</a>
          <button class="btn-icon" data-action="obs-refresh" title="Refresh" aria-label="Refresh observers">🔄</button>
        </div>
        <div class="obs-help">
          <div class="help-box">
            <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px;cursor:pointer" data-action="toggle-help">
              <strong>ℹ️ How to connect your observer to Cornmeister.nl</strong>
              <span class="help-toggle" style="font-size:14px;user-select:none">▶</span>
            </div>

            <div class="help-content" style="overflow:hidden;transition:max-height 0.3s ease;max-height:0px">
              <div class="text-muted" style="font-size:12px;margin-bottom:12px">
                Connect your node to the Cornmeister MQTT broker to share raw packets.
              </div>

              <table class="help-table">
                <tr><td>Server:</td><td><code>mqtt.cornmeister.nl</code></td></tr>
                <tr><td>Port (TLS):</td><td><code>8883</code></td></tr>
                <tr><td>Port (plain):</td><td><code>1883</code></td></tr>
                <tr><td>Transport:</td><td><code>TCP</code></td></tr>
                <tr><td>Username:</td><td><code>observer</code></td></tr>
                <tr><td>Password:</td><td><code>hiermetdiedata</code></td></tr>
              </table>

              <div style="margin-top:16px">
                <strong>Alternative (Meshwiki Community MQTT)</strong>
                <div class="text-muted" style="font-size:12px;margin-top:4px;margin-bottom:8px">
                  Community endpoint shares data with multiple projects:
                </div>

                <table class="help-table">
                  <tr><td>Server:</td><td><code>mqtt.mwiki.nl</code></td></tr>
                  <tr><td>Port (TLS):</td><td><code>8883</code></td></tr>
                  <tr><td>Port (plain):</td><td><code>1883</code></td></tr>
                  <tr><td>Transport:</td><td><code>TCP</code></td></tr>
                  <tr><td>Username:</td><td><code>observer</code></td></tr>
                  <tr><td>Password:</td><td><code>86w7bW9NJxuPcErp2Y5NCQ==</code></td></tr>
                </table>
              </div>

              <hr style="margin:16px 0;border:none;border-top:1px solid var(--border)">

              <div style="margin-bottom:16px">
                <strong>MQTT Bridge Firmware Commands</strong>
                <div class="text-muted" style="font-size:12px;margin-top:4px;margin-bottom:12px">
                  Paste into your device console:
                </div>

                <div style="margin-bottom:12px;padding:10px 12px;background:var(--surface-0);border:1px solid var(--border);border-radius:6px">
                  <strong style="font-size:12px">📍 Your region (IATA code)</strong>
                  <div class="text-muted" style="font-size:12px;margin-top:4px;margin-bottom:8px">
                    CoreScope groups observers by the IATA airport code nearest to them.
                    If your observer shows as <strong>Offline</strong> or doesn't appear in the list, you most likely haven't set this yet.
                    Pick your region below — the commands update automatically.
                  </div>
                  <select id="obsIataSelect" style="width:100%;padding:5px 8px;border:1px solid var(--border);border-radius:6px;background:var(--input-bg);color:var(--text);font-size:12px;cursor:pointer">
                    <option value="AMS">AMS – Amsterdam Schiphol</option>
                    <option value="RTM">RTM – Rotterdam The Hague</option>
                    <option value="EIN">EIN – Eindhoven</option>
                    <option value="MST">MST – Maastricht Aachen</option>
                    <option value="GRQ">GRQ – Groningen Eelde</option>
                    <option value="LEY">LEY – Lelystad</option>
                    <option value="DHR">DHR – Den Helder (De Kooy)</option>
                    <option value="ENS">ENS – Enschede Twente</option>
                  </select>
                </div>                

                <div style="margin-bottom:12px">
                  <strong>Cornmeister.nl (Recommended)</strong>
                  <div class="text-muted" style="font-size:12px;margin-top:4px;margin-bottom:8px">
                    Unencrypted non-tls connection uses port 1883
                  </div>
                  <pre class="help-code"><code>set mqtt.server mqtt.cornmeister.nl
set mqtt.port 8883
set mqtt.username observer
set mqtt.password hiermetdiedata
set mqtt.iata <span class="obs-iata-val">AMS</span></code></pre>
                </div>

                <div>
                  <strong>Meshwiki Community (Feeds multiple projects)</strong>
                  <div class="text-muted" style="font-size:12px;margin-top:4px;margin-bottom:8px">
                    Unencrypted non-tls connection uses port 1883
                  </div>
                  <pre class="help-code"><code>set mqtt.server mqtt.mwiki.nl
set mqtt.port 8883
set mqtt.username observer
set mqtt.password 86w7bW9NJxuPcErp2Y5NCQ==
set mqtt.iata <span class="obs-iata-val">AMS</span></code></pre>
                </div>
              </div>

              <hr style="margin:16px 0;border:none;border-top:1px solid var(--border)">

              <div style="font-size:12px" class="text-muted">
                Live packets: <a href="https://cornmeister.nl" target="_blank" rel="noopener">cornmeister.nl</a>
              </div>
            </div>
          </div>
        </div>
		<hr class="section-divider">
        <div id="obsRegionFilter" class="region-filter-container"></div>
        <div id="obsContent"><div class="text-center text-muted" style="padding:40px">Loading…</div></div>
      </div>`;
    RegionFilter.init(document.getElementById('obsRegionFilter'));
    regionChangeHandler = RegionFilter.onChange(function () { render(); });
    loadObservers();
    // Event delegation for data-action buttons
    app.addEventListener('click', function (e) {
      var btn = e.target.closest('[data-action]');
      if (btn && btn.dataset.action === 'obs-refresh') loadObservers();
      if (btn && btn.dataset.action === 'toggle-help') {
        var content = btn.closest('.help-box').querySelector('.help-content');
        var toggle = btn.querySelector('.help-toggle');
        var isCollapsed = toggle.textContent === '▶';
        content.style.maxHeight = isCollapsed ? '2000px' : '0px';
        toggle.textContent = isCollapsed ? '▼' : '▶';
      }
      var row = e.target.closest('tr[data-action="navigate"]');
      if (row) location.hash = row.dataset.value;
    });
    // IATA picker — update both code blocks when a region is selected
    app.addEventListener('change', function (e) {
      if (e.target.id === 'obsIataSelect') {
        var code = e.target.value;
        app.querySelectorAll('.obs-iata-val').forEach(function (span) {
          span.textContent = code;
        });
      }
    });
    // #209 — Keyboard accessibility for observer rows
    app.addEventListener('keydown', function (e) {
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
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
    observers = [];
  }

  async function loadObservers() {
    try {
      const data = await api('/observers', { ttl: CLIENT_TTL.observers });
      observers = data.observers || [];
      render();
    } catch (e) {
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
    if (ago < 600000 + tolerance) return { cls: 'health-green', label: 'Online' };    // < 10 min + tolerance
    if (ago < 3600000 + tolerance) return { cls: 'health-yellow', label: 'Stale' };   // < 1 hour + tolerance
    return { cls: 'health-red', label: 'Offline' };
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

    // Summary counts
    const online = filtered.filter(o => healthStatus(o.last_seen).cls === 'health-green').length;
    const stale = filtered.filter(o => healthStatus(o.last_seen).cls === 'health-yellow').length;
    const offline = filtered.filter(o => healthStatus(o.last_seen).cls === 'health-red').length;

    el.innerHTML = `
      <div class="obs-summary">
        <span class="obs-stat"><span class="health-dot health-green">●</span> ${online} Online</span>
        <span class="obs-stat"><span class="health-dot health-yellow">▲</span> ${stale} Stale</span>
        <span class="obs-stat"><span class="health-dot health-red">✕</span> ${offline} Offline</span>
        <span class="obs-stat">📡 ${filtered.length} Total</span>
      </div>
      <div class="obs-table-scroll"><table class="data-table obs-table" id="obsTable">
        <caption class="sr-only">Observer status and statistics</caption>
        <thead><tr>
          <th scope="col">Status</th><th scope="col">Name</th><th scope="col">Region</th><th scope="col">Last Seen</th>
          <th scope="col">Packets</th><th scope="col">Packets/Hour</th><th scope="col">Uptime</th>
        </tr></thead>
        <tbody>${filtered.map(o => {
          const h = healthStatus(o.last_seen);
          const shape = h.cls === 'health-green' ? '●' : h.cls === 'health-yellow' ? '▲' : '✕';
          return `<tr style="cursor:pointer" tabindex="0" role="row" data-action="navigate" data-value="#/observers/${encodeURIComponent(o.id)}" onclick="location.hash='#/observers/${encodeURIComponent(o.id)}'">
            <td><span class="health-dot ${h.cls}" title="${h.label}">${shape}</span> ${h.label}</td>
            <td class="mono">${o.name || o.id}</td>
            <td>${o.iata ? `<span class="badge-region">${o.iata}</span>` : '—'}</td>
            <td>${timeAgo(o.last_seen)}</td>
            <td>${(o.packet_count || 0).toLocaleString()}</td>
            <td>${sparkBar(o.packetsLastHour || 0, maxPktsHr)}</td>
            <td>${uptimeStr(o.first_seen)}</td>
          </tr>`;
        }).join('')}</tbody>
      </table></div>`;
    makeColumnsResizable('#obsTable', 'meshcore-obs-col-widths');
  }


  registerPage('observers', { init, destroy });
})();
