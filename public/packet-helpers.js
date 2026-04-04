/* === CoreScope — packet-helpers.js (shared packet utilities) === */
'use strict';

/**
 * Cached JSON.parse helpers for packet data (issue #387).
 * Avoids repeated parsing of path_json / decoded_json on the same packet object.
 * Results are cached as _parsedPath / _parsedDecoded properties on the packet.
 *
 * Handles pre-parsed objects (non-string values) gracefully — returns them as-is.
 */

window.getParsedPath = function getParsedPath(p) {
  if (p._parsedPath !== undefined) return p._parsedPath || [];
  var raw = p.path_json;
  if (typeof raw !== 'string') {
    p._parsedPath = Array.isArray(raw) ? raw : [];
    return p._parsedPath;
  }
  try { p._parsedPath = JSON.parse(raw) || []; } catch (e) { p._parsedPath = []; }
  return p._parsedPath;
};

/**
 * Clear cached _parsedPath/_parsedDecoded from a packet object.
 * Must be called after spreading a parent packet into an observation/child,
 * otherwise the child inherits stale cached values from the parent (issue #504).
 */
window.clearParsedCache = function clearParsedCache(p) {
  delete p._parsedPath;
  delete p._parsedDecoded;
  delete p._parsedResolvedPath;
  return p;
};

/**
 * Parse resolved_path (server-side resolved full pubkeys).
 * Returns array of pubkey strings (or null entries) if present, or null if absent.
 * Cached as _parsedResolvedPath on the packet object.
 */
window.getResolvedPath = function getResolvedPath(p) {
  if (p._parsedResolvedPath !== undefined) return p._parsedResolvedPath;
  var raw = p.resolved_path;
  if (!raw) { p._parsedResolvedPath = null; return null; }
  if (typeof raw !== 'string') {
    p._parsedResolvedPath = Array.isArray(raw) ? raw : null;
    return p._parsedResolvedPath;
  }
  try { p._parsedResolvedPath = JSON.parse(raw) || null; } catch (e) { p._parsedResolvedPath = null; }
  return p._parsedResolvedPath;
};

window.getParsedDecoded = function getParsedDecoded(p) {
  if (p._parsedDecoded !== undefined) return p._parsedDecoded || {};
  var raw = p.decoded_json;
  if (typeof raw !== 'string') {
    p._parsedDecoded = (raw && typeof raw === 'object') ? raw : {};
    return p._parsedDecoded;
  }
  try { p._parsedDecoded = JSON.parse(raw) || {}; } catch (e) { p._parsedDecoded = {}; }
  return p._parsedDecoded;
};
