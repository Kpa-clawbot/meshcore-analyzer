# UI/UX Review: Live Page + Packets Page

## Live Page

### Accessibility

| # | Severity | Issue | Location |
|---|----------|-------|----------|
| L-A1 | **Critical** | VCR buttons use emoji-only labels (`⏪`, `⏸`, `▶`) with no `aria-label`. Screen readers will announce meaningless characters. | `live.js` ~L310-315 (init HTML template) |
| L-A2 | **Critical** | Sound toggle button (`🔇`/`🔊`) has a `title` but no `aria-label` and no `aria-pressed` state. | `live.js` ~L324, ~L390 |
| L-A3 | **Major** | Heat/Ghost checkbox toggles use bare `<label><input>` with short text but no `id`/`for` association — works due to nesting, but the checkboxes lack `aria-` descriptions of what they control. | `live.js` ~L326-329 |
| L-A4 | **Major** | VCR LCD canvas (`#vcrLcdCanvas`) has no `aria-label` or `role="img"` — the 7-segment time display is completely invisible to screen readers. No text alternative exists. | `live.js` ~L349, `live.css` ~L263 |
| L-A5 | **Major** | Feed items are `<div>` elements with `cursor: pointer` and click handlers but no `role="button"`, `tabindex`, or keyboard handler. Entirely mouse-only. | `live.js` ~L502-510 |
| L-A6 | **Major** | Feed detail card (`.feed-detail-card`) is a popup with no focus trap, no `role="dialog"`, no `aria-label`. Dismiss is mouse-only (click outside). No Escape key handler. | `live.js` ~L527-545 |
| L-A7 | **Minor** | Legend panel (`.live-legend`) uses plain `<div>` for colored dots — no semantic list (`<ul>`/`<li>`) and colored dots rely solely on color to convey meaning. | `live.js` ~L332-345 |
| L-A8 | **Minor** | Scope buttons (`1h`, `6h`, etc.) have no `aria-pressed` or `role="radiogroup"` semantics. Active state is visual-only via CSS class. | `live.js` ~L339-344 |
| L-A9 | **Minor** | The VCR prompt buttons (`▶ Replay`, `⏭ Skip to live`) are created via `innerHTML` — no keyboard focus management after they appear. | `live.js` ~L100-112 |

### Mobile Responsive

| # | Severity | Issue | Location |
|---|----------|-------|----------|
| L-M1 | **Major** | VCR bar on mobile (≤600px) only reduces padding/font slightly. The bar has: 4 buttons + mode indicator + scope buttons + timeline + LCD panel, all in a row. This will overflow or be extremely cramped on phones <375px wide. | `live.css` ~L296-301 |
| L-M2 | **Major** | VCR scope buttons (`1h`/`6h`/`12h`/`24h`) are tiny at `0.6rem` / `1px 4px` padding on mobile — well below 44px touch target minimum. | `live.css` ~L299 |
| L-M3 | **Major** | VCR control buttons on mobile are `3px 6px` padding at `0.7rem` font — similarly tiny touch targets (~24px). | `live.css` ~L298 |
| L-M4 | **Major** | Timeline tooltip (`mousemove` only) doesn't work on touch. Touch scrubbing works but there's no time feedback tooltip during touch drag. | `live.js` ~L405-412 |
| L-M5 | **Minor** | Legend is `display: none` on mobile (`live.css` ~L179) which is good, but there's no alternative way to access it (e.g., a toggle button). |
| L-M6 | **Minor** | Feed detail card is positioned `right: 14px; top: 50%; transform: translateY(-50%)` absolutely — on narrow phones it may overlap the feed panel or go off-screen. | `live.css` ~L186 |
| L-M7 | **Minor** | The `live-header` wraps on mobile but the sound button and toggles may push to a second row without clear separation. | `live.css` ~L175-179 |

### Desktop Space Efficiency

| # | Severity | Issue | Location |
|---|----------|-------|----------|
| L-D1 | **Minor** | Feed panel is fixed at 360px width — on ultrawide monitors this is a small fraction of the screen. Could be wider or resizable. | `live.css` ~L83 |
| L-D2 | **Minor** | Feed is capped at 25 items (`live.js` ~L515) and `max-height: 340px` — reasonable but no scroll indicator for users. The `overflow: hidden` means items are silently dropped, not scrollable. | `live.css` ~L84 |
| L-D3 | **Minor** | VCR LCD panel has `min-width: 110px` — takes space even when mode text is short. Fine overall. | `live.css` ~L252 |

### Bugs / Inconsistencies

| # | Severity | Issue | Location |
|---|----------|-------|----------|
| L-B1 | **Major** | `overflow: hidden` on `.live-feed` means older feed items are clipped, not scrollable. Users can never scroll to see older items — they're just cut off. Should be `overflow-y: auto`. | `live.css` ~L84 |
| L-B2 | **Major** | `drawLcdText` reuses variable name `ch` (function param) shadowed by `ch2` but the outer `ch` in the canvas sizing (`const ch = canvas.offsetHeight`) is shadowed by a loop variable `const ch2 = text[i]` — actually this is fine since renamed to `ch2`. However, the dim color calculation `color.replace(/[\d.]+\)$/, '0.07)')` assumes the color is always in `rgba()` format, but it's called with `'#4ade80'` (hex). The regex won't match, so ghost segments get the raw hex string as color, likely rendering as black or transparent. | `live.js` ~L188-189 |
| L-B3 | **Major** | Multiple `setInterval` calls in `init()` (rate counter ~L376, timeline refresh ~L429, clock tick ~L434) are never cleared in `destroy()`. These leak across page navigations. | `live.js` ~L376, L429, L434 vs L593-610 |
| L-B4 | **Minor** | `vcrRewind` fetches `limit=200` packets but `vcrReplayFromTs` fetches `limit=10000` — inconsistent fetch sizes for similar operations. The 10K fetch could be very slow on large datasets. | `live.js` ~L126 vs L91 |
| L-B5 | **Minor** | `replayRecent` fetches `limit=8` — hardcoded magic number with no configuration. | `live.js` ~L398 |
| L-B6 | **Minor** | Dead/unused CSS: `.vcr-clock { display: none; }` and `.vcr-lcd-time { display: none; }` — leftover from refactor. | `live.css` ~L247, L266 |
| L-B7 | **Minor** | The nav auto-hide timeout (4s) means the nav disappears while users may still be reading it. No way to pin it open. | `live.js` ~L445-454 |
| L-B8 | **Minor** | `VCR.buffer` is capped at 2000 entries by splicing 500 from the front (`live.js` ~L236-237), which means timeline playhead indices could become stale if packets are spliced while in PAUSED or REPLAY mode. | `live.js` ~L236-237 |

---

## Packets Page

### Accessibility

| # | Severity | Issue | Location |
|---|----------|-------|----------|
| P-A1 | **Critical** | Table rows use `onclick` inline handlers (`onclick="window._pktSelect(…)"`) with no `tabindex`, `role`, or `onkeydown`. Entire table is keyboard-inaccessible. | `packets.js` ~L209-212, L238-244 |
| P-A2 | **Critical** | Global functions exposed on `window` (`_pktSelect`, `_pktToggleGroup`, `_pktRefresh`, `_pktBYOP`) via `onclick` attributes — no keyboard equivalent and pollutes global namespace. | `packets.js` ~L363-380 |
| P-A3 | **Major** | Filter `<select>` elements and `<input>` fields have no associated `<label>` elements. Only `placeholder` text which disappears on input. Screen readers get no context. | `packets.js` ~L144-150 |
| P-A4 | **Major** | "Group by Hash" toggle button has no `aria-pressed` state to indicate current on/off status. | `packets.js` ~L152 |
| P-A5 | **Major** | BYOP modal has no focus trap, no `role="dialog"`, no `aria-label`. Escape key doesn't close it. | `packets.js` ~L303-325 |
| P-A6 | **Major** | Node filter dropdown (autocomplete) has no ARIA combobox pattern (`role="listbox"`, `aria-activedescendant`, etc.). Arrow key navigation not supported. | `packets.js` ~L172-192 |
| P-A7 | **Minor** | Path hop links have `onclick="event.stopPropagation()"` as an inline HTML attribute string — screen readers see these as links which is correct, but `stopPropagation` prevents row selection which may confuse keyboard users. | `packets.js` ~L42 |
| P-A8 | **Minor** | The "Loading…" state in the detail panel is a plain `<div>` with no `aria-live` region. Screen readers won't announce when content loads. | `packets.js` ~L224 |

### Mobile Responsive

| # | Severity | Issue | Location |
|---|----------|-------|----------|
| P-M1 | **Major** | The packets table has 10 columns (expand, region, time, hash, size, type, observer, path, repeat count, details). On mobile, `style.css` sets `max-width: 120px` per cell and allows horizontal scroll on `.panel-left`, but the table will still be very wide. No column hiding strategy for mobile. | `style.css` ~L496-499 |
| P-M2 | **Major** | On mobile (≤640px), `.split-layout` stacks vertically with `.panel-right` getting `max-height: 50vh` — but the detail panel has complex content (hex dump, field table, message preview) that may need more space. No way to expand it. | `style.css` ~L489 |
| P-M3 | **Minor** | Filter bar goes `flex-direction: column` on mobile, which is good, but the node filter dropdown (`position: absolute`) may not align correctly in the stacked layout. | `style.css` ~L493-495 |
| P-M4 | **Minor** | Panel resize handle (drag to resize) is mouse-only — no touch support implemented. The handle is 6px wide, hard to grab on touch. | `packets.js` ~L14-36 |
| P-M5 | **Minor** | BYOP modal textarea at `min-height: 60px` is small on mobile for pasting long hex strings. | `style.css` modal styles |

### Desktop Space Efficiency

| # | Severity | Issue | Location |
|---|----------|-------|----------|
| P-D1 | **Minor** | Detail panel defaults to 420px (`style.css` ~L117) which is reasonable. Saved width is restored from localStorage which is nice. |
| P-D2 | **Minor** | The table has no column visibility toggle — on wide screens all 10 columns show, but some (like the empty expand column for non-grouped rows, or the "Rpt" column) waste space. | `packets.js` ~L139 |
| P-D3 | **Minor** | `max-width: 180px` on `<td>` (`style.css` ~L139) truncates path and detail columns even when there's plenty of room. Column resize helps but the default is tight. |

### Bugs / Inconsistencies

| # | Severity | Issue | Location |
|---|----------|-------|----------|
| P-B1 | **Major** | `renderLeft()` rebuilds entire filter bar HTML on every `loadPackets()` call, destroying and re-creating event listeners. This means: (1) user's cursor position in filter inputs is lost, (2) dropdown state is reset, (3) it's called on every WS `packet` message, causing constant re-renders while typing. | `packets.js` ~L115 (wsHandler calls loadPackets), ~L122 (renderLeft rebuilds everything) |
| P-B2 | **Major** | Regions are hardcoded: `window._regions = {"SJC":…,"LAR":…}` — this is a TODO/hack that should come from the server. | `packets.js` ~L354-358 |
| P-B3 | **Minor** | `escapeHtml` is defined in both `live.js` (~L548) and `packets.js` (~L267) — duplicated utility. | Both files |
| P-B4 | **Minor** | `payloadTypeName`, `payloadTypeColor`, `routeTypeName`, `truncate`, `timeAgo`, `api`, `onWS`, `offWS`, `registerPage`, `makeColumnsResizable` — these are all called but never imported/defined in `packets.js`. They must be globals from `app.js`. No error handling if they're missing. | Throughout `packets.js` |
| P-B5 | **Minor** | `directPacketId` is module-scoped but set to `null` in init, then read and cleared — race condition if init is called twice rapidly. | `packets.js` ~L70, L100-115 |
| P-B6 | **Minor** | The `destroy()` function clears `packets` and `selectedId` but doesn't clear `expandedHashes`, `hopNameCache`, `totalCount`, or `observers` — stale state persists across page navigations. | `packets.js` ~L119-123 |
| P-B7 | **Minor** | No empty state — when no packets match filters, the table body is just empty with no message. | `packets.js` renderTableRows |
| P-B8 | **Minor** | No error state — `loadPackets` catches errors with `console.error` only. User sees stale data with no indication of failure. | `packets.js` ~L113 |
| P-B9 | **Minor** | The field table section rows use dark mode hardcoded colors: `.section-row td { background: #eef2ff }` — this won't respect dark theme. | `style.css` ~L160 |
