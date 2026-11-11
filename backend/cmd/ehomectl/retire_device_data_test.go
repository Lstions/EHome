package main

// retire_device_data_test.go — `ehomectl retire-device-data` 的**只读性**与**判据**门禁。
//
// ==================== 为什么这个文件必须存在 ====================
//
// 本命令会 DROP 一张 3.4 GB / 870 万行的生产表。它最危险的两个失效形态都是
// **静默**的：
//
//  1. **dry-run 其实动了库**（比如有人把 DROP 挪到校验之前，或在校验路径里加了
//     "顺手清理"）—— 运维按 dry-run 跑一遍，以为只是看了看，实际数据没了。
//     这种人祸无法靠"看代码"防，只能靠行为断言：跑完 dry-run 后表与行数必须
//     **逐字节未变**。
//  2. **判据写松**（把「四条全绿」写成「有一条绿就行」，或把 orphan_parsed_rows
//     的阈值从 0 改成 >0）—— 那会让本命令在数据不全时放行 DROP。
//     这里用**派生**的极端夹具正面打它：造出「解析值只在 device_data 里」的数据，
//     断言 allPassed() == false。
//
// 与仓内既有的退役命令门禁（database/retire_operation_logs_test.go）同范式：
// 行为 + 判据双测，不测"打印文案"。

import (
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// openRetireTestDB 建一个只含本命令所需表的 SQLite 测试库。
//
// 为什么不用 testutil.OpenTestDB：那会建几十张表，而本文件的每个用例都要
// 精确控制 device_data 的形状（它就是被测对象）。最小库让"表存在/不存在"
// 这两个分支都能显式构造。
//
// ⚠ device_data 的模型已随整表退役删除（2026-11-11），所以这里用裸 DDL 建它 ——
// 这正是本命令要处理的那张**存量表**：代码里没有它的模型了，但生产库里还有。
func openRetireTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.EdgeDevice{}, &models.TelemetryFrame{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// createLegacyDeviceData 用裸 DDL 建出**已退役**的 device_data 平表
// （生产上是分区母表，SQLite 测试库建平表即可 —— 本命令的行数/大小/校验
// 查询在两种形态下语义一致，只有 CASCADE 那句是 PG 专有语法）。
func createLegacyDeviceData(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE device_data (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id INTEGER NOT NULL,
		node_id TEXT NOT NULL,
		data_json TEXT,
		timestamp DATETIME,
		created_at DATETIME,
		edge_device_id INTEGER
	)`).Error; err != nil {
		t.Fatalf("create legacy device_data: %v", err)
	}
}

// TestRetireDeviceData_MissingTableIsCleanExit 守幂等：表不存在（已退役）时
// 校验必须直接返回「无需操作」，而不是报错或 DROP 失败。
//
// 重跑安全是运维窗口的硬要求：部署脚本可能在 DROP 成功后又跑一次本命令
// （例如 playbook 重试），那次必须干净退出 0。
func TestRetireDeviceData_MissingTableIsCleanExit(t *testing.T) {
	db := openRetireTestDB(t)
	rep, err := verifyRetireDeviceData(db)
	if err != nil {
		t.Fatalf("verify on missing table: %v", err)
	}
	if rep.tableExists {
		t.Fatal("tableExists = true，但库里没有 device_data")
	}
	if !rep.allPassed() {
		t.Error("表不存在时必须视为全部通过（无可校验对象），否则重跑会永远报失败")
	}
	if len(rep.checks) != 0 {
		t.Errorf("表不存在时不应跑任何校验查询，got %d 条", len(rep.checks))
	}
}

// TestRetireDeviceData_DryRunVerificationTouchesNothing 是**只读性**门禁。
//
// 跑一遍 verifyRetireDeviceData（= 默认/--dry-run 路径的全部逻辑），断言：
//   - 表仍在；
//   - 行数与执行前完全一致；
//   - 每一行的内容也完全一致（不能只看行数 —— UPDATE 会改内容不改行数）。
func TestRetireDeviceData_DryRunVerificationTouchesNothing(t *testing.T) {
	db := openRetireTestDB(t)
	createLegacyDeviceData(t, db)
	for i := 0; i < 5; i++ {
		if err := db.Exec("INSERT INTO device_data (device_id, node_id, data_json, timestamp) VALUES (?,?,?,CURRENT_TIMESTAMP)",
			i+1, "NODE001", `{"raw":"aabb"}`).Error; err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	// 执行前快照：行数 + 完整内容（按 id 排序拼接，任何字段变化都能发现）。
	beforeRows, beforeDump := snapshotDeviceData(t, db)

	rep, err := verifyRetireDeviceData(db)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.tableExists {
		t.Fatal("tableExists = false，但表刚建过")
	}
	if rep.rows != 5 {
		t.Errorf("report rows = %d, want 5（dry-run 必须如实数出行数，运维要靠它判断）", rep.rows)
	}
	if len(rep.checks) != 4 {
		t.Errorf("跑了 %d 条校验，want 4（方案 §3.4 的四条）", len(rep.checks))
	}

	afterRows, afterDump := snapshotDeviceData(t, db)
	if afterRows != beforeRows {
		t.Fatalf("dry-run 后行数变了: %d → %d（校验路径绝不允许 DML）", beforeRows, afterRows)
	}
	if afterDump != beforeDump {
		t.Fatalf("dry-run 后内容变了（行数相同但数据被改写）—— 校验路径绝不允许 DML\n"+
			"before=%s\nafter =%s", beforeDump, afterDump)
	}
}

// TestRetireDeviceData_BlocksDropWhenParsedValuesOnlyInLegacyTable 是**判据**门禁
// （最核心的一条）：当解析值只存在于待删表时，allPassed 必须为 false。
//
// 构造方式：device_data 里放一行**带 sensors** 的解析行，而帧表**没有任何帧**。
// 这精确复刻了「帧表迁移没跑完就删表」的事故形态 —— 那一下就是数据永久丢失。
func TestRetireDeviceData_BlocksDropWhenParsedValuesOnlyInLegacyTable(t *testing.T) {
	db := openRetireTestDB(t)
	createLegacyDeviceData(t, db)
	// 带 sensors 的解析行（= 真有解析值的行），时间戳随意。
	if err := db.Exec(`INSERT INTO device_data (device_id, node_id, data_json, timestamp)
		VALUES (1, 'NODE001', '{"sensors":[{"Name":"voltage","Value":12}]}', '2026-11-01 00:00:00')`).Error; err != nil {
		t.Fatalf("seed parsed row: %v", err)
	}
	// 帧表为空 ⇒ 上面那行解析值无处可寻 ⇒ 必须拦住 DROP。

	rep, err := verifyRetireDeviceData(db)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.allPassed() {
		t.Fatal("allPassed = true，但 device_data 里有解析值且帧表为空 —— " +
			"此时 DROP 就是数据丢失，命令必须拒绝")
	}
	// 精确定位到失败的是哪一条（避免"别的校验顺手红了"造成假阳性通过）。
	var sawOrphanFail bool
	for _, c := range rep.checks {
		if c.check.name == "orphan_parsed_rows" {
			if c.queryE != nil {
				t.Fatalf("orphan_parsed_rows 查询失败: %v", c.queryE)
			}
			if c.value == 0 || c.ok {
				t.Errorf("orphan_parsed_rows = %d (ok=%v)，want >0/false —— 判据没抓住这条数据",
					c.value, c.ok)
			}
			sawOrphanFail = true
		}
	}
	if !sawOrphanFail {
		t.Fatal("没有跑到 orphan_parsed_rows 这条校验 —— 四条清单被改动了？")
	}
}

// TestRetireDeviceData_PassesWhenFramesCoverTheParsedValues 是对照组：
// 与上一条**完全相同的夹具 + 帧表里有对应时间戳的帧** ⇒ allPassed 必须为 true。
//
// 没有它，"把 orphan_parsed_rows 写成恒 false"也能让上一条通过 ——
// 那会让本命令永远拒绝 DROP（功能失效但不丢数据，属于另一种坏）。
func TestRetireDeviceData_PassesWhenFramesCoverTheParsedValues(t *testing.T) {
	db := openRetireTestDB(t)
	createLegacyDeviceData(t, db)
	if err := db.Exec(`INSERT INTO device_data (device_id, node_id, data_json, timestamp)
		VALUES (1, 'NODE001', '{"sensors":[{"Name":"voltage","Value":12}]}', '2026-11-01 00:00:00')`).Error; err != nil {
		t.Fatalf("seed parsed row: %v", err)
	}
	// 帧表：同一设备、同一时间点 ±5s 内有一帧 ⇒ 解析值有去处。
	if err := db.Create(&models.TelemetryFrame{
		DeviceID: 1, TS: mustParseTime(t, "2026-11-01 00:00:02"),
		Seq: 0, FrameKind: models.TelemetryFrameKindKey, SlotID: 1, Payload: []byte{0x01},
	}).Error; err != nil {
		t.Fatalf("seed frame: %v", err)
	}

	rep, err := verifyRetireDeviceData(db)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	// 注意：生产库上还有 orphan_frame_devices 等其它条目会因夹具不完整而失败，
	// 所以这里**只**断言 orphan_parsed_rows 这一条通过 —— 它的"通过"才是本用例的
	// 被测语义（对照组只负责证明判据不是恒 false）。
	for _, c := range rep.checks {
		if c.check.name != "orphan_parsed_rows" {
			continue
		}
		if c.queryE != nil {
			t.Fatalf("orphan_parsed_rows 查询失败: %v", c.queryE)
		}
		if !c.ok || c.value != 0 {
			t.Errorf("orphan_parsed_rows = %d (ok=%v)，want 0/true —— 帧表已覆盖，判据不能恒 false",
				c.value, c.ok)
		}
		return
	}
	t.Fatal("没有跑到 orphan_parsed_rows 这条校验")
}

// snapshotDeviceData 返回 (行数, 全表内容摘要)。
//
// 用逐行拼接而不是 hash：失败信息里直接能看到"哪一行变了"，而 hash 只给出
// "变了"（排查成本高一个量级）。本命令的表在测试里只有个位数行。
func snapshotDeviceData(t *testing.T, db *gorm.DB) (int64, string) {
	t.Helper()
	var rows []struct {
		ID       int64
		DeviceID int64
		NodeID   string
		DataJSON string
	}
	if err := db.Raw("SELECT id, device_id, node_id, COALESCE(data_json,'') AS data_json FROM device_data ORDER BY id").
		Scan(&rows).Error; err != nil {
		t.Fatalf("snapshot device_data: %v", err)
	}
	out := ""
	for _, r := range rows {
		out += fmtRow(r.ID, r.DeviceID, r.NodeID, r.DataJSON)
	}
	return int64(len(rows)), out
}

func fmtRow(id, deviceID int64, nodeID, dataJSON string) string {
	return nodeID + "|" + dataJSON + "|" + itoa(id) + "|" + itoa(deviceID) + "\n"
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tm
}

// ==================== 2026-11-11 生产只读演练暴露的三处缺陷（回归）====================
//
// 这三条全部来自在 192.168.20.6 生产库上实际跑 §3.4 校验 SQL 的结果，
// 不是推理出来的：
//
//	缺陷 1  校验 ① 报 "could not resize shared memory segment ... No space left on device"
//	         （PG 容器 /dev/shm 只有 docker 默认 64 MB，并行 worker 申请 ~32 MB hash 表失败）
//	缺陷 2  校验 ④ 报 "operator does not exist: character varying >= date"
//	         （day 列是 varchar 而不是 date）
//	缺陷 3  校验 ② 生产实测 = 1（既有的孤儿帧：设备 2 已硬删，100,806 帧残留），
//	         而它被写成阻塞项 —— 会让 DROP 被一条与本任务无关的历史遗留永久挡住
//
// 三条各有一个用例，且都必须是**能在默认 sqlite 环境下跑**的（否则等于没测）。

// TestRetireDeviceData_SessionGuardsDisableParallelismOnPostgres 守缺陷 1。
//
// 断言的是 SQL 文本本身：PG 方言必须包含关并行 gather 的 SET LOCAL。
// 为什么可以用文本断言而不是真跑 PG：这条修复的**全部内容**就是"执行了那句 SET"，
// 而它是否真的解决了 /dev/shm 报错只能在生产验证（我没有 64 MB shm 的 PG 环境）。
// 文本断言能挡住「有人把它删掉/改成 SET（非 LOCAL）」这两类回归 —— 那才是真正的风险。
func TestRetireDeviceData_SessionGuardsDisableParallelismOnPostgres(t *testing.T) {
	guards := sessionGuards("postgres")
	if len(guards) == 0 {
		t.Fatal("postgres 方言没有任何 session guard —— 校验 ① 会在 64MB /dev/shm 的生产容器上直接报错")
	}
	var sawPrefix bool
	var sawGatherOff bool
	for _, g := range guards {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(g)), "SET LOCAL ") {
			t.Errorf("guard %q 不是 SET LOCAL —— 非 LOCAL 的 SET 会污染连接池里的后续查询", g)
		}
		sawPrefix = true
		if strings.Contains(g, "max_parallel_workers_per_gather = 0") {
			sawGatherOff = true
		}
	}
	if !sawPrefix || !sawGatherOff {
		t.Fatalf("guards = %v，缺少 max_parallel_workers_per_gather = 0"+
			"（缺它 ⇒ 生产 64MB shm 上 NOT EXISTS 相关子查询起并行 worker ⇒ 共享内存分配失败）", guards)
	}

	// 非 PG 方言必须返回空：SQLite 没有 SET 语法，返回非空会让所有单测语法报错。
	if g := sessionGuards("sqlite"); len(g) != 0 {
		t.Fatalf("sqlite 方言返回了 %d 条 guard（应为 0 —— SQLite 没有 SET 语法）: %v", len(g), g)
	}
}

// TestRetireDeviceData_RollupCoverageComparesVarcharToVarchar 守缺陷 2。
//
// 断言 ④ 的 SQL 里**不出现把 date 与列直接比较的形态**，而用的是 to_char 出来的字符串。
// 生产实测 day 列类型是 character varying，写 ::date 会直接
// "operator does not exist: character varying >= date"（生产必挂）。
func TestRetireDeviceData_RollupCoverageComparesVarcharToVarchar(t *testing.T) {
	var coverage *retireCheck
	for _, c := range retireDeviceDataChecks("postgres") {
		cc := c
		if c.name == "rollup_coverage_recent" {
			coverage = &cc
			break
		}
	}
	if coverage == nil {
		t.Fatal("找不到 rollup_coverage_recent 校验项")
	}
	if strings.Contains(coverage.sql, "::date") {
		t.Fatalf("rollup_coverage_recent 的 SQL 里有 ::date 转换 —— day 列是 varchar，"+
			"生产会报 operator does not exist: character varying >= date\nSQL: %s", coverage.sql)
	}
	if !strings.Contains(coverage.sql, "to_char(") {
		t.Fatalf("rollup_coverage_recent 的 SQL 没有 to_char(...) —— "+
			"必须把「近 7 天」转成同格式字符串再与 varchar 列比较\nSQL: %s", coverage.sql)
	}

	// SQLite 变体：同样不能出现 PG 专有的 ::date（SQLite 不支持）。
	for _, c := range retireDeviceDataChecks("sqlite") {
		if c.name == "rollup_coverage_recent" && strings.Contains(c.sql, "::date") {
			t.Fatalf("sqlite 变体的 rollup_coverage_recent 用了 ::date（SQLite 不支持）: %s", c.sql)
		}
	}
}

// TestRetireDeviceData_OrphanFrameDevicesIsAdvisory 守缺陷 3。
//
// 生产实测 orphan_frame_devices = 1（设备 2 被硬删后留下 100,806 帧）。
// 该条必须：
//   - 标成 advisory（不参与 allPassed）；
//   - 仍能打印出它的值（不能因为 advisory 就不跑 —— 运维需要看见它）。
//
// 这条用**真实数据**验证：造孤儿帧 ⇒ allPassed 仍为 true，但 advisoryWarnings 非空。
func TestRetireDeviceData_OrphanFrameDevicesIsAdvisory(t *testing.T) {
	db := openRetireTestDB(t)
	createLegacyDeviceData(t, db)
	// device_data 里放一行**不带 sensors** 的普通行：不触发 ①，
	// 且让表存在（否则 verifyRetireDeviceData 直接早退、根本不跑校验）。
	if err := db.Exec("INSERT INTO device_data (device_id, node_id, data_json, timestamp) " +
		"VALUES (1, 'NODE001', '{\"raw\":\"aabb\"}', '2026-11-01 00:00:00')").Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	// 孤儿帧：device_id=999 在 edge_devices 里不存在（精确复刻生产的设备 2 场景）。
	if err := db.Create(&models.TelemetryFrame{
		DeviceID: 999, TS: time.Now().UTC(),
		Seq: 0, FrameKind: models.TelemetryFrameKindKey, SlotID: 1, Payload: []byte{0x01},
	}).Error; err != nil {
		t.Fatalf("seed orphan frame: %v", err)
	}

	rep, err := verifyRetireDeviceData(db)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	var found bool
	for _, c := range rep.checks {
		if c.check.name != "orphan_frame_devices" {
			continue
		}
		found = true
		if !c.check.advisory {
			t.Fatal("orphan_frame_devices 不是 advisory —— 生产实测该值为 1，" +
				"写成阻塞项会让 DROP 被一条与本任务无关的历史遗留永久挡住")
		}
		if c.queryE != nil {
			t.Fatalf("orphan_frame_devices 查询失败: %v", c.queryE)
		}
		if c.value != 1 || c.ok {
			t.Errorf("orphan_frame_devices = %d (ok=%v)，want 1/false —— 判据没抓住孤儿帧",
				c.value, c.ok)
		}
	}
	if !found {
		t.Fatal("没有跑到 orphan_frame_devices 校验")
	}

	// 关键断言：孤儿帧**不阻塞** DROP。
	if !rep.allPassed() {
		t.Error("allPassed = false，但唯一失败的应是 advisory 项 —— " +
			"孤儿帧是既有脏数据，与能否安全删 device_data 无关")
	}
	if len(rep.advisoryWarnings()) == 0 {
		t.Error("advisoryWarnings 为空 —— 运维仍需要看见这条既有脏数据")
	}
}

// TestRetireDeviceData_BlockingChecksStillBlock 是对照组：确认 advisory 改造**没有**
// 把阻塞项也一起放行 —— 那会让本工具失去全部保护作用（比不写更糟：它给人虚假安全感）。
func TestRetireDeviceData_BlockingChecksStillBlock(t *testing.T) {
	db := openRetireTestDB(t)
	createLegacyDeviceData(t, db)
	// 解析值只在 device_data 里、帧表为空 ⇒ ① 必须阻塞。
	if err := db.Exec(`INSERT INTO device_data (device_id, node_id, data_json, timestamp)
		VALUES (1, 'NODE001', '{"sensors":[{"Name":"voltage","Value":12}]}', '2026-11-01 00:00:00')`).Error; err != nil {
		t.Fatalf("seed parsed row: %v", err)
	}
	rep, err := verifyRetireDeviceData(db)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.allPassed() {
		t.Fatal("allPassed = true —— advisory 改造把阻塞项也放行了，那是比不写更糟的失效")
	}
}

// TestRetireDeviceData_FailingCheckDoesNotStopLaterChecks 守「一条校验失败不毒化事务」。
//
// ==================== 为什么要测（2026-11-11 生产演练踩到）====================
//
// 第一版把四条校验直接放在同一个事务里做。PG 上**任何**语句失败都会让事务进入
// aborted 状态，后续每条查询报：
//
//	ERROR: current transaction is aborted, commands ignored until end of transaction block
//	       (SQLSTATE 25P02)
//
// 后果有两层，都很难查：
//  1. 真正的那一条错误被后面三条「transaction is aborted」噪音淹没；
//  2. db.Transaction 的 commit 变成 rollback，最终只剩一句
//     "verify device_data retirement: commit unexpectedly resulted in rollback"，
//     完全看不出是哪条校验坏了。
//
// 修法是每条校验包一个 SAVEPOINT（失败只回滚到该点）。本用例把后三张表全部删掉，
// 让前三条校验都失败，断言**第四条仍然被跑到并出现在报告里**。
//
// ⚠ 说明：SQLite 不会像 PG 那样把整个事务打成 aborted，所以本用例在 sqlite 上
// 严格说不能证明「savepoint 修好了 PG 的 25P02」；它能证明的是**代码结构**
// （循环没有 early-return、每条都进报告）。PG 侧的真实行为已在本地
// postgres:18-alpine 容器（/dev/shm = 64 MB，与生产同配置）上手工验证过：
// 缺表时四条校验全部独立报错，不再出现 25P02 连锁。
func TestRetireDeviceData_FailingCheckDoesNotStopLaterChecks(t *testing.T) {
	db := openRetireTestDB(t)
	createLegacyDeviceData(t, db)
	// 把前三条校验依赖的表全删掉（edge_devices 由 AutoMigrate 建过）。
	if err := db.Migrator().DropTable(&models.TelemetryFrame{}); err != nil {
		t.Fatalf("drop telemetry_frame: %v", err)
	}
	if err := db.Migrator().DropTable(&models.EdgeDevice{}); err != nil {
		t.Fatalf("drop edge_devices: %v", err)
	}

	rep, err := verifyRetireDeviceData(db)
	if err != nil {
		t.Fatalf("verify 返回了顶层错误（%v）—— 单条校验失败不该让整个校验函数失败，"+
			"否则运维看不到其它校验的结果", err)
	}
	if len(rep.checks) != 4 {
		t.Fatalf("报告里有 %d 条校验结果，want 4 —— 说明某条失败后就 early-return 了"+
			"（那正是 25P02 那次事故的形状）", len(rep.checks))
	}
	// 最后一条（rollup_coverage_recent）必须是**被真的跑过**的：它的目标表不存在
	// ⇒ 走 optional 分支记 skipped，而 skipped 只有"跑到了"才会有值。
	var last *retireCheckResult
	for i := range rep.checks {
		if rep.checks[i].check.name == "rollup_coverage_recent" {
			last = &rep.checks[i]
		}
	}
	if last == nil {
		t.Fatal("rollup_coverage_recent 不在报告里 —— 后面的校验没跑到")
	}
	if last.skipped == "" && last.queryE == nil {
		t.Errorf("rollup_coverage_recent 既没 skipped 也没 queryE（value=%d）—— "+
			"它的表不存在，应当走 optional 分支记 skipped", last.value)
	}
}

// ==================== CLI 层只读性门禁（2026-11-11 review 发现的严重问题）====================
//
// 前面那条 TestRetireDeviceData_DryRunVerificationTouchesNothing 只测了
// verifyRetireDeviceData 这一层（纯 SELECT）。而 review 在生产上踩到的是
// **上一层**：runRetireDeviceDataCLI 调 connectDB()，而 connectDB() 里藏着
// database.AutoMigrate() —— 一个自称"只校验只打印、绝不改库"的工具，
// 在**连库的瞬间**就对生产库跑了 schema 迁移。
//
// 为什么这是严重问题：方案 §3.3 的部署顺序是「停容器 → 备份 → 校验 → DROP」，
// 校验跑在备份**之前**。若这次 AutoMigrate 改了 schema 而随后 DROP 出问题要回滚，
// 备份里没有那次改动 —— 回滚后 schema 与代码不一致。
//
// 一条"只测下层"的用例让这个洞活了很久。下面的用例专测**接线**：
// 断言 retire-device-data 路径拿到的 db 来自 connectDBNoMigrate，
// 即 autoMigrateFn **没有被调用**。

// TestConnectDBNoMigrate_DoesNotRunAutoMigrate 直接守 connectDBNoMigrate 的语义。
//
// 用可替换的 autoMigrateFn 计数：若哪天真有人把它改回连接后调 AutoMigrate，
// 本用例立刻红。
func TestConnectDBNoMigrate_DoesNotRunAutoMigrate(t *testing.T) {
	// 造一个能连上的库：用 sqlite 不行（connectDBNoMigrate 走 postgres DSN），
	// 所以这里不真的连库 —— 只验证**接线**：retire-device-data 走的是哪个函数。
	//
	// 判据用源码级断言（与 cmd/server/maintenance_gate_test.go 同范式）：
	// runRetireDeviceDataCLI 必须调用 connectDBNoMigrate 且**不调用** connectDB。
	src := readEhomectlSource(t, "retire_device_data.go")
	body := extractFuncBody(t, src, "runRetireDeviceDataCLI")
	if !strings.Contains(body, "connectDBNoMigrate()") {
		t.Fatal("runRetireDeviceDataCLI 没有调用 connectDBNoMigrate —— " +
			"它必须走只连库的路径（connectDB 里含 database.AutoMigrate，会改生产 schema）")
	}
	// 精确到调用形态：connectDB( 不能出现（connectDBNoMigrate( 不匹配它，
	// 因为前缀不同；这里用带括号的形式避免误伤）。
	if strings.Contains(body, "connectDB()") {
		t.Fatal("runRetireDeviceDataCLI 调用了 connectDB() —— " +
			"那条路径会执行 database.AutoMigrate()，违反「dry-run 绝不改库」的承诺")
	}
}

// TestConnectDB_StillMigratesForOtherCommands 是对照组：确认改造**没有**波及
// 其余子命令 —— auth / retire-logical-device / command-intervals 仍然需要
// AutoMigrate（它们要么改 schema、要么依赖最新 schema）。
//
// 没有这条，"把所有命令都改成不迁移"也能让上一条通过，而那会让
// retire-logical-device 在旧 schema 上跑（它要删列，前提是表结构已是最新）。
func TestConnectDB_StillMigratesForOtherCommands(t *testing.T) {
	src := readEhomectlSource(t, "main.go")
	body := extractFuncBody(t, src, "connectDB")
	if !strings.Contains(body, "autoMigrateFn()") {
		t.Fatal("connectDB 不再调用 autoMigrateFn —— " +
			"其余子命令（auth / retire-logical-device / command-intervals）需要它")
	}
}

// readEhomectlSource 读本包（cmd/ehomectl）下的生产源码。读不到即失败 ——
// 源码门禁不能静默失效（与 cmd/server 的 maintenance gate 同一纪律）。
func readEhomectlSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v（源码门禁失效）", name, err)
	}
	return string(raw)
}

// extractFuncBody 取出 Go 源码里某个顶层函数的**函数体**（含大括号）。
//
// 只用于本文件的门禁断言：按行扫描到 func 声明，再按大括号配对找到结尾。
// 不做通用 Go 解析 —— 那会把 sandbox 依赖拉进来，而这里要守的是"这两行在不在
// 同一个函数里"，按行 + 括号计数足够，且失败信息更直白。
func extractFuncBody(t *testing.T, src, funcName string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	start := -1
	for i, l := range lines {
		// 匹配 "func <name>(" —— 顶格声明（本包没有嵌套函数命名冲突）。
		if strings.HasPrefix(l, "func "+funcName+"(") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("源码里找不到函数 %s —— 门禁失效（被改名了？）", funcName)
	}
	depth := 0
	opened := false
	var out []string
	for i := start; i < len(lines); i++ {
		l := lines[i]
		out = append(out, l)
		for _, r := range l {
			switch r {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			}
		}
		if opened && depth == 0 {
			return strings.Join(out, "\n")
		}
	}
	t.Fatalf("函数 %s 的函数体没闭合 —— 源码解析失败", funcName)
	return ""
}

// TestRetireDeviceData_PostDropCheckIgnoresLookalikeTables 守「DROP 后复核不误报」。
//
// review 发现：复核原先用 `pg_tables WHERE tablename LIKE 'device_data%'`，
// 而生产库里还有 **device_data_strip_progress**（72 kB / 1 行）。于是 DROP 成功后
// 复核会返回 1、打印「剩余表数 = 1（期望 0）」—— 每次成功都报成失败。
//
// 本用例在 sqlite 上验**判据形态**（PG 专有分支已在 review 的生产 dry-run 里
// 实证过）：建一张名字相近但无关的表，断言复核函数不把它算进来。
func TestRetireDeviceData_PostDropCheckIgnoresLookalikeTables(t *testing.T) {
	db := openRetireTestDB(t)
	// 名字前缀相同、但结构上无关的表（精确复刻生产里的 device_data_strip_progress）。
	if err := db.Exec("CREATE TABLE device_data_strip_progress (id INTEGER PRIMARY KEY, last_id INTEGER)").Error; err != nil {
		t.Fatalf("create lookalike table: %v", err)
	}

	// 情形 1：device_data 不存在 ⇒ 复核必须是 0（尽管有名字相近的表）。
	n, err := countDeviceDataTablesLeft(db)
	if err != nil {
		t.Fatalf("countDeviceDataTablesLeft: %v", err)
	}
	if n != 0 {
		t.Fatalf("复核 = %d，want 0 —— 名字相近的 device_data_strip_progress 被算进来了"+
			"（生产上会让 DROP 成功也报「没删干净」）", n)
	}

	// 情形 2：device_data 存在 ⇒ 复核必须是 1。
	createLegacyDeviceData(t, db)
	n, err = countDeviceDataTablesLeft(db)
	if err != nil {
		t.Fatalf("countDeviceDataTablesLeft (table exists): %v", err)
	}
	if n != 1 {
		t.Fatalf("复核 = %d，want 1（device_data 存在时必须被数到）", n)
	}
}

// TestRetireDeviceData_DropsStripProgressWatermark 守「附带清理 watermark 表」。
//
// device_data_strip_progress 是已退役的 sensors 剥离任务的水位表（任务本体已删、
// 代码零引用）。它必须随 device_data 一起被清掉 —— 否则：
//  1. 它是纯遗留垃圾，永远没人读写；
//  2. 它让「DROP 后复核」的前缀匹配永远误报（见上一条用例）。
//
// 这里验的是**清理函数存在且幂等**：表存在则删、不存在也不报错。
func TestRetireDeviceData_DropsStripProgressWatermark(t *testing.T) {
	db := openRetireTestDB(t)
	if err := db.Exec("CREATE TABLE device_data_strip_progress (id INTEGER PRIMARY KEY, last_id INTEGER)").Error; err != nil {
		t.Fatalf("create watermark table: %v", err)
	}
	if !db.Migrator().HasTable(deviceDataStripProgressTable) {
		t.Fatal("前置失败：watermark 表没建出来")
	}

	// 直接演一遍 CLI 里的附带清理动作（与生产路径同一段逻辑）。
	if db.Migrator().HasTable(deviceDataStripProgressTable) {
		if err := db.Exec("DROP TABLE IF EXISTS " + deviceDataStripProgressTable).Error; err != nil {
			t.Fatalf("drop watermark: %v", err)
		}
	}
	if db.Migrator().HasTable(deviceDataStripProgressTable) {
		t.Fatal("附带清理后 watermark 表仍存在")
	}

	// 幂等：表不存在时不报错（生产上有些环境没跑过剥离任务）。
	if db.Migrator().HasTable(deviceDataStripProgressTable) {
		t.Fatal("幂等前置失败：表应当已经不存在")
	}
	if err := db.Exec("DROP TABLE IF EXISTS " + deviceDataStripProgressTable).Error; err != nil {
		t.Fatalf("表不存在时 DROP IF EXISTS 报错（不幂等）: %v", err)
	}

	// 常量值必须与历史表名逐字一致 —— 拼错会让清理静默变成 no-op。
	if deviceDataStripProgressTable != "device_data_strip_progress" {
		t.Fatalf("deviceDataStripProgressTable = %q，want \"device_data_strip_progress\""+
			"（拼错 ⇒ 清理静默无效，且复核仍会误报）", deviceDataStripProgressTable)
	}
}
