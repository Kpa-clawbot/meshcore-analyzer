/* Issue #1061 — Bottom navigation for narrow viewports.
 * Issue #1174 — Add 6th "More" tab + bottom-anchored sheet for long-tail routes.
 *
 * Renders 6 tabs anchored to the bottom on viewports ≤768px:
 *   1. Home    — primary
 *   2. Packets — primary
 *   3. Live    — primary
 *   4. Map     — primary
 *   5. Channels — primary
 *   6. More    — toggles a bottom-anchored sheet listing the long-tail
 *                routes (Nodes, Tools, Observers, Analytics, Perf, Audio Lab).
 *                Replaces the hamburger at ≤768px (#1174 design call).
 *
 * Tabs are <a href="#/..."> so they reuse the existing hashchange-driven
 * router in app.js (no full reload, no reimplementation of routing logic).
 * The "More" tab is a <button> (not <a>) since it toggles UI rather than
 * navigating to a hash.
 *
 * Stable selectors for tests / future automation:
 *   [data-bottom-nav]                       — the <nav> container
 *   [data-bottom-nav-tab="<route>"]         — each tab including "more"
 *   [data-bottom-nav-sheet]                 — the popover sheet
 *   [data-bottom-nav-more-route="<route>"]  — each long-tail route in the sheet
 *
 * Active-tab highlight is a class toggle ("active") set on hashchange.
 * Visual treatment lives in bottom-nav.css and respects
 * prefers-reduced-motion (transitions disabled).
 *
 * Sheet behavior:
 *   - tap More → sheet opens, aria-expanded="true"
 *   - tap More while open → sheet closes (toggle, not push)
 *   - tap any route inside → in-app router navigates AND sheet closes
 *   - tap outside (anywhere not the sheet or the More tab) → sheet closes
 *   - sheet has role="menu" for a11y
 *
 * The sheet DOM is built lazily on first open — it's only used at ≤768px
 * and there's no point sitting in the DOM at desktop widths.
 */
(function () {
  'use strict';

  if (typeof document === 'undefined') return;

  // Phosphor sprite ref helper (#1648 M1).
  // Returns an inline SVG that uses the bundled sprite at /icons/phosphor-sprite.svg.
  // Inherits color via currentColor and size via the surrounding font-size.
  function phIconHTML(name) {
    return '<svg class="ph-icon" aria-hidden="true" focusable="false">' +
           '<use href="/icons/phosphor-sprite.svg#ph-' + name + '"></use></svg>';
  }

  // 5 primary tabs + the More toggle. Each entry: { route, hash, label, ph }.
  // For More, hash is null (not a route). `ph` is the Phosphor icon id
  // (no "ph-" prefix) — see public/icons/phosphor-sprite.svg.
  var TABS = [
    { route: 'home',     hash: '#/home',     label: 'Home',     ph: 'house' },
    { route: 'packets',  hash: '#/packets',  label: 'Packets',  ph: 'package' },
    { route: 'live',     hash: '#/live',     label: 'Live',     ph: 'broadcast' },
    { route: 'map',      hash: '#/map',      label: 'Map',      ph: 'map-trifold' },
    { route: 'channels', hash: '#/channels', label: 'Channels', ph: 'chat-circle' },
    { route: 'more',     hash: null,         label: 'More',     ph: 'list' },
  ];

  // Long-tail routes surfaced in the More sheet. Mirrors data-route values
  // from the existing top-nav (public/index.html). Order matches what
  // operators expect from the desktop top-nav.
  //
  // !! MANUAL SYNC REQUIRED !!
  // This list is intentionally hardcoded (not generated from
  // `.top-nav .nav-link[data-route]`) because the top-nav HTML is in
  // mid-rewrite and not a reliable single-source-of-truth. If you add a
  // new top-nav route (e.g. a future "Lab" page), you MUST also append
  // it here, or it will be unreachable on phones at <=768px (the
  // hamburger is hidden at that breakpoint -- see bottom-nav.css).
  var MORE_ROUTES = [
    { route: 'nodes',     hash: '#/nodes',     label: 'Nodes',     ph: 'monitor' },
    { route: 'tools',     hash: '#/tools',     label: 'Tools',     ph: 'wrench' },
    { route: 'observers', hash: '#/observers', label: 'Observers', ph: 'eye' },
    { route: 'analytics', hash: '#/analytics', label: 'Analytics', ph: 'chart-bar' },
    { route: 'scope-audit', hash: '#/scope-audit', label: 'Scope Audit', ph: 'clipboard-text' },
    { route: 'perf',      hash: '#/perf',      label: 'Perf',      ph: 'lightning' },
    { route: 'audio-lab', hash: '#/audio-lab', label: 'Audio Lab', ph: 'music-note' },
  ];

  // The Coverage route is opt-in (config `clientRxCoverage`). roles.js injects
  // its link into the desktop top-nav only, so it must be added here too or it
  // is unreachable on phones. Evaluated at lazy build time (first sheet open),
  // by which point MeshConfigReady has resolved window.MC_CLIENT_RX_COVERAGE.
  var COVERAGE_ROUTE = { route: 'rx-coverage', hash: '#/rx-coverage', label: 'Coverage', ph: 'broadcast' };

  function moreRoutes() {
    if (!window.MC_CLIENT_RX_COVERAGE) return MORE_ROUTES;
    var out = MORE_ROUTES.slice();
    var after = out.findIndex(function (r) { return r.route === 'analytics'; }) + 1;
    out.splice(after, 0, COVERAGE_ROUTE);
    return out;
  }

  var SHEET_ID = 'bottomNavMoreSheet';

  function currentRoute() {
    // Mirror app.js navigate(): strip "#/" and any trailing "?…" / "/…".
    var h = (location.hash || '').replace(/^#\//, '');
    if (!h) return 'packets'; // app.js default
    var slash = h.indexOf('/');
    if (slash >= 0) h = h.substring(0, slash);
    var q = h.indexOf('?');
    if (q >= 0) h = h.substring(0, q);
    return h || 'packets';
  }

  function build() {
    if (document.querySelector('[data-bottom-nav]')) return;

    var nav = document.createElement('nav');
    nav.className = 'bottom-nav';
    nav.setAttribute('data-bottom-nav', '');
    nav.setAttribute('role', 'navigation');
    nav.setAttribute('aria-label', 'Bottom navigation');

    TABS.forEach(function (t) {
      var el;
      if (t.route === 'more') {
        // <button> for the toggle: it does not navigate.
        el = document.createElement('button');
        el.setAttribute('type', 'button');
        el.setAttribute('aria-haspopup', 'menu');
        el.setAttribute('aria-expanded', 'false');
        el.setAttribute('aria-controls', SHEET_ID);
      } else {
        el = document.createElement('a');
        el.setAttribute('href', t.hash);
      }
      el.className = 'bottom-nav-tab';
      el.setAttribute('data-bottom-nav-tab', t.route);
      el.setAttribute('data-route', t.route);
      el.setAttribute('aria-label', t.label);

      var ic = document.createElement('span');
      ic.className = 'bottom-nav-icon';
      ic.setAttribute('aria-hidden', 'true');
      ic.innerHTML = phIconHTML(t.ph);

      var lb = document.createElement('span');
      lb.className = 'bottom-nav-label';
      lb.textContent = t.label;

      el.appendChild(ic);
      el.appendChild(lb);
      nav.appendChild(el);
    });

    // Insert after <main> so it's a sibling at the body level — keeps
    // it out of the <main> scroll container. The CSS pins it bottom:0
    // via position:fixed so DOM order beyond "after the nav" doesn't
    // matter for layout, but document order matters for screen readers.
    var main = document.getElementById('app') || document.querySelector('main');
    if (main && main.parentNode) {
      main.parentNode.insertBefore(nav, main.nextSibling);
    } else {
      document.body.appendChild(nav);
    }

    wireMoreSheet();
  }

  function syncActive() {
    var route = currentRoute();
    // #1174 mesh-op review: the More tab represents the long-tail
    // routes; reflect that in the active-class so users on /tools,
    // /analytics, etc. still see WHICH tab they're under. Without this
    // every long-tail route lit up zero tabs.
    var moreRouteSet = {};
    for (var k = 0; k < MORE_ROUTES.length; k++) moreRouteSet[MORE_ROUTES[k].route] = 1;
    var routeIsLongTail = !!moreRouteSet[route];
    var tabs = document.querySelectorAll('[data-bottom-nav-tab]');
    for (var i = 0; i < tabs.length; i++) {
      var t = tabs[i];
      var tabRoute = t.getAttribute('data-bottom-nav-tab');
      if (tabRoute === 'more') {
        // The More tab IS active when the current route belongs to the
        // long-tail set surfaced by the More sheet. We do NOT add
        // aria-current here — the tab toggles a sheet, not a single
        // page, so aria-current="page" would lie. The visual active
        // class is the user-facing affordance; that's enough.
        if (routeIsLongTail) t.classList.add('active');
        else if (!isSheetOpen()) t.classList.remove('active');
        // If the sheet is open we leave .active alone — openSheet()
        // owns the class while open.
        continue;
      }
      if (tabRoute === route) {
        t.classList.add('active');
        t.setAttribute('aria-current', 'page');
      } else {
        t.classList.remove('active');
        t.removeAttribute('aria-current');
      }
    }
  }

  // ── More sheet ──
  // Built lazily on first open; lives as a sibling of the <nav> so the
  // bottom-nav's z-index/stacking is independent of the sheet. The sheet
  // is anchored above the bottom-nav via CSS (bottom: <nav-height>).
  function getOrBuildSheet() {
    var existing = document.getElementById(SHEET_ID);
    if (existing) return existing;

    var sheet = document.createElement('div');
    sheet.id = SHEET_ID;
    sheet.className = 'bottom-nav-sheet';
    sheet.setAttribute('data-bottom-nav-sheet', '');
    sheet.setAttribute('role', 'menu');
    sheet.setAttribute('aria-label', 'More navigation');
    sheet.hidden = true;

    moreRoutes().forEach(function (r) {
      var a = document.createElement('a');
      a.className = 'bottom-nav-sheet-item';
      a.setAttribute('href', r.hash);
      a.setAttribute('role', 'menuitem');
      a.setAttribute('data-bottom-nav-more-route', r.route);
      a.setAttribute('data-route', r.route);

      var ic = document.createElement('span');
      ic.className = 'bottom-nav-sheet-icon';
      ic.setAttribute('aria-hidden', 'true');
      ic.innerHTML = phIconHTML(r.ph);

      var lb = document.createElement('span');
      lb.className = 'bottom-nav-sheet-label';
      lb.textContent = r.label;

      a.appendChild(ic);
      a.appendChild(lb);

      // Tap a route → close sheet (the <a href> handles navigation via
      // the existing hashchange router in app.js).
      a.addEventListener('click', function () { closeSheet(); });

      sheet.appendChild(a);
    });

    // Dark mode toggle — mirrors #darkModeToggle in the hidden top-nav.
    // Delegates the click to the real button so app.js owns all theme logic.
    var sep = document.createElement('hr');
    sep.className = 'bottom-nav-sheet-sep';
    sheet.appendChild(sep);

    var darkBtn = document.createElement('button');
    darkBtn.type = 'button';
    darkBtn.className = 'bottom-nav-sheet-item bottom-nav-sheet-darkbtn';
    darkBtn.setAttribute('role', 'menuitem');
    darkBtn.setAttribute('data-bottom-nav-dark-toggle', '');

    var darkIc = document.createElement('span');
    darkIc.className = 'bottom-nav-sheet-icon';
    darkIc.setAttribute('aria-hidden', 'true');

    var darkLb = document.createElement('span');
    darkLb.className = 'bottom-nav-sheet-label';

    darkBtn.appendChild(darkIc);
    darkBtn.appendChild(darkLb);
    sheet.appendChild(darkBtn);

    darkBtn.addEventListener('click', function () {
      var realToggle = document.getElementById('darkModeToggle');
      if (realToggle) realToggle.click();
      syncDarkModeBtn();
      closeSheet();
    });

    // Sit the sheet next to the nav so they share a stacking context.
    var nav = document.querySelector('[data-bottom-nav]');
    if (nav && nav.parentNode) {
      nav.parentNode.insertBefore(sheet, nav);
    } else {
      document.body.appendChild(sheet);
    }
    return sheet;
  }

  function syncDarkModeBtn() {
    var btn = document.querySelector('[data-bottom-nav-dark-toggle]');
    if (!btn) return;
    var isDark = document.documentElement.getAttribute('data-theme') === 'dark';
    btn.querySelector('.bottom-nav-sheet-icon').innerHTML = phIconHTML(isDark ? 'moon' : 'sun');
    btn.querySelector('.bottom-nav-sheet-label').textContent = isDark ? 'Light mode' : 'Dark mode';
  }

  function isSheetOpen() {
    var sheet = document.getElementById(SHEET_ID);
    return !!(sheet && !sheet.hidden);
  }

  function openSheet() {
    var sheet = getOrBuildSheet();
    sheet.hidden = false;
    sheet.classList.add('open');
    var moreTab = document.querySelector('[data-bottom-nav-tab="more"]');
    if (moreTab) {
      moreTab.setAttribute('aria-expanded', 'true');
      moreTab.classList.add('active');
    }
    syncDarkModeBtn();
  }

  function closeSheet() {
    var sheet = document.getElementById(SHEET_ID);
    if (sheet) {
      sheet.hidden = true;
      sheet.classList.remove('open');
    }
    var moreTab = document.querySelector('[data-bottom-nav-tab="more"]');
    if (moreTab) {
      moreTab.setAttribute('aria-expanded', 'false');
      moreTab.classList.remove('active');
    }
  }

  function toggleSheet() {
    if (isSheetOpen()) closeSheet();
    else openSheet();
  }

  function wireMoreSheet() {
    var moreTab = document.querySelector('[data-bottom-nav-tab="more"]');
    if (!moreTab) return;
    // Toggle on tap. Use click — covers mouse and synthesized tap.
    moreTab.addEventListener('click', function (ev) {
      ev.preventDefault();
      ev.stopPropagation();
      toggleSheet();
    });

    // Outside-click closes the sheet. Listen at document level; ignore
    // clicks on the sheet itself or on the More tab (handled above).
    document.addEventListener('click', function (ev) {
      if (!isSheetOpen()) return;
      var t = ev.target;
      var sheet = document.getElementById(SHEET_ID);
      if (sheet && sheet.contains(t)) return;
      if (moreTab.contains(t)) return;
      closeSheet();
    });

    // Tapping any OTHER bottom-nav tab also closes the sheet.
    var otherTabs = document.querySelectorAll('[data-bottom-nav-tab]');
    for (var i = 0; i < otherTabs.length; i++) {
      var t = otherTabs[i];
      if (t.getAttribute('data-bottom-nav-tab') === 'more') continue;
      t.addEventListener('click', function () { closeSheet(); });
    }

    // Esc closes the sheet (a11y).
    document.addEventListener('keydown', function (ev) {
      if (ev.key === 'Escape' && isSheetOpen()) closeSheet();
    });

    // Hashchange (any nav) also closes — covers programmatic navigation.
    window.addEventListener('hashchange', function () { closeSheet(); });
  }

  function init() {
    // Singleton guard: init() may be invoked twice if (a) DOMContentLoaded
    // fires AND (b) something else re-imports the script later, or if a
    // future SPA-like re-mount path is added. The internal `build()` is
    // idempotent (early-returns on existing [data-bottom-nav]), but the
    // `hashchange` listener and the document-level outside-click /
    // keydown listeners in wireMoreSheet() would otherwise stack, leaking
    // handlers exactly like PR #1180's MQL-leak class. Bail on second call.
    if (window.__bottomNavInitDone) return;
    window.__bottomNavInitDone = true;
    build();
    syncActive();
    window.addEventListener('hashchange', syncActive);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
