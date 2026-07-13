package websocket

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Event represents a WebSocket event
type Event struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

type broadcastMessage struct {
	data         []byte
	broadcastAll bool
	targetRole   string
}

// MessageHandler is called when a client sends a message via WebSocket
type MessageHandler func(client *Client, evt Event)

// Hub manages WebSocket connections
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan broadcastMessage
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex

	// Subscription support for event-type listeners
	subMu       sync.RWMutex
	subscribers map[chan Event]bool

	// OnMessage is called when a client sends a message (e.g., terminal send)
	// C4 fix: protected by onMsgMu for concurrent read/write
	onMsgMu   sync.RWMutex
	onMessage MessageHandler
}

// Client represents a WebSocket client
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	// UserID from JWT auth (set during upgrade)
	UserID string
	// Role from JWT auth (M3 fix: needed for command authorization)
	Role string
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return checkOrigin(r)
	},
}

// checkOrigin validates the WebSocket Origin header.
// Allows all origins — the frontend nginx proxy is the trust boundary.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser clients (e.g., curl) don't send Origin
		return true
	}

	_, err := url.Parse(origin)
	if err != nil {
		logger.Errorf("WebSocket origin parse error: %v", err)
		return false
	}

	return true
}

// NewHub creates a new WebSocket hub
func NewHub() *Hub {
	return &Hub{
		clients:     make(map[*Client]bool),
		broadcast:   make(chan broadcastMessage),
		register:    make(chan *Client),
		unregister:  make(chan *Client),
		subscribers: make(map[chan Event]bool),
	}
}

// SetOnMessage sets the callback for client-sent messages
// C4 fix: protected by onMsgMu
func (h *Hub) SetOnMessage(handler MessageHandler) {
	h.onMsgMu.Lock()
	h.onMessage = handler
	h.onMsgMu.Unlock()
}

// Run starts the hub event loop
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			logger.Infof("WebSocket client registered")

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			logger.Infof("WebSocket client unregistered")

		case message := <-h.broadcast:
			h.mu.Lock()
			for client := range h.clients {
				if !message.broadcastAll && (message.targetRole == "" || client.Role != message.targetRole) {
					continue
				}
				select {
				case client.send <- message.data:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.Unlock()

			// Also forward to subscribers (parsed as Event)
			var evt Event
			if err := json.Unmarshal(message.data, &evt); err == nil {
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
	h.broadcast <- broadcastMessage{data: data, broadcastAll: true}
}

// Subscribe returns a channel that receives all broadcast events
// Callers can use this to listen for specific event types
func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, 256)
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

// BroadcastEvent sends a structured event to all clients.
func (h *Hub) BroadcastEvent(eventType string, payload interface{}) {
	h.broadcastEvent(eventType, payload, true, "")
}

// BroadcastEventToRole sends a structured event only to external clients with
// the requested role. The target is trimmed before matching and a blank target
// fails closed for external clients. Internal subscribers still receive the
// event because they are trusted backend consumers.
func (h *Hub) BroadcastEventToRole(eventType string, payload interface{}, role string) {
	h.broadcastEvent(eventType, payload, false, strings.TrimSpace(role))
}

func (h *Hub) broadcastEvent(eventType string, payload interface{}, broadcastAll bool, targetRole string) {
	event := Event{
		Type:    eventType,
		Payload: payload,
	}
	data, err := json.Marshal(event)
	if err != nil {
		logger.Errorf("Failed to marshal event: %v", err)
		return
	}
	h.broadcast <- broadcastMessage{
		data:         data,
		broadcastAll: broadcastAll,
		targetRole:   targetRole,
	}
}

// HandleWebSocket handles WebSocket upgrade requests
func (h *Hub) HandleWebSocket(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Errorf("WebSocket upgrade failed: %v", err)
		return
	}

	// Extract user_id from JWT auth context (set by middleware)
	// S4 fix: JWT middleware sets user_id as uint (Claims.UserID), handle both types
	client := &Client{hub: h, conn: conn, send: make(chan []byte, 256)}
	if userID, exists := c.Get("user_id"); exists {
		switch v := userID.(type) {
		case string:
			client.UserID = v
		case uint:
			client.UserID = strconv.FormatUint(uint64(v), 10)
		case float64:
			client.UserID = strconv.FormatUint(uint64(v), 10)
		}
	}
	// M3 fix: also extract role for command authorization
	if role, exists := c.Get("role"); exists {
		if r, ok := role.(string); ok {
			client.Role = r
		}
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
				logger.Errorf("WebSocket error: %v", err)
			}
			break
		}

		// Parse and dispatch client-sent messages
		// C4 fix: read onMessage under RLock
		c.hub.onMsgMu.RLock()
		handler := c.hub.onMessage
		c.hub.onMsgMu.RUnlock()
		if handler != nil {
			var evt Event
			if err := json.Unmarshal(message, &evt); err == nil {
				handler(c, evt)
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
