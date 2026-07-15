package websocket

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

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
	data []byte
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
	validator func(subjectID uint, sessionVersion int64) bool
}

// Client represents a WebSocket client
type Client struct {
	hub            *Hub
	conn           *websocket.Conn
	send           chan []byte
	SubjectID      uint
	SessionVersion int64
	ExpiresAt      time.Time
	JTI            string
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return checkOrigin(r)
	},
}

// checkOrigin permits same-origin browsers and explicit configured origins.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser clients are still authenticated by the session middleware.
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		logger.Errorf("WebSocket origin parse error: %v", err)
		return false
	}

	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	for _, allowed := range strings.Split(os.Getenv("EHOME_ALLOWED_ORIGINS"), ",") {
		if strings.EqualFold(strings.TrimSpace(allowed), origin) {
			return true
		}
	}
	return false
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

func (h *Hub) SetSessionValidator(validator func(subjectID uint, sessionVersion int64) bool) {
	h.onMsgMu.Lock()
	h.validator = validator
	h.onMsgMu.Unlock()
}

func (h *Hub) DisconnectSubject(subjectID uint) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for client := range h.clients {
		if client.SubjectID == subjectID {
			delete(h.clients, client)
			close(client.send)
			count++
		}
	}
	return count
}

func (c *Client) SessionValid() bool {
	if c.hub == nil || c.SubjectID == 0 || c.SessionVersion <= 0 || c.ExpiresAt.IsZero() || !time.Now().Before(c.ExpiresAt) {
		return false
	}
	c.hub.onMsgMu.RLock()
	validator := c.hub.validator
	c.hub.onMsgMu.RUnlock()
	return validator != nil && validator(c.SubjectID, c.SessionVersion)
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
				if !client.SessionValid() {
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
	h.broadcast <- broadcastMessage{data: data}
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

// BroadcastEvent sends a structured event to authenticated clients.
func (h *Hub) BroadcastEvent(eventType string, payload interface{}) {
	h.broadcastEvent(eventType, payload)
}

func (h *Hub) BroadcastAuthenticatedEvent(eventType string, payload interface{}) {
	h.broadcastEvent(eventType, payload)
}

func (h *Hub) broadcastEvent(eventType string, payload interface{}) {
	event := Event{
		Type:    eventType,
		Payload: payload,
	}
	data, err := json.Marshal(event)
	if err != nil {
		logger.Errorf("Failed to marshal event: %v", err)
		return
	}
	h.broadcast <- broadcastMessage{data: data}
}

// HandleWebSocket handles WebSocket upgrade requests
func (h *Hub) HandleWebSocket(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Errorf("WebSocket upgrade failed: %v", err)
		return
	}

	client := &Client{hub: h, conn: conn, send: make(chan []byte, 256)}
	if value, exists := c.Get("subject_id"); exists {
		client.SubjectID, _ = value.(uint)
	}
	if value, exists := c.Get("session_version"); exists {
		client.SessionVersion, _ = value.(int64)
	}
	if value, exists := c.Get("token_expires_at"); exists {
		client.ExpiresAt, _ = value.(time.Time)
	}
	if value, exists := c.Get("token_jti"); exists {
		client.JTI, _ = value.(string)
	}
	if client.SubjectID == 0 {
		logger.Warnf("WebSocket missing authenticated subject")
		conn.Close()
		return
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
