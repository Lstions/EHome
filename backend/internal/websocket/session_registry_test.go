package websocket

import (
	"testing"
	"time"
)

func TestHubDisconnectSubjectClosesOnlyMatchingSessions(t *testing.T) {
	hub := NewHub()
	first := &Client{SubjectID: 1, SessionVersion: 1, send: make(chan []byte, 1)}
	second := &Client{SubjectID: 2, SessionVersion: 1, send: make(chan []byte, 1)}
	hub.clients[first] = true
	hub.clients[second] = true

	if count := hub.DisconnectSubject(1); count != 1 {
		t.Fatalf("disconnected=%d", count)
	}
	if _, ok := hub.clients[first]; ok {
		t.Fatal("matching session remains registered")
	}
	if _, ok := hub.clients[second]; !ok {
		t.Fatal("unrelated session was disconnected")
	}
	select {
	case _, ok := <-first.send:
		if ok {
			t.Fatal("matching send channel is still open")
		}
	case <-time.After(time.Second):
		t.Fatal("matching session channel was not closed")
	}
}

func TestClientSessionValidRequiresSubjectVersionAndExpiry(t *testing.T) {
	hub := NewHub()
	hub.SetSessionValidator(func(subjectID uint, version int64) bool {
		return subjectID == 7 && version == 3
	})
	valid := &Client{hub: hub, SubjectID: 7, SessionVersion: 3, ExpiresAt: time.Now().Add(time.Minute)}
	if !valid.SessionValid() {
		t.Fatal("valid session rejected")
	}
	valid.ExpiresAt = time.Now().Add(-time.Second)
	if valid.SessionValid() {
		t.Fatal("expired session accepted")
	}
}
