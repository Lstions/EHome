package websocket

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Event represents a WebSocket event
type Event struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// MessageHandler is called when a client sends a message via WebSocket
type MessageHandler func(client *Client, evt Event)

// Hub manages WebSocket connections
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex

	// Subscription support for event-type listeners
	subMu       sync.RWMutex
	subscribers map[chan Event]bool

	// OnMessage is called when a client sends a message (e.g., terminal send)
	onMessage MessageHandler
}

// Client represents a WebSocket client
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	// UserID from JWT auth (set during upgrade)
	UserID string
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins in development
	},
}

// NewHub creates a new WebSocket hub
func NewHub() *Hub {
	return &Hub{
		clients:     make(map[*Client]bool),
		broadcast:   make(chan []byte),
		register:    make(chan *Client),
		unregister:  make(chan *Client),
		subscribers: make(map[chan Event]bool),
	}
}

// SetOnMessage sets the callback for client-sent messages
func (h *Hub) SetOnMessage(handler MessageHandler) {
	h.onMessage = handler
}

// Run starts the hub event loop
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Println("WebSocket client registered")

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			log.Println("WebSocket client unregistered")

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()

			// Also forward to subscribers (parsed as Event)
			var evt Event
			if err := json.Unmarshal(message, &evt); err == nil {
				h.subMu.RLock()
				for ch := range h.subscribers {
					select {
					case ch <- evt:
					default:
						// subscriber channel full, drop
					}
				}
				h.subMu.RUnlock()
			}
		}
	}
}

// Broadcast sends a message to all connected clients
func (h *Hub) Broadcast(data []byte) {
	h.broadcast <- data
}

// Subscribe returns a channel that receives all broadcast events
// Callers can use this to listen for specific event types
func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, 64)
	h.subMu.Lock()
	h.subscribers[ch] = true
	h.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscription channel
func (h *Hub) Unsubscribe(ch chan Event) {
	h.subMu.Lock()
	delete(h.subscribers, ch)
	h.subMu.Unlock()
	close(ch)
}

// BroadcastEvent sends a structured event to all clients
func (h *Hub) BroadcastEvent(eventType string, payload interface{}) {
	event := Event{
		Type:    eventType,
		Payload: payload,
	}
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("Failed to marshal event: %v", err)
		return
	}
	h.Broadcast(data)
}

// HandleWebSocket handles WebSocket upgrade requests
func (h *Hub) HandleWebSocket(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	// Extract user_id from JWT auth context (set by middleware)
	userID, _ := c.Get("user_id")

	client := &Client{hub: h, conn: conn, send: make(chan []byte, 256)}
	if uid, ok := userID.(string); ok {
		client.UserID = uid
	}
	h.register <- client

	go client.writePump()
	go client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		// Parse and dispatch client-sent messages
		if c.hub.onMessage != nil {
			var evt Event
			if err := json.Unmarshal(message, &evt); err == nil {
				c.hub.onMessage(c, evt)
			}
		}
	}
}

func (c *Client) writePump() {
	defer c.conn.Close()
	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			c.conn.WriteMessage(websocket.TextMessage, message)
		}
	}
}
