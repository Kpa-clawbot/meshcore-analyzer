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
        <div class="map-controls" id="mapControls" role="region" aria-label="Map controls">
          <h3>🗺️ Map Controls</h3>
          <fieldset class="mc-section">
            <legend class="mc-label">Node Types</legend>
            <div id="mcRoleChecks"></div>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Display</legend>
            <label><input type="checkbox" id="mcClusters"> Show clusters</label>
            <label><input type="checkbox" id="mcHeatmap"> Heat map</label>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Filters</legend>
            <label><input type="checkbox" id="mcMqtt"> MQTT Connected Only</label>
            <label><input type="checkbox" id="mcNeighbors"> Show direct neighbors</label>
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

    // Init Leaflet
    map = L.map('leaflet-map', { zoomControl: true }).setView([37.5, -122], 6);
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
      attribution: '© OpenStreetMap',
      maxZoom: 19,
    }).addTo(map);

    markerLayer = L.layerGroup().addTo(map);
    routeLayer = L.layerGroup().addTo(map);

    // Fix map size on SPA load
    setTimeout(() => map.invalidateSize(), 100);

    // Bind controls
    document.getElementById('mcClusters').addEventListener('change', e => { filters.clusters = e.target.checked; renderMarkers(); });
    document.getElementById('mcHeatmap').addEventListener('change', e => { toggleHeatmap(e.target.checked); });
    document.getElementById('mcMqtt').addEventListener('change', e => { filters.mqttOnly = e.target.checked; renderMarkers(); });
    document.getElementById('mcNeighbors').addEventListener('change', e => { filters.neighbors = e.target.checked; renderMarkers(); });
    document.getElementById('mcLastHeard').addEventListener('change', e => { filters.lastHeard = e.target.value; loadNodes(); });

    // WS for live advert updates
    wsHandler = msg => {
      if (msg.type === 'packet' && msg.data?.decoded?.header?.payloadTypeName === 'ADVERT') {
        loadNodes();
      }
    };
    onWS(wsHandler);

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
    // Resolve hop short hashes to node positions via prefix match
    const positions = [];
    for (const hop of hopKeys) {
      const hopLower = hop.toLowerCase();
      const node = nodes.find(n =>
        n.public_key.toLowerCase().startsWith(hopLower)
      );
      if (node && node.lat != null && node.lon != null && !(node.lat === 0 && node.lon === 0)) {
        positions.push({ lat: node.lat, lon: node.lon, name: node.name || hop });
      }
    }
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
      L.circleMarker([p.lat, p.lon], {
        radius: 10, fillColor: i === 0 ? '#22c55e' : i === positions.length - 1 ? '#ef4444' : '#f59e0b',
        fillOpacity: 0.9, color: '#fff', weight: 2
      }).addTo(routeLayer).bindTooltip(`${i + 1}. ${p.name}`, { permanent: true, direction: 'top', className: 'route-tooltip' });
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
      fitBounds();
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
      const lbl = document.createElement('label');
      lbl.innerHTML = `<input type="checkbox" data-role="${role}" ${filters[role] ? 'checked' : ''}> <span style="color:${ROLE_COLORS[role]};font-weight:600;" aria-hidden="true">●</span> ${ROLE_LABELS[role]} <span style="color:var(--text-muted)">(${count})</span>`;
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
    // Find nodes observed in this region — use all nodes with location and fit bounds
    // For now, just find the centroid of nodes that have location
    const nodesWithLoc = nodes.filter(n => n.lat && n.lon);
    if (nodesWithLoc.length === 0) return;
    const bounds = L.latLngBounds(nodesWithLoc.map(n => [n.lat, n.lon]));
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
      <div style="font-family:var(--font);min-width:180px;">
        <div style="font-weight:700;font-size:14px;margin-bottom:4px;">${node.name || 'Unknown'}</div>
        ${roleBadge}
        <table style="margin-top:8px;font-size:12px;border-collapse:collapse;width:100%;">
          <tr><td style="color:var(--text-muted);padding:2px 8px 2px 0;">Key</td><td style="font-family:var(--mono);font-size:11px;">${key}</td></tr>
          <tr><td style="color:var(--text-muted);padding:2px 8px 2px 0;">Location</td><td>${loc}</td></tr>
          <tr><td style="color:var(--text-muted);padding:2px 8px 2px 0;">Last Advert</td><td>${lastAdvert}</td></tr>
          <tr><td style="color:var(--text-muted);padding:2px 8px 2px 0;">Adverts</td><td>${node.advert_count || 0}</td></tr>
        </table>
        <div style="margin-top:8px;"><a href="#/nodes/${node.public_key}" style="color:var(--accent);font-size:12px;">View Node →</a></div>
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
