# UI/UX Review: Home Page, Map Page, Nodes Page

## Home Page (`home.js`, `home.css`)

### Accessibility

1. **Minor — Checklist accordion not keyboard accessible** (`home.js` ~L83-85)
   - `.checklist-q` elements are `<div>` with click handlers, not `<button>`. No `role="button"`, no `tabindex`, no `aria-expanded`. Keyboard users cannot open/close checklist items.

2. **Minor — Search suggestions not ARIA-linked** (`home.js` ~L97-130)
   - `#homeSuggest` dropdown has no `role="listbox"`, suggest items have no `role="option"`. The input has no `aria-owns`, `aria-activedescendant`, or `aria-expanded`. Screen readers won't announce suggestions.

3. **Minor — Missing ARIA on My Node cards** (`home.js` ~L168-210)
   - Node cards are clickable `<div>`s without `role="button"` or `tabindex`. Not keyboard-focusable.

4. **Minor — `.mnc-remove` button lacks visible label** (`home.js` ~L175)
   - Uses "✕" text only. Has `title` but no `aria-label`. Screen readers will read "times" or nothing useful.

5. **Minor — Timeline items not keyboard accessible** (`home.js` ~L283)
   - Clickable `.timeline-item` divs with no `tabindex` or `role`.

### Mobile Responsive

6. **Minor — Suggest dropdown touch targets slightly small** (`home.css` ~L68)
   - `.suggest-item` padding is `10px 14px` — adequate but `.suggest-claim` button at `4px 10px` is below 44px minimum touch target.

7. **Minor — My Nodes grid `minmax(380px, 1fr)` may overflow on small screens** (`home.css` ~L142)
   - On screens narrower than 380px (e.g. iPhone SE at 375px), grid items will overflow. The `@media (max-width: 640px)` override to `1fr` fixes this, but there's a gap between 375-640px where 380px min could cause horizontal scroll if only one column fits but the min forces wider than viewport minus padding.

### Desktop Space Efficiency

8. **Minor — Content capped at `max-width: 720px`** (`home.css` various)
   - All content (stats, health, checklist, footer) maxes at 720px. On wide monitors this leaves >50% of screen empty. My Nodes grid is 900px max — slightly better but still narrow for 1440p+ displays.

9. **Minor — Stats cards don't scale up** (`home.css` ~L53)
   - `flex: 1 1 120px` is fine but on wide screens the 720px cap means only 4 small cards. Could use the extra space.

### Bugs / Inconsistencies

10. **Major — `handleOutsideClick` listener not properly cleaned up** (`home.js` ~L136, ~L141)
    - `document.addEventListener('click', handleOutsideClick)` is added in `setupSearch()` and removed in `destroy()`. However if `renderHome()` is called multiple times (e.g. toggling experience level), `setupSearch()` is called again without removing the old listener, stacking duplicate listeners.

11. **Minor — `escapeHtml` used inconsistently in timeline** (`home.js` ~L263)
    - `obsId` passed through `escapeHtml` but `payloadTypeName()` return values are not — likely safe but inconsistent.

12. **Minor — Sparkline class name collision** (`home.js` ~L191, `home.css` ~L163 vs `style.css` ~L417)
    - `.spark-bar` and `.spark-label` are defined in both `home.css` and `style.css` with different meanings (home sparkline vs observers page spark bar). Could cause style conflicts.

13. **Minor — Error state in `loadHealth` uses undefined CSS variable** (`home.js` ~L293)
    - `color:var(--status-red)` is defined in `home.css` but if home.css fails to load, this falls back to nothing.

---

## Map Page (`map.js`)

### Accessibility

14. **Major — Map is entirely inaccessible to keyboard/screen reader users** (`map.js` entire)
    - The Leaflet map has no text alternative, no summary of nodes, no way to navigate nodes without a mouse. This is inherent to map UIs but there's no fallback table or list view.

15. **Minor — Checkboxes in map controls lack associated labels for some** (`map.js` ~L29-35)
    - `<label><input type="checkbox" id="mcClusters"> Show clusters</label>` — the label wraps the input which is fine for association, but there's no explicit `for` attribute. Acceptable but not ideal.

16. **Minor — Popup HTML is not semantically structured** (`map.js` ~L166-180)
    - Popup content uses inline styles and `<table>` for layout without proper `<th>` headers or `scope` attributes.

### Mobile Responsive

17. **Major — Map controls overlay covers most of the map on mobile** (`style.css` ~L498)
    - On mobile: `width: calc(100vw - 24px)` and `max-height: 200px` — the controls panel takes nearly full width and 200px height, which on a small phone (667px height minus 52px nav) leaves only ~415px for the map, with the controls overlaying a large portion. There's no way to collapse/dismiss the controls panel.

18. **Minor — No collapse/toggle for map controls** (`map.js` ~L22-45)
    - The controls panel is always visible. On mobile this is particularly problematic. A toggle button would help.

### Desktop Space Efficiency

19. **Minor — Map controls panel fixed at 220px wide** (`style.css` ~L187)
    - Adequate but could be collapsible to give more map space when not needed.

### Bugs / Inconsistencies

20. **Major — `savedView` referenced but never declared in scope** (`map.js` ~L93)
    - `if (!savedView) fitBounds();` — `savedView` is declared inside the `init()` function at line ~L54, but `loadNodes()` is called at line ~L82 and uses `savedView` at L93. Since `loadNodes` is `async` and `savedView` is a `const` in the outer `init` scope, this works due to closure. However, when `loadNodes` is called again later (e.g. from WS handler at L80 or filter changes), `savedView` will still hold the original value from init time. This means fitBounds is never called on subsequent data refreshes even if the user hasn't manually positioned the map — minor logic bug.

21. **Minor — `jumpToRegion` ignores the `iata` parameter** (`map.js` ~L124-128)
    - The function receives `iata` but then fits bounds to ALL nodes with location, not just nodes in that region. Every jump button does the same thing.

22. **Minor — WS handler triggers full `loadNodes()` on every ADVERT packet** (`map.js` ~L77-80)
    - Could cause excessive API calls and re-renders on busy networks. No debouncing.

23. **Minor — `esc()` function called but never defined in map.js** (`map.js` ~L109, ~L112)
    - `esc(p.name)` and `esc(p.pubkey)` — this likely relies on a global `esc` from `app.js`. If `app.js` doesn't define it, this will throw. Fragile dependency.

---

## Nodes Page (`nodes.js`)

### Accessibility

24. **Major — Table rows use `onclick` inline handler via global function** (`nodes.js` ~L164)
    - `onclick="window._nodeSelect('${n.public_key}')"` — rows are not keyboard-focusable (`tabindex` missing), have no `role="button"`, and rely on a global function. This is both an a11y issue and a code smell.

25. **Minor — Tab buttons lack ARIA tab pattern** (`nodes.js` ~L145-148)
    - `.node-tab` buttons don't have `role="tab"`, no `role="tablist"` on container, no `aria-selected`. Screen readers won't understand the tab interface.

26. **Minor — Sort controls on `<th>` elements lack ARIA sort indicators** (`nodes.js` ~L154-156)
    - Sortable columns don't have `aria-sort` attribute to indicate current sort direction.

27. **Minor — Select elements lack labels** (`nodes.js` ~L150-153)
    - `#nodeLastHeard` and `#nodeSort` selects have no `<label>` or `aria-label`. The first `<option>` acts as a pseudo-label ("Last Heard: Any", "Sort: Last Seen") which is a pattern but not accessible.

### Mobile Responsive

28. **Minor — Node table may be hard to read on mobile** (`nodes.js` ~L143)
    - 6 columns (Name, Key, Role, Regions, Last Seen, Adverts) with `font-size: 12px` on mobile. The "Regions" column always shows "—" (hardcoded) — wasted column space.

29. **Minor — Full-screen node view back button uses inline onclick** (`nodes.js` ~L58)
    - `onclick="location.hash='#/nodes'"` — works but not progressive enhancement. Also, `ch-back-btn` class reused from channels page.

### Desktop Space Efficiency

30. **Minor — Detail panel fixed at 420px** (`style.css` ~L52)
    - Panel right is 420px, reasonable. But the node detail includes a map that's only 180px tall — could be taller on desktop.

31. **Minor — "Regions" column always shows "—"** (`nodes.js` ~L167)
    - Column exists in the table but is never populated. Dead column wasting horizontal space.

### Bugs / Inconsistencies

32. **Major — `escapeHtml` defined locally but not used consistently** (`nodes.js` ~L6, ~L80)
    - `escapeHtml` is defined at top of IIFE, but in `renderDetail` (L199) `truncate(decoded.text, 50)` output is NOT escaped before insertion into innerHTML. Potential XSS if decoded text contains HTML.

33. **Minor — Dead code: `debounce` defined at bottom** (`nodes.js` ~L241)
    - `debounce` is defined at the bottom but also likely exists in `app.js` as a global. Redundant.

34. **Minor — `loadNodes` called on every WS packet** (`nodes.js` ~L70)
    - `if (msg.type === 'packet') loadNodes()` — no debouncing, could cause rapid API calls and flickering on busy networks.

35. **Minor — Leaflet map in detail panel not cleaned up on destroy** (`nodes.js` ~L73-76, ~L213)
    - When `selectNode` creates a Leaflet map in the detail panel, there's no reference kept to it and no cleanup. On re-selection, a new map is created without removing the old one, potentially leaking resources.

36. **Minor — `window._nodeSelect` is a global** (`nodes.js` ~L244)
    - Pollutes global namespace. Should use event delegation on the table body instead.

---

## Cross-Cutting Issues

### Style.css

37. **Minor — Duplicated dark theme definitions** (`style.css` ~L24-37 and ~L39-52)
    - `@media (prefers-color-scheme: dark)` and `[data-theme="dark"]` define identical variables. Necessary for the toggle but a maintenance burden — easy for them to drift apart.

38. **Minor — `.nav-btn` defined twice with identical properties** (`style.css` ~L72-73 and ~L97-101)
    - Once in "Touch Targets" section and again in "Nav" section with the same min-width/min-height.

### Index.html

39. **Minor — `onerror=""` on script tags** (`index.html` ~L36-42)
    - Empty `onerror` handlers swallow load errors silently. Better to have no handler or log the error.

40. **Minor — Leaflet loaded from CDN without SRI** (`index.html` ~L27-28)
    - `unpkg.com` scripts loaded without `integrity` or `crossorigin` attributes. Supply chain risk.
