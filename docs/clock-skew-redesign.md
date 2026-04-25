# Clock Skew Classifier — Redesign

**Status:** spec, pre-implementation
**Supersedes:** parts of #690 / #789 / #845 / PR #894
**Date drafted:** 2026-04-24

## Problem

The current classifier (`cmd/server/clock_skew.go`) uses windowed medians, hysteresis, "good fraction" floors, and a 365-day `no_clock` threshold. It produces:

- False `no_clock` flags on nodes whose clocks are working today but had garbage timestamps in recent samples.
- Symmetric severity bands that conflate "clock at firmware default" with "operator set the clock wrong by a year" — completely different operator actions required.
- Compounding over-engineering as each operator complaint added a new tier or window.

The actual physical reality of these devices is much simpler than the classifier assumes.

## Hardware reality

Most MeshCore nodes have **no auto-updating RTC**. There are two hardware paths:

1. **Volatile RTC nodes** (`firmware/src/helpers/ArduinoHelpers.h:11` — `VolatileRTCClock`):
   - On boot, `base_time` is hardcoded to a firmware-build constant (currently `1715770351` = 2024-05-15 20:52:31 UTC).
   - `getCurrentTime()` returns `base_time + millis()/1000`.
   - On reboot the value snaps back to the constant.
   - User must manually sync via companion app (`set time` CLI invokes `setCurrentTime(...)`) to set a real wall-clock time, which then ticks until the next reboot.

2. **Hardware-RTC nodes** (`firmware/src/helpers/AutoDiscoverRTCClock.cpp` — DS3231 / RV3028 / PCF8563):
   - Real-time chip with battery backup. Holds the time across reboots.
   - Behaves correctly once set; no default-snap behavior.

The `set time RESET` CLI command (`firmware/src/helpers/CommonCLI.cpp:215`) explicitly calls `setCurrentTime(1715770351)` regardless of hardware — so even hardware-RTC nodes can be deliberately reset to the default epoch.

**Therefore every node is in exactly one of these states:**

| State | Description |
|---|---|
| **Default / never set** | RTC is at a firmware-default epoch + ticking up since the last boot. |
| **Set, drifting normally** | RTC was synced; small skew accumulating at ~0.8s/day per #789 reports. |
| **Set, drifted past tolerance** | Like above but skew has grown beyond what's useful. |
| **Wrong** | Operator-set incorrect time, or genuine RTC malfunction not matching any known default. |

There is no "bimodal RTC bug" — what looked bimodal in #845 is just a sequence of `defaulted → user sync → reboot → defaulted again`. The "bad" timestamps are not noise; they're a constant (the default epoch + a small uptime).

## Production data analysis (2026-04-24)

### 00id.net (this deployment, 416 nodes, commit `abd9c46`)

`lastSkewSec` (advert_ts − observed_ts) distribution:

| Bucket | Count | Pct |
|---|---:|---:|
| OK ≤15s | 90 | 22% |
| Degrading ≤60s | 93 | 22% |
| Degraded ≤10m | 13 | 3% |
| off ≤1d | 5 | 1% |
| off ≤1y | 110 | 26% |
| absurd >1y | 105 | 25% |

Per-node `lastAdvertTS` raw timestamp distribution shows a sharp default cluster:

```
+0 days  count=19 samples=114969       ← exactly at 1715770351 (just rebooted)
+1d      count=9  samples=24766
+2d      count=7  samples=58101
+3d      count=2  samples=360
...                                     ← decay through ~110 days
+113d    count=2  samples=53776
```

103 of 416 nodes (25%) have `lastAdvertTS` between `1715770351` and `1715770351 + 1095 days`, consistent with the volatile-RTC-default-ticking-up pattern.

A second cluster of 5 nodes has `lastAdvertTS = 1672531542 ≈ 1672531200 + 5min` = **2023-01-01 00:00:00 UTC** + small uptime. This is a *different* firmware-default epoch from an older firmware version.

### Cascadia (analyzer.cascadiamesh.org, 433 nodes in 5000-packet sample, commit `111b03c` v3.5.0)

ADVERT timestamp by year-month:

```
1970-01    1     ← epoch zero (ESP32 native fallback OR ancient firmware)
2021-01    1     ← possible third default epoch
2023-01    2     ← old firmware default (matches 00id)
2024-05   60     ← current VolatileRTCClock + days uptime
2024-06   39     ← same default + weeks uptime
2024-07   21
2024-08   10
2024-09    2
2024-10    1
2024-11    2     ← decays out as fewer nodes have multi-month uptime since reboot
2025-10    1     ← pre-current-now miscellany
2025-11    2
2026-03    4
2026-04  285     ← currently set clocks (this is "now-ish")
2027-04    1     ← operator set wrong by ~1 year (typo?)
2067-12    1     ← operator set wildly wrong / corrupted RTC
```

Confirms the model: ~67% of nodes have a current clock, ~32% are at known firmware defaults at varying uptime offsets, ~3 outliers represent genuine misconfigurations.

## Known firmware default epochs

These are the values discovered in production data so far:

| Epoch (unix) | UTC | Source |
|---:|---|---|
| `0` | 1970-01-01 | Likely ESP32 boot when no RTC initialization runs (`time(NULL)` returns 0). |
| `1609459200` | 2021-01-01 | Speculation — single-sample evidence, validate as more data arrives. |
| `1672531200` | 2023-01-01 | Older firmware `VolatileRTCClock::base_time` value. |
| `1715770351` | 2024-05-15 20:52:31 | **Current** `VolatileRTCClock` constructor + `set time RESET` CLI. |

Treat the table as data, not fixed code. New firmware versions will introduce new defaults; expect to add to the list over time.

## Reconciliation with #690 — the four timestamps

#690 lists three timestamps; in practice there are four signals worth distinguishing:

| Signal | Source | Used for |
|---|---|---|
| `advert_ts` | Inside MeshCore packet, set by sending node | Per-node classification (THE signal). |
| `mqtt_envelope_ts` | Set by observer when it forwards via MQTT | Observer-side calibration only — *not* a direct node-skew signal because observer clock can itself be wrong. |
| `corescope_received_ts` | Wall clock when CoreScope ingested the message | Reference "now"; calibration cross-check. |
| `same_packet_across_observers` | Multiple observers seeing the same hash | Phase 2 calibration (triangulation). |

**Inputs flow:**

1. **Phase 2 (existing, kept):** for each packet hash seen by ≥2 observers, compute each observer's deviation from the per-packet median observed_ts → `observerOffset`. This is the triangulation #690 calls for ("Same packet observed by more than one (ideally 3+) observers gives good indication if one observer is off"). Observer offsets are the calibration table.
2. **Per-advert correction (existing, kept):** `correctedSkew = (advert_ts - observed_ts) + observerOffset[observer_id]`. If no calibration exists for an observer, fall back to raw skew with `calibrated: false`.
3. **Default detection (new):** runs on RAW `advert_ts`, not corrected. The firmware default is a fixed wall-clock value; observer offsets are seconds-to-minutes scale and cannot move `advert_ts` from 2024 to 2026. Default check is independent of calibration.
4. **Severity classification (new):** if `is_default(advert_ts)` → `default`; else classify by `|correctedSkew|` band.

This keeps everything #690 asks for (observer detection, bias subtraction, triangulation), and adds the firmware-default cluster as a new pre-empting tier.

## UI: explain WHY (#690 requirement)

The classifier alone doesn't satisfy #690's "present on the UI why clock skew is obvious or suspected." The evidence panel from PR #906 (per-hash observer breakdown showing raw vs corrected skew per observer) is the WHY.

For each per-node clock card the UI must show:

- **Tier badge** (default / ok / degrading / degraded / wrong) + magnitude.
- **Plain-English reason line**: e.g. "Last advert at 2024-05-15 + 3.2 days uptime — matches firmware default (volatile RTC, not yet user-set)" or "Last advert −12s vs wall clock — within OK tolerance."
- **Calibration footnote**: "Skew corrected using observer X offset +1.7s (computed from 412 multi-observer packets)" or "Single-observer measurement, no calibration available."
- **Evidence accordion** (PR #906 shape, retained): for the most recent N hashes, each observer's raw vs corrected skew + the observer's offset.

For the per-observer page (also from PR #906): show the observer's offset, the multi-observer sample count, and a tier badge using the same scale (treating `|observerOffset|` as the skew).

## Proposed classifier

Per-advert classification, no windowing:

```python
DEFAULT_EPOCHS = [0, 1609459200, 1672531200, 1715770351]
MAX_PLAUSIBLE_UPTIME_SEC = 1095 * 86400  # 3 years

def is_default(ts):
    return any(d <= ts <= d + MAX_PLAUSIBLE_UPTIME_SEC for d in DEFAULT_EPOCHS)

def classify(advert_ts, corrected_skew_sec):
    if is_default(advert_ts):
        return "default"          # gray
    abs_skew = abs(corrected_skew_sec)
    if abs_skew <= 15:    return "ok"           # green
    if abs_skew <= 60:    return "degrading"    # yellow
    if abs_skew <= 600:   return "degraded"     # orange
    return "wrong"                              # red
```

`corrected_skew_sec` is the observer-bias-subtracted skew per Phase 2 calibration. Default detection is independent of calibration (runs on raw `advert_ts`).

Per-node state = classification of the node's most-recent advert (per hash, picking the most recent observation across all observers). No medians, no good-fraction, no hysteresis.

## Severity tier definitions

| Tier | Condition | Color | UI label | Meaning |
|---|---|---|---|---|
| `default` | Advert ts within `[default, default + 3y]` of any known epoch | Gray | "Default" | Volatile RTC at firmware boot constant; never set or rebooted and not re-synced. |
| `ok` | abs(skew) ≤ 15s | Green | "OK" | Working clock. |
| `degrading` | 15s < abs(skew) ≤ 60s | Yellow | "Degrading" | Real but accumulating drift. |
| `degraded` | 60s < abs(skew) ≤ 600s | Orange | "Degraded" | Off by minutes — needs re-sync. |
| `wrong` | abs(skew) > 600s and not `default` | Red | "Wrong" | Operator-set error or RTC malfunction. |

## What this kills

- The 365-day `no_clock` threshold and the entire `recentSkewWindow{Count,Sec}` machinery.
- The hysteresis / `goodFraction` / `longTermGoodFraction` logic from PR #894.
- The proposed `bimodal_clock` tier from #845 — the pattern is not bimodal, it's defaulted vs set.
- All Theil-Sen drift calculations as classifier inputs (drift remains a derived display value).

## What this preserves

- **Phase 2 observer calibration** (`calibrateObservers()`) — kept verbatim. It's what powers the "subtract observer bias" requirement from #690 and provides the triangulation evidence the UI needs.
- **Drift display** (computed but not classifying).
- **PR #906 evidence UI** — orthogonal to the classifier; it is in fact the implementation of #690's "explain WHY" requirement. Only label strings change to match the new tier names.
- **`/api/observers/clock-skew`** — unchanged shape.

## API impact

`/api/nodes/{pubkey}/clock-skew` response changes:

- `severity` enum: `default | ok | degrading | degraded | wrong` (no more `no_clock | severe | warn | absurd`).
- New field `defaultEpoch` (int, optional): if `severity == "default"`, the matched epoch.
- Drop fields: `recentMedianSkewSec`, `goodFraction`, `recentBadSampleCount`, `longTermGoodFraction`.
- Keep: `lastSkewSec`, `medianSkewSec`, `meanSkewSec`, `driftPerDaySec`, `sampleCount`, `calibrated`, `lastAdvertTS`, `lastObservedTS`, `nodeName`, `nodeRole`.

`/api/nodes/clock-skew` (fleet) shape unchanged except severity enum values.

## UI impact

- New CSS classes `skew-badge--default`, `skew-badge--degrading`, `skew-badge--degraded`, `skew-badge--wrong`. Drop `--no_clock`, `--severe`, `--warn`, `--absurd`, `--bimodal_clock`.
- Tooltip text updated per tier.
- "Default" badge tooltip should explain the clock is at firmware default plus uptime since boot, and the operator hasn't set it yet (or hasn't re-set it since the last reboot).

## Migration

Single PR replaces the classifier in `clock_skew.go` and updates the frontend badges/labels. No database schema change, no data migration — all per-call computation.

## Open issues to close

- **#789** (median hides corrected clocks) — resolved by per-advert classification.
- **#845** (bimodal_clock tier) — replaced by `default` tier; the pattern that motivated it is correctly captured.
- **PR #894** — close without merging; this design supersedes Option C entirely.
- **#690** UI completion (PR #906) — keeps moving in parallel; only label updates needed.

## Validation plan

1. Hand-run the classifier against a snapshot of `/api/nodes/clock-skew` from 00id and cascadia. Confirm:
   - All 103 00id "absurd" nodes reclassify as `default`.
   - All 5 cascadia 2023-01 nodes reclassify as `default`.
   - The 2027 / 2067 cascadia outliers reclassify as `wrong`.
   - The 285 cascadia 2026-04 nodes reclassify as `ok` (or `degrading` if drift exceeds 15s).
2. Add per-tier unit tests in `cmd/server/clock_skew_test.go`.
3. Add a regression test for each known default epoch (synthesize advert at `default + 0s`, `default + 1d`, `default + 3y - 1s` → all classify as `default`).
4. Edge cases:
   - `advert_ts == 0` → matches default epoch 0.
   - `advert_ts == 1715770351 + 731 days` → no longer matches (uptime cap exceeded) — should fall through to time-based classification, likely `wrong`.
   - Future timestamps beyond `now + 600s` → `wrong`.

## Out of scope (follow-ups)

- Per-firmware-version known-default lookup (when `firmware_version` field becomes reliable on adverts).
- Reboot-count / flakiness indicator ("this node has hit default N times in last 30d").
- Auto-discovery of new default epochs from clustering analysis (could detect a 4th default emerging in the wild).
- Filtering defaulted-clock adverts out of time-windowed analytics queries (separate spec — affects path attribution).
