# Table Sorting Consistency Spec (#620)

## Problem

CoreScope has 20+ data tables. Only 2 are sortable (nodes list, channel activity). Those 2 use incompatible implementations — different property names (`column`/`direction` vs `col`/`dir`), different data attributes (`data-sort` vs `data-sort-col`), different function signatures. The remaining 18+ tables, including the packets table (30K+ rows), have zero sorting.

This violates AGENTS.md DRY rules and frustrates users who can see data but can't reorder it.

## Solution

One shared `TableSort` module. Every data table uses it. Same UX everywhere.

## Shared Utility Design

### Module: `public/table-sort.js`

IIFE pattern (like `channel-colors.js`). No dependencies. No build step.

```js
window.TableSort = (function() {
  return { init, sort, destroy };
})();
```

### API

```js
TableSort.init(tableEl, {
  defaultColumn: 'last_seen',     // initial sort column
  defaultDirection: 'desc',       // 'asc' or 'desc'
  storageKey: 'nodes-sort',       // localStorage key (optional)
  comparators: {                  // custom comparators for non-string columns
    time: (a, b) => ...,
    snr:  (a, b) => ...,
  },
  onSort: (column, direction) => {} // callback after sort completes
});
```

### How It Works

1. Scans `<th>` elements for `data-sort="columnName"` attribute
2. Attaches click handlers — click toggles asc/desc
3. On sort: reads `<td data-value="...">` (raw sortable value) from each row
4. Sorts rows in-place via DOM reorder (no innerHTML rebuild — important for 30K rows)
5. Updates visual indicator and `aria-sort` on active `<th>`

### Visual Indicator

Active column header gets `▲` (ascending) or `▼` (descending) appended as a `<span class="sort-arrow">`. Inactive columns show no arrow. CSS class `.sort-active` on the active `<th>`.

### Built-in Comparators

| Type | Detected From | Behavior |
|------|--------------|----------|
| `numeric` | `data-type="number"` on `<th>` | `Number(a) - Number(b)`, NaN sorts last |
| `text` | default | `localeCompare` |
| `date` | `data-type="date"` | Parse as timestamp, numeric compare |
| `dbm` | `data-type="dbm"` | Strip " dBm" suffix, numeric compare |

Custom comparators in `options.comparators` override built-in types.

### Accessibility

- `aria-sort="ascending"`, `"descending"`, or `"none"` on every sortable `<th>`
- `role="columnheader"` (already implicit for `<th>`)
- `cursor: pointer` and `:hover` style on sortable headers
- Keyboard: sortable headers are focusable, Enter/Space triggers sort

### Performance (Critical for Packets Table)

- Sort via DOM node reorder (`appendChild` loop), not `innerHTML`. Browser batches reflows.
- `data-value` attributes hold raw values — no parsing during sort.
- For 30K rows: expected sort time ~100-200ms (single `Array.sort` + DOM reorder). If >500ms, add a virtual scroll layer in a follow-up — but don't pre-optimize.
- No re-render of row content. Sort only changes order.

## Milestones

### M1: Shared utility + packets table
- Create `public/table-sort.js`
- Unit tests: `test-table-sort.js` (Node.js, jsdom or vm.createContext)
- Integrate with packets table (highest impact — 30K rows, currently unsortable)
- Default sort: time descending
- Columns: all current packets columns (Region, Time, Hash, Size, HB, Type, Observer, Path, Rpt, Details)
- Browser validation: sort 30K rows, verify <500ms

### M2: Nodes list + node detail tables
- Migrate nodes list from custom sort to `TableSort.init()`
- Add sorting to neighbor table (side pane + detail page)
- Add sorting to observer stats table (detail page)
- Remove old `sortState`/`sortArrow` code from `nodes.js`

### M3: Analytics tables
- Hash collisions tables (node table, sizes table, collision prefixes)
- RF statistics table
- Route frequency, co-appearance, topology tables
- Node health tables (top by packets/SNR/observers, recently active)
- Distance tables (by link type, top 20 longest)
- Per-node analytics: peer contacts

### M4: Channels list + observers list + comparison table
- Channel activity table: migrate from custom sort to `TableSort.init()`
- Remove old `_channelSortState` code from `analytics.js`
- Observers list table
- Comparison table (`compare.js`)

### M5: Cleanup
- Remove all old sorting code (both implementations)
- Verify no dead CSS/JS from old sort code
- Final consistency audit: every data table uses `TableSort.init()`

### Out of Scope
- `packets.js` hex breakdown (structural decode, fixed order)
- `audio-lab.js` debug tables (not user-facing)
- Virtual scroll / pagination (separate issue if perf requires it)

## Testing

### Unit Tests (`test-table-sort.js`)
- Numeric sort ascending/descending
- Text sort with localeCompare
- Date sort
- dBm sort (strip suffix)
- Custom comparator override
- NaN/null/undefined sort to end
- Toggle direction on repeated click
- `aria-sort` attribute updates
- localStorage persistence (read + write)
- `data-value` attribute used over text content

### Integration (per milestone)
- Playwright test: click column header, verify row order changes
- Playwright test: click again, verify direction toggles
- Playwright test: visual indicator present on active column

### Performance
- Unit test: sort 30K mock rows in <500ms (assert timing)
- Required per AGENTS.md: perf claims need proof

## Migration Path

Existing sort code in `nodes.js` and `analytics.js` will be replaced, not wrapped. Both current implementations are <100 lines each — replacing is simpler than adapting. The shared utility subsumes all their functionality.

Old localStorage keys (`nodes-sort-*`, channel sort state) should be migrated or cleared on first use of the new utility.
