/* map-tile-providers.js — Registry of map tile providers & runtime switcher (#1165/#1420).
 *
 * Scope:
 *   - Multiple providers: Carto (default), OSM, Stamen, Esri.
 *   - Carto requires an API key since 2026-08. `tiles.providers.carto.key` is
 *     appended as `?key=`; without it Carto returns watermarked tiles.
 *   - MC_setDarkTileProvider(id) / MC_setLightTileProvider(id) persist per-browser
 *     to localStorage and dispatch `mc-tile-provider-changed`.
 *   - Resolves localStorage → server default → 'carto-dark' / 'carto-light'.
 *   - MC_applyTileFilter() applies/clears the CSS filter on
 *     `.leaflet-tile-pane` based on current theme + selected provider.
 *   - MC_createLayerControl() builds a Leaflet control mapping providers.
 *
 * No new deps — URL-only providers.
 */
(function () {
  'use strict';

  var STORAGE_KEY  = 'mc-dark-tile-provider';
  var STORAGE_KEY_LIGHT = 'mc-light-tile-provider';

  var DEFAULT_ID   = 'carto-dark';
  var DEFAULT_ID_LIGHT = 'carto-light';
  var EVENT_NAME   = 'mc-tile-provider-changed';
  
  var _serverDefault = null;
  var _serverDefaultLight = null;
  
  var INVERT_CSS   = 'invert(1) hue-rotate(180deg) brightness(0.9) contrast(1.05)';

  var _cfg = null;

  var _getCartoBase = function() { return (_cfg && _cfg.providers && _cfg.providers.carto && _cfg.providers.carto.domain) ? 'https://{s}.' + _cfg.providers.carto.domain + '.cartocdn.com' : 'https://{s}.basemaps.cartocdn.com'; };
  // Carto started requiring an API key in 2026-08: unauthenticated tiles come back
  // stamped "API KEY REQUIRED". The key is a query param on the same host, so it is
  // appended to every Carto style URL. Free keys: https://carto.com/basemaps/apikey
  // NOTE: like the OSM/Stamen tokens above, this reaches the browser - restrict it
  // by domain in the Carto dashboard.
  var _getCartoKey = function() { return (_cfg && _cfg.providers && _cfg.providers.carto && _cfg.providers.carto.key) ? '?key=' + encodeURIComponent(_cfg.providers.carto.key) : ''; };
  var _getStamenUrl = function() { return 'https://tiles.stadiamaps.com/tiles/stamen_toner_lite/{z}/{x}/{y}{r}.png' + ((_cfg && _cfg.providers && _cfg.providers.stamen && _cfg.providers.stamen.token) ? '?api_key=' + encodeURIComponent(_cfg.providers.stamen.token) : ''); };
  var _getOsmUrl = function() {
    if (_cfg && _cfg.providers && _cfg.providers.osm && _cfg.providers.osm.provider && _cfg.providers.osm.token) {
      var prov = _cfg.providers.osm.provider.toLowerCase();
      var key = encodeURIComponent(_cfg.providers.osm.token);
      if (prov === 'thunderforest') return 'https://{s}.tile.thunderforest.com/osm-carto/{z}/{x}/{y}.png?apikey=' + key;
      if (prov === 'maptiler') return 'https://api.maptiler.com/maps/streets/{z}/{x}/{y}.png?key=' + key;
      if (prov === 'mapbox') return 'https://api.mapbox.com/styles/v1/mapbox/streets-v11/tiles/256/{z}/{x}/{y}@2x?access_token=' + key;
    }
    return 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png';
  };

  var BASE_STYLES = {
    'carto-dark': { provider: 'carto', label: 'Carto Dark', url: function() { return _getCartoBase() + '/dark_all/{z}/{x}/{y}{r}.png' + _getCartoKey(); }, invertFilter: null, type: 'dark', attribution: '© OpenStreetMap © CartoDB', maxZoom: 19 },
    'carto-light': { provider: 'carto', label: 'Carto Positron', url: function() { return _getCartoBase() + '/light_all/{z}/{x}/{y}{r}.png' + _getCartoKey(); }, invertFilter: null, type: 'light', attribution: '© OpenStreetMap © CartoDB', maxZoom: 19 },
    'carto-voyager': { provider: 'carto', label: 'Carto Voyager', url: function() { return _getCartoBase() + '/rastertiles/voyager/{z}/{x}/{y}{r}.png' + _getCartoKey(); }, invertFilter: null, type: 'light', attribution: '© OpenStreetMap © CartoDB', maxZoom: 19 },
    'carto-voyager-dark': { provider: 'carto', label: 'Carto Voyager', url: function() { return _getCartoBase() + '/rastertiles/voyager/{z}/{x}/{y}{r}.png' + _getCartoKey(); }, invertFilter: INVERT_CSS, type: 'dark', attribution: '© OpenStreetMap © CartoDB', maxZoom: 19 },
    'positron-dark': { provider: 'carto', label: 'Carto Positron', url: function() { return _getCartoBase() + '/light_all/{z}/{x}/{y}{r}.png' + _getCartoKey(); }, invertFilter: INVERT_CSS, type: 'dark', attribution: '© OpenStreetMap © CartoDB', maxZoom: 19 },
    'osm-standard': { provider: 'osm', label: 'OSM Standard', url: _getOsmUrl, invertFilter: null, type: 'light', attribution: '© OpenStreetMap contributors, Maps © Mapbox/Thunderforest/MapTiler', maxZoom: 18 },
    'osm-dark': { provider: 'osm', label: 'OSM Standard', url: _getOsmUrl, invertFilter: INVERT_CSS, type: 'dark', attribution: '© OpenStreetMap contributors, Maps © Mapbox/Thunderforest/MapTiler', maxZoom: 18 },
    'opentopomap': { provider: 'opentopomap', label: 'OpenTopoMap', url: function() { return 'https://{s}.tile.opentopomap.org/{z}/{x}/{y}.png'; }, invertFilter: null, type: 'light', attribution: '© OpenStreetMap contributors, SRTM, © OpenTopoMap (CC-BY-SA)', maxZoom: 17 },
    'stamen-toner-lite': { provider: 'stamen', label: 'Stamen Toner Lite', url: _getStamenUrl, invertFilter: null, type: 'light', attribution: '© Stadia Maps © Stamen Design © OpenStreetMap', maxZoom: 20 },
    'stamen-toner-dark': { provider: 'stamen', label: 'Stamen Toner Lite', url: _getStamenUrl, invertFilter: INVERT_CSS, type: 'dark', attribution: '© Stadia Maps © Stamen Design © OpenStreetMap', maxZoom: 20 },
    'usgs-topo': { provider: 'usgs', label: 'USGS Topographic', url: function() { return 'https://basemap.nationalmap.gov/arcgis/rest/services/USGSTopo/MapServer/tile/{z}/{y}/{x}'; }, invertFilter: null, type: 'light', attribution: 'U.S. Geological Survey, National Geospatial Program', maxZoom: 16 },
    'usgs-imagery': { provider: 'usgs', label: 'USGS Imagery', url: function() { return 'https://basemap.nationalmap.gov/arcgis/rest/services/USGSImageryTopo/MapServer/tile/{z}/{y}/{x}'; }, invertFilter: null, type: 'light', attribution: 'U.S. Geological Survey, National Geospatial Program', maxZoom: 16 },
    'esri-darkgray-labels': { provider: 'esri', label: 'Esri Dark Gray Canvas', url: function() { return 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Base/MapServer/tile/{z}/{y}/{x}'; }, refUrl: 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Reference/MapServer/tile/{z}/{y}/{x}', invertFilter: null, type: 'dark', attribution: 'Tiles © Esri', maxZoom: 19 }
  };

  var REGISTRY = {};

  function _hasId(id) {
    return typeof id === 'string' && Object.prototype.hasOwnProperty.call(REGISTRY, id);
  }

  window.MC_initTileRegistry = function(fromAsync) {
    _cfg = (typeof window !== 'undefined' && window.MC_MAP_CFG && window.MC_MAP_CFG.tiles) ? window.MC_MAP_CFG.tiles : null;

    var HAS_CARTO = !_cfg || !_cfg.providers || !_cfg.providers.carto || _cfg.providers.carto.enabled !== false;
    var HAS_OSM = _cfg && _cfg.providers && _cfg.providers.osm && _cfg.providers.osm.enabled;
    var HAS_OPENTOPOMAP = _cfg && _cfg.providers && _cfg.providers.opentopomap && _cfg.providers.opentopomap.enabled;
    var HAS_USGS = _cfg && _cfg.providers && _cfg.providers.usgs && _cfg.providers.usgs.enabled;
    var HAS_STAMEN = _cfg && _cfg.providers && _cfg.providers.stamen && _cfg.providers.stamen.enabled && !!_cfg.providers.stamen.token;
    var HAS_ESRI = true; // Kept for backwards compatibility

    REGISTRY = {};
    for (var key in BASE_STYLES) {
      var style = BASE_STYLES[key];
      if (style.provider === 'carto' && HAS_CARTO) REGISTRY[key] = style;
      if (style.provider === 'osm' && HAS_OSM) REGISTRY[key] = style;
      if (style.provider === 'opentopomap' && HAS_OPENTOPOMAP) REGISTRY[key] = style;
      if (style.provider === 'stamen' && HAS_STAMEN) REGISTRY[key] = style;
      if (style.provider === 'usgs' && HAS_USGS) REGISTRY[key] = style;
      if (style.provider === 'esri' && HAS_ESRI) REGISTRY[key] = style;
    }

    // Keep the public reference in sync with the newly rebuilt REGISTRY
    window.MC_TILE_PROVIDERS = REGISTRY;

    if (_cfg && _cfg.darkDefault && _hasId(_cfg.darkDefault)) _serverDefault = _cfg.darkDefault;
    if (_cfg && _cfg.lightDefault && _hasId(_cfg.lightDefault)) _serverDefaultLight = _cfg.lightDefault;

    // When called after async config loads, notify maps to re-sync tiles
    if (fromAsync) {
      try {
        var ev = (typeof CustomEvent === 'function')
          ? new CustomEvent('mc-tile-provider-changed', { detail: { fromConfig: true } })
          : { type: 'mc-tile-provider-changed', detail: { fromConfig: true } };
        window.dispatchEvent(ev);
      } catch (_) {}
    }
  };

  // Run once immediately with whatever config is available at parse time
  window.MC_initTileRegistry();

  function _isDark() {
    try {
      var attr = document.documentElement.getAttribute('data-theme');
      if (attr === 'dark') return true;
      if (attr === 'light') return false;
      return !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
    } catch (_) { return false; }
  }

  
  function getActiveId() {
    try {
      var stored = window.localStorage && window.localStorage.getItem(STORAGE_KEY);
      if (_hasId(stored) && REGISTRY[stored].type === 'dark') return stored;
    } catch (_) {}
    if (_cfg && _cfg.darkDefault && _hasId(_cfg.darkDefault)) return _cfg.darkDefault;
    if (_hasId(_serverDefault)) return _serverDefault;
    return DEFAULT_ID;
  }

  function getActiveLightId() {
    try {
      var stored = window.localStorage && window.localStorage.getItem(STORAGE_KEY_LIGHT);
      if (_hasId(stored) && REGISTRY[stored].type === 'light') return stored;
    } catch (_) {}
    if (_cfg && _cfg.lightDefault && _hasId(_cfg.lightDefault)) return _cfg.lightDefault;
    if (_hasId(_serverDefaultLight)) return _serverDefaultLight;
    return DEFAULT_ID_LIGHT;
  }
  
  function setActive(id, type) {
    if (!_hasId(id)) return false;
    if (REGISTRY[id].type !== type) return false;
    var skey = type === 'light' ? STORAGE_KEY_LIGHT : STORAGE_KEY;
    try {
      if (window.localStorage) window.localStorage.setItem(skey, id);
    } catch (_) { }
    var detail = { id: id, provider: REGISTRY[id], type: type };
    try {
      var ev = (typeof CustomEvent === 'function')
        ? new CustomEvent(EVENT_NAME, { detail: detail })
        : { type: EVENT_NAME, detail: detail };
      window.dispatchEvent(ev);
    } catch (_) { }
    applyTileFilter();
    return true;
  }


  function setServerDefault(id) { if (_hasId(id)) _serverDefault = id; }
  function setServerDefaultLight(id) { if (_hasId(id)) _serverDefaultLight = id; }

  
  function _isDarkEffective() {
    return _isDark();
  }

  function applyTileFilter() {
    var pane;
    try { pane = document.querySelector('.leaflet-tile-pane'); } catch (_) { pane = null; }
    if (!pane || !pane.style) return;
    // Contract: if an explicit layer was selected via the map control, it locks
    // the CSS filter state. We bypass our auto-theme logic here so we don't accidentally
    // invert an explicitly chosen Light map while the app theme is Dark.
    if (pane.getAttribute('data-explicit-layer') === 'true') {
      return;
    }

    var isDark = _isDarkEffective();
    var id = isDark ? getActiveId() : getActiveLightId();
    var p  = REGISTRY[id];
    pane.style.filter = (isDark && p && p.invertFilter) ? p.invertFilter : '';
  }


  // ── Public surface ──────────────────────────────────────────────────────
  
  window.MC_DARK_TILE_DEFAULT           = DEFAULT_ID;
  window.MC_TILE_PROVIDERS              = REGISTRY; // initial ref; MC_initTileRegistry keeps this in sync
  window.MC_setDarkTileProvider         = function(id) { return setActive(id, 'dark'); };
  window.MC_setLightTileProvider        = function(id) { return setActive(id, 'light'); };
  window.MC_getDarkTileProvider         = getActiveId;
  window.MC_getLightTileProvider        = getActiveLightId;
  window.MC_setServerDefaultTileProvider = setServerDefault;
  window.MC_setServerDefaultLightTileProvider = setServerDefaultLight;
  window.MC_applyTileFilter             = applyTileFilter;

  /**
   * Resolve a registry id to a concrete tile-URL string, invoking function-typed
   * `url` builders so callers never hand L.tileLayer a function (#1614). Returns
   * `fallback` when the id isn't in the active registry (e.g. provider disabled).
   * Lets non-map pages request a specific style without duplicating the URL.
   */
  window.MC_tileUrlById = function (id, fallback) {
    var p = _hasId(id) ? REGISTRY[id] : null;
    var u = p && (p.url || p.baseUrl);
    if (!u) return fallback;
    return (typeof u === 'function') ? u() : u;
  };


  /**
   * Build and attach a Leaflet layer control listing "Auto (follows theme)"
   * at the top, then every enabled provider as an individual selectable base
   * layer. Selecting Auto delegates to the existing theme-synced tile group;
   * selecting a specific provider overrides it.
   *
   * @param {L.Map} map - The Leaflet map instance
   * @param {L.LayerGroup} autoLayerGroup - Existing group managed by _syncDarkTiles
   */
  window.MC_createLayerControl = function(map, autoLayerGroup, position) {
    if (typeof L === 'undefined') return null;
    
    // Set a default position just in case it isn't provided
    var controlPosition = position || 'topleft';

    var AUTO_KEY   = '__auto__';
    var AUTO_LABEL = ' <span title="Follows the app\'s current light/dark theme">Auto</span>';
    var _control   = null;  // current L.control.layers instance
    var _layerMap  = {};    // provider-id → L.tileLayer
    var _isAuto    = true;  // true when "Auto" is the active selection
    var _overrideLightId = null;
    var _overrideDarkId  = null;

    // Restore the auto tile group and kick _syncDarkTiles
    function _activateAuto() {
      _isAuto = true;
      try { 
        var pane = map.getPane('tilePane');
        if (pane) pane.removeAttribute('data-explicit-layer');
      } catch (_) {}
      try { if (autoLayerGroup && !map.hasLayer(autoLayerGroup)) map.addLayer(autoLayerGroup); } catch (_) {}
      
      try {
        var ev = (typeof CustomEvent === 'function')
          ? new CustomEvent('mc-tile-provider-changed', { detail: { auto: true } })
          : { type: 'mc-tile-provider-changed', detail: { auto: true } };
        window.dispatchEvent(ev);
      } catch (_) {}
      
      if (typeof applyTileFilter === 'function') applyTileFilter();
    }

    function _buildControl() {
      // Tear down existing control + explicit tile layers
      if (_control) { try { _control.remove(); } catch (_) {} _control = null; }
      Object.keys(_layerMap).forEach(function (id) {
        try { map.removeLayer(_layerMap[id]); } catch (_) {}
      });
      _layerMap = {};
      // Remove stale baselayerchange listeners by cloning off event (Leaflet re-adds on new control)
      if (map._mcBaselayerchangeHandler) {
        try { map.off('baselayerchange', map._mcBaselayerchangeHandler); } catch (_) {}
      }

      var isDark       = _isDarkEffective();
      var activeDarkId  = _overrideDarkId || getActiveId();
      var activeLightId = _overrideLightId || getActiveLightId();

      // Light providers first, then dark — natural grouping in the UI
      var lightIds = Object.keys(REGISTRY).filter(function (id) { return REGISTRY[id].type === 'light'; });
      var darkIds  = Object.keys(REGISTRY).filter(function (id) { return REGISTRY[id].type === 'dark';  });

      function _makeLayer(id) {
        var p   = REGISTRY[id];
        var url = typeof p.url === 'function' ? p.url() : p.url;
        var opts = { attribution: p.attribution || '', maxZoom: p.maxZoom || 19 };
        var layer = L.tileLayer(url, opts);

        // Two-layer providers (Esri Dark Gray Canvas) ship their place labels as
        // a separate transparent reference tileset that has to be stacked on the
        // base. The theme-synced path in map.js/live.js already does this; the
        // layer control did not, so picking such a provider explicitly produced
        // a map with no place names at all.
        if (p.refUrl && typeof L.layerGroup === 'function') {
          layer = L.layerGroup([layer, L.tileLayer(p.refUrl, opts)]);
        }
        
        // Every explicit layer enforces its own filter and locks the pane
        layer.on('add', function () {
          var pane = map.getPane('tilePane');
          if (pane) {
            pane.setAttribute('data-explicit-layer', 'true');
            pane.style.filter = p.invertFilter || ''; // Clears it if null!
          }
        });

        _layerMap[id] = layer;
        return layer;
      }

      // "Auto" entry is backed by the theme-synced autoLayerGroup
      var baseMaps = {};
      baseMaps[AUTO_KEY] = autoLayerGroup;

      lightIds.forEach(function (id) { baseMaps[id] = _makeLayer(id); });
      darkIds.forEach(function  (id) { baseMaps[id] = _makeLayer(id); });

      // Use the dynamic controlPosition variable instead of a hardcoded string
      _control = L.control.layers(baseMaps, null, { position: controlPosition }).addTo(map);

      // DOM surgery: map IDs to human-readable labels, inject separator, set data attributes.
      // We pass raw IDs to Leaflet as keys so e.name is the exact ID without label parsing.
      try {
        var firstDarkId = darkIds.length > 0 ? darkIds[0] : null;
        var labels = _control.getContainer().querySelectorAll('.leaflet-control-layers-base label');
        for (var i = 0; i < labels.length; i++) {
          var labelEl = labels[i];
          var spans = labelEl.querySelectorAll('span');
          if (spans.length === 0) continue;

          // Target the last span to avoid nuking Leaflet's radio <input>
          var span = spans[spans.length - 1]; 

          var id = span.textContent.trim();
          labelEl.setAttribute('data-tile-id', id);
          
          if (id === AUTO_KEY) {
            span.innerHTML = AUTO_LABEL;
          } else if (REGISTRY[id]) {
            span.innerHTML = ' ' + (REGISTRY[id].label || id);
          }
          
          if (id === firstDarkId && lightIds.length > 0) {
            var sep = document.createElement('div');
            sep.className = 'leaflet-control-layers-separator';
            labelEl.parentNode.insertBefore(sep, labelEl);
          }
        }
      } catch (_) {}

      // Decide initial active layer
      if (_isAuto) {
        // Ensure autoLayerGroup is on the map; explicit layers are off
        _activateAuto();
      } else {
        // Re-select whichever explicit provider was active before rebuild
        var prevId = isDark ? activeDarkId : activeLightId;
        var prevLayer = _layerMap[prevId];
        if (prevLayer) {
          map.addLayer(prevLayer);
          try { if (autoLayerGroup && map.hasLayer(autoLayerGroup)) map.removeLayer(autoLayerGroup); } catch (_) {}
        } else {
          _activateAuto(); // fallback
        }
      }

      map._mcBaselayerchangeHandler = function (e) {
        if (e.name === AUTO_KEY) {
          _activateAuto();
          return;
        }

        // Explicit provider selected — the name parameter is exactly our registry ID
        var selectedId = e.name;
        if (!REGISTRY[selectedId]) return;

        _isAuto = false;
        try { 
          var pane = map.getPane('tilePane');
          // Contract: mark the pane as explicit so applyTileFilter skips auto-theme CSS filters
          if (pane) pane.setAttribute('data-explicit-layer', 'true');
        } catch (_) {}

        // Hide autoLayerGroup while an explicit layer is active
        try { if (autoLayerGroup && map.hasLayer(autoLayerGroup)) map.removeLayer(autoLayerGroup); } catch (_) {}

        var p = REGISTRY[selectedId];
        if (p.type === 'light') _overrideLightId = selectedId;
        else                    _overrideDarkId  = selectedId;

        // CSS invert filter is handled by the layer's own add/remove events above
      };

      // Now attach the correctly assigned handler
      map.on('baselayerchange', map._mcBaselayerchangeHandler);

      return _control;
    }

    _buildControl();

    // Re-build when server config arrives async so newly-enabled providers appear
    window.addEventListener('mc-tile-provider-changed', function (e) {
      if (e && e.detail && e.detail.fromConfig) _buildControl();
    });

    // When the site theme flips, update the Auto tile via the existing _syncDarkTiles
    // path (map.js/live.js listen for mc-tile-provider-changed and call _syncDarkTiles).
    // Nothing extra needed here — autoLayerGroup is managed externally.

    return _control;
  };


  // ── Cross-tab sync ──────────────────────────────────────────────────────
  // If another tab in the same browser changes the provider, mirror the
  // dispatch + filter-apply here so live map.js / live.js swap tiles too.
  try {
    window.addEventListener('storage', function (e) {
      if (!e || (e.key !== STORAGE_KEY && e.key !== STORAGE_KEY_LIGHT)) return;
      if (!_hasId(e.newValue)) return;
      var detail = { id: e.newValue, provider: REGISTRY[e.newValue], crossTab: true };
      try {
        var ev = (typeof CustomEvent === 'function')
          ? new CustomEvent(EVENT_NAME, { detail: detail })
          : { type: EVENT_NAME, detail: detail };
        window.dispatchEvent(ev);
      } catch (_) { /* dispatch optional */ }
      applyTileFilter();
    });
  } catch (_) { /* addEventListener may not exist in some envs */ }
})();
