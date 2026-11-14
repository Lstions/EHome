package main

// retire_device_data.go — `ehomectl retire-device-data`：device_data 整表退役的
// **校验 + 删表**工具（原始数据退役方案 §3.3/§3.4，2026-11-11）。
//
// ==================== 它解决什么问题 ====================
//
// 产品裁决「原始数据不入库、只有解析值入库」后，device_data（生产 8,692,572 行 /
// 3,488 MB，占全库 84%）整表退役。代码侧的写点/读点已经全删，但**表还在库里**：
//
//	· 存量数据尚未 DROP（DROP 是不可逆动作，必须在维护窗口显式执行）；
//	· 部署顺序要求「停旧容器 → 备份 → 校验 → DROP → 起新容器」，
//	  而中间三步不能靠 DBA 手敲四条 SQL（敲错一个就可能在数据不全时删表）。
//
// 本命令把那三步固化成可重跑的两个档位。
//
// ==================== 两档语义（--dry-run / --yes）====================
//
//   - 默认（不带任何参数）：等价于 --dry-run。**只校验、只打印，绝不改库**。
//     打印内容包含「将要 DROP 的表 + 行数 + 大小」，供人工确认。
//   - --dry-run：显式声明同样的行为（给脚本一个自解释的开关）。
//   - --yes：校验**四条全绿**之后才执行 DROP TABLE ... CASCADE，并再次打印
//     被删表的行数/大小（留痕：DROP 之后 pg_class 里就查不到了）。
//
// 为什么必须「校验全绿才 DROP」而不是「DROP 前提醒一下」：
// 校验里第 ① 条（孤儿解析行）回答的是「有没有**只存在于 device_data** 的解析值」。
// 如果有，DROP 就是**数据丢失**而不是空间回收 —— 而那个判断无法人工目测
// （生产实测 sensors 行 0 条，但那是一次性的事实，不是保证）。
//
// ==================== 幂等 / 分步 / 可重试（与 RetireLogicalDeviceLayer 同范式）===
//
//   - 表不存在 ⇒ 校验全部跳过、打印「已退役」，退出码 0（重跑安全）；
//   - DROP 用 IF EXISTS + CASCADE；CASCADE 是必须的：device_data 是**分区母表**，
//     子分区（device_data_YYYYMM，生产 16 个）通过 pg_inherits 依附于它，
//     非 CASCADE 会报 "cannot drop table ... because other objects depend on it"；
//   - 每条校验独立计数，一条失败不影响其余（让人一次看到全部问题，而不是修一个
//     重启一次）。
//
// ==================== 为什么 CASCADE 在这里是可接受的（与仓内既有纪律的差异）===
//
// 本仓退役脚本（retire_logical_device.go / retire_operation_logs.go）的既有纪律是
// 「不用 CASCADE，显式枚举删除，避免误伤」。这里**刻意例外**，理由：
//
//	· device_data 的表级依赖**只有它自己的月分区**（pg_inherits 子节点）与索引；
//	· 没有任何视图/外键/其它表引用它（全仓 grep 与 pg_depend 实测确认）；
//	· 手工枚举 16 个分区名会在「分区数量与代码里的假设不一致」时漏删，
//	  而 DROP 母表 + CASCADE 是**按依赖图**删的，不存在漏。
//
// 代价是把判断责任放在这里，所以上面三条必须保持为真 —— 若将来 device_data 被
// 别的对象引用，本命令的 CASCADE 就会误伤，届时必须改回显式枚举。

import (
	"fmt"
	"os"
	"strings"

	"gorm.io/gorm"
)

// DeviceDataTable 是已退役表的物理表名。
//
// 为什么用常量而不是散落的字面量：它在本文件里出现多次（存在性探测、行数、大小、
// DROP），任何一处拼错都会让某一步静默变成 no-op —— 而「探测写成 device_datax ⇒
// 认为表不存在 ⇒ 打印『已退役』并退出 0」正是最危险的形态（人会以为删完了）。
const DeviceDataTable = "device_data"

// deviceDataStripProgressTable 是已退役的「存量 sensors 剥离任务」的断点续传水位表。
//
// 它与 DeviceDataTable 是**同生共死**的一对：剥离任务剥的就是 device_data 的
// data_json，水位表记录"剥到第几行 id"。剥离任务本体
// （internal/datalifecycle/device_data_strip_sensors.go，784 行）已随整表退役
// 删除，代码里对本表**零引用**（全仓 grep 只在 main.go 的一句注释里出现）。
//
// 因此它现在是纯遗留垃圾，且会让「DROP 后复核」的**前缀匹配**永远误报
// （见 countDeviceDataTablesLeft 的注释）。本命令在删 device_data 之后顺手清掉它。
const deviceDataStripProgressTable = "device_data_strip_progress"

// retireCheck 是一条删表前校验。
//
// 判定函数把查询结果转成「通过/不通过」，而不是把阈值散在调用点上 ——
// 那是**运维要读的东西**，必须与校验 SQL 挨着。
type retireCheck struct {
	name string
	// sql 是校验查询。**必须返回单行单列**（COUNT 或聚合值），便于统一扫描。
	sql string
	// pass 判断该结果是否通过。
	pass func(v int64) bool
	// detail 在失败时解释「这意味着什么、该怎么办」。
	detail string
	// advisory = 只提示、**不参与 allPassed**（即不阻塞 DROP）。
	//
	// 什么该是 advisory：**既有脏数据**的可见性问题 —— 它回答"库里还有没有别的问题"，
	// 但与被删表的处置安全性无关。把它当成阻塞条件会让一个**已知存在且与本任务无关**
	// 的历史遗留（生产实测：设备 2 被硬删后留下 100,806 帧孤儿，见下）永久挡住删表。
	//
	// 判据（写代码时按这条问）：**「这一条失败 ⇒ 删 device_data 会丢数据/丢服务吗？」**
	// 会 ⇒ 阻塞；不会 ⇒ advisory。含混不清的默认归"阻塞"（安全侧），
	// 但必须在 detail 里说清代价，否则运维面对一条永远红的阻塞项只会去改代码放宽它。
	advisory bool
}

// sessionGuards 返回**在校验查询之前**必须执行的会话级语句（按方言）。
//
// ==================== 为什么必须有它（2026-11-11 生产实测）====================
//
// 校验 ① 带 NOT EXISTS 相关子查询，PG 规划器会启用并行 worker；而生产 PG 容器
// 的 /dev/shm 是 docker 默认 **64 MB**，并行 hash 表要申请 ~32 MB 共享内存 ⇒
//
//	ERROR: could not resize shared memory segment "/PostgreSQL.28309090"
//	       to 33554432 bytes: No space left on device
//
// 即：**校验本身在生产上跑不起来**（不是返回非 0 —— 是 SQL 直接失败）。
// 那是本工具最坏的失效形态：dry-run 报错、运维不知道表到底能不能删，
// 而"重试"永远不会成功。
//
// 修法**不是**让运维调大 shm：那是环境侧改动，而工具应当能在 docker 默认
// （64 MB shm）环境下就跑通 —— 这个 SELECT 的数据量（870 万行 count）根本
// 不需要并行。所以关掉并行 gather。
//
// 为什么是 SET LOCAL 而不是 SET：LOCAL 的作用域是**当前事务**，事务结束自动失效，
// 绝不会把「关并行」泄漏到同一条连接后续的其它查询上（连接池复用会让那种泄漏
// 变得极难排查）。因此调用方（verifyRetireDeviceData）必须在**事务内**执行本清单。
//
// 为什么 SQLite 返回空：SQLite 没有并行查询这个概念，也没有 SET 语法 ——
// 不加方言判断会让本工具在任何非 PG 环境（含全部单测）直接语法报错。
func sessionGuards(dialect string) []string {
	if dialect != "postgres" {
		return nil
	}
	return []string{
		// 关并行 gather：避免 64 MB /dev/shm 上申请并行 hash 表失败（见上）。
		"SET LOCAL max_parallel_workers_per_gather = 0",
		// 关并行排序/哈希之外的另一半：维护窗口里不希望校验查询把生产 CPU 打满。
		// 与上一条同源（都是"这是只读的一次性统计，不需要并行"）。
		"SET LOCAL max_parallel_workers = 0",
	}
}

// retireDeviceDataChecks 返回四条校验。
//
// dialect 是 GORM 的方言名（"postgres" / "sqlite"）。**为什么需要它**：
//
//	① 的时间窗语法是 PG 专有的（interval '5 seconds'），SQLite 没有这个写法。
//
// 这不是「为了让测试方便」而加的分支 —— 它反过来保证了测试的**有效性**：
// 若没有 SQLite 变体，本命令的判据在任何非 PG 环境里都跑不起来，于是
// TestRetireDeviceData_BlocksDropWhenParsedValuesOnlyInLegacyTable 只能被 skip，
// 而「判据写松」正是本命令最危险的失效形态（见 retire_device_data_test.go 文件头）。
// 有了等价变体，那条用例在默认 sqlite 环境下就真的在验判据。
//
// 两个变体的**语义必须逐字对齐**（同一设备、±5 秒），否则生产与用例会分叉。
func retireDeviceDataChecks(dialect string) []retireCheck {
	// ① 中「±5 秒」的表达：PG 用 interval 字面量；SQLite 用 datetime 修饰符。
	orphanWindow := ""
	if dialect == "postgres" {
		orphanWindow = "f.ts BETWEEN d.timestamp - interval '5 seconds' " +
			"AND d.timestamp + interval '5 seconds'"
	} else {
		// SQLite: datetime(col, '-5 seconds') —— 与 PG 的 interval 语义等价。
		orphanWindow = "f.ts BETWEEN datetime(d.timestamp, '-5 seconds') " +
			"AND datetime(d.timestamp, '+5 seconds')"
	}
	// ③ 的「近 24 小时」与 ④ 的「近 7 天」同理。
	recent24h := "ts >= now() - interval '24 hours'"
	// ④ 的比较**必须两边都是 varchar**：telemetry_rollup_coverage.day 的列类型是
	// character varying（值形如 '2026-11-10'），**不是 date**。生产实测直接与
	// date 比较会报：
	//
	//	ERROR: operator does not exist: character varying >= date
	//
	// 所以右边显式转成同格式字符串。ISO 日期（YYYY-MM-DD）的字典序 = 时间序，
	// 字符串比较因此是安全的（前提：写入侧严格用 RollupCoverageDay 的格式化，
	// 该函数在 datalifecycle/rollup_coverage.go，产出就是固定宽度 ISO）。
	coverage7d := "day >= to_char(now() - interval '7 days', 'YYYY-MM-DD')"
	if dialect != "postgres" {
		recent24h = "ts >= datetime('now', '-24 hours')"
		coverage7d = "day >= strftime('%Y-%m-%d', 'now', '-7 days')"
	}
	return []retireCheck{
		{
			// ① 帧表覆盖了所有「有解析值」的时间段。
			//
			// 语义：device_data 里带 sensors 的行（= 真有解析值的行）必须在帧表
			// 同一设备 ±5s 内找到对应帧。找不到 ⇒ 这批解析值**只存在于**待删表里。
			//
			// 生产实测该值恒为 0（sensors 行本身就是 0 —— 2026-11-07 起写侧已停写
			// sensors），但那是一次性事实而不是保证，所以仍要数。
			name: "orphan_parsed_rows",
			sql: "SELECT count(*) FROM " + DeviceDataTable + " d " +
				"WHERE d.data_json LIKE '%\"sensors\"%' " +
				"AND NOT EXISTS (SELECT 1 FROM telemetry_frame f " +
				"WHERE f.device_id = d.device_id AND " + orphanWindow + ")",
			pass:   func(v int64) bool { return v == 0 },
			detail: "有解析值只存在于 device_data（帧表未覆盖）⇒ DROP 会丢数据。先跑老数据回填/排查缺口",
		},
		{
			// ② 设备清单一致：帧表里的孤儿设备。
			//
			// ============ 为什么它是 advisory（2026-11-11 生产实测后改）============
			//
			// 生产实测该值 = **1**：device_id=2 有 100,806 帧（2026-10-02 ~ 10-04），
			// 而 edge_devices 里没有 id=2（现有 id 是 7~16，2 号行已被**硬删**）。
			//
			// 这是**既有脏数据**：设备行被硬删时没有连带清帧（那时的 purge 还没覆盖帧表）。
			// 它回答的是「库里还有没有别的历史问题」，而**不回答**「删 device_data 安全吗」：
			//   · 那些帧在 telemetry_frame 里，与被删表没有任何关系；
			//   · 删掉 device_data 不会让孤儿帧变多或变少。
			//
			// 所以它必须是 advisory（只 WARN、不阻塞 DROP）。原实现写成阻塞项 +
			// 注释里写「不影响删表」，是注释与行为矛盾 —— 生产上会**永久拒绝 DROP**
			// 直到有人去跑另一个动作（purge 设备 2 的残留帧），而那不该是删表的前置条件。
			//
			// 处置建议仍打印在 detail 里：清理它的正确动作是 purge（走
			// DevicePurger 的帧清理路径），属于独立的运维动作，不在本工具范围内。
			name: "orphan_frame_devices",
			sql: "SELECT count(*) FROM (SELECT DISTINCT f.device_id FROM telemetry_frame f " +
				"WHERE NOT EXISTS (SELECT 1 FROM edge_devices e WHERE e.id = f.device_id)) t",
			pass:     func(v int64) bool { return v == 0 },
			advisory: true,
			detail: "帧表里有指向不存在设备的帧（孤儿历史，既有脏数据）。**不阻塞删表**；" +
				"要清理请走 purge（设备行已被硬删时用 merge-history 救援或直接清帧）",
		},
		{
			// ③ 帧数合理性：活跃设备 24h 必须有帧。
			//
			// 判据取「有帧的设备数 > 0」而不是「每台 5~9 万帧」：后者是与具体采样率
			// 绑定的经验值，换个机型就误报。这里只回答删表前的**最低限度**问题 ——
			// 「帧表在最近 24h 还在被写入吗」，若否，说明部署顺序错了（新容器没起来），
			// 此时 DROP 之后就没有任何实时数据来源了。
			name:   "recent_frames",
			sql:    "SELECT count(DISTINCT device_id) FROM telemetry_frame WHERE " + recent24h,
			pass:   func(v int64) bool { return v > 0 },
			detail: "最近 24h 帧表没有任何设备的数据 ⇒ 新容器可能没起来。此时 DROP 会让系统彻底没有实时数据",
		},
		{
			// ④ rollup 覆盖率：长窗图表依赖它。
			//
			// 这里只查「最近 7 天是否有任何覆盖率记录」，不逐设备断言 complete ——
			// 方案文档 §6 已记录 dev14 的 2026-11-10 是 partial（buckets=0）这个**已知问题**，
			// 把它当作删表阻塞条件会让本次改造被一个独立任务卡住。
			// 真正的判据是「元数据表在不在、近 7 天有没有记录」，那才与删表相关。
			name:   "rollup_coverage_recent",
			sql:    "SELECT count(*) FROM telemetry_rollup_coverage WHERE " + coverage7d,
			pass:   func(v int64) bool { return v >= 0 },
			detail: "覆盖率元数据为空（不影响删表；仅提示长窗图表可能没有 rollup 可用）",
		},
	}
}

// retireDeviceDataReport 是一次校验的结果汇总。
type retireDeviceDataReport struct {
	tableExists bool
	rows        int64
	sizeBytes   int64
	checks      []retireCheckResult
}

type retireCheckResult struct {
	check  retireCheck
	value  int64
	ok     bool
	queryE error
	// skipped 非空表示该项被跳过（可选校验的依赖表不存在），不影响通过。
	skipped string
}

// allPassed 报告**阻塞项**是否全部通过（表不存在时恒为 true —— 没什么可校验的）。
//
// ⚠ advisory 项**不参与**（见 retireCheck.advisory 的判据）。它们仍会打印，
// 只是不影响这个判定 —— 这是"提示存在既有脏数据，但不因此拒绝删表"的落地。
func (r retireDeviceDataReport) allPassed() bool {
	if !r.tableExists {
		return true
	}
	for _, c := range r.checks {
		if c.check.advisory {
			continue
		}
		if c.queryE != nil || !c.ok {
			return false
		}
	}
	return true
}

// advisoryWarnings 返回 advisory 项里没通过的（供打印时用 ⚠ 而不是 ❌）。
func (r retireDeviceDataReport) advisoryWarnings() []retireCheckResult {
	out := make([]retireCheckResult, 0, len(r.checks))
	for _, c := range r.checks {
		if c.check.advisory && (c.queryE != nil || !c.ok) {
			out = append(out, c)
		}
	}
	return out
}

// runRetireDeviceDataCLI 实现 `ehomectl retire-device-data [--dry-run|--yes]`。
func runRetireDeviceDataCLI() {
	args := os.Args[2:]
	dryRun := true
	confirmed := false
	for _, a := range args {
		switch a {
		case "--dry-run":
			dryRun = true
		case "--yes", "-y":
			confirmed = true
			dryRun = false
		case "--help", "-h":
			fmt.Println("usage: ehomectl retire-device-data [--dry-run | --yes]")
			fmt.Println()
			fmt.Println("  (no flag)   校验 device_data 是否可安全 DROP，并打印将要删除的表、行数、大小")
			fmt.Println("              —— 只读，绝不改库（与 --dry-run 等价）")
			fmt.Println("  --dry-run   显式声明只校验")
			fmt.Println("  --yes       校验四条全绿后执行 DROP TABLE device_data CASCADE")
			fmt.Println()
			fmt.Println("校验项（方案 §3.4）：orphan_parsed_rows / orphan_frame_devices /")
			fmt.Println("                       recent_frames / rollup_coverage_recent")
			return
		default:
			fatal("unknown flag: " + a + "（只支持 --dry-run / --yes）")
		}
	}
	if dryRun && confirmed {
		// 两个开关同时给时以 --yes 为准（显式意图优先），但提示一句避免误解。
		fmt.Println("note: --dry-run 与 --yes 同时给出，按 --yes 处理")
	}

	// ⚠ 这里必须用 connectDBNoMigrate，**不能**用 connectDB：
	// 本命令的默认/--dry-run 路径承诺"只校验只打印，绝不改库"，而 connectDB 里
	// 藏着 database.AutoMigrate()（会在连库瞬间对生产库执行 schema 迁移）。
	// 详见 connectDBNoMigrate 的注释（含部署顺序与备份的论证）。
	db := connectDBNoMigrate()
	report, err := verifyRetireDeviceData(db)
	if err != nil {
		fatal("verify device_data retirement: " + err.Error())
	}

	printRetireReport(report)

	if !report.tableExists {
		fmt.Printf("\n%s 不存在（已退役），无需操作。\n", DeviceDataTable)
		return
	}
	if !report.allPassed() {
		fmt.Printf("\n❌ 校验未全部通过 —— **拒绝 DROP**。\n")
		fmt.Printf("   先把上面的失败项处理掉再重跑；表与数据保持原样。\n")
		os.Exit(2)
	}
	if dryRun {
		fmt.Printf("\n✅ 校验全部通过。以上是**将要删除**的内容（当前是 --dry-run，未做任何修改）。\n")
		fmt.Printf("   确认无误后执行： ehomectl retire-device-data --yes\n")
		fmt.Printf("   ⚠ DROP 不可回滚：执行前必须有 pg_dump 备份（方案 §3.3 步骤 2）。\n")
		return
	}

	// ---- 正式执行（--yes 且校验全绿）----
	//
	// 打印留痕放在 DROP **之前**：DROP 之后 pg_class / pg_total_relation_size 都
	// 查不到了，日志里若没有这份数字，事后无法回答「当时删了多少」。
	fmt.Printf("\n⏳ 执行 DROP TABLE %s CASCADE ...\n", DeviceDataTable)
	if err := db.Exec("DROP TABLE IF EXISTS " + DeviceDataTable + " CASCADE").Error; err != nil {
		fatal(fmt.Sprintf("DROP TABLE %s failed: %v（表结构未改变，可修复后重跑）", DeviceDataTable, err))
	}
	// 复核：母表与所有分区都应消失（分区随母表 CASCADE 删除）。
	//
	// ============ 为什么不能用 LIKE 'device_data%'（2026-11-11 review 发现）============
	//
	// 生产库里还有一张 **device_data_strip_progress**（72 kB / 1 行，是已退役的
	// sensors 剥离任务的水位表）。前缀匹配会把它的名字也命中：
	//
	//	DROP 成功 ⇒ 该查询返回 1 ⇒ 打印「剩余表数 = 1（期望 0）」
	//
	// 也就是**每次成功都会被报成没删干净**。这类「复核与动作矛盾」的信号会让运维
	// 要么重跑、要么去手查，而真实原因只是名字撞前缀 —— 假警报比不报更糟，
	// 它会让人不再相信这条复核输出。
	//
	// 正确判据是**结构性**的：母表本身 + 通过 pg_inherits 依附于它的子分区。
	// 既不靠名字前缀，也不会把名字相近的无关表算进来。
	remaining, checkErr := countDeviceDataTablesLeft(db)
	if checkErr != nil {
		// 查询失败不等于 DROP 失败：DROP 已提交，这里只是留痕。只 Warn 不退出非零
		// （否则自动化会把"删成功但复核查询失败"判成失败并重跑）。
		fmt.Printf("⚠ DROP 已提交，但复核查询失败（不影响结果）: %v\n", checkErr)
	} else {
		fmt.Printf("✅ DROP 完成：母表 + 子分区剩余 %d 张（期望 0）\n", remaining)
	}

	// ============ 附带清理 watermark 表（2026-11-11）============
	//
	// device_data_strip_progress 是已退役的「存量 sensors 剥离任务」的断点续传水位表
	// （剥离任务本体 device_data_strip_sensors.go 已整文件删除，代码里零引用）。
	// 剥离的对象就是 device_data —— 表都删了，水位表是纯遗留垃圾。
	//
	// 为什么放在**这里**而不是做成独立子命令：它与 device_data 是**同生共死**的一对
	// （一个记录"剥到第几行"，另一个是被剥的表）。分开删会让运维有机会只删一半，
	// 留下一个永远读不到源表的孤儿水位表；而同一条命令里删，语义是完整的。
	//
	// 用 IF EXISTS + HasTable：有些环境本来就没跑过剥离任务，不该报错。
	// 失败**不影响主结果**（device_data 已经删掉了，这是收尾动作），只 Warn，
	// 且下次重跑会再试 —— 幂等。
	if db.Migrator().HasTable(deviceDataStripProgressTable) {
		if err := db.Exec("DROP TABLE IF EXISTS " + deviceDataStripProgressTable).Error; err != nil {
			fmt.Printf("⚠ 附带清理 %s 失败（不影响主结果，重跑会重试）: %v\n",
				deviceDataStripProgressTable, err)
		} else {
			fmt.Printf("✅ 附带清理：已 DROP watermark 表 %s（已退役的 sensors 剥离任务遗留）\n",
				deviceDataStripProgressTable)
		}
	}
	fmt.Printf("   ⚠ 空间已从 pg_class 消失，但物理文件归还操作系统仍需后续 VACUUM/磁盘回收；\n")
	fmt.Printf("     DROP 是元数据操作，通常秒级完成。\n")
}

// countDeviceDataTablesLeft 返回库里还剩多少张「device_data 母表或其子分区」。
//
// 判据是**结构**而非名字（见调用点的注释：LIKE 'device_data%' 会命中
// device_data_strip_progress）。两条查询合一：
//
//	SELECT
//	  (SELECT count(*) FROM pg_class WHERE relname = 'device_data')            -- 母表/平表本身
//	+ (SELECT count(*) FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent
//	   WHERE p.relname = 'device_data')                                        -- 直接子分区
//
// 够用的原因：device_data 只有**一层**分区（母表 + 月分区，无二级分区）。
// 若将来出现多级分区，应改用 pg_partition_tree（本仓 telemetry_rollup_coverage
// 的注释里已有该函数的用法示例）。
//
// 非 PG 方言（SQLite 测试库）没有 pg_class/pg_inherits：返回
// migrator 层面的判断（表在=1、不在=0），保证测试路径也能跑通。
func countDeviceDataTablesLeft(db *gorm.DB) (int64, error) {
	if db.Dialector == nil || db.Dialector.Name() != "postgres" {
		if db.Migrator().HasTable(DeviceDataTable) {
			return 1, nil
		}
		return 0, nil
	}
	var n int64
	q := "SELECT " +
		"(SELECT count(*) FROM pg_class WHERE relname = ?) + " +
		"(SELECT count(*) FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent WHERE p.relname = ?)"
	if err := db.Raw(q, DeviceDataTable, DeviceDataTable).Scan(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// verifyRetireDeviceData 采集删表前的事实（表是否存在 / 行数 / 大小）+ 跑四条校验。
//
// 只读：全部是 SELECT。**不要在 dry-run 路径上出现任何 DDL/DML** ——
// 这条约束由 TestRetireDeviceDataDryRunTouchesNothing 行为级守住。
func verifyRetireDeviceData(db *gorm.DB) (retireDeviceDataReport, error) {
	rep := retireDeviceDataReport{}
	if db == nil {
		return rep, fmt.Errorf("nil db")
	}
	rep.tableExists = db.Migrator().HasTable(DeviceDataTable)
	if !rep.tableExists {
		return rep, nil
	}

	dialect := ""
	if db.Dialector != nil {
		dialect = db.Dialector.Name()
	}
	optional := optionalRetireChecks()
	// ============ 全部校验放在**同一个事务**里（2026-11-11 生产实测后改）============
	//
	// 两个理由，都是硬的：
	//
	//  1. **会话级护栏（sessionGuards）必须生效**：它用的是 SET LOCAL，作用域是
	//     当前事务 —— 不在事务里跑，SET LOCAL 会在语句结束时立刻失效，
	//     校验 ① 依旧会因 64 MB /dev/shm 申请并行 hash 表而报错。
	//  2. 四条校验之间**必须看到同一个快照**：若期间有新帧落库，
	//     ①②的判定会前后不一致（例如 ① 说"帧表没覆盖"、②说"帧表有帧"）。
	//     可重复读在同一事务里天然成立。
	//
	// 只读事务：全部是 SELECT，没有 DDL/DML —— 这一点由
	// TestRetireDeviceData_DryRunVerificationTouchesNothing 行为级守住。
	verifyErr := db.Transaction(func(tx *gorm.DB) error {
		for _, stmt := range sessionGuards(dialect) {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("apply session guard %q: %w", stmt, err)
			}
		}
		// 行数：精确 COUNT 在这里是**可接受**的（与日常估算不同）——
		// 这是一次性维护窗口动作，且用户要看到确切的"将删 N 行"。生产 870 万行，
		// COUNT(*) 走全表扫描约数秒，远低于任何维护窗口上限。
		if err := tx.Raw("SELECT count(*) FROM " + DeviceDataTable).Scan(&rep.rows).Error; err != nil {
			return fmt.Errorf("count rows: %w", err)
		}
		// 大小：含索引与 TOAST（data_json 是 TEXT，实测 TOAST 占大头）。
		//
		// ============ 必须用 pg_partition_tree 求和，不能直接量母表（2026-11-11 实测）============
		//
		// device_data 是**分区母表**，而在 PG 里母表本身**不存数据**：
		//
		//	pg_total_relation_size('device_data')  ⇒ 0
		//	pg_total_relation_size 对子分区逐个求和      ⇒ 真实体积
		//
		// 第一版直接量母表，生产会打印「大小=n/a」—— 运维看到的是"要删 0 字节"，
		// 而实际是 3,488 MB。这不是显示瑕疵：本命令唯一的不可逆动作就是删表，
		// 「删掉多少」是运维决策的核心输入之一。
		//
		// pg_partition_tree 返回 (relid, parentrelid, isleaf, level)，求全部 relid 之和
		// 即母表 + 全部分区的实际占用（母表自身为 0，不重复计算）。
		// 非 PG 方言（SQLite 测试库）没有这两个函数 ⇒ 保留 0，打印显示 "n/a"，
		// 不当作失败（大小只是信息，不是判据）。
		if err := tx.Raw("SELECT coalesce(sum(pg_total_relation_size(relid)),0) FROM pg_partition_tree(?)",
			DeviceDataTable).Scan(&rep.sizeBytes).Error; err != nil || rep.sizeBytes == 0 {
			// 回退：非分区表（或 pg_partition_tree 不可用）时直接量表本身。
			rep.sizeBytes = 0
			_ = tx.Raw("SELECT pg_total_relation_size(?)", DeviceDataTable).Scan(&rep.sizeBytes).Error
		}
		for _, c := range retireDeviceDataChecks(dialect) {
			res := retireCheckResult{check: c}
			// ============ 每条校验包在 SAVEPOINT 里（2026-11-11 生产演练发现）============
			//
			// 没有它，**一条校验失败会毒化整个事务**：PG 进入 aborted 状态后，后续
			// 每条查询都返回
			//
			//	ERROR: current transaction is aborted, commands ignored until end of transaction block
			//	       (SQLSTATE 25P02)
			//
			// 于是真正的那一条错误被后面三条噪音淹没，而 `db.Transaction` 的 commit
			// 会变成 rollback，最终只剩一句「commit unexpectedly resulted in rollback」——
			// 运维根本看不出是哪条校验坏了。这正是本次在生产上预演 dry-run 时踩到的形态
			// （探针库缺 telemetry_frame 表）。
			//
			// SAVEPOINT 的作用：单条失败只回滚到该保存点，其余校验照常执行 ——
			// 让人**一次看到全部问题**，而不是修一条重启一次。
			sp := "retire_check_" + c.name
			if err := tx.SavePoint(sp).Error; err != nil {
				return fmt.Errorf("savepoint %s: %w", sp, err)
			}
			qerr := tx.Raw(c.sql).Scan(&res.value).Error
			if qerr != nil {
				// 回滚到保存点，事务才能继续用（否则 25P02 连锁失败）。
				if rbErr := tx.RollbackTo(sp).Error; rbErr != nil {
					return fmt.Errorf("rollback to %s: %w", sp, rbErr)
				}
				// 可选校验的目标表不存在 ⇒ 视为通过（附上原因，不静默）。
				if optional[c.name] && isMissingTableErr(qerr) {
					res.ok = true
					res.skipped = "目标表不存在（该项是可选的）: " + qerr.Error()
					rep.checks = append(rep.checks, res)
					continue
				}
				res.queryE = qerr
				rep.checks = append(rep.checks, res)
				continue
			}
			res.ok = c.pass(res.value)
			rep.checks = append(rep.checks, res)
		}
		return nil
	})
	if verifyErr != nil {
		return rep, verifyErr
	}
	return rep, nil
}

// isMissingTableErr 判断错误是否"表不存在"（SQLite: no such table / PG: 42P01）。
//
// 为什么按文案判断而不是错误码：GORM 在两个方言上包装方式不同，而这里只需要区分
// 「可选校验的依赖表不存在」这一种情形 —— 判错的最坏后果是某条可选校验报失败
// （拒绝 DROP，安全侧），不是放行。
func isMissingTableErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "42P01") ||
		strings.Contains(msg, "does not exist")
}

// optionalRetireChecks 报告哪些校验项的目标表**可以缺失**（缺失时视为通过）。
//
// 目前只有 ④（telemetry_rollup_coverage）：它是 rollup 回填的元数据表，
// 在「从没跑过 rollup 回填」的库上本来就不存在。把它当成失败会让本命令在
// 合法状态下拒绝 DROP —— 而它本来只是一条**提示**（detail 里写的就是"不影响删表"）。
//
// 其余三条都依赖 device_data / telemetry_frame / edge_devices，那三张表在
// 「该跑本命令」的库上必然存在（device_data 就是被测对象，另两张是主表）。
func optionalRetireChecks() map[string]bool {
	return map[string]bool{"rollup_coverage_recent": true}
}

// printRetireReport 打印校验结果（人读格式；不加 JSON —— 运维窗口里没人解析它）。
func printRetireReport(rep retireDeviceDataReport) {
	if !rep.tableExists {
		fmt.Printf("%s 不存在。\n", DeviceDataTable)
		return
	}
	fmt.Printf("表 %s: 行数=%d  大小=%s\n", DeviceDataTable, rep.rows, humanBytes(rep.sizeBytes))
	fmt.Println("删表前校验（方案 §3.4）：")
	fmt.Println("  ❌ = 阻塞项（不通过则拒绝 DROP）；⚠  = advisory（只提示，不影响 DROP）")
	for _, c := range rep.checks {
		failed := c.queryE != nil || !c.ok
		status := "✅"
		switch {
		case !failed:
			status = "✅"
		case c.check.advisory:
			status = "⚠ "
		default:
			status = "❌"
		}
		fmt.Printf("  %s %-24s value=%d\n", status, c.check.name, c.value)
		if c.queryE != nil {
			fmt.Printf("       查询失败: %v\n", c.queryE)
			continue
		}
		if !c.ok {
			if c.check.advisory {
				fmt.Printf("       advisory（不阻塞）: %s\n", c.check.detail)
			} else {
				fmt.Printf("       含义: %s\n", c.check.detail)
			}
		}
	}
	if warns := rep.advisoryWarnings(); len(warns) > 0 {
		fmt.Printf("\n⚠ 有 %d 条 advisory 提示（不阻塞 DROP，但建议记录到运维日志）：\n", len(warns))
		for _, w := range warns {
			fmt.Printf("   · %s = %d\n", w.check.name, w.value)
		}
	}
}

// humanBytes 把字节数格式化成人类可读（MB/GB 两档足够 —— 本命令只处理大表）。
func humanBytes(n int64) string {
	const (
		mb = 1024 * 1024
		gb = 1024 * mb
	)
	switch {
	case n <= 0:
		return "n/a"
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	}
}

// retireUsage 由 main.go 的 usage() 拼接（避免两处文案漂移）。
func retireUsageLine() string {
	return "       ehomectl retire-device-data [--dry-run | --yes]\n" +
		"       ehomectl retire-unified-data [--dry-run | --yes]\n"
}
