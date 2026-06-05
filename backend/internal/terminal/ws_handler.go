package terminal

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/websocket"

	wslib "github.com/gorilla/websocket"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// v2.2: jwtSecret reads from EHOME_JWT_SECRET env var, matching api/middleware.go
var jwtSecret = func() string {
	if s := os.Getenv("EHOME_JWT_SECRET"); s != "" {
		return s
	}
	return "ehome-dev-secret-change-me"
}()

// HistoryFetcher retrieves terminal history for a channel (callback to avoid import cycle)
type HistoryFetcher func(channelID uint) ([]Entry, error)

// WriteSender sends a write command to a device (callback to avoid import cycle)
type WriteSender func(deviceID string, channelID uint32, data []byte, readSize uint32) error

// WSHandler handles terminal-specific WebSocket connections
type WSHandler struct {
	hub           *websocket.Hub
	historyFetch  HistoryFetcher
	writeSend     WriteSender
	upgrader      wslib.Upgrader
	mu            sync.Mutex
	subscriptions map[*termClient]map[uint]bool // client -> set of subscribed channelIDs
	eventCancel   func()                        // cancel the event listener
}

// termClient represents a terminal WebSocket client
type termClient struct {
	conn *wslib.Conn
	send chan []byte
	h    *WSHandler
	role string // user role from JWT
}

// Incoming message types
type wsMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type subscribePayload struct {
	ChannelID uint `json:"channel_id"`
}

type sendPayload struct {
	DeviceID  string `json:"device_id"`
	ChannelID uint   `json:"channel_id"`
	DataHex   string `json:"data_hex"`
	ReadSize  int    `json:"read_size"`
}

// Outgoing message types
type wsResponse struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// NewWSHandler creates a new terminal WebSocket handler
func NewWSHandler(hub *websocket.Hub, historyFetch HistoryFetcher, writeSend WriteSender) *WSHandler {
	h := &WSHandler{
		hub:           hub,
		historyFetch:  historyFetch,
		writeSend:     writeSend,
		subscriptions: make(map[*termClient]map[uint]bool),
		upgrader: wslib.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins in development
			},
		},
	}

	// Start listening to hub broadcast events
	h.startEventListener()

	return h
}

// startEventListener subscribes to hub broadcasts and forwards relevant events to terminal clients
func (h *WSHandler) startEventListener() {
	eventsCh := h.hub.Subscribe()
	stopCh := make(chan struct{})
	h.eventCancel = func() { close(stopCh) }

	go func() {
		for {
			select {
			case <-stopCh:
				return
			case event, ok := <-eventsCh:
				if !ok {
					return
				}
				h.dispatchEvent(event)
			}
		}
	}()
}

// dispatchEvent forwards a broadcast event to subscribed terminal clients
func (h *WSHandler) dispatchEvent(event websocket.Event) {
	// We only care about "channel_data" events for terminal
	if event.Type != "channel_data" {
		return
	}

	// Extract channel_id from payload
	payloadBytes, err := json.Marshal(event.Payload)
	if err != nil {
		return
	}
	var payload struct {
		DeviceID  string `json:"device_id"`
		ChannelID uint64 `json:"channel_id"`
		RawHex    string `json:"raw_hex"`
		Timestamp int64  `json:"timestamp"`
		ErrorCode uint64 `json:"error_code"`
		RequestID uint64 `json:"request_id"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return
	}

	// Build terminal data response
	resp := wsResponse{
		Type: "data",
		Payload: map[string]interface{}{
			"device_id":  payload.DeviceID,
			"channel_id": payload.ChannelID,
			"raw_hex":    payload.RawHex,
			"timestamp":  payload.Timestamp,
		},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}

	// Send to clients subscribed to this channel
	h.mu.Lock()
	defer h.mu.Unlock()

	for client, channels := range h.subscriptions {
		if channels[uint(payload.ChannelID)] {
			select {
			case client.send <- data:
			default:
				// Client send buffer full, drop
			}
		}
	}
}

// HandleTerminalWS handles the /ws/terminal WebSocket endpoint
// JWT auth: token from query param or Authorization header
func (h *WSHandler) HandleTerminalWS(c *gin.Context) {
	// 1. Extract JWT token
	tokenStr := c.Query("token")
	if tokenStr == "" {
		authHeader := c.GetHeader("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenStr = strings.TrimSpace(authHeader[7:])
		}
	}
	if tokenStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
		return
	}

	// 2. Parse JWT and extract role
	role := "viewer" // default
	token, err := jwt.ParseWithClaims(tokenStr, &jwt.MapClaims{}, func(t *jwt.Token) (interface{}, error) {
		return []byte(jwtSecret), nil
	})
	if err == nil && token.Valid {
		if claims, ok := token.Claims.(*jwt.MapClaims); ok {
			if r, ok := (*claims)["role"].(string); ok {
				role = r
			}
		}
	}

	// 3. Upgrade to WebSocket
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Terminal WS upgrade failed: %v", err)
		return
	}

	client := &termClient{
		conn: conn,
		send: make(chan []byte, 256),
		h:    h,
		role: role,
	}

	// Register client
	h.mu.Lock()
	h.subscriptions[client] = make(map[uint]bool)
	h.mu.Unlock()

	// Start pumps
	go client.writePump()
	go client.readPump()
}

// subscribe adds a channel subscription for a client and sends history
func (h *WSHandler) subscribe(client *termClient, channelID uint) {
	h.mu.Lock()
	h.subscriptions[client][channelID] = true
	h.mu.Unlock()

	// Send history for this channel
	if h.historyFetch != nil {
		entries, err := h.historyFetch(channelID)
		if err != nil {
			client.sendJSON(wsResponse{
				Type:    "error",
				Payload: map[string]string{"message": "failed to fetch history"},
			})
			return
		}
		client.sendJSON(wsResponse{
			Type: "data",
			Payload: map[string]interface{}{
				"channel_id": channelID,
				"history":    entries,
				"count":      len(entries),
			},
		})
	}

	client.sendJSON(wsResponse{
		Type: "ack",
		Payload: map[string]interface{}{
			"action":     "subscribe",
			"channel_id": channelID,
		},
	})
}

// unsubscribe removes a channel subscription for a client
func (h *WSHandler) unsubscribe(client *termClient, channelID uint) {
	h.mu.Lock()
	delete(h.subscriptions[client], channelID)
	h.mu.Unlock()

	client.sendJSON(wsResponse{
		Type: "ack",
		Payload: map[string]interface{}{
			"action":     "unsubscribe",
			"channel_id": channelID,
		},
	})
}

// unregisterClient removes a client completely
func (h *WSHandler) unregisterClient(client *termClient) {
	h.mu.Lock()
	delete(h.subscriptions, client)
	h.mu.Unlock()
}

func (c *termClient) sendJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
		// buffer full, drop
	}
}

func (c *termClient) readPump() {
	defer func() {
		c.h.unregisterClient(c)
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if wslib.IsUnexpectedCloseError(err, wslib.CloseGoingAway, wslib.CloseAbnormalClosure) {
				log.Printf("Terminal WS read error: %v", err)
			}
			break
		}

		var msg wsMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			c.sendJSON(wsResponse{
				Type:    "error",
				Payload: map[string]string{"message": "invalid json"},
			})
			continue
		}

		switch msg.Type {
		case "subscribe":
			var p subscribePayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil || p.ChannelID == 0 {
				c.sendJSON(wsResponse{
					Type:    "error",
					Payload: map[string]string{"message": "invalid subscribe payload, channel_id required"},
				})
				continue
			}
			c.h.subscribe(c, p.ChannelID)

		case "unsubscribe":
			var p subscribePayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil || p.ChannelID == 0 {
				// If no channel_id specified, unsubscribe from all
				c.h.mu.Lock()
				c.h.subscriptions[c] = make(map[uint]bool)
				c.h.mu.Unlock()
				c.sendJSON(wsResponse{
					Type: "ack",
					Payload: map[string]interface{}{
						"action": "unsubscribe_all",
					},
				})
				continue
			}
			c.h.unsubscribe(c, p.ChannelID)

		case "send":
			// Admin-only: only admin role can send commands to devices
			if c.role != "admin" {
				c.sendJSON(wsResponse{
					Type:    "error",
					Payload: map[string]string{"message": "forbidden: admin role required to send commands"},
				})
				continue
			}
			var p sendPayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil || p.DeviceID == "" || p.DataHex == "" {
				c.sendJSON(wsResponse{
					Type:    "error",
					Payload: map[string]string{"message": "invalid send payload, device_id and data_hex required"},
				})
				continue
			}
			if c.h.writeSend != nil {
				// Parse hex data
				data, err := parseHex(p.DataHex)
				if err != nil {
					c.sendJSON(wsResponse{
						Type:    "error",
						Payload: map[string]string{"message": "invalid hex data"},
					})
					continue
				}
				// Use channel_id from payload, fallback to first subscribed
				channelID := p.ChannelID
				if channelID == 0 {
					channelID = findFirstSubscribed(c)
				}
				if err := c.h.writeSend(p.DeviceID, uint32(channelID), data, uint32(p.ReadSize)); err != nil {
					c.sendJSON(wsResponse{
						Type:    "error",
						Payload: map[string]string{"message": err.Error()},
					})
					continue
				}
				c.sendJSON(wsResponse{
					Type: "ack",
					Payload: map[string]interface{}{
						"action":     "send",
						"channel_id": channelID,
						"data_hex":   p.DataHex,
					},
				})
			}

		case "ping":
			c.sendJSON(wsResponse{
				Type:    "pong",
				Payload: map[string]int64{"timestamp": time.Now().UnixMilli()},
			})

		default:
			c.sendJSON(wsResponse{
				Type:    "error",
				Payload: map[string]string{"message": "unknown message type: " + msg.Type},
			})
		}
	}
}

func (c *termClient) writePump() {
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
				c.conn.WriteMessage(wslib.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(wslib.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(wslib.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// findFirstSubscribed returns the first subscribed channel ID for a client
func findFirstSubscribed(c *termClient) uint {
	c.h.mu.Lock()
	defer c.h.mu.Unlock()
	for ch := range c.h.subscriptions[c] {
		return ch
	}
	return 0
}

// parseHex decodes a hex string to bytes
func parseHex(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, strconv.ErrRange
	}
	b := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		hi, err := strconv.ParseUint(s[i:i+1], 16, 8)
		if err != nil {
			return nil, err
		}
		lo, err := strconv.ParseUint(s[i+1:i+2], 16, 8)
		if err != nil {
			return nil, err
		}
		b[i/2] = byte(hi<<4 | lo)
	}
	return b, nil
}
