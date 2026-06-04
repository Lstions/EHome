package models

import (
	"time"

	"gorm.io/gorm"
)

// =====================================================================
// v2.2 核心概念: 节点 / 通道 / 设备配置 / 边缘设备
// 完整定义见 docs/设计/00-术语表.md
// =====================================================================

// Node 节点 (v2.2 新名, 替代 v2.1 Collector)
//
// 一个 Node = 一个物理边缘设备 (ESP32-C6/S3) 的中心端抽象
// 字段含义见 docs/设计/节点/详细设计.md
type Node struct {
	DeviceID        string     `gorm:"-" json:"device_id,omitempty"` // v2.1 兼容 (alias of NodeID, 不存 DB)
	ID              uint       `gorm:"primaryKey" json:"id"`
	NodeID          string     `gorm:"uniqueIndex;size:32;not null" json:"node_id"` // 物理 ID (e.g. esp32c6_404CCA57B7BC)
	Name            string     `gorm:"size:64;not null" json:"name"`
	Model           string     `gorm:"size:20" json:"model"`
	FirmwareVersion string     `gorm:"size:20" json:"firmware_version"`
	ProtocolVersion string     `gorm:"size:16;default:2.2" json:"protocol_version"` // v2.2
	Platform        string     `gorm:"size:16" json:"platform"`                     // ESP32 / ESP32S3 / ESP32C6
	Status          string     `gorm:"size:20;default:offline" json:"status"`
	ConfigVersion   string     `gorm:"size:64" json:"config_version"`
	ConfigStatus    string     `gorm:"size:20;default:pending" json:"config_status"`
	LastSeen        *time.Time `json:"last_seen"`
	LastPingAt      *time.Time `json:"last_ping_at"`
	UptimeSeconds   uint32     `json:"uptime_seconds"`
	PingLatencyMs   int32      `json:"ping_latency_ms"`
	MQTTTopicUp     string     `gorm:"size:128" json:"mqtt_topic_up"`
	MQTTTopicDown   string     `gorm:"size:128" json:"mqtt_topic_down"`
	WiFiSSID        string     `gorm:"size:64" json:"wifi_ssid"`
	WiFiRSSI        int        `json:"wifi_rssi"`
	FreeHeapBytes   int        `json:"free_heap_bytes"`
	Capabilities    string     `gorm:"type:jsonb" json:"capabilities"`
	HardwareInfo    string     `gorm:"type:jsonb" json:"hardware_info"`
	// v2.1 同步机制字段
	ConfigEpoch     uint64     `gorm:"default:0" json:"config_epoch"`
	LastManifestID  string     `gorm:"size:64" json:"last_manifest_id"`
	ConfigSyncState string     `gorm:"size:20;default:unknown" json:"config_sync_state"`
	LastSyncAt      *time.Time `json:"last_sync_at"`
	LastSyncID      string     `gorm:"size:64" json:"last_sync_id"`
	// v2.2 新增: 标记用 v1 (devices/{id}) 还是 v2 (nodes/{id}) topic
	MQTTTopicFormat string         `gorm:"size:16;default:v2" json:"mqtt_topic_format"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
	// 关联
	Channels []Channel `gorm:"foreignKey:NodeID" json:"channels,omitempty"`
}

// TableName GORM 表名 (Phase 2A-2 DB 迁移后, 用 nodes 表)
func (Node) TableName() string { return "nodes" }

// =====================================================================
// v2.1 兼容别名 (6 个月内保留, 之后 v2.3 删除)
// =====================================================================

// Collector v2.1 别名 → Node (v2.2)
//
// Deprecated: Use Node instead. Will be removed in v2.3.
type Collector = Node

// =====================================================================

// EdgeDevice 边缘设备 (v2.2 新名 + 增强字段, 替代 v2.1 Device)
//
// 一个 EdgeDevice = Node + Channel + DeviceConfig 三元组的实例化
// 字段含义见 docs/设计/边缘设备/详细设计.md
type EdgeDevice struct {
	Type           string         `gorm:"size:32;not null;default:'';index" json:"type"` // v2.1 字段保留 (从 DeviceConfig.DeviceType 同步, 由 v2.2 init 维护)
	ParserID       string         `gorm:"size:32" json:"parser_id"`                      // v2.1 字段保留 (从 DeviceConfig.Parser.ID 同步)
	ID             uint           `gorm:"primaryKey" json:"id"`
	Name           string         `gorm:"size:64;not null" json:"name"`
	NodeID         uint           `gorm:"index;not null" json:"node_id"`          // v2.2 显式 FK (was implicit via Channel)
	ChannelID      uint           `gorm:"index;not null" json:"channel_id"`       // 保留
	DeviceConfigID uint           `gorm:"index;not null" json:"device_config_id"` // v2.2 关键新增 FK
	HardwareID     uint           `gorm:"default:0" json:"hardware_id"`           // v2.2 新增 (从 Channel 移过来)
	IntervalMs     int            `gorm:"default:5000" json:"interval_ms"`
	Enabled        bool           `gorm:"default:true" json:"enabled"`
	Status         string         `gorm:"size:20;default:active" json:"status"`
	ErrorCode      int            `gorm:"default:0" json:"error_code"` // v2.2 新增
	LastDataAt     *time.Time     `json:"last_data_at"`                // v2.2 新增
	LastError      string         `gorm:"size:256" json:"last_error"`  // v2.2 新增
	ConfigVersion  string         `gorm:"size:64" json:"config_version"`
	InitState      string         `gorm:"size:20;default:pending" json:"init_state"` // v2.2 新增 (G6 准备)
	InitLastStep   int            `gorm:"default:0" json:"init_last_step"`           // v2.2 新增
	InitTotalSteps int            `gorm:"default:0" json:"init_total_steps"`         // v2.2 新增
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
	// 关联
	Node         Node         `gorm:"foreignKey:NodeID" json:"node,omitempty"`
	Channel      Channel      `gorm:"foreignKey:ChannelID" json:"channel,omitempty"`
	DeviceConfig DeviceConfig `gorm:"foreignKey:DeviceConfigID" json:"device_config,omitempty"`
}

// TableName GORM 表名
func (EdgeDevice) TableName() string { return "edge_devices" }

// =====================================================================

// Device v2.1 别名 → EdgeDevice (v2.2)
//
// Deprecated: Use EdgeDevice instead. Will be removed in v2.3.
type Device = EdgeDevice

// =====================================================================

// Channel 通道 (v2.2: 物理端点, 不再含 device_type 等业务字段)
//
// 一个 Channel = 一个物理总线实例 (UART/I2C/SPI/GPIO/ADC)
// v2.2 字段含义见 docs/设计/通道/详细设计.md
type Channel struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	NodeID uint `gorm:"index;not null;column:node_id" json:"node_id"` // v2.2 改名 (was CollectorID), column 重命名后保留旧名兼容
	// 兼容 v2.1 字段名 (gorm:"-" 不存 DB, 仅作 JSON 输出/Go 访问 alias)
	CollectorID  uint           `gorm:"-" json:"collector_id,omitempty"` // alias of NodeID (6 个月内保留)
	HardwareType string         `gorm:"size:20" json:"hardware_type"`    // SPI/I2C/UART/GPIO/ADC
	HardwareID   uint           `gorm:"default:0" json:"hardware_id"`    // 总线上的硬件地址 (e.g. I2C 0x76)
	IntervalMs   int            `gorm:"default:5000" json:"interval_ms"`
	BusType      string         `gorm:"size:20;default:I2C" json:"bus_type"` // I2C/SPI/UART/GPIO/ADC
	BusConfig    string         `gorm:"type:text" json:"bus_config"`         // JSON bus配置 (引脚/速率等)
	TemplateIDs  string         `gorm:"type:text" json:"template_ids"`       // 逗号分隔的template ID列表
	Config       string         `gorm:"type:text" json:"config"`             // JSON bus_config (兼容旧字段)
	Enabled      bool           `gorm:"default:true" json:"enabled"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
	// 关联 (v2.2 改用 EdgeDevice)
	EdgeDevices []EdgeDevice `gorm:"foreignKey:ChannelID" json:"edge_devices,omitempty"`
	// 兼容 v2.1 字段名 (gorm:"-" 不存 DB)
	Devices []EdgeDevice `gorm:"-" json:"devices,omitempty"` // alias of EdgeDevices (6 个月内保留)
}

// 兼容: v2.1 老代码访问 channel.CollectorID 仍可用
// (因为 DB 列是 node_id, Go 字段是 NodeID, JSON 字段是 node_id)
// 老代码需改: channel.CollectorID → channel.NodeID
// 6 个月内通过 grep 工具辅助迁移

// =====================================================================

// ConfigTemplate 配置模板 (保留 v2.1, 不变)
//
// 定义读取设备的寄存器序列 (hex write_data + read_length + delay_ms)
type ConfigTemplate struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CollectorID uint      `gorm:"index;not null" json:"collector_id"` // 待 v2.3 改为 NodeID
	WriteData   string    `gorm:"type:text;not null" json:"write_data"`
	ReadLength  uint32    `gorm:"default:0" json:"read_length"`
	DelayMs     uint32    `gorm:"default:0" json:"delay_ms"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// =====================================================================

// DeviceConfig 设备配置 (v2.2 强化字段, 但 struct 名不变)
//
// 一种设备型号的协议无关定义 (connection / parser / init_flow)
// 字段含义见 docs/设计/设备配置/详细设计.md
//
// v2.2 新增三大部分 JSONB 字段: connection / parser / init_flow
// (v2.1 老字段保留作 legacy, 后续 v2.3 软删除)
type DeviceConfig struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	Name         string `gorm:"size:128;not null;index" json:"name"`
	Description  string `gorm:"type:text" json:"description"`
	DeviceType   string `gorm:"size:64;not null;index" json:"device_type"`
	Protocol     string `gorm:"size:32" json:"protocol"`      // modbus / stream / custom (legacy)
	HardwareType string `gorm:"size:32" json:"hardware_type"` // uart / i2c / spi / gpio / adc (legacy)
	ParserID     string `gorm:"size:64" json:"parser_id"`     // legacy, v2.2 用 parser JSONB
	Config       string `gorm:"type:text" json:"config"`      // JSON: 硬件参数 (legacy, v2.2 用 connection JSONB)
	// v2.2 新增三大部分 JSONB 字段 (推荐使用)
	Connection string `gorm:"type:jsonb" json:"connection"`             // 设备连接参数 {protocol, default_params, ...}
	Parser     string `gorm:"type:jsonb;default:'{}'" json:"parser"`    // 数据解析器 {id, options}
	InitFlow   string `gorm:"type:jsonb;default:'[]'" json:"init_flow"` // 业务配置流程 {steps: [...]}
	// 关联 (可选)
	VendorID      *uint `gorm:"index" json:"vendor_id,omitempty"`
	DeviceModelID *uint `gorm:"index" json:"device_model_id,omitempty"`
	// 元数据
	IsDefault bool           `gorm:"default:false;index" json:"is_default"`
	Status    string         `gorm:"size:20;default:active" json:"status"` // active / inactive
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// DeviceTemplate v2.1 别名 → DeviceConfig (v2.2)
//
// Deprecated: Use DeviceConfig instead. Will be removed in v2.3.
type DeviceTemplate = DeviceConfig

// =====================================================================

// DeviceData 原始数据 (保留, v2.2 加 edge_device_id 关联)
type DeviceData struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	DeviceID     uint      `gorm:"index;not null" json:"device_id"`    // v2.2: 改为 EdgeDeviceID
	CollectorID  uint      `gorm:"index;not null" json:"collector_id"` // v2.2: 改为 NodeID
	DataJSON     string    `gorm:"type:text" json:"data_json"`
	Timestamp    time.Time `gorm:"index" json:"timestamp"`
	CreatedAt    time.Time `json:"created_at"`
	EdgeDeviceID *uint     `gorm:"index" json:"edge_device_id,omitempty"` // v2.2 新增 (与 DeviceID 二选一)
}

// =====================================================================

// UnifiedData 统一数据 (保留)
type UnifiedData struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	DeviceID     uint      `gorm:"index;not null" json:"device_id"` // v2.2: 改为 EdgeDeviceID
	SensorName   string    `gorm:"size:32;not null;index" json:"sensor_name"`
	Value        float64   `gorm:"not null" json:"value"`
	Unit         string    `gorm:"size:16" json:"unit"`
	Timestamp    time.Time `gorm:"index" json:"timestamp"`
	CreatedAt    time.Time `json:"created_at"`
	EdgeDeviceID *uint     `gorm:"index" json:"edge_device_id,omitempty"` // v2.2 新增
}

// =====================================================================

// DataSource 数据源主备管理 (保留)
type DataSource struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	Type      string    `gorm:"size:20;not null" json:"type"`
	Config    string    `gorm:"type:text" json:"config"` // JSON
	Priority  int       `gorm:"default:0" json:"priority"`
	Status    string    `gorm:"size:20;default:active" json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// =====================================================================

// OTATask OTA升级任务 (保留 CollectorID, 待 v2.3 改名 NodeID)
type OTATask struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	OtaID       string     `gorm:"column:ota_id;size:64;uniqueIndex;not null" json:"ota_id"`
	CollectorID uint       `gorm:"index;not null" json:"collector_id"` // 待 v2.3 改为 NodeID
	FirmwareID  uint       `gorm:"index" json:"firmware_id"`
	Status      string     `gorm:"size:20;default:pending" json:"status"`
	Progress    uint8      `gorm:"default:0" json:"progress"`
	ErrorMsg    string     `gorm:"size:256" json:"error_msg"`
	ToVersion   string     `gorm:"size:32" json:"to_version"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// =====================================================================

// Firmware 固件版本 (保留)
type Firmware struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Version   string    `gorm:"size:32;not null" json:"version"`
	Checksum  string    `gorm:"size:64;not null" json:"checksum"` // SHA256 hex
	SizeBytes uint64    `json:"size_bytes"`
	URL       string    `gorm:"size:256;not null" json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// =====================================================================

// Notification 通知 (保留)
type Notification struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Type      string    `gorm:"size:20;not null" json:"type"`
	Message   string    `gorm:"type:text;not null" json:"message"`
	Read      bool      `gorm:"default:false" json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// =====================================================================

// User 用户 (保留)
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"size:32;uniqueIndex;not null" json:"username"`
	PasswordHash string    `gorm:"size:128;not null" json:"-"`
	Role         string    `gorm:"size:20;default:user" json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// =====================================================================

// OperationLog 审计日志 (保留)
type OperationLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Action    string    `gorm:"size:32;not null" json:"action"`
	Target    string    `gorm:"size:64" json:"target"`
	CreatedAt time.Time `json:"created_at"`
}

// =====================================================================

// Vendor 厂商 (保留)
type Vendor struct {
	ID        uint          `gorm:"primaryKey" json:"id"`
	Name      string        `gorm:"size:64;not null" json:"name"`
	CreatedAt time.Time     `json:"created_at"`
	Models    []DeviceModel `gorm:"foreignKey:VendorID" json:"models,omitempty"`
}

// DeviceModel 设备型号 (保留)
type DeviceModel struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	VendorID  uint      `gorm:"index;not null" json:"vendor_id"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	Type      string    `gorm:"size:32;not null" json:"type"`
	Fields    string    `gorm:"type:text" json:"fields"`
	CreatedAt time.Time `json:"created_at"`
}

// =====================================================================

// CollectorEvent 节点状态变更事件 (保留 v2.1 名, 待 v2.3 改名 NodeEvent)
type CollectorEvent struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CollectorID uint      `gorm:"index;not null" json:"collector_id"` // 待 v2.3 改名 NodeID
	EventType   string    `gorm:"size:20;not null" json:"event_type"`
	OldStatus   string    `gorm:"size:20" json:"old_status"`
	NewStatus   string    `gorm:"size:20" json:"new_status"`
	CreatedAt   time.Time `json:"created_at"`
}

// =====================================================================

// CalibrationCache 校准数据缓存 (保留)
type CalibrationCache struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CollectorID uint      `gorm:"index;not null" json:"collector_id"` // 待 v2.3 改名 NodeID
	DeviceType  string    `gorm:"size:32;not null" json:"device_type"`
	Data        string    `gorm:"type:text;not null" json:"data"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// =====================================================================
