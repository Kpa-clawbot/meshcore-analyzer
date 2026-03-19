# UI/UX Review: Analytics, Channels & Observers Pages

Reviewer: subagent | Date: 2026-03-19

---

## Analytics Page

### Accessibility

1. **[Major]** Tab buttons lack `role="tablist"` / `role="tab"` / `aria-selected` — screen readers can't identify the tab pattern. (`analytics.js` ~L60-68, the `.analytics-tabs` div and `.tab-btn` buttons)

2. **[Major]** All SVG charts (bar charts, scatter plots, histograms, sparklines) have zero text alternatives — no `role="img"`, no `aria-label`, no `<title>` element. Screen readers get nothing. (`analytics.js` — `barChart()` L27, `sparkSvg()` L14, `renderScatter()` L142, `histogram()` L42)

3. **[Major]** Hash matrix cells use color alone (green/yellow/red) to convey collision status. Color-blind users can't distinguish them. No pattern/icon/text differentiation. (`analytics.js` ~L339-350)

4. **[Minor]** `clickable-row` elements use `onclick` inline handlers on `<tr>` — not keyboard-focusable, no `tabindex`, no `role="link"` or `role="button"`. (`analytics.js` L293, L318, L328 — multiple tables)

5. **[Minor]** Observer selector buttons in Topology tab reuse `.tab-btn` class but lack proper ARIA tab semantics. (`analytics.js` ~L220)

6. **[Minor]** Scatter plot quality zone labels ("Excellent", "Good", "Weak") use semi-transparent fills that may have insufficient contrast against various backgrounds. (`analytics.js` ~L166-170)

### Mobile Responsive

7. **[Major]** `.analytics-row` goes `flex-direction: column` on mobile (good), but the hash matrix table (`renderHashMatrix`) generates a fixed-width 16×16 grid with `cellSize=36px` → minimum ~600px wide. The `overflow-x:auto` wrapper helps but the detail panel beside it won't fit. (`analytics.js` ~L331, `style.css` — no specific mobile override for hash matrix)

8. **[Minor]** SVG charts use fixed `max-height` values (e.g., `max-height:300px`, `max-height:160px`) which may waste space or clip on very small screens. Width is `100%` though, which is correct. (`analytics.js` ~L143, L189, L207)

9. **[Minor]** `.subpath-layout` uses `height: calc(100vh - 160px)` — this assumes a specific header height. If the analytics tabs wrap to 2 lines on mobile, content gets clipped. (`style.css` — `.subpath-layout`)

10. **[Minor]** Route Patterns subpath detail panel has `min-width: 360px` — won't fit on phones <375px even in column layout. (`style.css` — `.subpath-detail`)

### Desktop Space Efficiency

11. **[Minor]** `.analytics-page` has `max-width: 1600px` — reasonable for most content but the hash matrix + detail panel side-by-side could use more width on ultrawide monitors. (`style.css` — `.analytics-page`)

12. **[Minor]** Overview stat cards use `minmax(160px, 1fr)` grid — on very wide screens you get many small cards in one row which looks sparse. Could benefit from a `max-width` per card. (`style.css` — `.stats-grid`)

### Bugs / Inconsistencies

13. **[Critical]** `svgLine()` function (L7-12) is defined but **never called anywhere**. Dead code. (`analytics.js` L7)

14. **[Major]** `window._analyticsData` is set as a global — potential for conflicts with other scripts, and the `destroy()` function only does `delete window._analyticsData` but doesn't clean up event listeners on `#analyticsTabs`. (`analytics.js` L87, L460)

15. **[Major]** `renderCollisions()` and `renderHashMatrix()` both independently fetch `/nodes?limit=2000` — duplicate API call when viewing the "Hash Collisions" tab. (`analytics.js` ~L329, L380)

16. **[Minor]** `renderSubpaths` uses `async function` but is called without `await` in `renderTab()` switch — the loading state and error handling work via the function's internal try/catch, but the `requestAnimationFrame` column resize in `renderTab` will fire before the async content renders. (`analytics.js` L96 calls renderSubpaths, L99-103 does column resize immediately)

17. **[Minor]** The `renderTab` function applies `makeColumnsResizable` to `.analytics-table` elements, but `makeColumnsResizable` is called without checking if it exists (it's presumably defined in `app.js`). No guard. (`analytics.js` L100)

18. **[Minor]** `timeAgo()` and `api()` are used but not imported/defined in this file — relies on global scope from `app.js`. Not a bug per se but fragile coupling. (`analytics.js` multiple locations)

19. **[Minor]** Hash matrix legend uses inline styles for color swatches rather than CSS classes — inconsistent with the rest of the codebase which uses `.legend-dot` class. (`analytics.js` ~L365)

---

## Channels Page

### Accessibility

20. **[Major]** Channel list items are `<button>` elements (good!) but message bubbles with sender links use `data-node` + base64-encoded names with click handlers via event delegation. These `<span>` elements with `data-node` are not focusable via keyboard — no `tabindex`, no `role="button"`. (`channels.js` ~L131 `highlightMentions()`, ~L229 message rendering)

21. **[Major]** The node detail panel slides in but doesn't trap focus — keyboard users can tab behind it. Close button exists but no focus management on open/close. (`channels.js` ~L60-80, `showNodeDetail()`)

22. **[Minor]** `aria-live="polite"` on scroll button is good, but the button text "↓ New messages" is static — it doesn't actually announce when new messages arrive, only when visibility toggles. (`channels.js` ~L152)

23. **[Minor]** Channel sidebar has `role="navigation"` and `aria-label="Channel list"` — semantically it's more of a listbox than navigation. (`channels.js` ~L141)

24. **[Minor]** Node tooltip (`.ch-node-tooltip`) has `pointer-events: none` — keyboard users can never interact with its content. (`style.css` — `.ch-node-tooltip`)

### Mobile Responsive

25. **[Minor]** Mobile channel layout uses absolute positioning with `transform: translateX(100%)` for the slide animation — this works but the sidebar gets `pointer-events: none` when main is shown, meaning you can't scroll it even if partially visible. Minor since back button exists. (`style.css` ~L478-484)

26. **[Minor]** Node detail panel is `max-width: 80%` and `width: 320px` — on small phones this leaves only 20% visible of the messages behind it, but the panel covers the content anyway. Adequate. (`style.css` — `.ch-node-panel`)

27. **[Minor]** `.ch-avatar` is 36×36px on desktop, bumped to 40×40 on mobile — meets 44px touch target when including the padding around messages, but the avatar itself is slightly under the 44px WCAG recommendation. (`style.css` — `.ch-avatar`, mobile override)

### Desktop Space Efficiency

28. **[Minor]** Channel sidebar is fixed at 280px (`min-width: 280px`) — not resizable. On wide monitors this is fine, but on 900-1024px tablets it shrinks to 220px which may truncate channel names. (`style.css` — `.ch-sidebar`, tablet media query)

29. **[Minor]** Messages area has no `max-width` — on ultrawide monitors, message bubbles stretch very wide. Chat apps typically cap message width at ~700-800px. (`style.css` — `.ch-messages` has no max-width, `.ch-msg-bubble` has `max-width: 100%`)

### Bugs / Inconsistencies

30. **[Major]** `window._chShowNode`, `_chCloseNode`, `_chHoverNode`, `_chUnhoverNode`, `_chBack`, `_chSelect` are all set as globals and **never cleaned up** in `destroy()`. If the page is navigated away and back, these persist. Also `_chSelect` is defined but only used via `data-hash` click delegation, making it dead code. (`channels.js` ~L98-103, L269)

31. **[Minor]** `getSenderColor()` checks `data-theme` attribute and `prefers-color-scheme` at call time — this means if the user toggles dark mode without reloading, already-rendered messages keep old colors while new ones get correct colors. Not reactively updated. (`channels.js` ~L116-120)

32. **[Minor]** `lookupNode()` caches results in `nodeCache` but cache is never invalidated. If node data changes (name, role), stale data persists until page reload. (`channels.js` ~L12-21)

33. **[Minor]** `refreshMessages()` compares `messages.length` AND last timestamp to detect changes — but at the 200-message limit, both could be the same even if older messages rotated out. Edge case. (`channels.js` ~L210-213)

---

## Observers Page

### Accessibility

34. **[Major]** Health status dots use color alone (green/yellow/red) — color-blind users can't distinguish. The text label "Online"/"Stale"/"Offline" is next to the dot in the table which helps, but the summary dots at the top have no text inside the dot itself. (`observers.js` ~L76-79, `style.css` — `.health-dot`)

35. **[Minor]** Refresh button uses `onclick="window._obsRefresh()"` inline handler — should be a proper event listener. Also uses emoji 🔄 as the only label with just a `title` attribute — screen readers may not convey the title. (`observers.js` ~L14)

36. **[Minor]** `.obs-table` has no `aria-label` or `<caption>` element. (`observers.js` ~L82)

37. **[Minor]** `.spark-bar` progress indicators have no ARIA — they're purely visual. Screen readers get the text "X/hr" from `.spark-label` which is acceptable, but `role="meter"` or similar would be better. (`observers.js` ~L41-44)

### Mobile Responsive

38. **[Minor]** `.observers-page` has `max-width: 1200px` and `padding: 20px` — on mobile this is fine. However, the table has 7 columns and no responsive override — it will require horizontal scrolling on phones. No `overflow-x: auto` wrapper. (`style.css` — `.observers-page`, `observers.js` ~L82)

39. **[Minor]** `.spark-bar` has fixed `width: 100px` — doesn't shrink on small screens, contributing to table overflow. (`style.css` — `.spark-bar`)

### Desktop Space Efficiency

40. **[Minor]** `max-width: 1200px` with `margin: 0 auto` is appropriate. No issues on desktop.

### Bugs / Inconsistencies

41. **[Minor]** `window._obsRefresh` is set globally and never cleaned up in `destroy()`. (`observers.js` L89)

42. **[Minor]** Every WebSocket packet triggers `loadObservers()` — if packets arrive rapidly (e.g., 10/sec), this fires 10 API calls per second. Should be debounced. (`observers.js` ~L20-22)

43. **[Minor]** `healthStatus()` computes time difference using `Date.now()` vs parsed date — doesn't account for timezone differences between server and client. Could show wrong status if clocks are skewed. (`observers.js` ~L32-37)

---

## Cross-Cutting CSS Issues

44. **[Major]** `@media (prefers-color-scheme: dark)` only applies when no `data-theme` attribute is set on `:root` (via `:root:not([data-theme="light"])`). But the dark mode toggle presumably sets `data-theme="dark"`. The auto-detection path (no attribute) and manual path (attribute set) duplicate all the same variables — if one is updated, the other may be forgotten. (`style.css` L18-31 vs L33-47)

45. **[Minor]** `.clickable-row:hover` uses `var(--hover-bg, rgba(0,0,0,.04))` — `--hover-bg` is never defined in `:root`. It falls back correctly, but the fallback `rgba(0,0,0,.04)` is nearly invisible on dark backgrounds. (`style.css` — `.clickable-row:hover`)

46. **[Minor]** `prefers-reduced-motion` media query correctly disables animations — good accessibility practice. (`style.css` ~L527)
