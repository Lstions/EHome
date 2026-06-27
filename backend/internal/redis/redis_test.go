package redis

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	client := redis.NewClient(&redis.Options{
		Addr:     mr.Addr(),
		PoolSize: 10,
	})
	// Override the package-level Client
	Client = client
	return mr, client
}

func TestConnect_Success(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	err = Connect(mr.Addr())
	if err != nil {
		t.Fatalf("Connect should succeed with miniredis, got: %v", err)
	}
	if Client == nil {
		t.Fatal("Client should be set after Connect")
	}
}

func TestConnect_Failure(t *testing.T) {
	err := Connect("localhost:1")
	if err == nil {
		t.Fatal("Connect should fail with unreachable address")
	}
}

func TestSetAndGetHeartbeat(t *testing.T) {
	mr, _ := setupMiniredis(t)
	defer mr.Close()

	deviceID := "device-001"
	ttl := 30 * time.Second

	err := SetHeartbeat(deviceID, ttl)
	if err != nil {
		t.Fatalf("SetHeartbeat: %v", err)
	}

	val, err := GetHeartbeat(deviceID)
	if err != nil {
		t.Fatalf("GetHeartbeat: %v", err)
	}
	if val == 0 {
		t.Error("GetHeartbeat should return non-zero after SetHeartbeat")
	}
}

func TestGetHeartbeat_Missing(t *testing.T) {
	mr, _ := setupMiniredis(t)
	defer mr.Close()

	val, err := GetHeartbeat("nonexistent")
	if err != nil {
		t.Fatalf("GetHeartbeat missing key: %v", err)
	}
	if val != 0 {
		t.Errorf("GetHeartbeat for missing key should return 0, got %d", val)
	}
}

func TestIsOnline(t *testing.T) {
	mr, _ := setupMiniredis(t)
	defer mr.Close()

	deviceID := "device-online"
	ttl := 60 * time.Second

	if IsOnline(deviceID) {
		t.Error("IsOnline should be false before SetHeartbeat")
	}

	err := SetHeartbeat(deviceID, ttl)
	if err != nil {
		t.Fatalf("SetHeartbeat: %v", err)
	}

	if !IsOnline(deviceID) {
		t.Error("IsOnline should be true after SetHeartbeat")
	}

	mr.FastForward(61 * time.Second)
	if IsOnline(deviceID) {
		t.Error("IsOnline should be false after TTL expires")
	}
}

func TestGetAllCollectors(t *testing.T) {
	mr, _ := setupMiniredis(t)
	defer mr.Close()

	ids, err := GetAllCollectors()
	if err != nil {
		t.Fatalf("GetAllCollectors empty: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 collectors, got %d", len(ids))
	}

	for _, id := range []string{"c1", "c2", "c3"} {
		if err := SetHeartbeat(id, 120*time.Second); err != nil {
			t.Fatalf("SetHeartbeat(%s): %v", id, err)
		}
	}

	ids, err = GetAllCollectors()
	if err != nil {
		t.Fatalf("GetAllCollectors: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 collectors, got %d: %v", len(ids), ids)
	}

	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	for _, expected := range []string{"c1", "c2", "c3"} {
		if !found[expected] {
			t.Errorf("missing collector %q in results", expected)
		}
	}
}

func TestDeleteHeartbeat(t *testing.T) {
	mr, _ := setupMiniredis(t)
	defer mr.Close()

	deviceID := "device-del"
	ttl := 60 * time.Second

	if err := SetHeartbeat(deviceID, ttl); err != nil {
		t.Fatalf("SetHeartbeat: %v", err)
	}
	if !IsOnline(deviceID) {
		t.Error("should be online before delete")
	}

	if err := DeleteHeartbeat(deviceID); err != nil {
		t.Fatalf("DeleteHeartbeat: %v", err)
	}

	if IsOnline(deviceID) {
		t.Error("should be offline after DeleteHeartbeat")
	}

	val, err := GetHeartbeat(deviceID)
	if err != nil {
		t.Fatalf("GetHeartbeat after delete: %v", err)
	}
	if val != 0 {
		t.Errorf("GetHeartbeat should return 0 after delete, got %d", val)
	}
}

func TestDeleteHeartbeat_Nonexistent(t *testing.T) {
	mr, _ := setupMiniredis(t)
	defer mr.Close()

	err := DeleteHeartbeat("ghost")
	if err != nil {
		t.Fatalf("DeleteHeartbeat nonexistent: %v", err)
	}
}
