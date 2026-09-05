/* === CoreScope — scope-audit.js ===
   Network-wide scope audit (route #/scope-audit): the whole-network answer
   to "which repeaters declare a region they are not actually forwarding".
   Fetches GET /api/scope-audit once per window and renders one row per
   declared repeater, sorted server-side so the interesting rows (missing a
   declared region, or contradicting its own wildcard) sit at the top.
   Reuses node-scopes.js's vocabulary: scope names are shown without the
   leading '#' (the two backends spell them differently — see normScope
   there), and '*' is never listed as a scope. */
'use strict';
(function () {
  var loadGen = 0; // bumped per load; guards against in-flight races (mirrors node-reach.js)
  var DEFAULT_WINDOW = '24h';
  var WINDOWS = [
    { key: '1h', label: '1h' },
    { key: '24h', label: '24h' },
    { key: '7d', label: '7d' }
  ];
  var win = DEFAULT_WINDOW;
  var searchQuery = ''; // free-text filter over name/pubkey/region, applied client-side
  var searchIndex = {}; // publicKey -> lowercased searchable haystack, rebuilt every renderBody
  var sortCtl = null; // tracked TableSort controller for destroy-before-reinit (mirrors observers.js)

  function windowBtn(key, cur, label) {
    var on = key === cur;
    return '<button data-window="' + key + '" aria-pressed="' + (on ? 'true' : 'false') + '"' +
      (on ? ' class="active"' : '') + '>' + label + '</button>';
  }

  // windowHonestyNote spells out, in the window's own words, why "declared
  // but not observed" is weak evidence over a short window (a quiet region
  // simply has no traffic) and strong evidence over a long one — the reader
  // must never mistake a 1h result for a 7d one.
  function windowHonestyNote(w) {
    var strength = w === '7d' ? 'a strong signal — a full week with zero matching traffic is hard to explain as bad luck'
      : (w === '24h' ? 'a moderate signal — worth checking, but a quiet region can still look this way for a day'
        : 'weak evidence — an hour with no traffic proves very little; treat this as a hint to re-check on 24h or 7d');
    return '<div class="sa-window-note">Rows below are ranked by declared regions with <strong>zero observed forwarding in the last ' +
      escapeHtml(w) + '</strong>. At this window, that absence is ' + strength + '.</div>';
  }

  function pageHtml() {
    return '<div class="sa-page">' +
      '<div class="sa-head">' +
      '<h2>Scope audit</h2>' +
      '<div class="analytics-time-range" id="saWindow">' +
      WINDOWS.map(function (w) { return windowBtn(w.key, win, w.label); }).join('') +
      '</div></div>' +
      '<div class="sa-intro">Network-wide comparison of declared vs. observed region-scope forwarding, across every repeater that has declared a region list over RF. <a href="#/nodes">Per-node detail lives on each node\'s page</a>.</div>' +
      '<div class="sa-search-bar"><input type="text" class="nodes-search sa-search" id="saSearch" placeholder="Search by repeater, pubkey, or region…" aria-label="Search scope audit rows"></div>' +
      '<div id="saBody"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading scope audit…</div></div>' +
      '</div>';
  }

  function ageHtml(row) {
    var age = timeAgo(row.declaredAt);
    return '<span title="Declared regions answer captured ' + escapeHtml(row.declaredAt) + '">' + escapeHtml(age) + '</span>';
  }

  // mergedScopeChips renders ONE chip per declared region, coloured by whether
  // that region was actually observed forwarding in the window.
  //
  // This replaces the old DECLARED and NOT OBSERVED pair. They were never
  // independent: notObserved is a strict subset of declaredRegions, checked
  // against a live 197-row response where it held on 197 of 197 rows. The two
  // columns printed the same set twice, once whole and once filtered, and left
  // the reader to diff them. On a typical mixed row that meant comparing two
  // lists of eight to find the one or two entries that differ. Here the
  // observed ones are simply the green ones.
  //
  // No new claim is made about the data: a region present in declaredRegions
  // and absent from notObserved is exactly what the server already means by
  // "observed forwarding in this window".
  function mergedScopeChips(row) {
    var missing = Object.create(null);
    row.notObserved.forEach(function (n) { missing[n] = true; });
    var chips = row.declaredRegions.map(function (n) {
      var observed = !missing[n];
      return '<span class="sa-chip ' + (observed ? 'sa-chip-observed' : 'sa-chip-missing') +
        '" title="' + escapeHtml(n) +
        (observed ? ': observed forwarding in this window' : ': declared, but no forwarding observed in this window') +
        '">' + escapeHtml(n) + '</span>';
    });
    if (!chips.length) return '<span class="text-muted">—</span>';
    return chips.join(' ');
  }

  // nameHtml renders the repeater identity cell. row.name == null means this
  // instance holds NO nodes row for the target at all (a declared-regions
  // answer can name a repeater the network has never recorded) — distinct
  // from row.name === "" (a known node that simply has no name). A
  // truthiness check collapses those two into the same pubkey-stub
  // rendering, which defeats the point of the API making the field
  // nullable. The unknown case also gets no link (FIX 4): #/nodes/<pubkey>
  // cannot resolve a target we hold no row for.
  function nameHtml(row) {
    if (row.name == null) {
      return '<span class="sa-name-unknown" title="No nodes row held for this target — a declared-regions answer can name a repeater this instance has never recorded.">' +
        escapeHtml(row.publicKey.slice(0, 10)) + '… (unknown node)</span>';
    }
    var name = row.name ? escapeHtml(row.name) : escapeHtml(row.publicKey.slice(0, 10)) + '…';
    return '<a href="#/nodes/' + encodeURIComponent(row.publicKey) + '">' + name + '</a>';
  }

  // CONFIG_STATES maps ScopeAuditRow.configState (server-computed from
  // declaredRegions + declaredWildcard — see docs/api-spec.md) to a compact
  // badge, reusing the same .ns-decl colour vocabulary the Status column
  // already uses on this page (node-scopes.css) rather than inventing a new
  // one. "no-scopes" gets the warn (red) treatment because it is the
  // headline finding this classification exists to surface — roughly a
  // third of repeaters answering at all fall in it — but its title text
  // carries the caveat that "*" alone does not strictly prove no region is
  // configured, only that no region is flood-allowed (see scopeAuditConfigState
  // in cmd/server/scopes.go): a repeater with regions defined but every one
  // marked deny-flood looks identical from this data.
  var CONFIG_STATES = {
    'full': {
      label: 'Full', cls: 'ns-decl-yes', summary: 'fully configured',
      title: 'Declares named regions and \'*\' — forwards both its declared regions and plain unscoped floods.'
    },
    'no-scopes': {
      label: 'No scopes', cls: 'ns-decl-warn', summary: 'no scopes configured',
      title: 'Declares only \'*\', no named regions — no region is flood-allowed. Almost always means no scopes are configured at all, but a repeater with regions defined that are ALL set to deny flooding would look identical from this data alone.'
    },
    'no-unscoped': {
      label: 'No unscoped', cls: 'ns-decl-quiet', summary: 'not forwarding unscoped floods',
      title: 'Declares named regions but not \'*\' — does not forward plain unscoped floods. Exact, not an inference.'
    },
    'no-flood': {
      label: 'No flood', cls: 'ns-decl-unknown', summary: 'nothing flood-allowed',
      title: 'Declares neither named regions nor \'*\' — an answered-but-empty list. Nothing is flood-allowed by this repeater, not even plain unscoped traffic.'
    }
  };
  var CONFIG_STATE_ORDER = ['no-scopes', 'no-unscoped', 'full', 'no-flood'];

  function configStateHtml(row) {
    var meta = CONFIG_STATES[row.configState];
    return '<span class="ns-decl ' + meta.cls + '" title="' + escapeHtml(meta.title) + '">' + meta.label + '</span>';
  }

  // configStateSummaryHtml gives the reader "13 repeaters have no scopes
  // configured" at a glance without reading every row — the counts are
  // tallied straight off d.repeaters[].configState, the same array the table
  // below renders, so they can never drift out of sync with the rows.
  function configStateSummaryHtml(repeaters) {
    var counts = { full: 0, 'no-scopes': 0, 'no-unscoped': 0, 'no-flood': 0 };
    repeaters.forEach(function (r) { counts[r.configState]++; });
    return '<div class="sa-summary">' +
      CONFIG_STATE_ORDER.map(function (state) {
        var meta = CONFIG_STATES[state];
        return '<span class="sa-summary-item" title="' + escapeHtml(meta.title) + '">' +
          '<span class="ns-decl ' + meta.cls + '">' + counts[state] + '</span> ' + escapeHtml(meta.summary) + '</span>';
      }).join('') +
      '</div>';
  }

  // ambiguousCaveat flags rows with a non-zero FIX 1 ambiguity count: hops
  // whose truncated hash prefix matched more than one declared target were
  // credited to none of them, so a notObserved finding here could be a
  // prefix collision rather than a confirmed gap.
  function ambiguousCaveat(row) {
    if (!row.ambiguousHops) return '';
    return ' <span class="sa-chip sa-chip-ambiguous" title="' + row.ambiguousHops + ' forwarder hop' + (row.ambiguousHops === 1 ? '' : 's') +
      ' in this window matched more than one declared target\'s pubkey prefix and could not be attributed to any of them. Any “not observed” entry on this row may be explained by that prefix collision rather than a real gap.">possibly ambiguous</span>';
  }

  // statusScore ranks a row's Status column numerically for sorting — a
  // simple weighted count (notObserved dominates, matching the server's own
  // findings-first ranking) rather than the badge text, which the Status
  // column shows as chips, not a single sortable label.
  function statusScore(row) {
    return row.notObserved.length * 100 + (row.wildcardContradiction ? 10 : 0) + row.undeclaredObserved.length;
  }

  function rowHtml(row) {
    var issues = [];
    if (row.notObserved.length) issues.push('<span class="ns-decl ns-decl-quiet" title="Declared flood-allowed but not observed forwarding it this window.">' + row.notObserved.length + ' not observed</span>');
    if (row.wildcardContradiction) issues.push('<span class="ns-decl ns-decl-warn" title="Observed forwarding plain (unscoped) floods, but the declared list omits the &#39;*&#39; wildcard that would allow that.">wildcard contradiction</span>');
    if (row.undeclaredObserved.length) issues.push('<span class="ns-decl ns-decl-unknown" title="Observed forwarding scopes absent from the declared list.">' + row.undeclaredObserved.length + ' undeclared</span>');
    var issuesHtml = issues.length ? issues.join(' ') : '<span class="ns-decl ns-decl-yes" title="Every declared region was observed forwarding, and nothing undeclared was observed.">agrees</span>';

    // declaredAtMs is the underlying declaredAt timestamp in epoch ms, fed to
    // the Declared age <td> as data-value (mirroring observers.js's
    // _lastSeenMs) so TableSort's numeric comparator sorts on the real
    // timestamp instead of the rendered "4h ago" text, which cannot be
    // parsed back into a date.
    var declaredAtMs = row.declaredAt ? new Date(row.declaredAt).getTime() : NaN;
    var nameSortValue = row.name != null ? row.name : row.publicKey;

    return '<tr data-pubkey="' + escapeHtml(row.publicKey) + '">' +
      '<td class="sa-name" data-value="' + escapeHtml(nameSortValue) + '">' + nameHtml(row) + (row.role != null && row.role !== '' ? '<span class="text-muted sa-role"> ' + escapeHtml(row.role) + '</span>' : '') + '</td>' +
      '<td data-value="' + statusScore(row) + '">' + issuesHtml + '</td>' +
      '<td data-value="' + escapeHtml(CONFIG_STATES[row.configState].label) + '">' + configStateHtml(row) + '</td>' +
      '<td data-value="' + row.notObserved.length + '">' + mergedScopeChips(row) + (row.declaredWildcard ? ' <span class="sa-chip sa-chip-wildcard" title="Declares the \'*\' wildcard — allows plain unscoped floods.">*</span>' : '') + ambiguousCaveat(row) + '</td>' +
      '<td data-value="' + (isNaN(declaredAtMs) ? '' : declaredAtMs) + '">' + ageHtml(row) + (row.truncated ? ' <span class="ns-truncated" title="Declared list was truncated by the repeater — a missing region here is not necessarily a real absence.">truncated</span>' : '') + '</td>' +
      '</tr>';
  }

  // buildSearchIndex maps publicKey -> lowercased haystack (name, pubkey,
  // and every region name this row mentions — declared, not-observed, and
  // undeclared-observed) so applyFilter can match a row without re-deriving
  // it from rendered chip text.
  function buildSearchIndex(repeaters) {
    var idx = {};
    repeaters.forEach(function (row) {
      var parts = [row.publicKey];
      if (row.name) parts.push(row.name);
      row.declaredRegions.forEach(function (r) { parts.push(r); });
      row.notObserved.forEach(function (r) { parts.push(r); });
      row.undeclaredObserved.forEach(function (o) { parts.push(o.scope); });
      idx[row.publicKey] = parts.join(' ').toLowerCase();
    });
    return idx;
  }

  // applyFilter toggles row visibility against searchQuery and refreshes the
  // shown-count line — independent of sort order, since it only sets
  // style.display on whatever <tr> elements are currently in the tbody, so
  // filtering a sorted table keeps the sort and sorting a filtered table
  // keeps the filter.
  function applyFilter() {
    var tbody = document.querySelector('#saTable tbody');
    var countEl = document.getElementById('saCount');
    if (!tbody) return;
    var q = searchQuery.trim().toLowerCase();
    var rows = tbody.querySelectorAll('tr');
    var shown = 0;
    for (var i = 0; i < rows.length; i++) {
      var pk = rows[i].getAttribute('data-pubkey');
      var hay = searchIndex[pk] || '';
      var match = !q || hay.indexOf(q) !== -1;
      rows[i].style.display = match ? '' : 'none';
      if (match) shown++;
    }
    if (!countEl) return;
    if (q) {
      countEl.textContent = 'Showing ' + shown + ' of ' + rows.length + ' repeater' + (rows.length === 1 ? '' : 's') + ' matching “' + searchQuery.trim() + '”.';
    } else {
      countEl.textContent = rows.length + ' repeater' + (rows.length === 1 ? '' : 's') + ' with a declared region list.';
    }
  }

  function renderBody(d) {
    var el = document.getElementById('saBody');
    if (!el) return;
    if (sortCtl && typeof sortCtl.destroy === 'function') {
      try { sortCtl.destroy(); } catch (e) { /* ignore */ }
    }
    sortCtl = null;
    if (!d.repeaters.length) {
      searchIndex = {};
      el.innerHTML = '<div class="ns-empty">No repeater has declared a region list yet — this fills in as devices drive and answer over RF.</div>';
      return;
    }
    searchIndex = buildSearchIndex(d.repeaters);
    el.innerHTML = configStateSummaryHtml(d.repeaters) +
      windowHonestyNote(d.window) +
      '<div class="sa-table-wrap"><table class="ns-table sa-table" id="saTable"><thead><tr>' +
      '<th data-sort-key="name">Repeater</th>' +
      '<th data-sort-key="status" data-type="numeric">Status</th>' +
      '<th data-sort-key="config">Config</th>' +
      '<th data-sort-key="notObserved" data-type="numeric" title="Declared regions, coloured by whether forwarding was observed in this window. Green = observed, red = declared but not observed.">Scopes</th>' +
      '<th data-sort-key="declaredAt" data-type="numeric">Declared age</th>' +
      '</tr></thead><tbody>' +
      d.repeaters.map(rowHtml).join('') +
      '</tbody></table></div>' +
      '<div class="sa-count text-muted" id="saCount"></div>';

    var saTbl = document.getElementById('saTable');
    if (saTbl && window.TableSort) {
      // No defaultColumn: the server's own findings-first order (most
      // notObserved at the top) stays the default until the reader picks a
      // column — that ordering is the reason this page exists.
      sortCtl = TableSort.init(saTbl, { storageKey: 'meshcore-scope-audit-sort' });
    } else if (saTbl && !window.TableSort) {
      console.warn('[scope-audit] window.TableSort missing — table will not be sortable');
    }
    applyFilter();
  }

  async function load(w) {
    win = w;
    var myGen = ++loadGen;
    var head = document.getElementById('saWindow');
    if (head) {
      WINDOWS.forEach(function (wd) {
        var b = head.querySelector('button[data-window="' + wd.key + '"]');
        if (b) { b.classList.toggle('active', wd.key === w); b.setAttribute('aria-pressed', wd.key === w ? 'true' : 'false'); }
      });
    }
    var body = document.getElementById('saBody');
    if (body) body.innerHTML = '<div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading scope audit…</div>';
    var d;
    try {
      d = await api('/scope-audit?window=' + encodeURIComponent(w), { ttl: 30000 });
    } catch (e) {
      if (myGen !== loadGen) return;
      if (body) body.innerHTML = '<div class="ns-empty">Failed to load scope audit: ' + escapeHtml(e.message) + '</div>';
      return;
    }
    if (myGen !== loadGen) return;
    renderBody(d);
    syncHash();
  }

  function syncHash() {
    try { history.replaceState(null, '', '#/scope-audit?window=' + win); } catch (e) {}
  }

  function init(container) {
    win = DEFAULT_WINDOW;
    searchQuery = '';
    try {
      var p = (typeof getHashParams === 'function') ? getHashParams() : null;
      var qw = p ? p.get('window') : null;
      if (qw && WINDOWS.some(function (w) { return w.key === qw; })) win = qw;
    } catch (e) {}
    container.innerHTML = pageHtml();
    var bar = document.getElementById('saWindow');
    if (bar) bar.addEventListener('click', function (e) {
      var b = e.target.closest('button[data-window]');
      if (b) load(b.getAttribute('data-window'));
    });
    var search = document.getElementById('saSearch');
    if (search) search.addEventListener('input', debounce(function (e) {
      searchQuery = e.target.value;
      applyFilter();
    }, 250));
    load(win);
  }

  function destroy() {
    loadGen++;
    if (sortCtl && typeof sortCtl.destroy === 'function') {
      try { sortCtl.destroy(); } catch (e) { /* ignore */ }
    }
    sortCtl = null;
  }

  if (typeof window !== 'undefined') {
    // Exposed so the helper tests can assert what the Scopes column RENDERS
    // rather than grepping this file, the same reason map.js exposes its label
    // builder (#1356/#1933).
    window.__meshcoreScopeAuditInternals = { mergedScopeChips: mergedScopeChips };
  }

  registerPage('scope-audit', { init: init, destroy: destroy });
})();
