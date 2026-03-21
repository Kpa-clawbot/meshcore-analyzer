'use strict';

/**
 * In-memory packet store — loads transmissions + observations from SQLite on startup,
 * serves reads from RAM, writes to both RAM + SQLite.
 * M3: Restructured around transmissions (deduped by hash) with observations.
 * Caps memory at configurable limit (default 1GB).
 */
class PacketStore {
  constructor(dbModule, config = {}) {
    this.dbModule = dbModule;  // The full db module (has .db, .insertPacket, .getPacket)
    this.db = dbModule.db;     // Raw better-sqlite3 instance for queries
    this.maxBytes = (config.maxMemoryMB || 1024) * 1024 * 1024;
    this.estPacketBytes = config.estimatedPacketBytes || 450;
    this.maxPackets = Math.floor(this.maxBytes / this.estPacketBytes);

    // SQLite-only mode: skip RAM loading, all reads go to DB
    this.sqliteOnly = process.env.NO_MEMORY_STORE === '1';

    // Primary storage: transmissions sorted by first_seen DESC (newest first)
    // Each transmission looks like a packet for backward compat
    this.packets = [];

    // Indexes
    this.byId = new Map();           // observation_id → observation object (backward compat for packet detail links)
    this.byTxId = new Map();         // transmission_id → transmission object
    this.byHash = new Map();         // hash → transmission object (1:1)
    this.byObserver = new Map();     // observer_id → [observation objects]
    this.byNode = new Map();         // pubkey → [transmission objects] (deduped)

    // Track which hashes are indexed per node pubkey (avoid dupes in byNode)
    this._nodeHashIndex = new Map(); // pubkey → Set<hash>
    this._advertByObserver = new Map(); // pubkey → Set<observer_id> (ADVERT-only, for region filtering)

    this.loaded = false;
    this.stats = { totalLoaded: 0, totalObservations: 0, evicted: 0, inserts: 0, queries: 0 };
  }

  /** Load all packets from SQLite into memory */
  load() {
    if (this.sqliteOnly) {
      console.log('[PacketStore] SQLite-only mode (NO_MEMORY_STORE=1) — all reads go to database');
      this.loaded = true;
      return this;
    }

    const t0 = Date.now();

    // Check if normalized schema exists
    const hasTransmissions = this.db.prepare(
      "SELECT name FROM sqlite_master WHERE type='table' AND name='transmissions'"
    ).get();

    if (hasTransmissions) {
      this._loadNormalized();
    } else {
      this._loadLegacy();
    }

    this.stats.totalLoaded = this.packets.length;
    this.loaded = true;
    const elapsed = Date.now() - t0;
    console.log(`[PacketStore] Loaded ${this.packets.length} transmissions (${this.stats.totalObservations} observations) in ${elapsed}ms (${Math.round(this.packets.length * this.estPacketBytes / 1024 / 1024)}MB est)`);
    return this;
  }

  /** Load from normalized transmissions + observations tables */
  _loadNormalized() {
    const rows = this.db.prepare(`
      SELECT t.id AS transmission_id, t.raw_hex, t.hash, t.first_seen, t.route_type,
             t.payload_type, t.payload_version, t.decoded_json,
             o.id AS observation_id, o.observer_id, o.observer_name, o.direction,
             o.snr, o.rssi, o.score, o.path_json, o.timestamp AS obs_timestamp
      FROM transmissions t
      LEFT JOIN observations o ON o.transmission_id = t.id
      ORDER BY t.first_seen DESC, o.timestamp DESC
    `).all();

    for (const row of rows) {
      if (this.packets.length >= this.maxPackets && !this.byHash.has(row.hash)) break;

      let tx = this.byHash.get(row.hash);
      if (!tx) {
        tx = {
          id: row.transmission_id,
          raw_hex: row.raw_hex,
          hash: row.hash,
          first_seen: row.first_seen,
          timestamp: row.first_seen,
          route_type: row.route_type,
          payload_type: row.payload_type,
          decoded_json: row.decoded_json,
          observations: [],
          observation_count: 0,
          // Filled from first observation for backward compat
          observer_id: null,
          observer_name: null,
          snr: null,
          rssi: null,
          path_json: null,
          direction: null,
        };
        this.byHash.set(row.hash, tx);
        this.byHash.set(row.hash, tx);
        this.packets.push(tx);
        this.byTxId.set(tx.id, tx);
        this._indexByNode(tx);
      }

      if (row.observation_id != null) {
        const obs = {
          id: row.observation_id,
          observer_id: row.observer_id,
          observer_name: row.observer_name,
          direction: row.direction,
          snr: row.snr,
          rssi: row.rssi,
          score: row.score,
          path_json: row.path_json,
          timestamp: row.obs_timestamp,
          // Carry transmission fields for backward compat
          hash: row.hash,
          raw_hex: row.raw_hex,
          payload_type: row.payload_type,
          decoded_json: row.decoded_json,
          route_type: row.route_type,
        };

        // Dedup: skip if same observer + same path already loaded
        const isDupeLoad = tx.observations.some(o => o.observer_id === obs.observer_id && (o.path_json || '') === (obs.path_json || ''));
        if (isDupeLoad) continue;

        tx.observations.push(obs);
        tx.observation_count++;

        // Fill first observation data into transmission for backward compat
        if (tx.observer_id == null && obs.observer_id) {
          tx.observer_id = obs.observer_id;
          tx.observer_name = obs.observer_name;
          tx.snr = obs.snr;
          tx.rssi = obs.rssi;
          tx.path_json = obs.path_json;
          tx.direction = obs.direction;
        }

        // byId maps observation IDs for packet detail links
        this.byId.set(obs.id, obs);

        // byObserver
        if (obs.observer_id) {
          if (!this.byObserver.has(obs.observer_id)) this.byObserver.set(obs.observer_id, []);
          this.byObserver.get(obs.observer_id).push(obs);
        }

        this.stats.totalObservations++;
      }
    }

    // Post-load: build ADVERT-by-observer index (needs all observations loaded first)
    for (const tx of this.packets) {
      if (tx.payload_type === 4 && tx.decoded_json) {
        try {
          const d = JSON.parse(tx.decoded_json);
          if (d.pubKey) this._indexAdvertObservers(d.pubKey, tx);
        } catch {}
      }
    }
    console.log(`[PacketStore] ADVERT observer index: ${this._advertByObserver.size} nodes tracked`);
  }

  /** Fallback: load from legacy packets table */
  _loadLegacy() {
    const rows = this.db.prepare(
      'SELECT * FROM packets_v ORDER BY timestamp DESC'
    ).all();

    for (const row of rows) {
      if (this.packets.length >= this.maxPackets) break;
      this._indexLegacy(row);
    }
  }

  /** Index a legacy packet row (old flat structure) — builds transmission + observation */
  _indexLegacy(pkt) {
    let tx = this.byHash.get(pkt.hash);
    if (!tx) {
      tx = {
        id: pkt.id,
        raw_hex: pkt.raw_hex,
        hash: pkt.hash,
        first_seen: pkt.timestamp,
        timestamp: pkt.timestamp,
        route_type: pkt.route_type,
        payload_type: pkt.payload_type,
        decoded_json: pkt.decoded_json,
        observations: [],
        observation_count: 0,
        observer_id: pkt.observer_id,
        observer_name: pkt.observer_name,
        snr: pkt.snr,
        rssi: pkt.rssi,
        path_json: pkt.path_json,
        direction: pkt.direction,
      };
      this.byHash.set(pkt.hash, tx);
      this.byHash.set(pkt.hash, tx);
      this.packets.push(tx);
        this.byTxId.set(tx.id, tx);
      this._indexByNode(tx);
    }

    if (pkt.timestamp < tx.first_seen) {
      tx.first_seen = pkt.timestamp;
      tx.timestamp = pkt.timestamp;
      tx.observer_id = pkt.observer_id;
      tx.observer_name = pkt.observer_name;
      tx.path_json = pkt.path_json;
    }

    const obs = {
      id: pkt.id,
      observer_id: pkt.observer_id,
      observer_name: pkt.observer_name,
      direction: pkt.direction,
      snr: pkt.snr,
      rssi: pkt.rssi,
      score: pkt.score,
      path_json: pkt.path_json,
      timestamp: pkt.timestamp,
      hash: pkt.hash,
      raw_hex: pkt.raw_hex,
      payload_type: pkt.payload_type,
      decoded_json: pkt.decoded_json,
      route_type: pkt.route_type,
    };
    // Dedup: skip if same observer + same path already recorded for this transmission
    const isDupe = tx.observations.some(o => o.observer_id === obs.observer_id && (o.path_json || '') === (obs.path_json || ''));
    if (isDupe) return tx;

    tx.observations.push(obs);
    tx.observation_count++;

    this.byId.set(pkt.id, obs);

    if (pkt.observer_id) {
      if (!this.byObserver.has(pkt.observer_id)) this.byObserver.set(pkt.observer_id, []);
      this.byObserver.get(pkt.observer_id).push(obs);
    }

    this.stats.totalObservations++;
  }

  /** Extract node pubkeys from decoded_json and index transmission in byNode */
  _indexByNode(tx) {
    if (!tx.decoded_json) return;
    try {
      const decoded = JSON.parse(tx.decoded_json);
      const keys = new Set();
      if (decoded.pubKey) keys.add(decoded.pubKey);
      if (decoded.destPubKey) keys.add(decoded.destPubKey);
      if (decoded.srcPubKey) keys.add(decoded.srcPubKey);
      for (const k of keys) {
        if (!this._nodeHashIndex.has(k)) this._nodeHashIndex.set(k, new Set());
        if (this._nodeHashIndex.get(k).has(tx.hash)) continue;
        this._nodeHashIndex.get(k).add(tx.hash);
        if (!this.byNode.has(k)) this.byNode.set(k, []);
        this.byNode.get(k).push(tx);
      }
    } catch {}
  }

  /** Track which observers saw an ADVERT from a given pubkey */
  _indexAdvertObservers(pubkey, tx) {
    if (!this._advertByObserver.has(pubkey)) this._advertByObserver.set(pubkey, new Set());
    const s = this._advertByObserver.get(pubkey);
    for (const obs of tx.observations) {
      if (obs.observer_id) s.add(obs.observer_id);
    }
  }

  /** Get node pubkeys whose ADVERTs were seen by any of the given observer IDs */
  getNodesByAdvertObservers(observerIds) {
    const result = new Set();
    for (const [pubkey, observers] of this._advertByObserver) {
      for (const obsId of observerIds) {
        if (observers.has(obsId)) { result.add(pubkey); break; }
      }
    }
    return result;
  }

  /** Remove oldest transmissions when over memory limit */
  _evict() {
    while (this.packets.length > this.maxPackets) {
      const old = this.packets.pop();
      this.byHash.delete(old.hash);
      this.byHash.delete(old.hash);
      this.byTxId.delete(old.id);
      // Remove observations from byId and byObserver
      for (const obs of old.observations) {
        this.byId.delete(obs.id);
        if (obs.observer_id && this.byObserver.has(obs.observer_id)) {
          const arr = this.byObserver.get(obs.observer_id).filter(o => o.id !== obs.id);
          if (arr.length) this.byObserver.set(obs.observer_id, arr); else this.byObserver.delete(obs.observer_id);
        }
      }
      // Skip node index cleanup (expensive, low value)
      this.stats.evicted++;
    }
  }

  /** Insert a new packet (to both memory and SQLite) */
  insert(packetData) {
    const id = this.dbModule.insertPacket(packetData);
    // Also write to normalized tables and get the transmission ID
    const txResult = this.dbModule.insertTransmission ? this.dbModule.insertTransmission(packetData) : null;
    const transmissionId = txResult ? txResult.transmissionId : null;
    const observationId = txResult ? txResult.observationId : id;

    // Build row directly from packetData — avoids view ID mismatch issues
    const row = {
      id: observationId,
      raw_hex: packetData.raw_hex,
      hash: packetData.hash,
      timestamp: packetData.timestamp,
      route_type: packetData.route_type,
      payload_type: packetData.payload_type,
      payload_version: packetData.payload_version,
      decoded_json: packetData.decoded_json,
      observer_id: packetData.observer_id,
      observer_name: packetData.observer_name,
      snr: packetData.snr,
      rssi: packetData.rssi,
      path_json: packetData.path_json,
      direction: packetData.direction,
    };
    if (!this.sqliteOnly) {
      // Update or create transmission in memory
      let tx = this.byHash.get(row.hash);
      if (!tx) {
        tx = {
          id: transmissionId || row.id,
          raw_hex: row.raw_hex,
          hash: row.hash,
          first_seen: row.timestamp,
          timestamp: row.timestamp,
          route_type: row.route_type,
          payload_type: row.payload_type,
          decoded_json: row.decoded_json,
          observations: [],
          observation_count: 0,
          observer_id: row.observer_id,
          observer_name: row.observer_name,
          snr: row.snr,
          rssi: row.rssi,
          path_json: row.path_json,
          direction: row.direction,
        };
        this.byHash.set(row.hash, tx);
        this.byHash.set(row.hash, tx);
        this.packets.unshift(tx); // newest first
        this.byTxId.set(tx.id, tx);
        this._indexByNode(tx);
      } else {
        // Update first_seen if earlier — also update observer + path to match
        if (row.timestamp < tx.first_seen) {
          tx.first_seen = row.timestamp;
          tx.timestamp = row.timestamp;
          tx.observer_id = row.observer_id;
          tx.observer_name = row.observer_name;
          tx.path_json = row.path_json;
        }
      }

      // Add observation
      const obs = {
        id: row.id,
        observer_id: row.observer_id,
        observer_name: row.observer_name,
        direction: row.direction,
        snr: row.snr,
        rssi: row.rssi,
        score: row.score,
        path_json: row.path_json,
        timestamp: row.timestamp,
        hash: row.hash,
        raw_hex: row.raw_hex,
        payload_type: row.payload_type,
        decoded_json: row.decoded_json,
        route_type: row.route_type,
      };
      // Dedup: skip if same observer + same path already recorded for this transmission
      const isDupe = tx.observations.some(o => o.observer_id === obs.observer_id && (o.path_json || '') === (obs.path_json || ''));
      if (!isDupe) {
        tx.observations.push(obs);
        tx.observation_count++;
      }

      // Update transmission's display fields if this is first observation
      if (tx.observations.length === 1) {
        tx.observer_id = obs.observer_id;
        tx.observer_name = obs.observer_name;
        tx.snr = obs.snr;
        tx.rssi = obs.rssi;
        tx.path_json = obs.path_json;
      }

      this.byId.set(obs.id, obs);
      if (obs.observer_id) {
        if (!this.byObserver.has(obs.observer_id)) this.byObserver.set(obs.observer_id, []);
        this.byObserver.get(obs.observer_id).push(obs);
      }

      this.stats.totalObservations++;

      // Update ADVERT observer index for live ingestion
      if (tx.payload_type === 4 && obs.observer_id && tx.decoded_json) {
        try {
          const d = JSON.parse(tx.decoded_json);
          if (d.pubKey) {
            if (!this._advertByObserver.has(d.pubKey)) this._advertByObserver.set(d.pubKey, new Set());
            this._advertByObserver.get(d.pubKey).add(obs.observer_id);
          }
        } catch {}
      }

      this._evict();
      this.stats.inserts++;
    }
    return id;
  }

  /**
   * Find ALL packets referencing a node — by pubkey index + name + pubkey text search.
   * Returns unique transmissions (deduped).
   * @param {string} nodeIdOrName - pubkey or friendly name
   * @param {Array} [fromPackets] - packet array to filter (defaults to this.packets)
   * @returns {{ packets: Array, pubkey: string, nodeName: string }}
   */
  findPacketsForNode(nodeIdOrName, fromPackets) {
    let pubkey = nodeIdOrName;
    let nodeName = nodeIdOrName;

    // Always resolve to get both pubkey and name
    try {
      const row = this.db.prepare("SELECT public_key, name FROM nodes WHERE public_key = ? OR name = ? LIMIT 1").get(nodeIdOrName, nodeIdOrName);
      if (row) { pubkey = row.public_key; nodeName = row.name || nodeIdOrName; }
    } catch {}

    // Combine: index hits + text search
    const indexed = this.byNode.get(pubkey);
    const hashSet = indexed ? new Set(indexed.map(t => t.hash)) : new Set();
    const source = fromPackets || this.packets;
    const packets = source.filter(t =>
      hashSet.has(t.hash) ||
      (t.decoded_json && (t.decoded_json.includes(nodeName) || t.decoded_json.includes(pubkey)))
    );

    return { packets, pubkey, nodeName };
  }

  /** Count transmissions and observations for a node */
  countForNode(pubkey) {
    const txs = this.byNode.get(pubkey) || [];
    let observations = 0;
    for (const tx of txs) observations += tx.observation_count;
    return { transmissions: txs.length, observations };
  }

  /** Query packets with filters — all from memory (or SQLite in fallback mode) */
  query({ limit = 50, offset = 0, type, route, region, observer, hash, since, until, node, order = 'DESC' } = {}) {
    this.stats.queries++;

    if (this.sqliteOnly) return this._querySQLite({ limit, offset, type, route, region, observer, hash, since, until, node, order });

    let results = this.packets;

    // Use indexes for single-key filters when possible
    if (hash && !type && !route && !region && !observer && !since && !until && !node) {
      const tx = this.byHash.get(hash);
      results = tx ? [tx] : [];
    } else if (observer && !type && !route && !region && !hash && !since && !until && !node) {
      // For observer filter, find unique transmissions where any observation matches
      results = this._transmissionsForObserver(observer);
    } else if (node && !type && !route && !region && !observer && !hash && !since && !until) {
      results = this.findPacketsForNode(node).packets;
    } else {
      // Apply filters sequentially
      if (type !== undefined) {
        const t = Number(type);
        results = results.filter(p => p.payload_type === t);
      }
      if (route !== undefined) {
        const r = Number(route);
        results = results.filter(p => p.route_type === r);
      }
      if (observer) results = this._transmissionsForObserver(observer, results);
      if (hash) {
        const h = hash.toLowerCase();
        const tx = this.byHash.get(h);
        results = tx ? results.filter(p => p.hash === h) : [];
      }
      if (since) results = results.filter(p => p.timestamp > since);
      if (until) results = results.filter(p => p.timestamp < until);
      if (region) {
        const regionObservers = new Set();
        try {
          const obs = this.db.prepare('SELECT id FROM observers WHERE iata = ?').all(region);
          obs.forEach(o => regionObservers.add(o.id));
        } catch {}
        results = results.filter(p =>
          p.observations.some(o => regionObservers.has(o.observer_id))
        );
      }
      if (node) {
        results = this.findPacketsForNode(node, results).packets;
      }
    }

    const total = results.length;

    // Sort
    if (order === 'ASC') {
      results = results.slice().sort((a, b) => {
        if (a.timestamp < b.timestamp) return -1;
        if (a.timestamp > b.timestamp) return 1;
        return 0;
      });
    }
    // Default DESC — packets array is already sorted newest-first

    // Paginate
    const paginated = results.slice(Number(offset), Number(offset) + Number(limit));
    return { packets: paginated, total };
  }

  /** Find unique transmissions that have at least one observation from given observer */
  _transmissionsForObserver(observerId, fromTransmissions) {
    if (fromTransmissions) {
      return fromTransmissions.filter(tx =>
        tx.observations.some(o => o.observer_id === observerId)
      );
    }
    // Use byObserver index: get observations, then unique transmissions
    const obs = this.byObserver.get(observerId) || [];
    const seen = new Set();
    const result = [];
    for (const o of obs) {
      if (!seen.has(o.hash)) {
        seen.add(o.hash);
        const tx = this.byHash.get(o.hash);
        if (tx) result.push(tx);
      }
    }
    return result;
  }

  /** Query with groupByHash — now trivial since packets ARE transmissions */
  queryGrouped({ limit = 50, offset = 0, type, route, region, observer, hash, since, until, node } = {}) {
    this.stats.queries++;

    if (this.sqliteOnly) return this._queryGroupedSQLite({ limit, offset, type, route, region, observer, hash, since, until, node });

    // Get filtered transmissions
    const { packets: filtered, total: filteredTotal } = this.query({
      limit: 999999, offset: 0, type, route, region, observer, hash, since, until, node
    });

    // Already grouped by hash — just format for backward compat
    const sorted = filtered.map(tx => ({
      hash: tx.hash,
      count: tx.observation_count,
      observer_count: new Set(tx.observations.map(o => o.observer_id).filter(Boolean)).size,
      latest: tx.observations.length ? tx.observations.reduce((max, o) => o.timestamp > max ? o.timestamp : max, tx.observations[0].timestamp) : tx.timestamp,
      observer_id: tx.observer_id,
      observer_name: tx.observer_name,
      path_json: tx.path_json,
      payload_type: tx.payload_type,
      raw_hex: tx.raw_hex,
      decoded_json: tx.decoded_json,
      observation_count: tx.observation_count,
    })).sort((a, b) => b.latest.localeCompare(a.latest));

    const total = sorted.length;
    const paginated = sorted.slice(Number(offset), Number(offset) + Number(limit));
    return { packets: paginated, total };
  }

  /** Get timestamps for sparkline */
  getTimestamps(since) {
    if (this.sqliteOnly) {
      return this.db.prepare('SELECT timestamp FROM packets_v WHERE timestamp > ? ORDER BY timestamp ASC').all(since).map(r => r.timestamp);
    }
    const results = [];
    for (const p of this.packets) {
      if (p.timestamp <= since) break;
      results.push(p.timestamp);
    }
    return results.reverse();
  }

  /** Get a single packet by ID — checks observation IDs first (backward compat) */
  getById(id) {
    if (this.sqliteOnly) return this.db.prepare('SELECT * FROM packets_v WHERE id = ?').get(id) || null;
    return this.byId.get(id) || null;
  }

  /** Get a transmission by its transmission table ID */
  getByTxId(id) {
    if (this.sqliteOnly) return this.db.prepare('SELECT * FROM transmissions WHERE id = ?').get(id) || null;
    return this.byTxId.get(id) || null;
  }

  /** Get all siblings of a packet (same hash) — returns observations array */
  getSiblings(hash) {
    const h = hash.toLowerCase();
    if (this.sqliteOnly) return this.db.prepare('SELECT * FROM packets_v WHERE hash = ? ORDER BY timestamp DESC').all(h);
    const tx = this.byHash.get(h);
    return tx ? tx.observations : [];
  }

  /** Get all transmissions (backward compat — returns packets array) */
  all() {
    if (this.sqliteOnly) return this.db.prepare('SELECT * FROM packets_v ORDER BY timestamp DESC').all();
    return this.packets;
  }

  /** Get all transmissions matching a filter function */
  filter(fn) {
    if (this.sqliteOnly) return this.db.prepare('SELECT * FROM packets_v ORDER BY timestamp DESC').all().filter(fn);
    return this.packets.filter(fn);
  }

  /** Memory stats */
  getStats() {
    return {
      ...this.stats,
      inMemory: this.sqliteOnly ? 0 : this.packets.length,
      sqliteOnly: this.sqliteOnly,
      maxPackets: this.maxPackets,
      estimatedMB: this.sqliteOnly ? 0 : Math.round(this.packets.length * this.estPacketBytes / 1024 / 1024),
      maxMB: Math.round(this.maxBytes / 1024 / 1024),
      indexes: {
        byHash: this.byHash.size,
        byObserver: this.byObserver.size,
        byNode: this.byNode.size,
        advertByObserver: this._advertByObserver.size,
      }
    };
  }

  /** SQLite fallback: query with filters */
  _querySQLite({ limit, offset, type, route, region, observer, hash, since, until, node, order }) {
    const where = []; const params = [];
    if (type !== undefined) { where.push('payload_type = ?'); params.push(Number(type)); }
    if (route !== undefined) { where.push('route_type = ?'); params.push(Number(route)); }
    if (observer) { where.push('observer_id = ?'); params.push(observer); }
    if (hash) { where.push('hash = ?'); params.push(hash.toLowerCase()); }
    if (since) { where.push('timestamp > ?'); params.push(since); }
    if (until) { where.push('timestamp < ?'); params.push(until); }
    if (region) { where.push('observer_id IN (SELECT id FROM observers WHERE iata = ?)'); params.push(region); }
    if (node) { try { const nr = this.db.prepare('SELECT public_key FROM nodes WHERE public_key = ? OR name = ? LIMIT 1').get(node, node); const pk = nr ? nr.public_key : node; where.push('(decoded_json LIKE ? OR id IN (SELECT packet_id FROM paths WHERE node_hash = ?))'); params.push('%' + pk + '%', pk.substring(0, 8)); } catch(e) { where.push('decoded_json LIKE ?'); params.push('%' + node + '%'); } }
    const w = where.length ? 'WHERE ' + where.join(' AND ') : '';
    const total = this.db.prepare(`SELECT COUNT(*) as c FROM packets_v ${w}`).get(...params).c;
    const packets = this.db.prepare(`SELECT * FROM packets_v ${w} ORDER BY timestamp ${order === 'ASC' ? 'ASC' : 'DESC'} LIMIT ? OFFSET ?`).all(...params, limit, offset);
    return { packets, total };
  }

  /** SQLite fallback: grouped query */
  _queryGroupedSQLite({ limit, offset, type, route, region, observer, hash, since, until, node }) {
    const where = []; const params = [];
    if (type !== undefined) { where.push('payload_type = ?'); params.push(Number(type)); }
    if (route !== undefined) { where.push('route_type = ?'); params.push(Number(route)); }
    if (observer) { where.push('observer_id = ?'); params.push(observer); }
    if (hash) { where.push('hash = ?'); params.push(hash.toLowerCase()); }
    if (since) { where.push('timestamp > ?'); params.push(since); }
    if (until) { where.push('timestamp < ?'); params.push(until); }
    if (region) { where.push('observer_id IN (SELECT id FROM observers WHERE iata = ?)'); params.push(region); }
    if (node) { try { const nr = this.db.prepare('SELECT public_key FROM nodes WHERE public_key = ? OR name = ? LIMIT 1').get(node, node); const pk = nr ? nr.public_key : node; where.push('(decoded_json LIKE ? OR id IN (SELECT packet_id FROM paths WHERE node_hash = ?))'); params.push('%' + pk + '%', pk.substring(0, 8)); } catch(e) { where.push('decoded_json LIKE ?'); params.push('%' + node + '%'); } }
    const w = where.length ? 'WHERE ' + where.join(' AND ') : '';

    const sql = `SELECT hash, COUNT(*) as count, COUNT(DISTINCT observer_id) as observer_count,
      MAX(timestamp) as latest, MIN(observer_id) as observer_id, MIN(observer_name) as observer_name,
      MIN(path_json) as path_json, MIN(payload_type) as payload_type, MIN(raw_hex) as raw_hex,
      MIN(decoded_json) as decoded_json
      FROM packets_v ${w} GROUP BY hash ORDER BY latest DESC LIMIT ? OFFSET ?`;
    const packets = this.db.prepare(sql).all(...params, limit, offset);

    const countSql = `SELECT COUNT(DISTINCT hash) as c FROM packets_v ${w}`;
    const total = this.db.prepare(countSql).get(...params).c;
    return { packets, total };
  }
}

module.exports = PacketStore;
