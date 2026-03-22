// IATA airport coordinates for regional node filtering
// Used by resolve-hops to determine if a node is geographically near an observer's region
const IATA_COORDS = {
  // US West Coast
  SJC: { lat: 37.3626, lon: -121.9290 },
  SFO: { lat: 37.6213, lon: -122.3790 },
  OAK: { lat: 37.7213, lon: -122.2208 },
  SEA: { lat: 47.4502, lon: -122.3088 },
  PDX: { lat: 45.5898, lon: -122.5951 },
  LAX: { lat: 33.9425, lon: -118.4081 },
  SAN: { lat: 32.7338, lon: -117.1933 },
  SMF: { lat: 38.6954, lon: -121.5908 },
  MRY: { lat: 36.5870, lon: -121.8430 },
  EUG: { lat: 44.1246, lon: -123.2119 },
  RDD: { lat: 40.5090, lon: -122.2934 },
  MFR: { lat: 42.3742, lon: -122.8735 },
  FAT: { lat: 36.7762, lon: -119.7181 },
  SBA: { lat: 34.4262, lon: -119.8405 },
  RNO: { lat: 39.4991, lon: -119.7681 },
  BOI: { lat: 43.5644, lon: -116.2228 },
  LAS: { lat: 36.0840, lon: -115.1537 },
  PHX: { lat: 33.4373, lon: -112.0078 },
  SLC: { lat: 40.7884, lon: -111.9778 },
  // US Mountain/Central
  DEN: { lat: 39.8561, lon: -104.6737 },
  DFW: { lat: 32.8998, lon: -97.0403 },
  IAH: { lat: 29.9844, lon: -95.3414 },
  AUS: { lat: 30.1975, lon: -97.6664 },
  MSP: { lat: 44.8848, lon: -93.2223 },
  // US East Coast
  ATL: { lat: 33.6407, lon: -84.4277 },
  ORD: { lat: 41.9742, lon: -87.9073 },
  JFK: { lat: 40.6413, lon: -73.7781 },
  EWR: { lat: 40.6895, lon: -74.1745 },
  BOS: { lat: 42.3656, lon: -71.0096 },
  MIA: { lat: 25.7959, lon: -80.2870 },
  IAD: { lat: 38.9531, lon: -77.4565 },
  CLT: { lat: 35.2144, lon: -80.9473 },
  DTW: { lat: 42.2124, lon: -83.3534 },
  MCO: { lat: 28.4312, lon: -81.3081 },
  BNA: { lat: 36.1263, lon: -86.6774 },
  RDU: { lat: 35.8801, lon: -78.7880 },
  // Canada
  YVR: { lat: 49.1967, lon: -123.1815 },
  YYZ: { lat: 43.6777, lon: -79.6248 },
  YYC: { lat: 51.1215, lon: -114.0076 },
  YEG: { lat: 53.3097, lon: -113.5800 },
  YOW: { lat: 45.3225, lon: -75.6692 },
  // Europe
  LHR: { lat: 51.4700, lon: -0.4543 },
  CDG: { lat: 49.0097, lon: 2.5479 },
  FRA: { lat: 50.0379, lon: 8.5622 },
  AMS: { lat: 52.3105, lon: 4.7683 },
  MUC: { lat: 48.3537, lon: 11.7750 },
  SOF: { lat: 42.6952, lon: 23.4062 },
  // Asia/Pacific
  NRT: { lat: 35.7720, lon: 140.3929 },
  HND: { lat: 35.5494, lon: 139.7798 },
  ICN: { lat: 37.4602, lon: 126.4407 },
  SYD: { lat: -33.9461, lon: 151.1772 },
  MEL: { lat: -37.6690, lon: 144.8410 },
};

// Haversine distance in km
function haversineKm(lat1, lon1, lat2, lon2) {
  const R = 6371;
  const dLat = (lat2 - lat1) * Math.PI / 180;
  const dLon = (lon2 - lon1) * Math.PI / 180;
  const a = Math.sin(dLat / 2) ** 2 +
    Math.cos(lat1 * Math.PI / 180) * Math.cos(lat2 * Math.PI / 180) *
    Math.sin(dLon / 2) ** 2;
  return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
}

// Default radius for "near region" — LoRa max realistic range ~300km
const DEFAULT_REGION_RADIUS_KM = 300;

/**
 * Check if a node is geographically within radius of an IATA region center.
 * Returns { near: boolean, distKm: number } or null if can't determine.
 */
function nodeNearRegion(nodeLat, nodeLon, iata, radiusKm = DEFAULT_REGION_RADIUS_KM) {
  const center = IATA_COORDS[iata];
  if (!center) return null;
  if (nodeLat == null || nodeLon == null || (nodeLat === 0 && nodeLon === 0)) return null;
  const distKm = haversineKm(nodeLat, nodeLon, center.lat, center.lon);
  return { near: distKm <= radiusKm, distKm: Math.round(distKm) };
}

module.exports = { IATA_COORDS, haversineKm, nodeNearRegion, DEFAULT_REGION_RADIUS_KM };
