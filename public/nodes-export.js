/* === CoreScope — nodes-export.js (MeshCore companion contacts export) === */
'use strict';

/*
 * Exports the visible node list as a MeshCore companion-app config file:
 * { "contacts": [ … ] }, the same shape the companion app itself writes, so the
 * result can be imported there directly. Kept out of nodes.js so the feature
 * stays a self-contained unit.
 */

(function () {
  var ROLE_TYPES = { repeater: 2, companion: 1, room: 3, sensor: 4 };
  var UNKNOWN_TYPE = 1;

  function epochSeconds(ts) {
    if (!ts) return 0;
    var ms = new Date(ts).getTime();
    return isNaN(ms) ? 0 : Math.floor(ms / 1000);
  }

  function coord(v) {
    if (v === null || v === undefined || v === '') return null;
    var n = Number(v);
    return isNaN(n) ? null : n;
  }

  // Returns the companion contact for a node, or null when the node cannot be
  // represented (no name, truncated pubkey, or no usable position).
  function contactFor(n) {
    if (!n) return null;
    if (typeof n.name !== 'string' || !n.name.trim()) return null;
    if (typeof n.public_key !== 'string' || n.public_key.length < 64) return null;
    var lat = coord(n.lat);
    var lon = coord(n.lon);
    if (lat === null || lon === null) return null;
    if (lat === 0 && lon === 0) return null;

    var advert = epochSeconds(n.last_seen);
    return {
      type: ROLE_TYPES[String(n.role || '').toLowerCase()] || UNKNOWN_TYPE,
      name: n.name,
      custom_name: null,
      public_key: n.public_key,
      flags: 0,
      latitude: String(lat),
      longitude: String(lon),
      last_advert: advert,
      last_modified: advert,
      out_path_list: null,
    };
  }

  function buildContacts(nodes) {
    var contacts = [];
    (nodes || []).forEach(function (n) {
      var c = contactFor(n);
      if (c) contacts.push(c);
    });
    return { contacts: contacts };
  }

  function pad2(v) { return v < 10 ? '0' + v : String(v); }

  function filename(areaKey, date) {
    var d = date || new Date();
    var stamp = d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) +
      '-' + pad2(d.getHours()) + pad2(d.getMinutes()) + pad2(d.getSeconds());
    var area = String(areaKey || '').replace(/[^A-Za-z0-9_-]+/g, '_').replace(/^_+|_+$/g, '');
    return 'corescope_nodes_' + (area || 'all') + '_' + stamp + '.json';
  }

  // Triggers the browser download. Returns the number of exported contacts.
  function download(nodes, areaKey) {
    var payload = buildContacts(nodes);
    var blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json;charset=utf-8' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = filename(areaKey, new Date());
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(function () { URL.revokeObjectURL(url); }, 0);
    return payload.contacts.length;
  }

  window.NodesExport = {
    buildContacts: buildContacts,
    filename: filename,
    download: download,
  };
})();
