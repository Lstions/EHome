package commandexec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"ehome/backend/internal/audit"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

var (
	ErrConfirmationRequired    = errors.New("confirmation is required")
	ErrConfirmationInvalid     = errors.New("confirmation is invalid, expired, or already used")
	ErrConfirmationForbidden   = errors.New("this actor may not confirm the action")
	ErrConfirmationNotNeeded   = errors.New("confirmation is not required for this action")
	ErrConfirmationRateLimited = errors.New("confirmation rate limit exceeded")
)

// 2026-11-15 用户裁定：危险动作只保留「二次点击确认」，**不再要求用户重新输入
// 账户密码**（整程序范围）。因此这里删除了 recentAuthenticationWindow（登录超过
// 10 分钟即要求重认证）这道门：
//   - 它是 ErrRecentAuthRequired 的唯一来源，删掉后确认弹窗不会再变成密码框；
//   - 保留的仍是**授权**判定（Enabled + 系统管理员主体），那是"谁能确认"，
//     与"刚刚是否输过密码"是两件事，混在一起会让非管理员永远卡在密码框上。
//
// 单次令牌、参数摘要绑定、5 分钟 TTL、限流、审计等安全不变量不变。
const confirmationLifetime = 5 * time.Minute
const confirmationRateWindow = time.Minute
const maxConfirmationsPerWindow = 5

type ConfirmationInput struct {
	EdgeDeviceID uint
	ActorUserID  uint
	ActionID     string
	Params       json.RawMessage
	Reason       string
	SourceIP     string
}

type ConfirmationGrant struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IssueConfirmation binds a single use token to the actor, target, action
// version and canonical request hash. It intentionally checks the same
// current availability facts as Create, so confirmation cannot mint a grant
// for an offline/stale node and use it later after the environment changed.
func (s *Service) IssueConfirmation(ctx context.Context, in ConfirmationInput) (*ConfirmationGrant, error) {
	// 2026-10-15 用户裁定：二次确认弹窗**只保留确认/取消**，不再强制填写
	// 「操作理由」。理由从必填降级为**可选审计备注**：留空合法，只保留
	// 长度上界防止超长文本进入审计元数据。
	if in.EdgeDeviceID == 0 || in.ActorUserID == 0 || strings.TrimSpace(in.ActionID) == "" || utf8.RuneCountInString(in.Reason) > 512 {
		return nil, ErrConfirmationRequired
	}
	var grant ConfirmationGrant
	// 2026-11-20（D5）：能力快照过期（后端重启后 Hello 作废快照）属于可自愈失败，
	// 刷一次资源快照再重试一次，避免手动操作得到一句无从下手的 action unavailable。
	err := s.runWithCapabilityRefresh(ctx, in.EdgeDeviceID, func() error {
		grant = ConfirmationGrant{}
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if !s.dispatchEnabled {
				return ErrActionUnavailable
			}
			// periph_cmd 分流: GPIO/PWM 动作无 channel/manifest/capability 事实,
			// 目标解析与 gate 与 Create 同源 (resolveActionTarget)。
			if deviceaction.IsPeriphAction(in.ActionID) {
				edge, err := resolveActionTarget(tx, CreateInput{EdgeDeviceID: in.EdgeDeviceID, ActionID: in.ActionID})
				if err != nil {
					return err
				}
				if edge.Node.Status != "online" {
					return ErrActionUnavailable
				}
				definition, ok := s.actions.Get(edge.Type, in.ActionID)
				if !ok || !definition.Enabled || !deviceaction.CurrentEngineAllows(definition) {
					return ErrActionUnavailable
				}
				if !definition.RiskLevel.RequiresConfirmation() {
					return ErrConfirmationNotNeeded
				}
				params, err := deviceaction.CanonicalizeParams(definition.InputSchema, in.Params)
				if err != nil {
					return fmt.Errorf("%w: %v", ErrInvalidParams, err)
				}
				if err := requirePeriphConfig(tx, edge.NodeID, definition.ID, params); err != nil {
					return ErrActionUnavailable
				}
				return s.issueConfirmation(tx, &grant, in.ActorUserID, edge.ID, definition, params, in.Reason, in.SourceIP)
			}
			var edge models.EdgeDevice
			if err := tx.Preload("Node").First(&edge, in.EdgeDeviceID).Error; err != nil {
				return err
			}
			if !edge.Enabled || edge.Status == "inactive" || edge.Node.Status != "online" {
				return ErrActionUnavailable
			}
			if _, err := loadActionChannel(tx, edge); err != nil {
				return ErrActionUnavailable
			}
			if err := requireReportedActionChannel(edge.Node, edge.ChannelID); err != nil {
				return ErrActionUnavailable
			}
			if _, _, capsErr := currentCapabilities(edge.Node, s.now); capsErr != nil {
				// 只有"快照缺失/过期"可自愈，必须把哨兵带出去让外层刷新重试；
				// "节点不支持 ChannelCmdV2"/"能力解码失败"仍按不可用处理。
				if errors.Is(capsErr, ErrCapabilityStale) {
					return errCapabilityStaleUnavailable()
				}
				return ErrActionUnavailable
			}
			definition, ok := s.actions.Get(edge.Type, in.ActionID)
			if !ok || !definition.Enabled {
				return ErrActionUnavailable
			}
			if !definition.RiskLevel.RequiresConfirmation() {
				return ErrConfirmationNotNeeded
			}
			params, err := deviceaction.CanonicalizeParams(definition.InputSchema, in.Params)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidParams, err)
			}
			return s.issueConfirmation(tx, &grant, in.ActorUserID, edge.ID, definition, params, in.Reason, in.SourceIP)
		})
	})
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

// issueConfirmation is the shared token-minting tail of IssueConfirmation:
// recent-auth + rate-limit + single-use token persistence + audit. Caller
// supplies canonical params and the resolved target id (edge_devices id for
// channel_cmd_v2, nodes id for periph_cmd).
func (s *Service) issueConfirmation(tx *gorm.DB, grant *ConfirmationGrant, actorUserID, targetID uint, definition deviceaction.Definition, params []byte, reason, sourceIP string) error {
	digest := sha256.Sum256(params)
	requestHash := hex.EncodeToString(digest[:])
	var user models.User
	if err := tx.First(&user, actorUserID).Error; err != nil {
		return err
	}
	now := s.now()
	// 只判"有没有资格确认"，不判"多久没输入过密码"（2026-11-15 用户裁定）。
	if !user.Enabled || user.SubjectKey == nil || *user.SubjectKey != models.SystemAdminSubjectKey {
		return ErrConfirmationForbidden
	}
	var issued int64
	if err := tx.Model(&models.CommandConfirmation{}).Where("actor_user_id = ? AND edge_device_id = ? AND action_id = ? AND created_at >= ?", actorUserID, targetID, definition.ID, now.Add(-confirmationRateWindow)).Count(&issued).Error; err != nil {
		return err
	}
	if issued >= maxConfirmationsPerWindow {
		return ErrConfirmationRateLimited
	}
	rawToken, err := randomConfirmationToken()
	if err != nil {
		return err
	}
	tokenHash := confirmationHash(rawToken)
	expiresAt := now.Add(confirmationLifetime)
	*grant = ConfirmationGrant{Token: rawToken, ExpiresAt: expiresAt}
	if err := tx.Create(&models.CommandConfirmation{TokenHash: tokenHash, ActorUserID: actorUserID, EdgeDeviceID: targetID, ActionID: definition.ID, ActionVersion: definition.Version, RequestHash: requestHash, ExpiresAt: grant.ExpiresAt, CreatedAt: now}).Error; err != nil {
		return err
	}
	return audit.NewWriter(tx).Write(audit.Event{ActorType: "user", ActorUserID: &actorUserID, EventName: "device_action.confirmation_issued", Result: "issued", SourceIP: sourceIP, TargetType: "edge_device", TargetID: fmt.Sprint(targetID), Metadata: map[string]interface{}{"action_id": definition.ID, "action_version": definition.Version, "request_hash": requestHash, "reason": reason}})
}

func (s *Service) consumeConfirmation(tx *gorm.DB, rawToken string, actorID, edgeDeviceID uint, definition deviceaction.Definition, requestHash string) error {
	if strings.TrimSpace(rawToken) == "" {
		return ErrConfirmationRequired
	}
	now := s.now()
	var user models.User
	if err := tx.First(&user, actorID).Error; err != nil {
		return err
	}
	if !user.Enabled {
		return ErrConfirmationForbidden
	}
	result := tx.Model(&models.CommandConfirmation{}).Where("token_hash = ? AND actor_user_id = ? AND edge_device_id = ? AND action_id = ? AND action_version = ? AND request_hash = ? AND consumed_at IS NULL AND expires_at > ?", confirmationHash(rawToken), actorID, edgeDeviceID, definition.ID, definition.Version, requestHash, now).Update("consumed_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConfirmationInvalid
	}
	return nil
}

func randomConfirmationToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func confirmationHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
