/* === CoreScope — compare.js === */
/* Observer packet comparison — Fixes #129 */
'use strict';

/**
 * Compare two sets of packet hashes using Set operations.
 * Returns { onlyA, onlyB, both } as arrays of hashes.
 * O(n) via Set lookups — no nested loops.
 */
function comparePacketSets(hashesA, hashesB) {
  var setA = hashesA instanceof Set ? hashesA : new Set(hashesA || []);
  var setB = hashesB instanceof Set ? hashesB : new Set(hashesB || []);
  var onlyA = [];
  var onlyB = [];
  var both = [];
  setA.forEach(function (h) {
    if (setB.has(h)) both.push(h);
    else onlyA.push(h);
  });
  setB.forEach(function (h) {
    if (!setA.has(h)) onlyB.push(h);
  });
  return { onlyA: onlyA, onlyB: onlyB, both: both };
}

// Expose for testing
if (typeof window !== 'undefined') window.comparePacketSets = comparePacketSets;

(function () {
  var PAYLOAD_LABELS = { 0: 'Request', 1: 'Response', 2: 'Direct Msg', 3: 'ACK', 4: 'Advert', 5: 'Channel Msg', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 11: 'Control' };
  var MAX_PACKETS = 10000;
  var observers = [];
  var selA = null;
  var selB = null;
  var comparisonResult = null;
  var packetsA = [];
  var packetsB = [];
  var currentView = 'summary';

  function init(app, routeParam) {
    // Parse preselected observers from URL: #/compare?a=ID1&b=ID2
    var hashParams = location.hash.split('?')[1] || '';
    var params = new URLSearchParams(hashParams);
    selA = params.get('a') || null;
    selB = params.get('b') || null;
    comparisonResult = null;
    packetsA = [];
    packetsB = [];
    currentView = 'summary';

    app.innerHTML = '<div class="compare-page" style="overflow-y:auto;height:calc(100vh - 56px);padding:16px">' +
      '<div class="page-header" style="display:flex;align-items:center;gap:12px;margin-bottom:16px">' +
        '<a href="#/observers" class="btn-icon" title="Back to Observers" aria-label="Back">\u2190</a>' +
        '<h2 style="margin:0">\uD83D\uDD0D Observer Comparison</h2>' +
      '</div>' +
      '<div id="compareControls" class="compare-controls"><div class="text-center text-muted" style="padding:20px">Loading observers\u2026</div></div>' +
      '<div id="compareContent"></div>' +
    '</div>';

    // #209 — Keyboard accessibility for compare table rows
    app.addEventListener('keydown', function (e) {
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      location.hash = row.dataset.value;
    });

    loadObservers();
  }

  function destroy() {
    observers = [];
    selA = null;
    selB = null;
    comparisonResult = null;
    packetsA = [];
    packetsB = [];
  }

  async function loadObservers() {
    try {
      var data = await api('/observers', { ttl: CLIENT_TTL.observers });
      observers = (data.observers || []).sort(function (a, b) {
        return (a.name || a.id).localeCompare(b.name || b.id);
      });
      renderControls();
      if (selA && selB) runComparison();
    } catch (e) {
      document.getElementById('compareControls').innerHTML =
        '<div class="text-muted" style="padding:20px">Error loading observers: ' + escapeHtml(e.message) + '</div>';
    }
  }

  function renderControls() {
    var el = document.getElementById('compareControls');
    if (!el) return;

    var optionsHtml = '<option value="">Select observer\u2026</option>' +
      observers.map(function (o) {
        var label = escapeHtml(o.name || o.id);
        var region = o.iata ? ' (' + escapeHtml(o.iata) + ')' : '';
        return '<option value="' + escapeHtml(o.id) + '">' + label + region + '</option>';
      }).join('');

    el.innerHTML =
      '<div class="compare-selector">' +
        '<div class="compare-select-group">' +
          '<label for="compareObsA">Observer A</label>' +
          '<select id="compareObsA" class="compare-select">' + optionsHtml + '</select>' +
        '</div>' +
        '<span class="compare-vs">vs</span>' +
        '<div class="compare-select-group">' +
          '<label for="compareObsB">Observer B</label>' +
          '<select id="compareObsB" class="compare-select">' + optionsHtml + '</select>' +
        '</div>' +
        '<button id="compareBtn" class="compare-btn" disabled>Compare</button>' +
      '</div>';

    var ddA = document.getElementById('compareObsA');
    var ddB = document.getElementById('compareObsB');
    var btn = document.getElementById('compareBtn');

    if (selA) ddA.value = selA;
    if (selB) ddB.value = selB;

    function updateBtn() {
      selA = ddA.value || null;
      selB = ddB.value || null;
      btn.disabled = !selA || !selB || selA === selB;
    }
    ddA.addEventListener('change', updateBtn);
    ddB.addEventListener('change', updateBtn);
    btn.addEventListener('click', function () { runComparison(); });
    updateBtn();
  }

  function sinceISO(hours) {
    return new Date(Date.now() - hours * 3600000).toISOString();
  }

  async function runComparison() {
    if (!selA || !selB || selA === selB) return;
    var content = document.getElementById('compareContent');
    if (!content) return;

    content.innerHTML = '<div class="text-center text-muted" style="padding:40px">Fetching packets\u2026</div>';

    // Update URL for shareability
    var base = '#/compare?a=' + encodeURIComponent(selA) + '&b=' + encodeURIComponent(selB);
    if (location.hash.split('?')[0] === '#/compare') {
      history.replaceState(null, '', base);
    }

    try {
      var since24h = sinceISO(24);
      var results = await Promise.all([
        api('/packets?observer=' + encodeURIComponent(selA) + '&limit=' + MAX_PACKETS + '&since=' + encodeURIComponent(since24h)),
        api('/packets?observer=' + encodeURIComponent(selB) + '&limit=' + MAX_PACKETS + '&since=' + encodeURIComponent(since24h))
      ]);

      packetsA = results[0].packets || [];
      packetsB = results[1].packets || [];

      var hashesA = new Set(packetsA.map(function (p) { return p.hash; }));
      var hashesB = new Set(packetsB.map(function (p) { return p.hash; }));

      comparisonResult = comparePacketSets(hashesA, hashesB);

      // Build hash→packet lookups for detail rendering
      comparisonResult.packetMapA = new Map();
      comparisonResult.packetMapB = new Map();
      packetsA.forEach(function (p) { comparisonResult.packetMapA.set(p.hash, p); });
      packetsB.forEach(function (p) { comparisonResult.packetMapB.set(p.hash, p); });

      currentView = 'summary';
      renderComparison();
    } catch (e) {
      content.innerHTML = '<div class="text-muted" style="padding:40px">Error: ' + escapeHtml(e.message) + '</div>';
    }
  }

  function obsName(id) {
    for (var i = 0; i < observers.length; i++) {
      if (observers[i].id === id) return observers[i].name || id;
    }
    return id ? id.substring(0, 12) : 'Unknown';
  }

  function renderComparison() {
    var content = document.getElementById('compareContent');
    if (!content || !comparisonResult) return;

    var r = comparisonResult;
    var nameA = escapeHtml(obsName(selA));
    var nameB = escapeHtml(obsName(selB));
    var total = r.onlyA.length + r.onlyB.length + r.both.length;
    var pctBoth = total > 0 ? Math.round(r.both.length / total * 100) : 0;
    var pctA = total > 0 ? Math.round(r.onlyA.length / total * 100) : 0;
    var pctB = total > 0 ? Math.round(r.onlyB.length / total * 100) : 0;

    // Type breakdown for "both" packets
    var typeBreakdown = {};
    r.both.forEach(function (h) {
      var p = r.packetMapA.get(h) || r.packetMapB.get(h);
      if (p) {
        var t = p.payload_type;
        typeBreakdown[t] = (typeBreakdown[t] || 0) + 1;
      }
    });

    var typeHtml = Object.keys(typeBreakdown).map(function (t) {
      return '<span class="compare-type-badge">' +
        escapeHtml(PAYLOAD_LABELS[t] || 'Type ' + t) + ': ' + typeBreakdown[t] +
      '</span>';
    }).join(' ');

    content.innerHTML =
      '<div class="compare-results">' +
        // Summary cards
        '<div class="compare-summary">' +
          '<div class="compare-card compare-card-both" data-view="both">' +
            '<div class="compare-card-count">' + r.both.length.toLocaleString() + '</div>' +
            '<div class="compare-card-label">Seen by both</div>' +
            '<div class="compare-card-pct">' + pctBoth + '%</div>' +
          '</div>' +
          '<div class="compare-card compare-card-a" data-view="onlyA">' +
            '<div class="compare-card-count">' + r.onlyA.length.toLocaleString() + '</div>' +
            '<div class="compare-card-label">Only ' + nameA + '</div>' +
            '<div class="compare-card-pct">' + pctA + '%</div>' +
          '</div>' +
          '<div class="compare-card compare-card-b" data-view="onlyB">' +
            '<div class="compare-card-count">' + r.onlyB.length.toLocaleString() + '</div>' +
            '<div class="compare-card-label">Only ' + nameB + '</div>' +
            '<div class="compare-card-pct">' + pctB + '%</div>' +
          '</div>' +
        '</div>' +

        // Visual bar
        '<div class="compare-bar-container">' +
          '<div class="compare-bar">' +
            (pctA > 0 ? '<div class="compare-bar-seg compare-bar-a" style="width:' + pctA + '%" title="Only ' + nameA + ': ' + r.onlyA.length + '"></div>' : '') +
            (pctBoth > 0 ? '<div class="compare-bar-seg compare-bar-both" style="width:' + pctBoth + '%" title="Both: ' + r.both.length + '"></div>' : '') +
            (pctB > 0 ? '<div class="compare-bar-seg compare-bar-b" style="width:' + pctB + '%" title="Only ' + nameB + ': ' + r.onlyB.length + '"></div>' : '') +
          '</div>' +
          '<div class="compare-bar-legend">' +
            '<span class="compare-legend-item"><span class="compare-dot compare-dot-a"></span> ' + nameA + ' only</span>' +
            '<span class="compare-legend-item"><span class="compare-dot compare-dot-both"></span> Both</span>' +
            '<span class="compare-legend-item"><span class="compare-dot compare-dot-b"></span> ' + nameB + ' only</span>' +
          '</div>' +
        '</div>' +

        // Type breakdown for shared packets
        (typeHtml ? '<div class="compare-type-summary"><strong>Shared packet types:</strong> ' + typeHtml + '</div>' : '') +

        // Detail tabs
        '<div class="compare-tabs">' +
          '<button class="tab-btn' + (currentView === 'summary' ? ' active' : '') + '" data-cview="summary">Summary</button>' +
          '<button class="tab-btn' + (currentView === 'both' ? ' active' : '') + '" data-cview="both">Both (' + r.both.length + ')</button>' +
          '<button class="tab-btn' + (currentView === 'onlyA' ? ' active' : '') + '" data-cview="onlyA">Only ' + nameA + ' (' + r.onlyA.length + ')</button>' +
          '<button class="tab-btn' + (currentView === 'onlyB' ? ' active' : '') + '" data-cview="onlyB">Only ' + nameB + ' (' + r.onlyB.length + ')</button>' +
        '</div>' +
        '<div id="compareDetail"></div>' +
      '</div>';

    // Bind tab clicks
    content.addEventListener('click', function handler(e) {
      var btn = e.target.closest('[data-cview]');
      if (btn) {
        currentView = btn.dataset.cview;
        content.querySelectorAll('.tab-btn').forEach(function (b) { b.classList.remove('active'); });
        btn.classList.add('active');
        renderDetail();
        return;
      }
      // Clickable summary cards
      var card = e.target.closest('[data-view]');
      if (card) {
        currentView = card.dataset.view;
        content.querySelectorAll('.tab-btn').forEach(function (b) {
          b.classList.toggle('active', b.dataset.cview === currentView);
        });
        renderDetail();
      }
    });

    renderDetail();
  }

  function renderDetail() {
    var el = document.getElementById('compareDetail');
    if (!el || !comparisonResult) return;
    var r = comparisonResult;
    var nameA = escapeHtml(obsName(selA));
    var nameB = escapeHtml(obsName(selB));

    if (currentView === 'summary') {
      // Textual summary
      var total = r.onlyA.length + r.onlyB.length + r.both.length;
      var overlap = total > 0 ? (r.both.length / total * 100).toFixed(1) : '0.0';
      el.innerHTML =
        '<div class="compare-summary-text">' +
          '<p>In the last 24 hours, <strong>' + nameA + '</strong> saw <strong>' + (r.onlyA.length + r.both.length).toLocaleString() + '</strong> unique packets ' +
          'and <strong>' + nameB + '</strong> saw <strong>' + (r.onlyB.length + r.both.length).toLocaleString() + '</strong> unique packets.</p>' +
          '<p><strong>' + r.both.length.toLocaleString() + '</strong> packets (' + overlap + '%) were seen by both observers. ' +
          '<strong>' + r.onlyA.length.toLocaleString() + '</strong> were exclusive to ' + nameA + ' and ' +
          '<strong>' + r.onlyB.length.toLocaleString() + '</strong> were exclusive to ' + nameB + '.</p>' +
          (r.both.length === 0 && total > 0 ? '<p class="compare-warning">\u26A0\uFE0F These observers share no packets \u2014 they may be on different frequencies or too far apart.</p>' : '') +
          (r.onlyA.length === 0 && r.onlyB.length === 0 && r.both.length > 0 ? '<p class="compare-good">\u2705 Perfect overlap \u2014 both observers see the same packets.</p>' : '') +
        '</div>';
      return;
    }

    var hashes = r[currentView] || [];
    if (hashes.length === 0) {
      el.innerHTML = '<div class="text-muted" style="padding:20px">No packets in this category.</div>';
      return;
    }

    // Show up to 200 packets in the table
    var displayLimit = 200;
    var displayed = hashes.slice(0, displayLimit);
    var mapA = r.packetMapA;
    var mapB = r.packetMapB;

    el.innerHTML =
      (hashes.length > displayLimit ? '<div class="text-muted" style="margin-bottom:8px">Showing first ' + displayLimit + ' of ' + hashes.length.toLocaleString() + ' packets.</div>' : '') +
      '<div class="analytics-table-scroll"><table class="data-table compare-table">' +
        '<thead><tr>' +
          '<th scope="col">Hash</th><th scope="col">Time</th><th scope="col">Type</th><th scope="col">Observer</th>' +
        '</tr></thead>' +
        '<tbody>' + displayed.map(function (h) {
          var p = mapA.get(h) || mapB.get(h);
          if (!p) return '';
          var typeName = PAYLOAD_LABELS[p.payload_type] || 'Type ' + p.payload_type;
          var obsLabel = '';
          if (currentView === 'both') {
            obsLabel = nameA + ', ' + nameB;
          } else if (currentView === 'onlyA') {
            obsLabel = nameA;
          } else {
            obsLabel = nameB;
          }
          return '<tr style="cursor:pointer" tabindex="0" role="row" data-action="navigate" data-value="#/packets/' + escapeHtml(h) + '" onclick="location.hash=\'#/packets/' + escapeHtml(h) + '\'">' +
            '<td class="mono" style="font-size:0.85em">' + escapeHtml(h.substring(0, 12)) + '</td>' +
            '<td>' + timeAgo(p.timestamp || p.first_seen) + '</td>' +
            '<td><span class="payload-badge badge-' + payloadTypeColor(p.payload_type) + '">' + escapeHtml(typeName) + '</span></td>' +
            '<td>' + obsLabel + '</td>' +
          '</tr>';
        }).join('') +
        '</tbody>' +
      '</table></div>';
  }

  registerPage('compare', { init: init, destroy: destroy });
})();
