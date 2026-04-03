# First-Hop Neighbor Affinity Graph

**Issue:** [#482](https://github.com/Kpa-clawbot/CoreScope/issues/482)
**Status:** Draft Spec
**Date:** 2026-04-03

---

## Overview

### What is the first-hop neighbor affinity graph?

A weighted, undirected graph where nodes are MeshCore devices and edges represent observed first-hop neighbor relationships. Each edge carries a weight (observation count), recency data, and optional signal quality metrics. The graph is built by analyzing `path_json` data from received packets to extract direct neighbor relationships from both ends of the path.

### Why is it needed?

MeshCore uses short hash prefixes (typically 1–2 bytes) to identify nodes in routing paths. When multiple nodes share the same short prefix (hash collision), the current "Show Neighbors" feature on the map page cannot reliably determine which physical node a hop refers to. This causes:

1. **Show Neighbors returns zero results** when the hop resolver picks the wrong collision candidate (#484)
2. **Incorrect topology display** — paths appear to route through the wrong node
3. **hash_size==0 nodes inflate collision counts** (#441), compounding the problem

### Primary value: 1-byte hash networks

The disambiguation value of this graph is highest in networks using 1-byte hash prefixes. With ~2K nodes and 1-byte prefixes, approximately 8 nodes share each prefix — making collisions the norm rather than the exception. This is the primary use case.

With 2+ byte hash prefixes, collisions are rare enough that simple prefix matching usually resolves unambiguously. The neighbor affinity graph still provides topology insight but is less critical for disambiguation.

### What problem does it solve?

By aggregating first-hop observations over time, we build a stable model of which nodes are physically adjacent. This model serves as a disambiguation signal: when a 1-byte prefix like `"C0"` appears as a hop, and we know from hundreds of prior observations that `c0dedad4...` is a neighbor of the adjacent hop but `c0dedad9...` is not, we can resolve the ambiguity with high confidence.

This is especially effective for **repeaters and routers** which are stationary — their neighbor relationships are stable and predictable.

---

## Protocol Reference

> **Source:** MeshCore firmware `Mesh.cpp`, `routeRecvPacket()` — verified 2026-04-03.

Repeaters **append** their hash to the path array in queue order (oldest first). This means:

| Position | Meaning |
|----------|---------|
| `path[0]` | Originator's direct neighbor — the **first repeater** that forwarded the packet |
| `path[last]` | Observer's direct neighbor — the **last repeater** before the packet reached the observer |
| Originator | **Never** appears in the path |
| Observer | **Never** appears in the path |
| `path = []` | **Direct/zero-hop** — the originator transmitted directly to the observer with no repeaters |

Example: node `X` sends a packet that is relayed by `R1 → R2 → R3` and received by observer `O`:
- `path = ["R1", "R2", "R3"]`
- `path[0] = R1` → `X`'s direct neighbor
- `path[last] = R3` → `O`'s direct neighbor

Implementers should not need to re-verify this against the firmware source.

---

## Data Model

### How neighbor relationships are derived

Every packet stored in CoreScope has a `path_json` field containing the route hops as a JSON array of hex strings, e.g. `["A3","B7","C0"]`. Two types of edges can be extracted:

#### Edge type 1: `originator ↔ path[0]` (ADVERT packets only)

Only **ADVERT** packets expose the originator's full public key in cleartext. All other packet types (REQ, TXT_MSG, ACK, etc.) have encrypted payloads that do not reveal the sender's identity. Therefore, the `originator ↔ path[0]` edge can **only** be extracted from ADVERT packets.

A packet from node `X` with path `["A3", "B7"]` tells us:
- `X` has a direct neighbor matching prefix `A3`

This is the highest-confidence edge because we know the originator exactly (full pubkey) and only need to resolve `path[0]`.

#### Edge type 2: `observer ↔ path[last]` (ALL packet types)

The observer's identity is always known from the server connection context — it is not derived from the packet payload. Therefore, `observer ↔ path[last]` can be extracted from **any** packet type, not just ADVERTs.

A packet with path `["A3", "B7"]` received by observer `O` tells us:
- `O` has a direct neighbor matching prefix `B7`

This effectively doubles the graph data compared to extracting only originator edges.

#### Summary of edge extraction by packet type

| Packet Type | `originator ↔ path[0]` | `observer ↔ path[last]` |
|-------------|:----------------------:|:-----------------------:|
| ADVERT      | ✅ Yes                 | ✅ Yes                  |
| All others  | ❌ No (encrypted)      | ✅ Yes                  |

### Empty path handling

- **ADVERT with `path = []`**: The originator transmitted directly to the observer — zero hops. Create edge `originator ↔ observer` directly (both identities are known).
- **Non-ADVERT with `path = []`**: No edge can be extracted. The originator identity is unknown (encrypted) and there are no path hops.
- **Single-hop (`path` has 1 element)**: For ADVERTs, `path[0] == path[last]`, so both edge types resolve to the same single edge. The originator's neighbor and the observer's neighbor are the same repeater.

### What constitutes a "first-hop neighbor"

A first-hop neighbor of node `X` is any node that appears as `path[0]` in ADVERT packets originating from `X`, any node `Y` where `X` appears as `path[0]` in ADVERT packets originating from `Y`, or any observer `O` where `X` appears as `path[last]` in packets received by `O`. The relationship is bidirectional in physical space (RF range is approximately symmetric), though observations may be asymmetric (node A's packets may be observed more often than node B's).

### Edge directionality

For v1, all edges are **undirected**. An edge between nodes A and B means "A and B have been observed as direct RF neighbors," regardless of which direction the packet traveled. The edge weight is the total observation count from both directions combined.

**Known limitation:** RF propagation is not perfectly symmetric — node A may hear node B but not vice versa. Directional edges would capture this asymmetry and could improve topology visualization accuracy. This is deferred as a future enhancement; for v1's primary purpose (hash disambiguation), directionality does not matter.

### Handling hash collisions

When `path[0]` or `path[last]` is a short prefix like `"A3"`, multiple nodes may match. The system must:

1. **Record all candidates** — do not discard ambiguous observations. Store the raw prefix alongside resolved candidates.
2. **Score candidates by context** — if node `X` has sent 500 packets with `path[0] = "A3"`, and candidate `a3xxxx` appears as a known neighbor of `X`'s other known neighbors but `a3yyyy` does not, `a3xxxx` scores higher.
3. **Use observation count as weight** — a candidate seen 500 times is more likely correct than one seen 2 times.
4. **Flag ambiguity** — edges where multiple candidates exist carry an `ambiguous` flag and a `candidates` list.

### Affinity scoring

Each edge `(A, B)` carries:

| Field | Type | Description |
|-------|------|-------------|
| `count` | int | Total observations of this neighbor relationship |
| `first_seen` | timestamp | Earliest observation |
| `last_seen` | timestamp | Most recent observation |
| `score` | float | Affinity score (0.0–1.0), computed from count + recency |
| `avg_snr` | float | Average SNR across observations (nullable) |
| `observers` | []string | Which observers witnessed this relationship |
| `ambiguous` | bool | Whether the hop prefix had multiple candidates |
| `candidates` | []string | All candidate pubkeys for ambiguous hops |

**Score formula:**

```
score = min(1.0, count / SATURATION_COUNT) × time_decay(last_seen)
```

Where:
- `SATURATION_COUNT = 100` — after 100 observations, count contributes max weight (configurable)
- `time_decay(t) = exp(-λ × hours_since(t))` where `λ = ln(2) / HALF_LIFE_HOURS`
- `HALF_LIFE_HOURS = 168` (7 days) — an edge unseen for 7 days decays to 50% score (configurable)

This means:
- A relationship seen 100+ times recently scores ~1.0
- A relationship seen 100+ times but not in 7 days scores ~0.5
- A relationship seen 5 times 30 days ago scores near 0

### Time decay

Time decay serves two purposes:

1. **Mobile nodes** — clients move; their neighbor relationships change. Decaying old edges prevents stale neighbors from polluting the graph.
2. **Network changes** — repeaters are occasionally moved or decommissioned. Decay ensures the graph converges to current topology.

Decay is applied at **query time**, not stored. The raw `count`, `first_seen`, and `last_seen` are stored; the score is computed when the API responds. This avoids background maintenance jobs and ensures freshness.

---

## Algorithm

### Input

- **Packet store** (`PacketStore`) — in-memory, contains all recent transmissions with observations
- **Node registry** — maps pubkeys to node metadata (name, role, hash_size)

### Processing

```
for each transmission T in packet store:
    from_pubkey = T.from_node (full pubkey, known)
    packet_type = T.type
    
    for each observation O of T:
        path = parsePathJSON(O.path_json)
        observer = O.observer_id
        
        if len(path) == 0:
            // Direct/zero-hop packet
            if packet_type == ADVERT:
                // Originator is observer's direct neighbor
                upsert_edge(from_pubkey, observer, observer, O.snr, O.timestamp)
            // Non-ADVERTs with empty path: no edge can be extracted
            continue
        
        // Edge 1: originator ↔ path[0] (ADVERTs only)
        if packet_type == ADVERT:
            first_hop_prefix = path[0]
            candidates = resolve_prefix(first_hop_prefix)
            upsert_edge(from_pubkey, first_hop_prefix, candidates, observer, O.snr, O.timestamp)
        
        // Edge 2: observer ↔ path[last] (ALL packet types)
        last_hop_prefix = path[len(path)-1]
        last_candidates = resolve_prefix(last_hop_prefix)
        upsert_edge(observer, last_hop_prefix, last_candidates, observer, O.snr, O.timestamp)
```

**`resolve_prefix(prefix)`** looks up all nodes whose pubkey starts with the given prefix (case-insensitive). Returns a list of `(pubkey, name)` tuples.

**`upsert_edge(from, prefix, candidates, observer, snr, timestamp)`**:
- Key: `(from_pubkey, neighbor_prefix)` — canonicalized so `A < B` lexicographically
- If single candidate: set `neighbor_pubkey = candidates[0]`, `ambiguous = false`
- If multiple candidates: set `neighbor_pubkey = null`, `ambiguous = true`, `candidates = [...]`
- Increment `count`, update `last_seen`, running average `avg_snr`
- Add `observer` to the `observers` set

### Disambiguation via graph structure

After building the initial graph, ambiguous edges can be resolved by cross-referencing:

```
for each ambiguous edge E(from=X, prefix="A3", candidates=[a3xx, a3yy]):
    for each candidate C in candidates:
        # Jaccard similarity: normalized mutual-neighbor overlap
        shared = neighbors(X) ∩ neighbors(C)
        union  = neighbors(X) ∪ neighbors(C)
        C.mutual_score = |shared| / |union|   # Jaccard coefficient, 0.0–1.0
    
    # Auto-resolve only with high confidence (see thresholds below)
    if best.mutual_score ≥ 3 × second_best.mutual_score AND best.observations ≥ 3:
        resolve E → best candidate
    else:
        # Remain ambiguous — return all candidates with scores for frontend/user
```

**Jaccard normalization:** Raw mutual-neighbor counts are biased toward high-degree nodes. A repeater with 50 neighbors will share many mutual neighbors with almost anything, inflating its score. Jaccard similarity (`|A ∩ B| / |A ∪ B|`) normalizes for node degree, ensuring that a node sharing 3 of 4 neighbors (Jaccard = 0.75) scores higher than a node sharing 5 of 50 (Jaccard = 0.10).

**Confidence threshold for auto-resolution:** A hash prefix is auto-resolved to a candidate ONLY when:
1. The best candidate's Jaccard score is **≥ 3× the second-best** candidate's score
2. AND the best candidate has **at least 3 observations**

If either condition is not met, the prefix remains ambiguous and the API returns all candidates with their scores. This prevents low-confidence auto-resolution and lets the frontend/user make the final call.

**Transitivity poisoning guard:** When resolving ambiguous prefixes via mutual-neighbor analysis, ONLY use edges where **both endpoints are fully resolved** (known pubkeys from direct observation or unique prefix match). Never use a previously auto-resolved prefix as evidence to resolve another prefix. This prevents cascading errors where one incorrect auto-resolution poisons subsequent resolutions — a single wrong guess could propagate through the graph and corrupt multiple edges.

This exploits graph transitivity: nodes that share many neighbors are likely in the same physical area and thus likely the correct resolution.

### Orphan prefix handling

When `resolve_prefix(prefix)` returns **zero candidates** (the prefix does not match any known node in the registry), the edge is still recorded using the raw prefix as a placeholder. The `NeighborEdge` stores the unresolved prefix with `NodeB = ""` and `Ambiguous = true`, but with an empty `Candidates` list.

The API includes these as `"unresolved": true` entries so the frontend can display them differently (greyed out, "unknown node" label). This preserves topology information — an observer clearly has *some* neighbor at that prefix, even if we don't know who it is yet. When new nodes are registered and match the prefix, the edge can be retroactively resolved on the next graph rebuild.

### Output

A neighbor graph with:
- **Nodes**: all MeshCore nodes (pubkey, name, role)
- **Edges**: weighted, undirected relationships with metadata
- **Clusters**: connected components (optional, for analytics)

---

## API Design

### `GET /api/nodes/{pubkey}/neighbors`

Returns the neighbor list for a specific node.

**Query parameters:**
| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `min_count` | int | 1 | Minimum observation count to include |
| `min_score` | float | 0.0 | Minimum affinity score to include |
| `include_ambiguous` | bool | true | Include edges with unresolved collisions |

**Response:**
```json
{
  "node": "c0dedad4208acb6cbe44b848943fc6d3c5d43cf38a21e48b43826a70862980e4",
  "neighbors": [
    {
      "pubkey": "b7e8f9a0...",
      "prefix": "B7",
      "name": "Ridge-Repeater",
      "role": "repeater",
      "count": 847,
      "score": 0.95,
      "first_seen": "2026-03-01T10:00:00Z",
      "last_seen": "2026-04-02T22:15:00Z",
      "avg_snr": -8.2,
      "observers": ["obs-sjc-1", "obs-sjc-2"],
      "ambiguous": false
    },
    {
      "pubkey": null,
      "prefix": "A3",
      "name": null,
      "role": null,
      "count": 12,
      "score": 0.08,
      "first_seen": "2026-03-15T...",
      "last_seen": "2026-03-20T...",
      "avg_snr": -14.1,
      "observers": ["obs-sjc-1"],
      "ambiguous": true,
      "candidates": [
        {"pubkey": "a3b4c5...", "name": "Node-Alpha", "role": "companion"},
        {"pubkey": "a3f0e1...", "name": "Node-Beta", "role": "companion"}
      ]
    },
    {
      "pubkey": null,
      "prefix": "FF",
      "name": null,
      "role": null,
      "count": 3,
      "score": 0.02,
      "first_seen": "2026-03-28T...",
      "last_seen": "2026-03-29T...",
      "avg_snr": -18.5,
      "observers": ["obs-sjc-1"],
      "ambiguous": true,
      "unresolved": true,
      "candidates": []
    }
  ],
  "total_observations": 2341
}
```

### `GET /api/analytics/neighbor-graph`

Returns the full neighbor graph for analytics/visualization.

**Query parameters:**
| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `min_count` | int | 5 | Minimum edge weight |
| `min_score` | float | 0.1 | Minimum affinity score |
| `region` | string | "" | Filter by observer region |
| `role` | string | "" | Filter nodes by role |

**Response:**
```json
{
  "nodes": [
    {
      "pubkey": "c0dedad4...",
      "name": "Kpa-Roof",
      "role": "repeater",
      "neighbor_count": 5
    }
  ],
  "edges": [
    {
      "source": "c0dedad4...",
      "target": "b7e8f9a0...",
      "weight": 847,
      "score": 0.95,
      "bidirectional": true,
      "avg_snr": -8.2,
      "ambiguous": false
    }
  ],
  "stats": {
    "total_nodes": 42,
    "total_edges": 87,
    "ambiguous_edges": 3,
    "avg_cluster_size": 4.2
  }
}
```

### Enhanced hop resolution

Modify the existing `/api/resolve-hops` endpoint (or add a `context` parameter) to accept neighbor-affinity context:

**Request:**
```json
POST /api/resolve-hops
{
  "hops": ["A3", "B7", "C0"],
  "from_node": "deadbeef...",
  "observer": "obs-sjc-1"
}
```

**Enhanced response (new fields):**
```json
{
  "resolved": [
    {
      "prefix": "A3",
      "pubkey": "a3b4c5...",
      "name": "Node-Alpha",
      "confidence": "neighbor_affinity",
      "affinity_score": 0.95,
      "alternatives": []
    },
    {
      "prefix": "B7",
      "pubkey": "b7e8f9...",
      "name": "Ridge-Repeater",
      "confidence": "unique_prefix",
      "affinity_score": null,
      "alternatives": []
    }
  ]
}
```

**Confidence levels:**
- `unique_prefix` — only one node matches the prefix (no collision)
- `neighbor_affinity` — resolved via neighbor graph (score > threshold)
- `ambiguous` — multiple candidates, no clear winner

---

## Storage

### Approach: In-memory graph, computed from packet store

The neighbor graph is **computed in-memory** from the existing `PacketStore` data. No new database table is needed for the initial implementation.

**Rationale:**
- The packet store already holds all path data in memory
- The graph is a derived view — it doesn't contain data that isn't already in the store
- Computing on demand avoids schema migrations and ingestor changes
- The packet store is the single source of truth; the graph stays consistent automatically

### Data structure

```go
type NeighborGraph struct {
    mu    sync.RWMutex
    edges map[edgeKey]*NeighborEdge  // (pubkeyA, pubkeyB) → edge
    byNode map[string][]*NeighborEdge // pubkey → edges involving this node
}

type edgeKey struct {
    A, B string // canonical order: A < B lexicographically
}

type NeighborEdge struct {
    NodeA       string
    NodeB       string    // may be "" if ambiguous (prefix only)
    Prefix      string    // the raw hop prefix that established this edge
    Count       int
    FirstSeen   time.Time
    LastSeen    time.Time
    SNRSum      float64   // for running average
    Observers   map[string]bool
    Ambiguous   bool
    Candidates  []string  // pubkeys, if ambiguous
}
```

### Cache strategy

- **Build**: Computed on first access after server start, or after packet store reload
- **Invalidation**: Rebuild when packet store ingests new data (piggyback on existing `PacketStore.Ingest()`)
- **TTL**: Cache the graph for 60 seconds (configurable). Neighbor relationships change slowly; sub-minute freshness is unnecessary
- **Cost**: Building the graph iterates all transmissions once. With 30K packets, this takes <100ms. Acceptable for a 60s cache

### Future: persistent storage

If the packet store window is too short to build reliable affinity data, a future milestone can add a `node_neighbors` SQLite table (as described in #482) to accumulate observations across server restarts. The API shape stays the same — only the data source changes from in-memory computation to DB query.

---

## Frontend Integration

### Replacing "Show Neighbors" on the map

The current implementation in `map.js` (`selectReferenceNode()`, lines ~748–770):
1. Fetches `/api/nodes/{pubkey}/transmissions`
2. Client-side walks paths to find hops adjacent to the selected node
3. Compares by full pubkey — **fails on collisions** (#484)

**New implementation:**
1. Fetch `GET /api/nodes/{pubkey}/neighbors?min_count=3`
2. Build `neighborPubkeys` set directly from the response
3. No client-side path walking needed
4. Collision disambiguation is handled server-side

```javascript
async function selectReferenceNode(pubkey, name) {
  selectedReferenceNode = pubkey;
  neighborPubkeys = new Set();
  try {
    const resp = await fetch(`/api/nodes/${pubkey}/neighbors?min_count=3`);
    const data = await resp.json();
    for (const n of data.neighbors) {
      if (n.pubkey) neighborPubkeys.add(n.pubkey);
      // For ambiguous edges, add all candidates (better to show extra than miss)
      if (n.candidates) n.candidates.forEach(c => neighborPubkeys.add(c.pubkey));
    }
  } catch (e) {
    console.warn('Failed to fetch neighbors:', e);
    neighborPubkeys = new Set();
  }
  // ... update UI
}
```

This directly fixes #484.

### Node detail page enhancement

#### Section placement

Insert as a new `node-full-card` section **between "Heard By (N observers)" and "Paths Through This Node"** in the existing node detail layout. Rationale: neighbors are closely related to observers (both represent "who sees this node") and naturally lead into the paths section.

#### Section header

- `<h4>Neighbors (N)</h4>` where N is the neighbor count from the API response
- **Loading state:** `<span class="spinner"></span> Loading neighbors…`
- **Empty state:** `<div class="text-muted">No neighbor data available yet. Neighbor relationships are built from observed packet paths over time.</div>`
- **Error state:** `<div class="text-muted">Could not load neighbor data</div>`

#### Neighbors table

| Column | Content |
|--------|---------|
| **Neighbor** | Name linked to `#/nodes/{pubkey}`, or `{prefix}… (unknown)` for unresolved prefixes |
| **Role** | Role badge (colored pill, same style as node list — uses existing `ROLE_COLORS` / `ROLE_STYLE`) |
| **Score** | Affinity score (number). Tooltip on hover shows raw observation count + Jaccard similarity value |
| **Observations** | Total observation count for this edge |
| **Last Seen** | Timestamp rendered via existing `renderNodeTimestampHtml()` helper |
| **Confidence** | Visual indicator (see below) |

**Confidence indicators:**

| Indicator | Label | Criteria |
|-----------|-------|----------|
| 🟢 | HIGH | Auto-resolved, score ratio ≥ 3×, ≥ 3 observations |
| 🟡 | MEDIUM | 2+ observations but ratio < 3× |
| 🔴 | LOW | Single observation |
| ⚠️ | AMBIGUOUS | Multiple candidates, couldn't resolve |

**Sorting:** Rows sorted by score descending.

**Ambiguous entries:** Show ⚠️ icon in the Confidence column. Clicking the row expands an inline sub-table showing all candidates with their individual affinity scores and Jaccard values.

#### Interaction

- **Click neighbor name** → navigate to that node's detail page (`#/nodes/{pubkey}`)
- **"Show on Map" button** (small, right-aligned per row) → highlights this neighbor on the node detail map with a line drawn between the two nodes showing the edge
- **Distance badge** — if both the current node and the neighbor have GPS coordinates, display a small badge showing the distance between them (e.g., `2.3 km`)

#### Condensed view (right panel in split view)

When a node is selected in the LEFT panel (node list), the RIGHT panel shows the condensed detail view. In condensed mode:

- Show **top 5 neighbors only** (by score descending)
- Display a `"View all N neighbors →"` link at the bottom that navigates to the full detail page
- Same table format as above, just truncated

#### Deep linking

Support `?section=node-neighbors` URL parameter to auto-scroll to the neighbors section when the page loads. This matches the existing pattern used by `?section=node-packets`.

#### Data fetching

- Call `GET /api/nodes/{pubkey}/neighbors` when the node detail view is rendered
- Show the spinner loading state while the request is in flight
- **Cache** the response for the lifetime of the detail view — do not re-fetch on tab switches within the same node
- On fetch error, display the error state message described above

### Analytics integration

Add a "Neighbor Graph" sub-tab in the Analytics section:
- Force-directed graph visualization using the `/api/analytics/neighbor-graph` endpoint
- Nodes colored by role (using existing `ROLE_COLORS`)
- Edge thickness proportional to `score`
- Collision groups highlighted (nodes sharing a prefix get matching border colors)
- Click a node to see its neighbors + disambiguation status

This is a later milestone — the API and "Show Neighbors" fix come first.

---

## Testing

### Unit tests for the algorithm

**Go tests** (`cmd/server/neighbor_test.go`):

```
TestBuildNeighborGraph_EmptyStore
    → empty graph, no edges

TestBuildNeighborGraph_AdvertSingleHopPath
    → ADVERT from_node=X, path=["A3"] → edge(X, A3-resolved) AND edge(observer, A3-resolved)

TestBuildNeighborGraph_AdvertMultiHopPath
    → ADVERT from_node=X, path=["A3","B7"] → edge(X, A3-resolved) AND edge(observer, B7-resolved)

TestBuildNeighborGraph_NonAdvertMultiHopPath
    → non-ADVERT from_node=X, path=["A3","B7"] → only edge(observer, B7-resolved), NO originator edge

TestBuildNeighborGraph_NonAdvertSingleHop
    → non-ADVERT with path=["A3"] → edge(observer, A3-resolved) only

TestBuildNeighborGraph_AdvertEmptyPath
    → ADVERT from_node=X, path=[] → edge(X, observer) directly (zero-hop)

TestBuildNeighborGraph_NonAdvertEmptyPath
    → non-ADVERT from_node=X, path=[] → no edges

TestBuildNeighborGraph_HashCollision
    → two nodes share prefix "A3" → edge marked ambiguous with both candidates

TestBuildNeighborGraph_AmbiguityResolution
    → node X has known neighbor Y; Y has known neighbor a3xx but not a3yy → disambiguation resolves to a3xx

TestBuildNeighborGraph_CountAccumulation
    → same edge observed 50 times → count=50, last_seen=latest

TestBuildNeighborGraph_MultipleObservers
    → same edge seen by obs1 and obs2 → observers=["obs1","obs2"]

TestBuildNeighborGraph_EqualMutualScores
    → two candidates for prefix "A3" have identical Jaccard scores and identical mutual-neighbor overlap → algorithm does NOT auto-resolve; returns both as ambiguous with their scores

TestBuildNeighborGraph_ObserverSelfEdgeGuard
    → observer's own hash prefix appears in path_json (due to a bug or routing loop) → algorithm never creates an edge from observer to itself; the self-referencing hop is skipped

TestBuildNeighborGraph_OrphanPrefix
    → path contains prefix "FF" matching zero known nodes → edge recorded with raw prefix as placeholder, marked unresolved=true, candidates=[]

TestAffinityScore_Fresh
    → count=100, last_seen=now → score ≈ 1.0

TestAffinityScore_Decayed
    → count=100, last_seen=7 days ago → score ≈ 0.5

TestAffinityScore_LowCount
    → count=5, last_seen=now → score ≈ 0.05

TestAffinityScore_StaleAndLow
    → count=5, last_seen=30 days ago → score ≈ 0.0
```

### API tests

```
TestNeighborAPI_ValidNode → 200, returns neighbor list
TestNeighborAPI_UnknownNode → 200, empty neighbors
TestNeighborAPI_MinCountFilter → only edges with count >= min_count
TestNeighborAPI_MinScoreFilter → only edges with score >= min_score
TestNeighborGraphAPI_RegionFilter → only edges from filtered observers
```

### Edge cases

| Case | Expected behavior |
|------|-------------------|
| ADVERT with empty path (direct reception) | Edge created: `originator ↔ observer` |
| Non-ADVERT with empty path | No edge created |
| Single-hop ADVERT | Both edge types resolve to same repeater; one edge created |
| Single-hop non-ADVERT | `observer ↔ path[0]` edge only |
| Hash collision on first hop | Edge marked ambiguous, candidates listed |
| Hash collision on last hop | Edge marked ambiguous, candidates listed |
| `hash_size == 0` node in path | Still processed (prefix matching works regardless of hash_size) |
| Stale data (node not seen in 30+ days) | Score decays to ~0; filtered out by `min_score` |
| Self-referencing path (`from_node` matches `path[0]`) | Skip — a node cannot be its own neighbor |
| Very long paths (10+ hops) | Extract first hop (ADVERTs only) and last hop (all types); ignore intermediate hops |
| Duplicate observations (same observer, same path, same timestamp) | Deduplicated by existing `PacketStore` logic |
| Non-ADVERT packet types (REQ, TXT_MSG, ACK, etc.) | Only `observer ↔ path[last]` edge extracted |
| Equal mutual-neighbor scores for two candidates | Do NOT auto-resolve; return both as ambiguous with scores |
| Observer's own prefix in path (self-edge) | Skip — never create edge from observer to itself |
| Orphan prefix (zero candidates from resolve_prefix) | Record edge with raw prefix, `unresolved: true`, empty candidates |

---

## Integration with Existing Disambiguation

The codebase currently has **three separate disambiguation mechanisms** that resolve ambiguous hash prefixes. The neighbor affinity graph must integrate with all three, serving as the highest-priority signal where sufficient data exists, while preserving existing heuristics as fallbacks.

### a. Frontend `map.js` — Geographic Centroid (lines ~342–368)

**Current behavior:** When drawing route lines on the map, `map.js` resolves hop prefixes to node positions. For collisions (multiple candidates match a prefix), it computes the geographic centroid of already-resolved hops and picks the candidate closest to that center:

```javascript
// Current: geo-centroid disambiguation
const cLat = knownPos.reduce((s, p) => s + p.lat, 0) / knownPos.length;
const cLon = knownPos.reduce((s, p) => s + p.lon, 0) / knownPos.length;
// ... picks candidate with minimum distance to (cLat, cLon)
```

**After #482:** Use affinity scores from the `/api/resolve-hops` response as the **primary** disambiguation signal. The enhanced response includes `affinityScore` per candidate and a `bestCandidate` field when confidence is high. Fall back to the geo-centroid heuristic only when:
- The affinity graph has no data for this prefix (cold start)
- No candidate meets the confidence threshold (sparse observations)
- The `bestCandidate` field is absent from the response

The geo-centroid heuristic is actually a reasonable fallback for route visualization — it produces visually plausible routes even without affinity data. Keep it as the secondary strategy.

### b. Backend `prefixMap.resolve()` — GPS Preference (`store.go`, lines ~3289–3305)

**Current behavior:** The `resolve()` method on `prefixMap` handles prefix-to-node resolution for all server-side analytics (hop distances, subpath computation, distance analytics). When multiple candidates match a prefix, it picks the first one with GPS coordinates. No intelligence, no context awareness — just "has GPS wins":

```go
// Current: naive GPS-preference disambiguation
for i := range candidates {
    if candidates[i].HasGPS {
        return &candidates[i]
    }
}
return &candidates[0]
```

This produces wrong answers on collisions. If two nodes share prefix `"C0"` and both have GPS, it returns whichever was indexed first — effectively random. This corrupts hop distance calculations, subpath analysis, and every other server-side analytic that resolves hops.

**After #482:** `resolve()` gains an **affinity-aware code path**. When the caller provides context (originator pubkey or observer pubkey), `resolve()` consults the neighbor graph for the best candidate:

1. Look up the context node's neighbors in the `NeighborGraph`
2. If a candidate appears as a known neighbor with sufficient confidence, return it
3. If no affinity data exists, fall back to the existing GPS-preference heuristic

This requires a new method signature — either `resolveWithContext(hop, contextPubkey)` or passing the `NeighborGraph` as a parameter. The existing `resolve(hop)` method remains as the no-context fallback for callers that lack originator/observer information.

**Impact:** Fixing `resolve()` fixes analytics accuracy across the board — hop distances, subpath computation, distance analytics, and any future feature that resolves hop prefixes server-side.

### c. API `handleResolveHops` — Candidate List (`routes.go`, lines ~1297–1346)

**Current behavior:** The `/api/resolve-hops` endpoint accepts a comma-separated list of hop prefixes, queries the database for matching nodes, and returns all candidates. When multiple candidates match, it sets `ambiguous: true` and returns the full candidate list. The client decides which candidate to use:

```go
// Current: returns all candidates, client decides
ambig := true
resolved[hop] = &HopResolution{
    Name: candidates[0].Name, Pubkey: candidates[0].Pubkey,
    Ambiguous: &ambig, Candidates: candidates,
}
```

**After #482:** Enhance the response with affinity data:

1. Add `affinityScore` (float, 0.0–1.0) to each `HopCandidate` in the response, computed from the neighbor graph using the `from_node`/`observer` context if provided in the request
2. Add `bestCandidate` field (string, pubkey) to the `HopResolution` when the top candidate's affinity score exceeds the confidence threshold (Jaccard ≥ 3× runner-up AND ≥ 3 observations)
3. Add `confidence` field (string: `unique_prefix` | `neighbor_affinity` | `geo_proximity` | `ambiguous`) indicating how the resolution was determined
4. The client can still override — `bestCandidate` is a recommendation, not a mandate

### Disambiguation Priority

When resolving an ambiguous prefix, apply these strategies in order (highest priority first):

| Priority | Strategy | Source | When it wins |
|----------|----------|--------|-------------|
| 1 | **Affinity graph score** | `NeighborGraph` | Score above confidence threshold (Jaccard ≥ 3× runner-up, ≥ 3 observations) |
| 2 | **Geographic proximity** | Geo-centroid of resolved hops | Affinity data insufficient; candidate positions available |
| 3 | **GPS preference** | Node has coordinates vs. doesn't | No affinity data, no geo context; at least one candidate has GPS |
| 4 | **First match** | Index order | No signal at all — current naive fallback (effectively random) |

The first strategy that produces a confident answer wins. Lower-priority strategies are only consulted when higher-priority ones lack data or confidence.

---

## Playwright E2E Tests

End-to-end tests using Playwright that verify the full user-visible behavior of the neighbor affinity feature. These tests run against a local server with the `test-fixtures/` SQLite database and exercise both the UI and the API surface.

**Test file:** `test-e2e-playwright.js` (extend existing Playwright test suite)

### a. Show Neighbors — Happy Path

```
Test: "Show Neighbors displays neighbor markers on map"

Steps:
  1. Navigate to the map page
  2. Click on a node marker with known neighbors (e.g., a repeater with stable topology)
  3. Click "Show Neighbors" in the node popup/side pane
  4. Assert: neighbor markers appear on the map (highlighted or differently styled)
  5. Assert: neighbor count badge/label matches the expected number of neighbors
  6. Assert: the selected node's marker is visually distinguished (reference node styling)

Validates: /api/nodes/{pubkey}/neighbors returns data, frontend renders it correctly
```

### b. Show Neighbors — Hash Collision Disambiguation

```
Test: "Show Neighbors resolves correct node on hash collision"

Setup:
  - Requires two nodes sharing the same 1-byte prefix in test fixtures
  - Node A (prefix "C0", pubkey c0dedad4...) has known neighbors R1, R2, R3
  - Node B (prefix "C0", pubkey c0f1a2b3...) has different neighbors R4, R5

Steps:
  1. Navigate to Node A's detail page (by full pubkey, not prefix)
  2. Click "Show Neighbors"
  3. Assert: R1, R2, R3 appear as neighbors on the map
  4. Assert: R4, R5 do NOT appear (they are Node B's neighbors)
  5. Navigate to Node B's detail page
  6. Click "Show Neighbors"
  7. Assert: R4, R5 appear as neighbors
  8. Assert: R1, R2, R3 do NOT appear

This is THE critical test — if this passes, #484 is fixed. The affinity graph
correctly disambiguates which "C0" node the path hops refer to.

Validates: Server-side disambiguation via neighbor affinity, not client-side prefix matching
```

### c. Neighbor API Response Shape

```
Test: "Neighbor API returns correct response structure"

Steps:
  1. GET /api/nodes/{known_pubkey}/neighbors
  2. Assert: response has "node" field matching the requested pubkey
  3. Assert: response has "neighbors" array
  4. Assert: each neighbor has fields: pubkey, prefix, name, role, count, score,
     first_seen, last_seen, avg_snr, observers, ambiguous
  5. Assert: "total_observations" is a positive integer
  6. For neighbors with ambiguous=true:
     a. Assert: "candidates" array is present and non-empty
     b. Assert: each candidate has "pubkey", "name", "role"
     c. Assert: each candidate has "affinityScore" (number or null)

Validates: API contract matches spec, affinityScore field is present on candidates
```

### d. Neighbor Graph — Empty Path (Zero-Hop ADVERT)

```
Test: "Zero-hop ADVERT shows observer as neighbor"

Setup:
  - Test fixtures contain a node X that has ADVERTs with empty path (path_json="[]"),
    received directly by observer O with no repeaters

Steps:
  1. GET /api/nodes/{X_pubkey}/neighbors
  2. Assert: response includes observer O in the neighbors array
  3. Assert: the edge between X and O has ambiguous=false (both pubkeys are fully known)

Validates: Empty path handling — direct ADVERT creates originator↔observer edge
```

### e. Resolve Hops with Affinity Scores

```
Test: "Resolve hops returns affinity scores for colliding prefixes"

Setup:
  - Two nodes share prefix "C0" in test fixtures

Steps:
  1. GET /api/resolve-hops?hops=C0&from_node={known_originator}&observer={known_observer}
  2. Assert: response resolved["C0"] has candidates array
  3. Assert: each candidate has "affinityScore" field (number or null)
  4. Assert: when confidence threshold is met, "bestCandidate" field is present
     with a valid pubkey
  5. Assert: "confidence" field is one of: "unique_prefix", "neighbor_affinity",
     "ambiguous"

Validates: Enhanced /api/resolve-hops response with affinity data
```

### f. Route Visualization with Disambiguation

```
Test: "Route line uses affinity-resolved positions for colliding hops"

Steps:
  1. Navigate to packets page
  2. Find a multi-hop packet where at least one hop prefix has a hash collision
  3. Open packet detail
  4. Click "Show Route" on the map
  5. Assert: route polyline is drawn on the map
  6. Assert: route passes through the affinity-resolved candidate's position,
     not the other collision candidate's position
  7. Verify by checking the polyline's latlng coordinates against the expected
     node positions

Validates: Frontend uses affinity scores (from enhanced /api/resolve-hops) instead
of falling back to geo-centroid for route disambiguation
```

### g. Cold Start Graceful Degradation

```
Test: "Empty packet store returns graceful results, not errors"

Setup:
  - Start server with empty/fresh SQLite database (no packets ingested)

Steps:
  1. GET /api/nodes/{any_pubkey}/neighbors
     - Assert: 200 status (not 500)
     - Assert: response has "neighbors": [] (empty array)
     - Assert: response has "total_observations": 0

  2. GET /api/resolve-hops?hops=C0
     - Assert: 200 status
     - Assert: resolved["C0"] uses existing behavior (GPS preference fallback)
     - Assert: no "affinityScore" or "bestCandidate" fields (no affinity data)
     - Assert: candidates array still populated from node registry

  3. GET /api/analytics/neighbor-graph
     - Assert: 200 status
     - Assert: "edges": [] (empty)
     - Assert: "stats.total_edges": 0

Validates: All affinity endpoints degrade gracefully on cold start — no crashes,
no misleading data, existing functionality preserved
```

---

## Observability & Debugging

A probabilistic graph algorithm is only as useful as its debuggability. Without clear visibility into edge weights, resolution decisions, and fallback paths, the affinity system becomes an opaque black box that's impossible to diagnose when it gets a disambiguation wrong. This section specifies the tooling needed to inspect, debug, and monitor the neighbor affinity graph in development and production.

### Debug API Endpoint — `/api/debug/affinity`

A dedicated admin endpoint that exposes the full internal state of the affinity graph for programmatic inspection.

**Response includes:**

- **Full graph state** — all edges with weights, observation counts, and last-seen timestamps
- **Per-prefix resolution log** — disambiguation decisions with full reasoning:
  ```
  prefix "C0DE" → chose c0dedad4... (score 47, Jaccard 0.82, confidence HIGH)
                   over  c0dedad9... (score 3, Jaccard 0.11, confidence LOW)
  ```
- **Scoring details** — Jaccard similarity scores, raw observation counts, which confidence threshold was applied, and whether the result was auto-resolved or flagged as ambiguous

**Authentication:** Protected by API key, using the same auth mechanism as other admin endpoints.

**Query parameters:**

| Parameter | Description | Example |
|-----------|-------------|---------|
| `prefix`  | Filter to a specific hex prefix | `?prefix=C0DE` |
| `node`    | Filter to a specific node's edges | `?node=<full_pubkey>` |

**Example response shape:**

```json
{
  "edges": [
    {
      "nodeA": "c0dedad4...",
      "nodeB": "a1b2c3d4...",
      "weight": 47,
      "observationCount": 52,
      "lastSeen": "2026-04-02T18:30:00Z",
      "jaccard": 0.82
    }
  ],
  "resolutions": [
    {
      "prefix": "C0DE",
      "chosen": "c0dedad4...",
      "chosenScore": 47,
      "chosenJaccard": 0.82,
      "confidence": "HIGH",
      "candidates": [
        { "pubkey": "c0dedad9...", "score": 3, "jaccard": 0.11, "confidence": "LOW" }
      ],
      "ratio": 15.7,
      "thresholdApplied": 3.0,
      "method": "auto-resolved"
    }
  ],
  "stats": {
    "totalEdges": 342,
    "totalResolutions": 128,
    "ambiguousCount": 7
  }
}
```

### Debug Overlay on Map

A toggle-able map layer (developer/admin tool) that visualizes affinity edges directly on the Leaflet map.

**Controls:**

- Checkbox in the map controls panel: "Show Affinity Edges" (unchecked by default, not shown to non-admin users)
- When enabled, draws lines between nodes that share affinity edges

**Visual encoding:**

| Property | Encoding |
|----------|----------|
| Line thickness | Proportional to affinity weight (normalized to 1–5px range) |
| Line opacity | Proportional to affinity weight (0.2–1.0 range) |
| Line color — green | High confidence (Jaccard ≥ 0.6, ratio ≥ 3×) |
| Line color — yellow | Medium confidence (Jaccard 0.3–0.6 or ratio 1.5–3×) |
| Line color — red | Low confidence / ambiguous (Jaccard < 0.3 or ratio < 1.5×) |

**Interactions:**

- **Unresolved prefixes** — shown as question mark (`?`) markers at estimated positions (or at the position of known candidate nodes)
- **Click an edge** — popup showing: observation count, last seen timestamp, Jaccard score, and list of contributing observers (which nodes reported seeing both endpoints)

**Implementation notes:**

- Uses a dedicated Leaflet layer group (`L.layerGroup`) so it can be toggled without affecting other map layers
- Edge data fetched from `/api/debug/affinity` on toggle-on, cached until manual refresh or page reload
- This is a developer/admin tool — the checkbox is hidden unless a `debugAffinity` config flag is enabled or the user has admin privileges

### Per-Node Debug Panel

On the node detail page, an expandable "Affinity Debug" section provides node-specific graph inspection.

**Display (collapsed by default):**

- **Neighbor edges table** — all edges involving this node, with columns: neighbor pubkey/name, affinity score, Jaccard similarity, observation count, last seen, confidence level
- **Prefix resolutions** — all disambiguation decisions where this node was either the chosen result or a rejected candidate
- **"Why was X chosen over Y?"** — explicit reasoning trace for each resolution:
  - Raw affinity scores for all candidates
  - Jaccard similarity values
  - Threshold comparison (ratio vs. configured threshold)
  - Fallback path taken: affinity → geo-distance → GPS → naive (which step resolved it, and why earlier steps didn't)
- **Edge timeline** — observation count over time for each neighbor edge, showing whether edges are growing (active relationship), stable (established neighbor), or decaying (stale/departed node)

**Data source:** Fetched from `/api/debug/affinity?node=<pubkey>` — no additional API endpoint needed.

### Server-Side Structured Logging

Structured log lines for every affinity resolution decision, enabling post-hoc debugging from server logs.

**Log format** (follows existing server log conventions):

```
[affinity] resolve C0DE: c0dedad4 score=47 Jaccard=0.82 vs c0dedad9 score=3 Jaccard=0.11 → auto-resolved (ratio 15.7×, threshold 3×)
```

**Event types:**

| Event | Log pattern |
|-------|------------|
| Auto-resolve (clear winner) | `[affinity] resolve <PREFIX>: <chosen> score=<N> Jaccard=<F> vs <rejected> score=<N> Jaccard=<F> → auto-resolved (ratio <N>×, threshold <N>×)` |
| Fallback (no affinity data) | `[affinity] resolve <PREFIX>: no affinity data, falling back to geo-distance` |
| Ambiguous (scores too close) | `[affinity] resolve <PREFIX>: scores too close (<N> vs <N>, ratio <F>×) → ambiguous, returning <N> candidates` |

**Gating:** All affinity logging is controlled by a configuration flag to avoid log spam in production:

- CLI flag: `--debug-affinity`
- Config file: `debugAffinity: true`
- When disabled (default), no affinity log lines are emitted
- When enabled, all resolution decisions are logged at INFO level

### Dashboard Stats Widget

A summary widget added to the existing dashboard/stats page, providing at-a-glance health metrics for the affinity graph.

**Metrics displayed:**

| Metric | Description |
|--------|-------------|
| Total edges | Number of edges in the affinity graph |
| Resolved prefixes | Count and percentage of prefixes that resolved unambiguously |
| Ambiguous prefixes | Count and percentage of prefixes where scores were too close to auto-resolve |
| Average confidence | Mean confidence score across all resolutions (weighted by observation count) |
| Cold-start coverage | Percentage of active 1-byte prefixes that have ≥3 observations (minimum threshold for auto-resolve) |
| Graph age | Timestamps of the oldest and newest edges, showing the observation window |

**Implementation notes:**

- Data sourced from `/api/debug/affinity` stats summary (or a lightweight `/api/analytics/neighbor-graph` stats sub-object if the debug endpoint is too heavy for the dashboard)
- Widget updates on page load and on manual refresh — no polling needed given the slow rate of graph change
- Displayed alongside existing analytics widgets (RF stats, topology stats, etc.)

---

## What's NOT in scope

- **Full mesh topology visualization** — this spec covers first-hop neighbors only, not multi-hop routing topology
- **Multi-hop path analysis beyond endpoints** — extracting `path[1]` ↔ `path[2]` relationships is a natural extension but adds complexity (both endpoints are prefixes, not full pubkeys). Defer to a future issue
- **Directional edges** — v1 uses undirected edges. Directional edges (capturing RF asymmetry) are a future enhancement for topology visualization
- **Real-time graph updates via WebSocket** — the graph is cached and served via REST. WebSocket push for graph changes is unnecessary given the slow rate of topology change
- **Persistent storage in SQLite** — initial implementation is in-memory only. A `node_neighbors` table can be added later if the in-memory window is insufficient
- **Geographic clustering** — while the `neighbor-graph` API response includes a `stats` field, actual geographic cluster detection (e.g., community detection algorithms) is deferred
- **Automatic hop rewriting** — the system provides disambiguation data; it does not retroactively rewrite stored `path_json` values

---

## Milestones

Each milestone is a self-contained unit that can be implemented, polished (pr-polish), tested, and merged before moving to the next.

| Milestone | What | Fixes | Dependencies |
|-----------|------|-------|-------------|
| 1 | Graph builder core | — | None |
| 2 | Neighbors API | — | M1 |
| 3 | Show Neighbors fix | #484 | M2 |
| 4 | Enhanced hop resolution | — | M2 |
| 5 | Node detail neighbors | — | M2 |
| 6 | Debugging tools | — | M2 + M5 |
| 7 | Analytics graph viz | — | M2 |

> **Note:** Milestones 3, 4, 5 can run in parallel after M2 merges.

---

### Milestone 1: Graph Builder Core

**Scope:** `neighbor_graph.go` — the `NeighborGraph` data structure and `BuildFromStore()` algorithm

- Extract edges from packet store: `originator ↔ path[0]` for ADVERTs, `observer ↔ path[last]` for all packets
- Affinity scoring with time decay
- Jaccard normalization for disambiguation
- Confidence threshold (3× ratio, ≥3 observations)
- Transitivity poisoning guard
- Cache management (60s TTL, rebuild from store)

**Tests:** `neighbor_graph_test.go` — all unit tests from the spec: single-hop, multi-hop, zero-hop, collision candidates, Jaccard scoring, confidence thresholds, equal scores → ambiguous, observer self-edge guard, orphan prefixes, time decay

**Files:** `cmd/server/neighbor_graph.go`, `cmd/server/neighbor_graph_test.go`

**User-visible:** Nothing yet (backend only)

**Dependencies:** None

**PR title:** `feat: neighbor affinity graph builder (#482) — milestone 1`

---

### Milestone 2: Neighbors API

**Scope:** Two new API endpoints

- `GET /api/nodes/{pubkey}/neighbors` — per-node neighbor list with scores
- `GET /api/analytics/neighbor-graph` — full graph summary with stats
- Wire graph into server startup and cache refresh

**Tests:** API-level tests (Go httptest), Playwright test (c) from spec — API response shape validation

**Files:** `cmd/server/routes.go`, `cmd/server/routes_test.go` (or `cmd/server/neighbor_api_test.go`)

**User-visible:** API available (can be tested via curl/browser dev tools)

**Dependencies:** Milestone 1 merged

**PR title:** `feat: neighbor affinity API endpoints (#482) — milestone 2`

---

### Milestone 3: Show Neighbors Fix (#484)

**Scope:** Replace broken client-side path walking in `map.js` with server-side `/neighbors` API call

- Replace `selectReferenceNode()` path-walking code
- Use affinity-resolved neighbors from API
- Fall back to geo-centroid for unresolved prefixes

**Tests:** Playwright tests (a) happy path and (b) hash collision disambiguation — THE critical test that proves #484 is fixed

**Files:** `public/map.js`

**User-visible:** "Show Neighbors" works correctly even with hash collisions

**Dependencies:** Milestone 2 merged

**PR title:** `fix: Show Neighbors uses affinity API for collision disambiguation (#484) — milestone 3`

---

### Milestone 4: Enhanced Hop Resolution

**Scope:** Upgrade `prefixMap.resolve()` and `handleResolveHops` with affinity awareness

- `resolveWithContext()` in store.go — affinity first, geo fallback, GPS fallback
- `handleResolveHops` adds `affinityScore` and `bestCandidate` to response
- 4-tier disambiguation priority

**Tests:** Go unit tests for `resolveWithContext`, Playwright test (e) resolve-hops with affinity scores, test (f) route visualization

**Files:** `cmd/server/store.go`, `cmd/server/routes.go`

**User-visible:** All hop resolution across the app benefits from affinity (analytics, route display, subpaths)

**Dependencies:** Milestone 2 merged

**PR title:** `feat: affinity-aware hop resolution (#482) — milestone 4`

---

### Milestone 5: Node Detail Neighbors Section

**Scope:** Add "Neighbors" section to the node detail page

- Placed between "Heard By" and "Paths" sections
- Table with Name, Role, Score, Observations, Last Seen, Confidence columns
- Confidence indicators (🟢🟡🔴⚠️)
- Click-to-navigate, "Show on Map" per row
- Condensed view (top 5) in right panel, full view on detail page
- Deep link support (`?section=node-neighbors`)
- Empty state, loading state, error state

**Tests:** Playwright tests — verify section appears, table has correct columns, click navigation works, empty state shows message

**Files:** `public/nodes.js`

**User-visible:** Every node detail page shows its neighbors

**Dependencies:** Milestone 2 merged

**PR title:** `feat: neighbors section in node detail page (#482) — milestone 5`

---

### Milestone 6: Observability & Debugging

**Scope:** Debug tools for the affinity system

- `/api/debug/affinity` endpoint with graph state dump, per-prefix resolution log
- Debug overlay on map (toggle-able layer showing affinity edges)
- Per-node debug panel (expandable "Affinity Debug" in node detail)
- Structured server-side logging (gated by `debugAffinity` config)
- Dashboard stats widget

**Tests:** API test for debug endpoint, Playwright test for debug overlay toggle

**Files:** `cmd/server/routes.go`, `public/map.js`, `public/nodes.js`

**User-visible:** Admins can diagnose disambiguation decisions

**Dependencies:** Milestones 2 + 5 merged

**PR title:** `feat: affinity debugging tools (#482) — milestone 6`

---

### Milestone 7: Analytics Graph Visualization (Future)

**Scope:** Force-directed neighbor graph in the Analytics section

- Interactive graph visualization using canvas or SVG
- Node coloring by role, edge thickness by score
- Click node to navigate to detail page
- Filter by region, role, confidence level

**Tests:** Playwright — graph renders, nodes clickable, filter works

**Files:** `public/analytics.js` (or new file)

**User-visible:** Visual network topology in Analytics

**Dependencies:** Milestone 2 merged

**PR title:** `feat: neighbor graph visualization (#482) — milestone 7`
