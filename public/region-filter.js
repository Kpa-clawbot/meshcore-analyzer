/* === MeshCore Analyzer — region-filter.js (shared region filter component) === */
'use strict';

(function () {
  var LS_KEY = 'meshcore-region-filter';
  var _regions = {};       // { code: label }
  var _selected = null;    // Set of selected region codes, null = all
  var _listeners = [];
  var _loaded = false;

  function loadFromStorage() {
    try {
      var stored = JSON.parse(localStorage.getItem(LS_KEY));
      if (Array.isArray(stored) && stored.length > 0) return new Set(stored);
    } catch (e) { /* ignore */ }
    return null; // null = all selected
  }

  function saveToStorage() {
    if (!_selected) {
      localStorage.removeItem(LS_KEY);
    } else {
      localStorage.setItem(LS_KEY, JSON.stringify(Array.from(_selected)));
    }
  }

  _selected = loadFromStorage();

  /** Fetch regions from server */
  async function fetchRegions() {
    if (_loaded) return _regions;
    try {
      var data = await fetch('/api/config/regions').then(function (r) { return r.json(); });
      _regions = data || {};
      _loaded = true;
      // If stored selection has codes no longer valid, clean up
      if (_selected) {
        var codes = Object.keys(_regions);
        var cleaned = new Set();
        _selected.forEach(function (c) { if (codes.includes(c)) cleaned.add(c); });
        _selected = cleaned.size > 0 ? cleaned : null;
        saveToStorage();
      }
    } catch (e) {
      _regions = {};
    }
    return _regions;
  }

  /** Get selected regions as array, or null if all */
  function getSelected() {
    if (!_selected || _selected.size === 0) return null;
    return Array.from(_selected);
  }

  /** Get region query param string for API calls: "SJC,SFO" or empty */
  function getRegionParam() {
    var sel = getSelected();
    return sel ? sel.join(',') : '';
  }

  /** Build query string fragment: "&region=SJC,SFO" or "" */
  function regionQueryString() {
    var p = getRegionParam();
    return p ? '&region=' + encodeURIComponent(p) : '';
  }

  /** Handle a region toggle (shared logic for both pill and dropdown modes) */
  function toggleRegion(region, codes, container) {
    if (region === '__all__') {
      _selected = null;
    } else {
      if (!_selected) {
        _selected = new Set([region]);
      } else if (_selected.has(region)) {
        _selected.delete(region);
        if (_selected.size === 0) _selected = null;
      } else {
        _selected.add(region);
      }
      if (_selected && _selected.size === codes.length) _selected = null;
    }
    saveToStorage();
    render(container);
    _listeners.forEach(function (fn) { fn(getSelected()); });
  }

  /** Build summary label for dropdown trigger */
  function dropdownLabel(codes) {
    if (!_selected) return 'All Regions';
    var sel = Array.from(_selected);
    if (sel.length === 0) return 'All Regions';
    if (sel.length <= 2) return sel.map(function (c) { return _regions[c] || c; }).join(', ');
    return sel.length + ' Regions';
  }

  /** Render pill bar mode (≤4 regions) */
  function renderPills(container, codes) {
    var allSelected = !_selected;
    var html = '<div class="region-filter-bar" role="group" aria-label="Region filter">';
    html += '<span class="region-filter-label" id="region-filter-label">Region:</span>';
    html += '<button class="region-pill' + (allSelected ? ' region-pill-active' : '') +
      '" data-region="__all__" role="checkbox" aria-checked="' + allSelected + '">All</button>';
    codes.forEach(function (code) {
      var label = _regions[code] || code;
      var active = allSelected || (_selected && _selected.has(code));
      html += '<button class="region-pill' + (active ? ' region-pill-active' : '') +
        '" data-region="' + code + '" role="checkbox" aria-checked="' + !!active + '">' + label + '</button>';
    });
    html += '</div>';
    container.innerHTML = html;

    container.onclick = function (e) {
      var btn = e.target.closest('[data-region]');
      if (!btn) return;
      toggleRegion(btn.dataset.region, codes, container);
    };
  }

  /** Render dropdown mode (>4 regions) */
  function renderDropdown(container, codes) {
    var allSelected = !_selected;
    var html = '<div class="region-dropdown-wrap" role="group" aria-label="Region filter">';
    html += '<button class="region-dropdown-trigger" aria-haspopup="listbox" aria-expanded="false">' +
      dropdownLabel(codes) + ' ▾</button>';
    html += '<div class="region-dropdown-menu" role="listbox" aria-label="Select regions" hidden>';
    html += '<label class="region-dropdown-item"><input type="checkbox" data-region="__all__"' +
      (allSelected ? ' checked' : '') + '> <strong>All</strong></label>';
    codes.forEach(function (code) {
      var label = _regions[code] || code;
      var active = allSelected || (_selected && _selected.has(code));
      html += '<label class="region-dropdown-item"><input type="checkbox" data-region="' + code + '"' +
        (active ? ' checked' : '') + '> ' + label + '</label>';
    });
    html += '</div></div>';
    container.innerHTML = html;

    var trigger = container.querySelector('.region-dropdown-trigger');
    var menu = container.querySelector('.region-dropdown-menu');

    trigger.onclick = function () {
      var open = !menu.hidden;
      menu.hidden = open;
      trigger.setAttribute('aria-expanded', String(!open));
    };

    menu.onchange = function (e) {
      var input = e.target;
      if (!input.dataset.region) return;
      toggleRegion(input.dataset.region, codes, container);
    };

    // Close on outside click
    function onDocClick(e) {
      if (!container.contains(e.target)) {
        menu.hidden = true;
        trigger.setAttribute('aria-expanded', 'false');
      }
    }
    document.addEventListener('click', onDocClick, true);
    container._regionCleanup = function () {
      document.removeEventListener('click', onDocClick, true);
    };
  }

  /** Render the filter bar into a container element */
  function render(container) {
    // Clean up previous outside-click listener if any
    if (container._regionCleanup) { container._regionCleanup(); container._regionCleanup = null; }

    var codes = Object.keys(_regions);
    if (codes.length < 2) {
      container.innerHTML = '';
      container.style.display = 'none';
      return;
    }
    container.style.display = '';

    if (codes.length > 4 || container._forceDropdown) {
      renderDropdown(container, codes);
    } else {
      renderPills(container, codes);
    }
  }

  /** Subscribe to selection changes. Callback receives selected array or null */
  function onChange(fn) {
    _listeners.push(fn);
    return fn;
  }

  /** Unsubscribe */
  function offChange(fn) {
    _listeners = _listeners.filter(function (f) { return f !== fn; });
  }

  /** Initialize filter in a container, fetch regions, render, return promise.
   *  Options: { dropdown: true } to force dropdown mode regardless of region count */
  async function initFilter(container, opts) {
    if (opts && opts.dropdown) container._forceDropdown = true;
    await fetchRegions();
    render(container);
  }

  // Expose globally
  window.RegionFilter = {
    init: initFilter,
    render: render,
    getSelected: getSelected,
    getRegionParam: getRegionParam,
    regionQueryString: regionQueryString,
    onChange: onChange,
    offChange: offChange,
    fetchRegions: fetchRegions
  };
})();
