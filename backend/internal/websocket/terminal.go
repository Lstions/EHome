package websocket

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"time"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/terminal"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// TerminalMessage represents a WebSocket message for the terminal protocol
type TerminalMessage struct {
	Type    string          `json:"type"`              // Message type
	Payload json.RawMessage `json:"payload,omitempty"` // Message payload
}

// TerminalSubscribePayload is the payload for terminal_subscribe
type TerminalSubscribePayload struct {
	ChannelID uint   `json:"channel_id" binding:"required"`
	DeviceID  string `json:"device_id,omitempty"`
}

// TerminalUnsubscribePayload is the payload for terminal_unsubscribe
type TerminalUnsubscribePayload struct {
	ChannelID uint `json:"channel_id" binding:"required"`
}

// TerminalSendPayload is the payload for terminal_send
type TerminalSendPayload struct {
	ChannelID uint   `json:"channel_id" binding:"required"`
	DeviceID  string `json:"device_id" binding:"required"`
	DataHex   string `json:"data_hex" binding:"required"`
	ReadSize  int    `json:"read_size,omitempty"`
}

// TerminalDataPayload is the payload for terminal_data (server→client)
type TerminalDataPayload struct {
	ChannelID uint   `json:"channel_id"`
	DeviceID  string `json:"device_id"`
	Direction string `json:"direction"` // "tx" or "rx"
	DataHex   string `json:"data_hex"`
	DataASCII string `json:"data_ascii"`
	Timestamp int64  `json:"timestamp"`
}

// TerminalHistoryPayload is the payload for terminal_history (server→client)
type TerminalHistoryPayload struct {
	ChannelID uint              `json:"channel_id"`
	Entries   []terminal.Entry  `json:"entries"`
}

// TerminalAckPayload is the payload for terminal_ack (server→client)
type TerminalAckPayload struct {
	ChannelID uint   `json:"channel_id"`
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
	DataHex   string `json:"data_hex,omitempty"`
}

// TerminalClient represents a connected terminal WebSocket client
type TerminalClient struct {
	hub          *TerminalHub
	conn         *websocket.Conn
	send         chan []byte
	userID       uint
	username     string
	subscriptions map[uint]bool // channel IDs this client is subscribed to
	mu           sync.RWMutex
}

// TerminalHub manages terminal WebSocket connections and subscriptions
type TerminalHub struct {
	clients      map[*TerminalClient]bool
	register     chan *TerminalClient
	unregister   chan *TerminalClient
	collectorMgr *collector.Manager
	mu           sync.RWMutex
}

// NewTerminalHub creates a new terminal hub
func NewTerminalHub(collectorMgr *collector.Manager) *TerminalHub {
	return &TerminalHub{
		clients:      make(map[*TerminalClient]bool),
		register:     make(chan *TerminalClient),
		unregister:   make(chan *TerminalClient),
		collectorMgr: collectorMgr,
	}
}

// Run starts the terminal hub event loop
func (h *TerminalHub) Run() {
	// Start listening for terminal events from the manager
	go h.listenTerminalEvents()

	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Printf("[TerminalWS] Client registered (user=%s)", client.username)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			log.Printf("[TerminalWS] Client unregistered (user=%s)", client.username)
		}
	}
}

// listenTerminalEvents forwards terminal events from the manager to subscribed WS clients
func (h *TerminalHub) listenTerminalEvents() {
	events := h.collectorMgr.TerminalMgr().Events()
	for evt := range events {
		payload := TerminalDataPayload{
			ChannelID: evt.ChannelID,
			DeviceID:  evt.DeviceID,
			Direction: evt.Direction,
			DataHex:   evt.DataHex,
			DataASCII: evt.DataASCII,
			Timestamp: evt.Timestamp,
		}

		msg := TerminalMessage{
			Type:    "terminal_data",
			Payload: mustMarshal(payload),
		}
		data := mustMarshal(msg)

		h.mu.RLock()
		for client := range h.clients {
			client.mu.RLock()
			subscribed := client.subscriptions[evt.ChannelID]
			client.mu.RUnlock()

			if subscribed {
				select {
				case client.send <- data:
				default:
					// Client buffer full, skip
				}
			}
		}
		h.mu.RUnlock()
	}
}

// HandleTerminalWebSocket handles WebSocket upgrade for terminal connections
func (h *TerminalHub) HandleTerminalWebSocket(c *gin.Context) {
	// Get JWT-authenticated user info from context (set by AuthMiddleware)
	userID, _ := c.Get("user_id")
	username, _ := c.Get("username")

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[TerminalWS] Upgrade failed: %v", err)
		return
	}

	client := &TerminalClient{
		hub:           h,
		conn:          conn,
		send:          make(chan []byte, 256),
		userID:        toUint(userID),
		username:      toString(username),
		subscriptions: make(map[uint]bool),
	}

	h.register <- client

	// If channel_id was specified in query, auto-subscribe
	if chIDStr := c.Query("channel_id"); chIDStr != "" {
		if chID, err := strconv.Atoi(chIDStr); err == nil && chID > 0 {
			client.subscriptions[uint(chID)] = true
			log.Printf("[TerminalWS] Auto-subscribed user=%s to channel=%d", client.username, chID)

			// Send history for this channel
			entries := h.collectorMgr.TerminalMgr().GetHistory(uint(chID), 50)
			historyMsg := TerminalMessage{
				Type:    "terminal_history",
				Payload: mustMarshal(TerminalHistoryPayload{
					ChannelID: uint(chID),
					Entries:   entries,
				}),
			}
			data := mustMarshal(historyMsg)
			select {
			case client.send <- data:
			default:
			}
		}
	}

	go client.terminalWritePump()
	go client.terminalReadPump()
}

// terminalReadPump reads messages from the WebSocket connection
func (c *TerminalClient) terminalReadPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(65536) // 64KB max message size
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[TerminalWS] Read error: %v", err)
			}
			break
		}

		c.handleTerminalMessage(message)
	}
}

// handleTerminalMessage processes incoming terminal messages
func (c *TerminalClient) handleTerminalMessage(data []byte) {
	var msg TerminalMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[TerminalWS] Invalid message format: %v", err)
		c.sendError("invalid message format")
		return
	}

	switch msg.Type {
	case "terminal_subscribe":
		c.handleSubscribe(msg.Payload)
	case "terminal_unsubscribe":
		c.handleUnsubscribe(msg.Payload)
	case "terminal_send":
		c.handleSend(msg.Payload)
	default:
		log.Printf("[TerminalWS] Unknown message type: %s", msg.Type)
		c.sendError("unknown message type: " + msg.Type)
	}
}

// handleSubscribe processes terminal_subscribe
func (c *TerminalClient) handleSubscribe(payload json.RawMessage) {
	var p TerminalSubscribePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.sendError("invalid subscribe payload: " + err.Error())
		return
	}

	c.mu.Lock()
	c.subscriptions[p.ChannelID] = true
	c.mu.Unlock()

	log.Printf("[TerminalWS] User=%s subscribed to channel=%d", c.username, p.ChannelID)

	// Send history for the newly subscribed channel
	entries := c.hub.collectorMgr.TerminalMgr().GetHistory(p.ChannelID, 50)
	historyMsg := TerminalMessage{
		Type:    "terminal_history",
		Payload: mustMarshal(TerminalHistoryPayload{
			ChannelID: p.ChannelID,
			Entries:   entries,
		}),
	}
	c.send <- mustMarshal(historyMsg)
}

// handleUnsubscribe processes terminal_unsubscribe
func (c *TerminalClient) handleUnsubscribe(payload json.RawMessage) {
	var p TerminalUnsubscribePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.sendError("invalid unsubscribe payload: " + err.Error())
		return
	}

	c.mu.Lock()
	delete(c.subscriptions, p.ChannelID)
	c.mu.Unlock()

	log.Printf("[TerminalWS] User=%s unsubscribed from channel=%d", c.username, p.ChannelID)
}

// handleSend processes terminal_send
func (c *TerminalClient) handleSend(payload json.RawMessage) {
	var p TerminalSendPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.sendAck(0, false, "invalid send payload: "+err.Error(), "")
		return
	}

	// Decode hex data
	data, err := hex.DecodeString(p.DataHex)
	if err != nil {
		c.sendAck(p.ChannelID, false, "invalid hex data", p.DataHex)
		return
	}

	// Send write command via collector manager
	var readSize uint32
	if p.ReadSize > 0 {
		readSize = uint32(p.ReadSize)
	}

	if err := c.hub.collectorMgr.SendWriteCommand(p.DeviceID, uint32(p.ChannelID), data, readSize); err != nil {
		c.sendAck(p.ChannelID, false, "send failed: "+err.Error(), p.DataHex)
		return
	}

	log.Printf("[TerminalWS] User=%s sent command to device=%s channel=%d data=%s",
		c.username, p.DeviceID, p.ChannelID, p.DataHex)

	c.sendAck(p.ChannelID, true, "command sent", p.DataHex)
}

// sendAck sends a terminal_ack message
func (c *TerminalClient) sendAck(channelID uint, success bool, message, dataHex string) {
	ackMsg := TerminalMessage{
		Type: "terminal_ack",
		Payload: mustMarshal(TerminalAckPayload{
			ChannelID: channelID,
			Success:   success,
			Message:   message,
			DataHex:   dataHex,
		}),
	}
	select {
	case c.send <- mustMarshal(ackMsg):
	default:
	}
}

// sendError sends an error ack
func (c *TerminalClient) sendError(message string) {
	c.sendAck(0, false, message, "")
}

// terminalWritePump writes messages to the WebSocket connection
func (c *TerminalClient) terminalWritePump() {
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

// Helper functions

func mustMarshal(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("[TerminalWS] Marshal error: %v", err)
		return json.RawMessage(`{}`)
	}
	return data
}

func toUint(v interface{}) uint {
	switch val := v.(type) {
	case uint:
		return val
	case int:
		return uint(val)
	case float64:
		return uint(val)
	default:
		return 0
	}
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return "unknown"
}
