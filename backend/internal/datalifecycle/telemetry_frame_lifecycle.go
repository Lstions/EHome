package datalifecycle

// telemetry_frame_lifecycle.go — L2 帧表（telemetry_frame）加入**生命周期**所需的
// 共享定义与辅助函数（评审 B-3；实施契约 docs/设计/遥测存储-L1L2无条件启用-契约-2026-11-07.md §4）。
//
// ============ 为什么需要这个文件 ============
//
// L2 帧级编码从「影子旁路」变成**主路径**后，帧表就是历史数据表。
// 生命周期（保留期清理 / 彻底删除设备 / 合并历史 / 数据量与时间范围统计）
// 必须覆盖它，否则会出现两类事故：
//
//	· 设备删了、帧数据还在（GDPR/「彻底删除」语义被打破）；
//	· 帧数据永不清理（第二张无界增长表，正是 rollup 退役裁决里点名的问题）。
//
// ============ 为什么还保留 TimeseriesTable 这层抽象（2026-11-11 后只剩一张表）============
//
// 2026-11-11 之后清单只剩 telemetry_frame 一张（unified_data 2026-11-08 停写、
// device_data 2026-11-11 整表退役 —— docs/设计/原始数据退役-改造方案-2026-11-11.md）。
// 看起来可以退化成"直接写死表名"，但**不要**：
//
//  1. purge / retention / merge-history / data-summary 四处都通过本抽象分流
//     （HasID 决定「UPDATE device_id 改归属」还是「复合主键复制 + 幂等插入 + 删源」），
//     把它删掉意味着把这套形状判定复制到四个调用点 —— 而它们已经在真实事故里
//     证明过会漂移（device_data 与帧表的主键差异就是当初引入它的原因）；
//  2. 下一张时序表加进来时（例如把 logstream 也纳入保留期），只需在本清单加一条，
//     四处自动生效。引入它的原因没有随表数量减少而消失。
//
// 因此下面用一个 TimeseriesTable 描述「每张表是什么形状」，调用方（retention /
// purge / merge / api）按 spec 分支生成 SQL。**禁止**把表名直接拼进
// 「WHERE id IN (...)」这类「每行有自增 id」专用语句里 —— telemetry_frame 没有 id 列。
//
// 历史形状对照（两张退役表的列已不存在，留档用于读旧代码/旧文档时对照）：
//
//	                unified_data(退役)  device_data(退役)  telemetry_frame
//	  时间列         timestamp           timestamp           ts          ← 名字不同
//	  主键           (id,timestamp)      (id,timestamp)      (device_id,ts,seq) ← 无 id 列
//	  批量删的锚      id                  id                  (device_id,ts,seq)

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ehome/backend/internal/models"
)

// TelemetryFrameTable 是 L2 帧表的物理表名。
//
// 与 internal/telemetry.TableFrame 同值但**不互相引用**：datalifecycle 是
// 数据生命周期层，internal/telemetry 是编码层，两者不应互相 import（会形成
// 双向依赖风险：telemetry 的 PG 集成测试已 import datalifecycle，反向 import
// 会变成环）。这里刻意用字面量 + 本注释，值由 telemetry 包的同名常量与
// partitionParentDDL 的登记键三处对齐（TestTelemetryFrameTableNameAligned 钉住）。
const TelemetryFrameTable = "telemetry_frame"

// TimeseriesTable 描述一张时序表在「生命周期操作」里需要的形状信息。
type TimeseriesTable struct {
	// Name 物理表名。
	Name string
	// TSColumn 时间列名（帧表是 ts；已退役的 unified_data/device_data 是 timestamp）。
	TSColumn string
	// HasID 是否含单列自增主键 id。
	//
	// false（帧表）⇒ 批量删除必须用复合主键 (device_id, ts, seq) 做锚，
	// 不能用 `WHERE id IN (SELECT id ...)`（列不存在，SQL 直接报错）。
	HasID bool
}

// TimeseriesTables 返回**所有**参与生命周期的时序表（顺序 = 清理顺序）。
//
// 返回副本：调用方（含测试）不得修改这份清单 —— 漏掉任何一张表都意味着
// 那条生命周期路径对它静默失效，这是本文件要防的事故本身。
//
// ============ 2026-11-11：清单从三张收敛到两张 ============
//
// 移除的两张（原因不同，不要混为一谈）：
//
//	· device_data —— **整表退役**（产品裁决「原始数据不入库」，见
//	  docs/设计/原始数据退役-改造方案-2026-11-11.md §2.1）。表的模型、写点、读点、
//	  DDL 全部删除，DROP 之后不存在，所以生命周期路径也就不该再有它。
//	· unified_data —— 已于 2026-11-08 停写，DROP 后同样不存在。
//
// 为什么**不能**只靠 existingTimeseriesTables 的表存在性过滤把它挡住：
// 过滤解决的是"表不存在时不报错"，而本清单还要回答另一件事 ——
// 「这张表是否**应该**参与保留期清理 / 设备彻底删除」。设备已退役的表如果留在
// 清单里，任何一次「表被重建」（例如有人手滑跑了旧版本的迁移）都会让一条本不该
// 存在的数据通路复活。清单是**意图**，存在性过滤是**防御**，两者都要。
//
// 现存两张表的形状差异（保留这段说明是因为它是本文件存在的理由）：
//
//	              unified_data(退役)  device_data(退役)  telemetry_frame
//	时间列         timestamp           timestamp           ts          ← 名字不同
//	主键           (id,timestamp)      (id,timestamp)      (device_id,ts,seq) ← 无 id 列
//	批量删的锚      id                  id                  (device_id,ts,seq)
func TimeseriesTables() []TimeseriesTable {
	return []TimeseriesTable{
		{Name: TelemetryFrameTable, TSColumn: "ts", HasID: false},
	}
}

// ExistingTimeseriesTables 过滤出**当前库里真存在**的时序表（导出给 api 层用）。
//
// api 的 data-summary / merge-history 也必须走它：精简测试库与「迁移尚未跑过」
// 的库没有 telemetry_frame，不过滤会让「读最后采集时间」直接报 no such table，
// 进而把整个 merge-history 变成 500 或带无谓 warning。
func ExistingTimeseriesTables(db *gorm.DB) []TimeseriesTable {
	return existingTimeseriesTables(db)
}

// existingTimeseriesTables 过滤出**当前库里真存在**的时序表。
//
// 为什么必须过滤而不是直接跑 SQL（这曾是 purge/retention 在本轮修复里的第一个
// 翻车点）：精简测试库（testutil 的 SQLite 清单）与「迁移尚未跑过」的早期部署
// 都没有 telemetry_frame。不过滤的后果是 purge 的整个批次报
// "no such table: telemetry_frame"，于是**设备根本删不掉** —— 一个新增的可选表
// 把既有功能打成故障。
//
// 与 cleanLedgerRows 的既有约定一致（sensor_field_obs / data_sources：「表不存在
// 时跳过」）。生产上 main.go 的迁移是 Fatal 的，因此这里的跳过路径**只可能**
// 出现在测试库或人为改库的场景。
func existingTimeseriesTables(db *gorm.DB) []TimeseriesTable {
	all := TimeseriesTables()
	out := make([]TimeseriesTable, 0, len(all))
	for _, t := range all {
		if db.Migrator().HasTable(t.Name) {
			out = append(out, t)
			continue
		}
		// 有界枚举的日志（每次清理一轮最多 3 条），用来解释「为什么这台设备
		// 的生命周期里少了一张表」。
		slog.Debug("datalifecycle: time-series table missing, skipped in lifecycle operation",
			"table", t.Name)
	}
	return out
}

// partitionLifecycleTables 返回参与「整月分区 DROP」的表名清单。
//
// 与 TimeseriesTables 的区别：这里只关心「是否月分区」—— 清单里的表都是
// （telemetry_frame 由 partition_mgr 按月建分区，见 partitionParentDDL）。
// 分区 DROP 与列名无关（DropPartitionsBeforeFor 只按分区名的 YYYYMM 后缀判定），
// 所以这个清单可以是纯表名。
func partitionLifecycleTables() []string {
	tables := TimeseriesTables()
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		out = append(out, t.Name)
	}
	return out
}

// deleteExpiredRowsFromTable 删除 table 中时间早于 cutoff 的行（单批）。
//
// 返回本次删除行数。两种 SQL 形状：
//
//	有 id 列  : DELETE FROM t WHERE id IN (SELECT id FROM t WHERE <ts> < ? LIMIT ?)
//	无 id 列  : DELETE FROM t WHERE (device_id, <ts>, seq) IN
//	                        (SELECT device_id, <ts>, seq FROM t WHERE <ts> < ? LIMIT ?)
//
// 第二种用**行值 IN 子查询**（row-value IN）：PostgreSQL 与 SQLite(>=3.15) 都支持，
// 语义与 id 版一致（可重复执行、每批独立、LIMIT 固定批大小）。
func deleteExpiredRowsFromTable(tx *gorm.DB, table TimeseriesTable, cutoff time.Time, batchSize int) (int64, error) {
	var sql string
	if table.HasID {
		sql = fmt.Sprintf("DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE %s < ? LIMIT ?)",
			table.Name, table.Name, table.TSColumn)
	} else {
		sql = fmt.Sprintf(
			"DELETE FROM %s WHERE (device_id, %s, seq) IN (SELECT device_id, %s, seq FROM %s WHERE %s < ? LIMIT ?)",
			table.Name, table.TSColumn, table.TSColumn, table.Name, table.TSColumn)
	}
	res := tx.Exec(sql, cutoff, batchSize)
	return res.RowsAffected, res.Error
}

// deleteDeviceRowsFromTable 删除 table 中某设备的全部行（单批）。
func deleteDeviceRowsFromTable(tx *gorm.DB, table TimeseriesTable, deviceID uint, batchSize int) (int64, error) {
	var sql string
	if table.HasID {
		sql = fmt.Sprintf("DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE device_id = ? LIMIT ?)",
			table.Name, table.Name)
	} else {
		sql = fmt.Sprintf(
			"DELETE FROM %s WHERE (device_id, %s, seq) IN (SELECT device_id, %s, seq FROM %s WHERE device_id = ? LIMIT ?)",
			table.Name, table.TSColumn, table.TSColumn, table.Name)
	}
	res := tx.Exec(sql, deviceID, batchSize)
	return res.RowsAffected, res.Error
}

// MigrateTelemetryFrameToPartitioned 建/迁移 L2 帧表与 slot 字典表。
//
// # 调用时机与失败语义（契约 §3.4）
//
// L2 帧表是**主路径**（不再是影子），因此「表没建但主写入成功」是不可接受的
// 半残状态 ⇒ 调用方（main.go）必须 Fatal，而不是 Warn 后继续。
//
// # 两件事
//
//  1. telemetry_slot：普通表（slot 字典，几十行），AutoMigrate 即可。
//     它此前**没有任何建表路径** —— models/telemetry_frame.go 明确要求
//     TelemetryFrame 不进生产 AutoMigrate（分区母表必须由 partition_mgr 建），
//     而 TelemetrySlot 也没被登记到任何清单里，导致「代码上线但表不存在」。
//     这里补上，且与帧表迁移绑在同一次调用里：两者同生共死。
//  2. telemetry_frame：走与（已退役的）unified_data/device_data 相同的泛化迁移入口
//     （partitionParentDDL / partitionParentIndexes / partitionCopyColumns 已在
//     commit 63fe95a8 登记），失败即返回 error。
//
// SQLite（单测/无 PG）下 MigrateTableToPartitioned 是 no-op；AutoMigrate 仍会
// 建出普通表，让读路径可测。
func MigrateTelemetryFrameToPartitioned(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("telemetry_frame migration: nil db")
	}
	// slot 字典表：普通表，先建（帧行的 slot_id 逻辑上指向它）。
	if !db.Migrator().HasTable(&models.TelemetrySlot{}) {
		if err := db.AutoMigrate(&models.TelemetrySlot{}); err != nil {
			return fmt.Errorf("telemetry_slot automigrate: %w", err)
		}
	}
	if err := MigrateTableToPartitioned(db, TelemetryFrameTable, TelemetryFrameTable+"_legacy"); err != nil {
		return fmt.Errorf("telemetry_frame partition migration: %w", err)
	}
	return nil
}

// DropTelemetryFramePartitionsBefore 删除整月早于 cutoff 的帧表分区（保留期的 O(1) 路径）。
//
// 帧表是月分区表，因此保留期与（已退役的）unified_data/device_data 同款：整分区 DROP
// （元数据操作，不逐行 DELETE）。这既是性能要求，也是「帧数据真的会按保留期
// 消失」的唯一可靠实现 —— 逐行 DELETE 在 1 Hz × 每设备的数据量下永远跑不完。
func DropTelemetryFramePartitionsBefore(db *gorm.DB, cutoff time.Time) ([]string, error) {
	if !isPostgres(db) {
		return nil, nil
	}
	if !IsTablePartitioned(db, TelemetryFrameTable) {
		return nil, nil
	}
	return NewPartitionManager(db).DropPartitionsBeforeFor(TelemetryFrameTable, cutoff)
}

// MigrateTelemetryFramesToDevice 把源设备的帧迁到目标设备（合并历史用）。
//
// # 为什么不能像（已退役的）unified_data 那样 UPDATE device_id
//
// 帧表主键是 (device_id, ts, seq)。直接把源行改成目标 device_id，一旦目标在
// 同一 (ts, seq) 上已有行就撞主键 ⇒ 整批失败。因此走「复制 + 幂等插入 + 精确删源」：
//
//  1. 读一批源行（内存有界，batchSize 行）；
//  2. 以目标 device_id 重新插入，ON CONFLICT DO NOTHING（撞主键的帧被丢弃并计数）；
//  3. 按**精确 (device_id, ts, seq) 三元组**删除本批源行（不是按 ts 列表删 ——
//     那会误删目标设备在同一时刻的帧）。
//
// 重试幂等：第 2 步 DO NOTHING、第 3 步删已删过的行是零影响。中途失败重跑即收敛。
//
// # 已知有损窗口（必须知情）
//
// 帧表的增量链按 (device, slot) 维护：源设备的最后一帧通常是**增量帧**（相对源设备
// 上一帧）。搬到目标设备后，解码顺序里它前面是目标设备的帧 ⇒ 该 slot 要等到下一个
// **关键帧**（默认 60s 内）才能重新对齐，这 60s 内的解码值会偏移。
//
// 这是帧级编码的固有限制（跨设备搬增量链），不是本函数的缺陷：
//
//	· 窗口有界：同一 slot 的下一个关键帧最迟 KeyFrameInterval（默认 60s）后到达，
//	  之后增量链重新对齐，不会无限传播；
//	· 合并只在「同一台物理设备换设备行」的场景发生，源设备已经软删（不再有活跃
//	  写入），实际解码顺序里源帧与目标帧之间隔着一次关键帧的概率很高。
//
// ⚠ 诚实边界：当前**没有**"让在线编码器对某设备重发关键帧"的钩子 —— 编码器
// 实例在 DualWriter 内部，HTTP handler（merge-history 的调用方）拿不到它。
// 若将来要求合并后 0 漂移，需要在 DualWriter 上加 InvalidateDevice 并把它注入
// handler（跨包接线），那是独立改动，不在本次契约范围内。
//
// 返回 moved（成功迁移行数）、skipped（撞主键被丢弃行数）。
func MigrateTelemetryFramesToDevice(ctx context.Context, db *gorm.DB, targetID uint, sources []uint, batchSize int) (moved, skipped int64, err error) {
	if db == nil {
		return 0, 0, fmt.Errorf("migrate telemetry frames: nil db")
	}
	if len(sources) == 0 {
		return 0, 0, nil
	}
	if !db.Migrator().HasTable(TelemetryFrameTable) {
		// 表不存在（未跑迁移的旧库 / SQLite 精简测试库）：合并历史时跳过帧表，
		// 不把「可选的新表」缺失变成合并失败。
		return 0, 0, nil
	}
	if batchSize <= 0 {
		batchSize = 5000
	}
	// 单批上限压到 5000：第 3 步要按三元组展开参数（3 × batchSize），
	// PG 的参数上限是 65535，保守留足余量。
	if batchSize > 5000 {
		batchSize = 5000
	}

	for {
		if cerr := ctx.Err(); cerr != nil {
			return moved, skipped, cerr
		}
		var rows []models.TelemetryFrame
		if qerr := db.WithContext(ctx).
			Where("device_id IN ?", sources).
			Order("device_id, ts, seq").
			Limit(batchSize).
			Find(&rows).Error; qerr != nil {
			return moved, skipped, fmt.Errorf("read source telemetry frames: %w", qerr)
		}
		if len(rows) == 0 {
			return moved, skipped, nil
		}

		// 复制成目标设备的新行（保留 ts/seq/kind/slot/payload/raw）。
		copies := make([]models.TelemetryFrame, len(rows))
		for i := range rows {
			copies[i] = models.TelemetryFrame{
				DeviceID:  targetID,
				TS:        rows[i].TS,
				Seq:       rows[i].Seq,
				FrameKind: rows[i].FrameKind,
				SlotID:    rows[i].SlotID,
				Payload:   rows[i].Payload,
				Raw:       rows[i].Raw,
			}
		}
		insErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Session(&gorm.Session{}).
				Clauses(clause.OnConflict{DoNothing: true}).
				Create(&copies)
			if res.Error != nil {
				return fmt.Errorf("insert target telemetry frames: %w", res.Error)
			}
			moved += res.RowsAffected
			skipped += int64(len(copies)) - res.RowsAffected

			// 精确删源：按 (device_id, ts, seq) 三元组展开 IN 列表。
			placeholders := make([]string, 0, len(rows))
			args := make([]interface{}, 0, len(rows)*3)
			for i := range rows {
				placeholders = append(placeholders, "(?, ?, ?)")
				args = append(args, rows[i].DeviceID, rows[i].TS, rows[i].Seq)
			}
			delSQL := fmt.Sprintf("DELETE FROM %s WHERE (device_id, ts, seq) IN (%s)",
				TelemetryFrameTable, strings.Join(placeholders, ", "))
			if derr := tx.WithContext(ctx).Exec(delSQL, args...).Error; derr != nil {
				return fmt.Errorf("delete migrated source telemetry frames: %w", derr)
			}
			return nil
		})
		if insErr != nil {
			return moved, skipped, insErr
		}
	}
}
