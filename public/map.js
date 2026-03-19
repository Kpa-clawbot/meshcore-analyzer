/* === MeshCore Analyzer — map.js === */
'use strict';

(function () {
  let map = null;
  let routeLayer = null;
  let markerLayer = null;
  let clusterGroup = null;
  let nodes = [];
  let observers = [];
  let filters = { repeater: true, companion: true, room: true, sensor: true, lastHeard: '30d', mqttOnly: false, neighbors: false, clusters: false };
  let wsHandler = null;
  let heatLayer = null;
  let userHasMoved = false;
  let controlsCollapsed = false;

  // Safe escape — falls back to identity if app.js hasn't loaded yet
  const safeEsc = (typeof esc === 'function') ? esc : function (s) { return s; };

  // Role → marker style (WCAG AA compliant: all ≥4.5:1 on both light/dark backgrounds)
  const ROLE_STYLE = {
    repeater:  { color: '#1d4ed8', fill: true,  radius: 8, weight: 2 },
    companion: { color: '#0369a1', fill: false, radius: 7, weight: 2 },
    room:      { color: '#6d28d9', fill: true,  radius: 7, weight: 2 },
    sensor:    { color: '#92400e', fill: true,  radius: 4, weight: 1 },
  };

  const ROLE_LABELS = { repeater: 'Repeaters', companion: 'Companions', room: 'Room Servers', sensor: 'Sensors' };
  const ROLE_COLORS = { repeater: '#1d4ed8', companion: '#0369a1', room: '#6d28d9', sensor: '#92400e' };

  function init(container) {
    container.innerHTML = `
      <div id="map-wrap" style="position:relative;width:100%;height:100%;">
        <div id="leaflet-map" style="width:100%;height:100%;"></div>
        <button class="map-controls-toggle" id="mapControlsToggle" aria-label="Toggle map controls" aria-expanded="true">⚙️</button>
        <div class="map-controls" id="mapControls" role="region" aria-label="Map controls">
          <h3>🗺️ Map Controls</h3>
          <fieldset class="mc-section">
            <legend class="mc-label">Node Types</legend>
            <div id="mcRoleChecks"></div>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Display</legend>
            <label for="mcClusters"><input type="checkbox" id="mcClusters"> Show clusters</label>
            <label for="mcHeatmap"><input type="checkbox" id="mcHeatmap"> Heat map</label>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Filters</legend>
            <label for="mcMqtt"><input type="checkbox" id="mcMqtt"> MQTT Connected Only</label>
            <label for="mcNeighbors"><input type="checkbox" id="mcNeighbors"> Show direct neighbors</label>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Last Heard</legend>
            <label for="mcLastHeard" class="sr-only">Filter by last heard time</label>
            <select id="mcLastHeard" aria-label="Filter by last heard time">
              <option value="1h">1 hour</option>
              <option value="6h">6 hours</option>
              <option value="24h">24 hours</option>
              <option value="7d">7 days</option>
              <option value="30d" selected>30 days</option>
            </select>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Quick Jump</legend>
            <div class="mc-jumps" id="mcJumps" role="group" aria-label="Jump to region"></div>
          </fieldset>
        </div>
      </div>`;

    // Init Leaflet — restore saved position or default to Bay Area
    const defaultCenter = [37.6, -122.1];
    const defaultZoom = 9;
    let initCenter = defaultCenter;
    let initZoom = defaultZoom;
    const savedView = localStorage.getItem('map-view');
    if (savedView) {
      try { const v = JSON.parse(savedView); initCenter = [v.lat, v.lng]; initZoom = v.zoom; } catch {}
    }
    map = L.map('leaflet-map', { zoomControl: true }).setView(initCenter, initZoom);
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
      attribution: '© OpenStreetMap',
      maxZoom: 19,
    }).addTo(map);

    // Save position on move
    map.on('moveend', () => {
      const c = map.getCenter();
      localStorage.setItem('map-view', JSON.stringify({ lat: c.lat, lng: c.lng, zoom: map.getZoom() }));
      userHasMoved = true;
    });

    markerLayer = L.layerGroup().addTo(map);
    routeLayer = L.layerGroup().addTo(map);

    // Fix map size on SPA load
    setTimeout(() => map.invalidateSize(), 100);

    // Controls toggle
    const toggleBtn = document.getElementById('mapControlsToggle');
    const controlsPanel = document.getElementById('mapControls');
    // Default collapsed on mobile
    if (window.innerWidth <= 640) {
      controlsCollapsed = true;
      controlsPanel.classList.add('collapsed');
      toggleBtn.setAttribute('aria-expanded', 'false');
    }
    toggleBtn.addEventListener('click', () => {
      controlsCollapsed = !controlsCollapsed;
      controlsPanel.classList.toggle('collapsed', controlsCollapsed);
      toggleBtn.setAttribute('aria-expanded', String(!controlsCollapsed));
    });

    // Bind controls
    document.getElementById('mcClusters').addEventListener('change', e => { filters.clusters = e.target.checked; renderMarkers(); });
    document.getElementById('mcHeatmap').addEventListener('change', e => { toggleHeatmap(e.target.checked); });
    document.getElementById('mcMqtt').addEventListener('change', e => { filters.mqttOnly = e.target.checked; renderMarkers(); });
    document.getElementById('mcNeighbors').addEventListener('change', e => { filters.neighbors = e.target.checked; renderMarkers(); });
    document.getElementById('mcLastHeard').addEventListener('change', e => { filters.lastHeard = e.target.value; loadNodes(); });

    // WS for live advert updates
    wsHandler = debouncedOnWS(function (msgs) {
      if (msgs.some(function (m) { return m.type === 'packet' && m.data?.decoded?.header?.payloadTypeName === 'ADVERT'; })) {
        loadNodes();
      }
    });

    loadNodes().then(() => {
      // Check for route from packet detail (via sessionStorage)
      const routeHopsJson = sessionStorage.getItem('map-route-hops');
      if (routeHopsJson) {
        sessionStorage.removeItem('map-route-hops');
        try {
          const hopKeys = JSON.parse(routeHopsJson);
          drawPacketRoute(hopKeys);
        } catch {}
      }
    });
  }

  function drawPacketRoute(hopKeys) {
    // Resolve hop short hashes to node positions with geographic disambiguation
    const raw = hopKeys.map(hop => {
      const hopLower = hop.toLowerCase();
      const candidates = nodes.filter(n => {
        const pk = n.public_key.toLowerCase();
        return (pk === hopLower || pk.startsWith(hopLower) || hopLower.startsWith(pk)) &&
          n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0);
      });
      if (candidates.length === 1) {
        const c = candidates[0];
        return { lat: c.lat, lon: c.lon, name: c.name || hop.slice(0,8), pubkey: c.public_key, role: c.role, resolved: true };
      } else if (candidates.length > 1) {
        return { name: hop.slice(0,8), resolved: false, candidates };
      }
      return null;
    });

    // Disambiguate: pick candidate closest to center of already-resolved hops
    const knownPos = raw.filter(h => h && h.resolved);
    if (knownPos.length > 0) {
      const cLat = knownPos.reduce((s, p) => s + p.lat, 0) / knownPos.length;
      const cLon = knownPos.reduce((s, p) => s + p.lon, 0) / knownPos.length;
      const dist = (lat, lon) => Math.sqrt((lat - cLat) ** 2 + (lon - cLon) ** 2);
      for (const hop of raw) {
        if (hop && !hop.resolved && hop.candidates) {
          hop.candidates.sort((a, b) => dist(a.lat, a.lon) - dist(b.lat, b.lon));
          const best = hop.candidates[0];
          hop.lat = best.lat; hop.lon = best.lon;
          hop.name = best.name || hop.name;
          hop.pubkey = best.public_key; hop.role = best.role;
          hop.resolved = true;
        }
      }
    }

    const positions = raw.filter(h => h && h.resolved);
    if (positions.length < 1) return;

    // Even a single node is worth showing (zoom to it)
    const coords = positions.map(p => [p.lat, p.lon]);

    if (positions.length >= 2) {
      // Draw route polyline
      L.polyline(coords, {
        color: '#f59e0b', weight: 3, opacity: 0.8, dashArray: '8 4'
      }).addTo(routeLayer);
    }

    // Add numbered markers at each hop
    positions.forEach((p, i) => {
      const color = i === 0 ? '#22c55e' : i === positions.length - 1 ? '#ef4444' : '#f59e0b';
      const label = i === 0 ? 'Origin' : i === positions.length - 1 ? 'Destination' : `Hop ${i}`;
      const marker = L.circleMarker([p.lat, p.lon], {
        radius: 10, fillColor: color,
        fillOpacity: 0.9, color: '#fff', weight: 2
      }).addTo(routeLayer);
      
      marker.bindTooltip(`${i + 1}. ${p.name}`, { permanent: true, direction: 'top', className: 'route-tooltip' });
      
      const popupHtml = `<div style="font-size:12px;min-width:160px">
        <div style="font-weight:700;margin-bottom:4px">${label}: ${safeEsc(p.name)}</div>
        <div style="color:#9ca3af;font-size:11px;margin-bottom:4px">${p.role || 'unknown'}</div>
        <div style="font-family:monospace;font-size:10px;color:#6b7280;margin-bottom:6px;word-break:break-all">${safeEsc(p.pubkey || '')}</div>
        <div style="font-size:11px;color:#9ca3af">${p.lat.toFixed(4)}, ${p.lon.toFixed(4)}</div>
        ${p.pubkey ? `<div style="margin-top:6px"><a href="#/nodes/${p.pubkey}" style="color:var(--accent);font-size:11px">View Node →</a></div>` : ''}
      </div>`;
      marker.bindPopup(popupHtml, { className: 'route-popup' });
    });

    // Fit map to route
    if (coords.length >= 2) {
      map.fitBounds(L.latLngBounds(coords).pad(0.3));
    } else {
      map.setView(coords[0], 13);
    }
  }

  async function loadNodes() {
    try {
      const data = await api(`/nodes?limit=10000&lastHeard=${filters.lastHeard}`);
      nodes = data.nodes || [];
      buildRoleChecks(data.counts || {});

      // Load observers for jump buttons
      const obsData = await api('/observers');
      observers = obsData.observers || [];
      buildJumpButtons();

      renderMarkers();
      // Don't fitBounds on initial load — respect the Bay Area default or saved view
      // Only fitBounds on subsequent data refreshes if user hasn't manually panned
    } catch (e) {
      console.error('Map load error:', e);
    }
  }

  function buildRoleChecks(counts) {
    const el = document.getElementById('mcRoleChecks');
    if (!el) return;
    el.innerHTML = '';
    for (const role of ['repeater', 'companion', 'room', 'sensor']) {
      const count = counts[role + 's'] || 0;
      const cbId = 'mcRole_' + role;
      const lbl = document.createElement('label');
      lbl.setAttribute('for', cbId);
      lbl.innerHTML = `<input type="checkbox" id="${cbId}" data-role="${role}" ${filters[role] ? 'checked' : ''}> <span style="color:${ROLE_COLORS[role]};font-weight:600;" aria-hidden="true">●</span> ${ROLE_LABELS[role]} <span style="color:var(--text-muted)">(${count})</span>`;
      lbl.querySelector('input').addEventListener('change', e => {
        filters[e.target.dataset.role] = e.target.checked;
        renderMarkers();
      });
      el.appendChild(lbl);
    }
  }

  const REGION_NAMES = { SJC: 'San Jose', SFO: 'San Francisco', OAK: 'Oakland', MTV: 'Mountain View', SCZ: 'Santa Cruz', MRY: 'Monterey', PAO: 'Palo Alto' };

  function buildJumpButtons() {
    const el = document.getElementById('mcJumps');
    if (!el) return;
    // Collect unique regions from observers
    const regions = new Set();
    observers.forEach(o => { if (o.iata) regions.add(o.iata); });

    // Also extract regions from node locations if we have them
    el.innerHTML = '';
    if (regions.size === 0) {
      el.innerHTML = '<span style="color:var(--text-muted);font-size:12px;">No regions yet</span>';
      return;
    }
    for (const r of [...regions].sort()) {
      const btn = document.createElement('button');
      btn.className = 'mc-jump-btn';
      btn.textContent = r;
      btn.setAttribute('aria-label', `Jump to ${REGION_NAMES[r] || r}`);
      btn.addEventListener('click', () => jumpToRegion(r));
      el.appendChild(btn);
    }
  }

  function jumpToRegion(iata) {
    // Find observers in this region, then find nodes seen by those observers
    const regionObserverIds = new Set(observers.filter(o => o.iata === iata).map(o => o.id || o.observer_id));
    // Filter nodes that have location; prefer nodes associated with region observers
    let regionNodes = nodes.filter(n => n.lat && n.lon && n.observer_id && regionObserverIds.has(n.observer_id));
    // Fallback: if observers don't link to nodes, use observers' own locations
    if (regionNodes.length === 0) {
      const obsWithLoc = observers.filter(o => o.iata === iata && o.lat && o.lon);
      if (obsWithLoc.length > 0) {
        const bounds = L.latLngBounds(obsWithLoc.map(o => [o.lat, o.lon]));
        map.fitBounds(bounds.pad(0.5), { padding: [40, 40], maxZoom: 12 });
        return;
      }
      // Final fallback: fit all nodes
      regionNodes = nodes.filter(n => n.lat && n.lon);
    }
    if (regionNodes.length === 0) return;
    const bounds = L.latLngBounds(regionNodes.map(n => [n.lat, n.lon]));
    map.fitBounds(bounds, { padding: [40, 40], maxZoom: 12 });
  }

  function renderMarkers() {
    markerLayer.clearLayers();

    const filtered = nodes.filter(n => {
      if (!n.lat || !n.lon) return false;
      if (!filters[n.role || 'companion']) return false;
      return true;
    });

    for (const node of filtered) {
      const style = ROLE_STYLE[node.role] || ROLE_STYLE.companion;
      const marker = L.circleMarker([node.lat, node.lon], {
        radius: style.radius,
        color: style.color,
        fillColor: style.color,
        fillOpacity: style.fill ? 0.8 : 0,
        weight: style.weight,
        alt: `${node.name || 'Unknown'} (${node.role || 'node'})`,
      });

      marker.bindPopup(buildPopup(node), { maxWidth: 280 });
      markerLayer.addLayer(marker);
    }
  }

  function buildPopup(node) {
    const key = node.public_key ? truncate(node.public_key, 16) : '—';
    const loc = (node.lat && node.lon) ? `${node.lat.toFixed(5)}, ${node.lon.toFixed(5)}` : '—';
    const lastAdvert = node.last_seen ? timeAgo(node.last_seen) : '—';
    const roleBadge = `<span style="display:inline-block;padding:2px 8px;border-radius:12px;font-size:11px;font-weight:600;background:${ROLE_COLORS[node.role] || '#4b5563'};color:#fff;">${(node.role || 'unknown').toUpperCase()}</span>`;

    return `
      <div class="map-popup" style="font-family:var(--font);min-width:180px;">
        <h3 style="font-weight:700;font-size:14px;margin:0 0 4px;">${safeEsc(node.name || 'Unknown')}</h3>
        ${roleBadge}
        <dl style="margin-top:8px;font-size:12px;">
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Key</dt>
          <dd style="font-family:var(--mono);font-size:11px;margin-left:88px;padding:2px 0;">${safeEsc(key)}</dd>
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Location</dt>
          <dd style="margin-left:88px;padding:2px 0;">${loc}</dd>
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Last Advert</dt>
          <dd style="margin-left:88px;padding:2px 0;">${lastAdvert}</dd>
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Adverts</dt>
          <dd style="margin-left:88px;padding:2px 0;">${node.advert_count || 0}</dd>
        </dl>
        <div style="margin-top:8px;clear:both;"><a href="#/nodes/${node.public_key}" style="color:var(--accent);font-size:12px;">View Node →</a></div>
      </div>`;
  }

  function fitBounds() {
    const nodesWithLoc = nodes.filter(n => n.lat && n.lon && filters[n.role || 'companion']);
    if (nodesWithLoc.length === 0) return;
    if (nodesWithLoc.length === 1) {
      map.setView([nodesWithLoc[0].lat, nodesWithLoc[0].lon], 10);
      return;
    }
    const bounds = L.latLngBounds(nodesWithLoc.map(n => [n.lat, n.lon]));
    map.fitBounds(bounds, { padding: [50, 50], maxZoom: 14 });
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (map) {
      map.remove();
      map = null;
    }
    markerLayer = null;
    routeLayer = null;
    if (heatLayer) { heatLayer = null; }
  }

  function toggleHeatmap(on) {
    if (heatLayer) { map.removeLayer(heatLayer); heatLayer = null; }
    if (!on || !map) return;
    const points = nodes
      .filter(n => n.lat != null && n.lon != null)
      .map(n => {
        const weight = n.advert_count || 1;
        return [n.lat, n.lon, weight];
      });
    if (points.length && typeof L.heatLayer === 'function') {
      heatLayer = L.heatLayer(points, {
        radius: 25, blur: 15, maxZoom: 14, minOpacity: 0.25,
        gradient: { 0.2: '#0d47a1', 0.4: '#1565c0', 0.6: '#42a5f5', 0.8: '#ffca28', 1.0: '#ff5722' }
      }).addTo(map);
    }
  }

  registerPage('map', { init, destroy });
})();
