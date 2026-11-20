package nodemgr

import (
	"context"
	"testing"
	"time"

	"ehome/backend/pkg/frame"
)

type scanTestPublisher struct {
	payload []byte
	started chan struct{}
}

func (p *scanTestPublisher) Publish(nodeID string, payload []byte) error {
	p.payload = append([]byte(nil), payload...)
	close(p.started)
	return nil
}

// TestSendI2CScanRoundTrip locks the correlation contract: a ScanReq carries a
// generated request_id, and a matching ScanRpt must wake the waiting HTTP
// request with the discovered addresses. The old stub never sent a frame, so
// this test would block until the context deadline.
func TestSendI2CScanRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	pub := &scanTestPublisher{started: make(chan struct{})}
	mgr := NewManager(db, pub, nil, nil, nil)
	defer mgr.Stop()

	go func() {
		<-pub.started
		dec, err := frame.NewDecoder(pub.payload)
		if err != nil {
			t.Errorf("decode ScanReq: %v", err)
			return
		}
		var requestID string
		for {
			field, err := dec.NextField()
			if err != nil {
				break
			}
			if field.FieldNum == 1 {
				requestID = frame.GetString(field)
			}
		}
		if requestID == "" {
			t.Errorf("ScanReq without request_id")
			return
		}
		mgr.deliverScanResult(requestID, true, []uint64{0x50, 0x68})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	requestID, addresses, err := mgr.SendI2CScan(ctx, "NODE001", 9)
	if err != nil {
		t.Fatalf("SendI2CScan: %v", err)
	}
	if requestID == "" {
		t.Fatal("empty request_id")
	}
	if len(addresses) != 2 || addresses[0] != 0x50 || addresses[1] != 0x68 {
		t.Fatalf("addresses = %v, want [0x50 0x68]", addresses)
	}
}
