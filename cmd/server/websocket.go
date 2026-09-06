package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Hub manages WebSocket clients and broadcasts.
type Hub struct {
	mu             sync.RWMutex
	clients        map[*Client]bool
	upgrader       websocket.Upgrader
	allowedOrigins []string   // exact-match allowlist for /ws CheckOrigin (see SetAllowedOrigins)
	limits         *wsLimiter // #1794: per-IP caps and deny list; nil allows everything
}

// SetAllowedOrigins configures the exact-match origin allowlist consulted by
// the WebSocket upgrader's CheckOrigin. The "*" wildcard is deliberately NOT
// honored here (it IS honored by the HTTP CORS middleware): OWASP's
// WebSocket Security Cheat Sheet recommends an explicit allowlist for CSWSH
// defense. If "*" appears in the slice, it is ignored and a startup WARN is
// logged once per call.
//
// See: https://cheatsheetseries.owasp.org/cheatsheets/WebSocket_Security_Cheat_Sheet.html
func (h *Hub) SetAllowedOrigins(origins []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.allowedOrigins = append(h.allowedOrigins[:0], origins...)
	for _, o := range origins {
		if o == "*" {
			log.Println(`[ws] WARNING: CORSAllowedOrigins contains "*" — CORS allows any origin for XHR, but /ws upgrade enforces explicit allowlist only (OWASP CSWSH guidance). Add specific origins to allow cross-origin WebSocket clients.`)
			break
		}
	}
}

// checkOrigin is the gorilla/websocket Upgrader.CheckOrigin hook. Rules:
//   - empty Origin header → allow (non-browser client; rate-limit / IP gate
//     is handled separately, see #1794).
//   - Origin host == request Host (same-origin) → allow.
//   - Origin in allowedOrigins by exact case-insensitive match → allow.
//   - "*" in allowedOrigins is ignored (see SetAllowedOrigins).
//   - anything else → reject (gorilla returns 403).
func (h *Hub) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	h.mu.RLock()
	allowed := h.allowedOrigins
	h.mu.RUnlock()
	for _, o := range allowed {
		if o == "*" {
			continue // deliberately not honored — see SetAllowedOrigins
		}
		if strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// Client is a single WebSocket connection.
type Client struct {
	conn      *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
	// release returns this connection's slot to the per-IP concurrent cap
	// (#1794). Always non-nil so Unregister needs no nil check; it is a no-op
	// when limits are disabled.
	release func()
}

// ConfigureLimits installs the #1794 transport limits. Called once at
// startup, before the listener binds, so no upgrade can race a half-built
// limiter. Passing zero values for both caps leaves only the deny list
// active, which is a legitimate configuration.
func (h *Hub) ConfigureLimits(maxConnsPerIP, upgradesPerMin int, trustedProxies, deny []string) {
	l := newWSLimiter()
	l.maxConnsPerIP = maxConnsPerIP
	l.upgradesPerMin = upgradesPerMin
	l.trustedProxies = parseCIDRList(trustedProxies, "webSocket.trustedProxies")
	l.denyNets = parseCIDRList(deny, "webSocket.deny")
	h.mu.Lock()
	h.limits = l
	h.mu.Unlock()
	if len(l.denyNets) > 0 {
		log.Printf("[ws] deny list active: %d network(s)", len(l.denyNets))
	}
	if len(l.trustedProxies) == 0 && (maxConnsPerIP > 0 || upgradesPerMin > 0) {
		log.Printf("[ws] per-IP limits configured with no trustedProxies: they apply only to " +
			"directly-connected clients. Behind a reverse proxy, set webSocket.trustedProxies.")
	}
}

func NewHub() *Hub {
	h := &Hub{
		clients: make(map[*Client]bool),
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin:     h.checkOrigin,
	}
	return h
}

func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
	log.Printf("[ws] client connected (%d total)", h.ClientCount())
}

func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		c.closeOnce.Do(func() { close(c.send) })
	}
	h.mu.Unlock()
	// #1794: outside the lock — release takes the limiter's own mutex, and
	// holding two locks in one order here and the other order elsewhere is how
	// deadlocks are made. Idempotent, so a double Unregister cannot over-credit.
	if c.release != nil {
		c.release()
	}
	log.Printf("[ws] client disconnected (%d total)", h.ClientCount())
}

// Close gracefully disconnects all WebSocket clients.
func (h *Hub) Close() {
	h.mu.Lock()
	for c := range h.clients {
		c.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
			time.Now().Add(3*time.Second),
		)
		c.closeOnce.Do(func() { close(c.send) })
		delete(h.clients, c)
	}
	h.mu.Unlock()
	log.Println("[ws] all clients disconnected")
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[ws] marshal error: %v", err)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- data:
		default:
			// Client buffer full — drop
		}
	}
}

// ServeWS handles the WebSocket upgrade and runs the client.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	// #1794: decide BEFORE the upgrade. Rejecting afterwards would already
	// have allocated the connection and completed the handshake, which is the
	// resource this is meant to protect.
	ok, reason, key, release := h.limits.allow(r)
	if !ok {
		status := http.StatusForbidden
		if reason == wsRejectRate {
			// 429 tells a well-behaved client to back off; 403 would read as
			// "never come back" for what is a temporary refusal.
			status = http.StatusTooManyRequests
		}
		log.Printf("[ws] reject ip=%s reason=%s", key, reason)
		http.Error(w, "websocket upgrade refused", status)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error: %v", err)
		release() // the slot was charged above and this connection never happened
		return
	}

	client := &Client{
		conn:    conn,
		send:    make(chan []byte, 256),
		release: release,
	}
	h.Register(client)

	go client.writePump()
	go client.readPump(h)
}

// wsOrStatic upgrades WebSocket requests at any path, serves static files otherwise.
func wsOrStatic(hub *Hub, static http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			hub.ServeWS(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})
}

func (c *Client) readPump(hub *Hub) {
	defer func() {
		hub.Unregister(c)
		c.conn.Close()
	}()
	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Poller watches for new transmissions in SQLite and broadcasts them.
type Poller struct {
	db       *DB
	hub      *Hub
	store    *PacketStore // optional: if set, new transmissions are ingested into memory
	interval time.Duration
	stop     chan struct{}
}

func NewPoller(db *DB, hub *Hub, interval time.Duration) *Poller {
	return &Poller{db: db, hub: hub, interval: interval, stop: make(chan struct{})}
}

func (p *Poller) Start() {
	lastID := p.db.GetMaxTransmissionID()
	lastObsID := p.db.GetMaxObservationID()
	// If the store already loaded data, use its max IDs as a floor.
	// This prevents replaying the entire DB when the DB query fails
	// (e.g., corrupted DB returns 0 from COALESCE).
	if p.store != nil {
		if storeMax := p.store.MaxTransmissionID(); storeMax > lastID {
			lastID = storeMax
		}
		if storeMaxObs := p.store.MaxObservationID(); storeMaxObs > lastObsID {
			lastObsID = storeMaxObs
		}
	}
	log.Printf("[poller] starting from transmission ID %d, obs ID %d, interval %v", lastID, lastObsID, p.interval)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if p.store != nil {
				// Ingest new transmissions into in-memory store and broadcast
				newTxs, newMax := p.store.IngestNewFromDB(lastID, 100)
				if newMax > lastID {
					lastID = newMax
				}
				// Ingest new observations for existing transmissions (fixes #174)
				nextObsID := lastObsID
				if err := p.db.conn.QueryRow(`
					SELECT COALESCE(MAX(id), ?) FROM (
						SELECT id FROM observations
						WHERE id > ?
						ORDER BY id ASC
						LIMIT 500
					)`, lastObsID, lastObsID).Scan(&nextObsID); err != nil {
					nextObsID = lastObsID
				}
				newObs := p.store.IngestNewObservations(lastObsID, 500)
				if nextObsID > lastObsID {
					lastObsID = nextObsID
				}
				if len(newTxs) > 0 {
					log.Printf("[broadcast] sending %d packets to %d clients (lastID now %d)", len(newTxs), p.hub.ClientCount(), lastID)
				}
				for _, tx := range newTxs {
					p.hub.Broadcast(WSMessage{
						Type: "packet",
						Data: tx,
					})
				}
				for _, obs := range newObs {
					p.hub.Broadcast(WSMessage{
						Type: "packet",
						Data: obs,
					})
				}
			} else {
				// Fallback: direct DB query (used when store is nil, e.g. tests)
				newTxs, err := p.db.GetNewTransmissionsSince(lastID, 100)
				if err != nil {
					log.Printf("[poller] error: %v", err)
					continue
				}
				for _, tx := range newTxs {
					id, _ := tx["id"].(int)
					if id > lastID {
						lastID = id
					}
					// Copy packet fields for the nested packet (avoids circular ref)
					pkt := make(map[string]interface{}, len(tx))
					for k, v := range tx {
						pkt[k] = v
					}
					tx["packet"] = pkt
					p.hub.Broadcast(WSMessage{
						Type: "packet",
						Data: tx,
					})
				}
			}
		case <-p.stop:
			return
		}
	}
}

func (p *Poller) Stop() {
	close(p.stop)
}
