/* nav-drawer.js — Issue #1064 (parent epic #1052)
 *
 * Edge-swipe nav drawer. Slide-over from the LEFT edge.
 *
 * Design (Option A): drawer is enabled at viewport widths > 768px ONLY.
 * At ≤768px the bottom-nav has a "More" tab (PR #1174) that surfaces the
 * same long-tail routes; a left-edge drawer there would compete with it.
 *
 * Inputs (Pointer Events only — touch + pen, never mouse):
 *   - pointerdown within the left edge trigger zone [24px, 44px]
 *     (first 24px reserved for iOS Safari back-swipe — Mesh-Op #1184)
 *   - pointermove                            → drawer translateX follows finger
 *   - pointerup                              → settle open/closed via velocity
 *                                              + position threshold
 *
 * Singleton + cleanup (mirrors #1180 fix):
 *   - module-scoped `wired` guard so SPA mounts don't re-bind
 *   - document-level pointermove/pointerup listeners registered ONCE
 *   - matchMedia listener registered ONCE
 *   - `window.__navDrawerPointerBindCount` debug seam (E2E asserts ≤ 1)
 *
 * Accessibility:
 *   - drawer has `inert` when closed (removed when open) — keyboard +
 *     screen-reader users skip the off-screen tree.
 *   - focus trap: Tab from last focusable wraps to first; Shift+Tab from
 *     first wraps to last.
 *   - Esc closes; backdrop tap closes; tap on a route closes.
 *   - prefers-reduced-motion: instant snap, no transition.
 *
 * Public API (also surfaced as `window.__navDrawer` for tests):
 *   open(), close(), toggle(), isOpen()
 */
'use strict';

(function () {
  if (typeof document === 'undefined') return;

  // ── Module-scoped singleton state ───────────────────────────────────────
  var wired = false;
  var drawerEl = null;
  var backdropEl = null;
  var dragging = false;
  var startX = 0;
  var startY = 0;
  var startT = 0;
  var lastX = 0;
  var lastT = 0;
  var drawerWidth = 0;
  var pointerActive = false;
  var narrowMql = null;
  // Element that had focus before the drawer was opened — restored on close
  // (same regression class as #1168: closing nav UI must return focus to its
  // trigger so keyboard users don't get dumped at <body>).
  var prevFocus = null;

  // Long-tail routes mirror PR #1174 / bottom-nav.js MORE_ROUTES exactly.
  // !! Keep in sync with public/bottom-nav.js MORE_ROUTES.
  // `ph` is the Phosphor icon id (no "ph-" prefix); see public/icons/phosphor-sprite.svg.
  var ROUTES = [
    { route: 'nodes',     hash: '#/nodes',     label: 'Nodes',     ph: 'monitor' },
    { route: 'tools',     hash: '#/tools',     label: 'Tools',     ph: 'wrench' },
    { route: 'observers', hash: '#/observers', label: 'Observers', ph: 'eye' },
    { route: 'analytics', hash: '#/analytics', label: 'Analytics', ph: 'chart-bar' },
    { route: 'scope-audit', hash: '#/scope-audit', label: 'Scope Audit', ph: 'clipboard-text' },
    { route: 'perf',      hash: '#/perf',      label: 'Perf',      ph: 'lightning' },
    { route: 'audio-lab', hash: '#/audio-lab', label: 'Audio Lab', ph: 'music-note' },
  ];

  // Coverage is opt-in (config `clientRxCoverage`). roles.js injects its link
  // into the desktop top-nav only, so add it here too or it is unreachable via
  // the drawer on touch tablets (>768px). Evaluated at lazy build time, by which
  // point MeshConfigReady has resolved window.MC_CLIENT_RX_COVERAGE.
  var COVERAGE_ROUTE = { route: 'rx-coverage', hash: '#/rx-coverage', label: 'Coverage', ph: 'broadcast' };

  function routes() {
    if (!window.MC_CLIENT_RX_COVERAGE) return ROUTES;
    var out = ROUTES.slice();
    var after = out.findIndex(function (r) { return r.route === 'analytics'; }) + 1;
    out.splice(after, 0, COVERAGE_ROUTE);
    return out;
  }

  function phIconHTML(name) {
    return '<svg class="ph-icon" aria-hidden="true" focusable="false">' +
           '<use href="/icons/phosphor-sprite.svg#ph-' + name + '"></use></svg>';
  }

  // ── Version footer (frontend version display) ───────────────────────────
  // GET /api/health reports {version, commit, buildTime}. Fetch once and cache
  // the promise for the page lifetime so re-opening the drawer doesn't refetch.
  var versionPromise = null;
  function fetchVersion() {
    if (versionPromise) return versionPromise;
    versionPromise = fetch('/api/health', { headers: { Accept: 'application/json' } })
      .then(function (r) { return r.ok ? r.json() : null; })
      .catch(function () { return null; });
    return versionPromise;
  }
  function fillVersion(el) {
    fetchVersion().then(function (h) {
      if (!h || !h.version) return; // leave the neutral "CoreScope" label as-is
      el.textContent = 'CoreScope ' + h.version;
      var bits = [];
      if (h.commit) bits.push('commit ' + h.commit);
      if (h.buildTime) bits.push('built ' + h.buildTime);
      if (bits.length) el.title = bits.join(' \u00B7 ');
    });
  }

  var EDGE_PX = 44;          // pointerdown must start within left N px (drawer trigger zone)
  var EDGE_MIN_PX = 24;      // first N px reserved for iOS Safari back-swipe (do not claim)
  var NARROW_MAX = 768;      // Option A: disabled at ≤ this width
  var OPEN_THRESHOLD = 0.5;  // % of drawer width at which open settles
  var VELOCITY_OPEN = 0.4;   // px/ms — fling-right opens regardless of position
  var VELOCITY_CLOSE = -0.4; // px/ms — fling-left closes

  function isWide() {
    // matchMedia is the source of truth; fall back to innerWidth in non-DOM
    // environments (won't trigger in browser).
    if (narrowMql && typeof narrowMql.matches === 'boolean') return !narrowMql.matches;
    return (window.innerWidth || 0) > NARROW_MAX;
  }

  function prefersReducedMotion() {
    try {
      return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    } catch (_e) { return false; }
  }

  // ── DOM construction (idempotent) ───────────────────────────────────────
  function buildDom() {
    if (drawerEl && backdropEl) return;

    backdropEl = document.createElement('div');
    backdropEl.className = 'nav-drawer-backdrop';
    backdropEl.setAttribute('data-nav-drawer-backdrop', '');
    backdropEl.hidden = true;
    backdropEl.addEventListener('click', function () { close(); });

    drawerEl = document.createElement('aside');
    drawerEl.className = 'nav-drawer';
    drawerEl.setAttribute('data-nav-drawer', '');
    drawerEl.setAttribute('role', 'navigation');
    drawerEl.setAttribute('aria-label', 'Edge-swipe navigation drawer');
    drawerEl.setAttribute('aria-hidden', 'true');
    drawerEl.setAttribute('inert', '');
    drawerEl.tabIndex = -1;

    var header = document.createElement('div');
    header.className = 'nav-drawer-header';
    var title = document.createElement('span');
    title.className = 'nav-drawer-title';
    title.textContent = 'Navigate';
    var closeBtn = document.createElement('button');
    closeBtn.type = 'button';
    closeBtn.className = 'nav-drawer-close';
    closeBtn.setAttribute('aria-label', 'Close navigation drawer');
    closeBtn.textContent = '×';
    closeBtn.addEventListener('click', function () { close(); });
    header.appendChild(title);
    header.appendChild(closeBtn);
    drawerEl.appendChild(header);

    var list = document.createElement('nav');
    list.className = 'nav-drawer-list';
    routes().forEach(function (r) {
      var a = document.createElement('a');
      a.className = 'nav-drawer-item';
      a.setAttribute('href', r.hash);
      a.setAttribute('data-nav-drawer-item', r.route);
      a.setAttribute('data-route', r.route);

      var ic = document.createElement('span');
      ic.className = 'nav-drawer-icon';
      ic.setAttribute('aria-hidden', 'true');
      ic.innerHTML = phIconHTML(r.ph);

      var lb = document.createElement('span');
      lb.className = 'nav-drawer-label';
      lb.textContent = r.label;

      a.appendChild(ic);
      a.appendChild(lb);
      a.addEventListener('click', function () { close(); });
      list.appendChild(a);
    });
    drawerEl.appendChild(list);

    var footer = document.createElement('div');
    footer.className = 'nav-drawer-footer';
    var ver = document.createElement('a');
    ver.className = 'nav-drawer-version';
    ver.setAttribute('data-nav-drawer-version', '');
    ver.href = 'https://github.com/Kpa-clawbot/CoreScope/releases';
    ver.target = '_blank';
    ver.rel = 'noopener noreferrer';
    ver.textContent = 'CoreScope';
    footer.appendChild(ver);
    drawerEl.appendChild(footer);
    fillVersion(ver);

    document.body.appendChild(backdropEl);
    document.body.appendChild(drawerEl);

    // Defer width measurement until after layout.
    requestAnimationFrame(function () {
      drawerWidth = drawerEl.getBoundingClientRect().width || 320;
    });
  }

  // ── Open/close primitives ───────────────────────────────────────────────
  function setTranslate(px) {
    if (!drawerEl) return;
    drawerEl.style.transform = 'translateX(' + px + 'px)';
  }

  function clearInlineTransform() {
    if (drawerEl) drawerEl.style.transform = '';
  }

  function isOpen() {
    return !!(drawerEl && drawerEl.classList.contains('is-open'));
  }

  function open() {
    buildDom();
    if (!isWide()) return; // Option A
    if (!drawerWidth) drawerWidth = drawerEl.getBoundingClientRect().width || 320;
    // Capture the previously-focused element BEFORE we move focus, so close()
    // can restore it. Guard against opening twice (don't overwrite on re-open).
    if (!isOpen()) {
      try {
        var ae = document.activeElement;
        prevFocus = (ae && ae !== document.body) ? ae : null;
      } catch (_e) { prevFocus = null; }
    }
    drawerEl.classList.add('is-open');
    drawerEl.removeAttribute('inert');
    drawerEl.setAttribute('aria-hidden', 'false');
    backdropEl.hidden = false;
    backdropEl.classList.add('is-open');
    clearInlineTransform();
    // Move focus into the drawer for keyboard users / screen readers.
    var firstFocusable = drawerEl.querySelector(
      'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"]), input, select, textarea'
    );
    if (firstFocusable) {
      try { firstFocusable.focus({ preventScroll: true }); } catch (_e) { firstFocusable.focus(); }
    }
  }

  function close() {
    if (!drawerEl) return;
    var wasOpen = drawerEl.classList.contains('is-open');
    // Decide whether to restore focus BEFORE applying `inert`. Setting
    // `inert` synchronously moves document.activeElement to <body>, so any
    // "is focus inside the drawer?" check after that point is useless.
    // The right invariant: restore if we were open, prevFocus is still in
    // the DOM, and it isn't a descendant of the drawer itself.
    var toRestore = null;
    if (wasOpen && prevFocus && typeof prevFocus.focus === 'function' &&
        document.body && document.body.contains(prevFocus) &&
        !drawerEl.contains(prevFocus)) {
      toRestore = prevFocus;
    }
    prevFocus = null;
    // Restore FIRST so the upcoming `inert` doesn't bump us to <body>.
    if (toRestore) {
      try { toRestore.focus({ preventScroll: true }); }
      catch (_e) { /* element may be gone after SPA nav — ignore */ }
    }
    drawerEl.classList.remove('is-open');
    drawerEl.setAttribute('inert', '');
    drawerEl.setAttribute('aria-hidden', 'true');
    if (backdropEl) {
      backdropEl.hidden = true;
      backdropEl.classList.remove('is-open');
    }
    clearInlineTransform();
  }

  function toggle() { if (isOpen()) close(); else open(); }

  // ── Pointer drag-tracking ───────────────────────────────────────────────
  function onPointerDown(e) {
    // Mesh-Op review (PR #1184): only respond to touch + pen. Mouse drags
    // from the left edge must NOT open the drawer (a stray mouse-down at
    // x<EDGE_PX would otherwise hijack a click). Filter BEFORE any
    // edge-zone math so the rest of the handler stays touch/pen-only.
    if (e.pointerType !== 'touch' && e.pointerType !== 'pen') return;
    if (!isWide()) return;
    var x = e.clientX;
    if (isOpen()) {
      // Allow drag-to-close from anywhere inside drawer's left half.
      if (!drawerEl) return;
      var r = drawerEl.getBoundingClientRect();
      if (x > r.right) return;
    } else {
      // Drawer trigger zone: [EDGE_MIN_PX, EDGE_PX]. The first EDGE_MIN_PX
      // are reserved for iOS Safari's system back-swipe gesture (Mesh-Op
      // review on #1184); claiming x < 24 collides with the OS gesture and
      // leaves iPad users with a flaky double-fire.
      if (x < EDGE_MIN_PX) return;
      if (x > EDGE_PX) return;
    }
    buildDom();
    if (!drawerWidth) drawerWidth = drawerEl.getBoundingClientRect().width || 320;
    dragging = true;
    pointerActive = true;
    startX = lastX = x;
    startY = e.clientY;
    startT = lastT = (e.timeStamp || performance.now());
  }

  function onPointerMove(e) {
    if (!dragging || !pointerActive) return;
    var x = e.clientX;
    var y = e.clientY;
    // If the gesture is mostly vertical near the start, abandon (let scroll win).
    if (Math.abs(x - startX) < 8 && Math.abs(y - startY) > 12) {
      dragging = false;
      pointerActive = false;
      clearInlineTransform();
      return;
    }
    lastX = x;
    lastT = (e.timeStamp || performance.now());
    if (prefersReducedMotion()) return; // no live tracking — settle on up
    // Compute drawer x-position based on whether we started open or closed.
    var basis = isOpen() ? 0 : -drawerWidth;
    var delta = x - startX;
    var px = Math.max(-drawerWidth, Math.min(0, basis + delta));
    setTranslate(px);
  }

  function onPointerUp(e) {
    if (!pointerActive) return;
    pointerActive = false;
    if (!dragging) { clearInlineTransform(); return; }
    dragging = false;
    var x = (e && typeof e.clientX === 'number') ? e.clientX : lastX;
    var t = (e && e.timeStamp) || performance.now();
    var dt = Math.max(1, t - startT);
    var velocity = (x - startX) / dt; // px/ms
    var openedBefore = isOpen();
    clearInlineTransform();
    if (openedBefore) {
      if (velocity < VELOCITY_CLOSE || (x - startX) < -drawerWidth * OPEN_THRESHOLD) {
        close();
      } else {
        open();
      }
    } else {
      if (velocity > VELOCITY_OPEN || (x - startX) > drawerWidth * OPEN_THRESHOLD) {
        open();
      } else {
        close();
      }
    }
  }

  // ── Focus trap ──────────────────────────────────────────────────────────
  function onKeydown(e) {
    if (!isOpen()) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
      return;
    }
    if (e.key !== 'Tab' || !drawerEl) return;
    var focusables = drawerEl.querySelectorAll(
      'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"]), input, select, textarea'
    );
    if (focusables.length === 0) return;
    var first = focusables[0];
    var last = focusables[focusables.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }

  // ── Wire-up (called once) ───────────────────────────────────────────────
  function wireOnce() {
    if (wired) return;
    wired = true;

    try { narrowMql = window.matchMedia('(max-width: ' + NARROW_MAX + 'px)'); }
    catch (_e) { narrowMql = null; }

    document.addEventListener('pointerdown', onPointerDown, { passive: true });
    document.addEventListener('pointermove', onPointerMove, { passive: true });
    document.addEventListener('pointerup',   onPointerUp,   { passive: true });
    document.addEventListener('pointercancel', onPointerUp, { passive: true });
    document.addEventListener('keydown', onKeydown);

    // Close drawer if viewport drops to narrow (Option A).
    if (narrowMql && typeof narrowMql.addEventListener === 'function') {
      narrowMql.addEventListener('change', function () { if (!isWide()) close(); });
    }

    // Debug seam — E2E asserts this ≤ 1 across SPA navs (singleton proof).
    window.__navDrawerPointerBindCount = (window.__navDrawerPointerBindCount || 0) + 1;
  }

  function init() {
    wireOnce();
    buildDom();
  }

  // Public API for tests + manual triggers (e.g. a hamburger button).
  window.__navDrawer = { open: open, close: close, toggle: toggle, isOpen: isOpen };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init, { once: true });
  } else {
    init();
  }
})();
