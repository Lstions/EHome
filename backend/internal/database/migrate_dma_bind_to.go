package database

import (
	"encoding/json"
	"fmt"
	"strings"

	"ehome/backend/internal/hwid"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// MigrateDMABindToResult 记录一次 dma_configs.bind_to 大小写规范化的结果。
//
// Changes 的每项形如 `F0F5BD02F35C: dma_configs[1] "uart/uart1" -> "uart/UART1"`，
// 用于日志/运维核对"到底改了哪些值"；Unknown 是认识不了的 id（原样保留）。
type MigrateDMABindToResult struct {
	Scanned int
	Updated int
	Skipped int
	Changes []string
	Unknown []string
}

// MigrateDMABindToCanonicalCase 把 nodes.config.dma_configs[].bind_to 里
// **仅仅大小写不符**的历史值改写为设备端规范 id（uart/UART1、spi/SPI2 …）。

// 为什么需要这条迁移（2026-11-19 回归）：
//
//	前端 busResourceHelpers.resourceBindingKey() 曾对整串调用 toLowerCase()，
//	于是「总线配置」页每次绑定 DMA 都下发 uart/uart1 这类小写值。设备端
//	dma_pool 用 strcmp 比对（预抢占/释放），小写值永远匹配不上 ⇒ 对应 GDMA
//	通道被永久占用（2026-10-04 事故的同一机制，a001a34d 已加写入校验）。
//	前端修好后，库里已经存下的小写值不会自己变干净：它们仍会随每次配置同步
//	下发，继续占着通道。
//
// 边界（刻意保守）：
//   - 只修"与规范 id 仅大小写不同"的值；其它值（拼错、超长、未知控制器）
//     原样保留并逐条告警 —— 它们应当由**写入校验**拒绝并让用户显式修正，
//     迁移不做猜测性改写；
//   - 只改 dma_configs 里每个条目的 bind_to，其它配置键的值语义保持不变
//     （顶层用 json.RawMessage 原样回写，避免数字精度/键序被 Go 二次编码改变；
//     jsonb 列的空白与键序由 PG 自己归一，这一点无法也不应由迁移控制）；
//   - 不触碰 nodes.updated_at：这是数据修复，不是用户编辑；
//   - 幂等：修好后重跑扫不到任何"仅大小写不符"的值，Updated=0。
func MigrateDMABindToCanonicalCase(db *gorm.DB) (*MigrateDMABindToResult, error) {
	res := &MigrateDMABindToResult{}
	if db == nil {
		return nil, fmt.Errorf("migrate dma bind_to: database is nil")
	}
	if !db.Migrator().HasTable("nodes") {
		return res, nil
	}

	type row struct {
		ID     uint
		NodeID string
		Config string
	}

	// 分批（按主键游标）而不是一次 Find 全表：nodes.config 是 jsonb，
	// 生产库行数不多但单行可能不小，分批可让内存上界与行数无关。
	//
	// ⚠ WHERE 里**只能**用 id 游标与 IS NOT NULL，不能拿 config 做文本比较：
	//   nodes.config 是 jsonb，PG 会把 `config <> ''` 解析成 ''::jsonb 并报
	//   "invalid input syntax for type json"，`config LIKE '%…%'` 更直接报
	//   "operator does not exist: jsonb ~~ unknown"。
	// 2026-11-19 生产实测：这条 WHERE 让启动迁移直接 FATAL，容器进入 CrashLoop
	// （镜像 9628b39 部署约 40 秒后被回滚到 b262f744）。因此过滤改在 Go 侧做。
	const batch = 200
	var lastID uint
	for {
		var rows []row
		if err := db.Table("nodes").
			Select("id, node_id, config").
			Where("id > ? AND config IS NOT NULL", lastID).
			Order("id ASC").Limit(batch).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("scan nodes.config: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			lastID = r.ID
			// 与旧 WHERE 的 LIKE 过滤等价，只是搬到了 Go 侧（见上面的 jsonb 说明）。
			if !strings.Contains(r.Config, "dma_configs") {
				continue
			}
			res.Scanned++

			rewritten, changes, unknown, err := canonicalizeDMABindTo(r.Config)
			if err != nil {
				// 单行 JSON 坏了不阻断启动：其它节点照常修，坏行留给人看。
				logger.Warnf("[migrate] node %s: skip dma_configs canonicalization: %v", r.NodeID, err)
				res.Skipped++
				continue
			}
			for _, u := range unknown {
				res.Unknown = append(res.Unknown, r.NodeID+": "+u)
				logger.Warnf("[migrate] node %s: dma_configs.bind_to %q 不是已知控制器 id，保持原样（下次写入会被 API 拒绝）", r.NodeID, u)
			}

			if len(changes) == 0 {
				continue
			}
			if err := db.Table("nodes").Where("id = ?", r.ID).Update("config", rewritten).Error; err != nil {
				return nil, fmt.Errorf("update nodes.config for %s: %w", r.NodeID, err)
			}
			res.Updated++
			for _, c := range changes {
				res.Changes = append(res.Changes, r.NodeID+": "+c)
				logger.Infof("[migrate] node %s: dma_configs.bind_to %s", r.NodeID, c)
			}
		}
	}

	if res.Updated > 0 {
		logger.Infof("[migrate] dma_configs.bind_to 大小写规范化：扫描 %d 个节点，改写 %d 个节点", res.Scanned, res.Updated)
	}
	return res, nil
}

// canonicalizeDMABindTo 对一份 node.Config JSON 做 bind_to 大小写归一。
//
// 返回 rewritten="" 表示无需改写；unknown 是认识不了的值（原样保留）。
// 顶层用 map[string]json.RawMessage 而非 map[string]any：未涉及的配置键
// 原样回写，不因二次 json 编解码丢失数字精度或改变字段顺序 —— 对 jsonb 列来说，
// 数据库本来就会归一空白/键序，我们只需保证**值语义**不被这一层改动。
func canonicalizeDMABindTo(configJSON string) (string, []string, []string, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return "", nil, nil, fmt.Errorf("parse node config: %w", err)
	}
	raw, ok := cfg["dma_configs"]
	if !ok {
		return "", nil, nil, nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "", nil, nil, fmt.Errorf("parse dma_configs: %w", err)
	}

	var changes, unknown []string
	for i, entry := range entries {
		b, ok := entry["bind_to"]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(b, &value); err != nil {
			return "", nil, nil, fmt.Errorf("dma_configs[%d].bind_to: %w", i, err)
		}
		if value == "" {
			continue // 未绑定：合法的空值，不动
		}
		canonical, known := hwid.Normalize(value)
		if !known {
			unknown = append(unknown, value)
			continue
		}
		if canonical == value {
			continue
		}
		nb, err := json.Marshal(canonical)
		if err != nil {
			return "", nil, nil, fmt.Errorf("marshal canonical bind_to: %w", err)
		}
		entry["bind_to"] = nb
		changes = append(changes, fmt.Sprintf("dma_configs[%d] %q -> %q", i, value, canonical))
	}
	if len(changes) == 0 {
		return "", nil, unknown, nil
	}

	out, err := json.Marshal(entries)
	if err != nil {
		return "", nil, nil, fmt.Errorf("marshal dma_configs: %w", err)
	}
	cfg["dma_configs"] = out
	full, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, nil, fmt.Errorf("marshal node config: %w", err)
	}
	return string(full), changes, unknown, nil
}
