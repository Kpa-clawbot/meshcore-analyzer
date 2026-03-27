package main

import (
	"database/sql"
	"encoding/json"
	"log"
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
	mu         sync.RWMutex
	db         *DB
	packets    []*StoreTx              // sorted by first_seen DESC
	byHash     map[string]*StoreTx     // hash → *StoreTx
	byTxID     map[int]*StoreTx        // transmission_id → *StoreTx
	byObsID    map[int]*StoreObs       // observation_id → *StoreObs
	byObserver map[string][]*StoreObs  // observer_id → observations
	byNode     map[string][]*StoreTx   // pubkey → transmissions
	nodeHashes map[string]map[string]bool // pubkey → Set<hash>
	loaded     bool
	totalObs   int
}

// NewPacketStore creates a new empty packet store backed by db.
func NewPacketStore(db *DB) *PacketStore {
	return &PacketStore{
		db:         db,
		packets:    make([]*StoreTx, 0, 65536),
		byHash:     make(map[string]*StoreTx, 65536),
		byTxID:     make(map[int]*StoreTx, 65536),
		byObsID:    make(map[int]*StoreObs, 65536),
		byObserver: make(map[string][]*StoreObs),
		byNode:     make(map[string][]*StoreTx),
		nodeHashes: make(map[string]map[string]bool),
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
