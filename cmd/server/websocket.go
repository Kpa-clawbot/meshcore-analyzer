package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Hub manages WebSocket clients and broadcasts.
type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]bool
}

// Client is a single WebSocket connection.
type Client struct {
	conn *websocket.Conn
	send chan []byte
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*Client]bool),
	}
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
		close(c.send)
	}
	h.mu.Unlock()
	log.Printf("[ws] client disconnected (%d total)", h.ClientCount())
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
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error: %v", err)
		return
	}

	client := &Client{
		conn: conn,
		send: make(chan []byte, 256),
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
	log.Printf("[poller] starting from transmission ID %d, interval %v", lastID, p.interval)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if p.store != nil {
				// Ingest into in-memory store and broadcast
				newTxs, newMax := p.store.IngestNewFromDB(lastID, 100)
				if newMax > lastID {
					lastID = newMax
				}
				for _, tx := range newTxs {
					p.hub.Broadcast(map[string]interface{}{
						"type": "packet",
						"data": tx,
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
					p.hub.Broadcast(map[string]interface{}{
						"type": "packet",
						"data": tx,
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
