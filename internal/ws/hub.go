package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const maxMessageSize = 512

var (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

// Event is sent to clients. Each event includes type, trip_id, trip_status, and emitted_at for reliability.
type Event struct {
	Type       string      `json:"type"`
	TripID     string      `json:"trip_id,omitempty"`
	TripStatus string      `json:"trip_status,omitempty"`
	EmittedAt string      `json:"emitted_at,omitempty"` // RFC3339
	Payload    interface{} `json:"payload,omitempty"`
}

// client is a WebSocket connection subscribed to one or more trip IDs. userID is set when using auth (for disconnect logging).
type client struct {
	hub     *Hub
	conn    *websocket.Conn
	send    chan []byte
	tripIDs map[string]struct{}
	userID  int64
	mu      sync.Mutex
}

// Hub holds registered clients and broadcasts events by trip_id.
type Hub struct {
	mu             sync.RWMutex
	tripToClients  map[string]map[*client]struct{}
	clients        map[*client]struct{}
	register       chan *client
	unregister     chan *client
	broadcast      chan broadcastReq
}

type broadcastReq struct {
	tripID string
	event  Event
}

// NewHub creates a new Hub. Call Run() to start the loop.
func NewHub() *Hub {
	return &Hub{
		tripToClients: make(map[string]map[*client]struct{}),
		clients:       make(map[*client]struct{}),
		register:      make(chan *client),
		unregister:    make(chan *client),
		broadcast:     make(chan broadcastReq, 64),
	}
}

// Run runs the hub loop (blocking). Run in a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			for tripID := range c.tripIDs {
				if h.tripToClients[tripID] == nil {
					h.tripToClients[tripID] = make(map[*client]struct{})
				}
				h.tripToClients[tripID][c] = struct{}{}
			}
			h.mu.Unlock()

		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				for tripID := range c.tripIDs {
					delete(h.tripToClients[tripID], c)
					if len(h.tripToClients[tripID]) == 0 {
						delete(h.tripToClients, tripID)
					}
				}
				close(c.send)
			}
			h.mu.Unlock()

		case req := <-h.broadcast:
			h.mu.RLock()
			clients := h.tripToClients[req.tripID]
			if clients == nil {
				h.mu.RUnlock()
				continue
			}
			msg, err := json.Marshal(req.event)
			if err != nil {
				h.mu.RUnlock()
				continue
			}
			for c := range clients {
				select {
				case c.send <- msg:
				default:
				}
			}
			h.mu.RUnlock()
		}
	}
}

// BroadcastToTrip sends an event to all clients subscribed to the trip. Sets EmittedAt if empty.
func (h *Hub) BroadcastToTrip(tripID string, event Event) {
	if tripID == "" {
		return
	}
	event.TripID = tripID
	if event.EmittedAt == "" {
		event.EmittedAt = time.Now().UTC().Format(time.RFC3339)
	}
	select {
	case h.broadcast <- broadcastReq{tripID: tripID, event: event}:
	default:
		log.Printf("ws: broadcast queue full for trip %s", tripID)
	}
}

// Upgrader upgrades HTTP to WebSocket.
var Upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}
