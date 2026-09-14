package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/notify"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// =====================================================================
// 外发通知通道 HTTP 层 (设计/外发通知通道.md §4 冻结契约: 6 个端点)
//
//	GET    /api/v1/notification-channels          列表 (永不返回 Secret)
//	POST   /api/v1/notification-channels          创建
//	PUT    /api/v1/notification-channels/:id      更新 (secret 未传 = 不改, 传空串 = 清空)
//	DELETE /api/v1/notification-channels/:id      删除 (deliveries 保留, 审计价值)
//	POST   /api/v1/notification-channels/:id/test 发送测试消息 (用户即时验证配置)
//	GET    /api/v1/notification-deliveries        投递审计 (channel_id/state 过滤 + 分页)
//
// 三条贯穿全文件的纪律:
//  1. DTO 白名单: 绝不 c.ShouldBindJSON(&model) —— 逐字段显式赋值, 防止
//     mass-assignment 改到 ID/CreatedAt/SecretHint 这类不该由请求体决定的列;
//  2. Secret 只写不读 (设计 §2.3-b): 响应体只可能出现 secret_hint 与 has_secret,
//     明文 secret 绝不进任何 HTTP 响应。注意凭据还可能在 target_url 的查询串里
//     (企业微信 key=), 由 models.NotificationChannel.MarshalJSON 统一脱敏;
//  3. 零值语义 (设计 §7.6): TimeoutSec/MaxRetries 是 *int —— nil = 未配置 (用
//     应用层默认), 非 nil 的 0 = 用户显式零值 (max_retries=0 就是"不重试")。
//     归一化只发生在应用层, 绝不在写库前把 0 折叠成默认值。
// =====================================================================

// 分页默认/上限。与 handler_automation.go 的 automation-events 保持同一口径:
// 默认 20, 非法或越界一律回落到 20 (越界页由分页器返回空集, 不报错)。
const (
	defaultNotificationPageSize = 20
	maxNotificationPageSize     = 200
)

// validChannelTypes 是通道类型合法集 (models 已冻结 webhook|wecom|onebot 三个预设标识)。
var validChannelTypes = map[string]bool{
	models.ChannelTypeWebhook: true,
	models.ChannelTypeWeCom:   true,
	models.ChannelTypeOneBot:  true,
}

// validDeliveryStates 是投递状态合法集 (设计 §5 状态机)。
var validDeliveryStates = map[string]bool{
	models.DeliveryStatePending:   true,
	models.DeliveryStateDelivered: true,
	models.DeliveryStateFailed:    true,
}

// maxChannelURLBytes 是 target_url 的形态上限, 与模型列 size:512 对齐。
// 为什么在应用层再挡一次: 超长 URL 在 SQLite 上会被静默存下, 到 PG 才报错;
// 提前拒绝能给出明确 400 而不是 500。
const maxChannelURLBytes = 512

// =====================================================================
// handler 依赖注入
// =====================================================================

// notificationChannelTester 是本层需要的最小投递能力 (一个方法)。
//
// 为什么是 DeliverToChannelAsync 而不是 DeliverAsync: /test 是**配置自检**,
// 必须投到用户点的那条通道上 —— 走 DeliverAsync 的话, enabled=false 的通道
// 会被调度器的 SQL 直接筛掉 (设计 §5 的 enabled 语义由 enabledChannels 保证),
// 用户点"测试"将什么都看不到 (静默无操作), 这正是本任务要杜绝的"假绿"。
// notify.Dispatcher.DeliverToChannelAsync 保留全部出站纪律 (SSRF/超时/重试/
// 脱敏/每次尝试一行审计), 只跳过 enabled 与 min_level 这两道筛选。
//
// 为什么用接口而不是直接依赖 *notify.Dispatcher 做参数类型: 单测要注入
// 契约为"记录收到了什么"的假实现, 用来断言 handler **确实调用了投递**;
// 若 handler 自己构造 Dispatcher, 测试就只能断言"表里有没有行", 无法区分
// "端点真的发起了投递"与"别的路径写了行"。
type notificationChannelTester interface {
	DeliverToChannelAsync(ctx context.Context, channel models.NotificationChannel, msg notify.Message)
}

// notificationChannelHandler 是 6 个端点的处理器。
type notificationChannelHandler struct {
	db     *gorm.DB
	tester notificationChannelTester
}

// registerNotificationChannelRoutes 注册外发通知通道 + 投递审计路由。
//
// tester 为 nil 时 /test 显式返回 503, 而不是 nil 解引用 panic
// (与 routes.go 里 datasourceSvc == nil 的既有处理同思路)。
func registerNotificationChannelRoutes(v1 *gin.RouterGroup, db *gorm.DB, tester notificationChannelTester) {
	h := &notificationChannelHandler{db: db, tester: tester}
	channels := v1.Group("/notification-channels")
	{
		channels.GET("", h.list)
		channels.POST("", h.create)
		channels.PUT("/:id", h.update)
		channels.DELETE("/:id", h.remove)
		channels.POST("/:id/test", h.test)
	}
	v1.GET("/notification-deliveries", h.listDeliveries)
}

// =====================================================================
// 请求 DTO (白名单)
// =====================================================================

// createNotificationChannelRequest 创建通道。
// secret 可空 (可空即"暂不配置密钥", 例如仅用 URL 认证的企业微信)。
// max_retries / timeout_sec 是指针: 区分"未传"(用默认)与"显式 0"(不重试)。
type createNotificationChannelRequest struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	TargetURL    string `json:"target_url"`
	Secret       string `json:"secret"`
	Template     string `json:"template"`
	MinLevel     string `json:"min_level"`
	Enabled      *bool  `json:"enabled"`
	TimeoutSec   *int   `json:"timeout_sec"`
	MaxRetries   *int   `json:"max_retries"`
	AllowPrivate *bool  `json:"allow_private"`
}

// updateNotificationChannelRequest 更新通道。
//
// Secret 是 *string, 这正是设计 §7.6 的教训: 用 string 的话
// "未传" 与 "传空串" 都是 "", 于是"不改密钥"与"清空密钥"不可区分 ——
// 每次改名字都会静默把已配置的密钥抹掉 (§7.6 变异自证的 ②)。
// 全字段指针: 未传 = 不动该字段 (部分更新), 传了才写。
type updateNotificationChannelRequest struct {
	Name         *string `json:"name"`
	Type         *string `json:"type"`
	TargetURL    *string `json:"target_url"`
	Secret       *string `json:"secret"`
	Template     *string `json:"template"`
	MinLevel     *string `json:"min_level"`
	Enabled      *bool   `json:"enabled"`
	TimeoutSec   *int    `json:"timeout_sec"`
	MaxRetries   *int    `json:"max_retries"`
	AllowPrivate *bool   `json:"allow_private"`
}

// =====================================================================
// 视图模型 (响应白名单)
// =====================================================================

// notificationChannelView 是通道的**唯一**出站形状。
//
// 为什么不直接 Success(c, channel): 模型带 MarshalJSON 确实挡住了 secret 与
// target_url 查询串里的 key, 但 ①"挡住"是模型的实现细节, API 层应当**结构性**
// 保证凭据不可能出现在响应里 (少一个字段就少一条泄露路径), ②has_secret 这个
// 前端需要的布尔量在模型里没有对应列。因此这里显式列出允许出站的字段,
// 凭据只以 secret_hint (末 4 位) 与 has_secret 的形式出现。
type notificationChannelView struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	TargetURL  string `json:"target_url"`
	SecretHint string `json:"secret_hint"`
	// HasSecret 让前端能渲染"已设置 (末 4 位 xxxx)"与"未设置"两种态,
	// 而不必去猜 secret_hint 为空是"没密钥"还是"密钥太短"。
	HasSecret    bool      `json:"has_secret"`
	Template     string    `json:"template"`
	MinLevel     string    `json:"min_level"`
	Enabled      bool      `json:"enabled"`
	TimeoutSec   *int      `json:"timeout_sec"`
	MaxRetries   *int      `json:"max_retries"`
	AllowPrivate bool      `json:"allow_private"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// newNotificationChannelView 把通道模型投影成出站视图。
//
// target_url 先过 models.RedactTargetURL: 企业微信的 key 就在查询串里,
// 原样回显等于把密钥交给任何能读列表的人 (设计 §2.3-b 的完整含义)。
func newNotificationChannelView(channel models.NotificationChannel) notificationChannelView {
	return notificationChannelView{
		ID:           channel.ID,
		Name:         channel.Name,
		Type:         channel.Type,
		TargetURL:    models.RedactTargetURL(channel.TargetURL),
		SecretHint:   channel.SecretHint,
		HasSecret:    strings.TrimSpace(channel.Secret) != "",
		Template:     channel.Template,
		MinLevel:     channel.MinLevel,
		Enabled:      channel.Enabled,
		TimeoutSec:   channel.TimeoutSec,
		MaxRetries:   channel.MaxRetries,
		AllowPrivate: channel.AllowPrivate,
		CreatedAt:    channel.CreatedAt,
		UpdatedAt:    channel.UpdatedAt,
	}
}

func newNotificationChannelViews(channels []models.NotificationChannel) []notificationChannelView {
	views := make([]notificationChannelView, 0, len(channels))
	for _, channel := range channels {
		views = append(views, newNotificationChannelView(channel))
	}
	return views
}

// =====================================================================
// GET /api/v1/notification-channels
// =====================================================================

// list 返回分页后的通道列表 (与 automation-events 同一分页形状:
// items + total + page + page_size 回显)。
func (h *notificationChannelHandler) list(c *gin.Context) {
	page := parsePositiveInt(c.DefaultQuery("page", "1"))
	pageSize := normalizeNotificationPageSize(c.DefaultQuery("page_size", "20"))

	q := h.db.Model(&models.NotificationChannel{})
	if channelType := strings.TrimSpace(c.Query("type")); channelType != "" {
		q = q.Where("type = ?", channelType)
	}
	if raw := strings.TrimSpace(c.Query("enabled")); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid enabled filter")
			return
		}
		q = q.Where("enabled IS NOT NULL AND enabled = ?", enabled)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		Error(c, http.StatusInternalServerError, "查询通知通道失败")
		return
	}
	// 非 nil 空切片: 空集序列化为 [] 而非 null (与 handler_data_source.go 同约定)。
	items := make([]models.NotificationChannel, 0)
	if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		Error(c, http.StatusInternalServerError, "查询通知通道失败")
		return
	}
	Success(c, gin.H{
		"items":     newNotificationChannelViews(items),
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// =====================================================================
// POST /api/v1/notification-channels
// =====================================================================

func (h *notificationChannelHandler) create(c *gin.Context) {
	var req createNotificationChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	// 逐字段白名单赋值 —— 绝不用请求体直接构造/更新模型。
	name := strings.TrimSpace(req.Name)
	channelType := strings.ToLower(strings.TrimSpace(req.Type))
	if name == "" || len(name) > 64 {
		Error(c, http.StatusBadRequest, "name 必填且不超过 64 字符")
		return
	}
	if !validChannelTypes[channelType] {
		Error(c, http.StatusBadRequest, "type 必须是 webhook|wecom|onebot 之一")
		return
	}
	targetURL := strings.TrimSpace(req.TargetURL)
	if len(targetURL) > maxChannelURLBytes {
		Error(c, http.StatusBadRequest, "target_url 不超过 512 字符")
		return
	}
	// URL 在**写路径**就校验形态 (scheme 必须 http/https、必须有 host)。
	// 内网地址的 SSRF 判定不在这里: allow_private 尚未随通道存下, 而出站时
	// notify.Client 会按存在 DB 里的 allow_private 重新判定 (设计 §7.1)。
	if targetURL != "" {
		if _, err := notify.ParseTargetURL(targetURL); err != nil {
			Error(c, http.StatusBadRequest, "target_url 无效: "+err.Error())
			return
		}
	}

	// 设计 §7.6: 默认值只在应用层给, 且必须**可表达显式零值** ——
	// 指针原样落库, 不做任何折叠 (nil → DB NULL → 运行时取默认)。
	channel := models.NotificationChannel{
		Name:       name,
		Type:       channelType,
		TargetURL:  targetURL,
		Template:   strings.TrimSpace(req.Template),
		MinLevel:   notify.MinLevelOf(req.MinLevel),
		Enabled:    req.Enabled == nil || *req.Enabled,
		TimeoutSec: req.TimeoutSec,
		MaxRetries: req.MaxRetries,
	}
	if req.AllowPrivate != nil {
		channel.AllowPrivate = *req.AllowPrivate
	}
	channel.ApplySecret(req.Secret)

	if err := h.db.Create(&channel).Error; err != nil {
		logger.Warnf("api: create notification channel failed: %v", err)
		Error(c, http.StatusInternalServerError, "创建通知通道失败")
		return
	}
	// 回读一次: GORM 在部分驱动下会把未指定列的 DB 默认值回写进结构体,
	// 回读保证响应里的 enabled/min_level 就是库里真实生效的值。
	if err := h.db.First(&channel, channel.ID).Error; err != nil {
		logger.Warnf("api: reload notification channel %d failed: %v", channel.ID, err)
	}
	Success(c, newNotificationChannelView(channel))
}

// =====================================================================
// PUT /api/v1/notification-channels/:id
// =====================================================================

func (h *notificationChannelHandler) update(c *gin.Context) {
	var channel models.NotificationChannel
	if err := h.db.First(&channel, c.Param("id")).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			Error(c, http.StatusNotFound, "通知通道不存在")
			return
		}
		Error(c, http.StatusInternalServerError, "查询通知通道失败")
		return
	}
	var req updateNotificationChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	updates := map[string]interface{}{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > 64 {
			Error(c, http.StatusBadRequest, "name 必填且不超过 64 字符")
			return
		}
		updates["name"] = name
	}
	if req.Type != nil {
		channelType := strings.ToLower(strings.TrimSpace(*req.Type))
		if !validChannelTypes[channelType] {
			Error(c, http.StatusBadRequest, "type 必须是 webhook|wecom|onebot 之一")
			return
		}
		updates["type"] = channelType
	}
	if req.TargetURL != nil {
		targetURL := strings.TrimSpace(*req.TargetURL)
		if len(targetURL) > maxChannelURLBytes {
			Error(c, http.StatusBadRequest, "target_url 不超过 512 字符")
			return
		}
		if targetURL != "" {
			if _, err := notify.ParseTargetURL(targetURL); err != nil {
				Error(c, http.StatusBadRequest, "target_url 无效: "+err.Error())
				return
			}
		}
		updates["target_url"] = targetURL
	}
	if req.Template != nil {
		updates["template"] = strings.TrimSpace(*req.Template)
	}
	if req.MinLevel != nil {
		updates["min_level"] = notify.MinLevelOf(*req.MinLevel)
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	// 指针原样写入 map: 显式 0 是不会被 GORM 省略的非零 interface 值,
	// 未传的 nil 则根本不出现在 updates 里 (设计 §7.6 的完整落实)。
	if req.TimeoutSec != nil {
		updates["timeout_sec"] = *req.TimeoutSec
	}
	if req.MaxRetries != nil {
		updates["max_retries"] = *req.MaxRetries
	}
	if req.AllowPrivate != nil {
		updates["allow_private"] = *req.AllowPrivate
	}
	// Secret 的两种语义在这里分岔 (设计 §4 冻结契约 + §7.6 零值陷阱):
	//   req.Secret == nil  → 未传 = **不改**, 连 secret_hint 都不动;
	//   req.Secret != nil  → 传了, "" 表示清空 (ApplySecret 同步清 hint)。
	if req.Secret != nil {
		channel.ApplySecret(*req.Secret)
		updates["secret"] = channel.Secret
		updates["secret_hint"] = channel.SecretHint
	}

	if len(updates) > 0 {
		if err := h.db.Model(&models.NotificationChannel{}).Where("id = ?", channel.ID).Updates(updates).Error; err != nil {
			logger.Warnf("api: update notification channel %d failed: %v", channel.ID, err)
			Error(c, http.StatusInternalServerError, "更新通知通道失败")
			return
		}
	}
	// 回读: 响应必须反映库里真实的值 (而不是内存里拼出来的期望值)。
	if err := h.db.First(&channel, channel.ID).Error; err != nil {
		Error(c, http.StatusInternalServerError, "查询通知通道失败")
		return
	}
	Success(c, newNotificationChannelView(channel))
}

// =====================================================================
// DELETE /api/v1/notification-channels/:id
// =====================================================================

// remove 删除通道。
//
// **不**连带删除 notification_deliveries (设计 §4 明文冻结): 投递审计证明
// "某条告警当时是否真的发出去了", 删通道就抹掉审计等于允许事后销毁证据。
// 审计行保留 channel_id 悬空 (模型本来就没有 FK 级联, 与 AutoMigrate 的
// DisableForeignKeyConstraintWhenMigrating 一致)。
func (h *notificationChannelHandler) remove(c *gin.Context) {
	var channel models.NotificationChannel
	if err := h.db.First(&channel, c.Param("id")).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			Error(c, http.StatusNotFound, "通知通道不存在")
			return
		}
		Error(c, http.StatusInternalServerError, "查询通知通道失败")
		return
	}
	if err := h.db.Delete(&models.NotificationChannel{}, channel.ID).Error; err != nil {
		logger.Warnf("api: delete notification channel %d failed: %v", channel.ID, err)
		Error(c, http.StatusInternalServerError, "删除通知通道失败")
		return
	}
	Success(c, gin.H{"id": channel.ID, "deleted": true, "deliveries_retained": true})
}

// =====================================================================
// POST /api/v1/notification-channels/:id/test
// =====================================================================

// 测试消息的固定标识: 用户与运维都靠它区分"这是配置自检"与"这是真实告警"
// (审计行里同样以 source=notification-channel-test 标识)。
const (
	testNotificationTitle  = "通知通道测试"
	testNotificationSource = "notification-channel-test"
)

// test 立即投递一条测试消息到该通道。
//
// 语义 (设计 §4: "让用户能立即验证配置对不对"):
//   - 先落一条 notifications 行, 再经 Dispatcher 投递 —— 投递审计 (deliveries)
//     与真实通知走**完全相同**的路径, 用户看到的成功/失败就是真实投递的结果;
//   - 通道不存在 → 404; 引擎未装配 → 503;
//   - 异步投递, 响应不等待出站往返 (设计 §2.2: 出站绝不阻塞 HTTP 请求)。
//
// 与常规投递的唯一差别: 走 DeliverToChannelAsync 直达用户选中的那条通道,
// 因此 enabled=false 与 min_level 过滤都不会让"测试"静默无操作 —— 用户点
// "测试"就是要立刻验证凭据/URL 对不对, 一个被停用的通道恰恰最需要自检
// (否则"先禁用再测通"这个最自然的操作顺序根本不成立)。
func (h *notificationChannelHandler) test(c *gin.Context) {
	var channel models.NotificationChannel
	if err := h.db.First(&channel, c.Param("id")).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			Error(c, http.StatusNotFound, "通知通道不存在")
			return
		}
		Error(c, http.StatusInternalServerError, "查询通知通道失败")
		return
	}
	if h.tester == nil {
		Error(c, http.StatusServiceUnavailable, "通知投递引擎未启用")
		return
	}
	notification := models.Notification{
		Type:        models.AlertLevelInfo,
		Title:       testNotificationTitle,
		Message:     "这是一条来自 EHomeSystem 的测试消息。收到即表示通道配置可用。",
		Description: "通道 #" + strconv.FormatUint(uint64(channel.ID), 10) + " (" + channel.Name + ") 配置自检",
		Source:      testNotificationSource,
		SourceID:    strconv.FormatUint(uint64(channel.ID), 10),
	}
	if err := h.db.Create(&notification).Error; err != nil {
		logger.Warnf("api: persist notification channel test message failed: %v", err)
		Error(c, http.StatusInternalServerError, "创建测试通知失败")
		return
	}
	// 与真实通知走**完全相同**的投递实现 (deliverToChannel): 同样的出站 client、
	// 同样的 SSRF 判定/超时/重试, 因此用户看到的成功/失败就是真实投递的结果。
	h.tester.DeliverToChannelAsync(testDeliveryContext(c), channel, notify.MessageFromNotification(notification))

	SuccessMsg(c, gin.H{
		"channel_id":      channel.ID,
		"notification_id": notification.ID,
		"state":           models.DeliveryStatePending,
		"deliveries_url":  "/api/v1/notification-deliveries?channel_id=" + strconv.FormatUint(uint64(channel.ID), 10),
	}, "测试消息已发出")
}

// testDeliveryContext 给异步投递一个**脱离请求生命周期**的 context:
// 请求返回后 c.Request.Context() 会被取消, 若不脱钩, 出站请求会被连带取消,
// 用户永远看不到投递结果 (审计行停在 pending)。取消信号一律丢弃, 取值保留。
func testDeliveryContext(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return context.WithoutCancel(c.Request.Context())
}

// =====================================================================
// GET /api/v1/notification-deliveries
// =====================================================================

// listDeliveries 返回投递审计 (channel_id / state 过滤, 分页)。
// 只读列表: 审计行本身不含凭据 (error_message 在落库前已过 RedactText 截断)。
func (h *notificationChannelHandler) listDeliveries(c *gin.Context) {
	page := parsePositiveInt(c.DefaultQuery("page", "1"))
	pageSize := normalizeNotificationPageSize(c.DefaultQuery("page_size", "20"))

	q := h.db.Model(&models.NotificationDelivery{})
	if raw := strings.TrimSpace(c.Query("channel_id")); raw != "" {
		channelID, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid channel_id")
			return
		}
		q = q.Where("channel_id = ?", uint(channelID))
	}
	if state := strings.TrimSpace(c.Query("state")); state != "" {
		if !validDeliveryStates[state] {
			Error(c, http.StatusBadRequest, "state 必须是 pending|delivered|failed 之一")
			return
		}
		q = q.Where("state = ?", state)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		Error(c, http.StatusInternalServerError, "查询投递记录失败")
		return
	}
	// 非 nil 空切片: 空集序列化为 [] 而非 null。
	items := make([]models.NotificationDelivery, 0)
	if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		Error(c, http.StatusInternalServerError, "查询投递记录失败")
		return
	}
	Success(c, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// =====================================================================
// 参数归一化
// =====================================================================

// parsePositiveInt 把查询参数转成正整数; 非法/非正一律回落到 1。
func parsePositiveInt(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 {
		return 1
	}
	return value
}

// normalizeNotificationPageSize 归一化 page_size: 非数字/0/负数/超上限 → 默认 20。
func normalizeNotificationPageSize(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 || value > maxNotificationPageSize {
		return defaultNotificationPageSize
	}
	return value
}
