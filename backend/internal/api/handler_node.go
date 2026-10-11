package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// findNodeByID resolves a node by DB primary key (if numeric) or node_id string.
func findNodeByID(db *gorm.DB, id string) (*models.Node, error) {
	var node models.Node
	if intID, err := strconv.Atoi(id); err == nil {
		if db.First(&node, intID).Error == nil {
			return &node, nil
		}
	}
	if err := db.Where("node_id = ?", id).First(&node).Error; err != nil {
		return nil, err
	}
	return &node, nil
}

// emptyHardwareResources is returned until a node has reported ResourceReport.
// Hardware capabilities are authoritative node data and must never be invented server-side.
func emptyHardwareResources() map[string]interface{} {
	return map[string]interface{}{}
}

// registerNodeRoutes sets up node CRUD routes.
//
// driverRegistry is variadic so existing call sites (unit tests that do not
// exercise address semantics) stay source-compatible: a missing registry
// resolves through resolveDriverRegistry to the built-in one, exactly like
// registerEdgeDeviceRoutes. It is needed because PUT /nodes/:id/config writes
// edge_devices.hardware_id and must apply the SAME address gate as the
// edge-device create/update paths (G1, 2026-09-21).
func registerNodeRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager, registries ...*drivers.Registry) {
	driverRegistry := resolveDriverRegistry(registries...)
	eventBus := nodeMgr.EventBus()

	// List nodes (v2.2 compat path)
	//
	// 分页契约 (架构与接口评估 P1.2 裁决: items + total; 参数语义与
	// /automation-events、/logical-devices、/vendors、/device-configs 一致):
	// 查询参数 page (默认 1, <1 归 1) / page_size (默认 20, 超出 [1,200] 归 20),
	// 响应 data = {items, total, page, page_size}。
	// total 语义: **过滤后全量**条数, 不是当前页条数 (前端分页器用它算总页数)。
	//
	// 历史 (为什么必须改, 负债 I-11「假分页」): 本端点原为无参全量 Find + 裸数组
	// 返回 —— 前端 NodeList.vue 一直发 page/page_size, 后端**静默丢弃**,
	// :data 绑定的是本地全量数组, el-pagination 纯装饰: 用户以为在翻页, 实际是
	// 本地切片。同时 status/model/search 三个筛选也都只在前端当前页本地过滤
	// (§3.3.5 禁止把当前页本地筛选伪装成全局检索)。
	//
	// Order("id") 不是装饰: 真分页必须有稳定全序, 否则跨页可能重复或漏项
	// (无 ORDER BY 时 PG/SQLite 都不保证两次 OFFSET 查询的相对次序一致)。
	v1.GET("/nodes", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		if page < 1 {
			page = 1
		}
		if pageSize < 1 || pageSize > 200 {
			pageSize = 20
		}
		// 过滤条件必须在 Count 与 Find **两侧同时**生效, 否则 total 会变成未过滤的
		// 全量、分页器算出多余页数 (用户翻到空页)。
		q := db.WithContext(c.Request.Context()).Model(&models.Node{})
		if st := strings.TrimSpace(c.Query("status")); st != "" {
			q = q.Where("status = ?", st)
		}
		if model := strings.TrimSpace(c.Query("model")); model != "" {
			q = q.Where("model = ?", model)
		}
		// search 是**服务端**全库检索 (§3.3.5): 与前端本地过滤同口径 (name 或 model)。
		// LOWER+LIKE 在 PostgreSQL 与 SQLite 测试库都可用 (与 handler_logical_device.go:60
		// 同一写法)。
		if search := strings.TrimSpace(c.Query("search")); search != "" {
			like := "%" + strings.ToLower(search) + "%"
			q = q.Where("LOWER(name) LIKE ? OR LOWER(model) LIKE ?", like, like)
		}
		var total int64
		if err := q.Count(&total).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		// 非 nil 空切片: 空集序列化为 [] 而非 null (与 handler_data_source.go 同约定)。
		nodes := make([]models.Node, 0)
		if err := q.Order("id").Offset((page - 1) * pageSize).Limit(pageSize).Find(&nodes).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		Success(c, gin.H{"items": nodes, "total": total, "page": page, "page_size": pageSize})
	})

	// Global status transition history for the dashboard's operational timeline.
	// Must be registered before /nodes/:id so "status-history" is not treated as an ID.
	// GET /api/v1/nodes/status-history?limit=50
	v1.GET("/nodes/status-history", func(c *gin.Context) {
		limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
		if err != nil || limit < 1 || limit > 200 {
			limit = 50
		}

		type statusEvent struct {
			models.NodeEvent
			NodeName string `json:"node_name"`
		}
		var events []statusEvent
		if err := db.Table("node_events AS event").
			Select("event.*, nodes.name AS node_name").
			Joins("LEFT JOIN nodes ON nodes.node_id = event.node_id").
			Order("event.created_at DESC").
			Limit(limit).
			Scan(&events).Error; err != nil {
			Error(c, http.StatusInternalServerError, "query node status history failed")
			return
		}
		Success(c, events)
	})

	// Get node by DB id or node_id (v2.2 path)
	v1.GET("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		var node models.Node
		// Try by primary key first (if numeric)
		if intID, err := strconv.Atoi(id); err == nil {
			if db.First(&node, intID).Error == nil {
				Success(c, nodeDetailResponse{Node: node, ConfigWarnings: parseConfigWarnings(node)})
				return
			}
		}
		// Fallback: lookup by node_id string
		if err := db.Where("node_id = ?", id).First(&node).Error; err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		Success(c, nodeDetailResponse{Node: node, ConfigWarnings: parseConfigWarnings(node)})
	})

	// Status transition history for operational timelines.
	// GET /api/v1/nodes/:id/status-history?limit=50
	v1.GET("/nodes/:id/status-history", func(c *gin.Context) {
		node, err := findNodeByID(db, c.Param("id"))
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}

		limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
		if err != nil || limit < 1 || limit > 200 {
			limit = 50
		}

		var events []models.NodeEvent
		if err := db.Where("node_id = ?", node.NodeID).
			Order("created_at DESC").
			Limit(limit).
			Find(&events).Error; err != nil {
			Error(c, http.StatusInternalServerError, "query node status history failed")
			return
		}
		Success(c, events)
	})

	// Create node (v2.2 compat path)
	v1.POST("/nodes", func(c *gin.Context) {
		var dto struct {
			NodeID string `json:"node_id" binding:"required"`
			Name   string `json:"name"`
			Config string `json:"config"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		node := models.Node{NodeID: dto.NodeID, Name: dto.Name, Config: dto.Config}
		if err := db.Create(&node).Error; err != nil {
			if isUniqueConstraintError(err) {
				Error(c, http.StatusConflict, "node_id already exists")
				return
			}
			Error(c, http.StatusInternalServerError, "failed to create node")
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionCreate, node.NodeID, fmt.Sprint(node.ID))
		SuccessWithCode(c, http.StatusCreated, node)
	})

	// Update node (v2.2 path for PUT /collectors/:id)
	// M1 fix: use separate DTO to prevent field injection (ID, CreatedAt, etc.)
	v1.PUT("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		// M1 fix: bind to a separate DTO, then copy allowed fields only
		var dto struct {
			Name   *string `json:"name"`
			Config *string `json:"config"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		updates := map[string]interface{}{}
		if dto.Name != nil {
			updates["name"] = *dto.Name
		}

		if dto.Config != nil {
			updates["config"] = *dto.Config
		}
		if len(updates) > 0 {
			if err := db.Model(node).Updates(updates).Error; err != nil {
				Error(c, http.StatusInternalServerError, "failed to update node")
				return
			}
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionUpdate, node.NodeID, fmt.Sprint(node.ID))
		// Reload node to get updated fields
		if err := db.First(node, node.ID).Error; err != nil {
			Error(c, http.StatusInternalServerError, "failed to reload node")
			return
		}
		Success(c, node)
	})

	// Delete node (v2.2 path for DELETE /collectors/:id)
	v1.DELETE("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		nodeIDStr := node.NodeID
		if err := db.Delete(node).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.InvalidateNodeIDCache(nodeIDStr)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionDelete, nodeIDStr, nodeIDStr)
		SuccessMsg(c, nil, "deleted")
	})

	// Get channels for node
	v1.GET("/nodes/:id/channels", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var channels []models.Channel
		db.Where("node_id = ?", node.NodeID).Find(&channels)
		Success(c, channels)
	})

	// Get node data
	//
	// S4 修复 (2026-10-07): limit 必须归一到有界默认值。
	// 改前 limit 的错误被丢弃、也不做范围检查, 于是:
	//   ?limit=-1  → GORM 丢弃整条 LIMIT 子句 → 返回**全表**(无界);
	//   ?limit=0 或 ?limit=abc → GORM 写 LIMIT 0 → **静默空页**(用户看不到任何提示);
	//   ?limit=100000000 → 未钳制的超大值 → 单次响应体不受控。
	// 三种都归 100, 与 handler_data.go 的 limit 写法保持同一口径。
	v1.GET("/nodes/:id/data", func(c *gin.Context) {
		id := c.Param("id")
		limitStr := c.DefaultQuery("limit", "100")
		limit, _ := strconv.Atoi(limitStr)
		if limit <= 0 || limit > 1000 {
			limit = 100
		}
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var data []models.DeviceData
		db.Where("node_id = ?", node.NodeID).Order("timestamp DESC").Limit(limit).Find(&data)
		Success(c, data)
	})

	// Ping node
	v1.POST("/nodes/:id/ping", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		if err := nodeMgr.SendPing(node.NodeID); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		SuccessMsg(c, nil, "ping sent")
	})

	// Get node config (v2.2 ConfigManifest)
	v1.GET("/nodes/:id/config", getNodeConfig(db, nodeMgr))

	// Update node config (v2.2 incremental update)
	v1.PUT("/nodes/:id/config", updateNodeConfig(db, nodeMgr, driverRegistry))

	// BUG-08 fix: OTA history for a specific node
	v1.GET("/nodes/:id/ota/history", getNodeOTAHistory(db))

	// POST /api/v1/nodes/:id/bus/i2c/scan
	n := v1.Group("/nodes")
	n.POST("/:id/bus/i2c/scan", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			HardwareID string `json:"hardware_id"`
		}
		// P1-B: a discarded bind error answered "scan triggered" for a request
		// whose hardware_id was never read.
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		// NOTE: requires MQTT broadcast I2C_SCAN to node firmware
		Success(c, gin.H{
			"devices": []string{}, "request_id": fmt.Sprintf("i2c-%s-%d", node.NodeID, time.Now().Unix()),
		})
	})

	// POST /api/v1/nodes/:id/config/sync
	n.POST("/:id/config/sync", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		// Force one full config push for this exact node.  Do not reuse
		// OnServerStartup here: that path consults the manager's transient
		// online cache, which may be empty immediately after a backend restart
		// even when this DB-backed node is online.  Returning "syncing" without
		// publishing a manifest makes the API lie and can send a V2 action to a
		// device that has no matching runtime channel.
		decisions := nodeMgr.SyncGate().OnConfigChange(nodemgr.ConfigChangeEvent{
			Type: nodemgr.CfgChangeNode, Action: nodemgr.CfgActionUpdate,
			NodeID: node.NodeID, EntityID: node.NodeID, Actor: "api:force_config_sync",
		})
		if len(decisions) != 1 {
			Error(c, http.StatusInternalServerError, "unable to create config sync decision")
			return
		}
		if err := nodeMgr.SendConfigManifestWithDecision(decisions[0]); err != nil {
			// ⚠ 2026-10-09（用户要求"前端显示错误"，真机验证时发现）：
			// 把**用户配置错误**与**服务端故障**分开。
			//
			// 原实现一律 500。于是"两条 UART 都开了 DMA"（用户可以在前端改的
			// 配置问题）返回 500 Internal Server Error —— 前端会把它当成
			// 服务端故障（重试、报"系统错误"），用户根本看不出是自己配错了。
			//
			// 判据：nodemgr 的**校验类**错误是用户可行动的 ⇒ 400 + 原文。
			// 其余（编码失败、DB 写失败、链路不可用）仍是 500。
			if nodemgr.IsManifestValidationError(err) {
				Error(c, http.StatusBadRequest, err.Error())
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		Success(c, gin.H{"node_id": node.NodeID, "status": "syncing"})
	})

	// GET /api/v1/nodes/:id/capabilities
	n.GET("/:id/capabilities", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		buses := emptyHardwareResources()
		var capList []string
		if node.Capabilities != "" && node.Capabilities != "{}" {
			var parsed map[string]interface{}
			if json.Unmarshal([]byte(node.Capabilities), &parsed) == nil {
				if raw, ok := parsed["buses"]; ok {
					if reported, ok := raw.(map[string]interface{}); ok {
						buses = reported
					}
				}
			}
		}
		for k := range buses {
			capList = append(capList, k)
		}
		sort.Strings(capList)
		Success(c, gin.H{
			"capabilities": capList,
			"buses":        buses,
		})
	})

	// GET /api/v1/nodes/:id/hardware/config
	n.GET("/:id/hardware/config", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		hardware := gin.H{
			"wifi_ssid": node.WiFiSSID,
			"wifi_rssi": node.WiFiRSSI,
			"free_heap": node.FreeHeapBytes,
		}
		if node.HardwareInfo != "" && node.HardwareInfo != "{}" {
			var parsed map[string]interface{}
			if json.Unmarshal([]byte(node.HardwareInfo), &parsed) == nil {
				if b, ok := parsed["buses"]; ok {
					hardware["buses"] = b
				}
			}
		}
		if hardware["buses"] == nil {
			hardware["buses"] = gin.H{}
		}
		Success(c, gin.H{
			"hardware": hardware,
		})
	})

	// PUT /api/v1/nodes/:id/hardware/config
	n.PUT("/:id/hardware/config", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Hardware map[string]interface{} `json:"hardware"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body")
			return
		}
		if _, ok := req.Hardware["buses"]; ok {
			Error(c, http.StatusBadRequest, "hardware.buses is read-only reported state")
			return
		}
		Success(c, gin.H{
			"node_id": node.NodeID,
			"status":  "updated",
		})
	})

	// POST /api/v1/nodes/:id/query-resources
	n.POST("/:id/query-resources", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		deviceID := node.NodeID
		requestID, err := nodeMgr.SendQueryResources(deviceID)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		Success(c, gin.H{
			"node_id":    id,
			"request_id": requestID,
			"status":     "sent",
		})
	})

	// GET /api/v1/nodes/:id/dma-channels — get DMA channel info for a node
	// Merges device-reported state with config intent (node.Config.dma_configs).
	// If config says enabled but device reports disabled, use config intent state.
	n.GET("/:id/dma-channels", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}

		var channels []models.DmaChannelInfo
		if node.DmaChannels != "" && node.DmaChannels != "[]" {
			if err := json.Unmarshal([]byte(node.DmaChannels), &channels); err != nil {
				logger.Warnf("[%s] Failed to parse dma_channels JSONB: %v", node.NodeID, err)
			}
		}

		// Merge config intent: if dma_configs says enabled=true but device state=2(disabled),
		// override state to 0(free) — config has been pushed but device hasn't reported back yet.
		var configDmas []models.DmaChannelConfig
		if node.Config != "" {
			var cfg map[string]interface{}
			if err := json.Unmarshal([]byte(node.Config), &cfg); err == nil {
				if dc, ok := cfg["dma_configs"]; ok {
					if dcJSON, err := json.Marshal(dc); err == nil {
						json.Unmarshal(dcJSON, &configDmas)
					}
				}
			}
		}
		if len(configDmas) > 0 {
			configMap := make(map[uint32]bool)
			for _, cd := range configDmas {
				configMap[cd.DmaID] = cd.Enabled
			}
			for i := range channels {
				if enabled, ok := configMap[channels[i].DmaID]; ok {
					if enabled && channels[i].State == 2 {
						channels[i].State = 0 // config says enabled, device hasn't caught up
					} else if !enabled && channels[i].State != 2 {
						channels[i].State = 2 // config says disabled
					}
				}
			}
		}

		Success(c, gin.H{
			"dma_channels": channels,
		})
	})

	// PUT /api/v1/nodes/:id/dma-config — update DMA configuration for a node
	// Merges new configs by dma_id (does not overwrite unrelated channels).
	// Uses SELECT FOR UPDATE to prevent read-modify-write race conditions.
	n.PUT("/:id/dma-config", func(c *gin.Context) {
		id := c.Param("id")

		var configs []models.DmaChannelConfig
		if err := c.ShouldBindJSON(&configs); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		// Input validation: dma_id, duplicate check, bind_to length AND VALUE.
		//
		// bind_to 必须校验**值**，不能只看长度。2026-10-04 实错：S3 的 node.Config 里
		// 长期存着
		//
		//	{"dma_id": 1, "bind_to": "uart/uart1", "enabled": true}
		//
		// 而固件派生的规范 hw_id 是 "uart/UART1"（只有大小写不同）。固件侧
		// dma_pool_apply_config 不校验、直接把它标成 ALLOCATED 并写入 bound_to，
		// 于是：
		//   - 预抢占（preempt）用 strcmp 比对，永远匹配不上 -> 占不回那条通道；
		//   - dma_pool_release_by_hw 同样 strcmp -> 永远释放不掉。
		// 结果是该 GDMA 通道被**永久占用**。S3 只有 5 条 GDMA，被占 1 条后
		// 「3 UART + SPI2 + I2C0」就分不出 DMA，I2C0 打
		// "No DMA for i2c/I2C0"，整个 ConfigManifest 以 ESP_ERR_NOT_FOUND 被拒、
		// success=false。表面症状像 I2C/SPI 的问题，真正原因却是一条没人记得的
		// 历史 DMA 绑定 —— 而且它只在"通道数刚好用满 DMA 资源"时才暴露。
		//
		// 两类拒绝：
		//   1) 形状不对（不是 "<bus>/<ID>"）；
		//   2) 大小写与规范 id 不匹配 —— 只提示正确写法，不静默改写：
		//      静默改写会让"用户以为填的值"与"系统实际用的值"再次不一致，
		//      正是本次事故的成因。
		seen := make(map[uint32]bool)
		for i, cfg := range configs {

			if seen[cfg.DmaID] {
				Error(c, http.StatusBadRequest, fmt.Sprintf("duplicate dma_id %d", cfg.DmaID))
				return
			}
			seen[cfg.DmaID] = true
			if len(cfg.BindTo) > 16 {
				Error(c, http.StatusBadRequest, fmt.Sprintf("configs[%d].bind_to exceeds 16 characters", i))
				return
			}
			if cfg.BindTo != "" {
				if !hwIDShapeOK(cfg.BindTo) {
					Error(c, http.StatusBadRequest, fmt.Sprintf(
						"configs[%d].bind_to %q is not a hardware id; expected \"<bus>/<ID>\" such as \"uart/UART0\" or \"spi/SPI2\"",
						i, cfg.BindTo))
					return
				}
				if suggestion := hwIDCaseSuggestion(cfg.BindTo); suggestion != "" {
					Error(c, http.StatusBadRequest, fmt.Sprintf(
						"configs[%d].bind_to %q does not match any hardware id (compared case-sensitively on the device); did you mean %q?",
						i, cfg.BindTo, suggestion))
					return
				}
			}
		}

		// Use transaction with SELECT FOR UPDATE to prevent concurrent modification
		tx := db.Begin()
		var node models.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", id).First(&node).Error; err != nil {
			tx.Rollback()
			Error(c, http.StatusNotFound, "node not found")
			return
		}

		// Parse existing node.Config JSON
		var cfg map[string]interface{}
		if node.Config != "" && node.Config != "{}" {
			if err := json.Unmarshal([]byte(node.Config), &cfg); err != nil {
				logger.Warnf("[%s] Failed to parse node.Config JSON: %v", node.NodeID, err)
			}
		}
		if cfg == nil {
			cfg = map[string]interface{}{}
		}

		// Load existing DMA configs for merge
		var existingConfigs []models.DmaChannelConfig
		if dc, ok := cfg["dma_configs"]; ok {
			if dcJSON, err := json.Marshal(dc); err == nil {
				if err := json.Unmarshal(dcJSON, &existingConfigs); err != nil {
					logger.Warnf("[%s] Failed to parse existing dma_configs: %v", node.NodeID, err)
				}
			}
		}

		// Merge by dma_id: existing configs as base, overlay new ones
		configMap := make(map[uint32]models.DmaChannelConfig)
		for _, ec := range existingConfigs {
			configMap[ec.DmaID] = ec
		}
		for _, nc := range configs {
			configMap[nc.DmaID] = nc
		}
		merged := make([]models.DmaChannelConfig, 0, len(configMap))
		for _, v := range configMap {
			merged = append(merged, v)
		}
		// Sort by dma_id for deterministic output (avoids spurious config sync)
		sort.Slice(merged, func(i, j int) bool { return merged[i].DmaID < merged[j].DmaID })

		cfg["dma_configs"] = merged
		cfgJSON, err := json.Marshal(cfg)
		if err != nil {
			tx.Rollback()
			Error(c, http.StatusInternalServerError, "failed to marshal config")
			return
		}
		if err := tx.Model(&node).Update("config", string(cfgJSON)).Error; err != nil {
			tx.Rollback()
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		// v2.5: Immediately update DmaChannels JSONB so GET /dma-channels
		// returns the correct state without waiting for next ResourceReport.
		var devChannels []models.DmaChannelInfo
		if node.DmaChannels != "" && node.DmaChannels != "[]" {
			json.Unmarshal([]byte(node.DmaChannels), &devChannels)
		}
		for _, nc := range configs {
			found := false
			for j := range devChannels {
				if devChannels[j].DmaID == nc.DmaID {
					devChannels[j].BoundTo = nc.BindTo
					if nc.Enabled {
						devChannels[j].State = 1 // allocated
					} else {
						devChannels[j].State = 2 // disabled
					}
					found = true
					break
				}
			}
			if !found {
				state := uint8(2)
				if nc.Enabled {
					state = 1
				}
				devChannels = append(devChannels, models.DmaChannelInfo{
					DmaID:   nc.DmaID,
					State:   state,
					BoundTo: nc.BindTo,
				})
			}
		}
		dmaJSON, err := json.Marshal(devChannels)
		if err != nil {
			tx.Rollback()
			Error(c, http.StatusInternalServerError, "failed to marshal dma_channels")
			return
		}
		if err := tx.Model(&node).Update("dma_channels", string(dmaJSON)).Error; err != nil {
			tx.Rollback()
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		tx.Commit()

		// Trigger config sync to push updated manifest to device
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionUpdate, node.NodeID, node.NodeID)

		Success(c, gin.H{
			"node_id": node.NodeID,
			"status":  "sent",
		})
	})

	// v2.5: Log stream routes
	registerLogStreamRoutes(n, db, nodeMgr)
}

// edgeDeviceConfigItem is a lightweight EdgeDevice representation for config API responses.
// It omits nested associations (Node, Channel, DeviceConfig) to keep the response clean.
type edgeDeviceConfigItem struct {
	Type           string     `json:"type"`
	ParserID       string     `json:"parser_id"`
	ID             uint       `json:"id"`
	Name           string     `json:"name"`
	NodeID         string     `json:"node_id"`
	ChannelID      uint       `json:"channel_id"`
	DeviceConfigID uint       `json:"device_config_id"`
	HardwareID     string     `json:"hardware_id"`
	IntervalMs     int        `json:"interval_ms"`
	Enabled        bool       `json:"enabled"`
	Status         string     `json:"status"`
	ErrorCode      int        `json:"error_code"`
	LastDataAt     *time.Time `json:"last_data_at"`
	LastError      string     `json:"last_error"`
	ConfigVersion  string     `json:"config_version"`
	InitState      string     `json:"init_state"`
	InitLastStep   int        `json:"init_last_step"`
	InitTotalSteps int        `json:"init_total_steps"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func edgeDeviceToConfigItem(ed models.EdgeDevice) edgeDeviceConfigItem {
	return edgeDeviceConfigItem{
		Type:           ed.Type,
		ParserID:       ed.ParserID,
		ID:             ed.ID,
		Name:           ed.Name,
		NodeID:         ed.NodeID,
		ChannelID:      ed.ChannelID,
		DeviceConfigID: ed.DeviceConfigID,
		HardwareID:     ed.HardwareID,
		IntervalMs:     ed.IntervalMs,
		Enabled:        ed.Enabled,
		Status:         ed.Status,
		ErrorCode:      ed.ErrorCode,
		LastDataAt:     ed.LastDataAt,
		LastError:      ed.LastError,
		ConfigVersion:  ed.ConfigVersion,
		InitState:      ed.InitState,
		InitLastStep:   ed.InitLastStep,
		InitTotalSteps: ed.InitTotalSteps,
		CreatedAt:      ed.CreatedAt,
		UpdatedAt:      ed.UpdatedAt,
	}
}

// nodeConfigResponse is the response structure for GET /nodes/:id/config
type nodeConfigResponse struct {
	Node            models.Node            `json:"node"`
	Channels        []models.Channel       `json:"channels"`
	EdgeDevices     []edgeDeviceConfigItem `json:"edge_devices"`
	DeviceConfigs   []models.DeviceConfig  `json:"device_configs"`
	Epoch           uint64                 `json:"epoch"`
	ProtocolVersion string                 `json:"protocol_version"`
}

// getNodeConfig returns the full configuration manifest for a node.
// nodeDetailResponse 是节点详情的响应体：在 models.Node 之上**覆盖**
// config_warnings 的序列化形状。
//
// ⚠⚠ 2026-10-09（真机验证发现的缺陷，§215）：
//
//	models.Node.ConfigWarnings 是 **string**（gorm jsonb，未加 serializer），
//	直接 Success(c, node) 会让 API 把整个 JSON **当成字符串**返回：
//	  "config_warnings": "[{\"code\":...}]"   <-- 字符串，不是数组
//	而前端写的是 Array.isArray(raw) 判断 ⇒ **永远不成立** ⇒ 提示永不显示。
//	⚠ 我的前端单测用的 mock 是手写的**真数组**，所以测试全绿 ——
//	  又一个"mock 形状与后端真实响应对不上"的假绿（与 §214.4 同型）。
//
// 为何用**嵌入**而不手写字段清单：
//
//	手写一份 Node 字段清单就是 P4 禁止的"同一语义两处定义"，
//	而且会随着 models.Node 加字段而静默漏掉（我第一版手写时
//	就把 WiFiRSSI 写成了 WifiRSSI，编译报错才发现）。
//	嵌入后，Node 的全部字段由嵌入体自动提供，
//	只有 config_warnings 被外层字段遮蔽（Go 嵌入的标准行为：浅层优先）。
type nodeDetailResponse struct {
	models.Node
	ConfigWarnings []any `json:"config_warnings"`
}

// parseConfigWarnings 把 nodes.config_warnings 的 JSON 字符串解析成数组。
// 失败时返回空数组（而非保留原字符串）：一条格式错误的告警不该崩掉整个详情页。
func parseConfigWarnings(node models.Node) []any {
	warnings := []any{}
	if node.ConfigWarnings == "" {
		return warnings
	}
	if err := json.Unmarshal([]byte(node.ConfigWarnings), &warnings); err != nil {
		logger.Warnf("[%s] Failed to parse config_warnings JSONB: %v", node.NodeID, err)
		return []any{}
	}
	return warnings
}

func getNodeConfig(db *gorm.DB, nodeMgr *nodemgr.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}

		// Load channels for this node
		var channels []models.Channel
		db.Where("node_id = ?", node.NodeID).Find(&channels)

		// Load edge devices for this node
		var edgeDevices []models.EdgeDevice
		db.Where("node_id = ?", node.NodeID).Find(&edgeDevices)

		// Collect device_config IDs from edge devices
		deviceConfigIDs := make([]uint, 0, len(edgeDevices))
		for _, ed := range edgeDevices {
			if ed.DeviceConfigID > 0 {
				deviceConfigIDs = append(deviceConfigIDs, ed.DeviceConfigID)
			}
		}

		// Load device configs
		var deviceConfigs []models.DeviceConfig
		if len(deviceConfigIDs) > 0 {
			db.Where("id IN ?", deviceConfigIDs).Find(&deviceConfigs)
		}

		// Convert edge devices to clean response items (no nested associations)
		edgeDeviceItems := make([]edgeDeviceConfigItem, 0, len(edgeDevices))
		for _, ed := range edgeDevices {
			edgeDeviceItems = append(edgeDeviceItems, edgeDeviceToConfigItem(ed))
		}

		// Get current epoch
		epoch := nodeMgr.EventBus().CurrentEpoch()

		protocolVersion := node.ProtocolVersion
		if protocolVersion == "" {
			protocolVersion = "2.2"
		}

		Success(c, gin.H{"data": nodeConfigResponse{
			Node:            *node,
			Channels:        channels,
			EdgeDevices:     edgeDeviceItems,
			DeviceConfigs:   deviceConfigs,
			Epoch:           epoch,
			ProtocolVersion: protocolVersion,
		}, "message": "ok"})
	}
}

// nodeConfigUpdateRequest is the request body for PUT /nodes/:id/config
// Only non-nil fields will be updated (partial update).
type nodeConfigUpdateRequest struct {
	Channels    *[]channelUpdateItem    `json:"channels,omitempty"`
	EdgeDevices *[]edgeDeviceUpdateItem `json:"edge_devices,omitempty"`
}

type channelUpdateItem struct {
	ID          uint   `json:"id"`
	Address     string `json:"address,omitempty"` // maps to HardwareID hex string
	HardwareID  string `json:"hardware_id,omitempty"`
	IntervalMs  *int   `json:"interval_ms,omitempty"`
	BusType     string `json:"bus_type,omitempty"`
	BusConfig   string `json:"bus_config,omitempty"`
	Config      string `json:"config,omitempty"`
	TemplateIDs string `json:"template_ids,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type edgeDeviceUpdateItem struct {
	ID             uint   `json:"id"`
	Name           string `json:"name,omitempty"`
	ChannelID      *uint  `json:"channel_id,omitempty"`
	DeviceConfigID *uint  `json:"device_config_id,omitempty"`
	HardwareID     string `json:"hardware_id,omitempty"`
	IntervalMs     *int   `json:"interval_ms,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`
}

// updateNodeConfig handles incremental (partial) config updates for a node.
// BUG-04 fix: idempotent — if the effective config content is unchanged,
// skip epoch increment and MQTT push, return 200 immediately.
func updateNodeConfig(db *gorm.DB, nodeMgr *nodemgr.Manager, registries ...*drivers.Registry) gin.HandlerFunc {
	driverRegistry := resolveDriverRegistry(registries...)
	return func(c *gin.Context) {
		id := c.Param("id")

		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}

		var req nodeConfigUpdateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if req.Channels != nil {
			seen := make(map[uint]struct{}, len(*req.Channels))
			for _, ch := range *req.Channels {
				if ch.ID != 0 {
					if _, exists := seen[ch.ID]; exists {
						Error(c, http.StatusBadRequest, "duplicate channel update")
						return
					}
					seen[ch.ID] = struct{}{}
				}
				if isPeripheralChannelType(ch.BusType) {
					Error(c, http.StatusBadRequest, "GPIO and PWM are peripheral resources, not channels")
					return
				}
			}
		}
		if req.EdgeDevices != nil {
			seen := make(map[uint]struct{}, len(*req.EdgeDevices))
			for _, edge := range *req.EdgeDevices {
				if edge.ID != 0 {
					if _, exists := seen[edge.ID]; exists {
						Error(c, http.StatusBadRequest, "duplicate edge device update")
						return
					}
					seen[edge.ID] = struct{}{}
				}
				if edge.ChannelID == nil {
					continue
				}
				var channel models.Channel
				if err := db.Where("id = ? AND node_id = ?", *edge.ChannelID, node.NodeID).First(&channel).Error; err != nil || validateTransportChannel(&channel) != nil {
					Error(c, http.StatusBadRequest, "edge device must bind to a transport channel")
					return
				}
			}
		}

		channelUpdates := []channelUpdateItem{}
		if req.Channels != nil {
			channelUpdates = *req.Channels
		}
		edgeDeviceUpdates := []edgeDeviceUpdateItem{}
		if req.EdgeDevices != nil {
			edgeDeviceUpdates = *req.EdgeDevices
		}
		var updatedFields []string
		if err := db.Transaction(func(tx *gorm.DB) error {
			var lockedNode models.Node
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", node.NodeID).First(&lockedNode).Error; err != nil {
				return err
			}
			channelCandidates := make(map[uint]models.Channel)
			channelWrites := make(map[uint]map[string]interface{})
			loadChannel := func(id uint) (*models.Channel, error) {
				if candidate, ok := channelCandidates[id]; ok {
					return &candidate, nil
				}
				var current models.Channel
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND node_id = ?", id, node.NodeID).First(&current).Error; err != nil {
					return nil, err
				}
				channelCandidates[id] = current
				return &current, nil
			}
			for _, ch := range channelUpdates {
				if ch.ID == 0 {
					continue
				}
				current, err := loadChannel(ch.ID)
				if err != nil {
					return err
				}
				candidate := *current
				updates := map[string]interface{}{}
				if ch.Address != "" {
					candidate.HardwareID, updates["hardware_id"] = ch.Address, ch.Address
				}
				if ch.HardwareID != "" {
					candidate.HardwareID, updates["hardware_id"] = ch.HardwareID, ch.HardwareID
				}
				if ch.IntervalMs != nil {
					candidate.IntervalMs, updates["interval_ms"] = *ch.IntervalMs, *ch.IntervalMs
				}
				if ch.BusType != "" {
					candidate.BusType, updates["bus_type"] = ch.BusType, ch.BusType
				}
				if ch.BusConfig != "" {
					candidate.BusConfig, updates["bus_config"] = ch.BusConfig, ch.BusConfig
				}
				if ch.Config != "" {
					candidate.Config, updates["config"] = ch.Config, ch.Config
				}
				if ch.TemplateIDs != "" {
					candidate.TemplateIDs, updates["template_ids"] = ch.TemplateIDs, ch.TemplateIDs
				}
				if ch.Enabled != nil {
					candidate.Enabled, updates["enabled"] = *ch.Enabled, *ch.Enabled
				}
				if err := validateTransportChannelType(&candidate); err != nil {
					return err
				}
				// 第四条写 channels.bus_config 的路径（2026-10-03 审查发现的 P2）：
				// POST /channels、向导内联、PUT /channels 都有 ensureUARTBusConfig 守卫，
				// 只有这里没有 —— 同一不变量只守一半等于没守。
				// 本路径会原样落库调用方给的 bus_config，因此必须同样校验：
				// 连引脚路由都不是的值（非 hex / <2 字节）直接拒绝，
				// 避免绕过其它三条路径的守卫写入一个不可解析的通道。
				// 注意只校验合法性，不要求「可改波特率」—— 理由见
				// ensureUARTBusConfig 上方关于两个约束被混为一谈的说明。
				if isUARTChannel(&candidate) {
					if err := ensureBusConfigExtras(node, &candidate); err != nil {
						return err
					}
					if candidate.BusConfig != updates["bus_config"] {
						updates["bus_config"] = candidate.BusConfig
					}
				}
				if err := validateChannelPeripheralConflicts(tx, candidate); err != nil {
					return err
				}
				channelCandidates[ch.ID] = candidate
				channelWrites[ch.ID] = updates
			}
			type edgeWrite struct {
				existing       models.EdgeDevice
				updates        map[string]interface{}
				finalChannelID uint
			}
			edgeWrites := make(map[uint]edgeWrite)
			for _, ed := range edgeDeviceUpdates {
				if ed.ID == 0 {
					continue
				}
				var existing models.EdgeDevice
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND node_id = ?", ed.ID, node.NodeID).First(&existing).Error; err != nil {
					return err
				}
				targetChannelID := existing.ChannelID
				if ed.ChannelID != nil {
					targetChannelID = *ed.ChannelID
				}
				targetChannel, err := loadChannel(targetChannelID)
				if err != nil {
					return fmt.Errorf("channel does not belong to node")
				}
				if err := validateTransportChannel(targetChannel); err != nil {
					return err
				}
				configID := existing.DeviceConfigID
				if ed.DeviceConfigID != nil {
					configID = *ed.DeviceConfigID
				}
				config, err := validateDeviceConfigForChannel(tx, configID, targetChannel)
				if err != nil {
					return err
				}
				// G2 (2026-09-21): type/device_config_id are written ONLY when this request
				// really selects a device_config_id.
				//
				// The old code wrote `{"device_config_id": config.ID, "type": config.DeviceType}`
				// unconditionally. When the request omitted device_config_id, configID stayed
				// existing.DeviceConfigID; for the devices that matter here it is 0, and
				// validateDeviceConfigForChannel(0) returns the ZERO DeviceConfig (ID 0,
				// DeviceType ""). The row was therefore rewritten as device_config_id=0,
				// type="" — the device silently lost its driver type while keeping its
				// hardware_id. That is not only data loss: type is the input that selects the
				// address gate, so an erased type made validateEdgeDeviceAddress treat the
				// device as "not cataloged" and pass every later address write (G1 连带效应).
				//
				// Partial-update semantics (fields absent from the request stay untouched) are
				// what the endpoint advertises in nodeConfigUpdateRequest, so the fix restores
				// the documented behaviour instead of adding a new rule.
				updates := map[string]interface{}{}
				if ed.DeviceConfigID != nil {
					if configID > 0 {
						updates["device_config_id"] = config.ID
						updates["type"] = config.DeviceType
					} else {
						// Explicitly detaching the template is a legal partial update, but it
						// must NOT erase the driver type: handler_edge_device.go (the R2-fixed
						// update path) does exactly this — configID == 0 writes
						// device_config_id = 0 and leaves `type` alone. Erasing the type here
						// would leave the row with hardware_id but no type, and the address
						// gate keys off `type` (driverRequiresTargetAddress returns
						// cataloged=false for ""), so a follow-up hardware_id write in the SAME
						// request would be validated against nothing at all.
						updates["device_config_id"] = uint(0)
					}
				}
				if ed.Name != "" {
					updates["name"] = ed.Name
				}
				if ed.ChannelID != nil {
					updates["channel_id"] = targetChannelID
				}
				if ed.HardwareID != "" {
					updates["hardware_id"] = ed.HardwareID
				}
				if ed.IntervalMs != nil {
					updates["interval_ms"] = *ed.IntervalMs
				}
				if ed.Enabled != nil {
					updates["enabled"] = *ed.Enabled
				}
				// G1 (2026-09-21): this endpoint used to write edge_devices.hardware_id with
				// ZERO validation, so it bypassed the create/update gate in
				// handler_edge_device.go and accepted "UART1" (a bus name) as a device
				// address. Every dispatch then failed inside deviceaction.ParseHardwareAddress
				// and the operation sat QUEUED until its deadline.
				//
				// The gate below reuses validateEdgeDeviceAddress (single truth source) and
				// runs on the CANDIDATE values before any write, so a rejected batch leaves
				// the row untouched.
				//
				// ORDER IS THE CONTRACT: candidateType must be the FINAL type this request
				// writes. type and hardware_id can change in the same batch, and validating
				// against the STORED type would check the device against the action catalog it
				// is leaving — that is precisely how a bus name survives a type switch
				// (bmp280 -> sn3001_rain with hardware_id="UART1" would be judged as bmp280,
				// which needs no address, and pass).
				candidateType := existing.Type
				if v, ok := updates["type"]; ok {
					candidateType, _ = v.(string)
				}
				candidateHardwareID := existing.HardwareID
				if ed.HardwareID != "" {
					candidateHardwareID = ed.HardwareID
				}
				// Same trigger contract as the R2-fixed update path
				// (handler_edge_device.go: `dto.HardwareID != nil || candidateType != d.Type`),
				// so the two write paths cannot disagree about when a stored address is
				// re-validated:
				//   - the request supplies hardware_id, or
				//   - the device type changes, which re-binds the stored address to a
				//     different action catalog.
				// A request that touches neither (rename / enable / interval on a row whose
				// address predates the gate) is deliberately NOT re-validated: rejecting it
				// would turn pre-existing data into "this device can no longer even be
				// renamed", and repair stays available by sending a legal address. That is the
				// reviewed R2 contract, and G1 must not silently tighten it.
				//
				// This is still not a hole: switching such a device back onto an
				// address-requiring type changes candidateType, which re-validates the stored
				// value at that moment and rejects it.
				if ed.HardwareID != "" || candidateType != existing.Type {
					if err := validateEdgeDeviceAddress(driverRegistry, candidateType, candidateHardwareID); err != nil {
						return err
					}
				}
				// G6 (2026-09-21): this endpoint wrote edge_devices.hardware_id with the
				// address gate above but WITHOUT the per-channel uniqueness guard, so two
				// devices of one model could be parked on the same slave address through
				// the side door while the front door (PUT /edge-devices/:id) rejected the
				// very same state with 400. Two identical Modbus unit ids on one bus is
				// not a validation nicety: every addressed action for both devices is
				// then ambiguous at the wire level.
				//
				// It reuses checkDeviceUniqueness (single truth source, same function the
				// create and update paths call) on the CANDIDATE triple — channel, type
				// and address all AFTER this request's changes — so the two doors cannot
				// disagree about which states are reachable. excludeID is the row's own id,
				// otherwise the device collides with itself.
				//
				// Ordering note: the address gate above is deliberately first. It rejects
				// impossible values (a bus name) with the message that names the legal
				// 1-254 domain; uniqueness then judges the remaining, well-formed states.
				if err := checkDeviceUniqueness(tx, targetChannelID, candidateType, candidateHardwareID, existing.ID); err != nil {
					return err
				}
				edgeWrites[ed.ID] = edgeWrite{existing: existing, updates: updates, finalChannelID: targetChannelID}
			}
			// Validate every resulting reference to each changed channel. Devices moved
			// away by this same request do not block a channel lifecycle update.
			for channelID, candidate := range channelCandidates {
				if _, changed := channelWrites[channelID]; !changed {
					continue
				}
				if err := validateTransportChannel(&candidate); err != nil {
					return err
				}
				var bound []models.EdgeDevice
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("channel_id = ? AND node_id = ?", channelID, node.NodeID).Find(&bound).Error; err != nil {
					return err
				}
				for _, device := range bound {
					if write, updated := edgeWrites[device.ID]; updated && write.finalChannelID != channelID {
						continue
					}
					configID := device.DeviceConfigID
					if write, updated := edgeWrites[device.ID]; updated {
						// Comma-ok, not a bare assertion: G2 (2026-09-21) made this key
						// conditional (it is present only when the request selected a
						// device_config_id), and a bare .(uint) on an absent key panics.
						// Falling back to the stored value is also the correct semantics:
						// the device keeps its current template when the request does not
						// change it.
						if v, ok := write.updates["device_config_id"]; ok {
							configID, _ = v.(uint)
						}
					}
					if _, err := validateDeviceConfigForChannel(tx, configID, &candidate); err != nil {
						return err
					}
				}
			}
			for channelID, updates := range channelWrites {
				if len(updates) == 0 {
					continue
				}
				if err := tx.Model(&models.Channel{}).Where("id = ?", channelID).Updates(updates).Error; err != nil {
					return err
				}
			}
			for _, write := range edgeWrites {
				if err := tx.Model(&write.existing).Updates(write.updates).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		for i, ch := range channelUpdates {
			if ch.ID == 0 {
				continue
			}
			if ch.Address != "" {
				updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.address", i))
			}
			if ch.HardwareID != "" {
				updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.hardware_id", i))
			}
			if ch.IntervalMs != nil {
				updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.interval_ms", i))
			}
			if ch.BusType != "" {
				updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.bus_type", i))
			}
			if ch.BusConfig != "" {
				updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.bus_config", i))
			}
			if ch.Config != "" {
				updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.config", i))
			}
			if ch.TemplateIDs != "" {
				updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.template_ids", i))
			}
			if ch.Enabled != nil {
				updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.enabled", i))
			}
		}
		for i, ed := range edgeDeviceUpdates {
			if ed.ID == 0 {
				continue
			}
			if ed.Name != "" {
				updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.name", i))
			}
			if ed.ChannelID != nil {
				updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.channel_id", i))
			}
			if ed.DeviceConfigID != nil {
				updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.device_config_id", i))
			}
			if ed.HardwareID != "" {
				updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.hardware_id", i))
			}
			if ed.IntervalMs != nil {
				updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.interval_ms", i))
			}
			if ed.Enabled != nil {
				updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.enabled", i))
			}
		}

		// Only check if updatedFields is non-empty; skip DB snapshot comparison
		// to avoid race conditions. Worst case: extra config push, which is safe.
		configChanged := len(updatedFields) > 0

		if configChanged {
			// Increment epoch via EventBus (Publish increments epoch)
			nodemgr.EmitConfigChange(
				c,
				nodeMgr.EventBus(),
				nodemgr.CfgChangeNode,
				nodemgr.CfgActionUpdate,
				node.NodeID,
				node.NodeID,
			)
		}

		// Get the new epoch after increment (or current if unchanged)
		newEpoch := nodeMgr.EventBus().CurrentEpoch()

		// If node is online, SyncGate will automatically push config
		// (EmitConfigChange publishes to the bus, SyncGate consumes and pushes)
		// No need to manually call PushConfig here.

		Success(c, gin.H{"data": gin.H{
			"epoch":          newEpoch,
			"updated_fields": updatedFields,
		}, "message": "config updated"})
	}
}

// --- BUG-04 helper functions for idempotency ---

// getNodeOTAHistory returns OTA task history for a specific node.
// BUG-08 fix: This route was missing, causing 404 on the node detail page.
func getNodeOTAHistory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}

		var tasks []models.OTATask
		db.Where("node_id = ?", node.NodeID).Order("created_at DESC").Find(&tasks)

		Success(c, gin.H{"data": tasks})
	}
}
