/* === CoreScope — table-sort.js === */
/* Shared table sorting utility. IIFE, no dependencies. */
'use strict';

window.TableSort = (function() {

  /**
   * Built-in comparators. Each takes two raw string values (from data-value or textContent)
   * and returns a number for Array.sort.
   */
  var comparators = {
    text: function(a, b) {
      if (a == null) a = '';
      if (b == null) b = '';
      return String(a).localeCompare(String(b));
    },
    numeric: function(a, b) {
      var na = Number(a), nb = Number(b);
      var aIsNaN = isNaN(na), bIsNaN = isNaN(nb);
      if (aIsNaN && bIsNaN) return 0;
      if (aIsNaN) return 1;   // NaN sorts last
      if (bIsNaN) return -1;
      return na - nb;
    },
    date: function(a, b) {
      var ta = a ? new Date(a).getTime() : NaN;
      var tb = b ? new Date(b).getTime() : NaN;
      var aIsNaN = isNaN(ta), bIsNaN = isNaN(tb);
      if (aIsNaN && bIsNaN) return 0;
      if (aIsNaN) return 1;
      if (bIsNaN) return -1;
      return ta - tb;
    },
    dbm: function(a, b) {
      var na = parseFloat(String(a).replace(/\s*dBm\s*/i, ''));
      var nb = parseFloat(String(b).replace(/\s*dBm\s*/i, ''));
      var aIsNaN = isNaN(na), bIsNaN = isNaN(nb);
      if (aIsNaN && bIsNaN) return 0;
      if (aIsNaN) return 1;
      if (bIsNaN) return -1;
      return na - nb;
    }
  };

  /**
   * Resolve the comparator for a <th> element.
   * Priority: custom comparator from options > data-type attribute > text default.
   */
  function resolveComparator(key, thEl, customComparators) {
    if (customComparators && customComparators[key]) return customComparators[key];
    var type = thEl.getAttribute('data-type');
    if (type && comparators[type]) return comparators[type];
    return comparators.text;
  }

  /**
   * Get the sort value for a <td>. Prefers data-value attribute, falls back to textContent.
   */
  function getCellValue(td) {
    if (!td) return '';
    var dv = td.getAttribute('data-value');
    return dv != null ? dv : td.textContent.trim();
  }

  /**
   * Initialize sorting on a table element.
   *
   * @param {HTMLTableElement} tableEl - The table to make sortable
   * @param {Object} [options]
   * @param {string} [options.defaultColumn] - data-sort-key of initial sort column
   * @param {string} [options.defaultDirection='asc'] - 'asc' or 'desc'
   * @param {string} [options.storageKey] - localStorage key for persistence
   * @param {Object} [options.comparators] - custom comparator functions keyed by column key
   * @param {Function} [options.onSort] - callback(column, direction) after sort
   * @param {boolean} [options.domReorder=true] - if false, skip DOM reorder (for virtual scroll tables)
   * @returns {Object} instance with sort(), destroy(), getState() methods
   */
  function init(tableEl, options) {
    if (!tableEl) return null;
    options = options || {};
    var thead = tableEl.querySelector('thead');
    if (!thead) return null;

    var state = { column: options.defaultColumn || null, direction: options.defaultDirection || 'asc' };
    var domReorder = options.domReorder !== false;

    // Restore from localStorage
    if (options.storageKey) {
      try {
        var saved = JSON.parse(localStorage.getItem(options.storageKey));
        if (saved && saved.column) {
          state.column = saved.column;
          state.direction = saved.direction || 'asc';
        }
      } catch(e) { /* ignore */ }
    }

    var ths = thead.querySelectorAll('th[data-sort-key]');
    var thMap = {}; // key → th element
    var handlers = []; // for cleanup

    for (var i = 0; i < ths.length; i++) {
      (function(th) {
        var key = th.getAttribute('data-sort-key');
        thMap[key] = th;
        th.style.cursor = 'pointer';
        th.setAttribute('tabindex', '0');
        th.setAttribute('aria-sort', 'none');

        var handler = function(e) {
          if (e.type === 'keydown' && e.key !== 'Enter' && e.key !== ' ') return;
          if (e.type === 'keydown') e.preventDefault();
          if (state.column === key) {
            state.direction = state.direction === 'asc' ? 'desc' : 'asc';
          } else {
            state.column = key;
            state.direction = options.defaultDirection || 'asc';
          }
          doSort();
        };

        th.addEventListener('click', handler);
        th.addEventListener('keydown', handler);
        handlers.push({ el: th, click: handler, keydown: handler });
      })(ths[i]);
    }

    // Apply initial sort if defaultColumn is set
    if (state.column && thMap[state.column]) {
      updateArrows();
      if (domReorder) sortDOM();
    }

    function doSort() {
      updateArrows();
      if (options.storageKey) {
        try { localStorage.setItem(options.storageKey, JSON.stringify(state)); } catch(e) { /* ignore */ }
      }
      if (domReorder) sortDOM();
      if (options.onSort) options.onSort(state.column, state.direction);
    }

    function updateArrows() {
      for (var k in thMap) {
        var th = thMap[k];
        // Remove existing arrow
        var arrow = th.querySelector('.sort-arrow');
        if (arrow) arrow.remove();

        if (k === state.column) {
          th.classList.add('sort-active');
          th.setAttribute('aria-sort', state.direction === 'asc' ? 'ascending' : 'descending');
          var span = document.createElement('span');
          span.className = 'sort-arrow';
          span.textContent = state.direction === 'asc' ? ' ▲' : ' ▼';
          th.appendChild(span);
        } else {
          th.classList.remove('sort-active');
          th.setAttribute('aria-sort', 'none');
        }
      }
    }

    function sortDOM() {
      var tbody = tableEl.querySelector('tbody');
      if (!tbody) return;
      var th = thMap[state.column];
      if (!th) return;

      var cmp = resolveComparator(state.column, th, options.comparators);
      var colIndex = -1;
      var allThs = thead.querySelectorAll('th');
      for (var j = 0; j < allThs.length; j++) {
        if (allThs[j] === th) { colIndex = j; break; }
      }
      if (colIndex < 0) return;

      var rows = Array.prototype.slice.call(tbody.querySelectorAll('tr'));
      var dir = state.direction === 'asc' ? 1 : -1;

      rows.sort(function(rowA, rowB) {
        var a = getCellValue(rowA.cells[colIndex]);
        var b = getCellValue(rowB.cells[colIndex]);
        return dir * cmp(a, b);
      });

      // DOM reorder via appendChild (no innerHTML rebuild)
      for (var r = 0; r < rows.length; r++) {
        tbody.appendChild(rows[r]);
      }
    }

    function destroy() {
      for (var h = 0; h < handlers.length; h++) {
        handlers[h].el.removeEventListener('click', handlers[h].click);
        handlers[h].el.removeEventListener('keydown', handlers[h].keydown);
        // Clean up aria/classes
        handlers[h].el.removeAttribute('aria-sort');
        handlers[h].el.classList.remove('sort-active');
        var arrow = handlers[h].el.querySelector('.sort-arrow');
        if (arrow) arrow.remove();
      }
      handlers = [];
    }

    function sort(column, direction) {
      if (column) state.column = column;
      if (direction) state.direction = direction;
      doSort();
    }

    function getState() {
      return { column: state.column, direction: state.direction };
    }

    return { sort: sort, destroy: destroy, getState: getState };
  }

  return {
    init: init,
    comparators: comparators
  };

})();
