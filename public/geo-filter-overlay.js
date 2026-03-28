// Shared helper — initialises the geo-filter polygon overlay on a Leaflet map.
// Returns the L.layerGroup (or null if no filter is configured / fetch fails).
// The returned layer is added to the map when the checkbox is toggled on, and
// removed when toggled off.  The toggle state is persisted in localStorage
// under the key 'meshcore-map-geo-filter'.
//
// Parameters:
//   map        – Leaflet map instance
//   checkboxId – id of the <input type="checkbox"> that controls visibility
//   labelId    – id of the <label> wrapper to reveal once data is loaded
async function initGeoFilterOverlay(map, checkboxId, labelId) {
  try {
    const gf = await api('/config/geo-filter', { ttl: 3600 });
    if (!gf || !gf.polygon || gf.polygon.length < 3) return null;

    const latlngs = gf.polygon.map(function (p) { return [p[0], p[1]]; });
    const innerPoly = L.polygon(latlngs, {
      color: '#3b82f6', weight: 2, opacity: 0.8,
      fillColor: '#3b82f6', fillOpacity: 0.08
    });

    const bufferPoly = gf.bufferKm > 0 ? (function () {
      let cLat = 0, cLon = 0;
      gf.polygon.forEach(function (p) { cLat += p[0]; cLon += p[1]; });
      cLat /= gf.polygon.length; cLon /= gf.polygon.length;
      const cosLat = Math.cos(cLat * Math.PI / 180);
      const outer = gf.polygon.map(function (p) {
        const dLatM = (p[0] - cLat) * 111000;
        const dLonM = (p[1] - cLon) * 111000 * cosLat;
        const dist = Math.sqrt(dLatM * dLatM + dLonM * dLonM);
        if (dist === 0) return [p[0], p[1]];
        const scale = (gf.bufferKm * 1000) / dist;
        return [p[0] + dLatM * scale / 111000, p[1] + dLonM * scale / (111000 * cosLat)];
      });
      return L.polygon(outer, {
        color: '#3b82f6', weight: 1.5, opacity: 0.4, dashArray: '6 4',
        fillColor: '#3b82f6', fillOpacity: 0.04
      });
    })() : null;

    const layer = L.layerGroup(bufferPoly ? [bufferPoly, innerPoly] : [innerPoly]);

    const label = document.getElementById(labelId);
    if (label) label.style.display = '';
    const el = document.getElementById(checkboxId);
    if (el) {
      const saved = localStorage.getItem('meshcore-map-geo-filter');
      if (saved === 'true') { el.checked = true; layer.addTo(map); }
      el.addEventListener('change', function (e) {
        localStorage.setItem('meshcore-map-geo-filter', e.target.checked);
        if (e.target.checked) { layer.addTo(map); } else { map.removeLayer(layer); }
      });
    }
    return layer;
  } catch (e) { return null; }
}
