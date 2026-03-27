package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// StoreTx is an in-memory transmission with embedded observations.
type StoreTx struct {
	ID               int
	RawHex           string
	Hash             string
	FirstSeen        string
	RouteType        *int
	PayloadType      *int
	DecodedJSON      string
	Observations     []*StoreObs
	ObservationCount int
	// Display fields from longest-path observation
	ObserverID   string
	ObserverName string
	SNR          *float64
	RSSI         *float64
	PathJSON     string
	Direction    string
}

// StoreObs is a lean in-memory observation (no duplication of transmission fields).
type StoreObs struct {
	ID             int
	TransmissionID int
	ObserverID     string
	ObserverName   string
	Direction      string
	SNR            *float64
	RSSI           *float64
	Score          *int
	PathJSON       string
	Timestamp      string
}

// PacketStore holds all transmissions in memory with indexes for fast queries.
type PacketStore struct {
	mu            sync.RWMutex
	db            *DB
	packets       []*StoreTx              // sorted by first_seen DESC
	byHash        map[string]*StoreTx     // hash → *StoreTx
	byTxID        map[int]*StoreTx        // transmission_id → *StoreTx
	byObsID       map[int]*StoreObs       // observation_id → *StoreObs
	byObserver    map[string][]*StoreObs  // observer_id → observations
	byNode        map[string][]*StoreTx   // pubkey → transmissions
	nodeHashes    map[string]map[string]bool // pubkey → Set<hash>
	byPayloadType map[int][]*StoreTx      // payload_type → transmissions
	loaded        bool
	totalObs      int
	// Response caches (separate mutex to avoid contention with store RWMutex)
	cacheMu    sync.Mutex
	rfCache    map[string]*cachedResult // region → cached RF result
	rfCacheTTL time.Duration
}

type cachedResult struct {
	data      map[string]interface{}
	expiresAt time.Time
}

// NewPacketStore creates a new empty packet store backed by db.
func NewPacketStore(db *DB) *PacketStore {
	return &PacketStore{
		db:            db,
		packets:       make([]*StoreTx, 0, 65536),
		byHash:        make(map[string]*StoreTx, 65536),
		byTxID:        make(map[int]*StoreTx, 65536),
		byObsID:       make(map[int]*StoreObs, 65536),
		byObserver:    make(map[string][]*StoreObs),
		byNode:        make(map[string][]*StoreTx),
		nodeHashes:    make(map[string]map[string]bool),
		byPayloadType: make(map[int][]*StoreTx),
		rfCache:       make(map[string]*cachedResult),
		rfCacheTTL:    15 * time.Second,
	}
}

// Load reads all transmissions + observations from SQLite into memory.
func (s *PacketStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t0 := time.Now()

	var loadSQL string
	if s.db.isV3 {
		loadSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, obs.id, obs.name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, datetime(o.timestamp, 'unixepoch')
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			ORDER BY t.first_seen DESC, o.timestamp DESC`
	} else {
		loadSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, o.observer_id, o.observer_name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			ORDER BY t.first_seen DESC, o.timestamp DESC`
	}

	rows, err := s.db.conn.Query(loadSQL)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var txID int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		var obsID sql.NullInt64
		var observerID, observerName, direction, pathJSON, obsTimestamp sql.NullString
		var snr, rssi sql.NullFloat64
		var score sql.NullInt64

		if err := rows.Scan(&txID, &rawHex, &hash, &firstSeen, &routeType, &payloadType,
			&payloadVersion, &decodedJSON,
			&obsID, &observerID, &observerName, &direction,
			&snr, &rssi, &score, &pathJSON, &obsTimestamp); err != nil {
			log.Printf("[store] scan error: %v", err)
			continue
		}

		hashStr := nullStrVal(hash)
		tx := s.byHash[hashStr]
		if tx == nil {
			tx = &StoreTx{
				ID:          txID,
				RawHex:      nullStrVal(rawHex),
				Hash:        hashStr,
				FirstSeen:   nullStrVal(firstSeen),
				RouteType:   nullIntPtr(routeType),
				PayloadType: nullIntPtr(payloadType),
				DecodedJSON: nullStrVal(decodedJSON),
			}
			s.byHash[hashStr] = tx
			s.packets = append(s.packets, tx)
			s.byTxID[txID] = tx
			s.indexByNode(tx)
			if tx.PayloadType != nil {
				pt := *tx.PayloadType
				s.byPayloadType[pt] = append(s.byPayloadType[pt], tx)
			}
		}

		if obsID.Valid {
			oid := int(obsID.Int64)
			obsIDStr := nullStrVal(observerID)
			obsPJ := nullStrVal(pathJSON)

			// Dedup: skip if same observer + same path already loaded
			isDupe := false
			for _, existing := range tx.Observations {
				if existing.ObserverID == obsIDStr && existing.PathJSON == obsPJ {
					isDupe = true
					break
				}
			}
			if isDupe {
				continue
			}

			obs := &StoreObs{
				ID:             oid,
				TransmissionID: txID,
				ObserverID:     obsIDStr,
				ObserverName:   nullStrVal(observerName),
				Direction:      nullStrVal(direction),
				SNR:            nullFloatPtr(snr),
				RSSI:           nullFloatPtr(rssi),
				Score:          nullIntPtr(score),
				PathJSON:       obsPJ,
				Timestamp:      nullStrVal(obsTimestamp),
			}

			tx.Observations = append(tx.Observations, obs)
			tx.ObservationCount++

			s.byObsID[oid] = obs

			if obsIDStr != "" {
				s.byObserver[obsIDStr] = append(s.byObserver[obsIDStr], obs)
			}

			s.totalObs++
		}
	}

	// Post-load: pick best observation (longest path) for each transmission
	for _, tx := range s.packets {
		pickBestObservation(tx)
	}

	s.loaded = true
	elapsed := time.Since(t0)
	estMB := (len(s.packets)*450 + s.totalObs*100) / (1024 * 1024)
	log.Printf("[store] Loaded %d transmissions (%d observations) in %v (~%dMB est)",
		len(s.packets), s.totalObs, elapsed, estMB)
	return nil
}

// pickBestObservation selects the observation with the longest path
// and sets it as the transmission's display observation.
func pickBestObservation(tx *StoreTx) {
	if len(tx.Observations) == 0 {
		return
	}
	best := tx.Observations[0]
	bestLen := pathLen(best.PathJSON)
	for _, obs := range tx.Observations[1:] {
		l := pathLen(obs.PathJSON)
		if l > bestLen {
			best = obs
			bestLen = l
		}
	}
	tx.ObserverID = best.ObserverID
	tx.ObserverName = best.ObserverName
	tx.SNR = best.SNR
	tx.RSSI = best.RSSI
	tx.PathJSON = best.PathJSON
	tx.Direction = best.Direction
}

func pathLen(pathJSON string) int {
	if pathJSON == "" {
		return 0
	}
	var hops []interface{}
	if json.Unmarshal([]byte(pathJSON), &hops) != nil {
		return 0
	}
	return len(hops)
}

// indexByNode extracts pubkeys from decoded_json and indexes the transmission.
func (s *PacketStore) indexByNode(tx *StoreTx) {
	if tx.DecodedJSON == "" {
		return
	}
	var decoded map[string]interface{}
	if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) != nil {
		return
	}
	for _, field := range []string{"pubKey", "destPubKey", "srcPubKey"} {
		if v, ok := decoded[field].(string); ok && v != "" {
			if s.nodeHashes[v] == nil {
				s.nodeHashes[v] = make(map[string]bool)
			}
			if s.nodeHashes[v][tx.Hash] {
				continue
			}
			s.nodeHashes[v][tx.Hash] = true
			s.byNode[v] = append(s.byNode[v], tx)
		}
	}
}

// QueryPackets returns filtered, paginated packets from memory.
func (s *PacketStore) QueryPackets(q PacketQuery) *PacketResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Order == "" {
		q.Order = "DESC"
	}

	results := s.filterPackets(q)
	total := len(results)

	if q.Order == "ASC" {
		sorted := make([]*StoreTx, len(results))
		copy(sorted, results)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].FirstSeen < sorted[j].FirstSeen
		})
		results = sorted
	}

	// Paginate
	start := q.Offset
	if start >= len(results) {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	end := start + q.Limit
	if end > len(results) {
		end = len(results)
	}

	packets := make([]map[string]interface{}, 0, end-start)
	for _, tx := range results[start:end] {
		packets = append(packets, txToMap(tx))
	}
	return &PacketResult{Packets: packets, Total: total}
}

// QueryGroupedPackets returns transmissions grouped by hash (already 1:1).
func (s *PacketStore) QueryGroupedPackets(q PacketQuery) *PacketResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if q.Limit <= 0 {
		q.Limit = 50
	}

	results := s.filterPackets(q)

	// Build grouped output sorted by latest observation DESC
	type groupEntry struct {
		tx     *StoreTx
		latest string
	}
	entries := make([]groupEntry, len(results))
	for i, tx := range results {
		latest := tx.FirstSeen
		for _, obs := range tx.Observations {
			if obs.Timestamp > latest {
				latest = obs.Timestamp
			}
		}
		entries[i] = groupEntry{tx: tx, latest: latest}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].latest > entries[j].latest
	})

	total := len(entries)
	start := q.Offset
	if start >= total {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	end := start + q.Limit
	if end > total {
		end = total
	}

	packets := make([]map[string]interface{}, 0, end-start)
	for _, e := range entries[start:end] {
		tx := e.tx
		observerCount := 0
		seen := make(map[string]bool)
		for _, obs := range tx.Observations {
			if obs.ObserverID != "" && !seen[obs.ObserverID] {
				seen[obs.ObserverID] = true
				observerCount++
			}
		}
		packets = append(packets, map[string]interface{}{
			"hash":              strOrNil(tx.Hash),
			"first_seen":        strOrNil(tx.FirstSeen),
			"count":             tx.ObservationCount,
			"observer_count":    observerCount,
			"observation_count": tx.ObservationCount,
			"latest":            strOrNil(e.latest),
			"observer_id":       strOrNil(tx.ObserverID),
			"observer_name":     strOrNil(tx.ObserverName),
			"path_json":         strOrNil(tx.PathJSON),
			"payload_type":      intPtrOrNil(tx.PayloadType),
			"route_type":        intPtrOrNil(tx.RouteType),
			"raw_hex":           strOrNil(tx.RawHex),
			"decoded_json":      strOrNil(tx.DecodedJSON),
			"snr":               floatPtrOrNil(tx.SNR),
			"rssi":              floatPtrOrNil(tx.RSSI),
		})
	}

	return &PacketResult{Packets: packets, Total: total}
}

// GetStoreStats returns aggregate counts (packet data from memory, node/observer from DB).
func (s *PacketStore) GetStoreStats() (*Stats, error) {
	s.mu.RLock()
	txCount := len(s.packets)
	obsCount := s.totalObs
	s.mu.RUnlock()

	st := &Stats{
		TotalTransmissions: txCount,
		TotalPackets:       txCount,
		TotalObservations:  obsCount,
	}

	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	s.db.conn.QueryRow("SELECT COUNT(*) FROM nodes WHERE last_seen > ?", sevenDaysAgo).Scan(&st.TotalNodes)
	s.db.conn.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&st.TotalNodesAllTime)
	s.db.conn.QueryRow("SELECT COUNT(*) FROM observers").Scan(&st.TotalObservers)

	oneHourAgo := time.Now().Add(-1 * time.Hour).Unix()
	s.db.conn.QueryRow("SELECT COUNT(*) FROM observations WHERE timestamp > ?", oneHourAgo).Scan(&st.PacketsLastHour)

	return st, nil
}

// GetTransmissionByID returns a transmission by its DB ID, formatted as a map.
func (s *PacketStore) GetTransmissionByID(id int) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byTxID[id]
	if tx == nil {
		return nil
	}
	return txToMap(tx)
}

// GetPacketByHash returns a transmission by content hash.
func (s *PacketStore) GetPacketByHash(hash string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byHash[strings.ToLower(hash)]
	if tx == nil {
		return nil
	}
	return txToMap(tx)
}

// GetPacketByID returns an observation (enriched with transmission fields) by observation ID.
func (s *PacketStore) GetPacketByID(id int) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	obs := s.byObsID[id]
	if obs == nil {
		return nil
	}
	return s.enrichObs(obs)
}

// GetObservationsForHash returns all observations for a hash, enriched with transmission fields.
func (s *PacketStore) GetObservationsForHash(hash string) []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byHash[strings.ToLower(hash)]
	if tx == nil {
		return []map[string]interface{}{}
	}

	result := make([]map[string]interface{}, 0, len(tx.Observations))
	for _, obs := range tx.Observations {
		result = append(result, s.enrichObs(obs))
	}
	return result
}

// GetTimestamps returns transmission first_seen timestamps after since, in ASC order.
func (s *PacketStore) GetTimestamps(since string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// packets sorted newest first — scan from start until older than since
	var result []string
	for _, tx := range s.packets {
		if tx.FirstSeen <= since {
			break
		}
		result = append(result, tx.FirstSeen)
	}
	// Reverse to get ASC order
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// QueryMultiNodePackets filters packets matching any of the given pubkeys.
func (s *PacketStore) QueryMultiNodePackets(pubkeys []string, limit, offset int, order, since, until string) *PacketResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(pubkeys) == 0 {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: 0}
	}
	if limit <= 0 {
		limit = 50
	}

	resolved := make([]string, len(pubkeys))
	for i, pk := range pubkeys {
		resolved[i] = s.db.resolveNodePubkey(pk)
	}

	var filtered []*StoreTx
	for _, tx := range s.packets {
		if tx.DecodedJSON == "" {
			continue
		}
		match := false
		for _, pk := range resolved {
			if strings.Contains(tx.DecodedJSON, pk) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		if since != "" && tx.FirstSeen < since {
			continue
		}
		if until != "" && tx.FirstSeen > until {
			continue
		}
		filtered = append(filtered, tx)
	}

	total := len(filtered)

	if order == "ASC" {
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].FirstSeen < filtered[j].FirstSeen
		})
	}

	if offset >= total {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	end := offset + limit
	if end > total {
		end = total
	}

	packets := make([]map[string]interface{}, 0, end-offset)
	for _, tx := range filtered[offset:end] {
		packets = append(packets, txToMap(tx))
	}
	return &PacketResult{Packets: packets, Total: total}
}

// IngestNewFromDB loads new transmissions from SQLite into memory and returns
// broadcast-ready maps plus the new max transmission ID.
func (s *PacketStore) IngestNewFromDB(sinceID, limit int) ([]map[string]interface{}, int) {
	if limit <= 0 {
		limit = 100
	}

	var querySQL string
	if s.db.isV3 {
		querySQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, obs.id, obs.name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, datetime(o.timestamp, 'unixepoch')
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE t.id > ?
			ORDER BY t.id ASC, o.timestamp DESC`
	} else {
		querySQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, o.observer_id, o.observer_name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			WHERE t.id > ?
			ORDER BY t.id ASC, o.timestamp DESC`
	}

	rows, err := s.db.conn.Query(querySQL, sinceID)
	if err != nil {
		log.Printf("[store] ingest query error: %v", err)
		return nil, sinceID
	}
	defer rows.Close()

	// Scan into temp structures
	type tempRow struct {
		txID                                                   int
		rawHex, hash, firstSeen, decodedJSON                   string
		routeType, payloadType                                 *int
		obsID                                                  *int
		observerID, observerName, direction, pathJSON, obsTS   string
		snr, rssi                                              *float64
		score                                                  *int
	}

	var tempRows []tempRow
	txCount := 0
	lastTxID := sinceID

	for rows.Next() {
		var txID int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		var obsIDVal sql.NullInt64
		var observerID, observerName, direction, pathJSON, obsTimestamp sql.NullString
		var snrVal, rssiVal sql.NullFloat64
		var scoreVal sql.NullInt64

		if err := rows.Scan(&txID, &rawHex, &hash, &firstSeen, &routeType, &payloadType,
			&payloadVersion, &decodedJSON,
			&obsIDVal, &observerID, &observerName, &direction,
			&snrVal, &rssiVal, &scoreVal, &pathJSON, &obsTimestamp); err != nil {
			continue
		}

		if txID != lastTxID {
			txCount++
			if txCount > limit {
				break
			}
			lastTxID = txID
		}

		tr := tempRow{
			txID:         txID,
			rawHex:       nullStrVal(rawHex),
			hash:         nullStrVal(hash),
			firstSeen:    nullStrVal(firstSeen),
			decodedJSON:  nullStrVal(decodedJSON),
			routeType:    nullIntPtr(routeType),
			payloadType:  nullIntPtr(payloadType),
			observerID:   nullStrVal(observerID),
			observerName: nullStrVal(observerName),
			direction:    nullStrVal(direction),
			pathJSON:     nullStrVal(pathJSON),
			obsTS:        nullStrVal(obsTimestamp),
			snr:          nullFloatPtr(snrVal),
			rssi:         nullFloatPtr(rssiVal),
			score:        nullIntPtr(scoreVal),
		}
		if obsIDVal.Valid {
			oid := int(obsIDVal.Int64)
			tr.obsID = &oid
		}
		tempRows = append(tempRows, tr)
	}

	if len(tempRows) == 0 {
		return nil, sinceID
	}

	// Now lock and merge into store
	s.mu.Lock()
	defer s.mu.Unlock()

	newMaxID := sinceID
	broadcastTxs := make(map[int]*StoreTx) // track new transmissions for broadcast
	var broadcastOrder []int

	for _, r := range tempRows {
		if r.txID > newMaxID {
			newMaxID = r.txID
		}

		tx := s.byHash[r.hash]
		if tx == nil {
			tx = &StoreTx{
				ID:          r.txID,
				RawHex:      r.rawHex,
				Hash:        r.hash,
				FirstSeen:   r.firstSeen,
				RouteType:   r.routeType,
				PayloadType: r.payloadType,
				DecodedJSON: r.decodedJSON,
			}
			s.byHash[r.hash] = tx
			// Prepend (newest first)
			s.packets = append([]*StoreTx{tx}, s.packets...)
			s.byTxID[r.txID] = tx
			s.indexByNode(tx)
			if tx.PayloadType != nil {
				pt := *tx.PayloadType
				s.byPayloadType[pt] = append(s.byPayloadType[pt], tx)
			}

			if _, exists := broadcastTxs[r.txID]; !exists {
				broadcastTxs[r.txID] = tx
				broadcastOrder = append(broadcastOrder, r.txID)
			}
		}

		if r.obsID != nil {
			oid := *r.obsID
			// Dedup
			isDupe := false
			for _, existing := range tx.Observations {
				if existing.ObserverID == r.observerID && existing.PathJSON == r.pathJSON {
					isDupe = true
					break
				}
			}
			if isDupe {
				continue
			}

			obs := &StoreObs{
				ID:             oid,
				TransmissionID: r.txID,
				ObserverID:     r.observerID,
				ObserverName:   r.observerName,
				Direction:      r.direction,
				SNR:            r.snr,
				RSSI:           r.rssi,
				Score:          r.score,
				PathJSON:       r.pathJSON,
				Timestamp:      r.obsTS,
			}
			tx.Observations = append(tx.Observations, obs)
			tx.ObservationCount++
			s.byObsID[oid] = obs
			if r.observerID != "" {
				s.byObserver[r.observerID] = append(s.byObserver[r.observerID], obs)
			}
			s.totalObs++
		}
	}

	// Pick best observation for new transmissions
	for _, tx := range broadcastTxs {
		pickBestObservation(tx)
	}

	// Build broadcast maps (same shape as GetNewTransmissionsSince)
	result := make([]map[string]interface{}, 0, len(broadcastOrder))
	for _, txID := range broadcastOrder {
		tx := broadcastTxs[txID]
		result = append(result, map[string]interface{}{
			"id":           tx.ID,
			"raw_hex":      strOrNil(tx.RawHex),
			"hash":         strOrNil(tx.Hash),
			"first_seen":   strOrNil(tx.FirstSeen),
			"route_type":   intPtrOrNil(tx.RouteType),
			"payload_type": intPtrOrNil(tx.PayloadType),
			"decoded_json": strOrNil(tx.DecodedJSON),
		})
	}
	return result, newMaxID
}

// MaxTransmissionID returns the highest transmission ID in the store.
func (s *PacketStore) MaxTransmissionID() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	maxID := 0
	for id := range s.byTxID {
		if id > maxID {
			maxID = id
		}
	}
	return maxID
}

// --- Internal filter/query helpers ---

// filterPackets applies PacketQuery filters to the in-memory packet list.
func (s *PacketStore) filterPackets(q PacketQuery) []*StoreTx {
	// Fast path: single-key index lookups
	if q.Hash != "" && q.Type == nil && q.Route == nil && q.Observer == "" &&
		q.Region == "" && q.Node == "" && q.Since == "" && q.Until == "" {
		h := strings.ToLower(q.Hash)
		tx := s.byHash[h]
		if tx == nil {
			return nil
		}
		return []*StoreTx{tx}
	}
	if q.Observer != "" && q.Type == nil && q.Route == nil &&
		q.Region == "" && q.Node == "" && q.Hash == "" && q.Since == "" && q.Until == "" {
		return s.transmissionsForObserver(q.Observer, nil)
	}

	results := s.packets

	if q.Type != nil {
		t := *q.Type
		results = filterTxSlice(results, func(tx *StoreTx) bool {
			return tx.PayloadType != nil && *tx.PayloadType == t
		})
	}
	if q.Route != nil {
		r := *q.Route
		results = filterTxSlice(results, func(tx *StoreTx) bool {
			return tx.RouteType != nil && *tx.RouteType == r
		})
	}
	if q.Observer != "" {
		results = s.transmissionsForObserver(q.Observer, results)
	}
	if q.Hash != "" {
		h := strings.ToLower(q.Hash)
		results = filterTxSlice(results, func(tx *StoreTx) bool {
			return tx.Hash == h
		})
	}
	if q.Since != "" {
		results = filterTxSlice(results, func(tx *StoreTx) bool {
			return tx.FirstSeen > q.Since
		})
	}
	if q.Until != "" {
		results = filterTxSlice(results, func(tx *StoreTx) bool {
			return tx.FirstSeen < q.Until
		})
	}
	if q.Region != "" {
		regionObservers := s.resolveRegionObservers(q.Region)
		if len(regionObservers) > 0 {
			results = filterTxSlice(results, func(tx *StoreTx) bool {
				for _, obs := range tx.Observations {
					if regionObservers[obs.ObserverID] {
						return true
					}
				}
				return false
			})
		} else {
			results = nil
		}
	}
	if q.Node != "" {
		pk := s.db.resolveNodePubkey(q.Node)
		// Use node index if available
		if indexed, ok := s.byNode[pk]; ok && results == nil {
			results = indexed
		} else {
			results = filterTxSlice(results, func(tx *StoreTx) bool {
				if tx.DecodedJSON == "" {
					return false
				}
				return strings.Contains(tx.DecodedJSON, pk) || strings.Contains(tx.DecodedJSON, q.Node)
			})
		}
	}

	return results
}

// transmissionsForObserver returns unique transmissions for an observer.
func (s *PacketStore) transmissionsForObserver(observerID string, from []*StoreTx) []*StoreTx {
	if from != nil {
		return filterTxSlice(from, func(tx *StoreTx) bool {
			for _, obs := range tx.Observations {
				if obs.ObserverID == observerID {
					return true
				}
			}
			return false
		})
	}
	// Use byObserver index
	observations := s.byObserver[observerID]
	if len(observations) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(observations))
	var result []*StoreTx
	for _, obs := range observations {
		if seen[obs.TransmissionID] {
			continue
		}
		seen[obs.TransmissionID] = true
		tx := s.byTxID[obs.TransmissionID]
		if tx != nil {
			result = append(result, tx)
		}
	}
	return result
}

// resolveRegionObservers returns a set of observer IDs for a given IATA region.
func (s *PacketStore) resolveRegionObservers(region string) map[string]bool {
	ids, err := s.db.GetObserverIdsForRegion(region)
	if err != nil || len(ids) == 0 {
		return nil
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// enrichObs returns a map with observation fields + transmission fields.
func (s *PacketStore) enrichObs(obs *StoreObs) map[string]interface{} {
	tx := s.byTxID[obs.TransmissionID]

	m := map[string]interface{}{
		"id":            obs.ID,
		"timestamp":     strOrNil(obs.Timestamp),
		"observer_id":   strOrNil(obs.ObserverID),
		"observer_name": strOrNil(obs.ObserverName),
		"direction":     strOrNil(obs.Direction),
		"snr":           floatPtrOrNil(obs.SNR),
		"rssi":          floatPtrOrNil(obs.RSSI),
		"score":         intPtrOrNil(obs.Score),
		"path_json":     strOrNil(obs.PathJSON),
	}

	if tx != nil {
		m["hash"] = strOrNil(tx.Hash)
		m["raw_hex"] = strOrNil(tx.RawHex)
		m["payload_type"] = intPtrOrNil(tx.PayloadType)
		m["route_type"] = intPtrOrNil(tx.RouteType)
		m["decoded_json"] = strOrNil(tx.DecodedJSON)
	}

	return m
}

// --- Conversion helpers ---

// txToMap converts a StoreTx to the map shape matching scanTransmissionRow output.
func txToMap(tx *StoreTx) map[string]interface{} {
	return map[string]interface{}{
		"id":                tx.ID,
		"raw_hex":           strOrNil(tx.RawHex),
		"hash":              strOrNil(tx.Hash),
		"first_seen":        strOrNil(tx.FirstSeen),
		"timestamp":         strOrNil(tx.FirstSeen),
		"route_type":        intPtrOrNil(tx.RouteType),
		"payload_type":      intPtrOrNil(tx.PayloadType),
		"decoded_json":      strOrNil(tx.DecodedJSON),
		"observation_count": tx.ObservationCount,
		"observer_id":       strOrNil(tx.ObserverID),
		"observer_name":     strOrNil(tx.ObserverName),
		"snr":               floatPtrOrNil(tx.SNR),
		"rssi":              floatPtrOrNil(tx.RSSI),
		"path_json":         strOrNil(tx.PathJSON),
		"direction":         strOrNil(tx.Direction),
	}
}

func strOrNil(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func intPtrOrNil(p *int) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func floatPtrOrNil(p *float64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func nullIntPtr(ni sql.NullInt64) *int {
	if ni.Valid {
		v := int(ni.Int64)
		return &v
	}
	return nil
}

func nullFloatPtr(nf sql.NullFloat64) *float64 {
	if nf.Valid {
		return &nf.Float64
	}
	return nil
}

func filterTxSlice(s []*StoreTx, fn func(*StoreTx) bool) []*StoreTx {
	var result []*StoreTx
	for _, tx := range s {
		if fn(tx) {
			result = append(result, tx)
		}
	}
	return result
}

// GetChannels returns channel list from in-memory packets (payload_type 5, decoded type CHAN).
func (s *PacketStore) GetChannels(region string) []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	type chanInfo struct {
		Hash         string
		Name         string
		LastMessage  interface{}
		LastSender   interface{}
		MessageCount int
		LastActivity string
	}
	type decodedGrp struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
		Text    string `json:"text"`
		Sender  string `json:"sender"`
	}
	channelMap := map[string]*chanInfo{}

	grpTxts := s.byPayloadType[5]
	for _, tx := range grpTxts {

		// Region filter: check if any observation is from a regional observer
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		var decoded decodedGrp
		if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) != nil {
			continue
		}
		if decoded.Type != "CHAN" {
			continue
		}

		channelName := decoded.Channel
		if channelName == "" {
			channelName = "unknown"
		}
		key := channelName

		ch := channelMap[key]
		if ch == nil {
			ch = &chanInfo{
				Hash: key, Name: channelName,
				LastActivity: tx.FirstSeen,
			}
			channelMap[key] = ch
		}
		ch.MessageCount++
		if tx.FirstSeen >= ch.LastActivity {
			ch.LastActivity = tx.FirstSeen
			if decoded.Text != "" {
				idx := strings.Index(decoded.Text, ": ")
				if idx > 0 {
					ch.LastMessage = decoded.Text[idx+2:]
				} else {
					ch.LastMessage = decoded.Text
				}
				if decoded.Sender != "" {
					ch.LastSender = decoded.Sender
				}
			}
		}
	}

	channels := make([]map[string]interface{}, 0, len(channelMap))
	for _, ch := range channelMap {
		channels = append(channels, map[string]interface{}{
			"hash": ch.Hash, "name": ch.Name,
			"lastMessage": ch.LastMessage, "lastSender": ch.LastSender,
			"messageCount": ch.MessageCount, "lastActivity": ch.LastActivity,
		})
	}
	return channels
}

// GetChannelMessages returns deduplicated messages for a channel from in-memory packets.
func (s *PacketStore) GetChannelMessages(channelHash string, limit, offset int) ([]map[string]interface{}, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	type msgEntry struct {
		Data      map[string]interface{}
		Repeats   int
		Observers []string
	}
	msgMap := map[string]*msgEntry{}
	var msgOrder []string

	// Iterate type-5 packets oldest-first (byPayloadType is in load order = newest first)
	type decodedMsg struct {
		Type            string      `json:"type"`
		Channel         string      `json:"channel"`
		Text            string      `json:"text"`
		Sender          string      `json:"sender"`
		SenderTimestamp interface{} `json:"sender_timestamp"`
		PathLen         int         `json:"path_len"`
	}

	grpTxts := s.byPayloadType[5]
	for i := len(grpTxts) - 1; i >= 0; i-- {
		tx := grpTxts[i]
		if tx.DecodedJSON == "" {
			continue
		}

		var decoded decodedMsg
		if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) != nil {
			continue
		}
		if decoded.Type != "CHAN" {
			continue
		}
		ch := decoded.Channel
		if ch == "" {
			ch = "unknown"
		}
		if ch != channelHash {
			continue
		}

		text := decoded.Text
		sender := decoded.Sender
		if sender == "" && text != "" {
			idx := strings.Index(text, ": ")
			if idx > 0 && idx < 50 {
				sender = text[:idx]
			}
		}

		dedupeKey := sender + ":" + tx.Hash

		if existing, ok := msgMap[dedupeKey]; ok {
			existing.Repeats++
			existing.Data["repeats"] = existing.Repeats
			// Add observer if new
			obsName := tx.ObserverName
			if obsName == "" {
				obsName = tx.ObserverID
			}
			if obsName != "" {
				found := false
				for _, o := range existing.Observers {
					if o == obsName {
						found = true
						break
					}
				}
				if !found {
					existing.Observers = append(existing.Observers, obsName)
					existing.Data["observers"] = existing.Observers
				}
			}
		} else {
			displaySender := sender
			displayText := text
			if text != "" {
				idx := strings.Index(text, ": ")
				if idx > 0 && idx < 50 {
					displaySender = text[:idx]
					displayText = text[idx+2:]
				}
			}

			hops := pathLen(tx.PathJSON)

			var snrVal interface{}
			if tx.SNR != nil {
				snrVal = *tx.SNR
			}

			senderTs := decoded.SenderTimestamp

			observers := []string{}
			obsName := tx.ObserverName
			if obsName == "" {
				obsName = tx.ObserverID
			}
			if obsName != "" {
				observers = []string{obsName}
			}

			entry := &msgEntry{
				Data: map[string]interface{}{
					"sender":           displaySender,
					"text":             displayText,
					"timestamp":        strOrNil(tx.FirstSeen),
					"sender_timestamp": senderTs,
					"packetId":         tx.ID,
					"packetHash":       strOrNil(tx.Hash),
					"repeats":          1,
					"observers":        observers,
					"hops":             hops,
					"snr":              snrVal,
				},
				Repeats:   1,
				Observers: observers,
			}
			msgMap[dedupeKey] = entry
			msgOrder = append(msgOrder, dedupeKey)
		}
	}

	total := len(msgOrder)
	// Return latest messages (tail)
	start := total - limit - offset
	if start < 0 {
		start = 0
	}
	end := total - offset
	if end < 0 {
		end = 0
	}
	if end > total {
		end = total
	}

	messages := make([]map[string]interface{}, 0, end-start)
	for i := start; i < end; i++ {
		messages = append(messages, msgMap[msgOrder[i]].Data)
	}
	return messages, total
}

// GetAnalyticsRF returns full RF analytics computed from in-memory observations.
func (s *PacketStore) GetAnalyticsRF(region string) map[string]interface{} {
	s.cacheMu.Lock()
	if cached, ok := s.rfCache[region]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMu.Unlock()

	result := s.computeAnalyticsRF(region)

	s.cacheMu.Lock()
	s.rfCache[region] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsRF(region string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ptNames := map[int]string{0: "REQ", 1: "RESPONSE", 2: "TXT_MSG", 3: "ACK", 4: "ADVERT", 5: "GRP_TXT", 7: "ANON_REQ", 8: "PATH", 9: "TRACE", 11: "CONTROL"}

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	// Collect all observations matching the region
	estCap := s.totalObs
	if estCap > 2000000 {
		estCap = 2000000
	}
	snrVals := make([]float64, 0, estCap/2)
	rssiVals := make([]float64, 0, estCap/2)
	packetSizes := make([]int, 0, len(s.packets))
	seenSizeHashes := make(map[string]bool, len(s.packets))
	seenTypeHashes := make(map[string]bool, len(s.packets))
	typeBuckets := map[int]int{}
	hourBuckets := map[string]int{}
	snrByType := map[string]*struct{ vals []float64 }{}
	sigTime := map[string]*struct {
		snrs  []float64
		count int
	}{}
	scatterAll := make([]struct{ snr, rssi float64 }, 0, estCap/4)
	totalObs := 0
	regionalHashes := make(map[string]bool, len(s.packets))
	var minTimestamp, maxTimestamp string

	if regionObs != nil {
		// Regional: iterate observations from matching observers
		for obsID := range regionObs {
			obsList := s.byObserver[obsID]
			for _, obs := range obsList {
				totalObs++
				tx := s.byTxID[obs.TransmissionID]
				hash := ""
				if tx != nil {
					hash = tx.Hash
				}
				if hash != "" {
					regionalHashes[hash] = true
				}

				ts := obs.Timestamp
				if ts != "" {
					if minTimestamp == "" || ts < minTimestamp {
						minTimestamp = ts
					}
					if ts > maxTimestamp {
						maxTimestamp = ts
					}
				}

				// SNR/RSSI
				if obs.SNR != nil {
					snrVals = append(snrVals, *obs.SNR)
					typeName := "UNK"
					if tx != nil && tx.PayloadType != nil {
						if n, ok := ptNames[*tx.PayloadType]; ok {
							typeName = n
						} else {
							typeName = fmt.Sprintf("UNK(%d)", *tx.PayloadType)
						}
					}
					if snrByType[typeName] == nil {
						snrByType[typeName] = &struct{ vals []float64 }{}
					}
					snrByType[typeName].vals = append(snrByType[typeName].vals, *obs.SNR)

					if obs.RSSI != nil {
						scatterAll = append(scatterAll, struct{ snr, rssi float64 }{*obs.SNR, *obs.RSSI})
					}

					// Signal over time
					if len(ts) >= 13 {
						hr := ts[:13]
						if sigTime[hr] == nil {
							sigTime[hr] = &struct {
								snrs  []float64
								count int
							}{}
						}
						sigTime[hr].snrs = append(sigTime[hr].snrs, *obs.SNR)
						sigTime[hr].count++
					}
				}
				if obs.RSSI != nil {
					rssiVals = append(rssiVals, *obs.RSSI)
				}

				// Packets per hour
				if len(ts) >= 13 {
					hr := ts[:13]
					hourBuckets[hr]++
				}

				// Packet sizes (unique by hash)
				if hash != "" && !seenSizeHashes[hash] && tx != nil && tx.RawHex != "" {
					seenSizeHashes[hash] = true
					packetSizes = append(packetSizes, len(tx.RawHex)/2)
				}

				// Payload type distribution (unique by hash)
				if hash != "" && !seenTypeHashes[hash] && tx != nil && tx.PayloadType != nil {
					seenTypeHashes[hash] = true
					typeBuckets[*tx.PayloadType]++
				}
			}
		}
	} else {
		// No region: iterate all transmissions and their observations
		for _, tx := range s.packets {
			hash := tx.Hash
			if hash != "" {
				regionalHashes[hash] = true
				if !seenSizeHashes[hash] && tx.RawHex != "" {
					seenSizeHashes[hash] = true
					packetSizes = append(packetSizes, len(tx.RawHex)/2)
				}
				if !seenTypeHashes[hash] && tx.PayloadType != nil {
					seenTypeHashes[hash] = true
					typeBuckets[*tx.PayloadType]++
				}
			}

			// Pre-resolve type name once per transmission
			typeName := "UNK"
			if tx.PayloadType != nil {
				if n, ok := ptNames[*tx.PayloadType]; ok {
					typeName = n
				} else {
					typeName = fmt.Sprintf("UNK(%d)", *tx.PayloadType)
				}
			}

			if len(tx.Observations) > 0 {
				for _, obs := range tx.Observations {
					totalObs++
					ts := obs.Timestamp
					if ts != "" {
						if minTimestamp == "" || ts < minTimestamp {
							minTimestamp = ts
						}
						if ts > maxTimestamp {
							maxTimestamp = ts
						}
					}

					if obs.SNR != nil {
						snr := *obs.SNR
						snrVals = append(snrVals, snr)
						entry := snrByType[typeName]
						if entry == nil {
							entry = &struct{ vals []float64 }{}
							snrByType[typeName] = entry
						}
						entry.vals = append(entry.vals, snr)

						if obs.RSSI != nil {
							scatterAll = append(scatterAll, struct{ snr, rssi float64 }{snr, *obs.RSSI})
						}

						if len(ts) >= 13 {
							hr := ts[:13]
							st := sigTime[hr]
							if st == nil {
								st = &struct {
									snrs  []float64
									count int
								}{}
								sigTime[hr] = st
							}
							st.snrs = append(st.snrs, snr)
							st.count++
						}
					}
					if obs.RSSI != nil {
						rssiVals = append(rssiVals, *obs.RSSI)
					}

					if len(ts) >= 13 {
						hourBuckets[ts[:13]]++
					}
				}
			} else {
				// Legacy: transmission without observations
				totalObs++
				if tx.SNR != nil {
					snrVals = append(snrVals, *tx.SNR)
				}
				if tx.RSSI != nil {
					rssiVals = append(rssiVals, *tx.RSSI)
				}
				ts := tx.FirstSeen
				if ts != "" {
					if minTimestamp == "" || ts < minTimestamp {
						minTimestamp = ts
					}
					if ts > maxTimestamp {
						maxTimestamp = ts
					}
				}
				if len(ts) >= 13 {
					hourBuckets[ts[:13]]++
				}
			}
		}
	}

	// Stats helpers
	sortedF64 := func(arr []float64) []float64 {
		c := make([]float64, len(arr))
		copy(c, arr)
		sort.Float64s(c)
		return c
	}
	medianF64 := func(arr []float64) float64 {
		s := sortedF64(arr)
		if len(s) == 0 {
			return 0
		}
		return s[len(s)/2]
	}
	stddevF64 := func(arr []float64, avg float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		sum := 0.0
		for _, v := range arr {
			d := v - avg
			sum += d * d
		}
		return math.Sqrt(sum / float64(len(arr)))
	}
	minF64 := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v < m {
				m = v
			}
		}
		return m
	}
	maxF64 := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v > m {
				m = v
			}
		}
		return m
	}
	minInt := func(arr []int) int {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v < m {
				m = v
			}
		}
		return m
	}
	maxInt := func(arr []int) int {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v > m {
				m = v
			}
		}
		return m
	}

	snrAvg := 0.0
	if len(snrVals) > 0 {
		sum := 0.0
		for _, v := range snrVals {
			sum += v
		}
		snrAvg = sum / float64(len(snrVals))
	}
	rssiAvg := 0.0
	if len(rssiVals) > 0 {
		sum := 0.0
		for _, v := range rssiVals {
			sum += v
		}
		rssiAvg = sum / float64(len(rssiVals))
	}

	// Packets per hour
	type hourCount struct {
		Hour  string `json:"hour"`
		Count int    `json:"count"`
	}
	hourKeys := make([]string, 0, len(hourBuckets))
	for k := range hourBuckets {
		hourKeys = append(hourKeys, k)
	}
	sort.Strings(hourKeys)
	packetsPerHour := make([]hourCount, len(hourKeys))
	for i, k := range hourKeys {
		packetsPerHour[i] = hourCount{Hour: k, Count: hourBuckets[k]}
	}

	// Payload types
	type ptEntry struct {
		Type  int    `json:"type"`
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	payloadTypes := make([]ptEntry, 0, len(typeBuckets))
	for t, c := range typeBuckets {
		name := ptNames[t]
		if name == "" {
			name = fmt.Sprintf("UNK(%d)", t)
		}
		payloadTypes = append(payloadTypes, ptEntry{Type: t, Name: name, Count: c})
	}
	sort.Slice(payloadTypes, func(i, j int) bool { return payloadTypes[i].Count > payloadTypes[j].Count })

	// SNR by type
	type snrTypeEntry struct {
		Name  string  `json:"name"`
		Count int     `json:"count"`
		Avg   float64 `json:"avg"`
		Min   float64 `json:"min"`
		Max   float64 `json:"max"`
	}
	snrByTypeArr := make([]snrTypeEntry, 0, len(snrByType))
	for name, d := range snrByType {
		sum := 0.0
		for _, v := range d.vals {
			sum += v
		}
		snrByTypeArr = append(snrByTypeArr, snrTypeEntry{
			Name: name, Count: len(d.vals),
			Avg: sum / float64(len(d.vals)),
			Min: minF64(d.vals), Max: maxF64(d.vals),
		})
	}
	sort.Slice(snrByTypeArr, func(i, j int) bool { return snrByTypeArr[i].Count > snrByTypeArr[j].Count })

	// Signal over time
	type sigTimeEntry struct {
		Hour   string  `json:"hour"`
		Count  int     `json:"count"`
		AvgSnr float64 `json:"avgSnr"`
	}
	sigKeys := make([]string, 0, len(sigTime))
	for k := range sigTime {
		sigKeys = append(sigKeys, k)
	}
	sort.Strings(sigKeys)
	signalOverTime := make([]sigTimeEntry, len(sigKeys))
	for i, k := range sigKeys {
		d := sigTime[k]
		sum := 0.0
		for _, v := range d.snrs {
			sum += v
		}
		signalOverTime[i] = sigTimeEntry{Hour: k, Count: d.count, AvgSnr: sum / float64(d.count)}
	}

	// Scatter (downsample to 500)
	type scatterPoint struct {
		SNR  float64 `json:"snr"`
		RSSI float64 `json:"rssi"`
	}
	scatterStep := 1
	if len(scatterAll) > 500 {
		scatterStep = len(scatterAll) / 500
	}
	scatterData := make([]scatterPoint, 0, 500)
	for i, p := range scatterAll {
		if i%scatterStep == 0 {
			scatterData = append(scatterData, scatterPoint{SNR: p.snr, RSSI: p.rssi})
		}
	}

	// Histograms
	buildHistogramF64 := func(values []float64, bins int) map[string]interface{} {
		if len(values) == 0 {
			return map[string]interface{}{"bins": []interface{}{}, "min": 0, "max": 0}
		}
		mn, mx := minF64(values), maxF64(values)
		rng := mx - mn
		if rng == 0 {
			rng = 1
		}
		binWidth := rng / float64(bins)
		counts := make([]int, bins)
		for _, v := range values {
			idx := int((v - mn) / binWidth)
			if idx >= bins {
				idx = bins - 1
			}
			counts[idx]++
		}
		binArr := make([]map[string]interface{}, bins)
		for i, c := range counts {
			binArr[i] = map[string]interface{}{"x": mn + float64(i)*binWidth, "w": binWidth, "count": c}
		}
		return map[string]interface{}{"bins": binArr, "min": mn, "max": mx}
	}
	buildHistogramInt := func(values []int, bins int) map[string]interface{} {
		if len(values) == 0 {
			return map[string]interface{}{"bins": []interface{}{}, "min": 0, "max": 0}
		}
		mn, mx := float64(minInt(values)), float64(maxInt(values))
		rng := mx - mn
		if rng == 0 {
			rng = 1
		}
		binWidth := rng / float64(bins)
		counts := make([]int, bins)
		for _, v := range values {
			idx := int((float64(v) - mn) / binWidth)
			if idx >= bins {
				idx = bins - 1
			}
			counts[idx]++
		}
		binArr := make([]map[string]interface{}, bins)
		for i, c := range counts {
			binArr[i] = map[string]interface{}{"x": mn + float64(i)*binWidth, "w": binWidth, "count": c}
		}
		return map[string]interface{}{"bins": binArr, "min": mn, "max": mx}
	}

	snrHistogram := buildHistogramF64(snrVals, 20)
	rssiHistogram := buildHistogramF64(rssiVals, 20)
	sizeHistogram := buildHistogramInt(packetSizes, 25)

	// Time span from min/max timestamps tracked during first pass
	timeSpanHours := 0.0
	if minTimestamp != "" && maxTimestamp != "" && minTimestamp != maxTimestamp {
		// Parse only 2 timestamps instead of 1.2M
		parseTS := func(ts string) (time.Time, bool) {
			t, err := time.Parse("2006-01-02 15:04:05", ts)
			if err != nil {
				t, err = time.Parse(time.RFC3339, ts)
			}
			if err != nil {
				return time.Time{}, false
			}
			return t, true
		}
		if tMin, ok := parseTS(minTimestamp); ok {
			if tMax, ok := parseTS(maxTimestamp); ok {
				timeSpanHours = float64(tMax.UnixMilli()-tMin.UnixMilli()) / 3600000.0
			}
		}
	}

	// Avg packet size
	avgPktSize := 0
	if len(packetSizes) > 0 {
		sum := 0
		for _, v := range packetSizes {
			sum += v
		}
		avgPktSize = sum / len(packetSizes)
	}

	snrStats := map[string]interface{}{"min": 0.0, "max": 0.0, "avg": 0.0, "median": 0.0, "stddev": 0.0}
	if len(snrVals) > 0 {
		snrStats = map[string]interface{}{
			"min": minF64(snrVals), "max": maxF64(snrVals),
			"avg": snrAvg, "median": medianF64(snrVals),
			"stddev": stddevF64(snrVals, snrAvg),
		}
	}
	rssiStats := map[string]interface{}{"min": 0.0, "max": 0.0, "avg": 0.0, "median": 0.0, "stddev": 0.0}
	if len(rssiVals) > 0 {
		rssiStats = map[string]interface{}{
			"min": minF64(rssiVals), "max": maxF64(rssiVals),
			"avg": rssiAvg, "median": medianF64(rssiVals),
			"stddev": stddevF64(rssiVals, rssiAvg),
		}
	}

	return map[string]interface{}{
		"totalPackets":       len(snrVals),
		"totalAllPackets":    totalObs,
		"totalTransmissions": len(regionalHashes),
		"snr":                snrStats,
		"rssi":               rssiStats,
		"snrValues":          snrHistogram,
		"rssiValues":         rssiHistogram,
		"packetSizes":        sizeHistogram,
		"minPacketSize":      minInt(packetSizes),
		"maxPacketSize":      maxInt(packetSizes),
		"avgPacketSize":      avgPktSize,
		"packetsPerHour":     packetsPerHour,
		"payloadTypes":       payloadTypes,
		"snrByType":          snrByTypeArr,
		"signalOverTime":     signalOverTime,
		"scatterData":        scatterData,
		"timeSpanHours":      timeSpanHours,
	}
}
