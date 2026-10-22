// Package testutil provides test helpers for EHomeSystem backend tests.
//
// It abstracts database creation so tests can run against either SQLite (fast,
// local, default) or PostgreSQL (integration, requires running PG instance).
//
// Switch via environment variable:
//
//	EHOME_TEST_DB=postgres   → connect to PG (uses EHOME_DB_HOST/PORT/USER/PASSWORD/NAME)
//	EHOME_TEST_DB=sqlite     → in-memory SQLite (default)
//
// Usage in test files:
//
//	db := testutil.OpenTestDB(t)
//	// ... use db as *gorm.DB ...
package testutil

import (
	"ehome/backend/internal/dbschema"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// allModels is the complete list of models for AutoMigrate.
// Centralised here so every test gets the same set.
var allModels = []interface{}{
	&models.Node{},
	&models.Channel{},
	&models.ConfigTemplate{},
	&models.EdgeDevice{},
	&models.DeviceConfig{},
	&models.DeviceData{},
	&models.UnifiedData{},
	&models.DataSource{},
	&models.DataSourceHealth{},
	&models.FailoverLog{},
	&models.OTATask{},
	&models.Firmware{},
	&models.Notification{},
	&models.NotificationChannel{},
	&models.NotificationDelivery{},
	&models.User{},
	&models.AuthState{},
	&models.AuthOutbox{},
	&models.InitializationToken{},
	&models.SecurityAuditEvent{},
	&models.Vendor{},
	&models.DeviceModel{},
	&models.NodeEvent{},
	&models.CalibrationCache{},
	&models.PendingWriteRecord{},
	&models.NodeLog{},
	&models.CommandExecution{},
	&models.CommandAttempt{},
	&models.CommandOutbox{},
	&models.CommandInbox{},
	&models.CommandConfirmation{},
	&models.CommandManualResolution{},
	&models.ConfigChangeOutbox{},
	// 清理前置条件 B: 命令域监控计数基线 (单行表, 与生产 AutoMigrate 列表同步)
	&models.CommandMetricsBaseline{},
	// v3.0: GPIO/PWM peripheral control models
	&models.GPIOConfig{},
	&models.PWMConfig{},
	// 数据生命周期 P0: 逻辑设备身份
	&models.LogicalDevice{},
	// 数据生命周期 M 迁移: 大表回填进度水位
	&models.BackfillJob{},
	// 数据生命周期 P3: 合并搬迁任务进度
	&models.MergeJob{},
	// 阈值告警引擎 (方案 v0.4 §5.1.1 任务C; 与生产 AutoMigrate 列表同步)
	&models.AlertRule{},
	&models.AlertEvent{},
	// 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1)
	&models.AutomationRule{},
	&models.AutomationEvent{},
	// 多动作子表 (方案 §3.2.3 M1 / §5.1.1; 与生产 AutoMigrate 列表同步)
	&models.AutomationRuleAction{},
	// 数据字段解析层 (方案 v0.2 §4.2/§4.3; 与生产 AutoMigrate 列表同步)
	&models.SensorFieldObs{},
	&models.SensorFieldMeta{},
	// 规则数据来源与复合来源 (方案 2026-10-22 §4; 与生产 AutoMigrate 列表同步)
	&models.SourceGroup{},
	&models.SourceGroupMember{},
	&models.SourceGroupFailoverLog{},
	&models.SourceGroupHealth{},
}

// OpenTestDB opens a test database based on EHOME_TEST_DB env var.
//   - "sqlite" or empty → in-memory SQLite (default, fast)
//   - "postgres"        → PostgreSQL using EHOME_DB_* env vars
//
// The database is migrated with all models automatically.
// For PostgreSQL, each test gets an isolated schema that is cleaned up via t.Cleanup.
func OpenTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	driver := os.Getenv("EHOME_TEST_DB")
	switch driver {
	case "", "sqlite":
		return openSQLite(t)
	case "postgres":
		return openPostgres(t)
	default:
		t.Fatalf("unknown EHOME_TEST_DB value %q (use 'sqlite' or 'postgres')", driver)
		return nil
	}
}

// IsPostgres returns true if the test DB backend is PostgreSQL.
func IsPostgres() bool {
	return os.Getenv("EHOME_TEST_DB") == "postgres"
}

func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(allModels...); err != nil {
		t.Fatalf("sqlite automigrate: %v", err)
	}
	// 与生产启动共用同一迁移函数（N-23）：否则部分唯一索引只在生产存在，
	// SG-04「两库各跑」会在 SQLite 测试库假绿。
	if err := dbschema.EnsureSourceGroupSchema(db); err != nil {
		t.Fatalf("sqlite source_group schema: %v", err)
	}
	return db
}

// reapStaleSchemas 回收**上一次运行残留**的 test_* 隔离 schema。
//
// 为什么需要它（本仓真实事故，非假设）：
//
//	openPostgres 用 `t.Cleanup` 拆 schema，而 `t.Cleanup` **只在进程正常退出时执行**。
//	若 go test 被 SIGKILL / SIGPIPE 打断（`go test … | head` 就是 SIGPIPE），
//	清理钩子根本不会跑，schema 永久留在库里。
//	实测复现：跑 PG 测试并在约 800ms 时 `kill -9`，`ehome_sim_pg` 里留下
//	`test_3861920_1789489580224261933`。
//	历史同源事故见 docs/取证/投递审计清理与PG验证-2026-09-14.md §3.4/§8.2
//	（当时靠人工 DROP 收场，§8 明确建议「给 openPostgres 增加启动时的陈旧 schema 兜底回收」——本条即该建议的落地）。
//
// ── 安全规则（**绝不动可能仍在使用的 schema**）──────────────────────────
//
//	schema 名格式为 `test_<pid>_<unixnano>`（见 randomSuffix）。
//	仅当**该 pid 当前不存在**时才回收（`syscall.Kill(pid, 0)` 判存活）。
//	这样并行跑多个测试进程时互不误伤；进程已死而 schema 还在 ⇒ 必然是残留。
//
// ── 边界（诚实声明）──────────────────────────────────────────────────────
//
//	· 只回收**本库**里的 schema；不跨库，不碰非 `test_` 前缀；
//	· pid 复用理论上会让「已死进程的 pid」被新进程占用 ⇒ 该 schema 多留一轮
//	  （**只会漏收，不会误删** —— 刻意选的失败方向）；
//	· 认不出命名格式的一律不碰（fail-closed）；
//	· 回收失败只忽略，**不让测试失败**（清理是尽力而为，不该阻断用例）。
func reapStaleSchemas(base *gorm.DB) {
	rows, err := base.Raw("SELECT nspname FROM pg_namespace WHERE nspname LIKE 'test\\_%'").Rows()
	if err != nil {
		return
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}
	for _, name := range names {
		if !shouldReapSchema(name, processAlive) {
			continue
		}
		base.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", name))
	}
}

// shouldReapSchema 是回收**决策**（纯函数，存活判定可注入，故可被行为测试覆盖）。
//
// 为什么必须抽成纯函数：决策就是安全规则的全部内容，必须能被**行为**验证，
// 而不是靠扫描源码里有没有某个字符串。
// 本仓刚发生过反例：判据 `strings.Contains(body, "processAlive(pid)")` 在
// `if processAlive(pid) && false {…}` 之下**依然成立** —— 变异当场证明那是假绿。
//
// 规则：
//
//	· 认不出 `test_<pid>_<nano>` 格式 ⇒ **不回收**（fail-closed，宁可漏收不误删）；
//	· pid 仍存活 ⇒ **不回收**（可能是并行测试进程正在用）；
//	· 其余（格式合法且 pid 已死）⇒ 回收。
//
// 已知宽松点：pid 复用会让「已死进程的 pid」被新进程占用 ⇒ 该 schema 多留一轮。
// 方向是**漏收而非误删**，这是刻意选的失败方向。
func shouldReapSchema(name string, alive func(int) bool) bool {
	pid, ok := pidFromSchemaName(name)
	if !ok {
		return false
	}
	return !alive(pid)
}

// pidFromSchemaName 从 `test_<pid>_<nano>` 解析 pid；格式不符返回 ok=false。
func pidFromSchemaName(name string) (int, bool) {
	rest := strings.TrimPrefix(name, "test_")
	if rest == name {
		return 0, false
	}
	parts := strings.SplitN(rest, "_", 2)
	if len(parts) != 2 {
		return 0, false
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processAlive 判断 pid 是否仍存在（signal 0 只做权限/存在性检查，不真的发信号）。
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// productionDBNames 是**禁止测试连接**的库名（生产库白名单，精确匹配）。
var productionDBNames = []string{"ehome"}

// IsProductionDBName 报告 dbname 是否是生产库名（测试上下文的禁用名单）。
//
// 只做**精确匹配**：`ehome_test` / `ehome_e2e` / `ehome_merge_prod` 都放行，
// 只有裸 `ehome` 被拦。刻意不用前缀匹配 —— 那会把所有合法测试库都误伤。
func IsProductionDBName(dbname string) bool {
	name := strings.TrimSpace(dbname)
	for _, n := range productionDBNames {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// GuardProductionDB 在测试连接疑似生产库时直接 fail-fast。
//
// 为什么必须有这道守卫（2026-10-22 实测事故）：
// 有一次 PG 集成测试带着 `EHOME_DB_NAME=ehome` 跑，某个**自带 openDB 助手**的包
// （绕过本包的 schema 隔离）直接把测试夹具写进了生产库 public schema，
// 留下 2 条同键双 active 的孤儿行。危害不止脏数据：**后续部署的迁移自检会
// fail-closed 拒绝启动**（该自检正是为这种双 active 设计的）。
//
// 逃生门 `EHOME_ALLOW_PROD_DB_TESTS=1` 只用于「在生产形状副本上验证迁移」，
// 变量名带 PROD 且需显式设值，避免手滑。
func GuardProductionDB(t *testing.T, dbname string) {
	t.Helper()
	if os.Getenv("EHOME_ALLOW_PROD_DB_TESTS") == "1" {
		return
	}
	if IsProductionDBName(dbname) {
		t.Fatalf("拒绝在疑似生产库 %q 上运行测试：请设 EHOME_DB_NAME=<专用测试库>（如 ehome_test）。\n"+"\t"+
			"真要在生产形状副本上验证迁移：EHOME_ALLOW_PROD_DB_TESTS=1 并先确认已备份。\n"+"\t"+
			"（2026-10-22 事故：EHOME_DB_NAME=ehome 曾把测试夹具写进生产库 data_sources）", dbname)
	}
}

func openPostgres(t *testing.T) *gorm.DB {
	t.Helper()

	host := envOr("EHOME_DB_HOST", "localhost")
	port := envOr("EHOME_DB_PORT", "5432")
	user := envOr("EHOME_DB_USER", "ehome")
	pass := envOr("EHOME_DB_PASSWORD", "ehome123")
	dbname := envOr("EHOME_DB_NAME", "ehome_test")

	GuardProductionDB(t, dbname)

	// First connection: create the isolated schema
	baseDSN := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, pass, dbname)

	base, err := gorm.Open(postgres.Open(baseDSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open postgres (base): %v (dsn: host=%s port=%s user=%s dbname=%s)", err, host, port, user, dbname)
	}

	// 回收上一次被 SIGKILL/SIGPIPE 打断留下的 schema（见 reapStaleSchemas 的说明）。
	// 放在建新 schema **之前**：这样残留在本轮就被清掉，而不是滚雪球。
	reapStaleSchemas(base)

	schemaName := fmt.Sprintf("test_%s", randomSuffix())
	if err := base.Exec(fmt.Sprintf("CREATE SCHEMA %s", schemaName)).Error; err != nil {
		sqlDB, _ := base.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
		t.Fatalf("create postgres schema %s: %v", schemaName, err)
	}

	// Close the base connection and reopen with search_path in the DSN so
	// every connection in the pool resolves unqualified table names correctly.
	sqlDB, err := base.DB()
	if err != nil {
		t.Fatalf("get postgres base connection: %v (schema: %s)", err, schemaName)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close postgres base connection: %v (schema: %s)", err, schemaName)
	}

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable search_path=%s,public",
		host, port, user, pass, dbname, schemaName)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(gormlogger.Silent),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open postgres: %v (dsn: host=%s port=%s user=%s dbname=%s schema=%s)", err, host, port, user, dbname, schemaName)
	}

	if err := db.AutoMigrate(allModels...); err != nil {
		t.Fatalf("postgres automigrate: %v", err)
	}

	// Cleanup: drop the test schema when test finishes
	t.Cleanup(func() {
		d, _ := db.DB()
		if d != nil {
			d.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
			d.Close()
		}
	})

	return db
}

// SetTransactionIsolation is a cross-DB helper for setting transaction
// isolation level. On PostgreSQL it executes SET TRANSACTION ISOLATION LEVEL
// REPEATABLE READ. On SQLite it is a no-op (SQLite doesn't support the
// syntax, but its transactions are already SERIALIZABLE which is stricter).
//
// Usage:
//
//	db.Transaction(func(tx *gorm.DB) error {
//	    testutil.SetTransactionIsolation(tx)
//	    // ... queries see a consistent snapshot ...
//	})
func SetTransactionIsolation(tx *gorm.DB) {
	if IsPostgres() {
		tx.Exec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
	}
	// SQLite: no-op. SQLite transactions are SERIALIZABLE by default,
	// which is strictly stronger than REPEATABLE READ.
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func randomSuffix() string {
	// Use PID + timestamp for uniqueness without importing crypto/rand
	// Fix: previously used os.Getpid() twice (identical values → schema collision)
	return fmt.Sprintf("%d_%d", os.Getpid(), time.Now().UnixNano())
}
