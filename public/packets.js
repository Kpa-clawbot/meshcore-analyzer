/* === CoreScope — packets.js === */
'use strict';

/* === #1056: TableResponsive — fluid columns + "+N hidden" pill ============
 * Tiny helper, defined once, used by packets/nodes/observers tables.
 *
 * Usage: TableResponsive.apply(tableEl)
 *
 * Each <th> may carry a `data-priority` attribute (1=keep always, higher
 * numbers = drop first as viewport narrows). Default priority is 1.
 *
 * apply() measures the container width and progressively hides the highest-
 * priority columns (and matching <td>s) until the table's natural scrollWidth
 * fits, then renders a "+N hidden" pill in the last visible <th>. Click the
 * pill to reveal all hidden columns until the next layout pass.
 *
 * Re-runs on window resize (debounced) and is idempotent — safe to call after
 * every render. ResizeObserver on the wrapping element also triggers re-fit.
 */
(function () {
  if (window.TableResponsive) return;

  const REVEAL_FLAG = '__tr_reveal';
  const PILL_CLASS = 'col-hidden-pill';
  const HIDDEN_CLASS = 'col-hidden';

  function thsOf(table) { return Array.from(table.querySelectorAll('thead > tr > th')); }

  function clearHidden(table) {
    table.querySelectorAll('.' + HIDDEN_CLASS).forEach(el => el.classList.remove(HIDDEN_CLASS));
    const pill = table.querySelector('.' + PILL_CLASS);
    if (pill) pill.remove();
  }

  function colIndexCells(table, idx) {
    // Return the <td> at column index `idx` for every body row.
    const out = [];
    const rows = table.querySelectorAll('tbody > tr');
    rows.forEach(r => {
      // colSpan-aware mapping: walk cells, accumulate colspans.
      let i = 0;
      for (const cell of r.children) {
        const span = cell.colSpan || 1;
        if (i <= idx && idx < i + span) { out.push(cell); break; }
        i += span;
      }
    });
    return out;
  }

  function apply(table) {
    if (!table || !table.isConnected) return;
    if (table[REVEAL_FLAG]) {
      // user explicitly requested reveal — clear hidden state and skip
      clearHidden(table);
      return;
    }
    clearHidden(table);

    const ths = thsOf(table);
    if (ths.length === 0) return;

    // Viewport-breakpoint hiding (per issue #1056 acceptance criteria):
    //   data-priority on each <th>:
    //     1 → always visible
    //     2 → hide when viewport ≤ 1280
    //     3 → hide when viewport ≤ 1024  (per AC #1 wording)
    //     4 → hide when viewport ≤  900
    //     5 → hide when viewport ≤  768
    // Higher priority numbers drop FIRST (least important).
    // Drop direction: a column is hidden if its breakpoint ≥ current viewport.
    const BP = { 2: 1280, 3: 1024, 4: 900, 5: 768 };
    const vw = window.innerWidth || document.documentElement.clientWidth;

    const candidates = ths
      .map((th, i) => ({ th, i, prio: parseInt(th.getAttribute('data-priority') || '1', 10) }))
      .filter(c => c.prio > 1 && BP[c.prio] !== undefined && vw <= BP[c.prio])
      // hide highest priority numbers first (drop-first), then right-to-left ties
      .sort((a, b) => b.prio - a.prio || b.i - a.i);

    let hidden = 0;
    for (const c of candidates) {
      c.th.classList.add(HIDDEN_CLASS);
      colIndexCells(table, c.i).forEach(td => td.classList.add(HIDDEN_CLASS));
      hidden++;
    }

    if (hidden > 0) {
      const visible = ths.filter(th => !th.classList.contains(HIDDEN_CLASS));
      const host = visible[visible.length - 1] || ths[0];
      const pill = document.createElement('button');
      pill.type = 'button';
      pill.className = PILL_CLASS;
      pill.textContent = '+' + hidden + ' hidden';
      pill.title = 'Click to reveal hidden columns';
      pill.setAttribute('aria-label', hidden + ' columns hidden — click to reveal');
      pill.addEventListener('click', function (ev) {
        ev.stopPropagation();
        ev.preventDefault();
        table[REVEAL_FLAG] = true;
        clearHidden(table);
        // Add a small "hide again" affordance after reveal so the user isn't stuck.
        const rehide = document.createElement('button');
        rehide.type = 'button';
        rehide.className = PILL_CLASS + ' col-rehide-pill';
        rehide.textContent = 'hide';
        rehide.title = 'Re-hide collapsed columns';
        rehide.setAttribute('aria-label', 'Re-hide previously collapsed columns');
        rehide.addEventListener('click', function (ev2) {
          ev2.stopPropagation();
          ev2.preventDefault();
          table[REVEAL_FLAG] = false;
          apply(table);
        });
        rehide.addEventListener('keydown', function (ev2) {
          // Prevent Enter/Space from bubbling up to TableSort handler on the <th>.
          if (ev2.key === 'Enter' || ev2.key === ' ') ev2.stopPropagation();
        });
        host.appendChild(rehide);
      });
      // MAJOR-3: prevent Enter/Space keydown on the pill from bubbling to the
      // <th>'s TableSort keydown handler (which would also trigger a sort).
      pill.addEventListener('keydown', function (ev) {
        if (ev.key === 'Enter' || ev.key === ' ') ev.stopPropagation();
      });
      host.appendChild(pill);
    }
  }

  // Track tables we've wired up so resize triggers re-apply.
  // Map<table, ResizeObserver|null> — we need the RO ref so SPA remounts can
  // disconnect the orphaned observer when its <table> leaves the DOM.
  // Pre-#1213 fix this was a Set and each /nodes (or /packets/observers)
  // remount registered a fresh RO against a freshly-rendered table while the
  // previous table+RO sat detached but observed → 1 leaked RO per remount.
  const wired = new Map();
  // Track last-seen wrap width per table so we only treat ACTUAL container
  // resizes as a reason to drop the user's reveal state. Hiding/showing
  // columns and removing the pill mutate layout and re-trigger ResizeObserver,
  // which would otherwise immediately stomp on the reveal the user just asked for.
  const lastWrapW = new WeakMap();
  // Parallel map of tbody MutationObservers per wired table. Separate from
  // `wired` (which stores the ResizeObserver) so the existing sweep/dispose
  // contract on `wired` stays unchanged for callers reading its shape.
  const wiredMO = new WeakMap();
  // Sweep tables that have been detached from the DOM (e.g. SPA destroyed
  // their page) and release their ResizeObserver. Called opportunistically on
  // every register() — cheap O(n) over wired tables, n is tiny in practice.
  function sweepDetached() {
    wired.forEach((ro, t) => {
      if (!t || !t.isConnected) {
        if (ro) { try { ro.disconnect(); } catch (_) {} }
        const mo = wiredMO.get(t);
        if (mo) { try { mo.disconnect(); } catch (_) {} wiredMO.delete(t); }
        wired.delete(t);
      }
    });
  }
  function register(table) {
    sweepDetached();
    if (!table || wired.has(table)) { apply(table); return; }
    let ro = null;
    if (typeof ResizeObserver !== 'undefined') {
      const wrap = table.closest('.table-fluid-wrap, .obs-table-scroll, .table-scroll-wrap') || table.parentElement;
      if (wrap) {
        lastWrapW.set(table, wrap.clientWidth || 0);
        ro = new ResizeObserver(() => {
          const prev = lastWrapW.get(table) || 0;
          const cur = wrap.clientWidth || 0;
          // Ignore self-induced layout reflows from apply()/clearHidden() —
          // they don't change the wrap width. Only real viewport/container
          // changes (>2px) clear the reveal flag.
          if (Math.abs(cur - prev) <= 2) return;
          lastWrapW.set(table, cur);
          table[REVEAL_FLAG] = false;
          apply(table);
        });
        ro.observe(wrap);
      }
    }
    // #1693 r3: re-apply on tbody mutations SYNCHRONOUSLY. Incremental
    // renderTableRows() calls (observer-decorated re-render, HopResolver
    // re-render, etc.) add fresh <td>s after register() ran. Without
    // re-running apply() those new cells miss the `col-hidden` class until
    // the next resize/window event, which made the mobile E2E flake
    // (`td.col-observer` visible at 375px).
    //
    // r2 used a 100ms setTimeout debounce; the mobile E2E test
    // (test-observer-iata-1188) reads `td.col-observer` display IMMEDIATELY
    // after waitForSelector(first row) — the debounce hadn't fired yet, so
    // the test still saw the unhidden column. Run apply() synchronously on
    // each mutation: apply() is cheap (single pass over header + cells,
    // toggling a class) and idempotent, so consecutive mutations in a
    // render burst just re-run the same cheap loop.
    const tbody = table.querySelector('tbody');
    if (tbody && typeof MutationObserver !== 'undefined') {
      const mo = new MutationObserver(() => {
        if (!table.isConnected) return;
        apply(table);
      });
      mo.observe(tbody, { childList: true });
      wiredMO.set(table, mo);
    }
    wired.set(table, ro);
    apply(table);
  }

  let _winTimer = null;
  window.addEventListener('resize', function () {
    clearTimeout(_winTimer);
    _winTimer = setTimeout(() => {
      wired.forEach((ro, t) => {
        if (!t.isConnected) {
          if (ro) { try { ro.disconnect(); } catch (_) {} }
          wired.delete(t);
          return;
        }
        t[REVEAL_FLAG] = false;
        apply(t);
      });
    }, 120);
  });

  window.TableResponsive = { apply, register, sweep: sweepDetached };
})();

/* === #1056 AC#4: SlideOver — narrow-viewport row-detail overlay ============
 * Singleton backdrop + right-anchored panel injected into <body>. Used by
 * packets/nodes/observers when window.innerWidth <= SLIDE_OVER_BP (1023,
 * matching the data-priority="3" breakpoint reused by TableResponsive).
 *
 *   SlideOver.shouldUse()   → boolean (current viewport <= breakpoint)
 *   SlideOver.open(opts)    → returns the inner content element. opts:
 *     { title?: string, onClose?: function, restoreFocus?: () => Element|null }
 *     `restoreFocus` (optional) overrides the auto-captured
 *     `document.activeElement` and is invoked at close time to look up the
 *     element to focus. Use this when the caller re-renders the originating
 *     row before/after opening (which would otherwise detach the focused
 *     row from the DOM and leave nothing for auto-restore to find).
 *   SlideOver.close()       → close + dispatch onClose
 *   SlideOver.isOpen()      → boolean
 *
 * Close affordances: X button (.slide-over-close), backdrop click, Escape.
 * Reuses `slideInRight` keyframe in style.css.
 */
(function () {
  if (window.SlideOver) return;

  // #1168 Munger #3: shared, ref-counted scroll-lock helper. Multiple
  // modal surfaces (SlideOver, ChannelColorPicker, future modals) call
  // acquire()/release() with their own token; the body keeps the
  // `scroll-locked` class (CSS supplies overflow:hidden in style.css)
  // for as long as the count > 0. Last release removes the class.
  // This replaces the previous capture-and-restore-string approach
  // which corrupted body.style.overflow under last-writer-wins races.
  if (!window.__scrollLock) {
    let count = 0;
    let next = 1;
    const live = new Set();
    function acquire() {
      const token = next++;
      live.add(token);
      count++;
      if (count === 1) document.body.classList.add('scroll-locked');
      return token;
    }
    function release(token) {
      if (token == null || !live.has(token)) return;
      live.delete(token);
      count--;
      if (count <= 0) {
        count = 0;
        document.body.classList.remove('scroll-locked');
      }
    }
    window.__scrollLock = { acquire: acquire, release: release };
  }

  const BP = 1023;
  let backdrop = null, panel = null, content = null, closeCb = null;
  let prevFocus = null, prevFocusResolver = null;
  // #1168 Munger #1: openSeq counter so a stale rAF from close() can
  // detect a newer open() happened in between and skip its focus call.
  let openSeq = 0;
  // #1168 Munger #3: ref-counted scroll-lock token held by THIS surface
  // (multiple SlideOver opens reuse the same token; only paired with a
  // matching release on close).
  let scrollLockToken = null;
  // #1616: open state is now tracked by a flag instead of panel.hidden.
  // The architectural close path DETACHES panel + backdrop from the DOM
  // (removeChild) — there is no "focused-but-hidden" intermediate state
  // for Chromium-headless to race against. See close() below.
  let panelOpen = false;
  // #1616: list of pending one-shot MutationObservers (per-close-invocation)
  // waiting for a row to re-attach so we can land focus on the new instance.
  // Tracked so a follow-up close()/open() can disconnect any stragglers.
  let pendingFocusObservers = [];

  function disconnectFocusObservers() {
    for (var i = 0; i < pendingFocusObservers.length; i++) {
      try { pendingFocusObservers[i].disconnect(); } catch (_) {}
    }
    pendingFocusObservers = [];
  }

  function ensureNodes() {
    if (panel && backdrop) return;
    backdrop = document.createElement('div');
    backdrop.className = 'slide-over-backdrop';
    // #1616: state is data-state="open|closed"; closed nodes are DETACHED
    // from the DOM in close() so panel.hidden never has to be true while
    // anything inside (or the panel itself) holds focus. We keep
    // backdrop.hidden as a defensive fallback for the brief window after
    // ensureNodes() runs before the first open() attaches them.
    backdrop.hidden = true;
    backdrop.setAttribute('data-state', 'closed');
    // Backdrop is decorative — assistive tech should not announce it.
    backdrop.setAttribute('aria-hidden', 'true');
    backdrop.addEventListener('click', function () { close(); });

    panel = document.createElement('aside');
    panel.className = 'slide-over-panel';
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-modal', 'true');
    // #1168 must-fix #4: a static aria-label="Detail" would override the
    // meaningful <h3 id="slideOverTitle"> (e.g. "Packet ab12cd…", node name)
    // for screen-reader users. Use aria-labelledby so the announced name
    // is the actual title rendered into the panel.
    panel.setAttribute('aria-labelledby', 'slideOverTitle');
    panel.hidden = true;
    panel.setAttribute('data-state', 'closed');
    panel.tabIndex = -1;
    panel.innerHTML =
      '<div class="slide-over-header">' +
        '<h3 class="slide-over-title" id="slideOverTitle"></h3>' +
        '<button type="button" class="slide-over-close" aria-label="Close detail (Esc)" title="Close"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></button>' +
      '</div>' +
      '<div class="slide-over-content"></div>';
    panel.querySelector('.slide-over-close').addEventListener('mousedown', function (e) {
      // Prevent the X from stealing focus on pointer-press. Without this,
      // Chromium focuses the button on mousedown → close() runs while X has
      // focus → hiding the panel triggers an implicit blur to <body> that
      // races with (and clobbers) our row-focus-restore. With this guard,
      // the originating row keeps focus throughout the click → the post-
      // close rAF restore runs unopposed.
      e.preventDefault();
    });
    panel.querySelector('.slide-over-close').addEventListener('click', function (e) {
      e.preventDefault();
      e.stopPropagation();
      close();
    });
    // Focus trap: keep Tab cycling inside the panel while open.
    panel.addEventListener('keydown', function (e) {
      if (e.key !== 'Tab' || !isOpen()) return;
      const focusables = panel.querySelectorAll(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
      );
      if (!focusables.length) return;
      const first = focusables[0], last = focusables[focusables.length - 1];
      const active = document.activeElement;
      if (e.shiftKey && (active === first || active === panel)) {
        e.preventDefault();
        try { last.focus(); } catch {}
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        try { first.focus(); } catch {}
      }
    });
    // #1616: do NOT attach panel + backdrop to <body> on ensureNodes().
    // They are appended in open() and removed in close() so the closed
    // state is "not in the DOM" — Chromium-headless cannot land focus on
    // something that isn't there.

    // Single Escape handler shared across all uses.
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && isOpen()) {
        e.stopPropagation();
        close();
      }
    });

    // #1168 Munger #2: hashchange cleanup. Without this, navigating from
    // /#/packets to /#/nodes via location.hash leaves panel + backdrop +
    // scroll-lock dangling across pages. Registered once with the other
    // singleton listeners.
    //
    // Scope: only close on PAGE-route changes (first hash segment), not
    // on within-page detail navigation. Observers (and others) write
    // /#/observers/<id> when opening a row; that hashchange must NOT
    // close the slide-over we just opened.
    window.addEventListener('hashchange', function (e) {
      if (!isOpen()) return;
      function pageOf(hash) {
        var m = String(hash || '').match(/^#?\/?([^\/?#]+)/);
        return m ? m[1] : '';
      }
      var oldPage = pageOf(e && e.oldURL ? e.oldURL.split('#')[1] || '' : '');
      var newPage = pageOf(e && e.newURL ? e.newURL.split('#')[1] || '' : location.hash);
      if (oldPage !== newPage) close();
    });
  }

  function shouldUse() {
    return (window.innerWidth || document.documentElement.clientWidth) <= BP;
  }

  function isOpen() {
    // #1616: open state is the panelOpen flag, NOT (!panel.hidden).
    // When closed the panel is DETACHED from <body>, so a `.hidden`
    // check on a node nothing roots to <body> is meaningless.
    return panelOpen;
  }

  function open(opts) {
    // If already open, properly close the prior caller first so its onClose
    // (which clears `selectedKey`/hash state) fires before we replace it.
    if (isOpen()) close();
    ensureNodes();
    opts = opts || {};
    // #1168 Munger #1: bump open sequence so any pending rAF from a
    // prior close() can detect that a newer open has happened and skip
    // its stale focus-restore.
    openSeq++;
    // #1616: a fresh open() invalidates any pending focus-observers
    // from a recent close() — disconnect them so they can't land a
    // stale focus on row A after row B has opened.
    disconnectFocusObservers();
    closeCb = typeof opts.onClose === 'function' ? opts.onClose : null;
    // If the caller passes restoreFocus(), it owns lookup at close-time —
    // useful when the caller re-renders the row table (which would detach
    // any auto-captured prevFocus DOM node).
    prevFocusResolver = typeof opts.restoreFocus === 'function' ? opts.restoreFocus : null;
    // Remember what was focused so we can restore on close.
    prevFocus = (document.activeElement && document.activeElement !== document.body)
      ? document.activeElement : null;
    // #1168 Munger #3: ref-counted scroll-lock — class-based, not value-restore.
    // Survives interleaved lockers (other modals can also acquire/release).
    if (scrollLockToken == null) {
      scrollLockToken = window.__scrollLock.acquire();
    }
    const title = panel.querySelector('.slide-over-title');
    title.textContent = opts.title || 'Detail';
    content = panel.querySelector('.slide-over-content');
    content.innerHTML = '';
    // #1616: ATTACH panel + backdrop now. They were removed (or never
    // appended) by the prior close(). Order matters — backdrop first so
    // it sits beneath the panel in source/paint order.
    if (backdrop.parentNode !== document.body) document.body.appendChild(backdrop);
    if (panel.parentNode !== document.body) document.body.appendChild(panel);
    backdrop.hidden = false;
    panel.hidden = false;
    backdrop.setAttribute('data-state', 'open');
    panel.setAttribute('data-state', 'open');
    panelOpen = true;
    // Focus the close button so Esc/Enter works without an extra tab.
    const x = panel.querySelector('.slide-over-close');
    if (x) try { x.focus(); } catch {}
    return content;
  }

  function close() {
    if (!panel || !panelOpen) return;
    // #1168 Munger #3: release the ref-counted scroll-lock token.
    if (scrollLockToken != null) {
      window.__scrollLock.release(scrollLockToken);
      scrollLockToken = null;
    }
    const cb = closeCb;
    closeCb = null;
    const resolver = prevFocusResolver;
    let toFocus = prevFocus;
    prevFocus = null;
    prevFocusResolver = null;
    const seqAtClose = openSeq;

    // Fire the user's onClose BEFORE we touch focus. Callers typically
    // re-render the originating table inside cb() (e.g. clearing the
    // selected row state), which detaches the originating <tr>; we
    // intentionally re-look-up via the resolver below.
    if (cb) try { cb(); } catch {}

    // Resolve the focus target by DATA-VALUE LOOKUP AT RESTORE TIME.
    // #1616: no captured-element-ref path — if cb() re-rendered the
    // tbody, the captured prevFocus is now an orphaned node detached
    // from the document, and Chromium-headless will silently swallow
    // a .focus() call on it (activeElement falls back to <body>).
    if (resolver) {
      try {
        const resolved = resolver();
        if (resolved) toFocus = resolved;
      } catch (_) {}
    }

    // #1616 (architectural): SYNCHRONOUSLY focus the target row BEFORE
    // the panel/backdrop are detached. This eliminates the
    // focused-but-hidden intermediate state Chromium-headless reasons
    // about non-deterministically. If activeElement is inside the panel
    // when the panel detaches, focus drops to <body>; landing focus on
    // the row first means the detach is a no-op for the active element.
    // The deferred microtask + rAF + observer chain below handles the
    // case where cb() queued an async re-render that detaches this row.
    if (toFocus && typeof toFocus.focus === 'function' && document.body.contains(toFocus)) {
      try { toFocus.focus({ preventScroll: true }); } catch (_) {}
    }

    // DETACH panel + backdrop. After this point, there is no "panel
    // node sitting hidden in the document tree" for Chromium to race
    // a stale blur against.
    panelOpen = false;
    panel.setAttribute('data-state', 'closed');
    backdrop.setAttribute('data-state', 'closed');
    if (content) content.innerHTML = '';
    panel.hidden = true;
    backdrop.hidden = true;
    if (panel.parentNode) panel.parentNode.removeChild(panel);
    if (backdrop.parentNode) backdrop.parentNode.removeChild(backdrop);

    // If sync focus landed: schedule a microtask re-check. The
    // caller's cb() may have queued an ASYNC re-render (microtask
    // or rAF) that detaches the row AFTER our sync focus() — in that
    // case activeElement falls to <body> AFTER this function returns,
    // and we need a second-pass focus restore. queueMicrotask() runs
    // AFTER any Promise.resolve().then(...) the caller queued from
    // inside cb() (FIFO microtask queue), so re-resolving here picks
    // up the NEW row instance.
    if (!resolver) {
      // No resolver = no second-pass capability; best-effort done.
      return;
    }

    // Always run the deferred re-check pass, even if sync focus
    // appeared to land — it may be on a soon-to-be-detached orphan.
    function deferredRecheck() {
      // Stale guard — newer open() bumped openSeq; bail.
      if (openSeq !== seqAtClose) return;
      // If focus already landed on something non-body that matches
      // the resolver, we're done.
      let fresh = null;
      try { fresh = resolver(); } catch (_) {}
      if (fresh && document.body.contains(fresh)) {
        if (document.activeElement === fresh) return;
        try { fresh.focus({ preventScroll: true }); } catch (_) {}
        if (document.activeElement === fresh) return;
      }
      // Still not landed. Attach a one-shot MutationObserver rooted
      // at document.body — broader than the prior tbody-rooted
      // observer (which fails when the tbody itself is innerHTML-
      // swapped), still bounded by a short timeout below.
      attachBodyObserver();
    }

    function attachBodyObserver() {
      const obs = new MutationObserver(function () {
        if (openSeq !== seqAtClose) {
          obs.disconnect();
          const idx = pendingFocusObservers.indexOf(obs);
          if (idx >= 0) pendingFocusObservers.splice(idx, 1);
          return;
        }
        let target = null;
        try { target = resolver(); } catch (_) {}
        if (!target || !document.body.contains(target)) return;
        try { target.focus({ preventScroll: true }); } catch (_) {}
        if (document.activeElement === target) {
          obs.disconnect();
          const idx = pendingFocusObservers.indexOf(obs);
          if (idx >= 0) pendingFocusObservers.splice(idx, 1);
        }
      });
      // #1616 followup: root = document.body. The prior 'smart' tbody
      // root logic broke when cb() replaced the entire tbody via
      // innerHTML — the observer was watching a node that no longer
      // received the mutation we cared about. document.body is broader
      // but correctly catches the re-attachment in all known callsites
      // (nodes/packets/observers all swap rows under tbody under body).
      obs.observe(document.body, { childList: true, subtree: true });
      pendingFocusObservers.push(obs);
      // Tight timeout — 200ms is enough for any sync or microtask/rAF
      // re-render to commit. Longer (e.g. the prior 2s) creates new
      // races where the observer fires AFTER a subsequent open() and
      // steals focus from row B back to row A.
      setTimeout(function () {
        obs.disconnect();
        const idx = pendingFocusObservers.indexOf(obs);
        if (idx >= 0) pendingFocusObservers.splice(idx, 1);
      }, 200);
    }

    // Step 1: microtask — runs AFTER cb's Promise.resolve().then(...)
    // re-renders, BEFORE next animation frame. Catches the common
    // async-render path.
    queueMicrotask(function () {
      if (openSeq !== seqAtClose) return;
      let fresh = null;
      try { fresh = resolver(); } catch (_) {}
      if (fresh && document.body.contains(fresh)
          && document.activeElement !== fresh) {
        try { fresh.focus({ preventScroll: true }); } catch (_) {}
      }
      // Step 2: rAF — runs after layout commits. Catches re-renders
      // that wait on rAF or that resolve only after style/layout work.
      requestAnimationFrame(deferredRecheck);
    });
  }

  // If the viewport grows past the breakpoint while open, close the slide-over
  // so callers can re-route into the wide-viewport side panel.
  let _resizeT = null;
  window.addEventListener('resize', function () {
    if (!isOpen()) return;
    clearTimeout(_resizeT);
    _resizeT = setTimeout(function () {
      if (isOpen() && !shouldUse()) close();
    }, 120);
  });

  window.SlideOver = { open: open, close: close, isOpen: isOpen, shouldUse: shouldUse, BP: BP };
})();


(function () {
  let packets = [];
  let hashIndex = new Map(); // hash → packet group for O(1) dedup

  // #1799 PR #1804 r1 item 9 (adv6): payload-labels.js is loaded
  // synchronously before this script in index.html. A missing module
  // is a packaging bug, not a runtime degradation — fail loud.
  if (typeof window !== 'undefined' && !window.PayloadLabels) {
    throw new Error('packets.js: window.PayloadLabels missing — payload-labels.js failed to load');
  }
  const SHORT_BY_ID = window.PayloadLabels.api
    ? window.PayloadLabels.api.SHORT_BY_ID
    : window.PayloadLabels.SHORT_BY_ID;

  // Resolve observer_id to friendly name from loaded observers list
  function obsName(id) {
    if (!id) return '—';
    const o = observerMap.get(id);
    if (!o) return id;
    return o.iata ? `${o.name} (${o.iata})` : o.name;
  }
  // Compact IATA pill (#1188) — renders next to observer name. Prefers
  // packet.observer_iata (now joined on the server) and falls back to the
  // observer lookup map for callers that haven't been updated yet.
  function obsIataBadge(packet) {
    if (!packet) return '';
    let iata = packet.observer_iata;
    if (!iata) {
      const o = packet.observer_id ? observerMap.get(packet.observer_id) : null;
      iata = o && o.iata;
    }
    return iata ? `<span class="badge-iata">${escapeHtml(iata)}</span>` : '';
  }
  // Plain observer name without the trailing IATA — used when the IATA is
  // rendered separately as a badge (so the cell doesn't show "Name (SJC) SJC").
  function obsNameOnly(id) {
    if (!id) return '—';
    const o = observerMap.get(id);
    if (!o) return id;
    return o.name;
  }
  // #1189 R1 mesh-operator feedback: in a grouped row the old cell showed ONE
  // observer's IATA + `+N` — operators couldn't tell whether the N additional
  // observers were SAME-region (redundant copies) or CROSS-region (interesting
  // multi-site reception). This helper returns the cell's badge HTML showing
  // the DISTINCT IATA set: `<badge>SJC</badge>` or `<badge>SJC</badge><badge>SFO</badge>+1`
  // (capped at 2 visible, remainder rolled into +N of distinct-region count).
  // Returns '' when no observer in the group carries any IATA.
  //
  // #1189 R2: source of truth is `p.distinct_iatas` from the server
  // (added to /api/packets?groupByHash=true so the default collapsed view
  // works without needing to expand a row). Falls back to walking
  // p._children + observerMap for legacy callers and for client-side groups
  // synthesised by the websocket appender.
  function groupedObserverIataBadgesHtml(p) {
    if (!p) return '';
    const seen = new Set();
    // R2 happy path: server-provided distinct_iatas.
    if (Array.isArray(p.distinct_iatas)) {
      for (const code of p.distinct_iatas) {
        if (code) seen.add(String(code).toUpperCase());
      }
    }
    // Fallback / supplement: walk header + children (covers in-memory groups
    // built client-side from websocket events before any server round-trip).
    if (!seen.size) {
      const pushIata = (rec) => {
        if (!rec) return;
        let iata = rec.observer_iata;
        if (!iata && rec.observer_id) {
          const o = observerMap.get(rec.observer_id);
          iata = o && o.iata;
        }
        if (iata) seen.add(String(iata).toUpperCase());
      };
      pushIata(p);
      if (p._children && p._children.length) {
        for (const c of p._children) pushIata(c);
      }
    }
    if (!seen.size) return '';
    const list = Array.from(seen).sort();
    const visible = list.slice(0, 2);
    const extra = list.length - visible.length;
    let html = visible
      .map(code => `<span class="badge-iata">${escapeHtml(code)}</span>`)
      .join('');
    if (extra > 0) html += ` +${extra}`;
    return html;
  }
  let selectedId = null;
  function _isColorByHash() { return localStorage.getItem('meshcore-color-packets-by-hash') !== 'false'; }
  function _currentTheme() { return document.documentElement.dataset.theme || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'); }
  function _hashStripeStyle(hash) { return _isColorByHash() && hash && window.HashColor ? 'border-left:4px solid ' + HashColor.hashToHsl(hash, _currentTheme()) + ';' : ''; }
  let groupByHash = true;
  let filters = {};
  { const o = localStorage.getItem('meshcore-observer-filter'); if (o) filters.observer = o;
    const t = localStorage.getItem('meshcore-type-filter'); if (t) filters.type = t; }
  let wsHandler = null;
  let packetsPaused = false;
  let pauseBuffer = [];
  let observers = [];
  let observerMap = new Map(); // id → observer for O(1) lookups (#383)
  // #1693 — hook set by renderLeft() so loadObservers() can refresh the
  // observer-filter dropdown when observers resolve after the filter UI is
  // already built (decouples observer fetch from row render).
  let _rebuildObserverMenu = null;
  let regionMap = {};
  const TYPE_NAMES = SHORT_BY_ID;
  function typeName(t) { return TYPE_NAMES[t] ?? `Type ${t}`; }
  const isMobile = window.innerWidth <= 1024;
  const PACKET_LIMIT = isMobile ? 1000 : 50000;
  let savedTimeWindowMin = Number(localStorage.getItem('meshcore-time-window'));
  if (!Number.isFinite(savedTimeWindowMin) || savedTimeWindowMin <= 0) savedTimeWindowMin = 15;
  if (isMobile && savedTimeWindowMin > 180) savedTimeWindowMin = 15;
  let totalCount = 0;
  let expandedHashes = new Set();
  let hopNameCache = {};
  let _tableSortInstance = null;
  let _packetSortColumn = null;
  let _packetSortDirection = 'desc';
  let showHexHashes = localStorage.getItem('meshcore-hex-hashes') === 'true';
  var _pendingUrlRegion = null;

  var DEFAULT_TIME_WINDOW = 15;

  function buildPacketsQuery(timeWindowMin, regionParam, skipHash) {
    var parts = [];
    if (timeWindowMin && timeWindowMin !== DEFAULT_TIME_WINDOW) parts.push('timeWindow=' + timeWindowMin);
    if (regionParam) parts.push('region=' + encodeURIComponent(regionParam));
    if (!skipHash && filters.hash) parts.push('hash=' + encodeURIComponent(filters.hash));
    if (filters.node) parts.push('node=' + encodeURIComponent(filters.node));
    if (filters.observer) parts.push('observer=' + encodeURIComponent(filters.observer));
    if (filters.channel) parts.push('channel=' + encodeURIComponent(filters.channel));
    if (filters._filterExpr) parts.push('filter=' + encodeURIComponent(filters._filterExpr));
    // Sort state (#749) — encode as 'col[:asc]'; default 'time:desc' is omitted.
    if (_packetSortColumn) {
      var sortDefault = _packetSortColumn === 'time' && _packetSortDirection === 'desc';
      if (!sortDefault && window.URLState) {
        var sortToken = URLState.serializeSort(_packetSortColumn, _packetSortDirection);
        if (sortToken) parts.push('sort=' + encodeURIComponent(sortToken));
      }
    }
    return parts.length ? '?' + parts.join('&') : '';
  }
  window.buildPacketsQuery = buildPacketsQuery;

  function updatePacketsUrl() {
    // Preserve any subpath after /packets (e.g. #/packets/<hash>).
    var cur = String(location.hash || '');
    var subpath = '';
    var m = cur.match(/^#\/packets(\/[^?]*)?/);
    if (m && m[1]) subpath = m[1];
    // Don't double-encode filters.hash when it's already the path segment.
    var skipHash = !!(filters.hash && subpath === '/' + filters.hash);
    history.replaceState(null, '', '#/packets' + subpath + buildPacketsQuery(savedTimeWindowMin, RegionFilter.getRegionParam(), skipHash));
    // Update clear-filters button visibility
    var cb = document.getElementById('clearFiltersBtn');
    if (cb) {
      var active = !!(filters.hash || filters.node || filters.observer || filters.channel || filters.type || filters._filterExpr || filters.myNodes) || !!RegionFilter.getRegionParam() || savedTimeWindowMin !== DEFAULT_TIME_WINDOW;
      cb.style.display = active ? '' : 'none';
    }
  }

  let filtersBuilt = false;
  let _renderTimer = null;
  function scheduleRender() {
    clearTimeout(_renderTimer);
    _renderTimer = setTimeout(() => renderTableRows(), 200);
  }

  // Coalesce WS-triggered renders into one per animation frame (#396).
  // Multiple WS batches arriving within the same frame only trigger a single
  // renderTableRows() call on the next rAF, preventing rapid full rebuilds.
  function scheduleWSRender() {
    _wsRenderDirty = true;
    if (_wsRafId) return;  // already scheduled
    _wsRafId = requestAnimationFrame(function () {
      _wsRafId = null;
      if (_wsRenderDirty) {
        _wsRenderDirty = false;
        renderTableRows();
      }
    });
  }
  const PANEL_WIDTH_KEY = 'meshcore-panel-width';
  const PANEL_CLOSE_HTML = '<button class="panel-close-btn" title="Close detail pane (Esc)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></button>';

  // getParsedPath / getParsedDecoded are in shared packet-helpers.js (loaded before this file)
  const getParsedPath = window.getParsedPath;
  const getParsedDecoded = window.getParsedDecoded;

  // --- Virtual scroll state ---
  let VSCROLL_ROW_HEIGHT = 36;    // measured dynamically on first render; fallback 36px
  let _vscrollRowHeightMeasured = false;
  let _vscrollTheadHeight = 40;   // measured dynamically on first render; fallback 40px
  const VSCROLL_BUFFER = 30;      // extra rows above/below viewport
  let _displayPackets = [];       // filtered packets for current view
  let _displayGrouped = false;    // whether _displayPackets is in grouped mode
  let _rowCounts = [];            // per-entry DOM row counts (1 for flat, 1+children for expanded groups)
  let _rowCountsDirty = false;    // set when _rowCounts may be stale (e.g. WS added children) (#410)
  let _cumulativeOffsetsCache = null; // cached cumulative offsets, invalidated on _rowCounts change
  let _lastVisibleStart = -1;     // last rendered start index (for dirty checking)
  let _lastVisibleEnd = -1;       // last rendered end index (for dirty checking)
  let _vsScrollHandler = null;    // scroll listener reference
  let _wsRenderTimer = null;      // debounce timer for WS-triggered renders
  let _wsRafId = null;            // rAF id for coalescing WS-triggered renders (#396)
  let _wsRenderDirty = false;     // dirty flag for rAF render coalescing (#396)
  let _observerFilterSet = null;  // cached Set from filters.observer, hoisted above loops (#427)

  // Pure function: calculate visible entry range from scroll state.
  // Extracted for testability (#405, #409).
  function _calcVisibleRange(offsets, entryCount, scrollTop, viewportHeight, rowHeight, theadHeight, buffer) {
    const adjustedScrollTop = Math.max(0, scrollTop - theadHeight);
    const firstDomRow = Math.floor(adjustedScrollTop / rowHeight);
    const visibleDomCount = Math.ceil(viewportHeight / rowHeight);

    // Binary search for first entry whose cumulative offset covers firstDomRow
    let lo = 0, hi = entryCount;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (offsets[mid + 1] <= firstDomRow) lo = mid + 1;
      else hi = mid;
    }
    const firstEntry = lo;

    // Binary search for last visible entry
    const lastDomRow = firstDomRow + visibleDomCount;
    lo = firstEntry; hi = entryCount;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (offsets[mid + 1] <= lastDomRow) lo = mid + 1;
      else hi = mid;
    }
    const lastEntry = Math.min(lo + 1, entryCount);

    const startIdx = Math.max(0, firstEntry - buffer);
    const endIdx = Math.min(entryCount, lastEntry + buffer);
    return { startIdx, endIdx, firstEntry, lastEntry };
  }

  function closeDetailPanel() {
    var panel = document.getElementById('pktRight');
    if (panel) {
      panel.classList.add('empty');
      panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div>' + PANEL_CLOSE_HTML + '<span>Select a packet to view details</span>';
      var layout = panel.closest('.split-layout');
      if (layout) layout.classList.add('detail-collapsed');
      selectedId = null;
      renderTableRows();
    }
  }

  function initPanelResize() {
    const handle = document.getElementById('pktResizeHandle');
    const panel = document.getElementById('pktRight');
    if (!handle || !panel) return;
    // Restore saved width
    const saved = localStorage.getItem(PANEL_WIDTH_KEY);
    if (saved) panel.style.width = saved + 'px';

    let startX, startW;
    function startResize(clientX) {
      startX = clientX;
      startW = panel.offsetWidth;
      handle.classList.add('dragging');
      document.body.style.cursor = 'col-resize';
      document.body.style.userSelect = 'none';
    }
    function doResize(clientX) {
      const w = Math.max(280, Math.min(window.innerWidth * 0.7, startW - (clientX - startX)));
      panel.style.width = w + 'px';
      panel.style.minWidth = w + 'px';
      const left = document.getElementById('pktLeft');
      if (left) {
        const available = left.parentElement.clientWidth - w;
        left.style.width = available + 'px';
      }
    }
    function endResize() {
      handle.classList.remove('dragging');
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
      localStorage.setItem(PANEL_WIDTH_KEY, panel.offsetWidth);
      const left = document.getElementById('pktLeft');
      if (left) left.style.width = '';
    }

    handle.addEventListener('mousedown', (e) => {
      e.preventDefault();
      startResize(e.clientX);

      function onMove(e2) { doResize(e2.clientX); }
      function onUp() {
        endResize();
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', onUp);
      }
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });

    handle.addEventListener('touchstart', (e) => {
      if (e.touches.length !== 1) return;
      e.preventDefault();
      startResize(e.touches[0].clientX);

      function onTouchMove(e2) {
        if (e2.touches.length !== 1) return;
        e2.preventDefault();
        doResize(e2.touches[0].clientX);
      }
      function onTouchEnd() {
        endResize();
        document.removeEventListener('touchmove', onTouchMove);
        document.removeEventListener('touchend', onTouchEnd);
      }
      document.addEventListener('touchmove', onTouchMove, { passive: false });
      document.addEventListener('touchend', onTouchEnd);
    }, { passive: false });
  }

  // Ensure HopResolver is initialized with the nodes list + observer IATA data
  async function ensureHopResolver() {
    if (!HopResolver.ready()) {
      try {
        const [nodeData, obsData, coordData] = await Promise.all([
          fetchAllNodes('', { ttl: 60000 }),
          api('/observers', { ttl: 60000 }),
          api('/iata-coords', { ttl: 300000 }).catch(() => ({ coords: {} })),
        ]);
        HopResolver.init(nodeData.nodes || [], {
          observers: obsData.observers || obsData || [],
          iataCoords: coordData.coords || {},
        });
      } catch (e) {
        // Non-fatal: hops will render as unresolved hex prefixes until a later
        // call succeeds. Log so a paginated /api/nodes failure isn't silent.
        console.warn('[packets] HopResolver init failed:', e);
      }
    }
  }

  // Resolve hop hex prefixes to node names (cached, client-side)
  async function resolveHops(hops) {
    const unknown = hops.filter(h => !(h in hopNameCache));
    if (unknown.length) {
      await ensureHopResolver();
      const resolved = HopResolver.resolve(unknown);
      Object.assign(hopNameCache, resolved || {});
      // Cache misses as null so we don't re-query
      unknown.forEach(h => { if (!(h in hopNameCache)) hopNameCache[h] = null; });
    }
  }

  /**
   * Pre-populate hopNameCache from server-side resolved_path on packets.
   * Packets with resolved_path skip client-side HopResolver entirely.
   * Must call ensureHopResolver() first so nodesList is available for name lookup.
   */
  async function cacheResolvedPaths(packets) {
    if (!packets || !packets.length) return;
    let needsInit = false;
    for (const p of packets) {
      const rp = getResolvedPath(p);
      if (rp) { needsInit = true; break; }
    }
    if (!needsInit) return;
    await ensureHopResolver();
    for (const p of packets) {
      const rp = getResolvedPath(p);
      if (!rp) continue;
      const hops = getParsedPath(p);
      const resolved = HopResolver.resolveFromServer(hops, rp);
      Object.assign(hopNameCache, resolved);
    }
  }

  function renderHop(h, observerId) {
    // Use per-packet cache key if observer context available (ambiguous hops differ by region)
    const cacheKey = observerId ? h + ':' + observerId : h;
    const entry = hopNameCache[cacheKey] || hopNameCache[h];
    return HopDisplay.renderHop(h, entry, { hexMode: showHexHashes });
  }

  function renderPath(hops, observerId) {
    if (!hops || !hops.length) return '—';
    // #1633 — render-time filter (default OFF). Applies at every consumer
    // because every site funnels through this function (group header, child
    // observation, packet detail dt/dd, byop overlay).
    var filtered = (typeof window !== 'undefined' && window.MC_filterPathHops)
      ? window.MC_filterPathHops(hops)
      : hops;
    if (!filtered.length) return '— <span class="text-muted" title="All path hops were 1-byte and are hidden by the customizer toggle">(1-byte filtered)</span>';
    return filtered.map(h => renderHop(h, observerId)).join('<span class="arrow">→</span>');
  }

  let directPacketId = null;
  let directPacketHash = null;
  let initGeneration = 0;
  let _docActionHandler = null;
  let _docMenuCloseHandler = null;
  let _docColMenuCloseHandler = null;

  let directObsId = null;

  function removeAllByopOverlays() {
    document.querySelectorAll('.byop-overlay').forEach(function (el) { el.remove(); });
  }

  function bindDocumentHandler(kind, eventName, handler) {
    const prev = kind === 'action'
      ? _docActionHandler
      : kind === 'menu'
        ? _docMenuCloseHandler
        : _docColMenuCloseHandler;
    if (prev) document.removeEventListener(eventName, prev);
    document.addEventListener(eventName, handler);
    if (kind === 'action') _docActionHandler = handler;
    else if (kind === 'menu') _docMenuCloseHandler = handler;
    else _docColMenuCloseHandler = handler;
  }

  function renderTimestampCell(isoString) {
    if (typeof formatTimestampWithTooltip !== 'function' || typeof getTimestampMode !== 'function') {
      return escapeHtml(typeof timeAgo === 'function' ? timeAgo(isoString) : '—');
    }
    const f = formatTimestampWithTooltip(isoString, getTimestampMode());
    const warn = f.isFuture
      ? ' <span class="timestamp-future-icon" title="Timestamp is in the future — node clock may be skewed"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg></span>'
      : '';
    return `<span class="timestamp-text" title="${escapeHtml(f.tooltip)}">${escapeHtml(f.text)}</span>${warn}`;
  }

  async function init(app, routeParam) {
    const gen = ++initGeneration;
    // #1689 r1 (adv #4): subscribe to the customizer hide-1-byte-hops toggle
    // so the packets table re-renders LIVE when operators flip it (the
    // toggle promised "applies everywhere" — without a listener it only
    // took effect on next navigation, which the inline comment lied about).
    // The listener is idempotent via a flag on `window` so re-entering the
    // route doesn't stack handlers.
    if (typeof window !== 'undefined' && !window.__mc_packets_hide1byte_wired) {
      window.__mc_packets_hide1byte_wired = true;
      window.addEventListener('mc-hide-1byte-hops-changed', function () {
        try { renderTableRows(); } catch (e) { console.warn('[packets] hide-1byte re-render failed', e); }
      });
    }
    // Parse ?obs=OBSERVER_ID from routeParam
    if (routeParam && routeParam.includes('?')) {
      const qIdx = routeParam.indexOf('?');
      const qs = new URLSearchParams(routeParam.substring(qIdx));
      directObsId = qs.get('obs');
      routeParam = routeParam.substring(0, qIdx);
    }
    // Detect route param type: "id/123" for direct packet, short hex for hash, long hex for node
    if (routeParam) {
      if (routeParam.startsWith('id/')) {
        directPacketId = routeParam.slice(3);
      } else if (routeParam.length <= 16) {
        filters.hash = routeParam;
        directPacketHash = routeParam;
      } else {
        filters.node = routeParam;
      }
    }

    // Read URL params (router strips query from routeParam; read from location.hash)
    var _initUrlParams = getHashParams();
    var _urlTimeWindow = Number(_initUrlParams.get('timeWindow'));
    if (Number.isFinite(_urlTimeWindow) && _urlTimeWindow > 0) {
      savedTimeWindowMin = _urlTimeWindow;
      localStorage.setItem('meshcore-time-window', String(_urlTimeWindow));
    }
    var _urlRegion = _initUrlParams.get('region');
    if (_urlRegion) _pendingUrlRegion = _urlRegion;
    var _urlHash = _initUrlParams.get('hash');
    if (_urlHash) filters.hash = _urlHash;
    var _urlNode = _initUrlParams.get('node');
    if (_urlNode) { filters.node = _urlNode; filters.nodeName = _urlNode.slice(0, 8); }
    var _urlObserver = _initUrlParams.get('observer');
    if (_urlObserver) filters.observer = _urlObserver;
    var _urlChannel = _initUrlParams.get('channel');
    if (_urlChannel) filters.channel = _urlChannel;
    var _urlFilterExpr = _initUrlParams.get('filter');
    if (_urlFilterExpr) filters._filterExpr = _urlFilterExpr;
    // #749 — restore sort state from URL (overrides localStorage).
    var _urlSort = _initUrlParams.get('sort');
    if (_urlSort && window.URLState) {
      var _parsed = URLState.parseSort(_urlSort);
      if (_parsed) {
        _packetSortColumn = _parsed.column;
        _packetSortDirection = _parsed.direction;
        // Persist so TableSort init picks it up.
        try { localStorage.setItem('meshcore-packets-sort', JSON.stringify({ column: _parsed.column, direction: _parsed.direction })); } catch {}
      }
    }

    app.innerHTML = `<div class="split-layout detail-collapsed">
      <div class="panel-left" id="pktLeft" aria-live="polite" aria-relevant="additions removals"></div>
      <div class="panel-right empty" id="pktRight" aria-live="polite">
        <div class="panel-resize-handle" id="pktResizeHandle"></div>
        ${PANEL_CLOSE_HTML}
        <span>Select a packet to view details</span>
      </div>
    </div>`;
    initPanelResize();
    document.getElementById('pktRight').addEventListener('click', function(e) {
      if (e.target.closest('.panel-close-btn')) closeDetailPanel();
    });
    // #1692 — parallelize loadObservers + loadPackets. Both hit independent
    // API endpoints (/api/observers, /api/packets) and have no data
    // dependency between the two fetches; only the post-fetch renderLeft()
    // code reads `observers` (for the observer filter dropdown), so awaiting
    // them together rather than in series halves worst-case time-to-first-row
    // on slow links / loaded CI runners without changing render contracts.
    await Promise.all([loadObservers(), loadPackets()]);

    // Auto-select packet detail when arriving via hash URL
    if (directPacketHash) {
      const h = directPacketHash;
      const obsTarget = directObsId;
      directPacketHash = null;
      directObsId = null;
      try {
        const data = await api(`/packets/${h}`);
        if (gen === initGeneration && data?.packet) {
          if (obsTarget && data.observations) {
            // Find the matching observation by its unique id
            const obs = data.observations.find(o => String(o.id) === String(obsTarget));
            if (obs) {
              expandedHashes.add(h);
              const obsPacket = {...data.packet, observer_id: obs.observer_id, observer_name: obs.observer_name, snr: obs.snr, rssi: obs.rssi, path_json: obs.path_json, resolved_path: obs.resolved_path, direction: obs.direction, timestamp: obs.timestamp, first_seen: obs.timestamp};
              clearParsedCache(obsPacket);
              selectPacket(obs.id, h, {packet: obsPacket, observations: data.observations}, obs.id);
            } else {
              selectPacket(data.packet.id, h, data);
            }
          } else {
            selectPacket(data.packet.id, h, data);
          }
        }
      } catch {}
    }

    // Event delegation for data-action buttons
    bindDocumentHandler('action', 'click', function (e) {
      var btn = e.target.closest('[data-action]');
      if (!btn) return;
      if (btn.dataset.action === 'pkt-refresh') loadPackets();
      else if (btn.dataset.action === 'pkt-byop') showBYOP();
      else if (btn.dataset.action === 'pkt-pause') {
        packetsPaused = !packetsPaused;
        const pauseBtn = document.getElementById('pktPauseBtn');
        if (pauseBtn) {
          // #1648 M4: Phosphor sprite glyph for play/pause (was ▶/⏸). // EMOJI-OK: comment
          pauseBtn.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#' +
            (packetsPaused ? 'ph-play' : 'ph-pause') + '"/></svg>';
          pauseBtn.title = packetsPaused ? 'Resume live updates' : 'Pause live updates';
          pauseBtn.classList.toggle('active', packetsPaused);
        }
        if (!packetsPaused && pauseBuffer.length) {
          const handler = wsHandler;
          pauseBuffer.forEach(msg => { if (handler) handler(msg); });
          pauseBuffer = [];
        }
      }
    });

    // If linked directly to a packet by ID, load its detail and filter list
    if (directPacketId) {
      const pktId = Number(directPacketId);
      directPacketId = null;
      try {
        const data = await api(`/packets/${pktId}`);
        if (gen !== initGeneration) return;
        if (data.packet?.hash) {
          filters.hash = data.packet.hash;
          const hashInput = document.getElementById('fHash');
          if (hashInput) hashInput.value = filters.hash;
          await loadPackets();
        }
        // Show detail in sidebar
        const panel = document.getElementById('pktRight');
        if (panel) {
          panel.classList.remove('empty');
          panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div>' + PANEL_CLOSE_HTML;
          const content = document.createElement('div');
          panel.appendChild(content);
          const pkt = data.packet;
          try {
            const hops = getParsedPath(pkt);
            const newHops = hops.filter(h => !(h in hopNameCache));
            if (newHops.length) await resolveHops(newHops);
          } catch {}
          await renderDetail(content, data);
          initPanelResize();
        }
      } catch {}
    }
    wsHandler = debouncedOnWS(function (msgs) {
      if (packetsPaused) {
        pauseBuffer.push(...msgs);
        if (pauseBuffer.length > 2000) pauseBuffer = pauseBuffer.slice(-2000);
        const btn = document.getElementById('pktPauseBtn');
        if (btn) btn.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-play"/></svg> ' + pauseBuffer.length;
        return;
      }
      const newPkts = msgs
        .filter(m => m.type === 'packet' && m.data?.packet)
        .map(m => m.data.packet);
      if (!newPkts.length) return;

      // Check if new packets pass current filters
      const filtered = newPkts.filter(p => {
        // When user pinned a hash, accept ONLY that exact packet — bypass all
        // other filters (window/region/type/observer/node).
        if (filters.hash) return p.hash === filters.hash;
        // Respect time window filter — drop packets outside the selected window
        const windowMin = savedTimeWindowMin;
        if (windowMin > 0) {
          const cutoff = new Date(Date.now() - windowMin * 60000).toISOString();
          const pktTime = p.latest || p.timestamp || p.first_seen;
          if (pktTime && pktTime < cutoff) return false;
        }
        if (filters.type) { const types = filters.type.split(',').map(Number); if (!types.includes(p.payload_type)) return false; }
        if (filters.observer) { const obsSet = new Set(filters.observer.split(',')); if (!obsSet.has(p.observer_id) && !(p._children && p._children.some(c => obsSet.has(String(c.observer_id))))) return false; }
        if (RegionFilter.getRegionParam()) {
          const selectedRegions = RegionFilter.getRegionParam().split(',');
          const obs = observerMap.get(p.observer_id);
          if (!obs || !selectedRegions.includes(obs.iata)) return false;
        }
        if (filters.node && !(p.decoded_json || '').includes(filters.node)) return false;
        return true;
      });
      if (!filtered.length) return;

      // Resolve any new hops, then update and re-render
      // Pre-populate from server-side resolved_path, then fall back for remaining
      const newHops = new Set();
      for (const p of filtered) {
        const rp = getResolvedPath(p);
        const hops = getParsedPath(p);
        if (rp && rp.length === hops.length && window.HopResolver && HopResolver.ready()) {
          const resolved = HopResolver.resolveFromServer(hops, rp);
          Object.assign(hopNameCache, resolved);
        }
        try { hops.forEach(h => { if (!(h in hopNameCache)) newHops.add(h); }); } catch {}
      }
      (newHops.size ? resolveHops([...newHops]) : Promise.resolve()).then(() => {
        if (groupByHash) {
          // Update existing groups or create new ones
          for (const p of filtered) {
            const h = p.hash;
            const existing = hashIndex.get(h);
            if (existing) {
              existing.count = (existing.count || 1) + 1;
              existing.observation_count = (existing.observation_count || 1) + 1;
              existing.latest = p.timestamp > existing.latest ? p.timestamp : existing.latest;
              // Track unique observers
              if (p.observer_id && p.observer_id !== existing.observer_id) {
                existing.observer_count = (existing.observer_count || 1) + 1;
              }
              // Don't update path — header always shows first observer's path
              // Update decoded_json to latest
              if (p.decoded_json) existing.decoded_json = p.decoded_json;
              // Update expanded children if this group is expanded
              if (expandedHashes.has(h) && existing._children) {
                existing._children.unshift(clearParsedCache({...p, _isObservation: true}));
                if (existing._children.length > 200) existing._children.length = 200;
                sortGroupChildren(existing);
                // Invalidate row counts — child count changed, so virtual scroll
                // heights are stale until next renderTableRows() (#410)
                _invalidateRowCounts();
              }
            } else {
              // New group
              const newGroup = {
                hash: h,
                count: 1,
                observer_count: 1,
                latest: p.timestamp,
                observer_id: p.observer_id,
                observer_name: p.observer_name,
                path_json: p.path_json,
                payload_type: p.payload_type,
                raw_hex: p.raw_hex,
                decoded_json: p.decoded_json,
              };
              packets.unshift(newGroup);
              if (h) hashIndex.set(h, newGroup);
            }
          }
          // Re-sort by active sort column (or latest DESC as default), then evict oldest beyond the limit
          if (_packetSortColumn) {
            sortPacketsArray();
          } else {
            packets.sort((a, b) => (b.latest || '').localeCompare(a.latest || ''));
          }
          if (packets.length > PACKET_LIMIT) {
            const evicted = packets.splice(PACKET_LIMIT);
            for (const p of evicted) { if (p.hash) hashIndex.delete(p.hash); }
          }
        } else {
          // Flat mode: prepend, then evict oldest beyond the limit
          packets = filtered.concat(packets);
          if (packets.length > PACKET_LIMIT) packets.length = PACKET_LIMIT;
        }
        totalCount += filtered.length;
        // Coalesce WS-triggered renders via rAF (#396)
        scheduleWSRender();
      });
    });
  }

  function destroy() {
    clearTimeout(_renderTimer);
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (_tableSortInstance) { _tableSortInstance.destroy(); _tableSortInstance = null; }
    detachVScrollListener();
    clearTimeout(_wsRenderTimer);
    if (_wsRafId) { cancelAnimationFrame(_wsRafId); _wsRafId = null; }
    _wsRenderDirty = false;
    _displayPackets = [];
    _rowCounts = [];
    _rowCountsDirty = false;
    _cumulativeOffsetsCache = null;
    _observerFilterSet = null;
    _lastVisibleStart = -1;
    _lastVisibleEnd = -1;
    if (_docActionHandler) { document.removeEventListener('click', _docActionHandler); _docActionHandler = null; }
    if (_docMenuCloseHandler) { document.removeEventListener('click', _docMenuCloseHandler); _docMenuCloseHandler = null; }
    if (_docColMenuCloseHandler) { document.removeEventListener('click', _docColMenuCloseHandler); _docColMenuCloseHandler = null; }
    removeAllByopOverlays();
    packets = [];
    hashIndex = new Map();    selectedId = null;
    filtersBuilt = false;
    delete filters.node;
    expandedHashes = new Set();
    hopNameCache = {};
    totalCount = 0;
    observers = [];
    observerMap = new Map();
    directPacketId = null;
    directPacketHash = null;
    groupByHash = true;
    filters = {};
    regionMap = {};
  }

  async function loadObservers() {
    try {
      const data = await api('/observers', { ttl: CLIENT_TTL.observers });
      observers = data.observers || [];
      observerMap = new Map(observers.map(o => [o.id, o]));
      // #1693 — observers now resolve asynchronously relative to row render.
      // If the filter UI was already built with an empty observer list,
      // re-populate the observer-multi-select dropdown so the filter is
      // usable as soon as observers arrive.
      if (typeof _rebuildObserverMenu === 'function') {
        try { _rebuildObserverMenu(); } catch {}
      }
    } catch {}
  }

  // Build URLSearchParams for /api/packets given UI state. Pure function for
  // testability — returns the params object the next call to /api/packets
  // would use. The hash filter is an exact identifier: when present it
  // suppresses ALL other filters (region, time window, observer, node,
  // channel). The user is asking for THAT packet regardless of saved
  // selections.
  function buildPacketsParams({ filters, regionParam, areaParam, windowMin, groupByHash, limit }) {
    const params = new URLSearchParams();
    if (filters.hash) {
      params.set('hash', filters.hash);
      params.set('limit', String(limit));
      if (groupByHash) {
        params.set('groupByHash', 'true');
      } else {
        params.set('expand', 'observations');
      }
      return params;
    }
    if (windowMin > 0) {
      const since = new Date(Date.now() - windowMin * 60000).toISOString();
      params.set('since', since);
    }
    params.set('limit', String(limit));
    if (regionParam) params.set('region', regionParam);
    if (areaParam) params.set('area', areaParam);
    if (filters.node) params.set('node', filters.node);
    if (filters.observer) params.set('observer', filters.observer);
    if (filters.channel) params.set('channel', filters.channel);
    if (groupByHash) {
      params.set('groupByHash', 'true');
    } else {
      params.set('expand', 'observations');
    }
    return params;
  }

  async function loadPackets() {
    try {
      const selectedWindow = Number(document.getElementById('fTimeWindow')?.value);
      const windowMin = Number.isFinite(selectedWindow) ? selectedWindow : savedTimeWindowMin;
      const params = buildPacketsParams({
        filters,
        regionParam: RegionFilter.getRegionParam(),
        areaParam: AreaFilter.getAreaParam(),
        windowMin,
        groupByHash,
        limit: PACKET_LIMIT,
      });

      const data = await api('/packets?' + params.toString());
      packets = data.packets || [];
      hashIndex = new Map();
      for (const p of packets) { if (p.hash) hashIndex.set(p.hash, p); }
      totalCount = data.total || packets.length;

      // When ungrouped, flatten observations inline (single API call, no N+1)
      if (!groupByHash) {
        const flat = [];
        for (const p of packets) {
          if (p.observations && p.observations.length > 1) {
            for (const o of p.observations) {
              flat.push(clearParsedCache({...p, ...o, _isObservation: true, observations: undefined}));
            }
          } else {
            flat.push(p);
          }
        }
        packets = flat;
        totalCount = flat.length;
      }

      // #1693 — hop resolution is OFF the critical path. Previously
      // cacheResolvedPaths() + resolveHops() each chained through
      // ensureHopResolver(), which calls api('/observers'). Under the
      // packets-init race (#1692) the observers fetch is still in-flight
      // (parallelised but slow), and the api() in-flight dedupe maps the
      // hop-resolver's /observers call onto the same pending promise.
      // Result: rows can't render until observers resolves, defeating the
      // #1692 parallelisation. Hop *names* are a presentation enhancement
      // (paths render as hex prefixes until names arrive), so resolve them
      // in the background and re-render rows when done.
      const hopJob = (async () => {
        try {
          await cacheResolvedPaths(packets);
          const allHops = new Set();
          for (const p of packets) {
            try { getParsedPath(p).forEach(h => allHops.add(h)); } catch {}
          }
          if (allHops.size) await resolveHops([...allHops]);
          // Re-render rows so resolved hop names replace hex prefixes.
          if (filtersBuilt) renderTableRows();
        } catch (e) {
          console.warn('[packets] background hop resolution failed:', e);
        }
      })();
      // Don't await — let rows render with hex hops, then upgrade in place.
      void hopJob;

      // Restore expanded group children (parallel fetch, Map lookup)
      if (groupByHash && expandedHashes.size > 0) {
        const expandedArr = [...expandedHashes];
        // Fetch the full packet detail (which includes per-observation rows) for each expanded hash.
        // Previously this used `/packets?hash=X&limit=20` which returned ONE aggregate row, causing
        // every "child" row in the table to carry the parent packet.id instead of unique observation
        // ids — so clicking any child pointed the side pane at the same aggregate. See #866.
        const results = await Promise.all(expandedArr.map(hash => {
          const group = hashIndex.get(hash);
          if (!group) return { hash, group: null, data: null };
          return api(`/packets/${hash}`)
            .then(data => ({ hash, group, data }))
            .catch(() => ({ hash, group, data: null }));
        }));
        for (const { hash, group, data } of results) {
          if (!group) {
            expandedHashes.delete(hash);
          } else if (data) {
            const pkt = data.packet || group;
            // Build per-observation children. Spread (pkt, obs) so obs-level fields
            // (id, observer_id/name, path_json, snr/rssi, timestamp, raw_hex) override
            // the aggregate. Each child's `id` is the observation id (unique per observer).
            const obs = data.observations || [];
            group._children = obs.length
              ? obs.map(o => clearParsedCache({...pkt, ...o, _isObservation: true}))
              : [pkt];
            group._fetchedData = { packet: pkt, observations: obs };
            sortGroupChildren(group);
          }
        }
      }

      sortPacketsArray();
      renderLeft();
    } catch (e) {
      console.error('Failed to load packets:', e);
      const tbody = document.getElementById('pktBody');
      if (tbody) tbody.innerHTML = '<tr><td colspan="' + _getColCount() + '" class="text-center" style="padding:24px;color:var(--error,#ef4444)"><div role="alert" aria-live="polite">Failed to load packets. Please try again.</div></td></tr>';
    } finally {
      // Always signal data-loaded — even on error — so E2E tests can proceed.
      var pktContainer = document.getElementById('pktLeft') || document.getElementById('pktBody');
      if (pktContainer) pktContainer.setAttribute('data-loaded', 'true');
    }
  }

  function renderLeft() {
    const el = document.getElementById('pktLeft');
    if (!el) return;

    // Only build the filter bar + table skeleton once; subsequent calls just update rows
    if (filtersBuilt) {
      renderTableRows();
      return;
    }
    filtersBuilt = true;

    el.innerHTML = `
      <div class="page-header">
        <h2>Latest Packets <span class="count">(${totalCount})</span></h2>
        <div>
          <button class="btn-icon" data-action="pkt-refresh" title="Refresh" aria-label="Refresh"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-arrow-clockwise"/></svg></button>
          <button class="btn-icon" id="pktPauseBtn" data-action="pkt-pause" title="Pause live updates" aria-label="Pause live updates"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-pause"/></svg></button>
          <button class="btn-icon" data-action="pkt-byop" title="Bring Your Own Packet" aria-label="Bring Your Own Packet - paste raw packet hex for analysis" aria-haspopup="dialog"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-package"/></svg> BYOP</button>
        </div>
      </div>
      <div class="filter-group pkt-filter-expr" style="flex:1;margin-bottom:8px;position:relative">
        <input type="text" id="packetFilterInput" class="packet-filter-input"
          placeholder='Filter: type == Advert && snr > 5 · payload.name contains "Gilroy"'
          aria-label="Packet filter expression"
          style="width:100%;padding:6px 10px;border:1px solid var(--border);border-radius:6px;font-family:var(--mono);font-size:13px;background:var(--input-bg);color:var(--text)">
        <div id="packetFilterError" style="color:var(--status-red);font-size:11px;margin-top:2px;display:none"></div>
        <div id="packetFilterCount" style="color:var(--text-muted);font-size:11px;margin-top:2px;display:none"></div>
      </div>
      <div class="filter-bar" id="pktFilters">
        <button class="btn filter-toggle-btn" id="filterToggleBtn">Filters ▾</button>
        <!-- #1124 (MAJOR-3) Group 1: Filter input + Clear -->
        <div class="filter-group filter-group-clear">
          <button class="btn btn-clear-filters" id="clearFiltersBtn" title="Clear all filters" style="display:none;font-size:12px;padding:2px 8px;color:var(--text-muted);border:1px solid var(--border);border-radius:4px;background:transparent;cursor:pointer"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg> Clear</button>
        </div>
        <!-- Group 2: Quick filters (hash, node name) -->
        <div class="filter-group filter-group-quick">
          <input type="text" placeholder="Packet hash…" id="fHash" aria-label="Filter by packet hash" title="Filter packets by hex hash prefix">
          <div class="node-filter-wrap" style="position:relative">
            <input type="text" placeholder="Node name…" id="fNode" autocomplete="off" role="combobox" aria-expanded="false" aria-owns="fNodeDropdown" aria-activedescendant="" aria-autocomplete="list" title="Filter packets involving this node (sender or path)">
            <div class="node-filter-dropdown hidden" id="fNodeDropdown" role="listbox"></div>
          </div>
        </div>
        <!-- Group 3: Quick toggles (time range, Group by Hash, My Nodes)
             — #1128 Bug 5: placed BEFORE the Observer/Region/Type/Channel
             dropdowns so the most-frequently-used controls sit next to
             the search input where the eye lands first. -->
        <div class="filter-group filter-group-toggles">
          <button class="btn ${groupByHash ? 'active' : ''}" id="fGroup" title="Collapse duplicate observations of the same packet into expandable groups">Group by Hash</button>
          <button class="btn" id="fMyNodes" title="Show only packets from your favorited/claimed nodes"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star-fill"/></svg> My Nodes</button>
          <select id="fTimeWindow" class="filter-select" aria-label="Time window filter">
            <option value="15">Last 15 min</option>
            <option value="30">Last 30 min</option>
            <option value="60">Last 1 hour</option>
            <option value="180">Last 3 hours</option>
            <option value="360"${isMobile ? ' disabled title="Disabled on mobile to prevent browser crashes"' : ''}>Last 6 hours</option>
            <option value="720"${isMobile ? ' disabled title="Disabled on mobile to prevent browser crashes"' : ''}>Last 12 hours</option>
            <option value="1440"${isMobile ? ' disabled title="Disabled on mobile to prevent browser crashes"' : ''}>Last 24 hours</option>
            ${isMobile ? '' : '<option value="0">All time</option>'}
          </select>
        </div>
        <!-- Group 4: Dropdowns (observers, regions, types, channels) -->
        <div class="filter-group filter-group-dropdowns">
          <div class="multi-select-wrap" id="observerFilterWrap">
            <button class="multi-select-trigger" id="observerTrigger" title="Show only packets seen by selected observer stations">All Observers ▾</button>
            <div class="multi-select-menu" id="observerMenu">
              <div class="multi-select-search-wrap">
                <input type="text" id="observerSearchInput" class="multi-select-search" placeholder="Search observers…" autocomplete="off" aria-label="Search observers">
              </div>
              <div class="multi-select-list" id="observerList"></div>
            </div>
          </div>
          <div id="packetsRegionFilter" class="region-filter-container" style="display:inline-block;vertical-align:middle"></div>
          <div id="packetsAreaFilter" style="display:none;vertical-align:middle"></div>
          <div class="multi-select-wrap" id="typeFilterWrap">
            <button class="multi-select-trigger" id="typeTrigger" title="Filter by packet type">All Types ▾</button>
            <div class="multi-select-menu" id="typeMenu"></div>
          </div>
          <select id="fChannel" class="filter-select" aria-label="Filter by channel" title="Filter Channel Messages (GRP_TXT) by channel">
            <option value="">All Channels</option>
          </select>
        </div>
        <!-- Group 5: Sort + Columns -->
        <div class="filter-group filter-group-sort">
          <select id="fObsSort" aria-label="Observation sort order" title="Controls how observations are ordered within packet groups and which observation appears in the header row. Observer: Groups by observer station, earliest first. Path: Orders by hop count. Time: Orders by observation timestamp.">
            <option value="observer">Sort: Observer</option>
            <option value="path-asc">Sort: Path ↑ (shortest)</option>
            <option value="path-desc">Sort: Path ↓ (longest)</option>
            <option value="chrono-asc">Sort: Time ↑ (earliest)</option>
            <option value="chrono-desc">Sort: Time ↓ (latest)</option>
          </select>
          <span class="sort-help" id="sortHelpIcon" tabindex="0" role="button" aria-label="Sort help">ⓘ</span>
          <div class="col-toggle-wrap">
            <button class="col-toggle-btn" id="colToggleBtn" title="Show/hide table columns">Columns ▾</button>
            <div class="col-toggle-menu" id="colToggleMenu"></div>
          </div>
          <button class="btn btn-icon${showHexHashes ? ' active' : ''}" id="hexHashToggle" title="Show raw hex hash prefixes instead of resolved node names in the path column">Hex Paths</button>
        </div>
      </div>
      <div class="path-symbols-legend-wrapper">${(window.HopDisplay && HopDisplay.renderPathSymbolsLegend) ? HopDisplay.renderPathSymbolsLegend() : ''}</div>
      <div class="table-fluid-wrap"><table class="data-table" id="pktTable">
        <thead><tr>
          <th scope="col" class="col-expand" data-priority="1"></th><th scope="col" class="col-region" data-sort-key="region" data-priority="3">Region</th><th scope="col" class="col-time" data-sort-key="time" data-type="date" data-priority="1">Time</th><th scope="col" class="col-hash" data-sort-key="hash" data-priority="3">Hash</th><th scope="col" class="col-size" data-sort-key="size" data-type="numeric" data-priority="4">Size</th>
          <th scope="col" class="col-hashsize" data-sort-key="hb" data-type="numeric" data-priority="5">HB</th>
          <th scope="col" class="col-type" data-sort-key="type" data-priority="1">Type</th><th scope="col" class="col-scope" data-sort-key="scope" data-priority="4">Scope</th><th scope="col" class="col-observer" data-sort-key="observer" data-priority="3">Observer</th><th scope="col" class="col-path" data-sort-key="path" data-priority="5">Path</th><th scope="col" class="col-rpt" data-sort-key="rpt" data-type="numeric" data-priority="3">Rpt</th><th scope="col" class="col-details" data-priority="1">Details</th>
        </tr></thead>
        <tbody id="pktBody"></tbody>
      </table></div>
    `;

    // Init shared RegionFilter component
    RegionFilter.init(document.getElementById('packetsRegionFilter'), { dropdown: true });
    AreaFilter.init(document.getElementById('packetsAreaFilter'));
    if (_pendingUrlRegion) {
      RegionFilter.setSelected(_pendingUrlRegion.split(',').filter(Boolean));
      _pendingUrlRegion = null;
    }
    RegionFilter.onChange(function() { updatePacketsUrl(); loadPackets(); });
    AreaFilter.onChange(function() { updatePacketsUrl(); loadPackets(); });

    // --- Packet Filter Language ---
    (function() {
      var pfInput = document.getElementById('packetFilterInput');
      var pfError = document.getElementById('packetFilterError');
      var pfCount = document.getElementById('packetFilterCount');
      if (!pfInput || !window.PacketFilter) return;
      // Restore Wireshark filter expression from URL
      if (filters._filterExpr) {
        pfInput.value = filters._filterExpr;
        var _restored = PacketFilter.compile(filters._filterExpr);
        if (!_restored.error) { pfInput.classList.add('filter-active'); filters._packetFilter = _restored.filter; }
      }
      var pfTimer = null;
      pfInput.addEventListener('input', function() {
        clearTimeout(pfTimer);
        pfTimer = setTimeout(function() {
          var expr = pfInput.value.trim();
          if (!expr) {
            pfInput.classList.remove('filter-error', 'filter-active');
            pfError.style.display = 'none';
            pfCount.style.display = 'none';
            filters._packetFilter = null;
            filters._filterExpr = undefined;
            updatePacketsUrl();
            renderTableRows();
            return;
          }
          var compiled = PacketFilter.compile(expr);
          if (compiled.error) {
            pfInput.classList.add('filter-error');
            pfInput.classList.remove('filter-active');
            pfError.textContent = compiled.error;
            pfError.style.display = 'block';
            pfCount.style.display = 'none';
            filters._packetFilter = null;
            filters._filterExpr = undefined;
            updatePacketsUrl();
            renderTableRows();
          } else {
            pfInput.classList.remove('filter-error');
            pfInput.classList.add('filter-active');
            pfError.style.display = 'none';
            filters._packetFilter = compiled.filter;
            filters._filterExpr = expr;
            updatePacketsUrl();
            renderTableRows();
          }
        }, 300);
      });
    })();

    // Wireshark-style filter UX (#966): help popover, autocomplete, right-click
    // context menu, saved-filter dropdown. Idempotent — safe to re-call.
    if (window.FilterUX && typeof window.FilterUX.init === 'function') {
      window.FilterUX.init();
    }
    // #1124 (MAJOR-1): wire the path overflow popover (delegated; idempotent).
    _wirePathOverflowPopover();

    // --- Observer multi-select ---
    const obsMenu = document.getElementById('observerMenu');
    const obsList = document.getElementById('observerList');
    const obsSearchInput = document.getElementById('observerSearchInput');
    const obsTrigger = document.getElementById('observerTrigger');
    const selectedObservers = new Set(filters.observer ? filters.observer.split(',') : []);
    function applyObserverSearchFilter() {
      const term = (obsSearchInput.value || '').trim().toLowerCase();
      obsList.querySelectorAll('.multi-select-item[data-obs-name]').forEach((item) => {
        item.style.display = (!term || item.dataset.obsName.startsWith(term)) ? '' : 'none';
      });
    }
    function buildObserverMenu() {
      const allChecked = selectedObservers.size === 0;
      let html = `<label class="multi-select-item"><input type="checkbox" data-obs-id="__all__" ${allChecked ? 'checked' : ''}> All Observers</label>`;
      if (observers.length === 0) {
        // #1693 — observers fetch may still be in flight (decoupled from
        // packet row render). Show a disabled placeholder until loadObservers
        // resolves and calls _rebuildObserverMenu().
        html += `<label class="multi-select-item" style="opacity:0.6"><input type="checkbox" disabled> Loading observers…</label>`;
      } else {
        for (const o of observers) {
          const checked = selectedObservers.has(String(o.id)) ? 'checked' : '';
          const name = o.name || String(o.id);
          html += `<label class="multi-select-item" data-obs-name="${escapeHtml(name.toLowerCase())}"><input type="checkbox" data-obs-id="${o.id}" ${checked}> ${escapeHtml(name)}</label>`;
        }
      }
      obsList.innerHTML = html;
      applyObserverSearchFilter();
    }
    obsSearchInput.addEventListener('click', (e) => e.stopPropagation());
    obsSearchInput.addEventListener('input', applyObserverSearchFilter);
    // #1693 — expose for loadObservers() to refresh on resolve.
    _rebuildObserverMenu = () => { buildObserverMenu(); updateObsTrigger(); };
    function updateObsTrigger() {
      if (selectedObservers.size === 0 || selectedObservers.size === observers.length) {
        obsTrigger.textContent = 'All Observers ▾';
      } else if (selectedObservers.size === 1) {
        const id = [...selectedObservers][0];
        const o = observerMap.get(id) || observerMap.get(Number(id));
        obsTrigger.textContent = (o ? (o.name || o.id) : id) + ' ▾';
      } else {
        obsTrigger.textContent = selectedObservers.size + ' Observers ▾';
      }
    }
    buildObserverMenu();
    updateObsTrigger();
    obsTrigger.addEventListener('click', (e) => {
      e.stopPropagation();
      obsMenu.classList.toggle('open');
      typeMenu.classList.remove('open');
      if (obsMenu.classList.contains('open')) obsSearchInput.focus();
    });
    obsMenu.addEventListener('change', (e) => {
      const id = e.target.dataset.obsId;
      if (id === '__all__') {
        selectedObservers.clear();
      } else {
        if (e.target.checked) selectedObservers.add(id); else selectedObservers.delete(id);
      }
      filters.observer = selectedObservers.size > 0 ? [...selectedObservers].join(',') : undefined;
      if (filters.observer) localStorage.setItem('meshcore-observer-filter', filters.observer); else localStorage.removeItem('meshcore-observer-filter');
      buildObserverMenu();
      updateObsTrigger();
      updatePacketsUrl();
      renderTableRows();
    });

    // --- Type multi-select ---
    const typeMenu = document.getElementById('typeMenu');
    const typeTrigger = document.getElementById('typeTrigger');
    const typeMap = SHORT_BY_ID;
    const selectedTypes = new Set(filters.type ? String(filters.type).split(',') : []);
    function buildTypeMenu() {
      const allChecked = selectedTypes.size === 0;
      let html = `<label class="multi-select-item"><input type="checkbox" data-type-id="__all__" ${allChecked ? 'checked' : ''}> All Types</label>`;
      for (const [k, v] of Object.entries(typeMap)) {
        const checked = selectedTypes.has(k) ? 'checked' : '';
        html += `<label class="multi-select-item"><input type="checkbox" data-type-id="${k}" ${checked}> ${v}</label>`;
      }
      typeMenu.innerHTML = html;
    }
    function updateTypeTrigger() {
      const total = Object.keys(typeMap).length;
      // #1128 (Bug 3): trigger has bounded max-width so long selections like
      // "TRACE,MULTIPART,GRP_TXT" get ellipsised. Always set the full label
      // as the `title` attribute so the user can recover it via tooltip.
      const fullList = [...selectedTypes].map(k => typeMap[k] || k).join(', ');
      if (selectedTypes.size === 0 || selectedTypes.size === total) {
        typeTrigger.textContent = 'All Types ▾';
        typeTrigger.title = 'Filter by packet type';
      } else if (selectedTypes.size === 1) {
        const k = [...selectedTypes][0];
        typeTrigger.textContent = (typeMap[k] || k) + ' ▾';
        typeTrigger.title = 'Selected: ' + fullList;
      } else {
        typeTrigger.textContent = selectedTypes.size + ' Types ▾';
        typeTrigger.title = 'Selected: ' + fullList;
      }
    }
    buildTypeMenu();
    updateTypeTrigger();
    typeTrigger.addEventListener('click', (e) => { e.stopPropagation(); typeMenu.classList.toggle('open'); obsMenu.classList.remove('open'); });
    typeMenu.addEventListener('change', (e) => {
      const id = e.target.dataset.typeId;
      if (id === '__all__') {
        selectedTypes.clear();
      } else {
        if (e.target.checked) selectedTypes.add(id); else selectedTypes.delete(id);
      }
      filters.type = selectedTypes.size > 0 ? [...selectedTypes].join(',') : undefined;
      if (filters.type) localStorage.setItem('meshcore-type-filter', filters.type); else localStorage.removeItem('meshcore-type-filter');
      buildTypeMenu();
      updateTypeTrigger();
      renderTableRows();
    });

    // --- Channel filter (#812) ---
    // Server-side filter: /api/packets?channel=<hash>. Triggers loadPackets()
    // (not just renderTableRows) so the filter applies before pagination.
    const channelSel = document.getElementById('fChannel');
    if (channelSel) {
      if (filters.channel) {
        // Pre-seed an option so the current filter shows as selected even
        // before the channels list arrives. Replaced when populateChannels resolves.
        const opt = document.createElement('option');
        opt.value = filters.channel;
        opt.textContent = filters.channel;
        opt.selected = true;
        channelSel.appendChild(opt);
      }
      api('/channels').then(data => {
        const channels = (data && data.channels) || [];
        // Build options via DOM API: channel names are network-supplied
        // and must NOT be interpolated into innerHTML (XSS, #812).
        // Sort alphabetically (case-insensitive) for predictable picker order;
        // the API returns last-activity order which is unstable for a dropdown.
        const sorted = channels.slice().sort((a, b) => {
          const an = (a.name || a.hash || '').toLowerCase();
          const bn = (b.name || b.hash || '').toLowerCase();
          return an < bn ? -1 : an > bn ? 1 : 0;
        });
        channelSel.textContent = '';
        const allOpt = document.createElement('option');
        allOpt.value = '';
        allOpt.textContent = 'All Channels';
        channelSel.appendChild(allOpt);
        let matched = false;
        for (const ch of sorted) {
          const v = ch.hash || ch.name || '';
          if (!v) continue;
          const opt = document.createElement('option');
          opt.value = v;
          opt.textContent = ch.name || v;
          if (v === filters.channel) { opt.selected = true; matched = true; }
          channelSel.appendChild(opt);
        }
        // If current filter isn't in the list (encrypted hash, stale, or
        // race with cache), keep it as a selected option so the UI reflects state.
        if (filters.channel && !matched) {
          const opt = document.createElement('option');
          opt.value = filters.channel;
          opt.textContent = filters.channel;
          opt.selected = true;
          channelSel.appendChild(opt);
        }
      }).catch(() => {});
      channelSel.addEventListener('change', (e) => {
        filters.channel = e.target.value || undefined;
        updatePacketsUrl();
        loadPackets();
      });
    }

    // Close multi-select menus on outside click
    bindDocumentHandler('menu', 'click', (e) => {
      const obsWrap = document.getElementById('observerFilterWrap');
      const typeWrap = document.getElementById('typeFilterWrap');
      if (obsWrap && !obsWrap.contains(e.target)) { const m = obsWrap.querySelector('.multi-select-menu'); if (m) m.classList.remove('open'); }
      if (typeWrap && !typeWrap.contains(e.target)) { const m = typeWrap.querySelector('.multi-select-menu'); if (m) m.classList.remove('open'); }
    });

    // Filter toggle button for mobile
    document.getElementById('filterToggleBtn').addEventListener('click', function() {
      const bar = document.getElementById('pktFilters');
      bar.classList.toggle('filters-expanded');
      this.textContent = bar.classList.contains('filters-expanded') ? 'Filters ▴' : 'Filters ▾';
    });

    // --- Clear filters button ---
    const clearBtn = document.getElementById('clearFiltersBtn');
    if (clearBtn) clearBtn.addEventListener('click', function() {
      // Reset filters object
      filters.hash = undefined;
      filters.node = undefined;
      filters.nodeName = undefined;
      filters.observer = undefined;
      filters.channel = undefined;
      filters.type = undefined;
      filters._filterExpr = undefined;
      filters._packetFilter = null;
      filters.myNodes = false;
      _observerFilterSet = null;

      // Clear localStorage filter entries
      localStorage.removeItem('meshcore-observer-filter');
      localStorage.removeItem('meshcore-type-filter');

      // Reset DOM inputs
      document.getElementById('fHash').value = '';
      document.getElementById('fNode').value = '';
      var pfInput = document.getElementById('packetFilterInput');
      if (pfInput) { pfInput.value = ''; pfInput.classList.remove('filter-active', 'filter-error'); }
      var pfError = document.getElementById('packetFilterError');
      if (pfError) pfError.style.display = 'none';
      var pfCount = document.getElementById('packetFilterCount');
      if (pfCount) pfCount.style.display = 'none';
      document.getElementById('fChannel').value = '';
      document.getElementById('fMyNodes').classList.remove('active');

      // Reset observer multi-select
      var obMenu = document.getElementById('observerMenu');
      if (obMenu) obMenu.querySelectorAll('input[type=checkbox]').forEach(function(cb) { cb.checked = false; });
      document.getElementById('observerTrigger').textContent = 'All Observers ▾';

      // Reset type multi-select
      var typeMenu = document.getElementById('typeMenu');
      if (typeMenu) typeMenu.querySelectorAll('input[type=checkbox]').forEach(function(cb) { cb.checked = false; });
      document.getElementById('typeTrigger').textContent = 'All Types ▾';

      // Reset time window to default
      savedTimeWindowMin = DEFAULT_TIME_WINDOW;
      var fTW = document.getElementById('fTimeWindow');
      if (fTW) fTW.value = String(DEFAULT_TIME_WINDOW);
      localStorage.removeItem('meshcore-time-window');

      // Reset region filter
      RegionFilter.setSelected([]);

      // Update URL and reload
      updatePacketsUrl();
      loadPackets();
    });
    // Show clear button if page loaded with active filters (e.g. from URL params)
    updatePacketsUrl();

    // Filter event listeners
    document.getElementById('fHash').value = filters.hash || '';
    document.getElementById('fHash').addEventListener('input', debounce((e) => { filters.hash = e.target.value || undefined; updatePacketsUrl(); loadPackets(); }, 300));

    // Time window dropdown — restore from localStorage and bind change
    const fTimeWindow = document.getElementById('fTimeWindow');
    fTimeWindow.value = String(savedTimeWindowMin);
    fTimeWindow.addEventListener('change', () => {
      savedTimeWindowMin = Number(fTimeWindow.value);
      if (!Number.isFinite(savedTimeWindowMin) || savedTimeWindowMin <= 0) savedTimeWindowMin = 15;
      localStorage.setItem('meshcore-time-window', fTimeWindow.value);
      updatePacketsUrl();
      loadPackets();
    });

    document.getElementById('fGroup').addEventListener('click', () => { groupByHash = !groupByHash; loadPackets(); });
    document.getElementById('fMyNodes').addEventListener('click', function () {
      filters.myNodes = !filters.myNodes;
      this.classList.toggle('active', filters.myNodes);
      loadPackets();
    });

    // Observation sort dropdown
    const obsSortSel = document.getElementById('fObsSort');
    obsSortSel.value = obsSortMode;
    const sortHelpEl = document.getElementById('sortHelpIcon');
    if (sortHelpEl) {
      const tip = document.createElement('span');
      tip.className = 'sort-help-tip';
      tip.textContent = "Sort controls how observations are ordered within packet groups and which observation appears in the header row.\n\nObserver — Groups by observer station, earliest first.\nPath \u2191 — Shortest paths first.\nPath \u2193 — Longest paths first.\nTime \u2191 — Earliest observation first.\nTime \u2193 — Most recent first.";
      sortHelpEl.appendChild(tip);
    }
    obsSortSel.addEventListener('change', async function () {
      obsSortMode = this.value;
      localStorage.setItem('meshcore-obs-sort', obsSortMode);
      // For non-observer sorts, batch-fetch children for visible groups that don't have them yet
      if (obsSortMode !== SORT_OBSERVER && groupByHash) {
        const toFetch = packets.filter(p => p.hash && !p._children && (p.observation_count || 0) > 1);
        if (toFetch.length > 0) {
          const hashes = toFetch.map(p => p.hash);
          try {
            const resp = await fetch('/api/packets/observations', {
              method: 'POST',
              headers: {'Content-Type': 'application/json'},
              body: JSON.stringify({hashes})
            });
            if (resp.ok) {
              const data = await resp.json();
              const results = data.results || {};
              for (const p of toFetch) {
                const obs = results[p.hash];
                if (obs && obs.length) {
                  p._children = obs.map(o => clearParsedCache({...p, ...o, _isObservation: true}));
                  p._fetchedData = {packet: p, observations: obs};
                }
              }
            }
          } catch {}
        }
      }
      // Re-sort all groups with children
      for (const p of packets) {
        if (p._children) sortGroupChildren(p);
      }
      // Resolve any new hops from updated header paths
      const newHops = new Set();
      for (const p of packets) {
        try { getParsedPath(p).forEach(h => { if (!(h in hopNameCache)) newHops.add(h); }); } catch {}
      }
      if (newHops.size) await resolveHops([...newHops]);
      renderTableRows();
    });

    // Column visibility toggle (#71)
    const COL_DEFS = [
      { key: 'region', label: 'Region' },
      { key: 'time', label: 'Time' },
      { key: 'hash', label: 'Hash' },
      { key: 'size', label: 'Size' },
      { key: 'type', label: 'Type' },
      { key: 'scope', label: 'Scope' },
      { key: 'observer', label: 'Observer' },
      { key: 'path', label: 'Path' },
      { key: 'rpt', label: 'Rpt' },
      { key: 'details', label: 'Details' },
    ];
    const isNarrow = window.innerWidth <= 640;
    // #1249: observer column must stay visible at narrow widths so the IATA
    // badge (#1188) renders on mobile. Without observer in scope the user
    // can't see who heard the packet at all.
    const defaultHidden = isNarrow ? ['region', 'hash', 'path', 'rpt', 'size', 'scope'] : ['region'];
    let visibleCols;
    let knownCols;
    try {
      visibleCols = JSON.parse(localStorage.getItem('packets-visible-cols'));
      knownCols = JSON.parse(localStorage.getItem('packets-known-cols'));
    } catch {}
    if (!visibleCols) visibleCols = COL_DEFS.map(c => c.key).filter(k => !defaultHidden.includes(k));
    else {
      // A column added after the visitor last saved their preferences is absent
      // from the stored array for the same reason a column they unchecked is:
      // the array alone can't tell the two apart, so a new column would arrive
      // silently hidden. `packets-known-cols` records which keys existed at save
      // time; anything newer than that gets the default treatment instead.
      if (!Array.isArray(knownCols)) knownCols = ['region', 'time', 'hash', 'size', 'type', 'observer', 'path', 'rpt', 'details'];
      COL_DEFS.forEach(c => {
        if (!knownCols.includes(c.key) && !visibleCols.includes(c.key) && !defaultHidden.includes(c.key)) {
          visibleCols.push(c.key);
        }
      });
    }
    const colMenu = document.getElementById('colToggleMenu');
    const pktTable = document.getElementById('pktTable');
    function applyColVisibility() {
      COL_DEFS.forEach(c => {
        pktTable.classList.toggle('hide-col-' + c.key, !visibleCols.includes(c.key));
      });
      localStorage.setItem('packets-visible-cols', JSON.stringify(visibleCols));
      localStorage.setItem('packets-known-cols', JSON.stringify(COL_DEFS.map(c => c.key)));
    }
    colMenu.innerHTML = COL_DEFS.map(c =>
      `<label><input type="checkbox" data-col="${c.key}" ${visibleCols.includes(c.key) ? 'checked' : ''}> ${c.label}</label>`
    ).join('');
    colMenu.addEventListener('change', (e) => {
      const cb = e.target;
      const col = cb.dataset.col;
      if (!col) return;
      if (cb.checked) { if (!visibleCols.includes(col)) visibleCols.push(col); }
      else { visibleCols = visibleCols.filter(k => k !== col); }
      applyColVisibility();
    });
    document.getElementById('colToggleBtn').addEventListener('click', (e) => {
      e.stopPropagation();
      colMenu.classList.toggle('open');
    });
    bindDocumentHandler('colmenu', 'click', () => colMenu.classList.remove('open'));
    applyColVisibility();

    document.getElementById('hexHashToggle').addEventListener('click', function () {
      showHexHashes = !showHexHashes;
      localStorage.setItem('meshcore-hex-hashes', showHexHashes);
      this.classList.toggle('active', showHexHashes);
      renderTableRows();
    });

    // Node name filter with autocomplete
    const fNode = document.getElementById('fNode');
    const fNodeDrop = document.getElementById('fNodeDropdown');
    fNode.value = filters.nodeName || '';
    let nodeActiveIdx = -1;
    fNode.addEventListener('input', debounce(async (e) => {
      const q = e.target.value.trim();
      nodeActiveIdx = -1;
      fNode.setAttribute('aria-activedescendant', '');
      if (!q) {
        fNodeDrop.classList.add('hidden');
        fNode.setAttribute('aria-expanded', 'false');
        if (filters.node) { filters.node = undefined; filters.nodeName = undefined; updatePacketsUrl(); loadPackets(); }
        return;
      }
      try {
        const resp = await fetch('/api/nodes/search?q=' + encodeURIComponent(q));
        const data = await resp.json();
        const nodes = data.nodes || [];
        if (nodes.length === 0) { fNodeDrop.classList.add('hidden'); fNode.setAttribute('aria-expanded', 'false'); return; }
        fNodeDrop.innerHTML = nodes.map((n, i) =>
          `<div class="node-filter-option" id="fNodeOpt-${i}" role="option" data-key="${n.public_key}" data-name="${escapeHtml(n.name || n.public_key.slice(0,8))}">${escapeHtml(n.name || n.public_key.slice(0,8))} <span style="color:var(--text-muted);font-size:0.8em">${n.public_key.slice(0,8)}</span></div>`
        ).join('');
        fNodeDrop.classList.remove('hidden');
        fNode.setAttribute('aria-expanded', 'true');
        fNodeDrop.querySelectorAll('.node-filter-option').forEach(opt => {
          opt.addEventListener('click', () => {
            selectNodeOption(opt);
          });
        });
      } catch {}
    }, 250));

    function selectNodeOption(opt) {
      filters.node = opt.dataset.key;
      filters.nodeName = opt.dataset.name;
      fNode.value = opt.dataset.name;
      fNodeDrop.classList.add('hidden');
      fNode.setAttribute('aria-expanded', 'false');
      fNode.setAttribute('aria-activedescendant', '');
      nodeActiveIdx = -1;
      updatePacketsUrl();
      loadPackets();
    }

    fNode.addEventListener('keydown', (e) => {
      const options = fNodeDrop.querySelectorAll('.node-filter-option');
      if (!options.length || fNodeDrop.classList.contains('hidden')) return;
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        nodeActiveIdx = Math.min(nodeActiveIdx + 1, options.length - 1);
        updateNodeActive(options);
      } else if (e.key === 'ArrowUp') {
        e.preventDefault();
        nodeActiveIdx = Math.max(nodeActiveIdx - 1, 0);
        updateNodeActive(options);
      } else if (e.key === 'Enter') {
        e.preventDefault();
        if (nodeActiveIdx >= 0 && options[nodeActiveIdx]) selectNodeOption(options[nodeActiveIdx]);
      } else if (e.key === 'Escape') {
        fNodeDrop.classList.add('hidden');
        fNode.setAttribute('aria-expanded', 'false');
        nodeActiveIdx = -1;
      }
    });

    function updateNodeActive(options) {
      options.forEach((o, i) => {
        o.classList.toggle('node-filter-active', i === nodeActiveIdx);
        o.setAttribute('aria-selected', i === nodeActiveIdx ? 'true' : 'false');
      });
      if (nodeActiveIdx >= 0 && options[nodeActiveIdx]) {
        fNode.setAttribute('aria-activedescendant', options[nodeActiveIdx].id);
        options[nodeActiveIdx].scrollIntoView({ block: 'nearest' });
      }
    }

    fNode.addEventListener('blur', () => { setTimeout(() => { fNodeDrop.classList.add('hidden'); fNode.setAttribute('aria-expanded', 'false'); }, 200); });

    // Delegated click/keyboard handler for table rows
    const pktBody = document.getElementById('pktBody');
    if (pktBody) {
      const handler = (e) => {
        // Let hop links navigate naturally without selecting the row
        if (e.target.closest('[data-hop-link]')) return;
        const row = e.target.closest('tr[data-action]');
        if (!row) return;
        if (e.type === 'keydown' && e.key !== 'Enter' && e.key !== ' ') return;
        if (e.type === 'keydown') e.preventDefault();
        const action = row.dataset.action;
        const value = row.dataset.value;
        if (action === 'select') {
          const hash = row.dataset.hash;
          if (hash) selectPacket(null, hash);
          else selectPacket(Number(value));
        }
        else if (action === 'select-observation') {
          const parentHash = row.dataset.parentHash;
          const group = hashIndex.get(parentHash);
          const child = group?._children?.find(c => String(c.id) === String(value));
          if (child) {
            const parentData = group._fetchedData;
            const obsPacket = parentData ? {...parentData.packet, observer_id: child.observer_id, observer_name: child.observer_name, snr: child.snr, rssi: child.rssi, path_json: child.path_json, resolved_path: child.resolved_path, direction: child.direction, timestamp: child.timestamp, first_seen: child.timestamp} : child;
            if (parentData) { clearParsedCache(obsPacket); }
            selectPacket(child.id, parentHash, {packet: obsPacket, observations: parentData?.observations}, child.id);
          }
        }
        else if (action === 'select-hash') pktSelectHash(value);
        else if (action === 'toggle-select') {
          // #1486: pktToggleGroup() already opens the detail panel on EXPAND
          // (via selectPacket()), and must NOT open it on COLLAPSE. The
          // previously-unconditional pktSelectHash() trailing call was both
          // redundant on expand AND reopened the panel the operator had just
          // closed when they clicked the chevron to collapse — drop it.
          pktToggleGroup(value);
        }
      };
      pktBody.addEventListener('click', handler);
      pktBody.addEventListener('keydown', handler);
    }

    // Escape to close packet detail panel
    document.addEventListener('keydown', function pktEsc(e) {
      if (e.key === 'Escape') {
        closeDetailPanel();
      }
    });

    renderTableRows();
    makeColumnsResizable('#pktTable', 'meshcore-pkt-col-widths');
    // #1056: register fluid-column responsive behavior (drops priority>1 cols
    // when narrow, shows "+N hidden" pill, reveals on click). Idempotent.
    if (window.TableResponsive) {
      var _pktTbl = document.getElementById('pktTable');
      if (_pktTbl) window.TableResponsive.register(_pktTbl);
    }

    // Initialize table sorting (virtual scroll — sort data array, not DOM)
    if (window.TableSort) {
      var pktTableEl = document.getElementById('pktTable');
      if (pktTableEl) {
        if (_tableSortInstance) _tableSortInstance.destroy();
        _tableSortInstance = TableSort.init(pktTableEl, {
          defaultColumn: 'time',
          defaultDirection: 'desc',
          storageKey: 'meshcore-packets-sort',
          domReorder: false,
          onSort: function(column, direction) {
            _packetSortColumn = column;
            _packetSortDirection = direction;
            sortPacketsArray();
            renderTableRows();
            updatePacketsUrl();
          }
        });
        // Apply initial sort state from TableSort
        if (_tableSortInstance) {
          var st = _tableSortInstance.getState();
          _packetSortColumn = st.column;
          _packetSortDirection = st.direction;
          sortPacketsArray();
        }
      }
    }
  }

  // Build HTML for a single grouped packet row
  function buildGroupRowHtml(p, entryIdx = -1) {
    const isExpanded = expandedHashes.has(p.hash);
    let headerObserverId = p.observer_id;
    let headerPathJson = p.path_json;
    if (_observerFilterSet && p._children?.length) {
      const match = p._children.find(c => _observerFilterSet.has(String(c.observer_id)));
      if (match) {
        headerObserverId = match.observer_id;
        headerPathJson = match.path_json;
      }
    }
    const groupRegion = headerObserverId ? (observerMap.get(headerObserverId)?.iata || '') : '';
    let groupPath = [];
    try { groupPath = JSON.parse(headerPathJson || '[]'); } catch {}
    const groupPathStr = renderPath(groupPath, headerObserverId);
    const groupTypeName = payloadTypeName(p.payload_type);
    const groupTypeClass = payloadTypeColor(p.payload_type);
    const groupSize = p.raw_hex ? Math.floor(p.raw_hex.length / 2) : 0;
    const _grpPlOff = getPathLenOffset(p.route_type);
    // #1849: TRACE (payload_type=9) header path bytes are per-hop SNR readings,
    // not truncated hop hashes (see internal/packetpath/route.go PathBytesAreHops).
    // The (>>6)+1 derivation is meaningless for TRACE — render — with a tooltip.
    const _grpIsTrace = p.payload_type === 9;
    const groupHashBytes = _grpIsTrace ? '—' : (((parseInt(p.raw_hex?.slice(_grpPlOff * 2, _grpPlOff * 2 + 2), 16) || 0) >> 6) + 1);
    const _grpHashSizeTitle = _grpIsTrace ? ' title="TRACE path bytes are SNR readings, not hash prefixes — see sidebar decoder for actual hop count"' : '';
    const isSingle = p.count <= 1;
    // Channel color highlighting (#271)
    const _grpDecoded = getParsedDecoded(p) || {};
    const _grpChanStyle = window.ChannelColors ? window.ChannelColors.getRowStyle(_grpDecoded.type || groupTypeName, _grpDecoded.channel) : '';
    const _grpHashStripe = _hashStripeStyle(p.hash);
    const _grpStyle = _grpHashStripe + _grpChanStyle;
    let html = `<tr class="${isSingle ? '' : 'group-header'} ${isExpanded ? 'expanded' : ''}" data-hash="${p.hash}" data-action="${isSingle ? 'select-hash' : 'toggle-select'}" data-value="${p.hash}" data-entry-idx="${entryIdx}" tabindex="0" role="row"${_grpStyle ? ' style="' + _grpStyle + '"' : ''}>
          <td class="col-expand" style="text-align:center;cursor:pointer">${isSingle ? '' : (isExpanded ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-down"/></svg>' : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-up"/></svg>')}</td>
          <td class="col-region">${groupRegion ? `<span class="badge-region">${groupRegion}</span>` : '—'}</td>
          <td class="col-time">${renderTimestampCell(p.latest)}</td>
          <td class="mono col-hash" data-filter-field="hash" data-filter-value="${escapeHtml(p.hash || '')}">${truncate(p.hash || '—', 8)}</td>
          <td class="col-size" data-filter-field="size" data-filter-value="${groupSize || ''}">${groupSize ? groupSize + 'B' : '—'}</td>
          <td class="col-hashsize mono"${_grpHashSizeTitle}>${groupHashBytes}</td>
          <td class="col-type" data-filter-field="type" data-filter-value="${escapeHtml(groupTypeName || '')}">${p.payload_type != null ? `<span class="badge badge-${groupTypeClass}">${groupTypeName}</span>${transportBadge(p.route_type)}` : '—'}</td>
          <td class="col-scope" data-filter-field="scope" data-filter-value="${escapeHtml(p.scope_name || '')}">${scopeCellHtml(p.scope_name)}</td>
          <td class="col-observer" data-filter-field="observer" data-filter-value="${escapeHtml(obsNameOnly(headerObserverId) || '')}">${isSingle ? escapeHtml(truncate(obsNameOnly(headerObserverId), 16)) + obsIataBadge(p) : escapeHtml(truncate(obsNameOnly(headerObserverId), 10)) + groupedObserverIataBadgesHtml(p)}</td>
          <td class="col-path"><span class="path-hops">${groupPathStr}</span></td>
          <td class="col-rpt">${p.observation_count > 1 ? '<span class="badge badge-obs" title="Seen ' + p.observation_count + ' times"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> ' + p.observation_count + '</span>' : (isSingle ? '' : p.count)}</td>
          <td class="col-details"><span class="col-details-clip">${getDetailPreview(getParsedDecoded(p))}</span></td>
        </tr>`;
    if (isExpanded && p._children) {
      let visibleChildren = p._children;
      if (_observerFilterSet) {
        visibleChildren = visibleChildren.filter(c => _observerFilterSet.has(String(c.observer_id)));
      }
      for (const c of visibleChildren) {
        const typeName = payloadTypeName(c.payload_type);
        const typeClass = payloadTypeColor(c.payload_type);
        const size = c.raw_hex ? Math.floor(c.raw_hex.length / 2) : 0;
        const childPath = getParsedPath(c);
        const _cPlOff = getPathLenOffset(p.route_type);
        // #1849: TRACE header path bytes are SNR readings, not hop hashes.
        const _cIsTrace = c.payload_type === 9;
        const childHashBytes = _cIsTrace
          ? '—'
          : (c.raw_hex
            ? (((parseInt(c.raw_hex.slice(_cPlOff * 2, _cPlOff * 2 + 2), 16) || 0) >> 6) + 1)
            : (childPath.length > 0 ? childPath[0].length / 2 : 0));
        const _cHashSizeTitle = _cIsTrace ? ' title="TRACE path bytes are SNR readings, not hash prefixes — see sidebar decoder for actual hop count"' : '';
        const childRegion = c.observer_id ? (observerMap.get(c.observer_id)?.iata || '') : '';
        const childPathStr = renderPath(childPath, c.observer_id);
        const _childHashStripe = _hashStripeStyle(c.hash || p.hash);
        html += `<tr class="group-child" data-id="${c.id}" data-hash="${c.hash || ''}" data-action="select-observation" data-value="${c.id}" data-parent-hash="${p.hash}" data-entry-idx="${entryIdx}" tabindex="0" role="row"${_childHashStripe ? ' style="' + _childHashStripe + '"' : ''}>
              <td class="col-expand"></td><td class="col-region">${childRegion ? `<span class="badge-region">${childRegion}</span>` : '—'}</td>
              <td class="col-time">${renderTimestampCell(c.timestamp)}</td>
              <td class="mono col-hash" data-filter-field="hash" data-filter-value="${escapeHtml(c.hash || '')}">${truncate(c.hash || '', 8)}</td>
              <td class="col-size" data-filter-field="size" data-filter-value="${size || ''}">${size}B</td>
              <td class="col-hashsize mono"${_cHashSizeTitle}>${childHashBytes}</td>
              <td class="col-type" data-filter-field="type" data-filter-value="${escapeHtml(typeName || '')}"><span class="badge badge-${typeClass}">${typeName}</span>${transportBadge(c.route_type)}</td>
              <td class="col-scope" data-filter-field="scope" data-filter-value="${escapeHtml(c.scope_name || '')}">${scopeCellHtml(c.scope_name)}</td>
              <td class="col-observer" data-filter-field="observer" data-filter-value="${escapeHtml(obsNameOnly(c.observer_id) || '')}">${escapeHtml(truncate(obsNameOnly(c.observer_id), 16))}${obsIataBadge(c)}</td>
              <td class="col-path"><span class="path-hops">${childPathStr}</span></td>
              <td class="col-rpt"></td>
              <td class="col-details"><span class="col-details-clip">${getDetailPreview(getParsedDecoded(c))}</span></td>
            </tr>`;
      }
    }
    return html;
  }

  // Build HTML for a single flat (ungrouped) packet row
  function buildFlatRowHtml(p, entryIdx = -1) {
    const decoded = getParsedDecoded(p) || {};
    const pathHops = getParsedPath(p) || [];
    const region = p.observer_id ? (observerMap.get(p.observer_id)?.iata || '') : '';
    const typeName = payloadTypeName(p.payload_type);
    const typeClass = payloadTypeColor(p.payload_type);
    // Channel color highlighting (#271)
    const _chanStyle = window.ChannelColors ? window.ChannelColors.getRowStyle(decoded.type || typeName, decoded.channel) : '';
    const size = p.raw_hex ? Math.floor(p.raw_hex.length / 2) : 0;
    const _flatPlOff = getPathLenOffset(p.route_type);
    // #1849: TRACE path bytes = SNR readings, not hop hashes.
    const _flatIsTrace = p.payload_type === 9;
    const hashBytes = _flatIsTrace ? '—' : (((parseInt(p.raw_hex?.slice(_flatPlOff * 2, _flatPlOff * 2 + 2), 16) || 0) >> 6) + 1);
    const _flatHashSizeTitle = _flatIsTrace ? ' title="TRACE path bytes are SNR readings, not hash prefixes — see sidebar decoder for actual hop count"' : '';
    const pathStr = renderPath(pathHops, p.observer_id);
    const detail = getDetailPreview(decoded);
    const _flatHashStripe = _hashStripeStyle(p.hash);
    const _flatStyle = _flatHashStripe + _chanStyle;
    return `<tr data-id="${p.id}" data-hash="${p.hash || ''}" data-action="select-hash" data-value="${p.hash || p.id}" data-entry-idx="${entryIdx}" tabindex="0" role="row" class="${selectedId === p.id ? 'selected' : ''}"${_flatStyle ? ' style="' + _flatStyle + '"' : ''}>
        <td class="col-expand"></td><td class="col-region">${region ? `<span class="badge-region">${region}</span>` : '—'}</td>
        <td class="col-time">${renderTimestampCell(p.timestamp)}</td>
        <td class="mono col-hash" data-filter-field="hash" data-filter-value="${escapeHtml(p.hash || '')}">${truncate(p.hash || String(p.id), 8)}</td>
        <td class="col-size" data-filter-field="size" data-filter-value="${size || ''}">${size}B</td>
        <td class="col-hashsize mono"${_flatHashSizeTitle}>${hashBytes}</td>
        <td class="col-type" data-filter-field="type" data-filter-value="${escapeHtml(typeName || '')}"><span class="badge badge-${typeClass}">${typeName}</span>${transportBadge(p.route_type)}</td>
        <td class="col-scope" data-filter-field="scope" data-filter-value="${escapeHtml(p.scope_name || '')}">${scopeCellHtml(p.scope_name)}</td>
        <td class="col-observer" data-filter-field="observer" data-filter-value="${escapeHtml(obsNameOnly(p.observer_id) || '')}">${escapeHtml(truncate(obsNameOnly(p.observer_id), 16))}${obsIataBadge(p)}</td>
        <td class="col-path"><span class="path-hops">${pathStr}</span></td>
        <td class="col-rpt"></td>
        <td class="col-details"><span class="col-details-clip">${detail}</span></td>
      </tr>`;
  }

  // Mark _rowCounts as stale so renderVisibleRows() recomputes them lazily.
  // Called when expanded group children change outside renderTableRows() (#410).
  function _invalidateRowCounts() {
    _rowCountsDirty = true;
    _cumulativeOffsetsCache = null;
  }

  // Recompute _rowCounts from _displayPackets if they've been invalidated.
  function _refreshRowCountsIfDirty() {
    if (!_rowCountsDirty || !_displayPackets.length) return;
    _rowCounts = _displayPackets.map(function(p) { return _getRowCount(p); });
    _cumulativeOffsetsCache = null;
    _rowCountsDirty = false;
  }

  // Compute the number of DOM <tr> rows a single entry produces.
  // Used by both row counting and renderVisibleRows to avoid divergence (#424).
  function _getRowCount(p) {
    if (!_displayGrouped) return 1;
    if (!expandedHashes.has(p.hash) || !p._children) return 1;
    let childCount = p._children.length;
    if (_observerFilterSet) {
      childCount = p._children.filter(c => _observerFilterSet.has(String(c.observer_id))).length;
    }
    return 1 + childCount;
  }

  // Get the column count from the thead (dynamic, avoids hardcoded colspan — #426)
  function _getColCount() {
    const thead = document.querySelector('#pktLeft thead tr');
    return thead ? thead.children.length : 11;
  }

  // Compute cumulative DOM row offsets from per-entry row counts.
  // Returns array where cumulativeOffsets[i] = total <tr> rows before entry i.
  function _cumulativeRowOffsets() {
    if (_cumulativeOffsetsCache) return _cumulativeOffsetsCache;
    const offsets = new Array(_rowCounts.length + 1);
    offsets[0] = 0;
    for (let i = 0; i < _rowCounts.length; i++) {
      offsets[i + 1] = offsets[i] + _rowCounts[i];
    }
    _cumulativeOffsetsCache = offsets;
    return offsets;
  }

  function renderVisibleRows() {
    const _rvr_t0 = performance.now();
    const tbody = document.getElementById('pktBody');
    if (!tbody || !_displayPackets.length) return;

    const scrollContainer = document.getElementById('pktLeft');
    if (!scrollContainer) return;

    // Recompute row counts if they were invalidated (e.g. WS added children) (#410)
    _refreshRowCountsIfDirty();

    // Compute total DOM rows accounting for expanded groups
    const offsets = _cumulativeRowOffsets();
    const totalDomRows = offsets[offsets.length - 1];
    const totalHeight = totalDomRows * VSCROLL_ROW_HEIGHT;
    const colCount = _getColCount();

    // Get or create spacer elements
    let topSpacer = document.getElementById('vscroll-top');
    let bottomSpacer = document.getElementById('vscroll-bottom');
    if (!topSpacer) {
      topSpacer = document.createElement('tr');
      topSpacer.id = 'vscroll-top';
      // aria-hidden + visibility:hidden so Playwright/AT treat the sentinel as invisible
      // while preserving its layout role (the inner <td> height drives virtual-scroll padding).
      topSpacer.setAttribute('aria-hidden', 'true');
      topSpacer.style.visibility = 'hidden';
      topSpacer.innerHTML = '<td colspan="' + colCount + '" style="padding:0;border:0"></td>';
    }
    if (!bottomSpacer) {
      bottomSpacer = document.createElement('tr');
      bottomSpacer.id = 'vscroll-bottom';
      bottomSpacer.setAttribute('aria-hidden', 'true');
      bottomSpacer.style.visibility = 'hidden';
      bottomSpacer.innerHTML = '<td colspan="' + colCount + '" style="padding:0;border:0"></td>';
    }

    // Calculate visible range based on scroll position
    const scrollTop = scrollContainer.scrollTop;
    const viewportHeight = scrollContainer.clientHeight;
    // Account for thead height (measured dynamically)
    const theadEl = scrollContainer.querySelector('thead');
    if (theadEl) _vscrollTheadHeight = theadEl.offsetHeight || _vscrollTheadHeight;

    const { startIdx, endIdx } = _calcVisibleRange(
      offsets, _displayPackets.length, scrollTop, viewportHeight,
      VSCROLL_ROW_HEIGHT, _vscrollTheadHeight, VSCROLL_BUFFER
    );

    // Skip DOM rebuild if visible range hasn't changed
    if (startIdx === _lastVisibleStart && endIdx === _lastVisibleEnd) {
      if (window.__PERF_LOG_RENDER) console.log('[perf] renderVisibleRows: skip (no change) %.2fms', performance.now() - _rvr_t0);
      return;
    }

    const prevStart = _lastVisibleStart;
    const prevEnd = _lastVisibleEnd;
    _lastVisibleStart = startIdx;
    _lastVisibleEnd = endIdx;

    // Compute padding using cumulative row counts
    const topPad = offsets[startIdx] * VSCROLL_ROW_HEIGHT;
    const bottomPad = (totalDomRows - offsets[endIdx]) * VSCROLL_ROW_HEIGHT;

    topSpacer.firstChild.style.height = topPad + 'px';
    bottomSpacer.firstChild.style.height = bottomPad + 'px';

    const builder = _displayGrouped ? buildGroupRowHtml : buildFlatRowHtml;
    const hasOverlap = prevStart !== -1 && startIdx < prevEnd && endIdx > prevStart;

    if (!hasOverlap) {
      // Full rebuild: initial render or large scroll jump past buffer
      const visibleHtml = _displayPackets.slice(startIdx, endIdx)
        .map((p, i) => builder(p, startIdx + i)).join('');
      tbody.innerHTML = '';
      tbody.appendChild(topSpacer);
      tbody.insertAdjacentHTML('beforeend', visibleHtml);
      tbody.appendChild(bottomSpacer);
      // Measure actual row height from first rendered data row (#407)
      if (!_vscrollRowHeightMeasured) {
        const firstRow = topSpacer.nextElementSibling;
        if (firstRow && firstRow !== bottomSpacer) {
          const h = firstRow.offsetHeight;
          if (h > 0) { VSCROLL_ROW_HEIGHT = h; _vscrollRowHeightMeasured = true; }
        }
      }
      if (window.__PERF_LOG_RENDER) console.log('[perf] renderVisibleRows: full rebuild %d entries, %.2fms', endIdx - startIdx, performance.now() - _rvr_t0);
      _finalizePathOverflow(tbody);
      // #1128 (Bug 1): hop-resolver mutates chip text from hex prefix to a
      // longer node name AFTER the initial finalize pass — chips that fit at
      // first measurement overflow once names resolve, but no `+N` pill gets
      // appended. Cheapest correct fix: re-measure on a delayed pass, after
      // clearing the per-host `overflowChecked` guard so the recheck runs.
      _scheduleReFinalizePathOverflow(tbody);
      return;
    }

    // Incremental update: remove rows that scrolled out at the top (positional)
    const headRowCount = offsets[Math.min(startIdx, prevEnd)] - offsets[prevStart];
    for (let r = 0; r < headRowCount; r++) {
      const row = topSpacer.nextElementSibling;
      if (row && row !== bottomSpacer) row.remove();
    }
    // Remove rows that scrolled out at the bottom (positional)
    const tailFrom = Math.max(endIdx, prevStart);
    const tailRowCount = offsets[prevEnd] - offsets[tailFrom];
    for (let r = 0; r < tailRowCount; r++) {
      const row = bottomSpacer.previousElementSibling;
      if (row && row !== topSpacer) row.remove();
    }
    // Prepend rows that scrolled into view at the top
    if (startIdx < prevStart) {
      let html = '';
      for (let i = startIdx; i < Math.min(prevStart, endIdx); i++) {
        html += builder(_displayPackets[i], i);
      }
      topSpacer.insertAdjacentHTML('afterend', html);
    }
    // Append rows that scrolled into view at the bottom
    if (endIdx > prevEnd) {
      let html = '';
      for (let i = Math.max(prevEnd, startIdx); i < endIdx; i++) {
        html += builder(_displayPackets[i], i);
      }
      bottomSpacer.insertAdjacentHTML('beforebegin', html);
    }
    if (window.__PERF_LOG_RENDER) console.log('[perf] renderVisibleRows: incremental head=%d tail=%d, %.2fms', headRowCount, tailRowCount, performance.now() - _rvr_t0);
    _finalizePathOverflow(tbody);
    _scheduleReFinalizePathOverflow(tbody);
  }

  // #1124 (MAJOR-1): when path chips overflow `.path-hops` (capped at 22px /
  // overflow:hidden in CSS), append a `<span class="path-overflow-pill">+N</span>`
  // showing how many hops are hidden. Click opens a popover listing all hops.
  function _finalizePathOverflow(tbody) {
    if (!tbody) return;
    var hosts = tbody.querySelectorAll('.path-hops');
    for (var i = 0; i < hosts.length; i++) {
      var host = hosts[i];
      // Skip if already finalized for this content
      if (host.dataset.overflowChecked === '1') continue;
      var children = Array.prototype.slice.call(host.children);
      // Strip any leftover pill before measuring
      var existingPill = host.querySelector('.path-overflow-pill');
      if (existingPill) existingPill.remove();
      var hostRight = host.getBoundingClientRect().right;
      if (!hostRight) continue;
      var hidden = 0;
      // Walk pairs of chip + arrow; count chips (not arrows) whose right edge
      // is past the host's right edge.
      for (var j = 0; j < children.length; j++) {
        var ch = children[j];
        if (ch.classList.contains('arrow')) continue;
        var r = ch.getBoundingClientRect();
        if (r.left >= hostRight || r.right > hostRight + 0.5) hidden++;
      }
      if (hidden > 0) {
        var pill = document.createElement('span');
        pill.className = 'path-overflow-pill';
        pill.textContent = '+' + hidden;
        pill.title = hidden + ' more hop' + (hidden === 1 ? '' : 's') + ' — click to view';
        pill.setAttribute('role', 'button');
        pill.setAttribute('tabindex', '0');
        pill.setAttribute('aria-label', hidden + ' more hops');
        host.appendChild(pill);
      }
      host.dataset.overflowChecked = '1';
    }
  }

  // #1128 (Bug 1): re-run overflow finalize after hop-resolver async pass has
  // had a chance to mutate chip text. Per-tbody so concurrent renders in
  // different tbodies don't cancel each other (#1131 BLOCKER-2). Uses a
  // MutationObserver bonded to the tbody to detect when hop-resolver finishes
  // mutating .path-hops chip text, then runs finalize once mutations settle
  // for 50ms — replaces the previous 120ms blind timeout, which regressed on
  // slow networks where the resolver took longer than 120ms (#1131 MAJOR-1).
  function _scheduleReFinalizePathOverflow(tbody) {
    if (!tbody) return;
    // If a quiesce timer is already armed for this tbody, leave it; new
    // mutations will keep extending it. If an observer is already wired,
    // we're done — it'll fire again on the next mutation.
    if (tbody._rePathOverflowObserver) return;
    var quiesceTimer = null;
    var stopTimer = null;
    function finalize() {
      if (tbody._rePathOverflowObserver) {
        try { tbody._rePathOverflowObserver.disconnect(); } catch (_e) {}
        tbody._rePathOverflowObserver = null;
      }
      if (stopTimer) { clearTimeout(stopTimer); stopTimer = null; }
      var hosts = tbody.querySelectorAll('.path-hops');
      for (var i = 0; i < hosts.length; i++) hosts[i].dataset.overflowChecked = '';
      _finalizePathOverflow(tbody);
    }
    if (typeof MutationObserver === 'function') {
      var obs = new MutationObserver(function () {
        if (quiesceTimer) clearTimeout(quiesceTimer);
        quiesceTimer = setTimeout(finalize, 50);
      });
      obs.observe(tbody, { subtree: true, childList: true, characterData: true });
      tbody._rePathOverflowObserver = obs;
      // Hard upper bound — if hop-resolver never mutates (e.g. all chips
      // already final), still run finalize once after a short delay so the
      // overflow pill appears.
      stopTimer = setTimeout(finalize, 1000);
    } else {
      // Fallback for environments without MutationObserver.
      setTimeout(finalize, 120);
    }
  }

  // Delegated click for path overflow pills — show popover of full path.
  function _wirePathOverflowPopover() {
    if (window.__pathOverflowWired) return;
    window.__pathOverflowWired = true;
    var existing = null;
    function dismiss() {
      if (existing) { existing.remove(); existing = null; }
      document.removeEventListener('mousedown', onDoc, true);
      document.removeEventListener('keydown', onKey, true);
    }
    function onDoc(ev) {
      if (existing && !existing.contains(ev.target) && !ev.target.classList.contains('path-overflow-pill')) dismiss();
    }
    function onKey(ev) { if (ev.key === 'Escape') dismiss(); }
    document.addEventListener('click', function(ev) {
      var pill = ev.target.closest && ev.target.closest('.path-overflow-pill');
      if (!pill) return;
      ev.stopPropagation();
      var host = pill.closest('.path-hops');
      if (!host) return;
      dismiss();
      var pop = document.createElement('div');
      pop.className = 'path-popover';
      // Clone all children except the pill, preserving rendered chips/arrows.
      var inner = '<div class="path-popover-title">Full path (' + (host.children.length) + ' items)</div><div>';
      var kids = Array.prototype.slice.call(host.children);
      for (var i = 0; i < kids.length; i++) {
        if (kids[i].classList.contains('path-overflow-pill')) continue;
        inner += kids[i].outerHTML;
      }
      inner += '</div>';
      pop.innerHTML = inner;
      document.body.appendChild(pop);
      var r = pill.getBoundingClientRect();
      // #1128 (Bug 2): position below by default, but flip ABOVE when there
      // isn't enough room — keeps the popover anchored to the pill instead of
      // hanging arbitrarily over adjacent rows / off-screen.
      var pr0 = pop.getBoundingClientRect();
      var popH = pr0.height;
      var roomBelow = window.innerHeight - r.bottom;
      var top;
      if (roomBelow < popH + 12 && r.top > popH + 12) {
        top = window.scrollY + r.top - popH - 4;
      } else {
        top = window.scrollY + r.bottom + 4;
      }
      var left = window.scrollX + r.left;
      pop.style.top = top + 'px';
      pop.style.left = left + 'px';
      var pr = pop.getBoundingClientRect();
      if (pr.right > window.innerWidth - 8) {
        pop.style.left = Math.max(8, window.scrollX + window.innerWidth - pr.width - 8) + 'px';
      }
      existing = pop;
      setTimeout(function() {
        document.addEventListener('mousedown', onDoc, true);
        document.addEventListener('keydown', onKey, true);
      }, 0);
    });
  }

  // Attach/detach scroll listener for virtual scrolling
  function attachVScrollListener() {
    const scrollContainer = document.getElementById('pktLeft');
    if (!scrollContainer) return;
    if (_vsScrollHandler) return; // already attached
    let scrollRaf = null;
    _vsScrollHandler = function () {
      if (scrollRaf) return;
      scrollRaf = requestAnimationFrame(function () {
        scrollRaf = null;
        renderVisibleRows();
      });
    };
    scrollContainer.addEventListener('scroll', _vsScrollHandler, { passive: true });
  }

  function detachVScrollListener() {
    if (!_vsScrollHandler) return;
    const scrollContainer = document.getElementById('pktLeft');
    if (scrollContainer) scrollContainer.removeEventListener('scroll', _vsScrollHandler);
    _vsScrollHandler = null;
  }

  /** Sort the packets array by the current sort column. Called before renderTableRows. */
  function sortPacketsArray() {
    if (!_packetSortColumn || !packets.length) return;
    var col = _packetSortColumn;
    var dir = _packetSortDirection === 'asc' ? 1 : -1;

    var accessor;
    switch (col) {
      case 'time': accessor = function(p) { return p.latest || p.timestamp || ''; }; break;
      case 'type': accessor = function(p) { return typeName(p.payload_type); }; break;
      case 'hash': accessor = function(p) { return p.hash || ''; }; break;
      case 'observer': accessor = function(p) { return obsName(p.observer_id); }; break;
      case 'size': accessor = function(p) { return p.packet_size || 0; }; break;
      case 'hb': accessor = function(p) { return p.hash_byte_count != null ? p.hash_byte_count : (p.hash_size || 0); }; break;
      case 'rpt': accessor = function(p) {
        try { var pj = typeof p.path_json === 'string' ? JSON.parse(p.path_json) : p.path_json; return Array.isArray(pj) ? pj.length : 0; } catch(e) { return 0; }
      }; break;
      case 'scope': accessor = function(p) { return p.scope_name || ''; }; break;
      case 'region': accessor = function(p) { return (regionMap && regionMap[p.observer_id]) || ''; }; break;
      case 'path': accessor = function(p) {
        try { var pj = typeof p.path_json === 'string' ? JSON.parse(p.path_json) : p.path_json; return Array.isArray(pj) ? pj.join(',') : ''; } catch(e) { return ''; }
      }; break;
      default: return; // unsortable column
    }

    // Choose comparator based on column type
    var isNumeric = (col === 'size' || col === 'hb' || col === 'rpt');
    var isDate = (col === 'time');

    packets.sort(function(a, b) {
      // Most packets carry no scope (FLOOD and DIRECT cannot), so an ascending
      // sort would bury every scoped row under a wall of dashes. Pin the empties
      // last in BOTH directions, as the nodes table does for default_scope.
      if (col === 'scope') {
        var aHasScope = a.scope_name ? 1 : 0, bHasScope = b.scope_name ? 1 : 0;
        if (aHasScope !== bHasScope) return bHasScope - aHasScope;
      }
      var va = accessor(a), vb = accessor(b);
      var result;
      if (isDate) {
        result = TableSort.comparators.date(va, vb);
      } else if (isNumeric) {
        result = TableSort.comparators.numeric(va, vb);
      } else {
        result = TableSort.comparators.text(va, vb);
      }
      // Stable tiebreaker: sort by timestamp (desc) when primary values are equal
      if (result === 0 && !isDate) {
        result = TableSort.comparators.date(
          a.timestamp || a.first_seen || '',
          b.timestamp || b.first_seen || ''
        ) * -1; // desc (newest first)
      }
      return dir * result;
    });
  }

  // applyObserverFilter decides which already-loaded packets remain visible
  // under the current observer filter. Extracted into its own function
  // (rather than left inline in renderTableRows) specifically so tests can
  // exercise the real production logic instead of a hand-copied
  // reimplementation — see #1748 PR review (kent-beck): a test that only
  // checks a copy of this logic doesn't fail if this function regresses.
  //
  // #1748: In grouped mode, the server already filters transmissions
  // correctly (buildTransmissionWhere emits an EXISTS subquery over ALL
  // observations of the transmission, not just the displayed one — see
  // cmd/server/db.go). Each row's `observer_id` here is only the
  // *representative* observer chosen for display (longest observed path),
  // which may legitimately differ from the observer that satisfied the
  // filter. Re-filtering client-side against that single representative —
  // with `_children` still unpopulated at this point (only fetched lazily
  // on row-expand or observer-sort-change) — hid every multi-observer
  // transmission whose representative happened not to be one of the
  // selected observers. In practice this meant a transmission only stayed
  // visible when the filtered observer was also the one with the longest
  // path (which is why the report described it as "works only for
  // whichever observer logged it first" in dense meshes, where
  // longest-path and earliest-seen correlate). The server-side EXISTS
  // filter is authoritative for grouped rows, so no client-side
  // re-filtering is needed or correct here.
  //
  // Flat/expanded mode (groupByHash === false) has no such
  // representative-vs-actual mismatch — buildPacketWhere filters each
  // observation row by its own exact observer_id — but we keep the
  // defensive re-filter for that path since it costs nothing and guards
  // against any future flat-mode server change.
  function applyObserverFilter(displayPackets, filters, groupByHash, hashOnly) {
    if (hashOnly || !filters.observer) return displayPackets;
    if (groupByHash) return displayPackets;
    const obsIds = new Set(filters.observer.split(','));
    return displayPackets.filter(p => {
      if (obsIds.has(p.observer_id)) return true;
      if (p._children) return p._children.some(c => obsIds.has(String(c.observer_id)));
      return false;
    });
  }

  async function renderTableRows() {
    const tbody = document.getElementById('pktBody');
    if (!tbody) return;

    // Preserve scroll position across re-render (#431)
    const scrollContainer = document.getElementById('pktLeft');
    const savedScrollTop = scrollContainer ? scrollContainer.scrollTop : 0;

    // Update dynamic parts of the header
    const countEl = document.querySelector('#pktLeft .count');
    const groupBtn = document.getElementById('fGroup');
    if (groupBtn) groupBtn.classList.toggle('active', groupByHash);

    // Filter to claimed/favorited nodes — pure client-side filter (no server round-trip)
    let displayPackets = packets;

    // When loading a specific packet by hash, bypass ALL client-side filters
    // (myNodes, type, observer, packet-filter-expression). The user is asking
    // for THAT exact packet — saved type/observer/expression filters must not
    // hide it. Hash filter is the exact identifier; nothing else applies.
    const hashOnly = !!filters.hash;

    if (!hashOnly && filters.myNodes) {
      const myNodes = JSON.parse(localStorage.getItem('meshcore-my-nodes') || '[]');
      const myKeys = myNodes.map(n => n.pubkey).filter(Boolean);
      const favs = getFavorites();
      const allKeys = [...new Set([...myKeys, ...favs])];
      if (allKeys.length > 0) {
        displayPackets = displayPackets.filter(p => {
          const dj = p.decoded_json || '';
          return allKeys.some(k => dj.includes(k));
        });
      } else {
        displayPackets = [];
      }
    }

    // Client-side type/observer filtering
    if (!hashOnly && filters.type) {
      const types = filters.type.split(',').map(Number);
      displayPackets = displayPackets.filter(p => types.includes(p.payload_type));
    }
    displayPackets = applyObserverFilter(displayPackets, filters, groupByHash, hashOnly);

    // Packet Filter Language
    const pfCount = document.getElementById('packetFilterCount');
    if (!hashOnly && filters._packetFilter) {
      const beforeCount = displayPackets.length;
      displayPackets = displayPackets.filter(filters._packetFilter);
      if (pfCount) {
        pfCount.textContent = 'Showing ' + displayPackets.length.toLocaleString() + ' of ' + beforeCount.toLocaleString() + ' packets';
        pfCount.style.display = 'block';
      }
    } else if (pfCount) {
      pfCount.style.display = 'none';
    }

    if (countEl) countEl.textContent = `(${displayPackets.length})`;

    if (!displayPackets.length) {
      _displayPackets = [];
      _rowCounts = [];
      _rowCountsDirty = false;
      _cumulativeOffsetsCache = null;
      _observerFilterSet = null;
      _lastVisibleStart = -1;
      _lastVisibleEnd = -1;
      detachVScrollListener();
      const colCount = _getColCount();
      tbody.innerHTML = '<tr><td colspan="' + colCount + '" class="text-center text-muted" style="padding:24px">' + (filters.myNodes ? 'No packets from your claimed/favorited nodes' : 'No packets found') + '</td></tr>';
      // Restore scroll position after DOM rebuild (#431)
      if (scrollContainer) scrollContainer.scrollTop = savedScrollTop;
      return;
    }

    // Lazy virtual scroll: store display packets and row counts, but do NOT
    // pre-generate HTML strings. HTML is built on-demand in renderVisibleRows()
    // for only the visible slice + buffer (#422).
    _lastVisibleStart = -1;
    _lastVisibleEnd = -1;
    _displayPackets = displayPackets;
    _displayGrouped = groupByHash;
    _observerFilterSet = filters.observer ? new Set(filters.observer.split(',')) : null;
    _rowCounts = displayPackets.map(p => _getRowCount(p));
    _rowCountsDirty = false;
    _cumulativeOffsetsCache = null;

    attachVScrollListener();
    renderVisibleRows();

    // Restore scroll position after re-render (#431)
    if (scrollContainer) scrollContainer.scrollTop = savedScrollTop;
  }

  function getDetailPreview(decoded) {
    if (!decoded) return '';
    // Channel messages (GRP_TXT) — show channel name and message text
    if (decoded.type === 'CHAN' && decoded.text) {
      const ch = decoded.channel ? `<span class="chan-tag">${escapeHtml(decoded.channel)}</span> ` : '';
      const t = decoded.text.length > 80 ? decoded.text.slice(0, 80) + '…' : decoded.text;
      return `${ch}<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chat-circle"/></svg> ${escapeHtml(t)}`;
    }
    // Advertisements — show node name and role
    if (decoded.type === 'ADVERT' && decoded.name) {
      const role = decoded.flags?.repeater ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg>' : decoded.flags?.room ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-house-line"/></svg>' : decoded.flags?.sensor ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-thermometer"/></svg>' : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-radio"/></svg>';
      return `${role} <a href="#/nodes/${encodeURIComponent(decoded.pubKey)}" class="hop-link hop-named" data-hop-link="true">${escapeHtml(decoded.name)}</a>`;
    }
    // Undecrypted channel messages — show channel hash and decryption status
    if (decoded.type === 'GRP_TXT' && decoded.channelHash != null) {
      const hashHex = decoded.channelHashHex || decoded.channelHash.toString(16).padStart(2, '0').toUpperCase();
      const statusLabel = decoded.decryptionStatus === 'no_key' ? 'no key' : 'decryption failed';
      return `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg> Ch 0x${hashHex} <span class="muted">(${statusLabel})</span>`;
    }
    // Group data (binary datagrams over channels) — #1792.
    // Envelope: channel_hash + MAC + ciphertext. When decrypted, inner is
    // data_type(uint16 LE) + data_len(1) + blob (firmware BaseChatMesh.cpp:382-385).
    if (decoded.type === 'GRP_DATA' && decoded.channelHash != null) {
      const hashHex = decoded.channelHashHex || decoded.channelHash.toString(16).padStart(2, '0').toUpperCase();
      // #1796 r1 DRY: all three GRP_DATA branches below share the same
      // database-icon + `Ch 0x${hashHex}` prefix. Extract once; behavior is
      // byte-identical with the prior inline form.
      const prefix = `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-database"/></svg> Ch 0x${hashHex}`;
      // Happy path: decrypted with a parsed inner header (dataType present, no parse error).
      // Per firmware/src/helpers/BaseChatMesh.cpp:387 a data_len of 0 is a LEGITIMATE
      // empty datagram (the firmware only drops data_len > available_len). Backend
      // cmd/ingestor/decoder.go:142 marshals DecryptedBlob with `omitempty`, so an
      // empty blob is normal — render the <code> block ONLY when blob is a non-empty
      // string. The 'data_len exceeds buffer' malformed branch sets decoded.error,
      // which falls through to the explicit malformed label below.
      if (decoded.decryptionStatus === 'decrypted' && decoded.dataType != null && !decoded.error) {
        const dt = Number(decoded.dataType).toString(16).padStart(4, '0').toUpperCase();
        const dl = decoded.dataLen != null ? Number(decoded.dataLen) : 0;
        const blob = decoded.decryptedBlob || '';
        const blobBlock = blob ? ` <code>${escapeHtml(blob.length > 32 ? blob.slice(0, 32) + '…' : blob)}</code>` : '';
        return `${prefix} <span class="muted">type=0x${dt} len=${dl}</span>${blobBlock}`;
      }
      // #1796 polish: decrypted-but-malformed branch. Backend leaves
      // status='decrypted' for two failure modes (decoder.go:619-654):
      //   (a) inner too short → DataType=nil
      //   (b) inner data_len exceeds buffer → DataType set, blob empty, Error set
      // Without this explicit branch we would either lie ('encrypted') or
      // render a misleading "type=0xNN len=N" header with an empty <code></code>.
      if (decoded.decryptionStatus === 'decrypted') {
        return `${prefix} <span class="muted">(decrypted, malformed)</span>`;
      }
      const statusLabel = decoded.decryptionStatus === 'no_key' ? 'no key'
        : decoded.decryptionStatus === 'decryption_failed' ? 'decryption failed'
        : 'encrypted';
      return `${prefix} <span class="muted">(${statusLabel})</span>`;
    }
    // Direct messages
    if (decoded.type === 'TXT_MSG') return `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-envelope"/></svg> ${decoded.srcHash?.slice(0,8) || '?'} → ${decoded.destHash?.slice(0,8) || '?'}`;
    // Path updates
    if (decoded.type === 'PATH') return `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-shuffle"/></svg> ${decoded.srcHash?.slice(0,8) || '?'} → ${decoded.destHash?.slice(0,8) || '?'}`;
    // Requests/responses (encrypted)
    if (decoded.type === 'REQ' || decoded.type === 'RESPONSE') return `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg> ${decoded.srcHash?.slice(0,8) || '?'} → ${decoded.destHash?.slice(0,8) || '?'}`;
    // Anonymous requests (#1864). ANON_REQ carries the sender's FULL 32-byte
    // pubkey (not a 1-byte srcHash) — resolve it to a node name if known,
    // else show the first 8 hex chars. Legacy ephemeralPubKey fallback covers
    // packets decoded before the backend field was renamed to srcPubKey.
    if (decoded.type === 'ANON_REQ') {
      const anonKey = decoded.srcPubKey || decoded.ephemeralPubKey || '';
      const anonName = (anonKey && window.HopResolver && HopResolver.nameForKey) ? HopResolver.nameForKey(anonKey) : null;
      const anonSrc = anonName ? escapeHtml(anonName) : (anonKey ? escapeHtml(anonKey.slice(0, 8)) : 'anon');
      return `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg> ${anonSrc} → ${decoded.destHash?.slice(0,8) || '?'}`;
    }
    // CONTROL packets (#1802) — DISCOVER_REQ / DISCOVER_RESP body fields,
    // decoded by cmd/ingestor/decoder.go decodeControl(). Wire format:
    //   firmware/src/Mesh.cpp:69
    //   firmware/examples/simple_repeater/MyMesh.cpp:773-820
    // Subtype enum on byte0 high nibble; body fields are length-gated and
    // may be absent on truncated packets, so each is rendered only when set.
    if (decoded.type === 'CONTROL') {
      const subtype = decoded.ctrlSubtype || 'CONTROL';
      const icon = `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg>`;
      const parts = [];
      if (subtype === 'DISCOVER_REQ') {
        if (decoded.ctrlFilter != null) {
          parts.push(`filter=0x${Number(decoded.ctrlFilter).toString(16).padStart(2, '0')}`);
        }
        if (decoded.ctrlTag != null) {
          parts.push(`tag=0x${(Number(decoded.ctrlTag) >>> 0).toString(16).padStart(8, '0')}`);
        }
        if (decoded.ctrlSince != null) {
          parts.push(`since=${Number(decoded.ctrlSince) >>> 0}`);
        }
      } else if (subtype === 'DISCOVER_RESP') {
        if (decoded.ctrlNodeType != null) {
          parts.push(`type=${Number(decoded.ctrlNodeType)}`);
        }
        if (decoded.ctrlSNR != null) {
          parts.push(`snr=${Number(decoded.ctrlSNR)}`);
        }
        if (decoded.ctrlTag != null) {
          parts.push(`tag=0x${(Number(decoded.ctrlTag) >>> 0).toString(16).padStart(8, '0')}`);
        }
        if (decoded.ctrlPubKey) {
          parts.push(`pubkey=${escapeHtml(decoded.ctrlPubKey)}`);
        }
      } else if (decoded.ctrlFlags) {
        parts.push(`flags=0x${escapeHtml(decoded.ctrlFlags)}`);
      }
      const tail = parts.length ? ` <span class="muted">${parts.join(' ')}</span>` : '';
      return `${icon} ${escapeHtml(subtype)}${tail}`;
    }
    // Companion bridge text
    if (decoded.text) return escapeHtml(decoded.text.length > 80 ? decoded.text.slice(0, 80) + '…' : decoded.text);
    // Bare adverts with just pubkey
    if (decoded.public_key) return `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> ${decoded.public_key.slice(0, 16)}…`;
    return '';
  }

  let selectedObservationId = null;

  async function selectPacket(id, hash, prefetchedData, obsRowId) {
    selectedId = id;
    selectedObservationId = obsRowId || null;
    const obsParam = selectedObservationId ? `?obs=${selectedObservationId}` : '';
    if (hash) {
      history.replaceState(null, '', `#/packets/${hash}${obsParam}`);
    } else {
      history.replaceState(null, '', `#/packets/${id}${obsParam}`);
    }
    renderTableRows();
    const isMobileNow = window.innerWidth <= 640;
    // #1168 review note: this branch is intentionally narrower than nodes.js /
    // observers.js. On packets, ≤640 falls through to the legacy mobile bottom
    // sheet (`isMobileNow` short-circuits before SlideOver), and SlideOver is
    // used only for the 641–1023 range. nodes.js and observers.js route into
    // SlideOver across the full ≤1023 range. Both satisfy AC#4 ("not a
    // separate page"); the per-page split is deliberate — the packets table
    // has heavier per-row affordances (hex breakdown, observations grid)
    // that the bottom sheet handles better at very narrow widths than a
    // side-anchored slide-over. Do NOT "fix" the inconsistency without
    // discussing with the issue author.
    const useSlideOver = !isMobileNow && window.SlideOver && window.SlideOver.shouldUse();
    let panel;
    if (useSlideOver) {
      // #1056 AC#4: narrow viewports (641–1023) — open detail in slide-over
      // overlay rather than the side panel.
      panel = window.SlideOver.open({
        title: hash ? ('Packet ' + String(hash).slice(0, 12)) : 'Packet detail',
        // After close, the rows are re-rendered (see onClose). Use a resolver
        // to look up the originating row in the post-render DOM by data-hash
        // / data-id, so keyboard focus restores to the actual table row.
        restoreFocus: function () {
          const lookup = hash || id;
          if (!lookup) return null;
          const esc = (window.CSS && CSS.escape) ? CSS.escape(String(lookup)) : String(lookup);
          return document.querySelector('#pktTable tbody tr[data-hash="' + esc + '"]')
              || document.querySelector('#pktTable tbody tr[data-id="' + esc + '"]');
        },
        onClose: function () {
          selectedId = null;
          selectedObservationId = null;
          history.replaceState(null, '', '#/packets');
          renderTableRows();
        }
      });
      panel.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading…</div>';
    } else if (isMobileNow) {
      // Use mobile bottom sheet
      let sheet = document.getElementById('mobileDetailSheet');
      if (!sheet) {
        sheet = document.createElement('div');
        sheet.id = 'mobileDetailSheet';
        sheet.className = 'mobile-detail-sheet';
        sheet.innerHTML = '<div class="mobile-sheet-handle"></div><button class="mobile-sheet-close" id="mobileSheetClose" aria-label="Close"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></button><div class="mobile-sheet-content"></div>';
        document.body.appendChild(sheet);
        sheet.querySelector('#mobileSheetClose').addEventListener('click', () => {
          sheet.classList.remove('open');
        });
        sheet.querySelector('.mobile-sheet-handle').addEventListener('click', () => {
          sheet.classList.remove('open');
        });
      }
      panel = sheet.querySelector('.mobile-sheet-content');
      panel.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading…</div>';
      sheet.classList.add('open');
    } else {
      panel = document.getElementById('pktRight');
      panel.classList.remove('empty');
      var layout = panel.closest('.split-layout');
      if (layout) layout.classList.remove('detail-collapsed');
      panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div>' + PANEL_CLOSE_HTML + '<div class="text-center text-muted" style="padding:40px">Loading…</div>';
      initPanelResize();
    }

    try {
      const data = prefetchedData || await api(hash ? `/packets/${hash}` : `/packets/${id}`);
      // Resolve path hops for detail view
      const pkt = data.packet;
      try {
        const hops = getParsedPath(pkt);
        const newHops = hops.filter(h => !(h in hopNameCache));
        if (newHops.length) await resolveHops(newHops);
      } catch {}
      panel.innerHTML = isMobileNow ? '' : (useSlideOver ? '' : ('<div class="panel-resize-handle" id="pktResizeHandle"></div>' + PANEL_CLOSE_HTML));
      const content = document.createElement('div');
      panel.appendChild(content);
      await renderDetail(content, data, selectedObservationId);
      if (!isMobileNow && !useSlideOver) initPanelResize();
    } catch (e) {
      panel.innerHTML = `<div class="text-muted">Error: ${e.message}</div>`;
    }
  }

  async function renderDetail(panel, data, chosenObsId) {
    const pkt = data.packet;
    const observations = data.observations || [];

    // Per-observation rendering (issue #849):
    // When opened from a packet row (no specific observer), default to first observation.
    // When opened from an observation child row, use that observation.
    // Clicking a different observation row in the detail re-renders with that observation.
    let currentObs = null;
    const targetObsId = chosenObsId || selectedObservationId;
    if (targetObsId && observations.length) {
      currentObs = observations.find(o => String(o.id) === String(targetObsId));
    }
    if (!currentObs && observations.length) {
      currentObs = observations[0]; // fall back to first observation
    }

    // If we have a current observation, build pkt fields from it so summary is per-observation
    const effectivePkt = currentObs ? clearParsedCache({...pkt, ...currentObs, _isObservation: true}) : pkt;
    const decoded = getParsedDecoded(effectivePkt) || {};
    const pathHops = getParsedPath(effectivePkt) || [];

    // Compute breakdown ranges from the actually-rendered raw_hex (per-observation).
    // Single source of truth — derived from the same bytes we display, so a
    // post-#882 per-obs raw_hex with a different path length than the top-level
    // packet's raw_hex still gets accurate byte highlights.
    const obsRawHexForRanges = effectivePkt.raw_hex || pkt.raw_hex || '';
    const ranges = obsRawHexForRanges
      ? computeBreakdownRanges(obsRawHexForRanges, pkt.route_type, pkt.payload_type)
      : [];

    // Cross-check: hop count from raw_hex path_len byte vs path_json length
    const obsRawHex = effectivePkt.raw_hex || pkt.raw_hex || '';
    let rawHopCount = null;
    if (obsRawHex.length >= 4) {
      // path_len byte position depends on route type
      const plOff = getPathLenOffset(pkt.route_type);
      const plByte = parseInt(obsRawHex.slice(plOff * 2, plOff * 2 + 2), 16);
      if (!isNaN(plByte)) rawHopCount = plByte & 0x3F;
    }
    if (rawHopCount != null && pathHops.length !== rawHopCount) {
      console.warn(`[CoreScope] Hop count inconsistency for packet ${pkt.hash}: path_json has ${pathHops.length} hops but raw_hex path_len has ${rawHopCount}. UI shows path_json.`);
    }

    // Resolve sender GPS — from packet directly, or from known node in DB
    let senderLat = decoded.lat != null ? decoded.lat : (decoded.latitude || null);
    let senderLon = decoded.lon != null ? decoded.lon : (decoded.longitude || null);
    if (senderLat == null) {
      // Try to find sender node GPS from DB
      const senderKey = decoded.pubKey || decoded.srcPubKey;
      const senderName = decoded.sender || decoded.name;
      try {
        if (senderKey) {
          const nd = await api(`/nodes/${senderKey}`, { ttl: 30000 }).catch(() => null);
          if (nd?.node?.lat && nd.node.lon) { senderLat = nd.node.lat; senderLon = nd.node.lon; }
        }
        if (senderLat == null && senderName) {
          const sd = await api(`/nodes/search?q=${encodeURIComponent(senderName)}`, { ttl: 30000 }).catch(() => null);
          const match = sd?.nodes?.[0];
          if (match?.lat && match.lon) { senderLat = match.lat; senderLon = match.lon; }
        }
      } catch {}
    }

    // Resolve hops: prefer server-side resolved_path, fall back to client-side HopResolver
    if (pathHops.length) {
      try {
        const serverResolved = getResolvedPath(pkt);
        let resolved;
        if (serverResolved && serverResolved.length === pathHops.length) {
          await ensureHopResolver();
          resolved = HopResolver.resolveFromServer(pathHops, serverResolved);
        } else {
          await ensureHopResolver();
          resolved = HopResolver.resolve(pathHops);
        }
        if (resolved) {
          for (const [k, v] of Object.entries(resolved)) {
            hopNameCache[k] = v;
            if (pkt.observer_id) hopNameCache[k + ':' + pkt.observer_id] = v;
          }
        }
      } catch {}
    }

    // Parse hash size from path byte
    const plOff = getPathLenOffset(pkt.route_type);
    const rawPathByte = pkt.raw_hex ? parseInt(pkt.raw_hex.slice(plOff * 2, plOff * 2 + 2), 16) : NaN;
    const hashSize = (isNaN(rawPathByte) || (rawPathByte & 0x3F) === 0) ? null : ((rawPathByte >> 6) + 1);

    const size = effectivePkt.raw_hex ? Math.floor(effectivePkt.raw_hex.length / 2) : (pkt.raw_hex ? Math.floor(pkt.raw_hex.length / 2) : 0);
    const typeName = payloadTypeName(pkt.payload_type);

    const snr = effectivePkt.snr ?? decoded.SNR ?? decoded.snr ?? null;
    const rssi = effectivePkt.rssi ?? decoded.RSSI ?? decoded.rssi ?? null;
    const hasRawHex = !!(effectivePkt.raw_hex || pkt.raw_hex);

    // Build message preview
    let messageHtml = '';
    if (decoded.text) {
      const chLabel = decoded.channel || (decoded.channel_idx != null ? `Ch ${decoded.channel_idx}` : null) || (decoded.channelHash != null ? `Ch 0x${decoded.channelHash.toString(16)}` : '');
      const hopLabel = decoded.path_len != null ? `${decoded.path_len} hops` : '';
      const snrLabel = snr != null ? `SNR ${snr} dB` : '';
      const meta = [chLabel, hopLabel, snrLabel].filter(Boolean).join(' · ');
      messageHtml = `<div class="detail-message" style="padding:12px;margin:8px 0;background:var(--card-bg);border-radius:8px;border-left:3px solid var(--accent)">
        <div style="font-size:1.1em">${escapeHtml(decoded.text)}</div>
        ${meta ? `<div style="font-size:0.85em;color:var(--text-muted);margin-top:4px">${meta}</div>` : ''}
      </div>`;
    } else if (decoded.type === 'GRP_TXT' && decoded.channelHash != null) {
      const hashHex = decoded.channelHashHex || decoded.channelHash.toString(16).padStart(2, '0').toUpperCase();
      const statusLabel = decoded.decryptionStatus === 'no_key' ? 'no key' : 'decryption failed';
      messageHtml = `<div class="detail-message" style="padding:12px;margin:8px 0;background:var(--card-bg);border-radius:8px;border-left:3px solid var(--warning, #f0ad4e)">
        <div style="font-size:1.1em"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg> Channel Hash: 0x${hashHex} <span style="color:var(--text-muted)">(${statusLabel})</span></div>
      </div>`;
    }

    const obsCount = data.observation_count || observations.length || 1;
    const uniqueObservers = new Set(observations.map(o => o.observer_id)).size;

    // Propagation time: spread between first and last observation
    let propagationHtml = '—';
    if (observations.length >= 2) {
      const times = observations.map(o => new Date(o.timestamp).getTime()).filter(t => !isNaN(t));
      if (times.length >= 2) {
        const first = Math.min(...times);
        const last = Math.max(...times);
        const spread = last - first;
        if (spread < 1000) {
          propagationHtml = `${spread}ms`;
        } else if (spread < 60000) {
          propagationHtml = `${(spread / 1000).toFixed(1)}s`;
        } else {
          propagationHtml = `${(spread / 60000).toFixed(1)}m`;
        }
        propagationHtml += ` <span style="color:var(--text-muted);font-size:0.85em">(${obsCount} obs × ${uniqueObservers} observers)</span>`;
      }
    }

    // Location: from ADVERT lat/lon, or from known node via pubkey/sender name.
    // Issue #1281: only render the row when we actually have transmitter GPS.
    // Non-ADVERT packets don't carry GPS in the unencrypted payload, so the row
    // would otherwise render as "—" and waste a slot on ~90% of packet types.
    let locationHtml = '';
    let locationNodeKey = null;
    if (decoded.lat != null && decoded.lon != null && !(decoded.lat === 0 && decoded.lon === 0)) {
      locationNodeKey = decoded.pubKey || decoded.srcPubKey || '';
      const nodeName = decoded.name || '';
      locationHtml = `${decoded.lat.toFixed(5)}, ${decoded.lon.toFixed(5)}`;
      if (nodeName) locationHtml = `${escapeHtml(nodeName)} — ${locationHtml}`;
      if (locationNodeKey) locationHtml += ` <a href="#/map?node=${encodeURIComponent(locationNodeKey)}" class="loc-map-link"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-pin"/></svg>map</a>`;
    } else {
      // Try to resolve sender node location from nodes list
      const senderKey = decoded.pubKey || decoded.srcPubKey;
      const senderName = decoded.sender || decoded.name;
      if (senderKey || senderName) {
        try {
          const nodeData = senderKey ? await api(`/nodes/${senderKey}`, { ttl: 30000 }).catch(() => null) : null;
          if (nodeData && nodeData.node && nodeData.node.lat && nodeData.node.lon) {
            locationNodeKey = nodeData.node.public_key;
            locationHtml = `${nodeData.node.lat.toFixed(5)}, ${nodeData.node.lon.toFixed(5)}`;
            if (nodeData.node.name) locationHtml = `${escapeHtml(nodeData.node.name)} — ${locationHtml}`;
            locationHtml += ` <a href="#/map?node=${encodeURIComponent(locationNodeKey)}" class="loc-map-link"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-pin"/></svg>map</a>`;
          } else if (senderName && !senderKey) {
            // Search by name
            const searchData = await api(`/nodes/search?q=${encodeURIComponent(senderName)}`, { ttl: 30000 }).catch(() => null);
            const match = searchData && searchData.nodes && searchData.nodes[0];
            if (match && match.lat && match.lon) {
              locationNodeKey = match.public_key;
              locationHtml = `${match.lat.toFixed(5)}, ${match.lon.toFixed(5)}`;
              locationHtml = `${escapeHtml(match.name)} — ${locationHtml}`;
              locationHtml += ` <a href="#/map?node=${encodeURIComponent(locationNodeKey)}" class="loc-map-link"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-pin"/></svg>map</a>`;
            }
          }
        } catch {}
      }
    }

    const anomalyBanner = decoded.anomaly
      ? `<div class="anomaly-banner" style="background:var(--warning, #f0ad4e); color:#000; padding:8px 12px; border-radius:4px; margin-bottom:8px; font-weight:600;"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Anomaly: ${escapeHtml(decoded.anomaly)}</div>`
      : '';

    // Hop count display: use pathHops length (= effective observation's path_json).
    // The raw_hex/path_json mismatch warning is logged above for diagnostics; the UI
    // must stay self-consistent — top pill names and byte breakdown rows must agree.
    const displayHopCount = pathHops.length;
    const obsIndicator = currentObs && observations.length > 1
      ? `<span style="font-size:0.8em;color:var(--text-muted);margin-left:6px">(observation ${observations.indexOf(currentObs) + 1} of ${observations.length})</span>`
      : '';

    // #1279 P2 #3 — Transport codes detail row (firmware/src/Packet.h:46,
    // parsed at cmd/server/decoder.go:492-498). Present on TRANSPORT_FLOOD/
    // TRANSPORT_DIRECT routes only.
    var tcCode1 = '—', tcCode2 = '—', tcShow = false;
    if (decoded.transportCodes) {
      tcShow = true;
      if (decoded.transportCodes.code1) tcCode1 = String(decoded.transportCodes.code1).toUpperCase();
      if (decoded.transportCodes.code2) tcCode2 = String(decoded.transportCodes.code2).toUpperCase();
    }
    var transportCodesRow = tcShow
      ? `<dt>Transport Codes</dt><dd class="transport-codes">Code1: <code>${escapeHtml(tcCode1)}</code> · Code2: <code>${escapeHtml(tcCode2)}</code></dd>`
      : '';

    // #1279 P2 #5 — RAW_CUSTOM detail row (firmware/src/Mesh.cpp:577).
    var rawCustomRow = '';
    if (pkt.payload_type === 15 && decoded.type === 'RAW_CUSTOM') {
      var rl = decoded.rawLength != null ? decoded.rawLength + ' byte' + (decoded.rawLength === 1 ? '' : 's') : '—';
      var ft = decoded.firstByteTag ? String(decoded.firstByteTag).toUpperCase() : '—';
      rawCustomRow = `<dt>Raw Custom</dt><dd class="raw-custom-detail">Length: <code>${escapeHtml(rl)}</code> · First byte tag: <code>${escapeHtml(ft)}</code></dd>`;
    }

    // #1458 P0-A — semantic identity header (type badge + decoded summary +
    // src→dst). Replaces the prior byte-count title that buried packet
    // identity behind a byte counter (#1458 P0-A).
    const semanticSummary = getDetailPreview(decoded);
    // #1864: ANON_REQ has no srcHash — its sender is the full srcPubKey.
    const _anonKey = decoded.srcPubKey || decoded.ephemeralPubKey || '';
    const _anonName = (_anonKey && window.HopResolver && HopResolver.nameForKey) ? HopResolver.nameForKey(_anonKey) : null;
    const srcLabel = decoded.sender || decoded.name || (decoded.srcHash ? decoded.srcHash.slice(0,8) : null) || _anonName || (_anonKey ? _anonKey.slice(0,8) + '…' : null) || (decoded.pubKey ? decoded.pubKey.slice(0,8) + '…' : null);
    const dstLabel = decoded.recipient || (decoded.destHash ? decoded.destHash.slice(0,8) : null);
    const srcDstHtml = (srcLabel || dstLabel)
      ? `<div class="detail-srcdst">${escapeHtml(srcLabel || '?')} <span class="arrow">→</span> ${escapeHtml(dstLabel || (decoded.channel ? '#' + decoded.channel : '?'))}</div>`
      : '';

    panel.innerHTML = `
      ${anomalyBanner}
      <div class="detail-title">
        <span class="badge badge-${payloadTypeColor(pkt.payload_type)}">${typeName}</span>
        ${semanticSummary ? `<span class="detail-summary">${semanticSummary}</span>` : ''}
        ${displayHopCount > 0 ? `<span class="badge badge-info">${displayHopCount} hop${displayHopCount !== 1 ? 's' : ''}</span>` : ''}
      </div>
      ${srcDstHtml}
      <div class="detail-hash">${pkt.hash || 'Packet #' + pkt.id}${obsIndicator}</div>
      ${messageHtml}
      <dl class="detail-meta">
        <dt>Payload Type</dt><dd><span class="badge badge-${payloadTypeColor(pkt.payload_type)}">${typeName}</span></dd>
        <dt>Path</dt><dd>${displayHopCount > 0 ? `<span class="badge badge-info">${displayHopCount} hop${displayHopCount !== 1 ? 's' : ''}</span> ` + renderPath(pathHops, effectivePkt.observer_id) : '— (direct)'}</dd>
        <dt>Timestamp</dt><dd>${renderTimestampCell(effectivePkt.timestamp)}</dd>
        <dt>Observer</dt><dd>${escapeHtml(obsNameOnly(effectivePkt.observer_id))}${obsIataBadge(effectivePkt)}</dd>
        ${locationHtml ? `<dt>Location</dt><dd>${locationHtml}</dd>` : ''}
        <dt>SNR / RSSI</dt><dd>${snr != null ? snr + ' dB' : '—'} / ${rssi != null ? rssi + ' dBm' : '—'}</dd>
        <dt>Route Type</dt><dd>${routeTypeName(pkt.route_type)}</dd>
        ${pkt.scope_name != null ? `<dt>Scope</dt><dd>${pkt.scope_name !== '' ? escapeHtml(pkt.scope_name) : '<span style="color:var(--text-muted)">unknown scope</span>'}</dd>` : ''}
        ${hashSize ? `<dt>Hash Size</dt><dd>${hashSize} byte${hashSize !== 1 ? 's' : ''}</dd>` : ''}
        <dt>Propagation</dt><dd>${propagationHtml}</dd>
        ${transportCodesRow}
        ${rawCustomRow}
        ${effectivePkt.direction ? `<dt>Direction</dt><dd>${escapeHtml(effectivePkt.direction)}</dd>` : ''}
      </dl>
      <div class="detail-actions">
        <button class="copy-link-btn" data-packet-hash="${pkt.hash || ''}" data-packet-id="${pkt.id}" title="Copy link to this packet"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-link"/></svg> Copy Link</button>
        ${pathHops.length ? `<button class="detail-map-link" id="viewRouteBtn"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-trifold"/></svg> View route on map</button>` : ''}
        ${pkt.hash ? `<a href="#/traces/${pkt.hash}" class="detail-map-link" style="text-decoration:none"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg> Trace</a>` : ''}
        <button class="replay-live-btn" title="Replay this packet on the live map"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-play"/></svg> Replay</button>
      </div>

      ${(hasRawHex || Object.keys(decoded).length) ? `<details class="detail-technical"${(typeof window !== 'undefined' && window.innerWidth > 480) ? ' open' : ''}>
        <summary>Show raw bytes</summary>
        ${hasRawHex ? `<div class="hex-legend">${buildHexLegend(ranges)}</div>
        <div class="hex-dump">${createColoredHexDump(effectivePkt.raw_hex || pkt.raw_hex, ranges)}</div>` : ''}

        ${hasRawHex ? buildFieldTable(effectivePkt.raw_hex ? effectivePkt : pkt, decoded, pathHops, ranges) : buildDecodedTable(decoded)}
      </details>` : ''}

      ${observations.length > 1 ? `
      <div class="detail-observations" style="margin-top:16px">
        <div style="font-weight:600;margin-bottom:6px">Observations (${observations.length})</div>
        <table class="detail-obs-table" style="width:100%;border-collapse:collapse;font-size:0.9em">
          <thead><tr style="border-bottom:1px solid var(--border)">
            <th style="padding:4px 6px;text-align:left">Observer</th>
            <th style="padding:4px 6px;text-align:left">Hops</th>
            <th style="padding:4px 6px;text-align:left">SNR</th>
            <th style="padding:4px 6px;text-align:left">RSSI</th>
            <th style="padding:4px 6px;text-align:left">Time</th>
          </tr></thead>
          <tbody>${observations.map(o => {
            const oPath = getParsedPath(o);
            const isCurrent = currentObs && String(o.id) === String(currentObs.id);
            return `<tr class="detail-obs-row${isCurrent ? ' observation-current' : ''}" data-obs-id="${o.id}" style="cursor:pointer;${isCurrent ? 'background:var(--accent-bg, rgba(0,122,255,0.1))' : ''}" title="Click to view this observation">
              <td style="padding:4px 6px">${escapeHtml(obsNameOnly(o.observer_id))}${obsIataBadge(o)}</td>
              <td style="padding:4px 6px">${oPath.length}</td>
              <td style="padding:4px 6px">${o.snr != null ? o.snr + ' dB' : '—'}</td>
              <td style="padding:4px 6px">${o.rssi != null ? o.rssi + ' dBm' : '—'}</td>
              <td style="padding:4px 6px">${renderTimestampCell(o.timestamp)}</td>
            </tr>`;
          }).join('')}</tbody>
        </table>
      </div>` : ''}

      ${observations.length > 1 ? (() => {
        // Cross-observer aggregate (Option B): show longest observed path across all observers
        const aggregatePath = getParsedPath(pkt) || [];
        return `<div class="detail-aggregate" style="margin-top:12px;padding:10px;background:var(--card-bg);border-radius:6px;border:1px solid var(--border);font-size:0.9em">
          <div style="font-weight:600;margin-bottom:4px;color:var(--text-muted)">Cross-observer aggregate</div>
          <div>Longest observed path: ${aggregatePath.length ? `${aggregatePath.length} hops — ${renderPath(aggregatePath, pkt.observer_id)}` : '— (direct)'}</div>
          <div style="font-size:0.8em;color:var(--text-muted);margin-top:2px">Longest path seen across all ${uniqueObservers} observer${uniqueObservers !== 1 ? 's' : ''}</div>
        </div>`;
      })() : ''}
    `;

    // Wire up observation row click handlers — re-render detail with clicked observation
    panel.querySelectorAll('.detail-obs-row').forEach(row => {
      row.addEventListener('click', () => {
        const obsId = row.dataset.obsId;
        selectedObservationId = obsId;
        // Update URL hash to reflect selected observation (deep linking)
        const pktHash = pkt.hash || pkt.id;
        const obsParam = obsId ? `?obs=${obsId}` : '';
        history.replaceState(null, '', `#/packets/${pktHash}${obsParam}`);
        renderDetail(panel, data, obsId);
      });
    });

    // Wire up copy link button
    const copyLinkBtn = panel.querySelector('.copy-link-btn');
    if (copyLinkBtn) {
      copyLinkBtn.addEventListener('click', () => {
        const pktHash = copyLinkBtn.dataset.packetHash;
        const obsParam = selectedObservationId ? `?obs=${selectedObservationId}` : '';
        const url = pktHash ? `${location.origin}/#/packets/${pktHash}${obsParam}` : `${location.origin}/#/packets/${copyLinkBtn.dataset.packetId}${obsParam}`;
        window.copyToClipboard(url, () => {
          copyLinkBtn.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> Copied!';
          setTimeout(() => { copyLinkBtn.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-link"/></svg> Copy Link'; }, 1500);
        });
      });
    }

    // Wire up replay button
    const replayBtn = panel.querySelector('.replay-live-btn');
    if (replayBtn) {
      replayBtn.addEventListener('click', () => {
        // Build replay packets for ALL observations of this transmission
        const obs = data.observations || [];
        const replayPackets = [];
        if (obs.length > 1) {
          for (const o of obs) {
            const oPath = getParsedPath(o);
            const oDec = getParsedDecoded(o);
            replayPackets.push({
              id: o.id, hash: pkt.hash, raw: o.raw_hex || pkt.raw_hex,
              _ts: new Date(o.timestamp).getTime(),
              decoded: { header: { payloadTypeName: typeName }, payload: oDec, path: { hops: oPath } },
              snr: o.snr, rssi: o.rssi, observer: obsName(o.observer_id),
              // #1900: carry the id itself, not just the resolved name. The Live
              // region filter matches on observer_id, so without it the replay
              // silently renders nothing whenever a region is selected.
              observer_id: o.observer_id, observer_iata: o.observer_iata
            });
          }
        } else {
          replayPackets.push({
            id: pkt.id, hash: pkt.hash, raw: pkt.raw_hex,
            _ts: new Date(pkt.timestamp).getTime(),
            decoded: { header: { payloadTypeName: typeName }, payload: decoded, path: { hops: pathHops } },
            snr: pkt.snr, rssi: pkt.rssi, observer: obsName(pkt.observer_id),
            observer_id: pkt.observer_id, observer_iata: pkt.observer_iata
          });
        }
        sessionStorage.setItem('replay-packet', JSON.stringify(replayPackets));
        window.location.hash = '#/live';
      });
    }

    // Wire up view route on map button
    const routeBtn = panel.querySelector('#viewRouteBtn');
    if (routeBtn && pathHops.length) {
      routeBtn.addEventListener('click', async () => {
        try {
          // Prefer server-side resolved_path if available
          const serverResolved = getResolvedPath(pkt);
          let resolvedKeys;
          if (serverResolved && serverResolved.length === pathHops.length) {
            // Use server-resolved pubkeys, fall back to short prefix for null entries
            resolvedKeys = pathHops.map((h, i) => serverResolved[i] || h);
          } else {
            // Fall back to client-side HopResolver
            const senderLat = decoded.lat || decoded.latitude;
            const senderLon = decoded.lon || decoded.longitude;
            let obsLat = null, obsLon = null;
            const obsId = obsName(pkt.observer_id);
            await ensureHopResolver();
            const data = { resolved: HopResolver.resolve(pathHops, senderLat || null, senderLon || null, obsLat, obsLon, pkt.observer_id) };
            resolvedKeys = pathHops.map(h => {
              const r = data.resolved?.[h];
              return r?.pubkey || h;
            });
          }
          // Build origin info for the sender node
          const origin = {};
          if (decoded.pubKey) origin.pubkey = decoded.pubKey;
          else if (decoded.srcHash) origin.pubkey = decoded.srcHash;
          if (decoded.adName || decoded.name) origin.name = decoded.adName || decoded.name;
          if (senderLat != null && senderLon != null) { origin.lat = senderLat; origin.lon = senderLon; }
          // #1418 Phase D: also include the recipient (destHash) so the route
          // displays as: sender → [intermediate hops] → recipient. Without
          // this the destination node is invisible — operator only sees the
          // last intermediate repeater.
          const destination = {};
          if (decoded.destHash) destination.pubkey = decoded.destHash;
          // #1418 Phase C: include ALL observations as alternate paths so the
          // route view can render union-of-edges with stroke-width weighting.
          // Each observation contributes its own path_json array.
          const allPaths = (observations || []).map(o => {
            let path = [];
            try { path = JSON.parse(o.path_json || '[]'); } catch (_) {}
            return { path: path, observer: o.observer_name, observer_id: o.observer_id, snr: o.snr, rssi: o.rssi };
          }).filter(p => p.path && p.path.length > 0);
          // #1418/#1419: navigate via deep-link URL only. The map page's
          // loadRouteFromDeepLink() re-fetches the packet from the API and
          // builds the full payload (incl. packetContext) consistently.
          // SessionStorage was unreliable — the deep-link path includes
          // packetContext but the sessionStorage payload didn't, leading
          // to missing chip + facts when entered from the packets page.
          const obsId = currentObs ? currentObs.id : (observations[0] && observations[0].id);
          const pkHash = pkt.hash || pkt.packet_hash;
          const obsPart = obsId ? '&obs=' + encodeURIComponent(obsId) : '';
          // Tufte audit fix: close ALL mobile packet panels so operator lands
          // on the route view, not behind a still-visible detail sheet.
          // Three different panels exist depending on viewport + flow:
          //   - #pktRight (desktop split-pane)
          //   - .slide-over-panel (mid-width SlideOver)
          //   - #mobileDetailSheet (small-mobile bottom sheet)
          if (window.innerWidth <= 767) {
            try { closeDetailPanel(); } catch (_) {}
            try { if (window.SlideOver && window.SlideOver.close) window.SlideOver.close(); } catch (_) {}
            try {
              const sheet = document.getElementById('mobileDetailSheet');
              if (sheet) sheet.classList.remove('open');
            } catch (_) {}
          }
          window.location.hash = '#/map?packet=' + encodeURIComponent(pkHash) + obsPart;
        } catch {
          window.location.hash = '#/map';
        }
      });
    }
  }

  function buildDecodedTable(decoded) {
    let rows = '';
    for (const [k, v] of Object.entries(decoded)) {
      if (v === null || v === undefined) continue;
      rows += `<tr><td style="font-weight:600;padding:4px 8px">${escapeHtml(k)}</td><td style="padding:4px 8px">${escapeHtml(String(v))}</td></tr>`;
    }
    return rows ? `<table class="detail-decoded" style="width:100%;border-collapse:collapse;margin-top:8px">${rows}</table>` : '';
  }

  function buildFieldTable(pkt, decoded, pathHops, ranges) {
    const buf = pkt.raw_hex || '';
    const size = Math.floor(buf.length / 2);
    let rows = '';

    // Header section
    rows += sectionRow('Header', 'section-header');
    rows += fieldRow(0, 'Header Byte', '0x' + (buf.slice(0, 2) || '??'), `Route: ${routeTypeName(pkt.route_type)}, Payload: ${payloadTypeName(pkt.payload_type)}`);

    // Transport codes come BEFORE path length for transport routes (bytes 1-4)
    let off = 1;
    if (isTransportRoute(pkt.route_type)) {
      rows += sectionRow('Transport Codes', 'section-transport');
      rows += fieldRow(off, 'Next Hop', buf.slice(off * 2, (off + 2) * 2), '');
      rows += fieldRow(off + 2, 'Last Hop', buf.slice((off + 2) * 2, (off + 4) * 2), '');
      off += 4;
    }

    // Path length byte is at current offset (byte 1 for non-transport, byte 5 for transport)
    const pathLenOffset = off;
    const pathByte0 = parseInt(buf.slice(off * 2, off * 2 + 2), 16);
    const hashSizeVal = isNaN(pathByte0) ? '?' : ((pathByte0 >> 6) + 1);
    const hashCountVal = isNaN(pathByte0) ? '?' : (pathByte0 & 0x3F);
    rows += fieldRow(off, 'Path Length', '0x' + (buf.slice(off * 2, off * 2 + 2) || '??'), hashCountVal === 0 ? `hash_count=0 (direct advert)` : `hash_size=${hashSizeVal} byte${hashSizeVal !== 1 ? 's' : ''}, hash_count=${hashCountVal}`);
    off += 1;

    // Path — render hops from path_json (what this observation reported).
    // Byte offsets advance by hashSize * pathHops.length to match.
    const hashSize = isNaN(pathByte0) ? 1 : ((pathByte0 >> 6) + 1);
    if (pathHops.length > 0) {
      rows += sectionRow('Path (' + pathHops.length + ' hops)', 'section-path');
      for (let i = 0; i < pathHops.length; i++) {
        const hopOff = off + i * hashSize;
        const hex = String(pathHops[i] || '').toUpperCase();
        const hopHtml = HopDisplay.renderHop(hex, hopNameCache[hex]);
        const label = `Hop ${i} — ${hopHtml}`;
        rows += fieldRow(hopOff, label, hex, '');
      }
      off += hashSize * pathHops.length;
    }

    // TRACE SNR values (from header path bytes, decoded by backend)
    if (decoded.type === 'TRACE' && decoded.snrValues && decoded.snrValues.length > 0) {
      rows += sectionRow('SNR Path (' + decoded.snrValues.length + ' hops completed)', 'section-path');
      for (let i = 0; i < decoded.snrValues.length; i++) {
        const snr = decoded.snrValues[i];
        const snrStr = (snr >= 0 ? '+' : '') + snr.toFixed(2) + ' dB';
        rows += fieldRow('', 'SNR (hop ' + i + ')', snrStr, '');
      }
    }

    // Payload
    rows += sectionRow('Payload — ' + payloadTypeName(pkt.payload_type), 'section-payload');

    if (decoded.type === 'ADVERT') {
      if (hashCountVal !== 0) rows += fieldRow(pathLenOffset, 'Advertised Hash Size', hashSizeVal + ' byte' + (hashSizeVal !== 1 ? 's' : ''), 'From path byte 0x' + (buf.slice(pathLenOffset * 2, pathLenOffset * 2 + 2) || '??') + ' — bits 7-6 = ' + (hashSizeVal - 1));
      rows += fieldRow(off, 'Public Key (32B)', truncate(decoded.pubKey || '', 24), '');
      rows += fieldRow(off + 32, 'Timestamp (4B)', decoded.timestampISO || '', 'Unix: ' + (decoded.timestamp || ''));
      rows += fieldRow(off + 36, 'Signature (64B)', truncate(decoded.signature || '', 24), '');
      if (decoded.flags) {
        const _typeLabels = {1:'Companion',2:'Repeater',3:'Room Server',4:'Sensor'};
        const _typeName = _typeLabels[decoded.flags.type] || ('Unknown(' + decoded.flags.type + ')');
        const _boolFlags = [decoded.flags.hasLocation && 'location', decoded.flags.hasName && 'name'].filter(Boolean);
        const _flagDesc = _typeName + (_boolFlags.length ? ' + ' + _boolFlags.join(', ') : '');
        rows += fieldRow(off + 100, 'App Flags', '0x' + (decoded.flags.raw?.toString(16).padStart(2,'0') || '??'), _flagDesc);
        let fOff = off + 101;
        if (decoded.flags.hasLocation) {
          rows += fieldRow(fOff, 'Latitude', decoded.lat?.toFixed(6) || '', '');
          rows += fieldRow(fOff + 4, 'Longitude', decoded.lon?.toFixed(6) || '', '');
          fOff += 8;
        }
        if (decoded.flags.hasName) {
          rows += fieldRow(fOff, 'Node Name', decoded.pubKey ? `<a href="#/nodes/${encodeURIComponent(decoded.pubKey)}" class="hop-link hop-named" data-hop-link="true">${escapeHtml(decoded.name || '')}</a>` : escapeHtml(decoded.name || ''), '');
        }
      }
    } else if (decoded.type === 'GRP_TXT') {
      const hashHex = decoded.channelHashHex || (decoded.channelHash != null ? decoded.channelHash.toString(16).padStart(2, '0').toUpperCase() : '??');
      const statusLabel = decoded.decryptionStatus === 'no_key' ? '(no key)' : decoded.decryptionStatus === 'decryption_failed' ? '(decryption failed)' : '';
      rows += fieldRow(off, 'Channel Hash', `0x${hashHex} ${statusLabel}`, '');
      rows += fieldRow(off + 1, 'MAC (2B)', decoded.mac || '', '');
      rows += fieldRow(off + 3, 'Encrypted Data', truncate(decoded.encryptedData || '', 30), '');
    } else if (decoded.type === 'CHAN') {
      rows += fieldRow(off, 'Channel', escapeHtml(decoded.channel || `0x${(decoded.channelHash || 0).toString(16)}`), '');
      rows += fieldRow(off + 1, 'Sender', escapeHtml(decoded.sender || '—'), '');
      if (decoded.sender_timestamp) rows += fieldRow(off + 2, 'Sender Time', escapeHtml(String(decoded.sender_timestamp)), '');
    } else if (decoded.type === 'ACK') {
      rows += fieldRow(off, 'Checksum (4B)', decoded.ackChecksum || '', '');
    } else if (decoded.type === 'TRACE') {
      rows += fieldRow(off, 'Trace Tag (4B)', decoded.tag ? '0x' + decoded.tag.toString(16).toUpperCase().padStart(8, '0') : '—', '');
      rows += fieldRow(off + 4, 'Auth Code (4B)', decoded.authCode ? '0x' + decoded.authCode.toString(16).toUpperCase().padStart(8, '0') : '—', '');
      rows += fieldRow(off + 8, 'Flags', decoded.traceFlags != null ? '0x' + decoded.traceFlags.toString(16).padStart(2, '0') : '—', decoded.traceFlags != null ? 'hash_size=' + (1 << (decoded.traceFlags & 0x03)) + ' byte(s)' : '');
      if (decoded.pathData) {
        rows += fieldRow(off + 9, 'Route Hops', decoded.pathData.toUpperCase(), pathHops.length + ' hop(s)');
      }
    } else if (decoded.type === 'ANON_REQ') {
      // #1864: ANON_REQ layout differs from REQ — the source is a FULL 32-byte
      // pubkey, not a 1-byte srcHash, so MAC/data sit at off+33/off+35 (not
      // off+2/off+4). Decode explicitly and resolve the key to a node link.
      const anonKey = decoded.srcPubKey || decoded.ephemeralPubKey || '';
      const anonName = (anonKey && window.HopResolver && HopResolver.nameForKey) ? HopResolver.nameForKey(anonKey) : null;
      rows += fieldRow(off, 'Dest Hash (1B)', decoded.destHash || '', '');
      const anonKeyCell = anonKey
        ? `<a href="#/nodes/${encodeURIComponent(anonKey)}" class="hop-link ${anonName ? 'hop-named' : ''}" data-hop-link="true">${anonName ? escapeHtml(anonName) : truncate(anonKey, 24)}</a>`
        : '—';
      rows += fieldRow(off + 1, 'Src Public Key (32B)', anonKeyCell, anonName ? '' : 'sender pubkey (unresolved)');
      rows += fieldRow(off + 33, 'MAC (2B)', decoded.mac || '', '');
      rows += fieldRow(off + 35, 'Encrypted Data', truncate(decoded.encryptedData || '', 30), '');
    } else if (decoded.destHash !== undefined) {
      rows += fieldRow(off, 'Dest Hash (1B)', decoded.destHash || '', '');
      rows += fieldRow(off + 1, 'Src Hash (1B)', decoded.srcHash || '', '');
      rows += fieldRow(off + 2, 'MAC (2B)', decoded.mac || '', '');
      rows += fieldRow(off + 4, 'Encrypted Data', truncate(decoded.encryptedData || '', 30), '');
    } else {
      rows += fieldRow(off, 'Raw', truncate(buf.slice(off * 2), 40), '');
    }

    if (decoded.anomaly) {
      rows += `<tr class="anomaly-row" style="background:var(--warning, #f0ad4e); color:#000; font-weight:600;"><td colspan="2"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Anomaly</td><td colspan="2">${escapeHtml(decoded.anomaly)}</td></tr>`;
    }

    return `<table class="field-table">
      <thead><tr><th scope="col">Offset</th><th scope="col">Field</th><th scope="col">Value</th><th scope="col">Description</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>`;
  }

  function sectionRow(label, cls) {
    return `<tr class="section-row${cls ? ' ' + cls : ''}"><td colspan="4">${label}</td></tr>`;
  }
  function fieldRow(offset, name, value, desc) {
    return `<tr><td class="mono">${offset}</td><td>${name}</td><td class="mono">${value}</td><td class="text-muted">${desc || ''}</td></tr>`;
  }

  // BYOP modal — decode only, no DB injection
  function showBYOP() {
    removeAllByopOverlays();
    const triggerBtn = document.querySelector('[data-action="pkt-byop"]');
    const overlay = document.createElement('div');
    overlay.className = 'modal-overlay byop-overlay';
    overlay.innerHTML = '<div class="modal byop-modal" role="dialog" aria-label="Decode a Packet" aria-modal="true">'
      + '<div class="byop-header"><h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-package"/></svg> Decode a Packet</h3><button class="btn-icon byop-x" title="Close" aria-label="Close dialog"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></button></div>'
      + '<p class="text-muted" style="margin:0 0 12px;font-size:.85rem">Paste raw hex bytes from your radio or MQTT feed:</p>'
      + '<textarea id="byopHex" class="byop-input" aria-label="Packet hex data" placeholder="e.g. 15C31A8D4674FEAE37..." spellcheck="false"></textarea>'
      + '<button class="btn-primary byop-go" id="byopDecode" style="width:100%;margin:8px 0">Decode</button>'
      + '<div id="byopResult" role="status" aria-live="polite"></div>'
      + '</div>';
    document.body.appendChild(overlay);

    const modal = overlay.querySelector('.byop-modal');
    const close = () => { removeAllByopOverlays(); if (triggerBtn) triggerBtn.focus(); };
    overlay.querySelector('.byop-x').onclick = close;
    overlay.addEventListener('click', (e) => { if (e.target === overlay) close(); });

    // Focus trap
    function getFocusable() {
      return modal.querySelectorAll('textarea, button, input, [tabindex]:not([tabindex="-1"])');
    }
    overlay.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { e.preventDefault(); close(); return; }
      if (e.key === 'Tab') {
        const focusable = getFocusable();
        if (!focusable.length) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (e.shiftKey) {
          if (document.activeElement === first) { e.preventDefault(); last.focus(); }
        } else {
          if (document.activeElement === last) { e.preventDefault(); first.focus(); }
        }
      }
    });

    const textarea = overlay.querySelector('#byopHex');
    textarea.focus();
    textarea.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        doDecode();
      }
    });

    overlay.querySelector('#byopDecode').onclick = doDecode;

    async function doDecode() {
      const hex = textarea.value.trim().replace(/[\s\n]/g, '');
      const result = overlay.querySelector('#byopResult');
      if (!hex) { result.innerHTML = '<p class="text-muted">Enter hex data</p>'; return; }
      if (!/^[0-9a-fA-F]+$/.test(hex)) { result.innerHTML = '<p class="byop-err" role="alert">Invalid hex — only 0-9 and A-F allowed</p>'; return; }
      result.innerHTML = '<p class="text-muted">Decoding...</p>';
      try {
        const res = await fetch('/api/decode', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ hex })
        });
        const data = await res.json();
        if (data.error) throw new Error(data.error);
        result.innerHTML = renderDecodedPacket(data.decoded, hex);
      } catch (e) {
        result.innerHTML = '<p class="byop-err" role="alert"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x-circle"/></svg> ' + e.message + '</p>';
      }
    }
  }

  function renderDecodedPacket(d, hex) {
    const h = d.header || {};
    const p = d.payload || {};
    const path = d.path || {};
    const size = hex ? Math.floor(hex.length / 2) : 0;

    let html = '<div class="byop-decoded">';

    // Anomaly banner
    if (d.anomaly) {
      html += '<div class="anomaly-banner" style="background:var(--warning, #f0ad4e); color:#000; padding:8px 12px; border-radius:4px; margin-bottom:8px; font-weight:600;"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Anomaly: ' + escapeHtml(d.anomaly) + '</div>';
    }

    // Header section
    html += '<div class="byop-section">'
      + '<div class="byop-section-title">Header</div>'
      + '<div class="byop-kv">'
      + kv('Route Type', routeTypeName(h.routeType))
      + kv('Payload Type', payloadTypeName(h.payloadType))
      + kv('Version', h.payloadVersion)
      + kv('Size', size + ' bytes')
      + '</div></div>';

    // Path section
    if (path.hops && path.hops.length) {
      html += '<div class="byop-section">'
        + '<div class="byop-section-title">Path (' + path.hops.length + ' hops)</div>'
        + '<div class="byop-path">' + path.hops.map(function(hop) { return '<span class="hop">' + hop + '</span>'; }).join('<span class="arrow">→</span>') + '</div>'
        + '</div>';
    }

    // Payload section
    html += '<div class="byop-section">'
      + '<div class="byop-section-title">Payload — ' + payloadTypeName(h.payloadType) + '</div>'
      + '<div class="byop-kv">';
    for (const [k, v] of Object.entries(p)) {
      if (v === null || v === undefined) continue;
      if (typeof v === 'object') {
        html += kv(k, '<pre class="byop-pre">' + JSON.stringify(v, null, 2) + '</pre>');
      } else {
        html += kv(k, String(v));
      }
    }
    // Special handling for advert signature validation
    if (h.payloadType === 4 && p.signatureValid !== undefined) {
      const status = p.signatureValid ? 'Valid' : 'Invalid';
      const badgeClass = p.signatureValid ? 'badge-success' : 'badge-danger';
      html += kv('Signature', `<span class="badge ${badgeClass}">${status}</span>`);
    }
    html += '</div></div>';

    // Raw hex
    html += '<div class="byop-section">'
      + '<div class="byop-section-title">Raw Hex</div>'
      + '<div class="byop-hex mono">' + hex.toUpperCase().match(/.{1,2}/g).join(' ') + '</div>'
      + '</div>';

    html += '</div>';
    return html;
  }

  function kv(key, val) {
    return '<div class="byop-row"><span class="byop-key">' + key + '</span><span class="byop-val">' + val + '</span></div>';
  }

  // Load regions from config API
  (async () => {
    try {
      regionMap = await api('/config/regions', { ttl: 3600 });
    } catch {}
  })();

  // Observation sort modes
  const SORT_OBSERVER = 'observer';
  const SORT_PATH_ASC = 'path-asc';
  const SORT_PATH_DESC = 'path-desc';
  const SORT_CHRONO_ASC = 'chrono-asc';
  const SORT_CHRONO_DESC = 'chrono-desc';
  let obsSortMode = localStorage.getItem('meshcore-obs-sort') || SORT_OBSERVER;

  function getPathHopCount(c) {
    try { return getParsedPath(c).length; } catch { return 0; }
  }

  function sortGroupChildren(group) {
    if (!group || !group._children || !group._children.length) return;
    const mode = obsSortMode;

    if (mode === SORT_CHRONO_ASC || mode === SORT_CHRONO_DESC) {
      const dir = mode === SORT_CHRONO_ASC ? 1 : -1;
      group._children.sort((a, b) => {
        const tA = a.timestamp || '', tB = b.timestamp || '';
        return tA < tB ? -dir : tA > tB ? dir : 0;
      });
    } else if (mode === SORT_PATH_ASC || mode === SORT_PATH_DESC) {
      const dir = mode === SORT_PATH_ASC ? 1 : -1;
      group._children.sort((a, b) => {
        const lenA = getPathHopCount(a), lenB = getPathHopCount(b);
        if (lenA !== lenB) return (lenA - lenB) * dir;
        const oA = (a.observer_name || '').toLowerCase(), oB = (b.observer_name || '').toLowerCase();
        return oA < oB ? -1 : oA > oB ? 1 : 0;
      });
    } else {
      // Default: group by observer, earliest-observer first, then ascending time within each
      const earliest = {};
      for (const c of group._children) {
        const obs = c.observer_name || c.observer || '';
        const t = c.timestamp || c.rx_at || c.created_at || '';
        if (!earliest[obs] || t < earliest[obs]) earliest[obs] = t;
      }
      group._children.sort((a, b) => {
        const oA = a.observer_name || a.observer || '', oB = b.observer_name || b.observer || '';
        const eA = earliest[oA] || '', eB = earliest[oB] || '';
        if (eA !== eB) return eA < eB ? -1 : 1;
        if (oA !== oB) return oA < oB ? -1 : 1;
        const tA = a.timestamp || a.rx_at || '', tB = b.timestamp || b.rx_at || '';
        return tA < tB ? -1 : tA > tB ? 1 : 0;
      });
    }

    // Update header row to match first sorted child
    const first = group._children[0];
    if (first) {
      group.observer_id = first.observer_id;
      group.observer_name = first.observer_name;
      group.snr = first.snr;
      group.rssi = first.rssi;
      group.path_json = first.path_json;
      group.direction = first.direction;
    }
  }

  // Global handlers
  async function pktToggleGroup(hash) {
    if (expandedHashes.has(hash)) {
      expandedHashes.delete(hash);
      renderTableRows();
      return;
    }
    // Single fetch — gets packet + observations + path
    try {
      const data = await api(`/packets/${hash}`);
      const pkt = data.packet;
      if (!pkt) return;
      const group = hashIndex.get(hash);
      if (group && data.observations) {
        group._children = data.observations.map(o => clearParsedCache({...pkt, ...o, _isObservation: true}));
        group._fetchedData = data;
        // Sort children based on current sort mode
        sortGroupChildren(group);
      }
      // Resolve hops from children: prefer server-side resolved_path
      await cacheResolvedPaths(group?._children || []);
      const childHops = new Set();
      for (const c of (group?._children || [])) {
        try { getParsedPath(c).forEach(h => childHops.add(h)); } catch {}
      }
      const newHops = [...childHops].filter(h => !(h in hopNameCache));
      if (newHops.length) await resolveHops(newHops);
      expandedHashes.add(hash);
      renderTableRows();
      // Also open detail panel — no extra fetch needed
      selectPacket(pkt.id, hash, data);
    } catch {}
  }
  async function pktSelectHash(hash) {
    // When grouped, select packet — reuse cached detail endpoint
    try {
      const data = await api(`/packets/${hash}`);
      if (data?.packet) selectPacket(data.packet.id, hash, data);
    } catch {}
  }

  let _lastColorByHash = _isColorByHash();
  function _onStorageChange() {
    var current = _isColorByHash();
    if (_lastColorByHash !== current) {
      _lastColorByHash = current;
      renderVisibleRows();
    }
  }

  let _themeRefreshHandler = null;

  registerPage('packets', {
    init: function(app, routeParam) {
      _themeRefreshHandler = () => { if (typeof renderTableRows === 'function') renderTableRows(); };
      window.addEventListener('theme-refresh', _themeRefreshHandler);
      window.addEventListener('storage', _onStorageChange);
      var result = init(app, routeParam);
      // Install channel color picker on packets table (M2, #271)
      if (window.ChannelColorPicker) window.ChannelColorPicker.installPacketsTable();
      return result;
    },
    destroy: function() {
      if (_themeRefreshHandler) { window.removeEventListener('theme-refresh', _themeRefreshHandler); _themeRefreshHandler = null; }
      window.removeEventListener('storage', _onStorageChange);
      return destroy();
    }
  });

  // Standalone packet detail page: #/packet/123 or #/packet/HASH
  // Expose pure functions for unit testing (vm.createContext pattern)
  if (typeof window !== 'undefined') {
    document.addEventListener('channel-colors-changed', function() { renderVisibleRows(); });
    window._packetsTestAPI = {
      typeName,
      obsName,
      getDetailPreview,
      sortGroupChildren,
      getPathHopCount,
      renderDecodedPacket,
      kv,
      buildFieldTable,
      sectionRow,
      fieldRow,
      renderTimestampCell,
      renderPath,
      _getRowCount,
      _cumulativeRowOffsets,
      _invalidateRowCounts,
      _refreshRowCountsIfDirty,
      buildGroupRowHtml,
      buildFlatRowHtml,
      _calcVisibleRange,
      buildPacketsParams,
      applyObserverFilter,
      renderTableRows,
      _setPackets: function(p) { packets = p; },
      _setFilter: function(k, v) { filters[k] = v; },
    };
  }

  registerPage('packet-detail', {
    init: async (app, routeParam) => {
      const param = routeParam;
      app.innerHTML = `<div style="max-width:800px;margin:0 auto;padding:20px"><div class="text-center text-muted" style="padding:40px">Loading packet…</div></div>`;
      try {
        await loadObservers();
        const data = await api(`/packets/${param}`);
        if (!data?.packet) { app.innerHTML = `<div style="max-width:800px;margin:0 auto;padding:40px;text-align:center"><h2>Packet not found</h2><p>Packet ${param} doesn't exist.</p><a href="#/packets">← Back to packets</a></div>`; return; }
        const hops = [];
        try { hops.push(...getParsedPath(data.packet)); } catch {}
        const newHops = hops.filter(h => !(h in hopNameCache));
        if (newHops.length) await resolveHops(newHops);
        const container = document.createElement('div');
        container.style.cssText = 'max-width:800px;margin:0 auto;padding:20px';
        container.innerHTML = `<div style="margin-bottom:16px"><a href="#/packets" style="color:var(--link-color);text-decoration:none">← Back to packets</a></div>`;
        const detail = document.createElement('div');
        container.appendChild(detail);
        await renderDetail(detail, data);
        app.innerHTML = '';
        app.appendChild(container);
      } catch (e) {
        app.innerHTML = `<div style="max-width:800px;margin:0 auto;padding:40px;text-align:center"><h2>Error</h2><p>${e.message}</p><a href="#/packets">← Back to packets</a></div>`;
      }
    },
    destroy: () => {}
  });
})();
