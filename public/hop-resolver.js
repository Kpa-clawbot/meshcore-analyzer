/**
 * Client-side hop resolver — eliminates /api/resolve-hops HTTP requests.
 * Mirrors the server's disambiguateHops() logic from server.js.
 */
window.HopResolver = (function() {
  'use strict';

  const MAX_HOP_DIST = 1.8; // ~200km in degrees
  const REGION_RADIUS_KM = 300;
  let prefixIdx = {};   // lowercase hex prefix → [node, ...]
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
    for (const n of nodesList) {
      if (!n.public_key) continue;
      const pk = n.public_key.toLowerCase();
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
   * Pick the best candidate using affinity first, then geo-distance fallback.
   * @param {Array} candidates - candidates with lat/lon/pubkey/name
   * @param {string|null} adjacentPubkey - pubkey of the previously/next resolved hop
   * @param {Object|null} anchor - {lat, lon} for geo fallback
   * @param {number|null} fallbackLat - fallback anchor lat (e.g. observer)
   * @param {number|null} fallbackLon - fallback anchor lon
   * @returns {Object} best candidate
   */
  function pickByAffinity(candidates, adjacentPubkey, anchor, fallbackLat, fallbackLon) {
    // If we have affinity data and an adjacent hop, prefer neighbors
    if (adjacentPubkey && Object.keys(affinityMap).length > 0) {
      const withAffinity = candidates
        .map(c => ({ ...c, affinity: getAffinity(adjacentPubkey, c.pubkey) }))
        .filter(c => c.affinity > 0);
      if (withAffinity.length > 0) {
        withAffinity.sort((a, b) => b.affinity - a.affinity);
        return withAffinity[0];
      }
    }
    // Fallback: geo-distance sort (existing behavior)
    const effectiveAnchor = anchor || (fallbackLat != null ? { lat: fallbackLat, lon: fallbackLon } : null);
    if (effectiveAnchor) {
      candidates.sort((a, b) => dist(a.lat, a.lon, effectiveAnchor.lat, effectiveAnchor.lon) - dist(b.lat, b.lon, effectiveAnchor.lat, effectiveAnchor.lon));
    }
    return candidates[0];
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

    // Forward pass
    let lastPos = (originLat != null && originLon != null) ? { lat: originLat, lon: originLon } : null;
    let lastResolvedPubkey = null;
    for (let i = 0; i < hops.length; i++) {
      const hop = hops[i];
      if (hopPositions[hop]) {
        lastPos = hopPositions[hop];
        lastResolvedPubkey = resolved[hop] ? resolved[hop].pubkey : null;
        continue;
      }
      const r = resolved[hop];
      if (!r || !r.ambiguous) continue;
      const withLoc = r.candidates.filter(c => c.lat && c.lon && !(c.lat === 0 && c.lon === 0));
      if (!withLoc.length) continue;

      // Affinity-aware: prefer candidates that are neighbors of the previous hop
      const picked = pickByAffinity(withLoc, lastResolvedPubkey, lastPos, i === hops.length - 1 ? observerLat : null, i === hops.length - 1 ? observerLon : null);
      r.name = picked.name;
      r.pubkey = picked.pubkey;
      hopPositions[hop] = { lat: picked.lat, lon: picked.lon };
      lastPos = hopPositions[hop];
      lastResolvedPubkey = picked.pubkey;
    }

    // Backward pass
    let nextPos = (observerLat != null && observerLon != null) ? { lat: observerLat, lon: observerLon } : null;
    let nextResolvedPubkey = null;
    for (let i = hops.length - 1; i >= 0; i--) {
      const hop = hops[i];
      if (hopPositions[hop]) {
        nextPos = hopPositions[hop];
        nextResolvedPubkey = resolved[hop] ? resolved[hop].pubkey : null;
        continue;
      }
      const r = resolved[hop];
      if (!r || !r.ambiguous) continue;
      const withLoc = r.candidates.filter(c => c.lat && c.lon && !(c.lat === 0 && c.lon === 0));
      if (!withLoc.length || !nextPos) continue;

      // Affinity-aware: prefer candidates that are neighbors of the next hop
      const picked = pickByAffinity(withLoc, nextResolvedPubkey, nextPos, null, null);
      r.name = picked.name;
      r.pubkey = picked.pubkey;
      hopPositions[hop] = { lat: picked.lat, lon: picked.lon };
      nextPos = hopPositions[hop];
      nextResolvedPubkey = picked.pubkey;
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

  /**
   * Resolve hops using server-provided resolved_path (full pubkeys).
   * Returns the same format as resolve() — { [hop]: { name, pubkey, ... } }.
   * resolved_path is an array aligned with path_json: each element is a
   * 64-char lowercase hex pubkey or null. Skips entries that are null.
   */
  function resolveFromServer(hops, resolvedPath) {
    if (!hops || !resolvedPath || hops.length !== resolvedPath.length) return {};
    var result = {};
    for (var i = 0; i < hops.length; i++) {
      var hop = hops[i];
      var pubkey = resolvedPath[i];
      if (!pubkey) continue; // null = unresolved, leave for client-side fallback
      // Find the node by pubkey to get its name
      var node = null;
      for (var j = 0; j < nodesList.length; j++) {
        if (nodesList[j].public_key === pubkey) { node = nodesList[j]; break; }
      }
      result[hop] = {
        name: node ? node.name : pubkey.slice(0, 8),
        pubkey: pubkey,
        candidates: node ? [{ name: node.name, pubkey: pubkey, lat: node.lat, lon: node.lon }] : [],
        conflicts: []
      };
    }
    return result;
  }

  return { init: init, resolve: resolve, resolveFromServer: resolveFromServer, ready: ready, haversineKm: haversineKm, setAffinity: setAffinity, getAffinity: getAffinity };
})();
