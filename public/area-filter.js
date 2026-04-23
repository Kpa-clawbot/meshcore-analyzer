/* === CoreScope — area-filter.js (single-select area filter component) === */
'use strict';

(function () {
  var LS_KEY = 'meshcore-area-filter';
  var _areas = [];       // [{key, label}, ...]
  var _selected = null;  // selected area key string, or null = all
  var _listeners = [];
  var _container = null;
  var _loaded = false;

  function loadFromStorage() {
    try {
      var v = localStorage.getItem(LS_KEY);
      if (v) return v;
    } catch (e) {}
    return null;
  }

  function saveToStorage() {
    if (!_selected) {
      localStorage.removeItem(LS_KEY);
    } else {
      localStorage.setItem(LS_KEY, _selected);
    }
  }

  _selected = loadFromStorage();

  async function fetchAreas() {
    if (_loaded) return _areas;
    try {
      var data = await fetch('/api/config/areas').then(function (r) { return r.json(); });
      _areas = Array.isArray(data) ? data : [];
      _loaded = true;
      if (_selected && !_areas.some(function (a) { return a.key === _selected; })) {
        _selected = null;
        saveToStorage();
      }
    } catch (e) {
      _areas = [];
    }
    return _areas;
  }

  function getSelected() { return _selected; }
  function getAreaParam() { return _selected || ''; }
  function areaQueryString() { return _selected ? '&area=' + encodeURIComponent(_selected) : ''; }

  function triggerLabel() {
    if (!_selected) return 'Area: All ▾';
    var area = _areas.find(function (a) { return a.key === _selected; });
    return 'Area: ' + (area ? area.label : _selected) + ' ▾';
  }

  function render(container) {
    if (_areas.length === 0) {
      container.innerHTML = '';
      container.style.display = 'none';
      return;
    }
    container.style.display = '';

    if (container._areaCleanup) { container._areaCleanup(); container._areaCleanup = null; }

    var html = '<div class="region-dropdown-wrap" role="group" aria-label="Area filter">';
    html += '<button class="region-dropdown-trigger" aria-haspopup="listbox" aria-expanded="false">' +
      triggerLabel() + '</button>';
    html += '<div class="region-dropdown-menu area-dropdown-menu" role="listbox" aria-label="Select area" hidden>';
    html += '<button class="region-dropdown-item area-dropdown-item' + (!_selected ? ' area-item-active' : '') +
      '" data-area="__all__">All</button>';
    _areas.forEach(function (a) {
      var active = _selected === a.key;
      html += '<button class="region-dropdown-item area-dropdown-item' + (active ? ' area-item-active' : '') +
        '" data-area="' + a.key + '">' + a.label + '</button>';
    });
    html += '</div></div>';
    container.innerHTML = html;

    var trigger = container.querySelector('.region-dropdown-trigger');
    var menu = container.querySelector('.area-dropdown-menu');

    trigger.onclick = function (e) {
      e.stopPropagation();
      var open = !menu.hidden;
      menu.hidden = open;
      trigger.setAttribute('aria-expanded', String(!open));
    };

    menu.onclick = function (e) {
      var btn = e.target.closest('[data-area]');
      if (!btn) return;
      _selected = (btn.dataset.area === '__all__') ? null : btn.dataset.area;
      saveToStorage();
      render(container);
      _listeners.forEach(function (fn) { fn(_selected); });
    };

    function onDocClick(e) {
      if (!container.contains(e.target)) {
        menu.hidden = true;
        trigger.setAttribute('aria-expanded', 'false');
      }
    }
    document.addEventListener('click', onDocClick, true);
    container._areaCleanup = function () {
      document.removeEventListener('click', onDocClick, true);
    };
  }

  function onChange(fn) { _listeners.push(fn); return fn; }
  function offChange(fn) { _listeners = _listeners.filter(function (f) { return f !== fn; }); }

  async function initFilter(container) {
    _container = container;
    await fetchAreas();
    render(container);
  }

  window.AreaFilter = {
    init: initFilter,
    render: render,
    getSelected: getSelected,
    getAreaParam: getAreaParam,
    areaQueryString: areaQueryString,
    onChange: onChange,
    offChange: offChange,
  };
})();
