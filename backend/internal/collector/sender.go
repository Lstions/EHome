package collector

import (
	"context"
	"fmt"
	"time"

	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/redis"
	"ehome/backend/pkg/frame"
)

// SendPing sends a Ping message to a device and records timestamp in Redis for verification
func (m *Manager) SendPing(deviceID string) error {
	ts := time.Now().UnixMicro()
	enc := frame.NewEncoder(frame.MsgPing)
	enc.EncodeVarint(1, uint64(ts))

	// Store ping timestamp in Redis for anti-forgery verification (TTL=30s)
	if redis.Client != nil {
		redis.Client.Set(context.Background(), fmt.Sprintf("ping:%s", deviceID), ts, 30*time.Second)
	}

	topic := mqtt.TopicForDevice(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendWriteCommand sends a WriteCommand to a device
func (m *Manager) SendWriteCommand(deviceID string, channelID uint32, data []byte, readSize uint32) error {
	// Record TX in terminal
	m.termMgr.RecordTX(deviceID, uint(channelID), data)

	enc := frame.NewEncoder(frame.MsgWriteCmd)
	enc.EncodeVarint(1, uint64(time.Now().UnixNano())) // request_id
	enc.EncodeVarint(2, uint64(channelID))
	enc.EncodeBytes(3, data)
	if readSize > 0 {
		enc.EncodeVarint(4, uint64(readSize))
	}

	topic := mqtt.TopicForDevice(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendScanRequest sends a ScanRequest to a device
func (m *Manager) SendScanRequest(deviceID string, hardwareID uint32) error {
	enc := frame.NewEncoder(frame.MsgScanReq)
	enc.EncodeString(1, fmt.Sprintf("scan-%d", time.Now().Unix()))
	enc.EncodeVarint(2, uint64(hardwareID))

	topic := mqtt.TopicForDevice(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendQueryRequest sends a QueryReq (type=0x0E) to a device
func (m *Manager) SendQueryRequest(deviceID string, queryType uint32) error {
	enc := frame.NewEncoder(frame.MsgQueryReq)
	enc.EncodeString(1, fmt.Sprintf("query-%d", time.Now().UnixMilli()))
	enc.EncodeVarint(2, uint64(queryType))

	topic := mqtt.TopicForDevice(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendHelloAck sends a HelloAck message to a device (0x12, SVR→ESP)
func (m *Manager) SendHelloAck(deviceID string, serverTime uint64, features uint32) error {
	enc := frame.NewEncoder(frame.MsgHelloAck)
	enc.EncodeVarint(1, serverTime)
	enc.EncodeVarint(2, uint64(features))

	topic := mqtt.TopicForDevice(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendConfigQuery sends a ConfigQuery (type=0x10) to a device
func (m *Manager) SendConfigQuery(deviceID string) error {
	enc := frame.NewEncoder(frame.MsgConfigQuery)
	enc.EncodeString(1, fmt.Sprintf("cfgq-%d", time.Now().UnixMilli()))

	topic := mqtt.TopicForDevice(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}
