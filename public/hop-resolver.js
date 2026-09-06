/**
 * Client-side hop resolver — eliminates /api/resolve-hops HTTP requests.
 * Mirrors the server's disambiguateHops() logic from server.js.
 */
window.HopResolver = (function() {
  'use strict';

  const MAX_HOP_DIST = 1.8; // ~200km in degrees
  const REGION_RADIUS_KM = 300;

  // Only repeaters and room servers can appear as path hops per protocol.
  // Companions/sensors originate but never relay packets.
  function canAppearInPath(role) {
    if (!role) return false;
    var r = String(role).toLowerCase();
    return r.indexOf('repeater') >= 0 || r.indexOf('room_server') >= 0 || r === 'room';
  }
  let prefixIdx = {};   // lowercase hex prefix → [node, ...]
  let pubkeyIdx = {};   // full lowercase pubkey → node (O(1) lookup)
  let nodesList = [];
  let observerIataMap = {}; // observer_id → iata
  let iataCoords = {};  // iata → {lat, lon}
  let affinityMap = {}; // pubkey → { neighborPubkey → score }

  function dist(lat1, lon1, lat2, lon2) {
    return Math.sqrt((lat1 - lat2) ** 2 + (lon1 - lon2) ** 2);
  }

  function haversineKm(lat1, lon1, lat2, lon2) {
    const R = 6371;
    const dLat = (lat2 - lat1) * Math.PI / 180;
    const dLon = (lon2 - lon1) * Math.PI / 180;
    const a = Math.sin(dLat / 2) ** 2 +
      Math.cos(lat1 * Math.PI / 180) * Math.cos(lat2 * Math.PI / 180) *
      Math.sin(dLon / 2) ** 2;
    return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
  }

  /**
   * Initialize (or rebuild) the prefix index from the full nodes list.
   * @param {Array} nodes - Array of {public_key, name, lat, lon, ...}
   * @param {Object} [opts] - Optional: { observers: [{id, iata}], iataCoords: {code: {lat,lon}} }
   */
  function init(nodes, opts) {
    nodesList = nodes || [];
    prefixIdx = {};
    pubkeyIdx = {};
    for (const n of nodesList) {
      if (!n.public_key) continue;
      const pk = n.public_key.toLowerCase();
      // pubkeyIdx includes ALL nodes — used by resolveFromServer for
      // server-confirmed full-pubkey lookups (any node type).
      pubkeyIdx[pk] = n;
      // prefixIdx only includes nodes that can appear as path hops.
      if (!canAppearInPath(n.role)) continue;
      for (let len = 1; len <= 3; len++) {
        const p = pk.slice(0, len * 2);
        if (!prefixIdx[p]) prefixIdx[p] = [];
        prefixIdx[p].push(n);
      }
    }
    // Store observer IATA mapping and coords if provided
    observerIataMap = {};
    if (opts && opts.observers) {
      for (const o of opts.observers) {
        if (o.id && o.iata) observerIataMap[o.id] = o.iata;
      }
    }
    iataCoords = (opts && opts.iataCoords) || (window.IATA_COORDS_GEO) || {};
  }

  /**
   * Check if a node is near an IATA region center.
   * Returns { near, method, distKm } or null.
   */
  function nodeInRegion(candidate, iata) {
    const center = iataCoords[iata];
    if (!center) return null;
    if (candidate.lat && candidate.lon && !(candidate.lat === 0 && candidate.lon === 0)) {
      const d = haversineKm(candidate.lat, candidate.lon, center.lat, center.lon);
      return { near: d <= REGION_RADIUS_KM, method: 'geo', distKm: Math.round(d) };
    }
    return null; // no GPS — can't geo-filter client-side
  }

  /**
   * Pick the best candidate by scoring against BOTH prev and next resolved hops.
   *
   * Strategy (in priority order):
   * 1. Neighbor-graph edge weight: sum of edge scores to prevPubkey + nextPubkey. Pick max.
   * 2. Geographic centroid: if no candidate has graph edges, compute centroid of
   *    prev+next positions and pick closest candidate by haversine distance.
   * 3. Single-anchor geo fallback: if only one neighbor is resolved, use it as anchor.
   * 4. Original heuristic: first candidate (when no context at all).
   *
   * @param {Array} candidates - candidates with lat/lon/pubkey/name
   * @param {string|null} prevPubkey - pubkey of previous resolved hop
   * @param {string|null} nextPubkey - pubkey of next resolved hop
   * @param {Object|null} prevPos - {lat, lon} of previous resolved hop or origin
   * @param {Object|null} nextPos - {lat, lon} of next resolved hop or observer
   * @returns {Object} best candidate
   */
  function pickByAffinity(candidates, prevPubkey, nextPubkey, prevPos, nextPos) {
    const hasGraph = Object.keys(affinityMap).length > 0;
    const hasAdj = prevPubkey || nextPubkey;

    // Strategy 1: neighbor-graph edge weights (sum of prev + next)
    if (hasGraph && hasAdj) {
      const scored = candidates.map(function(c) {
        let s = 0;
        if (prevPubkey) s += getAffinity(prevPubkey, c.pubkey);
        if (nextPubkey) s += getAffinity(nextPubkey, c.pubkey);
        return { candidate: c, edgeScore: s };
      });
      const withEdges = scored.filter(function(s) { return s.edgeScore > 0; });
      if (withEdges.length > 0) {
        withEdges.sort(function(a, b) { return b.edgeScore - a.edgeScore; });
        _traceMultiCandidate(candidates, scored, withEdges[0].candidate, 'graph');
        return withEdges[0].candidate;
      }
    }

    // Strategy 2/3: geographic — centroid of prev+next, or single anchor
    let anchorLat = null, anchorLon = null, anchorCount = 0;
    if (prevPos && prevPos.lat != null && prevPos.lon != null) {
      anchorLat = (anchorLat || 0) + prevPos.lat;
      anchorLon = (anchorLon || 0) + prevPos.lon;
      anchorCount++;
    }
    if (nextPos && nextPos.lat != null && nextPos.lon != null) {
      anchorLat = (anchorLat || 0) + nextPos.lat;
      anchorLon = (anchorLon || 0) + nextPos.lon;
      anchorCount++;
    }
    if (anchorCount > 0) {
      anchorLat /= anchorCount;
      anchorLon /= anchorCount;
      const geoScored = candidates.map(function(c) {
        const d = (c.lat != null && c.lon != null && !(c.lat === 0 && c.lon === 0))
          ? haversineKm(c.lat, c.lon, anchorLat, anchorLon) : 999999;
        return { candidate: c, distKm: d };
      });
      geoScored.sort(function(a, b) { return a.distKm - b.distKm; });
      _traceMultiCandidate(candidates, geoScored, geoScored[0].candidate, 'centroid');
      return geoScored[0].candidate;
    }

    // Strategy 4: no context — return first candidate
    _traceMultiCandidate(candidates, null, candidates[0], 'fallback');
    return candidates[0];
  }

  /** Dev-mode console trace for multi-candidate picks */
  function _traceMultiCandidate(candidates, scored, chosen, method) {
    if (typeof console === 'undefined' || !console.debug) return;
    if (candidates.length < 2) return;
    try {
      const prefix = candidates[0].pubkey ? candidates[0].pubkey.slice(0, 2) : '??';
      const scoreSummary = scored ? scored.map(function(s) {
        const pk = (s.candidate || s).pubkey || '?';
        const val = s.edgeScore != null ? s.edgeScore : (s.distKm != null ? s.distKm + 'km' : '?');
        return pk.slice(0, 8) + ':' + val;
      }) : [];
      console.debug('[hop-resolver] hash=' + prefix + ' candidates=' + candidates.length +
        ' scored=[' + scoreSummary.join(',') + '] chose=' + (chosen.pubkey || '?').slice(0, 8) +
        ' method=' + method);
    } catch(e) { /* trace is best-effort */ }
  }

  /**
   * Resolve an array of hex hop prefixes to node info.
   * Returns a map: { hop: {name, pubkey, lat, lon, ambiguous, unreliable} }
   *
   * @param {string[]} hops - Hex prefixes
   * @param {number|null} originLat - Sender latitude (forward anchor)
   * @param {number|null} originLon - Sender longitude (forward anchor)
   * @param {number|null} observerLat - Observer latitude (backward anchor)
   * @param {number|null} observerLon - Observer longitude (backward anchor)
   * @returns {Object} resolved map keyed by hop prefix
   */
  function resolve(hops, originLat, originLon, observerLat, observerLon, observerId) {
    if (!hops || !hops.length) return {};

    // Determine observer's IATA for regional filtering
    const packetIata = observerId ? observerIataMap[observerId] : null;

    const resolved = {};
    const hopPositions = {};

    // First pass: find candidates with regional filtering
    for (const hop of hops) {
      const h = hop.toLowerCase();
      const allCandidates = prefixIdx[h] || [];
      if (allCandidates.length === 0) {
        resolved[hop] = { name: null, candidates: [], conflicts: [] };
      } else if (allCandidates.length === 1) {
        const c = allCandidates[0];
        const regionCheck = packetIata ? nodeInRegion(c, packetIata) : null;
        resolved[hop] = { name: c.name, pubkey: c.public_key,
          candidates: [{ name: c.name, pubkey: c.public_key, lat: c.lat, lon: c.lon, regional: regionCheck ? regionCheck.near : false, filterMethod: regionCheck ? regionCheck.method : 'none', distKm: regionCheck ? regionCheck.distKm : undefined }],
          conflicts: [] };
      } else {
        // Multiple candidates — apply geo regional filtering
        const checked = allCandidates.map(c => {
          const r = packetIata ? nodeInRegion(c, packetIata) : null;
          return { ...c, regional: r ? r.near : false, filterMethod: r ? r.method : 'none', distKm: r ? r.distKm : undefined };
        });
        const regional = checked.filter(c => c.regional);
        regional.sort((a, b) => (a.distKm || 9999) - (b.distKm || 9999));
        const candidates = regional.length > 0 ? regional : checked;
        const globalFallback = regional.length === 0 && checked.length > 0 && packetIata != null;

        const conflicts = candidates.map(c => ({
          name: c.name, pubkey: c.public_key, lat: c.lat, lon: c.lon,
          regional: c.regional, filterMethod: c.filterMethod, distKm: c.distKm
        }));

        if (candidates.length === 1) {
          resolved[hop] = { name: candidates[0].name, pubkey: candidates[0].public_key,
            candidates: conflicts, conflicts, globalFallback };
        } else {
          resolved[hop] = { name: candidates[0].name, pubkey: candidates[0].public_key,
            ambiguous: true, candidates: conflicts, conflicts, globalFallback,
            hopBytes: Math.ceil(hop.length / 2), totalGlobal: allCandidates.length, totalRegional: regional.length };
        }
      }
    }

    // Build initial positions for unambiguous hops
    for (const hop of hops) {
      const r = resolved[hop];
      if (r && !r.ambiguous && r.pubkey) {
        const node = nodesList.find(n => n.public_key === r.pubkey);
        if (node && node.lat && node.lon && !(node.lat === 0 && node.lon === 0)) {
          hopPositions[hop] = { lat: node.lat, lon: node.lon };
        }
      }
    }

    // Combined disambiguation: resolve ambiguous hops using both neighbors.
    // We iterate until no more hops can be resolved (handles cascading dependencies).
    const originPos = (originLat != null && originLon != null) ? { lat: originLat, lon: originLon } : null;
    const observerPos = (observerLat != null && observerLon != null) ? { lat: observerLat, lon: observerLon } : null;

    let changed = true;
    let maxIter = hops.length + 1; // prevent infinite loops
    while (changed && maxIter-- > 0) {
      changed = false;
      for (let i = 0; i < hops.length; i++) {
        const hop = hops[i];
        if (hopPositions[hop]) continue; // already resolved
        const r = resolved[hop];
        if (!r || !r.ambiguous) continue;
        const withLoc = r.candidates.filter(c => c.lat != null && c.lon != null && !(c.lat === 0 && c.lon === 0));
        if (!withLoc.length) continue;

        // Find prev resolved neighbor
        let prevPubkey = null, prevPos = null;
        for (let j = i - 1; j >= 0; j--) {
          if (hopPositions[hops[j]]) {
            prevPos = hopPositions[hops[j]];
            prevPubkey = resolved[hops[j]] ? resolved[hops[j]].pubkey : null;
            break;
          }
        }
        if (!prevPos && originPos) prevPos = originPos;

        // Find next resolved neighbor
        let nextPubkey = null, nextPos = null;
        for (let j = i + 1; j < hops.length; j++) {
          if (hopPositions[hops[j]]) {
            nextPos = hopPositions[hops[j]];
            nextPubkey = resolved[hops[j]] ? resolved[hops[j]].pubkey : null;
            break;
          }
        }
        if (!nextPos && observerPos) nextPos = observerPos;

        // Skip if we have zero context (wait for a later iteration or neighbor resolution)
        if (!prevPubkey && !nextPubkey && !prevPos && !nextPos) continue;

        const picked = pickByAffinity(withLoc, prevPubkey, nextPubkey, prevPos, nextPos);
        r.name = picked.name;
        r.pubkey = picked.pubkey;
        hopPositions[hop] = { lat: picked.lat, lon: picked.lon };
        changed = true;
      }
    }

    // Sanity check: drop hops impossibly far from neighbors
    for (let i = 0; i < hops.length; i++) {
      const pos = hopPositions[hops[i]];
      if (!pos) continue;
      const prev = i > 0 ? hopPositions[hops[i - 1]] : null;
      const next = i < hops.length - 1 ? hopPositions[hops[i + 1]] : null;
      if (!prev && !next) continue;
      const dPrev = prev ? dist(pos.lat, pos.lon, prev.lat, prev.lon) : 0;
      const dNext = next ? dist(pos.lat, pos.lon, next.lat, next.lon) : 0;
      const tooFarPrev = prev && dPrev > MAX_HOP_DIST;
      const tooFarNext = next && dNext > MAX_HOP_DIST;
      if ((tooFarPrev && tooFarNext) || (tooFarPrev && !next) || (tooFarNext && !prev)) {
        const r = resolved[hops[i]];
        if (r) r.unreliable = true;
        delete hopPositions[hops[i]];
      }
    }

    return resolved;
  }

  /**
   * Check if the resolver has been initialized with nodes.
   */
  function ready() {
    return nodesList.length > 0;
  }

  /**
   * Load neighbor-graph affinity data.
   * @param {Object} graph - { edges: [{source, target, score, weight}, ...] }
   */
  function setAffinity(graph) {
    affinityMap = {};
    if (!graph || !graph.edges) return;
    for (const e of graph.edges) {
      if (!affinityMap[e.source]) affinityMap[e.source] = {};
      affinityMap[e.source][e.target] = e.score || e.weight || 1;
      if (!affinityMap[e.target]) affinityMap[e.target] = {};
      affinityMap[e.target][e.source] = e.score || e.weight || 1;
    }
  }

  /**
   * Get the affinity score between two pubkeys (0 if not neighbors).
   */
  function getAffinity(pubkeyA, pubkeyB) {
    if (!pubkeyA || !pubkeyB || !affinityMap[pubkeyA]) return 0;
    return affinityMap[pubkeyA][pubkeyB] || 0;
  }

  // #1864: O(1) node-name lookup by FULL pubkey (64-char hex). Returns the
  // node name when a node with this exact public key is known, else null.
  // Used to resolve ANON_REQ source pubkeys to a display name.
  function nameForKey(pubkey) {
    if (!pubkey) return null;
    const n = pubkeyIdx[String(pubkey).toLowerCase()];
    return n && n.name ? n.name : null;
  }

  /**
   * Resolve hops using server-provided resolved_path (full pubkeys).
   * Returns the same format as resolve() — { [hop]: { name, pubkey, ... } }.
   * resolved_path is an array aligned with path_json: each element is a
   * 64-char lowercase hex pubkey or null. Skips entries that are null.
   */
  function resolveFromServer(hops, resolvedPath) {
    if (!hops || !resolvedPath || hops.length !== resolvedPath.length) return {};
    const result = {};
    for (let i = 0; i < hops.length; i++) {
      const hop = hops[i];
      const pubkey = resolvedPath[i];
      if (!pubkey) continue; // null = unresolved, leave for client-side fallback
      // O(1) lookup via pubkeyIdx built during init()
      const node = pubkeyIdx[pubkey.toLowerCase()] || null;
      result[hop] = {
        name: node ? node.name : pubkey.slice(0, 8),
        pubkey: pubkey,
        candidates: node ? [{ name: node.name, pubkey: pubkey, lat: node.lat, lon: node.lon }] : [],
        conflicts: []
      };
    }
    return result;
  }

  return { init: init, resolve: resolve, resolveFromServer: resolveFromServer, ready: ready, haversineKm: haversineKm, setAffinity: setAffinity, getAffinity: getAffinity, nameForKey: nameForKey };
})();
