package database

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	// Initialize logger to avoid nil pointer panics
	_ = logger.Init("warn")
}

func TestConnect_SQLite(t *testing.T) {
	// We can't test Connect() directly because it hardcodes postgres.Open,
	// but we can test the equivalent flow with sqlite.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	// Verify DB is usable
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get underlying DB: %v", err)
	}
	if sqlDB == nil {
		t.Fatal("sqlDB should not be nil")
	}

	// Set connection pool (mirrors Connect logic)
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)

	// Verify ping works
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestAutoMigrate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	// Set the package-level DB so AutoMigrate can use it
	DB = db

	err = AutoMigrate()
	if err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// Verify tables were created by checking if we can insert/query each model
	t.Run("Node", func(t *testing.T) {
		node := models.Node{NodeID: "TEST001", Name: "test-node"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("create node: %v", err)
		}
		var fetched models.Node
		if err := db.First(&fetched, node.ID).Error; err != nil {
			t.Fatalf("fetch node: %v", err)
		}
		if fetched.NodeID != "TEST001" {
			t.Errorf("expected NodeID=TEST001, got %s", fetched.NodeID)
		}
	})

	t.Run("Channel", func(t *testing.T) {
		ch := models.Channel{NodeID: "TEST001", HardwareType: "I2C", BusType: "I2C"}
		if err := db.Create(&ch).Error; err != nil {
			t.Fatalf("create channel: %v", err)
		}
		var fetched models.Channel
		if err := db.First(&fetched, ch.ID).Error; err != nil {
			t.Fatalf("fetch channel: %v", err)
		}
	})

	t.Run("EdgeDevice", func(t *testing.T) {
		ed := models.EdgeDevice{Name: "test-device", Type: "sensor", NodeID: "TEST001", ChannelID: 1, DeviceConfigID: 1}
		if err := db.Create(&ed).Error; err != nil {
			t.Fatalf("create edge device: %v", err)
		}
	})

	t.Run("User", func(t *testing.T) {
		u := models.User{Username: "testuser", PasswordHash: "hash", Role: "admin"}
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	})

	t.Run("DeviceConfig", func(t *testing.T) {
		dc := models.DeviceConfig{Name: "test-config", DeviceType: "temperature"}
		if err := db.Create(&dc).Error; err != nil {
			t.Fatalf("create device config: %v", err)
		}
	})

	t.Run("DeviceData", func(t *testing.T) {
		dd := models.DeviceData{DeviceID: 1, NodeID: "TEST001", DataJSON: `{"temp": 25.5}`}
		if err := db.Create(&dd).Error; err != nil {
			t.Fatalf("create device data: %v", err)
		}
	})

	t.Run("UnifiedData", func(t *testing.T) {
		ud := models.UnifiedData{DeviceID: 1, SensorName: "temperature", Value: 25.5, Unit: "C"}
		if err := db.Create(&ud).Error; err != nil {
			t.Fatalf("create unified data: %v", err)
		}
	})

	t.Run("Notification", func(t *testing.T) {
		n := models.Notification{Type: "alert", Message: "test alert"}
		if err := db.Create(&n).Error; err != nil {
			t.Fatalf("create notification: %v", err)
		}
	})

	t.Run("OTATask", func(t *testing.T) {
		ota := models.OTATask{OtaID: "ota-001", NodeID: "TEST001", Status: "pending"}
		if err := db.Create(&ota).Error; err != nil {
			t.Fatalf("create OTA task: %v", err)
		}
	})

	t.Run("Firmware", func(t *testing.T) {
		fw := models.Firmware{Version: "1.0.0", Checksum: "abc123", SizeBytes: 1024, URL: "http://example.com/fw.bin"}
		if err := db.Create(&fw).Error; err != nil {
			t.Fatalf("create firmware: %v", err)
		}
	})

	t.Run("Vendor", func(t *testing.T) {
		v := models.Vendor{Name: "TestVendor"}
		if err := db.Create(&v).Error; err != nil {
			t.Fatalf("create vendor: %v", err)
		}
	})

	t.Run("DeviceModel", func(t *testing.T) {
		dm := models.DeviceModel{VendorID: 1, Name: "TestModel", Type: "sensor"}
		if err := db.Create(&dm).Error; err != nil {
			t.Fatalf("create device model: %v", err)
		}
	})

	t.Run("NodeEvent", func(t *testing.T) {
		ne := models.NodeEvent{NodeID: "TEST001", EventType: "status_change", NewStatus: "online"}
		if err := db.Create(&ne).Error; err != nil {
			t.Fatalf("create node event: %v", err)
		}
	})

	t.Run("OperationLog", func(t *testing.T) {
		ol := models.OperationLog{UserID: 1, Action: "login", Target: "system"}
		if err := db.Create(&ol).Error; err != nil {
			t.Fatalf("create operation log: %v", err)
		}
	})

	t.Run("DataSource", func(t *testing.T) {
		ds := models.DataSource{Name: "test-source", Type: "mqtt", Config: "{}"}
		if err := db.Create(&ds).Error; err != nil {
			t.Fatalf("create data source: %v", err)
		}
	})

	t.Run("CalibrationCache", func(t *testing.T) {
		cc := models.CalibrationCache{NodeID: "TEST001", DeviceType: "sensor", Data: "{}"}
		if err := db.Create(&cc).Error; err != nil {
			t.Fatalf("create calibration cache: %v", err)
		}
	})

	t.Run("ConfigTemplate", func(t *testing.T) {
		ct := models.ConfigTemplate{NodeID: "TEST001", WriteData: "010300000002", ReadLength: 9}
		if err := db.Create(&ct).Error; err != nil {
			t.Fatalf("create config template: %v", err)
		}
	})
}

func TestGetDB(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db

	got := GetDB()
	if got == nil {
		t.Fatal("GetDB should not return nil")
	}
	if got != db {
		t.Error("GetDB should return the same DB instance")
	}
}

func TestConnect_Failure(t *testing.T) {
	// Connect with invalid postgres config should fail
	err := Connect(Config{
		Host:     "nonexistent-host",
		Port:     5432,
		User:     "test",
		Password: "test",
		DBName:   "test",
		SSLMode:  "disable",
	})
	if err == nil {
		t.Fatal("Connect should fail with invalid postgres config")
	}
}
