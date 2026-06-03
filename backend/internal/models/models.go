package models

import (
	"time"

	"gorm.io/gorm"
)

// Collector 采集器节点
type Collector struct {
	ID              uint           `gorm:"primaryKey" json:"id"`
	DeviceID        string         `gorm:"uniqueIndex;size:32;not null" json:"device_id"`
	Model           string         `gorm:"size:20" json:"model"`
	FirmwareVersion string         `gorm:"size:20" json:"firmware_version"`
	Status          string         `gorm:"size:20;default:offline" json:"status"`
	ConfigVersion   string         `gorm:"size:64" json:"config_version"`
	ConfigStatus    string         `gorm:"size:20;default:pending" json:"config_status"` // pending/applied/failed
	LastSeen        *time.Time     `json:"last_seen"`
	UptimeSeconds   uint32         `json:"uptime_seconds"`
	PingLatencyMs   int32          `json:"ping_latency_ms"`    // latest RTT in ms (F7.5)
	LastPingAt      *time.Time     `json:"last_ping_at"`       // last successful ping time
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
	Channels        []Channel      `gorm:"foreignKey:CollectorID" json:"channels,omitempty"`
}

// Channel 通道配置
type Channel struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	CollectorID  uint      `gorm:"index;not null" json:"collector_id"`
	HardwareType string    `gorm:"size:20" json:"hardware_type"`  // SPI/I2C/UART/GPIO/ADC
	HardwareID   uint      `gorm:"default:0" json:"hardware_id"`  // 总线上的硬件地址
	IntervalMs   int       `gorm:"default:5000" json:"interval_ms"`
	BusType      string    `gorm:"size:20;default:I2C" json:"bus_type"`    // I2C/SPI/UART/GPIO/ADC
	BusConfig    string    `gorm:"type:text" json:"bus_config"`            // JSON bus配置 (引脚/速率等)
	TemplateIDs  string    `gorm:"type:text" json:"template_ids"`          // 逗号分隔的template ID列表
	Config       string    `gorm:"type:text" json:"config"`                // JSON bus_config (兼容旧字段)
	Enabled      bool      `gorm:"default:true" json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Devices      []Device  `gorm:"foreignKey:ChannelID" json:"devices,omitempty"`
}

// ConfigTemplate 配置模板（定义读取设备的寄存器序列）
type ConfigTemplate struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	CollectorID uint  `gorm:"index;not null" json:"collector_id"`
	WriteData  string `gorm:"type:text;not null" json:"write_data"` // hex格式写入数据
	ReadLength uint32 `gorm:"default:0" json:"read_length"`        // 期望读取长度
	DelayMs    uint32 `gorm:"default:0" json:"delay_ms"`           // 写后延迟
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Device 设备定义
type Device struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	Name      string     `gorm:"size:64;not null" json:"name"`
	Type      string     `gorm:"size:32;not null;index" json:"type"` // bmp280, lk_th01, sn3000
	ParserID  string     `gorm:"size:32" json:"parser_id"`
	ChannelID uint       `gorm:"index" json:"channel_id"`
	Status    string     `gorm:"size:20;default:active" json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// DeviceConfig 设备配置模板
//
// 与采集器级 ConfigTemplate (hex 读寄存器) 不同, DeviceConfig 是
// 设备级元数据模板, 用于:
//   - 创建设备时一键套用 (名称/描述/协议/硬件类型/参数/默认标志)
//   - 团队共享"标准传感器配方" (如 10 个 BMP280 全部套同一模板)
//
// 前端字段对应: src/api/deviceConfig.ts
type DeviceConfig struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Name         string    `gorm:"size:128;not null;index" json:"name"`
	Description  string    `gorm:"type:text" json:"description"`
	DeviceType   string    `gorm:"size:64;not null;index" json:"device_type"`
	Protocol     string    `gorm:"size:32" json:"protocol"`      // modbus / stream / custom
	HardwareType string    `gorm:"size:32" json:"hardware_type"` // uart / i2c / spi / gpio / adc
	ParserID     string    `gorm:"size:64" json:"parser_id"`
	Config       string    `gorm:"type:text" json:"config"`     // JSON: 硬件参数 (baudrate/data_bits/...)
	IsDefault    bool      `gorm:"default:false;index" json:"is_default"`
	Status       string    `gorm:"size:20;default:active" json:"status"` // active / inactive
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DeviceData 原始数据
type DeviceData struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	DeviceID    uint      `gorm:"index;not null" json:"device_id"`
	CollectorID uint      `gorm:"index;not null" json:"collector_id"`
	DataJSON    string    `gorm:"type:text" json:"data_json"`
	Timestamp   time.Time `gorm:"index" json:"timestamp"`
	CreatedAt   time.Time `json:"created_at"`
}

// UnifiedData 统一数据
type UnifiedData struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	DeviceID    uint      `gorm:"index;not null" json:"device_id"`
	SensorName  string    `gorm:"size:32;not null;index" json:"sensor_name"`
	Value       float64   `gorm:"not null" json:"value"`
	Unit        string    `gorm:"size:16" json:"unit"`
	Timestamp   time.Time `gorm:"index" json:"timestamp"`
	CreatedAt   time.Time `json:"created_at"`
}

// DataSource 数据源主备管理
type DataSource struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"size:64;not null" json:"name"`
	Type     string `gorm:"size:20;not null" json:"type"`
	Config   string `gorm:"type:text" json:"config"` // JSON
	Priority int    `gorm:"default:0" json:"priority"`
	Status   string `gorm:"size:20;default:active" json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OTATask OTA升级任务
// 状态机: pending → downloading → verifying → installing → success | failed
// 状态字面量与 docs/v2.0/requirements.md §6.1 一致
type OTATask struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	OtaID       string     `gorm:"column:ota_id;size:64;uniqueIndex;not null" json:"ota_id"`
	CollectorID uint       `gorm:"index;not null" json:"collector_id"`
	FirmwareID  uint       `gorm:"index" json:"firmware_id"`
	Status      string     `gorm:"size:20;default:pending" json:"status"` // pending/downloading/verifying/installing/success/failed
	Progress    uint8      `gorm:"default:0" json:"progress"`
	ErrorMsg    string     `gorm:"size:256" json:"error_msg"`
	ToVersion   string     `gorm:"size:32" json:"to_version"`     // Target firmware version (used for Hello-based completion reconciliation, §6.4.3)
	StartedAt   *time.Time `json:"started_at"`                    // When OTA first transitioned out of pending
	CompletedAt *time.Time `json:"completed_at"`                  // When status reached success or failed (§6.4.2)
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Firmware 固件版本
type Firmware struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Version    string    `gorm:"size:32;not null" json:"version"`
	Checksum   string    `gorm:"size:64;not null" json:"checksum"` // SHA256 hex
	SizeBytes  uint64    `json:"size_bytes"`
	URL        string    `gorm:"size:256;not null" json:"url"`
	CreatedAt  time.Time `json:"created_at"`
}

// Notification 通知
type Notification struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Type      string    `gorm:"size:20;not null" json:"type"`
	Message   string    `gorm:"type:text;not null" json:"message"`
	Read      bool      `gorm:"default:false" json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// User 用户
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"size:32;uniqueIndex;not null" json:"username"`
	PasswordHash string    `gorm:"size:128;not null" json:"-"`
	Role         string    `gorm:"size:20;default:user" json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// OperationLog 审计日志
type OperationLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Action    string    `gorm:"size:32;not null" json:"action"`
	Target    string    `gorm:"size:64" json:"target"`
	CreatedAt time.Time `json:"created_at"`
}

// Vendor 厂商
type Vendor struct {
	ID        uint          `gorm:"primaryKey" json:"id"`
	Name      string        `gorm:"size:64;not null" json:"name"`
	CreatedAt time.Time     `json:"created_at"`
	Models    []DeviceModel `gorm:"foreignKey:VendorID" json:"models,omitempty"`
}

// DeviceModel 设备型号
type DeviceModel struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	VendorID  uint      `gorm:"index;not null" json:"vendor_id"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	Type      string    `gorm:"size:32;not null" json:"type"`
	Fields    string    `gorm:"type:text" json:"fields"` // JSON data field definitions
	CreatedAt time.Time `json:"created_at"`
}

// CollectorEvent 节点状态变更事件
type CollectorEvent struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CollectorID uint      `gorm:"index;not null" json:"collector_id"`
	EventType   string    `gorm:"size:20;not null" json:"event_type"` // online, offline, config_update
	OldStatus   string    `gorm:"size:20" json:"old_status"`
	NewStatus   string    `gorm:"size:20" json:"new_status"`
	CreatedAt   time.Time `json:"created_at"`
}

// CalibrationCache 校准数据缓存
type CalibrationCache struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CollectorID uint      `gorm:"index;not null" json:"collector_id"`
	DeviceType  string    `gorm:"size:32;not null" json:"device_type"`
	Data        string    `gorm:"type:text;not null" json:"data"` // JSON calibration data
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
