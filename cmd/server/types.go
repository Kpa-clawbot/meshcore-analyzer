package main

// Types generated from proto/ definitions for compile-time type safety.
// Every API response is a typed struct — no map[string]interface{}.

// ─── Common ────────────────────────────────────────────────────────────────────

type PaginationInfo struct {
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type ErrorResp struct {
	Error string `json:"error"`
}

type OkResp struct {
	Ok bool `json:"ok"`
}

type RoleCounts struct {
	Repeaters  int `json:"repeaters"`
	Rooms      int `json:"rooms"`
	Companions int `json:"companions"`
	Sensors    int `json:"sensors"`
}

type HistogramBin struct {
	X     float64 `json:"x"`
	W     float64 `json:"w"`
	Count int     `json:"count"`
}

type Histogram struct {
	Bins []HistogramBin `json:"bins"`
	Min  float64        `json:"min"`
	Max  float64        `json:"max"`
}

type SignalStats struct {
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Avg    float64 `json:"avg"`
	Median float64 `json:"median"`
	Stddev float64 `json:"stddev"`
}

type TimeBucket struct {
	Label  *string `json:"label,omitempty"`
	Count  int     `json:"count"`
	Bucket *string `json:"bucket,omitempty"`
}

// ─── Stats ─────────────────────────────────────────────────────────────────────

type StatsResponse struct {
	TotalPackets       int        `json:"totalPackets"`
	TotalTransmissions *int       `json:"totalTransmissions"`
	TotalObservations  int        `json:"totalObservations"`
	TotalNodes         int        `json:"totalNodes"`
	TotalNodesAllTime  int        `json:"totalNodesAllTime"`
	TotalObservers     int        `json:"totalObservers"`
	PacketsLastHour    int        `json:"packetsLastHour"`
	PacketsLast24h     int        `json:"packetsLast24h"`
	Engine             string     `json:"engine"`
	Version            string     `json:"version"`
	Commit             string     `json:"commit"`
	BuildTime          string     `json:"buildTime"`
	Counts             RoleCounts `json:"counts"`
	Backfilling        bool       `json:"backfilling"`
	BackfillProgress   float64    `json:"backfillProgress"`
}

// ─── Health ────────────────────────────────────────────────────────────────────

type MemoryStats struct {
	RSS       int `json:"rss"`
	HeapUsed  int `json:"heapUsed"`
	HeapTotal int `json:"heapTotal"`
	External  int `json:"external"`
}

type EventLoopStats struct {
	CurrentLagMs float64 `json:"currentLagMs"`
	MaxLagMs     float64 `json:"maxLagMs"`
	P50Ms        float64 `json:"p50Ms"`
	P95Ms        float64 `json:"p95Ms"`
	P99Ms        float64 `json:"p99Ms"`
}

type CacheStats struct {
	Entries    int     `json:"entries"`
	Hits       int64   `json:"hits"`
	Misses     int64   `json:"misses"`
	StaleHits  int     `json:"staleHits"`
	Recomputes int64   `json:"recomputes"`
	HitRate    float64 `json:"hitRate"`
}

// PerfCacheStats uses "size" key instead of "entries" (matching Node.js /api/perf shape).
type PerfCacheStats struct {
	Size       int     `json:"size"`
	Hits       int64   `json:"hits"`
	Misses     int64   `json:"misses"`
	StaleHits  int     `json:"staleHits"`
	Recomputes int64   `json:"recomputes"`
	HitRate    float64 `json:"hitRate"`
}

type WebSocketStatsResp struct {
	Clients int `json:"clients"`
}

type HealthPacketStoreStats struct {
	Packets     int     `json:"packets"`
	EstimatedMB float64 `json:"estimatedMB"`
}

type SlowQuery struct {
	Path   string  `json:"path"`
	Ms     float64 `json:"ms"`
	Time   string  `json:"time"`
	Status int     `json:"status"`
}

type HealthPerfStats struct {
	TotalRequests int         `json:"totalRequests"`
	AvgMs         float64     `json:"avgMs"`
	SlowQueries   int         `json:"slowQueries"`
	RecentSlow    []SlowQuery `json:"recentSlow"`
}

type HealthResponse struct {
	Status      string                 `json:"status"`
	Engine      string                 `json:"engine"`
	Version     string                 `json:"version"`
	Commit      string                 `json:"commit"`
	BuildTime   string                 `json:"buildTime"`
	Uptime      int                    `json:"uptime"`
	UptimeHuman string                 `json:"uptimeHuman"`
	Memory      MemoryStats            `json:"memory"`
	EventLoop   EventLoopStats         `json:"eventLoop"`
	Cache       CacheStats             `json:"cache"`
	WebSocket   WebSocketStatsResp     `json:"websocket"`
	PacketStore HealthPacketStoreStats `json:"packetStore"`
	Perf        HealthPerfStats        `json:"perf"`
}

// ─── Perf ──────────────────────────────────────────────────────────────────────

type EndpointStatsResp struct {
	Count int     `json:"count"`
	AvgMs float64 `json:"avgMs"`
	P50Ms float64 `json:"p50Ms"`
	P95Ms float64 `json:"p95Ms"`
	MaxMs float64 `json:"maxMs"`
}

type PacketStoreIndexes struct {
	ByHash          int `json:"byHash"`
	ByObserver      int `json:"byObserver"`
	ByNode          int `json:"byNode"`
	AdvertByObserver int `json:"advertByObserver"`
}

type PerfPacketStoreStats struct {
	TotalLoaded       int                `json:"totalLoaded"`
	TotalObservations int                `json:"totalObservations"`
	Evicted           int                `json:"evicted"`
	Inserts           int64              `json:"inserts"`
	Queries           int64              `json:"queries"`
	InMemory          int                `json:"inMemory"`
	SqliteOnly        bool               `json:"sqliteOnly"`
	MaxPackets        int                `json:"maxPackets"`
	EstimatedMB       float64            `json:"estimatedMB"`
	TrackedMB         float64            `json:"trackedMB"`
	MaxMB             int                `json:"maxMB"`
	Indexes           PacketStoreIndexes `json:"indexes"`
}

type WalPages struct {
	Total        int `json:"total"`
	Checkpointed int `json:"checkpointed"`
	Busy         int `json:"busy"`
}

type SqliteRowCounts struct {
	Transmissions int `json:"transmissions"`
	Observations  int `json:"observations"`
	Nodes         int `json:"nodes"`
	Observers     int `json:"observers"`
}

type SqliteStats struct {
	DbSizeMB   float64          `json:"dbSizeMB"`
	WalSizeMB  float64          `json:"walSizeMB"`
	FreelistMB float64          `json:"freelistMB"`
	WalPages   *WalPages        `json:"walPages"`
	Rows       *SqliteRowCounts `json:"rows"`
}

type PerfResponse struct {
	Uptime        int                           `json:"uptime"`
	TotalRequests int64                         `json:"totalRequests"`
	AvgMs         float64                       `json:"avgMs"`
	Endpoints     map[string]*EndpointStatsResp `json:"endpoints"`
	SlowQueries   []SlowQuery                   `json:"slowQueries"`
	Cache         PerfCacheStats                `json:"cache"`
	PacketStore   *PerfPacketStoreStats         `json:"packetStore"`
	Sqlite        *SqliteStats                  `json:"sqlite"`
	GoRuntime     *GoRuntimeStats               `json:"goRuntime,omitempty"`
}

// GoRuntimeStats holds Go runtime metrics for the perf endpoint.
type GoRuntimeStats struct {
	Goroutines   int     `json:"goroutines"`
	NumGC        uint32  `json:"numGC"`
	PauseTotalMs float64 `json:"pauseTotalMs"`
	LastPauseMs  float64 `json:"lastPauseMs"`
	HeapAllocMB  float64 `json:"heapAllocMB"`
	HeapSysMB    float64 `json:"heapSysMB"`
	HeapInuseMB  float64 `json:"heapInuseMB"`
	HeapIdleMB   float64 `json:"heapIdleMB"`
	NumCPU       int     `json:"numCPU"`
}

// ─── Packets ───────────────────────────────────────────────────────────────────

type TransmissionResp struct {
	ID               int              `json:"id"`
	RawHex           interface{}      `json:"raw_hex"`
	Hash             string           `json:"hash"`
	FirstSeen        string           `json:"first_seen"`
	Timestamp        string           `json:"timestamp"`
	RouteType        interface{}      `json:"route_type"`
	PayloadType      interface{}      `json:"payload_type"`
	PayloadVersion   interface{}      `json:"payload_version,omitempty"`
	DecodedJSON      interface{}      `json:"decoded_json"`
	ObservationCount int              `json:"observation_count"`
	ObserverID       interface{}      `json:"observer_id"`
	ObserverName     interface{}      `json:"observer_name"`
	SNR              interface{}      `json:"snr"`
	RSSI             interface{}      `json:"rssi"`
	PathJSON         interface{}      `json:"path_json"`
	ResolvedPath     []*string        `json:"resolved_path,omitempty"`
	Direction        interface{}      `json:"direction"`
	Score            interface{}      `json:"score,omitempty"`
	Observations     []ObservationResp `json:"observations,omitempty"`
}

type ObservationResp struct {
	ID             int         `json:"id"`
	TransmissionID interface{} `json:"transmission_id,omitempty"`
	Hash           interface{} `json:"hash,omitempty"`
	ObserverID     interface{} `json:"observer_id"`
	ObserverName   interface{} `json:"observer_name"`
	SNR            interface{} `json:"snr"`
	RSSI           interface{} `json:"rssi"`
	PathJSON       interface{} `json:"path_json"`
	ResolvedPath   []*string   `json:"resolved_path,omitempty"`
	Timestamp      interface{} `json:"timestamp"`
}

type GroupedPacketResp struct {
	Hash             string      `json:"hash"`
	FirstSeen        string      `json:"first_seen"`
	Count            int         `json:"count"`
	ObserverCount    int         `json:"observer_count"`
	Latest           string      `json:"latest"`
	ObserverID       interface{} `json:"observer_id"`
	ObserverName     interface{} `json:"observer_name"`
	PathJSON         interface{} `json:"path_json"`
	PayloadType      int         `json:"payload_type"`
	RouteType        int         `json:"route_type"`
	RawHex           string      `json:"raw_hex"`
	DecodedJSON      interface{} `json:"decoded_json"`
	ObservationCount int         `json:"observation_count"`
	SNR              interface{} `json:"snr"`
	RSSI             interface{} `json:"rssi"`
}

type PacketListResponse struct {
	Packets []TransmissionResp `json:"packets"`
	Total   int                `json:"total"`
	Limit   int                `json:"limit,omitempty"`
	Offset  int                `json:"offset,omitempty"`
}

type PacketTimestampsResponse struct {
	Timestamps []string `json:"timestamps"`
}

type PacketDetailResponse struct {
	Packet           interface{}       `json:"packet"`
	Path             []interface{}     `json:"path"`
	Breakdown        *Breakdown        `json:"breakdown"`
	ObservationCount int               `json:"observation_count"`
	Observations     []ObservationResp `json:"observations,omitempty"`
}

type PacketIngestResponse struct {
	ID      int64       `json:"id"`
	Decoded interface{} `json:"decoded"`
}

type DecodeResponse struct {
	Decoded interface{} `json:"decoded"`
}

// ─── Nodes ─────────────────────────────────────────────────────────────────────

type NodeResp struct {
	PublicKey           string      `json:"public_key"`
	Name                interface{} `json:"name"`
	Role                interface{} `json:"role"`
	Lat                 interface{} `json:"lat"`
	Lon                 interface{} `json:"lon"`
	LastSeen            interface{} `json:"last_seen"`
	FirstSeen           interface{} `json:"first_seen"`
	AdvertCount         int         `json:"advert_count"`
	HashSize            interface{} `json:"hash_size,omitempty"`
	HashSizeInconsistent bool       `json:"hash_size_inconsistent,omitempty"`
	HashSizesSeen       []int       `json:"hash_sizes_seen,omitempty"`
	LastHeard           interface{} `json:"last_heard,omitempty"`
}

type NodeListResponse struct {
	Nodes  []map[string]interface{} `json:"nodes"`
	Total  int                      `json:"total"`
	Counts map[string]int           `json:"counts"`
}

type NodeSearchResponse struct {
	Nodes []map[string]interface{} `json:"nodes"`
}

type NodeDetailResponse struct {
	Node          map[string]interface{}   `json:"node"`
	RecentAdverts []map[string]interface{} `json:"recentAdverts"`
}

type NodeStatsResp struct {
	TotalTransmissions int         `json:"totalTransmissions"`
	TotalObservations  int         `json:"totalObservations"`
	TotalPackets       int         `json:"totalPackets"`
	PacketsToday       int         `json:"packetsToday"`
	AvgSnr             interface{} `json:"avgSnr"`
	LastHeard          interface{} `json:"lastHeard"`
	AvgHops            interface{} `json:"avgHops,omitempty"`
}

type NodeObserverStatsResp struct {
	ObserverID   interface{} `json:"observer_id"`
	ObserverName interface{} `json:"observer_name"`
	PacketCount  int         `json:"packetCount"`
	AvgSnr       interface{} `json:"avgSnr"`
	AvgRssi      interface{} `json:"avgRssi"`
	IATA         interface{} `json:"iata,omitempty"`
	FirstSeen    interface{} `json:"firstSeen,omitempty"`
	LastSeen     interface{} `json:"lastSeen,omitempty"`
}

type BulkHealthEntry struct {
	PublicKey string                  `json:"public_key"`
	Name      interface{}             `json:"name"`
	Role      interface{}             `json:"role"`
	Lat       interface{}             `json:"lat"`
	Lon       interface{}             `json:"lon"`
	Stats     NodeStatsResp           `json:"stats"`
	Observers []NodeObserverStatsResp `json:"observers"`
}

type NetworkStatusResponse struct {
	Total      int            `json:"total"`
	Active     int            `json:"active"`
	Degraded   int            `json:"degraded"`
	Silent     int            `json:"silent"`
	RoleCounts map[string]int `json:"roleCounts"`
}

// ─── Paths ─────────────────────────────────────────────────────────────────────

type PathHopResp struct {
	Prefix string      `json:"prefix"`
	Name   string      `json:"name"`
	Pubkey interface{} `json:"pubkey"`
	Lat    interface{} `json:"lat"`
	Lon    interface{} `json:"lon"`
}

type PathEntryResp struct {
	Hops       []PathHopResp `json:"hops"`
	Count      int           `json:"count"`
	LastSeen   interface{}   `json:"lastSeen"`
	SampleHash string        `json:"sampleHash"`
}

type NodePathsResponse struct {
	Node               map[string]interface{} `json:"node"`
	Paths              []PathEntryResp        `json:"paths"`
	TotalPaths         int                    `json:"totalPaths"`
	TotalTransmissions int                    `json:"totalTransmissions"`
}

// ─── Node Analytics ────────────────────────────────────────────────────────────

type TimeRangeResp struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`
}

type SnrTrendEntry struct {
	Timestamp    string      `json:"timestamp"`
	SNR          interface{} `json:"snr"`
	RSSI         interface{} `json:"rssi"`
	ObserverID   interface{} `json:"observer_id"`
	ObserverName interface{} `json:"observer_name"`
}

type PayloadTypeCount struct {
	PayloadType int `json:"payload_type"`
	Count       int `json:"count"`
}

type HopDistEntry struct {
	Hops  string `json:"hops"`
	Count int    `json:"count"`
}

type PeerInteraction struct {
	PeerKey      string `json:"peer_key"`
	PeerName     string `json:"peer_name"`
	MessageCount int    `json:"messageCount"`
	LastContact  string `json:"lastContact"`
}

type HeatmapCell struct {
	DayOfWeek int `json:"dayOfWeek"`
	Hour      int `json:"hour"`
	Count     int `json:"count"`
}

type ComputedNodeStats struct {
	AvailabilityPct     float64     `json:"availabilityPct"`
	LongestSilenceMs    int         `json:"longestSilenceMs"`
	LongestSilenceStart interface{} `json:"longestSilenceStart"`
	SignalGrade         string      `json:"signalGrade"`
	SnrMean             float64     `json:"snrMean"`
	SnrStdDev           float64     `json:"snrStdDev"`
	RelayPct            float64     `json:"relayPct"`
	TotalPackets        int         `json:"totalPackets"`
	UniqueObservers     int         `json:"uniqueObservers"`
	UniquePeers         int         `json:"uniquePeers"`
	AvgPacketsPerDay    float64     `json:"avgPacketsPerDay"`
}

type NodeAnalyticsResponse struct {
	Node                map[string]interface{}  `json:"node"`
	TimeRange           TimeRangeResp           `json:"timeRange"`
	ActivityTimeline    []TimeBucket            `json:"activityTimeline"`
	SnrTrend            []SnrTrendEntry         `json:"snrTrend"`
	PacketTypeBreakdown []PayloadTypeCount      `json:"packetTypeBreakdown"`
	ObserverCoverage    []NodeObserverStatsResp `json:"observerCoverage"`
	HopDistribution     []HopDistEntry          `json:"hopDistribution"`
	PeerInteractions    []PeerInteraction       `json:"peerInteractions"`
	UptimeHeatmap       []HeatmapCell           `json:"uptimeHeatmap"`
	ComputedStats       ComputedNodeStats       `json:"computedStats"`
}

// ─── Analytics — RF ────────────────────────────────────────────────────────────

type PayloadTypeSignal struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	Avg   float64 `json:"avg"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}

type SignalOverTimeEntry struct {
	Hour   string  `json:"hour"`
	Count  int     `json:"count"`
	AvgSnr float64 `json:"avgSnr"`
}

type ScatterPoint struct {
	SNR  float64 `json:"snr"`
	RSSI float64 `json:"rssi"`
}

type PayloadTypeEntry struct {
	Type  interface{} `json:"type"`
	Name  string      `json:"name"`
	Count int         `json:"count"`
}

type HourlyCount struct {
	Hour  string `json:"hour"`
	Count int    `json:"count"`
}

type RFAnalyticsResponse struct {
	TotalPackets       int                   `json:"totalPackets"`
	TotalAllPackets    int                   `json:"totalAllPackets"`
	TotalTransmissions int                   `json:"totalTransmissions"`
	SNR                SignalStats           `json:"snr"`
	RSSI               SignalStats           `json:"rssi"`
	SnrValues          Histogram             `json:"snrValues"`
	RssiValues         Histogram             `json:"rssiValues"`
	PacketSizes        Histogram             `json:"packetSizes"`
	MinPacketSize      int                   `json:"minPacketSize"`
	MaxPacketSize      int                   `json:"maxPacketSize"`
	AvgPacketSize      float64               `json:"avgPacketSize"`
	PacketsPerHour     []HourlyCount         `json:"packetsPerHour"`
	PayloadTypes       []PayloadTypeEntry    `json:"payloadTypes"`
	SnrByType          []PayloadTypeSignal   `json:"snrByType"`
	SignalOverTime     []SignalOverTimeEntry `json:"signalOverTime"`
	ScatterData        []ScatterPoint        `json:"scatterData"`
	TimeSpanHours      float64               `json:"timeSpanHours"`
}

// ─── Analytics — Topology ──────────────────────────────────────────────────────

type TopologyHopDist struct {
	Hops  int `json:"hops"`
	Count int `json:"count"`
}

type TopRepeater struct {
	Hop    string      `json:"hop"`
	Count  int         `json:"count"`
	Name   interface{} `json:"name"`
	Pubkey interface{} `json:"pubkey"`
}

type TopPair struct {
	HopA    string      `json:"hopA"`
	HopB    string      `json:"hopB"`
	Count   int         `json:"count"`
	NameA   interface{} `json:"nameA"`
	NameB   interface{} `json:"nameB"`
	PubkeyA interface{} `json:"pubkeyA"`
	PubkeyB interface{} `json:"pubkeyB"`
}

type HopsVsSnr struct {
	Hops   int     `json:"hops"`
	Count  int     `json:"count"`
	AvgSnr float64 `json:"avgSnr"`
}

type ObserverRef struct {
	ID   string      `json:"id"`
	Name interface{} `json:"name"`
}

type ReachNode struct {
	Hop       string      `json:"hop"`
	Name      interface{} `json:"name"`
	Pubkey    interface{} `json:"pubkey"`
	Count     int         `json:"count"`
	DistRange interface{} `json:"distRange,omitempty"`
}

type ReachRing struct {
	Hops  int         `json:"hops"`
	Nodes []ReachNode `json:"nodes"`
}

type ObserverReach struct {
	ObserverName string      `json:"observer_name"`
	Rings        []ReachRing `json:"rings"`
}

type MultiObsObserver struct {
	ObserverID   string `json:"observer_id"`
	ObserverName string `json:"observer_name"`
	MinDist      int    `json:"minDist"`
	Count        int    `json:"count"`
}

type MultiObsNode struct {
	Hop       string             `json:"hop"`
	Name      interface{}        `json:"name"`
	Pubkey    interface{}        `json:"pubkey"`
	Observers []MultiObsObserver `json:"observers"`
}

type BestPathEntry struct {
	Hop          string      `json:"hop"`
	Name         interface{} `json:"name"`
	Pubkey       interface{} `json:"pubkey"`
	MinDist      int         `json:"minDist"`
	ObserverID   string      `json:"observer_id"`
	ObserverName string      `json:"observer_name"`
}

type TopologyResponse struct {
	UniqueNodes      int                       `json:"uniqueNodes"`
	AvgHops          float64                   `json:"avgHops"`
	MedianHops       float64                   `json:"medianHops"`
	MaxHops          int                       `json:"maxHops"`
	HopDistribution  []TopologyHopDist         `json:"hopDistribution"`
	TopRepeaters     []TopRepeater             `json:"topRepeaters"`
	TopPairs         []TopPair                 `json:"topPairs"`
	HopsVsSnr        []HopsVsSnr              `json:"hopsVsSnr"`
	Observers        []ObserverRef             `json:"observers"`
	PerObserverReach map[string]*ObserverReach `json:"perObserverReach"`
	MultiObsNodes    []MultiObsNode            `json:"multiObsNodes"`
	BestPathList     []BestPathEntry           `json:"bestPathList"`
}

// ─── Analytics — Channels ──────────────────────────────────────────────────────

type ChannelAnalyticsSummary struct {
	Hash         int    `json:"hash"`
	Name         string `json:"name"`
	Messages     int    `json:"messages"`
	Senders      int    `json:"senders"`
	LastActivity string `json:"lastActivity"`
	Encrypted    bool   `json:"encrypted"`
}

type TopSender struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type ChannelTimelineEntry struct {
	Hour    string `json:"hour"`
	Channel string `json:"channel"`
	Count   int    `json:"count"`
}

type ChannelAnalyticsResponse struct {
	ActiveChannels  int                       `json:"activeChannels"`
	Decryptable     int                       `json:"decryptable"`
	Channels        []ChannelAnalyticsSummary `json:"channels"`
	TopSenders      []TopSender               `json:"topSenders"`
	ChannelTimeline []ChannelTimelineEntry    `json:"channelTimeline"`
	MsgLengths      []int                     `json:"msgLengths"`
}

// ─── Analytics — Distance ──────────────────────────────────────────────────────

type DistanceSummary struct {
	TotalHops  int     `json:"totalHops"`
	TotalPaths int     `json:"totalPaths"`
	AvgDist    float64 `json:"avgDist"`
	MaxDist    float64 `json:"maxDist"`
}

type DistanceHop struct {
	FromName  string      `json:"fromName"`
	FromPk    string      `json:"fromPk"`
	ToName    string      `json:"toName"`
	ToPk      string      `json:"toPk"`
	Dist      float64     `json:"dist"`
	Type      string      `json:"type"`
	SNR       interface{} `json:"snr"`
	Hash      string      `json:"hash"`
	Timestamp string      `json:"timestamp"`
}

type DistancePathHop struct {
	FromName string  `json:"fromName"`
	FromPk   string  `json:"fromPk"`
	ToName   string  `json:"toName"`
	ToPk     string  `json:"toPk"`
	Dist     float64 `json:"dist"`
}

type DistancePath struct {
	Hash      string            `json:"hash"`
	TotalDist float64           `json:"totalDist"`
	HopCount  int               `json:"hopCount"`
	Timestamp string            `json:"timestamp"`
	Hops      []DistancePathHop `json:"hops"`
}

type CategoryDistStats struct {
	Count  int     `json:"count"`
	Avg    float64 `json:"avg"`
	Median float64 `json:"median"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

type DistOverTimeEntry struct {
	Hour  string  `json:"hour"`
	Avg   float64 `json:"avg"`
	Count int     `json:"count"`
}

type DistanceAnalyticsResponse struct {
	Summary       DistanceSummary                `json:"summary"`
	TopHops       []DistanceHop                  `json:"topHops"`
	TopPaths      []DistancePath                 `json:"topPaths"`
	CatStats      map[string]*CategoryDistStats  `json:"catStats"`
	DistHistogram *Histogram                     `json:"distHistogram"`
	DistOverTime  []DistOverTimeEntry            `json:"distOverTime"`
}

// ─── Analytics — Hash Sizes ────────────────────────────────────────────────────

type HashSizeHourly struct {
	Hour  string `json:"hour"`
	Size1 int    `json:"1"`
	Size2 int    `json:"2"`
	Size3 int    `json:"3"`
}

type HashSizeHop struct {
	Hex    string      `json:"hex"`
	Size   int         `json:"size"`
	Count  int         `json:"count"`
	Name   interface{} `json:"name"`
	Pubkey interface{} `json:"pubkey"`
}

type MultiByteNode struct {
	Name     string      `json:"name"`
	HashSize int         `json:"hashSize"`
	Packets  int         `json:"packets"`
	LastSeen string      `json:"lastSeen"`
	Pubkey   interface{} `json:"pubkey"`
}

type HashSizeAnalyticsResponse struct {
	Total          int               `json:"total"`
	Distribution   map[string]int    `json:"distribution"`
	Hourly         []HashSizeHourly  `json:"hourly"`
	TopHops        []HashSizeHop     `json:"topHops"`
	MultiByteNodes []MultiByteNode   `json:"multiByteNodes"`
}

// ─── Analytics — Subpaths ──────────────────────────────────────────────────────

type SubpathResp struct {
	Path    string   `json:"path"`
	RawHops []string `json:"rawHops"`
	Count   int      `json:"count"`
	Hops    int      `json:"hops"`
	Pct     float64  `json:"pct"`
}

type SubpathsResponse struct {
	Subpaths   []SubpathResp `json:"subpaths"`
	TotalPaths int           `json:"totalPaths"`
}

type SubpathNode struct {
	Hop    string      `json:"hop"`
	Name   string      `json:"name"`
	Lat    interface{} `json:"lat"`
	Lon    interface{} `json:"lon"`
	Pubkey interface{} `json:"pubkey"`
}

type SubpathSignal struct {
	AvgSnr  interface{} `json:"avgSnr"`
	AvgRssi interface{} `json:"avgRssi"`
	Samples int         `json:"samples"`
}

type ParentPath struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

type SubpathObserver struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type SubpathDetailResponse struct {
	Hops             []string          `json:"hops"`
	Nodes            []SubpathNode     `json:"nodes"`
	TotalMatches     int               `json:"totalMatches"`
	FirstSeen        interface{}       `json:"firstSeen"`
	LastSeen         interface{}       `json:"lastSeen"`
	Signal           SubpathSignal     `json:"signal"`
	HourDistribution []int             `json:"hourDistribution"`
	ParentPaths      []ParentPath      `json:"parentPaths"`
	Observers        []SubpathObserver `json:"observers"`
}

// ─── Channels ──────────────────────────────────────────────────────────────────

type ChannelResp struct {
	Hash         string      `json:"hash"`
	Name         string      `json:"name"`
	LastMessage  interface{} `json:"lastMessage"`
	LastSender   interface{} `json:"lastSender"`
	MessageCount int         `json:"messageCount"`
	LastActivity string      `json:"lastActivity"`
}

type ChannelListResponse struct {
	Channels []map[string]interface{} `json:"channels"`
}

type ChannelMessageResp struct {
	Sender          string      `json:"sender"`
	Text            string      `json:"text"`
	Timestamp       string      `json:"timestamp"`
	SenderTimestamp interface{} `json:"sender_timestamp"`
	PacketID        int64       `json:"packetId"`
	PacketHash      string      `json:"packetHash"`
	Repeats         int         `json:"repeats"`
	Observers       []string    `json:"observers"`
	Hops            int         `json:"hops"`
	SNR             interface{} `json:"snr"`
}

type ChannelMessagesResponse struct {
	Messages []map[string]interface{} `json:"messages"`
	Total    int                      `json:"total"`
}

// ─── Observers ─────────────────────────────────────────────────────────────────

type ObserverResp struct {
	ID              string      `json:"id"`
	Name            interface{} `json:"name"`
	IATA            interface{} `json:"iata"`
	LastSeen        interface{} `json:"last_seen"`
	FirstSeen       interface{} `json:"first_seen"`
	PacketCount     int         `json:"packet_count"`
	Model           interface{} `json:"model"`
	Firmware        interface{} `json:"firmware"`
	ClientVersion   interface{} `json:"client_version"`
	Radio           interface{} `json:"radio"`
	BatteryMv       interface{} `json:"battery_mv"`
	UptimeSecs      interface{} `json:"uptime_secs"`
	NoiseFloor      interface{} `json:"noise_floor"`
	PacketsLastHour int         `json:"packetsLastHour"`
	Lat             interface{} `json:"lat"`
	Lon             interface{} `json:"lon"`
	NodeRole        interface{} `json:"nodeRole"`
}

type ObserverListResponse struct {
	Observers  []ObserverResp `json:"observers"`
	ServerTime string         `json:"server_time"`
}

type SnrDistributionEntry struct {
	Range string `json:"range"`
	Count int    `json:"count"`
}

type ObserverAnalyticsResponse struct {
	Timeline        []TimeBucket           `json:"timeline"`
	PacketTypes     map[string]int         `json:"packetTypes"`
	NodesTimeline   []TimeBucket           `json:"nodesTimeline"`
	SnrDistribution []SnrDistributionEntry `json:"snrDistribution"`
	RecentPackets   []map[string]interface{} `json:"recentPackets"`
}

// ─── Traces ────────────────────────────────────────────────────────────────────

type TraceEntry struct {
	Observer     interface{} `json:"observer"`
	ObserverName interface{} `json:"observer_name"`
	Time         string      `json:"time"`
	SNR          interface{} `json:"snr"`
	RSSI         interface{} `json:"rssi"`
	PathJSON     interface{} `json:"path_json"`
}

type TraceResponse struct {
	Traces []map[string]interface{} `json:"traces"`
}

// ─── Resolve Hops ──────────────────────────────────────────────────────────────

type HopCandidate struct {
	Name          interface{} `json:"name"`
	Pubkey        string      `json:"pubkey"`
	Lat           interface{} `json:"lat"`
	Lon           interface{} `json:"lon"`
	AffinityScore *float64    `json:"affinityScore"`
}

type HopResolution struct {
	Name          interface{}    `json:"name"`
	Pubkey        interface{}    `json:"pubkey,omitempty"`
	Ambiguous     *bool          `json:"ambiguous,omitempty"`
	Candidates    []HopCandidate `json:"candidates"`
	Conflicts     []interface{}  `json:"conflicts"`
	BestCandidate *string        `json:"bestCandidate,omitempty"`
	Confidence    string         `json:"confidence,omitempty"`
}

type ResolveHopsResponse struct {
	Resolved map[string]*HopResolution `json:"resolved"`
}

// ─── Config ────────────────────────────────────────────────────────────────────

type ThemeResponse struct {
	Branding   map[string]interface{} `json:"branding"`
	Theme      map[string]interface{} `json:"theme"`
	ThemeDark  map[string]interface{} `json:"themeDark"`
	NodeColors map[string]interface{} `json:"nodeColors"`
	TypeColors map[string]interface{} `json:"typeColors"`
	Home       interface{}            `json:"home"`
}

type MapConfigResponse struct {
	Center []float64 `json:"center"`
	Zoom   int       `json:"zoom"`
}

type ClientConfigResponse struct {
	Roles              interface{} `json:"roles"`
	HealthThresholds   interface{} `json:"healthThresholds"`
	Tiles              interface{} `json:"tiles"`
	SnrThresholds      interface{} `json:"snrThresholds"`
	DistThresholds     interface{} `json:"distThresholds"`
	MaxHopDist         interface{} `json:"maxHopDist"`
	Limits             interface{} `json:"limits"`
	PerfSlowMs         interface{} `json:"perfSlowMs"`
	WsReconnectMs      interface{} `json:"wsReconnectMs"`
	CacheInvalidateMs  interface{} `json:"cacheInvalidateMs"`
	ExternalUrls       interface{} `json:"externalUrls"`
	PropagationBufferMs float64         `json:"propagationBufferMs"`
	Timestamps          TimestampConfig `json:"timestamps"`
	DebugAffinity       bool            `json:"debugAffinity,omitempty"`
}

// ─── IATA Coords ───────────────────────────────────────────────────────────────

type IataCoord struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type IataCoordsResponse struct {
	Coords map[string]IataCoord `json:"coords"`
}

// ─── Audio Lab ─────────────────────────────────────────────────────────────────

type AudioLabPacket struct {
	Hash             interface{} `json:"hash"`
	RawHex           interface{} `json:"raw_hex"`
	DecodedJSON      interface{} `json:"decoded_json"`
	ObservationCount int         `json:"observation_count"`
	PayloadType      int         `json:"payload_type"`
	PathJSON         interface{} `json:"path_json"`
	ObserverID       interface{} `json:"observer_id"`
	Timestamp        interface{} `json:"timestamp"`
}

type AudioLabBucketsResponse struct {
	Buckets map[string][]AudioLabPacket `json:"buckets"`
}

// ─── WebSocket ─────────────────────────────────────────────────────────────────

type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}
