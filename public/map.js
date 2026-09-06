/* === CoreScope — map.js === */
'use strict';

(function () {
  let map = null;
  let routeLayer = null;
  let markerLayer = null;
  let clusterGroup = null;
  let nodes = [];
  let targetNodeKey = null;
  let observers = [];
  let filters = { repeater: true, companion: true, room: true, sensor: true, observer: true, lastHeard: '30d', neighbors: false, clustering: localStorage.getItem('meshcore-map-clustering') !== 'false', hashLabels: localStorage.getItem('meshcore-map-hash-labels') !== 'false', statusFilter: localStorage.getItem('meshcore-map-status-filter') || 'all', byteSize: localStorage.getItem('meshcore-map-byte-filter') || 'all', multiByteOverlay: localStorage.getItem('meshcore-map-multibyte-overlay') === 'true' };
  let selectedReferenceNode = null;  // pubkey of the reference node for neighbor filtering
  let neighborPubkeys = null;        // Set of pubkeys that are direct neighbors of selected node
  let wsHandler = null;
  let heatLayer = null;
  let geoFilterLayer = null;
  let affinityLayer = null;
  let affinityData = null;
  let topRoutesLayer = null;
  let topRoutesEdges = null; // cached neighbor-graph edges for the Important Links overlay
  let topRoutesRenderTimer = null; // debounce for the Top-N slider
  let topRoutesAccentCache = ''; // cached --accent, invalidated on theme-refresh
  let userHasMoved = false;
  let controlsCollapsed = false;

  // Safe escape — falls back to identity if app.js hasn't loaded yet.
  // Escape untrusted strings (mesh-advertised node names) before HTML interpolation
  // in map popups. The real global helper is `escapeHtml` (app.js); the previous
  // `globalThis.esc` lookup was always undefined, making this a no-op → stored XSS
  // via adv_name (#1536). Fall back to a real escaper, never identity.
  // 5-char OWASP set — including ' for single-quoted attribute safety.
  const safeEsc = (typeof escapeHtml === 'function') ? escapeHtml : function (s) {
    return s == null ? '' : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  };

  // Roles loaded from shared roles.js (ROLE_STYLE, ROLE_LABELS, ROLE_COLORS globals)

  // ── #1356 a11y constants — letter prefix + glyph + neutral fill carriers ──
  // ROLE_LETTERS gives each role a single capital-letter primary carrier
  // (legible at 10px monospace, survives full grayscale).
  var ROLE_LETTERS = {
    repeater:  'R',
    companion: 'C',
    room:      'M',
    sensor:    'S',
    observer:  'O',
  };
  // MB_GLYPHS prefix the hash text with a non-color status carrier.
  var MB_GLYPHS = {
    confirmed: '\u2713', // check
    suspected: '?',
    unknown:   '\u2717', // x-mark
  };
  // Per-status CSS class (drives the colored 3px left-border in style.css).
  var MB_STATUS_CLASS = {
    confirmed: 'status-confirmed',
    suspected: 'status-suspected',
    unknown:   'status-unknown',
  };
  // #1356 V3 marker-dot tint set — high-luminance accents that mirror the
  // CSS `--mc-mb-confirmed/suspected/unknown` left-border stripe palette so the
  // marker-dot and label-stripe surfaces stay visually consistent. Module
  // scope (not loop-local) to avoid per-iteration object allocation.
  var MB_MARKER_TINT = { confirmed: '#56F0A0', suspected: '#FFD966', unknown: '#FF8888' };

  function makeMarkerIcon(role, isStale, isAlsoObserver, colorOverride) {
    const s = ROLE_STYLE[role] || ROLE_STYLE.companion;
    // #1438: default fill resolves through the live CSS var so existing
    // mounted SVG markers recolor when cb-preset switches or the
    // operator picks a per-role override. colorOverride (MultiByte
    // status tint) wins when provided.
    const fillColor = colorOverride || ('var(--mc-role-' + (role || 'companion') + ')');
    // #1488 — marker stroke through CSS vars so operators can dial
    // colour / width / opacity via the customizer (Colors → Marker Stroke).
    const strokeAttr = ' stroke="var(--mc-marker-stroke-color)" stroke-width="var(--mc-marker-stroke-width)" stroke-opacity="var(--mc-marker-stroke-opacity)"';
    const size = s.radius * 2 + 4;
    const c = size / 2;
    let path;
    switch (s.shape) {
      case 'diamond':
        path = `<polygon points="${c},2 ${size-2},${c} ${c},${size-2} 2,${c}" fill="${fillColor}"${strokeAttr}/>`;
        break;
      case 'square':
        path = `<rect x="3" y="3" width="${size-6}" height="${size-6}" fill="${fillColor}"${strokeAttr}/>`;
        break;
      case 'triangle':
        path = `<polygon points="${c},2 ${size-2},${size-2} 2,${size-2}" fill="${fillColor}"${strokeAttr}/>`;
        break;
      case 'hexagon': {
        // #1293 — pointy-top hexagon for room servers
        const hr = c - 1.5;
        let hpts = '';
        for (let hi = 0; hi < 6; hi++) {
          const ha = (hi * 60 - 90) * Math.PI / 180;
          hpts += (c + hr * Math.cos(ha)).toFixed(2) + ',' +
                  (c + hr * Math.sin(ha)).toFixed(2) + ' ';
        }
        path = `<polygon points="${hpts.trim()}" fill="${fillColor}"${strokeAttr}/>`;
        break;
      }
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
        path = `<polygon points="${pts.trim()}" fill="${fillColor}"${strokeAttr}/>`;
        break;
      }
      default: // circle
        path = `<circle cx="${c}" cy="${c}" r="${c-2}" fill="${fillColor}"${strokeAttr}/>`;
    }
    // If this node is also an observer, add a small star overlay
    let obsOverlay = '';
    if (isAlsoObserver) {
      const starSize = 8;
      const sx = size - starSize, sy = 0;
      const scx = starSize / 2, scy = starSize / 2, so = starSize / 2 - 0.5, si = so * 0.4;
      let starPts = '';
      for (let i = 0; i < 5; i++) {
        const aO = (i * 72 - 90) * Math.PI / 180;
        const aI = ((i * 72) + 36 - 90) * Math.PI / 180;
        starPts += `${scx + so * Math.cos(aO)},${scy + so * Math.sin(aO)} `;
        starPts += `${scx + si * Math.cos(aI)},${scy + si * Math.sin(aI)} `;
      }
      obsOverlay = `<g transform="translate(${sx},${sy})"><polygon points="${starPts.trim()}" fill="var(--mc-role-observer)" stroke="var(--mc-marker-stroke-color)" stroke-width="0.8" stroke-opacity="var(--mc-marker-stroke-opacity)"/></g>`;
    }
    const svg = `<svg width="${size}" height="${size}" viewBox="0 0 ${size} ${size}" xmlns="http://www.w3.org/2000/svg">${path}${obsOverlay}</svg>`;
    return L.divIcon({
      html: svg,
      className: 'meshcore-marker' + (isStale ? ' marker-stale' : ''),
      iconSize: [size, size],
      iconAnchor: [c, c],
      popupAnchor: [0, -c],
    });
  }

  function makeRepeaterLabelIcon(node, isStale, isAlsoObserver, mbStatus) {
    // Show the short mesh hash ID (first N bytes of pubkey, uppercased). When
    // the width is unobserved the label falls back to one byte — it has to draw
    // something — but says so instead of asserting a 1-byte config.
    var hashInfo = hashPrefixInfo(node);
    var unknownWidth = hashInfo.known ? '' : ' hash-unconfirmed';
    // #1356 V3: glyph is the primary non-color status carrier, hash is the data,
    // status color is a thin left-border (CSS class drives the hue).
    //
    // ORDER MATTERS: the hash variable must stay immediately below the glyph
    // lookup. test-issue-1356-map-a11y.js:133 asserts the glyph-before-hash
    // ordering with a source grep bounded to 200 characters, so anything
    // inserted between the two declarations fails the build even though the
    // rendering is untouched. Deliberately worded without naming either
    // identifier: spelling them out here would satisfy that grep from inside
    // this comment and the assertion could then never fail.
    var status = mbStatus || null;
    var glyph = status ? (MB_GLYPHS[status] || MB_GLYPHS.unknown) : '';
    var shortHash = hashInfo.prefix;
    var statusClass = status ? (' ' + (MB_STATUS_CLASS[status] || MB_STATUS_CLASS.unknown)) : '';
    var ariaWidth = hashInfo.known ? '' : ', hash size unknown';
    var ariaStatus = status ? ('multi-byte ' + status + ', hash ' + shortHash + ariaWidth)
                            : ('repeater hash ' + shortHash + ariaWidth);
    // Observer indicator stays a star — it is an orthogonal signal, not a status color.
    var obsIndicator = isAlsoObserver
      ? ' <span aria-hidden="true" style="color:' + (ROLE_COLORS.observer || '#f1c40f') + ';font-size:13px;line-height:1;" title="Also an observer"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star-fill"/></svg></span>'
      : '';
    // Glyph + thin-space (U+2009) + hash. Visible content is aria-hidden so AT
    // reads the aria-label only (avoids "check mark 3 E" literal announcements).
    var visible = (glyph ? glyph + '\u2009' : '') + shortHash;
    var html = '<div class="mc-mb-label' + statusClass + unknownWidth + '" role="img" aria-label="' + ariaStatus + '">' +
      '<span aria-hidden="true">' + visible + '</span>' + obsIndicator + '</div>';
    return L.divIcon({
      html: html,
      className: 'meshcore-marker meshcore-label-marker' + (isStale ? ' marker-stale' : ''),
      iconSize: null,
      iconAnchor: [14, 12],
      popupAnchor: [0, -12],
    });
  }

  async function init(container) {
    container.innerHTML = `
      <div id="map-wrap" style="position:relative;width:100%;height:100%;display:flex;">
        <div id="leaflet-map" style="flex:1 1 0%;height:100%;"></div>
        <div class="map-side-pane" id="mapSidePane">
          <div class="pane-toggle" id="mapPaneToggle" title="Path Inspector"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-left"/></svg></div>
          <div class="pane-content">
            <h3 style="margin:0 0 8px 0;font-size:14px;">Path Inspector</h3>
            <p style="font-size:11px;color:var(--text-muted);margin:0 0 8px 0;">Hex prefixes (1-3 bytes), comma or space separated.</p>
            <div style="display:flex;gap:4px;margin-bottom:8px;">
              <input type="text" id="mapPiInput" class="input" placeholder="2C,A1,F4" style="flex:1;">
              <button id="mapPiSubmit" class="btn btn-primary btn-sm">Go</button>
            </div>
            <div id="mapPiError" class="path-inspector-error"></div>
            <div id="mapPiResults"></div>
          </div>
        </div>
        <button class="map-controls-toggle" id="mapControlsToggle" aria-label="Toggle map controls" aria-expanded="true"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-gear"/></svg></button>
        <div class="map-controls" id="mapControls" role="region" aria-label="Map controls">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-trifold"/></svg> Map Controls</h3>
          <fieldset class="mc-section">
            <legend class="mc-label">Node Types</legend>
            <div id="mcRoleChecks"></div>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Byte Size</legend>
            <div class="filter-group" id="mcByteFilter">
              <button class="btn ${filters.byteSize==='all'?'active':''}" data-byte="all">All</button>
              <button class="btn ${filters.byteSize==='1'?'active':''}" data-byte="1">1-byte</button>
              <button class="btn ${filters.byteSize==='2'?'active':''}" data-byte="2">2-byte</button>
              <button class="btn ${filters.byteSize==='3'?'active':''}" data-byte="3">3-byte</button>
              <button class="btn ${filters.byteSize==='unknown'?'active':''}" data-byte="unknown" title="No advert-derived hash size in the retention window">Unknown</button>
            </div>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Display</legend>
            <label for="mcClusters"><input type="checkbox" id="mcClusters"> Cluster markers</label>
            <label for="mcHeatmap"><input type="checkbox" id="mcHeatmap"> Heat map</label>
            <label for="mcHashLabels"><input type="checkbox" id="mcHashLabels"> Hash prefix labels</label>
            <label for="mcMultiByte"><input type="checkbox" id="mcMultiByte"> Multi-byte support</label>
            <label id="mcGeoFilterLabel" for="mcGeoFilter" style="display:none"><input type="checkbox" id="mcGeoFilter"> Mesh live area</label>
          </fieldset>
          <div id="mapAreaFilter"></div>
          <fieldset class="mc-section">
            <legend class="mc-label">Status</legend>
            <div class="filter-group" id="mcStatusFilter">
              <button class="btn ${filters.statusFilter==='all'?'active':''}" data-status="all">All</button>
              <button class="btn ${filters.statusFilter==='active'?'active':''}" data-status="active">Active</button>
              <button class="btn ${filters.statusFilter==='stale'?'active':''}" data-status="stale">Stale</button>
            </div>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Filters</legend>
            <label for="mcNeighbors"><input type="checkbox" id="mcNeighbors"> Show direct neighbors</label>
            <div id="mcNeighborRef" style="display:none;font-size:11px;color:var(--text-muted);margin-top:2px;padding-left:20px;">Ref: <span id="mcNeighborRefName">—</span></div>
            <div id="mcNeighborHint" style="display:none;font-size:11px;color:var(--text-muted);margin-top:2px;padding-left:20px;">Click a node marker to set the reference node</div>
            <label id="mcAffinityDebugLabel" for="mcAffinityDebug" style="display:none"><input type="checkbox" id="mcAffinityDebug"> <svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg> Affinity Debug</label>
          </fieldset>
          <fieldset class="mc-section">
            <legend class="mc-label">Important Links</legend>
            <label for="mcTopRoutes"><input type="checkbox" id="mcTopRoutes"> <svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-graph"/></svg> Show important links</label>
            <div id="mcTopRoutesOpts" style="display:none;padding-left:20px;margin-top:4px">
              <label for="mcTopRoutesRankBy" style="font-size:11px;color:var(--text-muted)">Rank by</label>
              <select id="mcTopRoutesRankBy" style="width:100%;margin:2px 0 6px">
                <option value="usefulness">Usefulness (composite)</option>
                <option value="bridge">Bridge</option>
                <option value="redundancy">Redundancy</option>
                <option value="traffic">Traffic share</option>
                <option value="affinity">Affinity only</option>
              </select>
              <label for="mcTopRoutesN" style="font-size:11px;color:var(--text-muted)">Top <span id="mcTopRoutesNVal">50</span> links</label>
              <input type="range" id="mcTopRoutesN" min="10" max="200" step="10" value="50" style="width:100%">
              <div id="mcTopRoutesHint" style="display:none;font-size:11px;color:var(--text-muted);margin-top:4px">No links to show — endpoints may lack GPS, or the chosen axis has no scores yet (the #672 scores need a server that ships them).</div>
            </div>
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

    // Init Leaflet — restore saved position or use configurable defaults (#115)
    let defaultCenter = [37.6, -122.1];
    let defaultZoom = 9;
    try {
      const mapCfg = await (await fetch('/api/config/map')).json();
      if (Array.isArray(mapCfg.center) && mapCfg.center.length === 2) defaultCenter = mapCfg.center;
      if (typeof mapCfg.zoom === 'number') defaultZoom = mapCfg.zoom;
    } catch {}
    let initCenter = defaultCenter;
    let initZoom = defaultZoom;
    // Check URL query params first (from packet detail links). #1709: shared
    // parseViewportHash helper validates lat/lon together + clamps zoom.
    const urlParams = new URLSearchParams(location.hash.split('?')[1] || '');
    const vp = (typeof parseViewportHash === 'function')
      ? parseViewportHash(location.hash) : null;
    if (vp) {
      initCenter = [vp.lat, vp.lon];
      initZoom = vp.zoom;
    } else {
      const savedView = localStorage.getItem('map-view');
      if (savedView) {
        try { const v = JSON.parse(savedView); initCenter = [v.lat, v.lng]; initZoom = v.zoom; } catch {}
      }
    }
    map = L.map('leaflet-map', { zoomControl: true }).setView(initCenter, initZoom);

    // If navigated with ?node=PUBKEY, highlight that node after markers load
    targetNodeKey = urlParams.get('node') || null;

    const isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
    // #1420 — multi-provider dark-tile picker. Light mode unchanged.
    let _darkRefLayer = null;  // Esri-only: labels overlay
    function _resolveTileUrl(dark) {
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
    const autoLayerGroup = L.layerGroup().addTo(map);
    
    function _syncDarkTiles(dark) {
      const r = _resolveTileUrl(dark);
      tileLayer.setUrl(r.url);
      if (tileLayer.options) tileLayer.options.attribution = r.attribution;
      // Esri reference (labels) overlay: add when needed, remove otherwise.
      if (dark && r.refUrl) {
        if (!_darkRefLayer) {
          _darkRefLayer = L.tileLayer(r.refUrl, { maxZoom: 19, attribution: r.attribution }).addTo(autoLayerGroup);
        } else {
          _darkRefLayer.setUrl(r.refUrl);
          if (!autoLayerGroup.hasLayer(_darkRefLayer)) _darkRefLayer.addTo(autoLayerGroup);
        }
      } else if (_darkRefLayer) {
        autoLayerGroup.removeLayer(_darkRefLayer);
        _darkRefLayer = null;
      }
      if (typeof window.MC_applyTileFilter === 'function') window.MC_applyTileFilter();
      // Make sure the map is loaded before trying to update the ui
      if (map && map.attributionControl) {
        try { map.attributionControl._update && map.attributionControl._update(); } catch (_) {}
      }
    }
    const _initTile = _resolveTileUrl(isDark);
    const tileLayer = L.tileLayer(_initTile.url, {
      attribution: _initTile.attribution,
      maxZoom: 19,
    }).addTo(autoLayerGroup);
    if (isDark && _initTile.refUrl) {
      _darkRefLayer = L.tileLayer(_initTile.refUrl, { maxZoom: 19, attribution: _initTile.attribution }).addTo(autoLayerGroup);
    }
    if (typeof window.MC_applyTileFilter === 'function') window.MC_applyTileFilter();
    
    // Add Layer Control, passing 'topleft' to put it on the left
    if (typeof window.MC_createLayerControl === 'function') {
      window.MC_createLayerControl(map, autoLayerGroup, 'topleft');
    }

    const _mapThemeObs = new MutationObserver(function () {
      const dark = document.documentElement.getAttribute('data-theme') === 'dark' ||
        (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
      _syncDarkTiles(dark);
    });
    _mapThemeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    // #1420 — re-render when the user picks a different dark provider in the customizer.
    window.addEventListener('mc-tile-provider-changed', function (e) {
      const dark = document.documentElement.getAttribute('data-theme') === 'dark' ||
        (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
      if (e && e.detail && e.detail.type && e.detail.type !== (dark ? 'dark' : 'light')) return;
      _syncDarkTiles(dark);
    });

    // #1689 r1 (adv #4): live re-render when the customizer "hide 1-byte
    // path hops" toggle flips. Before this listener the toggle only took
    // effect on the next navigation despite an inline comment claiming
    // "live update". Re-issue the most recent drawPacketRoute(Multi) call
    // so the polyline + redacted badge update in place.
    if (typeof window !== 'undefined' && !window.__mc_map_hide1byte_wired) {
      window.__mc_map_hide1byte_wired = true;
      window.addEventListener('mc-hide-1byte-hops-changed', function () {
        try {
          const last = window.__mc_lastRouteDraw;
          if (!last) return;
          if (last.kind === 'multi' && typeof window.drawPacketRouteMulti === 'function') {
            window.drawPacketRouteMulti(last.paths, last.origin, last.opts);
          } else if (last.kind === 'single' && typeof window.drawPacketRoute === 'function') {
            window.drawPacketRoute(last.hopKeys, last.origin, last.opts);
          }
        } catch (e) { console.warn('[map] hide-1byte re-render failed', e); }
      });
    }

    // Save position on move
    map.on('moveend', () => {
      const c = map.getCenter();
      localStorage.setItem('map-view', JSON.stringify({ lat: c.lat, lng: c.lng, zoom: map.getZoom() }));
      userHasMoved = true;
    });

    map.on('zoomend', () => {
      clearTimeout(_zoomResizeTimer);
      _zoomResizeTimer = setTimeout(() => {
        if (!_renderingMarkers) _repositionMarkers();
      }, 150);
    });

    map.on('resize', () => {
      clearTimeout(_zoomResizeTimer);
      _zoomResizeTimer = setTimeout(() => {
        if (!_renderingMarkers) _repositionMarkers();
      }, 150);
    });

    markerLayer = L.layerGroup().addTo(map);
    clusterGroup = createClusterGroup();
    if (filters.clustering && clusterGroup) clusterGroup.addTo(map);
    routeLayer = L.layerGroup().addTo(map);
    // Exposed for the #1374 route renderer (window.MeshRoute) and its E2E tests.
    if (typeof window !== 'undefined') {
      window.__mc_map = map;
      window.__mc_routeLayer = routeLayer;
      // Expose nodes array for route-view's path-picker isolation
      // (needs to resolve hop prefixes that aren't in the canonical path).
      Object.defineProperty(window, '__mc_nodes', { get: function () { return nodes; }, configurable: true });
      window.deconflictLabels = deconflictLabels;
    }

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

    // #1329: Map controls accordion. Make each section's legend a button-
    // style toggle with aria-expanded. On mobile (≤640px) only one section
    // is open at a time so the panel never needs internal scrolling. On
    // desktop the .mc-collapsed class has no visual effect (CSS only hides
    // section bodies inside the mobile media query) so all controls stay
    // visible — but single-open behaviour is still tracked for state
    // consistency. See test-issue-1329-map-controls-accordion-e2e.js.
    (function initMapControlsAccordion() {
      const isMobile = window.innerWidth <= 640;
      const sections = Array.from(controlsPanel.querySelectorAll('fieldset.mc-section'));
      sections.forEach((fs, idx) => {
        const legend = fs.querySelector('legend.mc-label');
        if (!legend) return;
        // Initial state: on mobile only the first section is open; on
        // desktop all sections are open.
        const open = !isMobile || idx === 0;
        legend.setAttribute('role', 'button');
        legend.setAttribute('tabindex', '0');
        legend.setAttribute('aria-expanded', String(open));
        fs.classList.toggle('mc-collapsed', !open);
        const setOpen = (target, openNow) => {
          target.setAttribute('aria-expanded', String(openNow));
          const parent = target.closest('fieldset.mc-section');
          if (parent) parent.classList.toggle('mc-collapsed', !openNow);
        };
        const onActivate = (e) => {
          e.preventDefault();
          const currentlyOpen = legend.getAttribute('aria-expanded') === 'true';
          // Single-open: close every other section first.
          sections.forEach(other => {
            const otherLegend = other.querySelector('legend.mc-label');
            if (otherLegend && otherLegend !== legend) setOpen(otherLegend, false);
          });
          setOpen(legend, !currentlyOpen);
        };
        legend.addEventListener('click', onActivate);
        legend.addEventListener('keydown', (e) => {
          if (e.key === 'Enter' || e.key === ' ') onActivate(e);
        });
      });
    })();

    // Bind controls
    var clustersEl = document.getElementById('mcClusters');
    if (clustersEl) {
      clustersEl.checked = filters.clustering;
      clustersEl.addEventListener('change', function (e) {
        filters.clustering = e.target.checked;
        localStorage.setItem('meshcore-map-clustering', filters.clustering);
        if (filters.clustering) {
          if (clusterGroup && !map.hasLayer(clusterGroup)) clusterGroup.addTo(map);
        } else {
          if (clusterGroup && map.hasLayer(clusterGroup)) map.removeLayer(clusterGroup);
        }
        renderMarkers();
      });
    }
    const heatEl = document.getElementById('mcHeatmap');
    if (localStorage.getItem('meshcore-map-heatmap') === 'true') { heatEl.checked = true; }
    heatEl.addEventListener('change', e => { localStorage.setItem('meshcore-map-heatmap', e.target.checked); toggleHeatmap(e.target.checked); });
    document.getElementById('mcNeighbors').addEventListener('change', e => {
      filters.neighbors = e.target.checked;
      const hintEl = document.getElementById('mcNeighborHint');
      const refEl = document.getElementById('mcNeighborRef');
      if (e.target.checked && !selectedReferenceNode) {
        hintEl.style.display = 'block';
        refEl.style.display = 'none';
      } else {
        hintEl.style.display = 'none';
        refEl.style.display = selectedReferenceNode ? 'block' : 'none';
      }
      renderMarkers();
    });

    // Affinity Debug overlay toggle — shown only when debugAffinity config is on or localStorage override
    (function initAffinityDebug() {
      var label = document.getElementById('mcAffinityDebugLabel');
      var show = (window.CLIENT_CONFIG && window.CLIENT_CONFIG.debugAffinity) || localStorage.getItem('meshcore-affinity-debug') === 'true';
      if (show && label) label.style.display = '';
      var cb = document.getElementById('mcAffinityDebug');
      if (!cb) return;
      cb.addEventListener('change', function (e) {
        if (e.target.checked) {
          loadAffinityDebugOverlay();
        } else {
          clearAffinityOverlay();
        }
      });
    })();

    // Important Links overlay (#672 / D) — public, B-weighted top routes.
    (function initTopRoutes() {
      const cb = document.getElementById('mcTopRoutes');
      if (!cb) return;
      const opts = document.getElementById('mcTopRoutesOpts');
      const rankBy = document.getElementById('mcTopRoutesRankBy');
      const nSlider = document.getElementById('mcTopRoutesN');
      const nVal = document.getElementById('mcTopRoutesNVal');
      const savedAxis = localStorage.getItem('meshcore-top-routes-axis');
      if (savedAxis && rankBy) rankBy.value = savedAxis;
      const savedN = localStorage.getItem('meshcore-top-routes-n');
      if (savedN && nSlider) { nSlider.value = savedN; if (nVal) nVal.textContent = savedN; }
      cb.addEventListener('change', e => {
        if (opts) opts.style.display = e.target.checked ? '' : 'none';
        if (e.target.checked) loadTopRoutes(); else clearTopRoutes();
      });
      if (rankBy) rankBy.addEventListener('change', e => {
        localStorage.setItem('meshcore-top-routes-axis', e.target.value);
        if (cb.checked) renderTopRoutes();
      });
      if (nSlider) nSlider.addEventListener('input', e => {
        if (nVal) nVal.textContent = e.target.value;
        localStorage.setItem('meshcore-top-routes-n', e.target.value);
        // Debounce: dragging the slider fires 'input' rapidly; redraw at most
        // ~every 80ms so a large top-N doesn't lag the map.
        if (cb.checked) {
          clearTimeout(topRoutesRenderTimer);
          topRoutesRenderTimer = setTimeout(renderTopRoutes, 80);
        }
      });
    })();

    // Hash Labels toggle
    const hashLabelEl = document.getElementById('mcHashLabels');
    if (hashLabelEl) {
      hashLabelEl.checked = filters.hashLabels;
      hashLabelEl.addEventListener('change', e => { filters.hashLabels = e.target.checked; localStorage.setItem('meshcore-map-hash-labels', filters.hashLabels); renderMarkers(); });
    }
    const multiByteEl = document.getElementById('mcMultiByte');
    if (multiByteEl) {
      multiByteEl.checked = filters.multiByteOverlay;
      multiByteEl.addEventListener('change', e => { filters.multiByteOverlay = e.target.checked; localStorage.setItem('meshcore-map-multibyte-overlay', e.target.checked); renderMarkers(); });
    }
    document.getElementById('mcLastHeard').addEventListener('change', e => { filters.lastHeard = e.target.value; loadNodes(); });

    AreaFilter.init(document.getElementById('mapAreaFilter'));
    AreaFilter.onChange(function () { loadNodes(); });

    // Status filter buttons
    document.querySelectorAll('#mcStatusFilter .btn').forEach(btn => {
      btn.addEventListener('click', () => {
        filters.statusFilter = btn.dataset.status;
        localStorage.setItem('meshcore-map-status-filter', filters.statusFilter);
        document.querySelectorAll('#mcStatusFilter .btn').forEach(b => b.classList.toggle('active', b.dataset.status === filters.statusFilter));
        renderMarkers();
      });
    });

    // Byte size filter buttons
    document.querySelectorAll('#mcByteFilter .btn').forEach(btn => {
      btn.addEventListener('click', () => {
        filters.byteSize = btn.dataset.byte;
        localStorage.setItem('meshcore-map-byte-filter', filters.byteSize);
        document.querySelectorAll('#mcByteFilter .btn').forEach(b => b.classList.toggle('active', b.dataset.byte === filters.byteSize));
        renderMarkers();
      });
    });

    // Geo filter overlay
    (async function () {
      try {
        const gf = await api('/config/geo-filter', { ttl: 3600 });
        if (!gf || !gf.polygon || gf.polygon.length < 3) return;
        const geoColor = getComputedStyle(document.documentElement).getPropertyValue('--geo-filter-color').trim() || '#3b82f6';
        const latlngs = gf.polygon.map(function (p) { return [p[0], p[1]]; });
        const innerPoly = L.polygon(latlngs, {
          color: geoColor, weight: 2, opacity: 0.8,
          fillColor: geoColor, fillOpacity: 0.08
        });
        // Approximate buffer zone — expand each vertex outward from centroid by bufferKm
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
        const label = document.getElementById('mcGeoFilterLabel');
        if (label) label.style.display = '';
        const el = document.getElementById('mcGeoFilter');
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
          if (Array.isArray(parsed)) {
            drawPacketRoute(parsed, null);
          } else if (parsed.paths && parsed.paths.length > 0) {
            drawPacketRouteMulti(parsed.paths, parsed.origin || null, {
              packetHash: parsed.packetHash,
              canonicalPath: parsed.hops || null,
              destination: parsed.destination || null
            });
          } else {
            drawPacketRoute(parsed.hops || [], parsed.origin || null, { destination: parsed.destination || null });
          }
        } catch {}
      } else {
        // #1418/#1419: deep-link via URL params — #/map?packet=<hash>&obs=<id>
        // (or just packet=<hash> for first observation). Fetch from API, build
        // the same payload as the sessionStorage flow, dispatch to renderer.
        // This makes routes shareable / bookmarkable without sessionStorage state.
        loadRouteFromDeepLink();
      }
    });
  }

  async function drawPacketRoute(hopKeys, origin, opts) {
    // Defensive: origin must be an object with pubkey/lat/lon/name. A bare
    // string slips through both branches at lines below and silently no-ops
    // the originator marker (caused PR #950's bug). Coerce string → object
    // and warn so callers get a clear signal.
    if (typeof origin === 'string') {
      console.warn('drawPacketRoute: origin should be an object {pubkey,lat,lon,name}, got string. Coercing.');
      origin = { pubkey: origin };
    }
    opts = opts || {};
    // #1689 r1 (adv #4): remember the last route-draw inputs so the
    // mc-hide-1byte-hops-changed listener can re-render in place.
    try { window.__mc_lastRouteDraw = { kind: 'single', hopKeys: hopKeys, origin: origin, opts: opts }; } catch (_e) {}
    if (window.__mc_routeTrustControl) {
      try { map.removeControl(window.__mc_routeTrustControl); } catch (_e) {}
      window.__mc_routeTrustControl = null;
    }
    // #1784 — path trust threshold: if ALL hops are below the configured
    // minimum hash bytes for mapping, don't draw speculative polylines.
    // The raw hops stay visible in packet detail views; this only gates
    // derived topology display. Operators can lower the threshold via
    // pathTrust.minHashBytesForMapping in config.json.
    if (window.MC_pathBelowTrust && window.MC_pathBelowTrust(hopKeys)) {
      var _t = window.MC_getPathTrustThreshold ? window.MC_getPathTrustThreshold() : 1;
      var _hb = 0;
      if (hopKeys && hopKeys.length) {
        for (var _hi = 0; _hi < hopKeys.length; _hi++) {
          _hb = Math.max(_hb, window.MC_hopByteLen(hopKeys[_hi]));
        }
      }
      routeLayer.clearLayers();
      var _trustMsg = L.control({ position: 'topright' });
      _trustMsg.onAdd = function () {
        var d = L.DomUtil.create('div', 'leaflet-bar mc-route-trust-message');
        d.innerHTML = '<strong>Route not displayed</strong><br><span class="mc-route-trust-message-detail">Packet uses ' + _hb + '-byte path hashes, below configured trust threshold (' + _t + '-byte minimum). Raw hops remain visible in packet detail views. Set <code>pathTrust.minHashBytesForMapping</code> in config.json to trust shorter prefixes.</span>';
        return d;
      };
      _trustMsg.addTo(map);
      window.__mc_routeTrustControl = _trustMsg;
      return;
    }
    // #1422: use the backend's /api/resolve-hops for proper disambiguation
    // (unique_prefix vs multi-byte vs gps_preference vs affinity scoring).
    // Falls back to naive nodes.filter() scan if the API is unreachable.
    let serverResolved = null;
    try {
      const hopList = (hopKeys || []).join(',');
      const apiUrl = '/api/resolve-hops?hops=' + encodeURIComponent(hopList);
      const resp = await fetch(apiUrl, { cache: 'no-cache' });
      if (resp.ok) {
        const json = await resp.json();
        serverResolved = json && json.resolved ? json.resolved : null;
      }
    } catch (e) {
      console.warn('resolve-hops API call failed, falling back to local nodes scan', e);
    }
    // Hide default markers so only the route is visible
    if (markerLayer) map.removeLayer(markerLayer);
    if (clusterGroup) map.removeLayer(clusterGroup);
    if (heatLayer) map.removeLayer(heatLayer);

    routeLayer.clearLayers();

    // Add close route button
    const closeBtn = L.control({ position: 'topright' });
    closeBtn.onAdd = function () {
      const div = L.DomUtil.create('div', 'leaflet-bar');
      div.innerHTML = '<a href="#" title="Close route" style="font-size:18px;font-weight:bold;text-decoration:none;display:block;width:36px;height:36px;line-height:36px;text-align:center;background:var(--input-bg,#1e293b);color:var(--text,#e2e8f0);border-radius:4px" aria-label="Close route"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></a>';
      L.DomEvent.on(div, 'click', function (e) {
        L.DomEvent.preventDefault(e);
        routeLayer.clearLayers();
        if (markerLayer) map.addLayer(markerLayer);
        if (clusterGroup) map.addLayer(clusterGroup);
        map.removeControl(closeBtn);
        var container = map.getContainer();
        var legend = container.querySelector('.mc-route-legend');
        if (legend) legend.remove();
        var ctx = container.querySelector('.mc-route-context-label');
        if (ctx) ctx.remove();
      });
      return div;
    };
    closeBtn.addTo(map);

    // Resolve hop short hashes to node positions with geographic disambiguation.
    // Unresolvable hops (no matching node) become {resolved:false} sentinels
    // so the modern renderer (#1374) can render dashed-gray placeholders + a
    // "X of N hops resolved" badge instead of silently dropping them.
    //
    // #1418/#1422: PREFER serverResolved from /api/resolve-hops which does
    //   - unique_prefix matching (uses multi-byte adverts to disambiguate 1-byte hops)
    //   - gps_preference (skips nodes with lat=0 sentinel)
    //   - affinity scoring (neighbor-graph aware)
    // Fall back to a naive local nodes.filter() scan when the API failed or
    // the hop wasn't in the server response. When a node MATCHES by
    // prefix/pubkey but has no GPS coords, we still want its name/role/pubkey
    // for the sidebar — flag it as {resolved:false, gpsless:true} so the
    // renderer can label it "no GPS" instead of "unresolved prefix".
    const raw = hopKeys.map(hop => {
      const hopLower = String(hop).toLowerCase();
      // #1633 — tag the original hex token so MC_isVisibleHop can filter
      // 1-byte hops out of the polyline/sidebar when the customizer toggle
      // is ON. The hop value stays in place upstream (resolve, fitBounds
      // ordering) — only the polyline render iterator drops it.
      const _hopHex = String(hop);
      // Try server resolution first
      const srv = serverResolved && (serverResolved[hop] || serverResolved[hopLower] || serverResolved[hop.toUpperCase()]);
      if (srv && srv.pubkey) {
        const c = srv.candidates && srv.candidates[0];
        if (c && c.lat != null && c.lon != null && !(c.lat === 0 && c.lon === 0)) {
          return { lat: c.lat, lon: c.lon, name: srv.name || c.name || hop.slice(0,8), pubkey: srv.pubkey, role: c.role, resolved: true, _hopHex };
        }
        // Server resolved but node has no usable GPS
        return { name: srv.name || hop.slice(0,8), pubkey: srv.pubkey, role: (c && c.role) || null, resolved: false, gpsless: true, _hopHex };
      }
      // Fallback: naive local scan (kept for resilience when API is down).
      const allMatches = nodes.filter(n => {
        const pk = n.public_key.toLowerCase();
        return (pk === hopLower || pk.startsWith(hopLower) || hopLower.startsWith(pk));
      });
      const withGps = allMatches.filter(n => n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0));
      if (withGps.length === 1) {
        const c = withGps[0];
        return { lat: c.lat, lon: c.lon, name: c.name || hop.slice(0,8), pubkey: c.public_key, role: c.role, resolved: true, _hopHex };
      } else if (withGps.length > 1) {
        return { name: hop.slice(0,8), pubkey: hop, resolved: false, candidates: withGps, _hopHex };
      } else if (allMatches.length >= 1) {
        const c = allMatches[0];
        return { name: c.name || hop.slice(0,8), pubkey: c.public_key, role: c.role, resolved: false, gpsless: true, _hopHex };
      }
      return { name: String(hop).slice(0, 8), pubkey: hop, resolved: false, _hopHex };
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

    let positions = raw.filter(h => h != null);

    // Resolve and prepend origin node
    if (origin) {
      let originPos = null;
      const originHasRealGps = (lat, lon) => lat != null && lon != null && !(lat === 0 && lon === 0);
      if (originHasRealGps(origin.lat, origin.lon)) {
        originPos = { lat: origin.lat, lon: origin.lon, name: origin.name || 'Sender', pubkey: origin.pubkey, role: origin.role || 'companion', resolved: true, isOrigin: true, _fromPayload: true };
      } else if (origin.pubkey) {
        const pk = origin.pubkey.toLowerCase();
        const match = nodes.find(n => n.public_key.toLowerCase() === pk || n.public_key.toLowerCase().startsWith(pk));
        if (match) {
          if (originHasRealGps(match.lat, match.lon)) {
            originPos = { lat: match.lat, lon: match.lon, name: origin.name || match.name || 'Sender', pubkey: match.public_key, role: match.role || 'companion', resolved: true, isOrigin: true, _fromPayload: true };
          } else {
            originPos = { name: origin.name || match.name || 'Sender', pubkey: match.public_key, role: match.role || 'companion', resolved: false, gpsless: true, isOrigin: true, _fromPayload: true };
          }
        }
      }
      if (originPos) positions.unshift(originPos);
    }

    // #1418 Phase D: append destination (recipient from decoded.destHash) so
    // the route view shows: sender → [intermediate hops] → recipient.
    // GPS-sanity: a lat=0,lon=0 sentinel or null coords means "no real GPS"
    // — show the node in the sidebar as gpsless rather than drawing it at
    // (0,0) which would force fitBounds to span the visible route → Africa.
    // _fromPayload: true so the renderer can visually mark these as "from
    // payload" (different source-of-truth than path hops).
    if (opts.destination && opts.destination.pubkey) {
      const dpk = opts.destination.pubkey.toLowerCase();
      const dmatch = nodes.find(n => n.public_key.toLowerCase() === dpk || n.public_key.toLowerCase().startsWith(dpk));
      if (dmatch) {
        const hasGps = dmatch.lat != null && dmatch.lon != null && !(dmatch.lat === 0 && dmatch.lon === 0);
        if (hasGps) {
          positions.push({ lat: dmatch.lat, lon: dmatch.lon, name: opts.destination.name || dmatch.name || 'Recipient', pubkey: dmatch.public_key, role: dmatch.role || 'companion', resolved: true, _fromPayload: true });
        } else {
          positions.push({ name: opts.destination.name || dmatch.name || 'Recipient', pubkey: dmatch.public_key, role: dmatch.role || 'companion', resolved: false, gpsless: true, _fromPayload: true });
        }
      }
    }

    if (positions.length < 1) return;
    // Mark final hop as destination so the renderer applies the dest glyph.
    positions[positions.length - 1].isDest = true;

    // #1633 — render-time 1-byte hop filter (default OFF). Origin / destination
    // positions are added without _hopHex (they came from the payload, not the
    // outer path bytes) and therefore always survive. Intermediate hops with a
    // 1-byte _hopHex are dropped when the customizer toggle is ON.
    //
    // #1689 r1 (adv #2): if the filter drops every intermediate hop the result
    // is a 2-point origin→dest polyline that is visually identical to a
    // "direct delivery, no hops" route. Operators misread that as ground-truth
    // direct path. To make the redaction LEGIBLE we tag the surviving
    // positions with `_hopsHiddenCount` so the renderer can draw the polyline
    // in a different style (dashed/lighter) and add a midpoint badge that
    // reads "N hops hidden (1-byte)".
    var hopsHiddenCount = 0;
    if (window.MC_isVisibleHop && window.MC_getHide1ByteHops && window.MC_getHide1ByteHops()) {
      var beforeCount = positions.length;
      var beforeIntermediate = positions.filter(function (p) { return !!p._hopHex; }).length;
      positions = positions.filter(function (p) {
        return !p._hopHex || window.MC_isVisibleHop(p._hopHex);
      });
      var afterIntermediate = positions.filter(function (p) { return !!p._hopHex; }).length;
      hopsHiddenCount = beforeIntermediate - afterIntermediate;
      if (positions.length < beforeCount && positions.length >= 1) {
        // Re-mark last surviving hop as destination if the original got dropped.
        positions[positions.length - 1].isDest = true;
      }
      if (positions.length < 1) return;
    }

    // Hand off to sequence-primary sequence-primary renderer (#1418), falling
    // back to the legacy role-aware MeshRoute (#1374), then to the minimal
    // polyline (should never run in production).
    if (window.MeshRouteView && typeof window.MeshRouteView.render === 'function') {
      window.MeshRouteView.render(map, routeLayer, positions, {
        timestamp: opts.timestamp || Date.now(),
        packetHash: opts.packetHash || null,
        observationId: opts.observationId || null,
        packetContext: opts.packetContext || null,
        // #1689 r1 (adv #2): tell the renderer how many intermediate hops
        // the 1-byte filter dropped so it can style the polyline as redacted.
        hopsHiddenCount: hopsHiddenCount
      });
      return;
    }
    if (window.MeshRoute && typeof window.MeshRoute.render === 'function') {
      window.MeshRoute.render(map, routeLayer, positions, {
        timestamp: opts.timestamp || Date.now(),
        hopsHiddenCount: hopsHiddenCount
      });
      return;
    }

    // ── Legacy fallback (kept tiny — should never run in production) ─────
    const coords = positions.filter(p => p.lat != null).map(p => [p.lat, p.lon]);
    if (coords.length >= 2) {
      L.polyline(coords, {
        color: '#f59e0b', weight: 3, opacity: hopsHiddenCount > 0 ? 0.55 : 0.8,
        dashArray: hopsHiddenCount > 0 ? '4 6' : '8 4'
      }).addTo(routeLayer);
      map.fitBounds(L.latLngBounds(coords).pad(0.3));
    } else if (coords.length === 1) {
      map.setView(coords[0], 13);
    }
  }

  // #1418 Phase C — multi-path renderer. Accepts an array of paths (each =
  // {path: [hopKeys], observer, snr, rssi}), aggregates into canonical hops +
  // per-edge observer-count (for stroke-width weighting), and dispatches to
  // the sequence renderer with multi-path metadata. Falls back to single-path
  // drawPacketRoute when only one observation is provided.
  //
  // opts.canonicalPath (optional): use this exact hop sequence as the canonical
  // spine — i.e. the observation the operator selected. Without it, longest-path
  // wins, which can show a totally different route than the user clicked.
  async function drawPacketRouteMulti(paths, origin, opts) {
    opts = opts || {};
    if (typeof origin === 'string') origin = { pubkey: origin };
    if (!Array.isArray(paths) || paths.length === 0) return;
    // #1689 r1 (adv #4): remember last multi-path draw for live re-render
    // when the hide-1-byte-hops toggle changes.
    try { window.__mc_lastRouteDraw = { kind: 'multi', paths: paths, origin: origin, opts: opts }; } catch (_e) {}
    if (paths.length === 1) {
      return drawPacketRoute(paths[0].path || [], origin, opts);
    }

    // Pick canonical: prefer caller-supplied (operator's chosen observation),
    // else fall back to longest path as the spine.
    var canonicalPath;
    if (opts.canonicalPath && Array.isArray(opts.canonicalPath) && opts.canonicalPath.length) {
      canonicalPath = opts.canonicalPath;
    } else {
      const sortedByLen = paths.slice().sort((a, b) => (b.path || []).length - (a.path || []).length);
      canonicalPath = sortedByLen[0].path || [];
    }
    const totalObservers = paths.length;

    // Count hop & edge occurrences across all paths.
    const hopCounts = {};
    const edgeCounts = {};
    paths.forEach(p => {
      const hops = p.path || [];
      hops.forEach(h => { hopCounts[h] = (hopCounts[h] || 0) + 1; });
      for (let i = 0; i < hops.length - 1; i++) {
        const key = hops[i] + '\u2192' + hops[i + 1];
        edgeCounts[key] = (edgeCounts[key] || 0) + 1;
      }
    });

    // Resolve canonical hops via /api/resolve-hops + naive fallback.
    let serverResolved = null;
    try {
      const apiUrl = '/api/resolve-hops?hops=' + encodeURIComponent(canonicalPath.join(','));
      const resp = await fetch(apiUrl, { cache: 'no-cache' });
      if (resp.ok) {
        const json = await resp.json();
        serverResolved = json && json.resolved ? json.resolved : null;
      }
    } catch (e) {}

    const raw = canonicalPath.map(hop => {
      const hopLower = String(hop).toLowerCase();
      const srv = serverResolved && (serverResolved[hop] || serverResolved[hopLower] || serverResolved[hop.toUpperCase()]);
      // hopCounts is keyed on the SHORT prefix from observation paths (e.g. "37"),
      // but canonicalPath may carry full pubkeys when the operator's selected
      // observation went through the resolver. Compute hop coverage by checking
      // EVERY path[]: a hop in canonicalPath is "covered" by a path if the path
      // contains an entry that is either an exact match OR a prefix of the hop's
      // full pubkey.
      let coverage = 0;
      const hopFullKey = (srv && srv.pubkey) ? srv.pubkey.toLowerCase() : hopLower;
      paths.forEach(pth => {
        const phops = (pth.path || []).map(h => String(h).toLowerCase());
        const matches = phops.some(ph => ph === hopFullKey || hopFullKey.startsWith(ph) || ph.startsWith(hopFullKey));
        if (matches) coverage++;
      });
      const obsInfo = { observerCount: coverage || (hopCounts[hop] || 1), observerTotal: totalObservers };
      if (srv && srv.pubkey) {
        const c = srv.candidates && srv.candidates[0];
        if (c && c.lat != null && c.lon != null && !(c.lat === 0 && c.lon === 0)) {
          return Object.assign({ lat: c.lat, lon: c.lon, name: srv.name || c.name || hop.slice(0,8), pubkey: srv.pubkey, role: c.role, resolved: true }, obsInfo);
        }
        return Object.assign({ name: srv.name || hop.slice(0,8), pubkey: srv.pubkey, role: (c && c.role) || null, resolved: false, gpsless: true }, obsInfo);
      }
      const allMatches = nodes.filter(n => {
        const pk = n.public_key.toLowerCase();
        return (pk === hopLower || pk.startsWith(hopLower) || hopLower.startsWith(pk));
      });
      const withGps = allMatches.filter(n => n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0));
      if (withGps.length >= 1) {
        const c = withGps[0];
        return Object.assign({ lat: c.lat, lon: c.lon, name: c.name || hop.slice(0,8), pubkey: c.public_key, role: c.role, resolved: true }, obsInfo);
      }
      if (allMatches.length >= 1) {
        const c = allMatches[0];
        return Object.assign({ name: c.name || hop.slice(0,8), pubkey: c.public_key, role: c.role, resolved: false, gpsless: true }, obsInfo);
      }
      return Object.assign({ name: String(hop).slice(0,8), pubkey: hop, resolved: false }, obsInfo);
    });

    if (raw.length < 1) return;
    if (origin && origin.pubkey) {
      const op = nodes.find(n => n.public_key.toLowerCase() === origin.pubkey.toLowerCase() ||
                                  n.public_key.toLowerCase().startsWith(origin.pubkey.toLowerCase()));
      if (op) {
        const hasGps = op.lat != null && op.lon != null && !(op.lat === 0 && op.lon === 0);
        if (hasGps) {
          raw.unshift({ lat: op.lat, lon: op.lon, name: origin.name || op.name || 'Sender', pubkey: op.public_key, role: op.role || 'companion', resolved: true, isOrigin: true, observerCount: totalObservers, observerTotal: totalObservers });
        } else {
          raw.unshift({ name: origin.name || op.name || 'Sender', pubkey: op.public_key, role: op.role || 'companion', resolved: false, gpsless: true, isOrigin: true, observerCount: totalObservers, observerTotal: totalObservers });
        }
      } else if (origin.name) {
        raw.unshift({ name: origin.name, pubkey: origin.pubkey, role: 'companion', resolved: false, isOrigin: true, observerCount: totalObservers, observerTotal: totalObservers });
      }
    }
    // #1418 Phase D: append destination (recipient from decoded.destHash) so
    // the route view shows: sender → [intermediate hops] → recipient.
    // GPS-sanity: a lat=0,lon=0 sentinel or null coords means "no real GPS"
    // — show the node in the sidebar as gpsless rather than drawing it at
    // (0,0) which would force fitBounds to span SF→Africa.
    if (opts.destination && opts.destination.pubkey) {
      const dp = nodes.find(n => n.public_key.toLowerCase() === opts.destination.pubkey.toLowerCase() ||
                                  n.public_key.toLowerCase().startsWith(opts.destination.pubkey.toLowerCase()));
      if (dp) {
        const hasGps = dp.lat != null && dp.lon != null && !(dp.lat === 0 && dp.lon === 0);
        if (hasGps) {
          raw.push({ lat: dp.lat, lon: dp.lon, name: opts.destination.name || dp.name || 'Recipient', pubkey: dp.public_key, role: dp.role || 'companion', resolved: true, observerCount: totalObservers, observerTotal: totalObservers });
        } else {
          raw.push({ name: opts.destination.name || dp.name || 'Recipient', pubkey: dp.public_key, role: dp.role || 'companion', resolved: false, gpsless: true, observerCount: totalObservers, observerTotal: totalObservers });
        }
      }
    }
    raw[raw.length - 1].isDest = true;
    if (raw[0]) raw[0].isOrigin = true;

    if (routeLayer) routeLayer.clearLayers();

    if (window.MeshRouteView && typeof window.MeshRouteView.render === 'function') {
      window.MeshRouteView.render(map, routeLayer, raw, {
        timestamp: opts.timestamp || Date.now(),
        multiPath: true,
        totalObservers: totalObservers,
        edgeCounts: edgeCounts,
        packetHash: opts.packetHash || null,
        observationId: opts.observationId || null,
        allPaths: paths,
        packetContext: opts.packetContext || null
      });
    }
  }
  window.drawPacketRouteMulti = drawPacketRouteMulti;

  // #1418/#1419: deep-link loader. Reads URL params
  //   #/map?packet=<hash>&obs=<observation_id>
  // fetches the packet+observations from /api/packets/<hash>, picks the
  // selected observation as canonical, and dispatches the same payload that
  // the packets-page 'View on map' button would have set in sessionStorage.
  // Without an obs param, the first observation is used.
  async function loadRouteFromDeepLink() {
    try {
      const hash = location.hash || '';
      const qs = hash.split('?')[1];
      if (!qs) return;
      const params = new URLSearchParams(qs);
      const packetHash = params.get('packet');
      const obsId = params.get('obs');
      if (!packetHash) return;
      // Wait for nodes to load (drawPacketRoute / Multi rely on `nodes` array
      // for the local-fallback resolver).
      if (!nodes || !nodes.length) {
        await new Promise(r => setTimeout(r, 600));
      }
      const resp = await fetch('/api/packets/' + encodeURIComponent(packetHash));
      if (!resp.ok) {
        console.warn('[deep-link] /api/packets/' + packetHash + ' returned ' + resp.status);
        return;
      }
      const data = await resp.json();
      const pkt = data.packet || data;
      const observations = data.observations || pkt.observations || [];
      if (!observations.length) return;
      // Pick the user-chosen observation by id, fall back to first
      let chosen = null;
      if (obsId) chosen = observations.find(o => String(o.id) === String(obsId));
      if (!chosen) chosen = observations[0];
      // Parse decoded for src/dst.
      // Try observation first, fall back to packet-level decoded_json (GRP_TXT
      // / TRACE packets often have channel + content at the packet level, not
      // per-observation).
      let decoded = {};
      try {
        const obsDec = JSON.parse(chosen.decoded_json || '{}');
        const pktDec = JSON.parse(pkt.decoded_json || '{}');
        decoded = Object.keys(obsDec).length ? obsDec : pktDec;
        // If observation has some fields but missing channel/text, merge from packet.
        if (decoded === obsDec && pktDec.channel) {
          decoded = Object.assign({}, pktDec, obsDec);
        }
      } catch (_) {}
      const origin = {};
      if (decoded.pubKey) origin.pubkey = decoded.pubKey;
      else if (decoded.srcHash) origin.pubkey = decoded.srcHash;
      if (decoded.adName || decoded.name) origin.name = decoded.adName || decoded.name;
      const destination = decoded.destHash ? { pubkey: decoded.destHash } : null;
      // Resolve the chosen observation's hops (canonical path).
      // Priority: 1) server-side `resolved_path` (authoritative, eyeball-
      // validated against packet detail), 2) client-side HopResolver (used
      // by packets.js — does observer-IATA-aware geographic disambiguation),
      // 3) raw prefixes (worst case, leaves naive lookup to drawPacketRoute).
      let chosenPath = [];
      let rawHops = [];
      try { rawHops = JSON.parse(chosen.path_json || '[]'); } catch (_) {}
      let resolvedHops = null;
      try {
        if (chosen.resolved_path) {
          resolvedHops = typeof chosen.resolved_path === 'string' ? JSON.parse(chosen.resolved_path) : chosen.resolved_path;
        }
      } catch (_) {}
      if (Array.isArray(resolvedHops) && resolvedHops.length === rawHops.length) {
        chosenPath = rawHops.map((h, i) => resolvedHops[i] || h);
      } else if (window.HopResolver && typeof window.HopResolver.resolve === 'function' && rawHops.length) {
        // Use the SAME resolver the packets page uses, so route view and
        // packet detail agree on hop identities.
        try {
          // Sender + observer hints help disambiguation
          const senderLat = decoded.lat || decoded.latitude || null;
          const senderLon = decoded.lon || decoded.longitude || null;
          let obsLat = null, obsLon = null;
          // Try to find observer's coords for geographic affinity scoring
          if (chosen.observer_id && Array.isArray(nodes)) {
            const obs = nodes.find(n => (n.public_key || '').toLowerCase() === String(chosen.observer_id).toLowerCase());
            if (obs && obs.lat != null && obs.lon != null) { obsLat = obs.lat; obsLon = obs.lon; }
          }
          // HopResolver.init may already have been done by packets.js; if not,
          // do a minimal init from window data we have.
          if (!window.HopResolver.ready || !window.HopResolver.ready()) {
            try {
              window.HopResolver.init(nodes || [], { observers: [], iataCoords: {} });
            } catch (_) {}
          }
          const resolveResult = window.HopResolver.resolve(rawHops, senderLat, senderLon, obsLat, obsLon, chosen.observer_id);
          chosenPath = rawHops.map(h => {
            const r = resolveResult ? resolveResult[h] : null;
            return r && r.pubkey ? r.pubkey : h;
          });
        } catch (_) {
          chosenPath = rawHops;
        }
      } else {
        chosenPath = rawHops;
      }
      // All observation paths for multi-path stroke weighting
      const allPaths = observations.map(o => {
        let p = [];
        try { p = JSON.parse(o.path_json || '[]'); } catch (_) {}
        return { path: p, observer: o.observer_name, observer_id: o.observer_id, snr: o.snr, rssi: o.rssi };
      }).filter(p => p.path && p.path.length > 0);
      // #1418 Phase Y: derive packet context for the sidebar fact-list.
      // type comes from decoded.type or pkt.payload_type. Resolve src/dst
      // names from the nodes table when possible.
      function resolveNameByHash(h) {
        if (!h || !nodes || !nodes.length) return null;
        const hL = String(h).toLowerCase();
        const m = nodes.find(n => n.public_key.toLowerCase().startsWith(hL));
        return m ? m.name : null;
      }
      // Map payload_type byte → string. Source: cmd/ingestor/decoder.go.
      // 0=REQ, 1=RESPONSE, 2=TXT_MSG, 3=ACK, 4=ADVERT, 5=GRP_TXT,
      // 6=GRP_DATA, 7=ANON_REQ, 8=PATH, 9=TRACE, 10=MULTIPART,
      // 11=CONTROL, 12=RAW_CUSTOM.
      const PAYLOAD_TYPE_MAP = {
        0: 'REQ', 1: 'RESPONSE', 2: 'TXT_MSG', 3: 'ACK', 4: 'ADVERT',
        5: 'GRP_TXT', 6: 'GRP_DATA', 7: 'ANON_REQ', 8: 'PATH',
        9: 'TRACE', 10: 'MULTIPART', 11: 'CONTROL', 12: 'RAW_CUSTOM'
      };
      const inferredType = decoded.type || PAYLOAD_TYPE_MAP[pkt.payload_type] || 'OTHER';
      // Try to peek at raw_hex bytes to extract src/destHash when decoded is empty.
      // TXT_MSG/REQ/RESPONSE/ANON_REQ all have the same wire layout:
      //   byte0=route+type, byte1=path_len, then path bytes, then destHash + srcHash + encrypted body.
      // ANON_REQ doesn't have a srcHash byte (sender is anonymous), only destHash.
      let inferredSrc = decoded.srcHash || null;
      let inferredDst = decoded.destHash || null;
      const TYPES_WITH_DST_SRC = [1, 2, 7, 8]; // RESPONSE=1, TXT_MSG=2, ANON_REQ=7, PATH=8 — all carry hashes
      if ((!inferredSrc || !inferredDst) && chosen.raw_hex && TYPES_WITH_DST_SRC.indexOf(pkt.payload_type) >= 0) {
        try {
          const hex = chosen.raw_hex;
          // MeshCore wire max for path length is 64 hops. Cap defensively:
          // a crafted ingest packet with pathLen=200 would slice random body
          // bytes into the srcHash/destHash UI fields. Guard before use.
          const pathLen = parseInt(hex.slice(2, 4), 16);
          if (!Number.isFinite(pathLen) || pathLen < 0 || pathLen > 64) {
            throw new Error('pathLen out of range: ' + pathLen);
          }
          const destOff = 4 + pathLen * 2;
          if (hex.length >= destOff + 2) {
            inferredDst = inferredDst || hex.slice(destOff, destOff + 2).toUpperCase();
            // ANON_REQ has no srcHash
            if (pkt.payload_type !== 7 && hex.length >= destOff + 4) {
              inferredSrc = inferredSrc || hex.slice(destOff + 2, destOff + 4).toUpperCase();
            }
          }
        } catch (_) {}
      }
      // GRP_TXT: channel_hash byte sits right after path bytes in raw_hex.
      // Layout: byte0=route+type, byte1=path_len, path bytes, channel_hash, encrypted body.
      let inferredChannelHash = decoded.channelHashHex || null;
      if (!inferredChannelHash && chosen.raw_hex && pkt.payload_type === 5) {
        try {
          const hex = chosen.raw_hex;
          const pathLen = parseInt(hex.slice(2, 4), 16);
          if (!Number.isFinite(pathLen) || pathLen < 0 || pathLen > 64) {
            throw new Error('pathLen out of range: ' + pathLen);
          }
          const chOff = 4 + pathLen * 2;
          if (hex.length >= chOff + 2) {
            inferredChannelHash = hex.slice(chOff, chOff + 2).toUpperCase();
          }
        } catch (_) {}
      }
      const pktCtx = {
        type: inferredType,
        decoded: Object.assign({}, decoded, { srcHash: inferredSrc, destHash: inferredDst, channelHashHex: inferredChannelHash || decoded.channelHashHex }),
        payloadType: pkt.payload_type || null,
        srcResolvedName: inferredSrc ? resolveNameByHash(inferredSrc) : null,
        destResolvedName: inferredDst ? resolveNameByHash(inferredDst) : null,
        observedHops: chosenPath.length,
        observationCount: observations.length
      };
      // GRP_TXT: try to resolve the channel name from /api/channels.
      if (inferredType === 'GRP_TXT' && inferredChannelHash) {
        try {
          const chResp = await fetch('/api/channels?includeEncrypted=true');
          if (chResp.ok) {
            const chData = await chResp.json();
            const chList = chData.channels || [];
            const wantUp = inferredChannelHash.toUpperCase();
            // Match by:
            //   1) hash field starts with target (full hex hash)
            //   2) hash == "enc_<HEX>" (no-key fallback channels)
            //   3) name contains "0x<HEX>" (encrypted placeholder)
            //   4) for keyed channels: compute SHA256(name)[0] === target byte
            //      (browser SubtleCrypto — async)
            let match = chList.find(c => {
              const ch = String(c.hash || '').toUpperCase();
              const nm = String(c.name || '').toUpperCase();
              return ch.startsWith(wantUp) ||
                     ch === 'ENC_' + wantUp ||
                     nm.includes('0X' + wantUp);
            });
            // If not matched and we have SubtleCrypto, try SHA256 lookup
            if (!match && window.crypto && window.crypto.subtle) {
              for (const c of chList) {
                if (c.encrypted) continue; // skip the enc_ placeholders
                const name = c.name || '';
                if (!name) continue;
                try {
                  const buf = new TextEncoder().encode(name);
                  const hashBuf = await window.crypto.subtle.digest('SHA-256', buf);
                  const arr = new Uint8Array(hashBuf);
                  const byteHex = arr[0].toString(16).padStart(2, '0').toUpperCase();
                  if (byteHex === wantUp) { match = c; break; }
                } catch (_) {}
              }
            }
            if (match) {
              const isEnc = !!match.encrypted || /^enc_/i.test(match.hash || '');
              pktCtx.channelName = isEnc ? ('Encrypted (0x' + inferredChannelHash + ')') : (match.name || '#' + inferredChannelHash);
              pktCtx.channelEncrypted = isEnc;
              if (match.lastMessage && match.lastMessage !== 'Encrypted — click to decrypt') {
                pktCtx.channelLastMessage = match.lastMessage;
              }
            }
          }
        } catch (_) {}
        if (!pktCtx.channelName) {
          pktCtx.channelName = 'channel 0x' + inferredChannelHash;
        }
        // Fetch decrypted text for this specific packet (only if not encrypted-only)
        if (!pktCtx.channelEncrypted) {
          try {
            const cleanName = (pktCtx.channelName || '').replace(/^#/, '');
            const msgResp = await fetch('/api/channels/' + encodeURIComponent(cleanName) + '/messages?limit=10');
            if (msgResp.ok) {
              const msgs = await msgResp.json();
              const m = (msgs.messages || []).find(mm => mm.packet_hash === packetHash || mm.hash === packetHash);
              if (m && (m.text || m.plainText)) pktCtx.decryptedText = m.text || m.plainText;
            }
          } catch (_) {}
        }
      }
      if (allPaths.length === 0) {
        // Polish review (doshi #1423): operator clicked a ?packet=<hash> URL
        // but every observation had an empty path. Previously this bailed
        // silently (map stayed where it was, no message). Surface a console
        // breadcrumb + a brief toast so the operator knows why nothing
        // rendered.
        console.warn('[deep-link] packet ' + packetHash + ' has no observed route data (all observations had empty path)');
        try {
          var toast = document.createElement('div');
          toast.className = 'mc-rt-toast';
          toast.setAttribute('role', 'status');
          toast.setAttribute('aria-live', 'polite');
          toast.style.cssText = 'position:fixed;top:80px;left:50%;transform:translateX(-50%);' +
            'background:var(--mc-bg-secondary,#1a1a1a);color:var(--mc-text-primary,#e5e5e5);' +
            'padding:10px 16px;border-radius:6px;box-shadow:0 4px 16px rgba(0,0,0,0.4);' +
            'z-index:10000;font:13px/1.4 system-ui,sans-serif;max-width:80vw;text-align:center;';
          toast.textContent = 'Packet has no observed route data.';
          document.body.appendChild(toast);
          setTimeout(function () { try { toast.remove(); } catch (_) {} }, 4000);
        } catch (e) { console.warn('[deep-link] toast failed:', e); }
        return;
      }
      if (allPaths.length === 1) {
        drawPacketRoute(chosenPath, origin, { destination: destination, packetHash: packetHash, observationId: obsId, packetContext: pktCtx });
      } else {
        drawPacketRouteMulti(allPaths, origin, {
          packetHash: packetHash,
          observationId: obsId,
          canonicalPath: chosenPath,
          destination: destination,
          packetContext: pktCtx
        });
      }
    } catch (e) {
      console.warn('[deep-link] route load failed', e);
    }
  }

  async function loadNodes() {
    try {
      // Load regions from config + observed IATAs
      try { REGION_NAMES = await api('/config/regions', { ttl: 3600 }); } catch {}

      const aqs = AreaFilter.areaQueryString();
      // Paginate past the server's per-request node cap (listLimits.nodesMax)
      // so actively-relaying repeaters that last advertised hours ago still
      // appear instead of being truncated by the top-N window. See fetchAllNodes.
      const data = await fetchAllNodes(`&lastHeard=${filters.lastHeard}${aqs}`, { ttl: CLIENT_TTL.nodeList });
      nodes = data.nodes || [];

      // Load observers for jump buttons + map markers
      const obsData = await api('/observers', { ttl: CLIENT_TTL.observers });
      observers = obsData.observers || [];

      buildRoleChecks(data.counts || {});
      buildJumpButtons();

      renderMarkers();

      // Keep the Important Links overlay in sync on a full node reload: re-fetch
      // the neighbor-graph edges (not just re-render the stale cache) so the
      // overlay tracks the current graph + scores.
      if (topRoutesEdges && document.getElementById('mcTopRoutes')?.checked) loadTopRoutes();

      // Restore heatmap if previously enabled
      if (localStorage.getItem('meshcore-map-heatmap') === 'true') {
        toggleHeatmap(true);
      }

      // If navigated with ?node=PUBKEY, center on and highlight that node
      if (targetNodeKey) {
        const targetNode = nodes.find(n => n.public_key === targetNodeKey);
        if (targetNode && targetNode.lat && targetNode.lon) {
          map.setView([targetNode.lat, targetNode.lon], 14);
          // Delay popup open slightly — Leaflet needs the map to settle after setView
          setTimeout(() => {
            let found = false;
            const findIn = function (layer) {
              if (found || !layer || !layer.eachLayer) return;
              layer.eachLayer(m => {
                if (found) return;
                if (m._nodeKey === targetNodeKey && m.openPopup) {
                  m.openPopup();
                  found = true;
                }
              });
            };
            findIn(markerLayer);
            if (!found) findIn(clusterGroup);
            if (!found) console.warn('[map] Target node marker not found:', targetNodeKey);
          }, 500);
        }
      }

      // Check for pending path inspector route (cross-page navigation from Path Inspector).
      if (window._pendingPathInspectorRoute) {
        var pending = window._pendingPathInspectorRoute;
        delete window._pendingPathInspectorRoute;
        if (pending.path && pending.path.length > 0) {
          if (window.routeLayer) window.routeLayer.clearLayers();
          // Pass full path as hopKeys; null origin (origin is already the first
          // hop). slice(1) + path[0] string was wrong — drawPacketRoute expects
          // origin to be an OBJECT with pubkey/lat/lon, and stripping the head
          // hid the originating node from the route polyline.
          drawPacketRoute(pending.path, null);
        }
      }

      // Wire up map side pane (Path Inspector embedded - spec §2.7).
      initMapSidePane();

      // Don't fitBounds on initial load — respect the Bay Area default or saved view
      // Only fitBounds on subsequent data refreshes if user hasn't manually panned
    } catch (e) {
      console.error('Map load error:', e);
    } finally {
      // Always signal data-loaded — even on error — so E2E tests can proceed.
      // Otherwise an api() failure leaves the test waiting forever.
      var mapContainer = document.getElementById('leaflet-map');
      if (mapContainer) mapContainer.setAttribute('data-loaded', 'true');
    }
  }

  function buildRoleChecks(counts) {
    const el = document.getElementById('mcRoleChecks');
    if (!el) return;
    el.innerHTML = '';
    const nodePubkeys = new Set(nodes.map(n => (n.public_key || '').toLowerCase()));
    const obsCount = observers.filter(o => o.lat && o.lon && !(o.id && nodePubkeys.has(o.id.toLowerCase()))).length;
    const roles = ['repeater', 'companion', 'room', 'sensor', 'observer'];
    const shapeMap = { repeater: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-diamond"/></svg>', companion: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg>', room: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-square-fill"/></svg>', sensor: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-triangle"/></svg>', observer: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star-fill"/></svg>' };

    // Count active/stale per role from loaded nodes
    const roleCounts = {};
    for (const role of roles) {
      roleCounts[role] = { active: 0, stale: 0 };
    }
    for (const n of nodes) {
      const role = (n.role || 'companion').toLowerCase();
      if (!roleCounts[role]) roleCounts[role] = { active: 0, stale: 0 };
      const status = getNodeStatus(n); // #1598: relay-aware for infra
      roleCounts[role][status]++;
    }

    for (const role of roles) {
      const cbId = 'mcRole_' + role;
      const lbl = document.createElement('label');
      lbl.setAttribute('for', cbId);
      const shape = shapeMap[role] || '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg>';
      let countStr;
      if (role === 'observer') {
        countStr = `(${obsCount})`;
      } else {
        const rc = roleCounts[role] || { active: 0, stale: 0 };
        const isInfra = role === 'repeater' || role === 'room';
        const thresh = isInfra ? '72h' : '24h';
        const activeTip = 'Active \u2014 heard within the last ' + thresh;
        const staleTip = 'Stale \u2014 not heard for over ' + thresh;
        countStr = `(<span title="${activeTip}">${rc.active} active</span>, <span title="${staleTip}">${rc.stale} stale</span>)`;
      }
      lbl.innerHTML = `<input type="checkbox" id="${cbId}" data-role="${role}" ${filters[role] ? 'checked' : ''}> <span style="color:${ROLE_COLORS[role]};font-weight:600;" aria-hidden="true">${shape}</span> ${ROLE_LABELS[role]} <span style="color:var(--text-muted)">${countStr}</span>`;
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
  var _currentMarkerData = []; // stored marker data for zoom-only repositioning
  var _observerByPubkey = new Map(); // observer id (pubkey) → observer object, rebuilt on each render
  var _zoomResizeTimer = null;

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

  /**
   * Create, update, or remove the offset indicator (dashed line + dot at true GPS position)
   * for a deconflicted marker. Shared by _renderMarkersInner and _repositionMarkers.
   * @param {Object} m - marker data object with latLng, adjustedLatLng, offset, _leafletLine, _leafletDot
   * @param {L.LayerGroup} layer - layer group to add/remove indicators from
   */
  function _updateOffsetIndicator(m, layer) {
    var pos = m.adjustedLatLng || m.latLng;
    var redColor = getComputedStyle(document.documentElement).getPropertyValue('--status-red').trim() || '#ef4444';

    if (m.offset > 10) {
      // Line from true position to adjusted position
      if (m._leafletLine) {
        m._leafletLine.setLatLngs([m.latLng, pos]);
      } else {
        m._leafletLine = L.polyline([m.latLng, pos], {
          color: redColor, weight: 2, dashArray: '6,4', opacity: 0.85
        });
        layer.addLayer(m._leafletLine);
      }
      // Dot at true GPS position
      if (!m._leafletDot) {
        m._leafletDot = L.circleMarker(m.latLng, {
          radius: 3, fillColor: redColor, fillOpacity: 0.9, stroke: true, color: '#fff', weight: 1
        });
        layer.addLayer(m._leafletDot);
      }
    } else {
      // No offset — remove indicator if it existed
      if (m._leafletLine) { layer.removeLayer(m._leafletLine); m._leafletLine = null; }
      if (m._leafletDot) { layer.removeLayer(m._leafletDot); m._leafletDot = null; }
    }
  }

  /**
   * Reposition existing markers by re-running deconfliction at the current zoom.
   * Avoids clearing and rebuilding all markers — eliminates flicker on zoom/resize.
   */
  function _repositionMarkers() {
    if (!map || _currentMarkerData.length === 0) return;
    // Markercluster handles its own re-layout on zoom/move — skip our deconfliction
    // dance when clustering is on.
    if (filters.clustering) return;
    map.invalidateSize({ animate: false });

    // Re-run deconfliction with current zoom pixel coordinates
    deconflictLabels(_currentMarkerData, map);

    for (var i = 0; i < _currentMarkerData.length; i++) {
      var m = _currentMarkerData[i];
      var pos = m.adjustedLatLng || m.latLng;

      // Update marker position
      if (m._leafletMarker) m._leafletMarker.setLatLng(pos);

      _updateOffsetIndicator(m, markerLayer);
    }
  }

  function renderMarkers() {
    if (_renderingMarkers) return;
    _renderingMarkers = true;
    try { _renderMarkersInner(); } finally { _renderingMarkers = false; }
  }

  function _renderMarkersInner() {
    // Don't clear what doesn't exist (map first loading will throw null error)
    if (markerLayer) {
      markerLayer.clearLayers();
    }
    if (clusterGroup) clusterGroup.clearLayers();
    _currentMarkerData = [];

    const filtered = nodes.filter(n => {
      if (!n.lat || !n.lon) return false;
      if (!filters[n.role || 'companion']) return false;
      // Byte size filter (applies only to repeaters). A node with no observed
      // size is its own bucket — folding it into "1-byte" made that bucket a
      // mix of measured and merely-unheard nodes.
      if (filters.byteSize !== 'all' && (n.role || 'companion') === 'repeater') {
        const hi = hashPrefixInfo(n);
        if (filters.byteSize === 'unknown') {
          if (hi.known) return false;
        } else if (!hi.known || String(hi.bytes) !== filters.byteSize) {
          return false;
        }
      }
      // Status filter
      if (filters.statusFilter !== 'all') {
        const status = getNodeStatus(n); // #1598: relay-aware for infra
        if (status !== filters.statusFilter) return false;
      }
      // Neighbor filter: show only the reference node and its direct neighbors
      if (filters.neighbors && selectedReferenceNode && neighborPubkeys) {
        const pk = n.public_key;
        if (pk !== selectedReferenceNode && !neighborPubkeys.has(pk)) return false;
      }
      return true;
    });

    const allMarkers = [];

    // Build a set of observer public keys for quick lookup
    _observerByPubkey = new Map();
    for (const obs of observers) {
      if (obs.id) _observerByPubkey.set(obs.id.toLowerCase(), obs);
    }

    for (const node of filtered) {
      const lastSeenTime = node.last_heard || node.last_seen;
      const isStale = getNodeStatus(node) === 'stale'; // #1598: relay-aware for infra
      const pk = (node.public_key || '').toLowerCase();
      const isAlsoObserver = _observerByPubkey.has(pk);
      const useLabel = node.role === 'repeater' && filters.hashLabels;
      // #1356 V3: multi-byte status is no longer encoded by label fill color.
      // Pass the raw status string to the label icon (it picks glyph + CSS class);
      // marker-dot tinting (for non-label rendering) keeps a colorblind-safe hex.
      var mbStatus = null;
      var mbColor = null;
      if (filters.multiByteOverlay && node.role === 'repeater') {
        mbStatus = node.multi_byte_status || 'unknown';
        // Marker-dot tint (module-scope MB_MARKER_TINT) — high-luminance accent
        // set kept in sync with --mc-mb-* CSS stripes so label + marker agree.
        mbColor = MB_MARKER_TINT[mbStatus] || MB_MARKER_TINT.unknown;
      }
      const icon = useLabel ? makeRepeaterLabelIcon(node, isStale, isAlsoObserver, mbStatus) : makeMarkerIcon(node.role || 'companion', isStale, isAlsoObserver, mbColor);
      const latLng = L.latLng(node.lat, node.lon);
      allMarkers.push({ latLng, node, icon, isLabel: useLabel, popupFn: function() { return buildPopup(node); }, alt: (node.name || 'Unknown') + ' (' + (node.role || 'node') + (isAlsoObserver ? ' + observer' : '') + ')' });
    }

    // Add observer markers (skip observers already represented as a node marker)
    // Build set of node pubkeys that are displayed on the map
    const displayedNodePubkeys = new Set(filtered.map(n => (n.public_key || '').toLowerCase()));
    if (filters.observer) {
      for (const obs of observers) {
        if (!obs.lat || !obs.lon) continue;
        // Skip observers whose pubkey matches a displayed node — they're shown as combined markers
        if (obs.id && displayedNodePubkeys.has(obs.id.toLowerCase())) continue;
        const icon = makeMarkerIcon('observer');
        const latLng = L.latLng(obs.lat, obs.lon);
        allMarkers.push({ latLng, node: obs, icon, isLabel: false, popupFn: function() { return buildObserverPopup(obs); }, alt: (obs.name || obs.id || 'Unknown') + ' (observer)' });
      }
    }

    // Ensure map has correct pixel dimensions before deconfliction
    // (SPA navigation may render markers before container is fully sized)
    map.invalidateSize({ animate: false });

    // Deconflict ALL markers — but only when clustering is OFF.
    // When clustering is ON, markercluster handles overlap collapse and
    // deconfliction would just waste CPU + draw offset polylines we don't want.
    if (allMarkers.length > 0 && !filters.clustering) {
      deconflictLabels(allMarkers, map);
    }

    // Store marker data for zoom/resize repositioning (avoids full rebuild)
    _currentMarkerData = allMarkers;

    var useCluster = filters.clustering && clusterGroup;
    var clusterMarkers = [];
    for (const m of allMarkers) {
      const pos = (useCluster ? m.latLng : (m.adjustedLatLng || m.latLng));
      const marker = L.marker(pos, { icon: m.icon, alt: m.alt });
      marker._nodeKey = m.node.public_key || m.node.id || null;
      marker._role = (m.node && m.node.role) || 'companion';
      marker.bindPopup(m.popupFn(), { maxWidth: 280 });
      m._leafletMarker = marker;
      m._leafletLine = null;
      m._leafletDot = null;

      if (useCluster) {
        clusterMarkers.push(marker);
      } else {
        markerLayer.addLayer(marker);
        _updateOffsetIndicator(m, markerLayer);
      }
    }
    if (useCluster && clusterMarkers.length > 0) {
      clusterGroup.addLayers(clusterMarkers);
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
        <a href="#/observers/${encodeURIComponent(obs.id || obs.observer_id)}" style="display:block;margin-top:8px;font-size:12px;color:var(--link-color);">View Detail →</a>
      </div>`;
  }

  async function selectReferenceNode(pubkey, name) {
    selectedReferenceNode = pubkey;
    neighborPubkeys = new Set();
    try {
      // Use affinity-based neighbor API (server-side disambiguation) instead of
      // client-side path walking which fails on hash collisions (#484)
      const data = await api('/nodes/' + pubkey + '/neighbors?min_count=3');
      for (const n of (data.neighbors || [])) {
        if (n.pubkey) neighborPubkeys.add(n.pubkey);
        // For ambiguous edges, include all candidates (better to show extra than miss)
        if (n.candidates) n.candidates.forEach(function(c) { if (c.pubkey) neighborPubkeys.add(c.pubkey); });
      }
      // If affinity data is insufficient, fall back to client-side path walking
      if (neighborPubkeys.size === 0) {
        const pathData = await api('/nodes/' + pubkey + '/paths');
        const paths = pathData.paths || [];
        for (const p of paths) {
          const hops = p.hops || [];
          for (var i = 0; i < hops.length; i++) {
            if (hops[i].pubkey === pubkey) {
              if (i > 0 && hops[i - 1].pubkey) neighborPubkeys.add(hops[i - 1].pubkey);
              if (i < hops.length - 1 && hops[i + 1].pubkey) neighborPubkeys.add(hops[i + 1].pubkey);
            }
          }
        }
      }
    } catch (e) {
      console.warn('Failed to fetch neighbors for', pubkey, ':', e);
      neighborPubkeys = new Set();
    }
    // Update sidebar UI
    const refEl = document.getElementById('mcNeighborRef');
    const refNameEl = document.getElementById('mcNeighborRefName');
    const hintEl = document.getElementById('mcNeighborHint');
    if (refEl) { refEl.style.display = 'block'; }
    if (refNameEl) { refNameEl.textContent = name || pubkey.slice(0, 8); }
    if (hintEl) { hintEl.style.display = 'none'; }
    // Auto-enable the neighbors filter
    filters.neighbors = true;
    const cb = document.getElementById('mcNeighbors');
    if (cb) cb.checked = true;
    renderMarkers();
  }
  // Event delegation for Show Neighbors links (avoids inline onclick / global function timing issues)
  document.addEventListener('click', function(e) {
    var link = e.target.closest('[data-show-neighbors]');
    if (link) {
      e.preventDefault();
      selectReferenceNode(link.dataset.pubkey, link.dataset.name);
    }
  });
  // Expose for testing
  window._mapSelectRefNode = selectReferenceNode;
  window._mapGetNeighborPubkeys = function() { return neighborPubkeys ? Array.from(neighborPubkeys) : []; };

  function buildPopup(node) {
    const key = node.public_key ? truncate(node.public_key, 16) : '—';
    const loc = (node.lat && node.lon) ? `${node.lat.toFixed(5)}, ${node.lon.toFixed(5)}` : '—';
    const lastAdvert = node.last_seen ? timeAgo(node.last_seen) : '—';
    const roleBadge = `<span style="display:inline-block;padding:2px 8px;border-radius:12px;font-size:11px;font-weight:600;background:${ROLE_COLORS[node.role] || '#4b5563'};color:#fff;">${(node.role || 'unknown').toUpperCase()}</span>`;
    // Check if this node is also an observer (combined repeater+observer)
    const matchingObs = node.public_key ? _observerByPubkey.get(node.public_key.toLowerCase()) : null;
    const obsBadge = matchingObs ? ` <span style="display:inline-block;padding:2px 8px;border-radius:12px;font-size:11px;font-weight:600;background:${ROLE_COLORS.observer || '#f1c40f'};color:#fff;">OBSERVER</span>` : '';
    // Unknown width is reported as unknown, not as 1 — same wording the node
    // detail page uses (nodes.js), so the two views can't disagree.
    const hashInfo = hashPrefixInfo(node);
    const hashPrefixValue = hashInfo.known
      ? `${safeEsc(hashInfo.prefix)} <span style="font-weight:400;color:var(--text-muted);">(${hashInfo.bytes}B)</span>`
      : `<span style="font-weight:400;color:var(--text-muted);">Unknown</span>`;
    const hashPrefixRow = `<dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Hash Prefix</dt>
          <dd style="font-family:var(--mono);font-size:11px;font-weight:700;margin-left:88px;padding:2px 0;">${hashPrefixValue}</dd>`;
    // Multi-byte support indicator for repeaters
    var mbRow = '';
    if (node.role === 'repeater' && node.multi_byte_status) {
      var mbLabel = { confirmed: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> Confirmed', suspected: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Suspected', unknown: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x-circle"/></svg> Unknown' }[node.multi_byte_status] || node.multi_byte_status;
      var mbEvidence = node.multi_byte_evidence ? ' (' + node.multi_byte_evidence + ')' : '';
      mbRow = '<dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Multi-byte</dt>' +
        '<dd style="margin-left:88px;padding:2px 0;font-size:12px;">' + mbLabel + mbEvidence + '</dd>';
    }

    return `
      <div class="map-popup" style="font-family:var(--font);min-width:180px;">
        <h3 style="font-weight:700;font-size:14px;margin:0 0 4px;">${safeEsc(node.name || 'Unknown')}</h3>
        ${roleBadge}${obsBadge}
        <dl style="margin-top:8px;font-size:12px;">
          ${hashPrefixRow}
          ${mbRow}
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Key</dt>
          <dd style="font-family:var(--mono);font-size:11px;margin-left:88px;padding:2px 0;">${safeEsc(key)}</dd>
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Location</dt>
          <dd style="margin-left:88px;padding:2px 0;">${loc}</dd>
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Last Advert</dt>
          <dd style="margin-left:88px;padding:2px 0;">${lastAdvert}</dd>
          <dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">Adverts</dt>
          <dd style="margin-left:88px;padding:2px 0;">${node.advert_count || 0}</dd>
        </dl>
        <div style="margin-top:8px;clear:both;">
          <a href="#/nodes/${node.public_key}" style="color:var(--link-color);font-size:12px;">View Node →</a>
          ${node.public_key ? ` · <a href="javascript:void(0)" role="button" data-show-neighbors data-pubkey="${escapeHtml(node.public_key)}" data-name="${escapeHtml(node.name || 'Unknown')}" style="color:var(--link-color);font-size:12px;cursor:pointer;">Show Neighbors</a>` : ''}
        </div>
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

  // === Map Side Pane — Path Inspector (spec §2.7) ===
  function initMapSidePane() {
    var pane = document.getElementById('mapSidePane');
    var toggle = document.getElementById('mapPaneToggle');
    var input = document.getElementById('mapPiInput');
    var btn = document.getElementById('mapPiSubmit');
    if (!pane || !toggle) return;

    toggle.addEventListener('click', function () {
      pane.classList.toggle('expanded');
      // #1648 M4: Phosphor sprite glyph for pane toggle (was ▶/◀). // EMOJI-OK: comment
      toggle.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#' +
        (pane.classList.contains('expanded') ? 'ph-caret-right' : 'ph-caret-left') + '"/></svg>';
      // Invalidate map size after transition.
      setTimeout(function () { if (map) map.invalidateSize(); }, 220);
    });

    if (btn && input) {
      btn.addEventListener('click', function () { mapPiSubmit(input.value); });
      input.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') mapPiSubmit(input.value);
      });
    }

    // Auto-open if URL has prefixes param while on map.
    var params = new URLSearchParams(location.hash.split('?')[1] || '');
    var prefixParam = params.get('prefixes');
    if (prefixParam && input) {
      pane.classList.add('expanded');
      toggle.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-right"/></svg>';
      input.value = prefixParam;
      setTimeout(function () { if (map) map.invalidateSize(); }, 220);
      mapPiSubmit(prefixParam);
    }
  }

  function mapPiSubmit(raw) {
    var errDiv = document.getElementById('mapPiError');
    var resultsDiv = document.getElementById('mapPiResults');
    if (!errDiv || !resultsDiv) return;
    errDiv.textContent = '';
    resultsDiv.innerHTML = '';

    // Reuse PathInspector validation if available.
    var prefixes = raw.trim().split(/[\s,]+/).filter(function (s) { return s.length > 0; }).map(function (s) { return s.toLowerCase(); });
    var err = (window.PathInspector && window.PathInspector.validatePrefixes) ? window.PathInspector.validatePrefixes(prefixes) : null;
    if (!err && prefixes.length === 0) err = 'Enter at least one prefix.';
    if (err) { errDiv.textContent = err; return; }

    resultsDiv.innerHTML = '<p style="font-size:12px;">Loading...</p>';
    fetch('/api/paths/inspect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ prefixes: prefixes })
    })
      .then(function (r) {
        if (r.status === 503) return r.json().then(function () { throw new Error('Service warming up, retry shortly.'); });
        if (!r.ok) return r.json().then(function (d) { throw new Error(d.error || 'Request failed'); });
        return r.json();
      })
      .then(function (data) { renderMapPiResults(data, resultsDiv); })
      .catch(function (e) { resultsDiv.innerHTML = ''; errDiv.textContent = e.message; });
  }

  function renderMapPiResults(data, div) {
    if (!data.candidates || data.candidates.length === 0) {
      div.innerHTML = '<p style="font-size:12px;color:var(--text-muted);">No candidates found.</p>';
      return;
    }
    var html = '<table class="path-inspector-table" style="font-size:11px;width:100%;"><thead><tr><th>#</th><th>Score</th><th>Path</th><th></th></tr></thead><tbody>';
    for (var i = 0; i < data.candidates.length; i++) {
      var c = data.candidates[i];
      var rowClass = c.speculative ? 'speculative-row' : '';
      html += '<tr class="' + rowClass + '">';
      html += '<td>' + (i + 1) + '</td>';
      html += '<td class="' + (c.speculative ? 'speculative-warning' : '') + '">' + c.score.toFixed(2) + (c.speculative ? ' <svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>' : '') + '</td>';
      html += '<td title="' + safeEsc(c.names.join(' → ')) + '">' + safeEsc(c.names.slice(0, 3).join('→')) + (c.names.length > 3 ? '…' : '') + '</td>';
      html += '<td><button class="btn btn-sm" data-idx="' + i + '" title="Show on Map" aria-label="Show on Map"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-pin"/></svg></button></td>';
      html += '</tr>';
      // Per-hop evidence (collapsed).
      html += '<tr class="evidence-row collapsed" data-evidence="' + i + '"><td colspan="4"><div class="evidence-detail" style="font-size:10px;">';
      if (c.evidence && c.evidence.perHop) {
        for (var j = 0; j < c.evidence.perHop.length; j++) {
          var h = c.evidence.perHop[j];
          html += '<div>Hop ' + (j+1) + ': ' + h.prefix + ' (×' + h.candidatesConsidered + ') w=' + h.edgeWeight.toFixed(2);
          if (h.alternatives && h.alternatives.length > 0) {
            html += ' <span style="color:var(--text-muted);">[+' + h.alternatives.length + ' alt]</span>';
          }
          html += '</div>';
        }
      }
      html += '</div></td></tr>';
    }
    html += '</tbody></table>';
    div.innerHTML = html;

    // Wire buttons.
    div.querySelectorAll('button[data-idx]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var idx = parseInt(btn.dataset.idx);
        var cand = data.candidates[idx];
        if (routeLayer) routeLayer.clearLayers();
        drawPacketRoute(cand.path, null);
      });
    });
    // Expand evidence on row click.
    div.querySelectorAll('.path-inspector-table tbody tr:not(.evidence-row)').forEach(function (row) {
      row.style.cursor = 'pointer';
      row.addEventListener('click', function (e) {
        if (e.target.tagName === 'BUTTON') return;
        var b = row.querySelector('button[data-idx]');
        if (!b) return;
        var ev = div.querySelector('tr[data-evidence="' + b.dataset.idx + '"]');
        if (ev) ev.classList.toggle('collapsed');
      });
    });
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (map) {
      map.remove();
      map = null;
    }
    markerLayer = null;
    clusterGroup = null;
    _currentMarkerData = [];
    routeLayer = null;
    if (heatLayer) { heatLayer = null; }
    geoFilterLayer = null;
    selectedReferenceNode = null;
    neighborPubkeys = null;
    delete window._mapSelectRefNode;
    delete window._mapGetNeighborPubkeys;
  }

  function toggleHeatmap(on) {
    if (!on || !map) {
      if (heatLayer) { map.removeLayer(heatLayer); heatLayer = null; window._meshcoreHeatLayer = null; }
      return;
    }
    const points = nodes
      .filter(n => n.lat != null && n.lon != null)
      .map(n => {
        const weight = n.advert_count || 1;
        return [n.lat, n.lon, weight];
      });
    if (!points.length || typeof L.heatLayer !== 'function') return;
    var savedOpacity = parseFloat(localStorage.getItem('meshcore-heatmap-opacity'));
    if (isNaN(savedOpacity)) savedOpacity = 0.25;
    // Update existing layer data without recreating (avoids opacity flash)
    if (heatLayer) {
      heatLayer.setLatLngs(points);
      return;
    }
    heatLayer = L.heatLayer(points, {
      radius: 25, blur: 15, maxZoom: 14, minOpacity: 0.05,
      gradient: { 0.2: '#0d47a1', 0.4: '#1565c0', 0.6: '#42a5f5', 0.8: '#ffca28', 1.0: '#ff5722' }
    });
    // Set opacity on canvas BEFORE it's visible — hook the 'add' event
    heatLayer.on('add', function() {
      var canvas = heatLayer._canvas || (heatLayer.getContainer && heatLayer.getContainer());
      if (canvas) canvas.style.opacity = savedOpacity;
    });
    heatLayer.addTo(map);
    window._meshcoreHeatLayer = heatLayer;
  }

  let _themeRefreshHandler = null;

  // ─── Affinity Debug Overlay ────────────────────────────────────────────────
  function clearAffinityOverlay() {
    if (affinityLayer) { map.removeLayer(affinityLayer); affinityLayer = null; }
    affinityData = null;
  }

  function loadAffinityDebugOverlay() {
    clearAffinityOverlay();
    // Fetch debug data — requires API key stored in localStorage
    var apiKey = localStorage.getItem('meshcore-api-key') || '';
    fetch('/api/debug/affinity', { headers: { 'X-API-Key': apiKey } })
      .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
      .then(function (data) {
        affinityData = data;
        renderAffinityOverlay();
      })
      .catch(function (err) {
        console.warn('[affinity-debug] Failed to load:', err);
        var cb = document.getElementById('mcAffinityDebug');
        if (cb) cb.checked = false;
      });
  }

  function renderAffinityOverlay() {
    if (!affinityData || !map) return;
    clearAffinityOverlay();
    affinityLayer = L.layerGroup();

    // Build node position lookup from current markers
    var nodePos = {};
    nodes.forEach(function (n) {
      if (n.latitude && n.longitude) {
        nodePos[n.public_key.toLowerCase()] = [n.latitude, n.longitude];
      }
    });

    var edges = affinityData.edges || [];
    edges.forEach(function (e) {
      var posA = nodePos[e.nodeA];
      var posB = e.nodeB ? nodePos[e.nodeB] : null;

      if (!posA) return;

      // Unresolved prefix - show question marker near nodeA
      if (e.unresolved || (!posB && e.ambiguous)) {
        if (posA) {
          var marker = L.marker([posA[0] + 0.001, posA[1] + 0.001], {
            icon: L.divIcon({ html: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg>', className: 'affinity-unresolved', iconSize: [20, 20] })
          });
          marker.bindPopup('<b>Unresolved prefix:</b> ' + escapeHtml(e.prefix) + '<br>Observations: ' + e.weight);
          affinityLayer.addLayer(marker);
        }
        return;
      }

      if (!posB) return;

      // Color by confidence
      var color = '#ef4444'; // red — ambiguous
      var score = e.score || 0;
      if (score >= 0.6) color = '#22c55e'; // green — high
      else if (score >= 0.3) color = '#eab308'; // yellow — medium

      // Thickness proportional to weight, clamped 1-5px
      var weight = Math.max(1, Math.min(5, Math.round((e.weight || 1) / 20)));

      var line = L.polyline([posA, posB], {
        color: color,
        weight: weight,
        opacity: 0.7,
        dashArray: e.ambiguous ? '5,5' : null
      });

      var popup = '<b>Affinity Edge</b><br>' +
        escapeHtml(e.nodeAName || e.nodeA.substring(0, 8)) + ' ↔ ' + escapeHtml(e.nodeBName || e.nodeB.substring(0, 8)) + '<br>' +
        'Observations: ' + e.observationCount + '<br>' +
        'Score: ' + (e.score || 0).toFixed(3) + '<br>' +
        'Last seen: ' + escapeHtml(e.lastSeen) + '<br>' +
        'Observers: ' + escapeHtml((e.observers || []).join(', '));
      if (e.avgSnr != null) popup += '<br>Avg SNR: ' + e.avgSnr.toFixed(1) + ' dB';

      line.bindPopup(popup);
      affinityLayer.addLayer(line);
    });

    affinityLayer.addTo(map);
  }
  // ─── End Affinity Debug ────────────────────────────────────────────────────

  // ─── Important Links (B-weighted top-routes overlay, issue #672 / D) ─────────
  // A public, user-facing overlay (NOT the API-key-gated Affinity Debug above)
  // that draws the most IMPORTANT affinity links on the map, weighted by the
  // #672 repeater-usefulness axes. It joins the loaded `nodes` array (coords +
  // per-node usefulness/bridge/redundancy/traffic scores from /api/nodes) with
  // the public neighbor-graph edges, ranks by a chosen axis, and draws the
  // top-N as weighted polylines so terrain-level chokepoints (the sole link
  // across a valley) stand out geographically.
  const TOP_ROUTES_AXES = {
    usefulness: 'usefulness_score',
    bridge: 'bridge_score',
    redundancy: 'redundancy_score',
    traffic: 'traffic_share_score',
  };

  // computeTopRouteEdges is the pure ranking core (no DOM/Leaflet): join edges
  // with node coords + the chosen axis score, compute per-edge importance, and
  // return the top-N drawable edges (both endpoints geo-located). Importance =
  // edge affinity × the mean of the two endpoints' axis score; for axis
  // 'affinity' it is the raw edge affinity. Edges with a missing endpoint coord,
  // or zero importance (e.g. a non-repeater endpoint with no axis score), are
  // dropped. Exported shape kept simple for behavioral testing.
  function computeTopRouteEdges(edges, nodeList, axis, topN) {
    const pos = {}, score = {};
    const scoreField = TOP_ROUTES_AXES[axis]; // undefined for 'affinity'
    (nodeList || []).forEach(n => {
      if (!n || !n.public_key) return;
      const k = n.public_key.toLowerCase();
      if (n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0)) pos[k] = [n.lat, n.lon];
      if (scoreField) score[k] = (n[scoreField] != null ? n[scoreField] : 0);
    });
    const scored = [];
    (edges || []).forEach(e => {
      const a = (e.source || '').toLowerCase();
      const b = (e.target || '').toLowerCase();
      const pa = pos[a], pb = pos[b];
      if (!pa || !pb) return; // need both endpoints on the map
      const edgeStrength = e.score != null ? e.score : 0;
      const importance = scoreField
        ? edgeStrength * (((score[a] || 0) + (score[b] || 0)) / 2)
        : edgeStrength;
      if (!(importance > 0)) return;
      scored.push({ a, b, pa, pb, importance, edge: e });
    });
    scored.sort((x, y) => y.importance - x.importance);
    return scored.slice(0, Math.max(0, Math.floor(Number(topN) || 0)));
  }

  function clearTopRoutes() {
    if (topRoutesLayer) { map.removeLayer(topRoutesLayer); topRoutesLayer = null; }
  }

  async function loadTopRoutes() {
    try {
      const data = await api('/analytics/neighbor-graph?min_count=1&min_score=0', { ttl: CLIENT_TTL.analyticsRF });
      topRoutesEdges = (data && data.edges) || [];
      renderTopRoutes();
    } catch (err) {
      console.warn('[top-routes] failed to load neighbor graph:', err);
      const cb = document.getElementById('mcTopRoutes');
      if (cb) cb.checked = false;
    }
  }

  // topRoutesAccent caches --accent (read once, invalidated on theme-refresh)
  // so the slider re-render loop doesn't hit getComputedStyle every frame.
  function topRoutesAccent() {
    if (!topRoutesAccentCache) {
      topRoutesAccentCache = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() || '#4a9eff';
    }
    return topRoutesAccentCache;
  }

  function renderTopRoutes() {
    if (!map || !topRoutesEdges) return;
    clearTopRoutes();
    const axis = (document.getElementById('mcTopRoutesRankBy') || {}).value || 'usefulness';
    const topN = parseInt((document.getElementById('mcTopRoutesN') || {}).value, 10) || 50;
    const top = computeTopRouteEdges(topRoutesEdges, nodes, axis, topN);
    // Empty-state hint: the toggle is on but nothing rendered (no geo-located
    // endpoints, or the chosen axis has no scores yet).
    const hint = document.getElementById('mcTopRoutesHint');
    if (hint) hint.style.display = top.length ? 'none' : '';
    topRoutesLayer = L.layerGroup();
    if (top.length) {
      const maxImp = top[0].importance || 1;
      const nameByPk = {};
      nodes.forEach(n => { if (n && n.public_key) nameByPk[n.public_key.toLowerCase()] = n.name || n.public_key.slice(0, 8); });
      const accent = topRoutesAccent();
      top.forEach(t => {
        const rel = maxImp > 0 ? t.importance / maxImp : 0;
        const line = L.polyline([t.pa, t.pb], {
          color: accent,
          weight: 1 + rel * 6,      // 1–7px ∝ importance
          opacity: 0.25 + rel * 0.55 // 0.25–0.8 ∝ importance
        });
        const e = t.edge;
        line.bindPopup('<b>Important link</b><br>' +
          escapeHtml(nameByPk[t.a] || t.a.slice(0, 8)) + ' ↔ ' + escapeHtml(nameByPk[t.b] || t.b.slice(0, 8)) + '<br>' +
          'Importance (' + escapeHtml(axis) + '): ' + t.importance.toFixed(3) + '<br>' +
          'Affinity: ' + (e.score != null ? e.score.toFixed(3) : '—') +
          (e.avg_snr != null ? '<br>Avg SNR: ' + e.avg_snr.toFixed(1) + ' dB' : ''));
        topRoutesLayer.addLayer(line);
      });
    }
    topRoutesLayer.addTo(map);
  }
  // ─── End Important Links ─────────────────────────────────────────────────────

  registerPage('map', {
    init: function(app, routeParam) {
      _themeRefreshHandler = () => {
        if (markerLayer) renderMarkers();
        // Re-read --accent on theme change; redraw the overlay if it's on.
        topRoutesAccentCache = '';
        if (topRoutesEdges && document.getElementById('mcTopRoutes')?.checked) renderTopRoutes();
      };
      window.addEventListener('theme-refresh', _themeRefreshHandler);
      return init(app, routeParam);
    },
    destroy: function() {
      if (_themeRefreshHandler) { window.removeEventListener('theme-refresh', _themeRefreshHandler); _themeRefreshHandler = null; }
      return destroy();
    }
  });

  // ── Marker clustering (issue #1036) ──
  // Wraps Leaflet.markercluster with CoreScope-themed cluster icons + sane perf
  // defaults for large meshes (target: smooth pan/zoom @ 2k nodes on mid mobile).
  function isMobileForClustering() {
    try {
      return /Android|iPhone|iPad|iPod|Mobile/i.test(navigator.userAgent || '');
    } catch (_) { return false; }
  }
  function createClusterGroup() {
    if (typeof L === 'undefined' || typeof L.markerClusterGroup !== 'function') {
      console.warn('[map] L.markerClusterGroup not loaded — clustering disabled');
      return null;
    }
    return L.markerClusterGroup({
      chunkedLoading: true,
      chunkInterval: 100,
      chunkDelay: 25,
      removeOutsideVisibleBounds: true,
      maxClusterRadius: 60,
      spiderfyOnMaxZoom: true,
      spiderfyDistanceMultiplier: 1.5,
      showCoverageOnHover: false,
      zoomToBoundsOnClick: true,
      disableClusteringAtZoom: 16,
      animate: !isMobileForClustering(),
      animateAddingMarkers: false,
      iconCreateFunction: makeClusterIcon,
    });
  }

  function makeClusterIcon(cluster) {
    var markers = cluster.getAllChildMarkers();
    var counts = { repeater: 0, companion: 0, room: 0, sensor: 0, observer: 0 };
    for (var i = 0; i < markers.length; i++) {
      var r = markers[i]._role || 'companion';
      if (counts[r] == null) counts[r] = 0;
      counts[r] += 1;
    }
    var total = (typeof cluster.getChildCount === 'function') ? cluster.getChildCount() : markers.length;
    var bucket = total >= 100 ? 'lg' : total >= 30 ? 'md' : 'sm';
    var roleOrder = ['repeater', 'companion', 'room', 'sensor', 'observer'];
    // #1356 V2: pill background uses the --mc-role-* Wong palette (CSS var),
    // pill text is the role letter (primary, monochrome-safe carrier).
    // #1407: pill text COLOR now comes from --mc-role-X-text (set by
    // cb-presets.js + style.css :root defaults), paired per-bg to clear
    // WCAG 1.4.3 AA on every preset (achromat / trit needed the flip).
    var ROLE_BG_VAR = {
      repeater:  'var(--mc-role-repeater)',
      companion: 'var(--mc-role-companion)',
      room:      'var(--mc-role-room)',
      sensor:    'var(--mc-role-sensor)',
      observer:  'var(--mc-role-observer)',
    };
    var ROLE_TEXT_VAR = {
      repeater:  'var(--mc-role-repeater-text, #1a1a1a)',
      companion: 'var(--mc-role-companion-text, #1a1a1a)',
      room:      'var(--mc-role-room-text, #1a1a1a)',
      sensor:    'var(--mc-role-sensor-text, #1a1a1a)',
      observer:  'var(--mc-role-observer-text, #1a1a1a)',
    };
    var pillsHtml = '';
    var tooltipParts = [];
    var pillsShown = 0;
    for (var j = 0; j < roleOrder.length; j++) {
      var role = roleOrder[j];
      var n = counts[role] || 0;
      if (n <= 0) continue;
      tooltipParts.push(n + ' ' + role + (n === 1 ? '' : 's'));
      if (pillsShown < 4) {
        var bg = ROLE_BG_VAR[role] || 'var(--mc-role-companion)';
        var fg = ROLE_TEXT_VAR[role] || 'var(--mc-role-companion-text, #1a1a1a)';
        var letter = ROLE_LETTERS[role] || '?';
        // #1360 follow-up: cap 4+ digit counts as "999+" to bound pill width.
        // Defense-in-depth: .mc-pill CSS also enforces max-width + ellipsis.
        if (n > 999) n = '999+';
        pillsHtml += '<span class="mc-pill role-' + role + '" ' +
                     'role="img" aria-label="' + n + ' ' + role + (n === 1 ? '' : 's') + '" ' +
                     'style="background:' + bg + ';color:' + fg + '" ' +
                     'title="' + n + ' ' + role + (n === 1 ? '' : 's') + '">' +
                     letter + n + '</span>';
        pillsShown += 1;
      }
    }
    // #1356 V1: cluster gets role="img" + an aria-label summarising the
    // count and per-role breakdown so screen readers announce the data.
    var ariaLabel = total + ' nodes — ' + tooltipParts.join(', ');
    var html = '<div class="mc-cluster mc-' + bucket + '" ' +
                 'role="img" aria-label="' + ariaLabel + '">' +
                 '<b class="mc-count" aria-hidden="true">' + total + '</b>' +
                 '<div class="mc-pills" aria-hidden="true">' + pillsHtml + '</div>' +
               '</div>';
    var icon = L.divIcon({
      html: html,
      className: 'mc-cluster-wrap mc-' + bucket,
      iconSize: L.point(48, 48),
    });
    // Stash a tooltip string for callers that want to bindTooltip (markercluster
    // does not natively pipe this through, but it's available via cluster icon
    // for E2E inspection).
    icon._tooltip = ariaLabel;
    return icon;
  }

  if (typeof window !== 'undefined') {
    window.__meshcoreMapInternals = {
      createClusterGroup: createClusterGroup,
      makeClusterIcon: makeClusterIcon,
      // #1356: exposed so the a11y test can assert what the label RENDERS
      // instead of grepping map.js for where two identifiers sit.
      makeRepeaterLabelIcon: makeRepeaterLabelIcon,
    };
  }
})();
