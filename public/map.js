/* === MeshCore Analyzer — map.js === */
'use strict';

(function () {
  let map = null;
  let routeLayer = null;
  let markerLayer = null;
  let clusterGroup = null;
  let nodes = [];
  let observers = [];
  let filters = { repeater: true, companion: true, room: true, sensor: true, observer: true, lastHeard: '30d', neighbors: false, clusters: false, hashLabels: localStorage.getItem('meshcore-map-hash-labels') !== 'false' };
  let wsHandler = null;
  let heatLayer = null;
  let userHasMoved = false;
  let controlsCollapsed = false;

  // Safe escape — falls back to identity if app.js hasn't loaded yet
  const safeEsc = (typeof esc === 'function') ? esc : function (s) { return s; };

  // Roles loaded from shared roles.js (ROLE_STYLE, ROLE_LABELS, ROLE_COLORS globals)

  function makeMarkerIcon(role) {
    const s = ROLE_STYLE[role] || ROLE_STYLE.companion;
    const size = s.radius * 2 + 4;
    const c = size / 2;
    let path;
    switch (s.shape) {
      case 'diamond':
        path = `<polygon points="${c},2 ${size-2},${c} ${c},${size-2} 2,${c}" fill="${s.color}" stroke="#fff" stroke-width="2"/>`;
        break;
      case 'square':
        path = `<rect x="3" y="3" width="${size-6}" height="${size-6}" fill="${s.color}" stroke="#fff" stroke-width="2"/>`;
        break;
      case 'triangle':
        path = `<polygon points="${c},2 ${size-2},${size-2} 2,${size-2}" fill="${s.color}" stroke="#fff" stroke-width="2"/>`;
        break;
      case 'star': {
        // 5-pointed star
        const cx = c, cy = c, outer = c - 1, inner = outer * 0.4;
        let pts = '';
        for (let i = 0; i < 5; i++) {
          const aOuter = (i * 72 - 90) * Math.PI / 180;
          const aInner = ((i * 72) + 36 - 90) * Math.PI / 180;
          pts += `${cx + outer * Math.cos(aOuter)},${cy + outer * Math.sin(aOuter)} `;
          pts += `${cx + inner * Math.cos(aInner)},${cy + inner * Math.sin(aInner)} `;
        }
        path = `<polygon points="${pts.trim()}" fill="${s.color}" stroke="#fff" stroke-width="1.5"/>`;
        break;
      }
      default: // circle
        path = `<circle cx="${c}" cy="${c}" r="${c-2}" fill="${s.color}" stroke="#fff" stroke-width="2"/>`;
    }
    const svg = `<svg width="${size}" height="${size}" viewBox="0 0 ${size} ${size}" xmlns="http://www.w3.org/2000/svg">${path}</svg>`;
    return L.divIcon({
      html: svg,
      className: 'meshcore-marker',
      iconSize: [size, size],
      iconAnchor: [c, c],
      popupAnchor: [0, -c],
    });
  }

  function makeRepeaterLabelIcon(node) {
    var s = ROLE_STYLE['repeater'] || ROLE_STYLE.companion;
    var hs = node.hash_size || 1;
    // Show the short mesh hash ID (first N bytes of pubkey, uppercased)
    var shortHash = node.public_key ? node.public_key.slice(0, hs * 2).toUpperCase() : '??';
    var bgColor = node.hash_size ? s.color : '#888';
    var html = '<div style="background:' + bgColor + ';color:#fff;font-weight:bold;font-size:11px;padding:2px 5px;border-radius:3px;border:2px solid #fff;box-shadow:0 1px 3px rgba(0,0,0,0.4);text-align:center;line-height:1.2;white-space:nowrap;">' +
      shortHash + '</div>';
    return L.divIcon({
      html: html,
      className: 'meshcore-marker meshcore-label-marker',
      iconSize: null,
      iconAnchor: [14, 12],
      popupAnchor: [0, -12],
    });
  }

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
            <label for="mcHashLabels"><input type="checkbox" id="mcHashLabels"> Hash prefix labels</label>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Filters</legend>
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

    const isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
    const tileLayer = L.tileLayer(isDark ? TILE_DARK : TILE_LIGHT, {
      attribution: '© OpenStreetMap © CartoDB',
      maxZoom: 19,
    }).addTo(map);
    const _mapThemeObs = new MutationObserver(function () {
      const dark = document.documentElement.getAttribute('data-theme') === 'dark' ||
        (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
      tileLayer.setUrl(dark ? TILE_DARK : TILE_LIGHT);
    });
    _mapThemeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });

    // Save position on move
    map.on('moveend', () => {
      const c = map.getCenter();
      localStorage.setItem('map-view', JSON.stringify({ lat: c.lat, lng: c.lng, zoom: map.getZoom() }));
      userHasMoved = true;
    });

    map.on('zoomend', () => {
      if (filters.hashLabels && !_renderingMarkers) renderMarkers();
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
    document.getElementById('mcNeighbors').addEventListener('change', e => { filters.neighbors = e.target.checked; renderMarkers(); });

    // Hash Labels toggle
    const hashLabelEl = document.getElementById('mcHashLabels');
    if (hashLabelEl) {
      hashLabelEl.checked = filters.hashLabels;
      hashLabelEl.addEventListener('change', e => { filters.hashLabels = e.target.checked; localStorage.setItem('meshcore-map-hash-labels', filters.hashLabels); renderMarkers(); });
    }
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
          const parsed = JSON.parse(routeHopsJson);
          // Support new format {origin, hops} and legacy plain array
          if (Array.isArray(parsed)) {
            drawPacketRoute(parsed, null);
          } else {
            drawPacketRoute(parsed.hops || [], parsed.origin || null);
          }
        } catch {}
      }
    });
  }

  function drawPacketRoute(hopKeys, origin) {
    // Hide default markers so only the route is visible
    if (markerLayer) map.removeLayer(markerLayer);
    if (clusterGroup) map.removeLayer(clusterGroup);
    if (heatLayer) map.removeLayer(heatLayer);

    routeLayer.clearLayers();

    // Add close route button
    const closeBtn = L.control({ position: 'topright' });
    closeBtn.onAdd = function () {
      const div = L.DomUtil.create('div', 'leaflet-bar');
      div.innerHTML = '<a href="#" title="Close route" style="font-size:18px;font-weight:bold;text-decoration:none;display:block;width:36px;height:36px;line-height:36px;text-align:center;background:var(--bg-secondary,#1e293b);color:var(--text-primary,#e2e8f0);border-radius:4px">✕</a>';
      L.DomEvent.on(div, 'click', function (e) {
        L.DomEvent.preventDefault(e);
        routeLayer.clearLayers();
        if (markerLayer) map.addLayer(markerLayer);
        if (clusterGroup) map.addLayer(clusterGroup);
        map.removeControl(closeBtn);
      });
      return div;
    };
    closeBtn.addTo(map);

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

    // Resolve and prepend origin node
    if (origin) {
      let originPos = null;
      if (origin.lat != null && origin.lon != null) {
        originPos = { lat: origin.lat, lon: origin.lon, name: origin.name || 'Sender', pubkey: origin.pubkey, isOrigin: true };
      } else if (origin.pubkey) {
        const pk = origin.pubkey.toLowerCase();
        const match = nodes.find(n => n.public_key.toLowerCase() === pk || n.public_key.toLowerCase().startsWith(pk));
        if (match && match.lat != null && match.lon != null) {
          originPos = { lat: match.lat, lon: match.lon, name: origin.name || match.name || 'Sender', pubkey: match.public_key, role: match.role, isOrigin: true };
        }
      }
      if (originPos) positions.unshift(originPos);
    }

    if (positions.length < 1) return;

    const coords = positions.map(p => [p.lat, p.lon]);

    if (positions.length >= 2) {
      L.polyline(coords, {
        color: '#f59e0b', weight: 3, opacity: 0.8, dashArray: '8 4'
      }).addTo(routeLayer);
    }

    // Add numbered markers at each hop
    positions.forEach((p, i) => {
      const isOrigin = i === 0 && p.isOrigin;
      const isLast = i === positions.length - 1 && positions.length > 1;
      const color = isOrigin ? '#06b6d4' : isLast ? '#ef4444' : i === 0 ? '#22c55e' : '#f59e0b';
      const radius = isOrigin ? 14 : 10;
      const label = isOrigin ? 'Sender' : isLast ? 'Last Hop' : `Hop ${isOrigin ? i : i}`;

      if (isOrigin) {
        // Double-ring origin marker
        L.circleMarker([p.lat, p.lon], {
          radius: radius + 4, fillColor: 'transparent', fillOpacity: 0, color: '#06b6d4', weight: 2, opacity: 0.6
        }).addTo(routeLayer);
      }

      const marker = L.circleMarker([p.lat, p.lon], {
        radius: radius, fillColor: color,
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
      // Load regions from config + observed IATAs
      try { REGION_NAMES = await api('/config/regions', { ttl: 3600 }); } catch {}

      const data = await api(`/nodes?limit=10000&lastHeard=${filters.lastHeard}`, { ttl: CLIENT_TTL.nodeList });
      nodes = data.nodes || [];

      // Load observers for jump buttons + map markers
      const obsData = await api('/observers', { ttl: CLIENT_TTL.observers });
      observers = obsData.observers || [];

      buildRoleChecks(data.counts || {});
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
    const obsCount = observers.filter(o => o.lat && o.lon).length;
    const roles = ['repeater', 'companion', 'room', 'sensor', 'observer'];
    const shapeMap = { repeater: '◆', companion: '●', room: '■', sensor: '▲', observer: '★' };
    for (const role of roles) {
      const count = role === 'observer' ? obsCount : (counts[role + 's'] || 0);
      const cbId = 'mcRole_' + role;
      const lbl = document.createElement('label');
      lbl.setAttribute('for', cbId);
      const shape = shapeMap[role] || '●';
      lbl.innerHTML = `<input type="checkbox" id="${cbId}" data-role="${role}" ${filters[role] ? 'checked' : ''}> <span style="color:${ROLE_COLORS[role]};font-weight:600;" aria-hidden="true">${shape}</span> ${ROLE_LABELS[role]} <span style="color:var(--text-muted)">(${count})</span>`;
      lbl.querySelector('input').addEventListener('change', e => {
        filters[e.target.dataset.role] = e.target.checked;
        renderMarkers();
      });
      el.appendChild(lbl);
    }
  }

  let REGION_NAMES = {};

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

  var _renderingMarkers = false;
  var _lastDeconflictZoom = null;

  function deconflictLabels(markers, mapRef) {
    const placed = [];
    const PAD = 4;

    var overlaps = function(b) {
      for (var k = 0; k < placed.length; k++) {
        var p = placed[k];
        if (b.x < p.x + p.w + PAD && b.x + b.w + PAD > p.x &&
            b.y < p.y + p.h + PAD && b.y + b.h + PAD > p.y) return true;
      }
      return false;
    };

    // Spiral offsets — 6 rings, 8 directions, up to ~132px
    var offsets = [];
    for (var ring = 1; ring <= 6; ring++) {
      var dist = ring * 22;
      for (var angle = 0; angle < 360; angle += 45) {
        var rad = angle * Math.PI / 180;
        offsets.push([Math.round(Math.cos(rad) * dist), Math.round(Math.sin(rad) * dist)]);
      }
    }

    for (var i = 0; i < markers.length; i++) {
      var m = markers[i];
      var w = m.isLabel ? 38 : 20;
      var h = m.isLabel ? 24 : 20;
      var pt = mapRef.latLngToLayerPoint(m.latLng);
      var bestPt = pt;
      var box = { x: pt.x - w / 2, y: pt.y - h / 2, w: w, h: h };

      if (overlaps(box)) {
        for (var j = 0; j < offsets.length; j++) {
          var tryPt = L.point(pt.x + offsets[j][0], pt.y + offsets[j][1]);
          var tryBox = { x: tryPt.x - w / 2, y: tryPt.y - h / 2, w: w, h: h };
          if (!overlaps(tryBox)) {
            bestPt = tryPt;
            box = tryBox;
            break;
          }
        }
      }

      placed.push(box);
      m.adjustedLatLng = mapRef.layerPointToLatLng(bestPt);
      m.offset = Math.sqrt(Math.pow(bestPt.x - pt.x, 2) + Math.pow(bestPt.y - pt.y, 2));
    }
  }

  function renderMarkers() {
    if (_renderingMarkers) return;
    _renderingMarkers = true;
    try { _renderMarkersInner(); } finally { _renderingMarkers = false; }
  }

  function _renderMarkersInner() {
    markerLayer.clearLayers();

    const filtered = nodes.filter(n => {
      if (!n.lat || !n.lon) return false;
      if (!filters[n.role || 'companion']) return false;
      return true;
    });

    const allMarkers = [];

    for (const node of filtered) {
      const useLabel = node.role === 'repeater' && filters.hashLabels;
      const icon = useLabel ? makeRepeaterLabelIcon(node) : makeMarkerIcon(node.role || 'companion');
      const latLng = L.latLng(node.lat, node.lon);
      allMarkers.push({ latLng, node, icon, isLabel: useLabel, popupFn: function() { return buildPopup(node); }, alt: (node.name || 'Unknown') + ' (' + (node.role || 'node') + ')' });
    }

    // Add observer markers
    if (filters.observer) {
      for (const obs of observers) {
        if (!obs.lat || !obs.lon) continue;
        const icon = makeMarkerIcon('observer');
        const latLng = L.latLng(obs.lat, obs.lon);
        allMarkers.push({ latLng, node: obs, icon, isLabel: false, popupFn: function() { return buildObserverPopup(obs); }, alt: (obs.name || obs.id || 'Unknown') + ' (observer)' });
      }
    }

    // Deconflict ALL markers
    if (allMarkers.length > 0) {
      deconflictLabels(allMarkers, map);
    }

    for (const m of allMarkers) {
      const pos = m.adjustedLatLng || m.latLng;
      const marker = L.marker(pos, { icon: m.icon, alt: m.alt });
      marker.bindPopup(m.popupFn(), { maxWidth: 280 });
      markerLayer.addLayer(marker);

      if (m.offset > 10) {
        const line = L.polyline([m.latLng, pos], {
          color: '#ef4444', weight: 2, dashArray: '6,4', opacity: 0.85
        });
        markerLayer.addLayer(line);
        // Small dot at true GPS position
        const dot = L.circleMarker(m.latLng, {
          radius: 3, fillColor: '#ef4444', fillOpacity: 0.9, stroke: true, color: '#fff', weight: 1
        });
        markerLayer.addLayer(dot);
      }
    }
  }

  function buildObserverPopup(obs) {
    const name = safeEsc(obs.name || obs.id || 'Unknown');
    const iata = obs.iata ? `<span class="badge-region">${safeEsc(obs.iata)}</span>` : '';
    const lastSeen = obs.last_seen ? timeAgo(obs.last_seen) : '—';
    const packets = (obs.packet_count || 0).toLocaleString();
    const loc = `${obs.lat.toFixed(5)}, ${obs.lon.toFixed(5)}`;
    const roleBadge = `<span style="display:inline-block;padding:2px 8px;border-radius:12px;font-size:11px;font-weight:600;background:${ROLE_COLORS.observer};color:#fff;">OBSERVER</span>`;

    return `
      <div class="map-popup" style="font-family:var(--font);min-width:180px;">
        <h3 style="font-weight:700;font-size:14px;margin:0 0 4px;">${name}</h3>
        ${roleBadge} ${iata}
        <dl style="margin-top:8px;font-size:12px;">
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Location</dt>
          <dd style="margin-left:88px;padding:2px 0;">${loc}</dd>
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Last Seen</dt>
          <dd style="margin-left:88px;padding:2px 0;">${lastSeen}</dd>
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Packets</dt>
          <dd style="margin-left:88px;padding:2px 0;">${packets}</dd>
        </dl>
        <a href="#/observers/${encodeURIComponent(obs.id || obs.observer_id)}" style="display:block;margin-top:8px;font-size:12px;color:var(--accent);">View Detail →</a>
      </div>`;
  }

  function buildPopup(node) {
    const key = node.public_key ? truncate(node.public_key, 16) : '—';
    const loc = (node.lat && node.lon) ? `${node.lat.toFixed(5)}, ${node.lon.toFixed(5)}` : '—';
    const lastAdvert = node.last_seen ? timeAgo(node.last_seen) : '—';
    const roleBadge = `<span style="display:inline-block;padding:2px 8px;border-radius:12px;font-size:11px;font-weight:600;background:${ROLE_COLORS[node.role] || '#4b5563'};color:#fff;">${(node.role || 'unknown').toUpperCase()}</span>`;
    const hs = node.hash_size || 1;
    const hashPrefix = node.public_key ? node.public_key.slice(0, hs * 2).toUpperCase() : '—';
    const hashPrefixRow = `<dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Hash Prefix</dt>
          <dd style="font-family:var(--mono);font-size:11px;font-weight:700;margin-left:88px;padding:2px 0;">${safeEsc(hashPrefix)} <span style="font-weight:400;color:var(--text-muted);">(${hs}B)</span></dd>`;

    return `
      <div class="map-popup" style="font-family:var(--font);min-width:180px;">
        <h3 style="font-weight:700;font-size:14px;margin:0 0 4px;">${safeEsc(node.name || 'Unknown')}</h3>
        ${roleBadge}
        <dl style="margin-top:8px;font-size:12px;">
          ${hashPrefixRow}
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
