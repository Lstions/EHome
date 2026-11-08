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
	"slices"
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
	// 注: 逻辑设备概念层已整体移除（2026-10-23, .contract/logical-device-removal.md）。
	// LogicalDevice / BackfillJob / MergeJob 三个模型与其表一并删除，因此这里
	// 不再 AutoMigrate 它们 —— 保留会导致「测试库有表、生产库没有」的二义性。
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

// ============ 陈旧 schema 回收：活主判据 + 年龄兜底（2026-11-08 最终版）============
//
// 为什么需要回收（本仓真实事故，非假设）：
//
//	openPostgres 用 `t.Cleanup` 拆 schema，而 `t.Cleanup` **只在进程正常退出时执行**。
//	若 go test 被 SIGKILL / SIGPIPE 打断（`go test … | head` 就是 SIGPIPE），
//	清理钩子根本不会跑，schema 永久留在库里。
//	实测复现：跑 PG 测试并在约 800ms 时 `kill -9`，`ehome_sim_pg` 里留下
//	`test_3861920_1789489580224261933`。
//
// ============ 活主判据为什么是 application_name，而不是 pid ============
//
// 原实现用 `syscall.Kill(pid, 0)` 判「这个 schema 的创建进程还活着吗」。
// 它在本机直接跑 `go test` 时是对的，但本仓的测试跑在 **docker 容器**里
// （gow.sh），而容器有**独立的 pid 命名空间**（不带 --pid host）：
//
//	容器 A 拿到容器 B 创建的 test_<pidB>_<nano>
//	在 A 的命名空间里查 pidB ⇒ 结论与 B 无关 ⇒ 常判「已死」
//	⇒ DROP SCHEMA ... CASCADE 掉 B **正在用**的 schema
//	⇒ B 的 current_schema() 落回 public
//	⇒ B 的 CREATE/DROP TABLE 全部打到 public
//
// **这不是推演**。2026-11-08 PG 日志实证：
//
//	ERROR:  "telemetry_rollup_1m_pkey" is not a table
//	STATEMENT: DROP TABLE IF EXISTS telemetry_rollup_1m_pkey CASCADE
//
// 而 ehome_test.public 的 telemetry_rollup_1m 就是那一刻消失的。
//
// ── 中途试过的两个错方案（留档，避免后人再走一遍）──────────────────────
//
//	① schema 名带 hostname 做命名空间标识
//	    错在：hostname 在容器里是**容器 ID**，每次 `docker run` 都不同
//	    ⇒ 上一轮容器创建的 schema，本轮容器永远认不出
//	    ⇒ 回收器变成**永久 no-op**。而「永久不回收」对一个会累积脏数据的
//	      回收器来说，比误删更隐蔽 —— 误删会让测试红（有症状），
//	      泄漏只让库慢慢变脏（没症状）。
//
//	② 给 gow.sh 加 --hostname 固定值
//	    错在：两个并发容器共享同一 namespace 段 ⇒ 又回到缺陷 ①（跨命名空间查 pid）
//
// ── 正确判据：PG 服务端的 application_name ─────────────────────────────
//
// 「这个 schema 有没有活着的使用者」只有 **PG 自己知道**（跨容器、跨机器）。
// 所以把「活主」表达成一条连接上的标记：
//
//	openPostgres 的**所有**连接（base 与正式）都带 `application_name=<schemaName>`
//	活主判据 = `SELECT count(*) FROM pg_stat_activity WHERE application_name = ?`
//
// 为什么它严格优于 pid 与 advisory lock：
//
//	· 跨 pid 命名空间 / 跨容器 / 跨机器都有效（判据在 PG 服务端，不在客户端）；
//	· 进程被 SIGKILL ⇒ 连接断开 ⇒ 计数归 0 ⇒ 下一轮可回收（**正是我们要的**）；
//	· **零额外连接、零生命周期管理** —— advisory lock 是 session 级、绑在具体
//	  那条连接上，而 DSN 带连接池：加锁和解锁可能落在不同连接上，
//	  解锁失败 ⇒ 锁 held 到池关闭 ⇒ 回收器永远认为有活主（静默失效）；
//	· 判据是幂等的只读查询，不需要成对操作，天然不会泄漏。
//
// ============ 三条规则（全部朝「宁可漏收」偏）============
//
//	① 格式不合法                    ⇒ 不回收（fail-closed）
//	② 年龄 < minReapAge             ⇒ 不回收  ← 防时钟漂移 + 建表窗口
//	③ 有活主连接（application_name） ⇒ 不回收
//
// ⚠ **规则 ② 不是活主判据**。它的角色是「兜底」：schema 名里的 nano 段是本机
// 时钟，跨机器不保证可比；而 CREATE SCHEMA 与「标记连接可用」之间也存在极短
// 的窗口（base 连接关闭 → 正式连接打开）。年龄阈值同时兜住这两件事。
// 把年龄当判活依据会得出「2 小时前的 schema 一定没人用」这种错误结论。
//
// ============ schema 名里的命名空间段：**不参与判活** ============
//
// 名字格式 `test_<namespace>_<pid>_<nano>` 里的 namespace（hostname）**只用于
// 人看**（能一眼认出是哪个环境建的），**不参与任何回收决策**。
// 原因见上面「中途试过的错方案 ①」：容器环境下它无法稳定标识同一个运行者。
func reapStaleSchemas(base *gorm.DB) {
	for _, info := range listTestSchemas(base) {
		alive := liveConnectionsFor(base, info.Name)
		if !shouldReapSchema(info, alive, time.Now()) {
			continue
		}
		// 两段式：DROP 前**重新确认**（候选列表可能是 T0 拍的，之后环境已变）。
		fresh, ok := reloadSchemaInfo(base, info.Name)
		if !ok {
			continue // 查不清就不动
		}
		alive = liveConnectionsFor(base, fresh.Name)
		if !shouldReapSchema(fresh, alive, time.Now()) {
			continue
		}
		base.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", fresh.Name))
	}
}

// schemaInfo 是一个候选隔离 schema 的**判定所需全部事实**。
//
// 抽成结构体而不是散着传参数：这些事实必须**同时**成立才允许回收，
// 而「同时成立」正是纯函数最容易写错的地方 —— 集中在一处才测得住。
type schemaInfo struct {
	Name string

	// Namespace 是 schema 名里的命名空间段（hostname）。
	//
	// ⚠ **不参与回收决策**（见文件头「schema 名里的命名空间段」）。
	// 保留它只为人看：出问题时能一眼认出是哪个环境留下的。
	Namespace string

	// PID 是本命名空间内的进程号（解析自名字）。0 表示解析失败。
	PID int

	// CreatedUnixNano 是 schema 的创建时刻（来自名字里的 nano 段）。
	// **不查 pg_namespace 的创建时间**：PG 不记录 schema 创建时间，
	// 名字里的 nano 段是唯一来源。0 表示解析失败。
	CreatedUnixNano int64
}

// minReapAge 是回收的最小年龄阈值（**兜底，不是活主判据**）。
//
// 2 小时：远大于任何单个测试用例的耗时（本仓最慢的 PG 用例约 2 秒，
// 整个 api 包约 2 分钟），所以「2 小时还活着」几乎必然是 SIGKILL 残留。
//
// 它兜住两件活主判据管不到的事：
//
//	· 时钟漂移：nano 段是本机时间，跨机器不可比，只有「明显很旧」时才敢下结论；
//	· 建表窗口：base 连接与正式连接交接的瞬间，application_name 计数可能短暂为 0
//	  —— 那个窗口里的 schema 年龄必然接近 0，阈值天然挡住。
const minReapAge = 2 * time.Hour

// shouldReapSchema 是回收**决策**（纯函数：所有外部事实都由入参给出）。
//
// 为什么必须是纯函数：决策就是安全规则的全部内容，必须能被**行为**验证，
// 而不是靠扫描源码里有没有某个字符串。
// 本仓刚发生过反例：判据 `strings.Contains(body, "processAlive(pid)")` 在
// `if processAlive(pid) && false {…}` 之下**依然成立** —— 变异当场证明那是假绿。
//
// 参数 liveConns 是该 schema 的活主连接数（由 liveConnectionsFor 查出后注入，
// 故本函数不碰数据库、可被纯行为测试覆盖）。
func shouldReapSchema(info schemaInfo, liveConns int, now time.Time) bool {
	// ① 格式
	if info.PID <= 0 || info.CreatedUnixNano <= 0 || info.Name == "" {
		return false
	}
	// ② 年龄兜底（不是活主判据，见 minReapAge 注释）
	created := time.Unix(0, info.CreatedUnixNano)
	if now.Sub(created) < minReapAge {
		return false
	}
	// ③ 活主
	return liveConns == 0
}

// liveConnectionsFor 数「有多少条活着的连接把 application_name 标成了这个 schema」。
//
// 查询失败返回 -1（**不是 0**）：0 表示「确认没有活主」，-1 表示「查不清」。
// shouldReapSchema 用 `liveConns == 0` 判定，所以 -1 会被安全地当成「不动」——
// 这是刻意的：查不清的时候宁可残留，不可误删。
func liveConnectionsFor(base *gorm.DB, schemaName string) int {
	if base == nil || schemaName == "" {
		return -1
	}
	var n int
	if err := base.Raw(
		"SELECT count(*) FROM pg_stat_activity WHERE application_name = ?", schemaName).
		Scan(&n).Error; err != nil {
		return -1
	}
	return n
}

// schemaPrefixes 是回收器认得的隔离 schema 前缀。
//
// ============ 为什么放宽成两个前缀，而不是给某个包开例外 ============
//
// `internal/dbschema` 的用例需要「DDL 跨用例持久化」（它测的正是「旧库已有重复
// active 行时迁移必须 fail-closed」，那要求表结构在用例之间保留）。原实现靠
// **不设 search_path、让表落到 public** 来实现这个需求 —— 那是把「需求」和
// 「默认落点」混成了一件事。
//
// 正确做法是给它一个自己的固定 schema（dbschema_<pid>），让 public 恒空。
// 但**它的 schema 也必须被回收器管**，否则就是「某类 schema 不受生命周期约束」
// —— 那是一条规则裂缝，下一个例外会顺着裂缝进来。
//
// 所以这里做的是**放宽合法命名**（规则统一、对所有前缀一视同仁），
// 而不是在 shouldReapSchema 里写 `if isDbschemaPkg { return false }`（规则裂缝）。
//
// 增加新前缀的门槛：它必须满足「回收器能安全判定」（可解析出 pid/nano +
// 用 application_name 表达活主）。做不到这两点的命名不要往这里加。
var schemaPrefixes = []string{"test_", "dbschema_"}

// stripSchemaPrefix 去掉识别得的前缀；不匹配任何前缀时返回 ok=false。
func stripSchemaPrefix(name string) (string, bool) {
	for _, p := range schemaPrefixes {
		if strings.HasPrefix(name, p) {
			return name[len(p):], true
		}
	}
	return "", false
}

// NewIsolatedSchemaName 生成一个**受回收器管理**的隔离 schema 名。
//
// ============ 为什么导出这个函数，而不是让各包自己拼名字 ============
//
// 回收器只认 `schemaPrefixes` 里的前缀 **且格式可解析** 的 schema。
// 如果某个包自己拼了个名字（比如 `dbschema_<pid>`），它看起来对，
// 但 parseSchemaName 会解析失败 ⇒ 回收器永远看不见它 ⇒ 遗留 schema 泄漏，
// 而回收器本身看起来工作正常。
//
// 所以名字必须由**这里**统一生成：格式与回收器的解析器天然同源，不会漂移。
//
// prefix 必须是 schemaPrefixes 里登记过的值之一（否则 panic —— 见下）。
func NewIsolatedSchemaName(prefix string) string {
	if !slices.Contains(schemaPrefixes, prefix) {
		// panic 而不是返回错误：这是**编程错误**，不是运行期状况。
		// 而且必须在测试进程启动时就暴露 —— 一个没登记的前缀意味着
		// 「创建的 schema 永远不会被回收」，静默运行只会积累垃圾。
		panic("testutil: 未登记的 schema 前缀 " + prefix +
			"（先加进 schemaPrefixes，否则回收器认不出它，遗留 schema 会泄漏）")
	}
	return fmt.Sprintf("%s%s_%d_%d", prefix, sanitizeSchemaSegment(currentNamespace()),
		os.Getpid(), time.Now().UnixNano())
}

// parseSchemaName 把 `test_<namespace>_<pid>_<nano>` 解析成 schemaInfo。
//
// 格式不符返回 ok=false（调用方据此不回收）。
//
// ⚠ 旧格式 `test_<pid>_<nano>`（没有 namespace 段）**一律解析失败**，
// 这是刻意的：那些 schema 谁创建的已无从判断，按新规则它们「格式不合法」。
// 它们会作为残留留在库里（除非人工清理）—— 方向是**漏收**，不是误删。
func parseSchemaName(name string) (schemaInfo, bool) {
	base, ok := stripSchemaPrefix(name)
	if !ok {
		return schemaInfo{}, false
	}
	rest := base
	if rest == "" {
		return schemaInfo{}, false
	}
	// 从右往左切：<namespace>_<pid>_<nano>。namespace 自身可能含下划线，
	// 所以只能从尾部切两段，剩下全部是 namespace。
	lastUnderscore := strings.LastIndex(rest, "_")
	if lastUnderscore <= 0 || lastUnderscore == len(rest)-1 {
		return schemaInfo{}, false
	}
	nanoStr := rest[lastUnderscore+1:]
	head := rest[:lastUnderscore]

	secondUnderscore := strings.LastIndex(head, "_")
	if secondUnderscore <= 0 || secondUnderscore == len(head)-1 {
		return schemaInfo{}, false
	}
	pidStr := head[secondUnderscore+1:]
	namespace := head[:secondUnderscore]

	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return schemaInfo{}, false
	}
	nano, err := strconv.ParseInt(nanoStr, 10, 64)
	if err != nil || nano <= 0 {
		return schemaInfo{}, false
	}
	if namespace == "" {
		return schemaInfo{}, false
	}
	return schemaInfo{Name: name, Namespace: namespace, PID: pid, CreatedUnixNano: nano}, true
}

// listTestSchemas 列出库里所有 `test_*` schema 并解析。
//
// 解析失败的也收进来（Name 保留）：shouldReapSchema 会因 PID<=0 拒绝它，
// 而保留名字让日志与调试能对账「库里到底有几个 test_*」。
func listTestSchemas(base *gorm.DB) []schemaInfo {
	if base == nil {
		return nil
	}
	// 前缀清单与 schemaPrefixes **同源**：两处各写一份清单，早晚漂移
	// （症状是「某类 schema 永远不被回收」，而回收器本身看起来工作正常）。
	//
	// LIKE 的 _ 是通配符，所以前缀里的下划线要转义成 \_。
	conds := make([]string, 0, len(schemaPrefixes))
	for _, p := range schemaPrefixes {
		conds = append(conds, "nspname LIKE '"+strings.ReplaceAll(p, "_", "\\_")+"%'")
	}
	rows, err := base.Raw(
		"SELECT nspname FROM pg_namespace WHERE " + strings.Join(conds, " OR ")).Rows()
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []schemaInfo
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		info, ok := parseSchemaName(name)
		if !ok {
			info = schemaInfo{Name: name} // 保留名字，供日志与调试
		}
		out = append(out, info)
	}
	return out
}

// reloadSchemaInfo 在 DROP 前**重新确认**一个候选仍然存在（两段式的第二段）。
//
// 返回 ok=false 表示查不清（查询失败 / schema 已不存在）⇒ 调用方不动它。
func reloadSchemaInfo(base *gorm.DB, name string) (schemaInfo, bool) {
	var count int64
	if err := base.Raw("SELECT count(*) FROM pg_namespace WHERE nspname = ?", name).
		Scan(&count).Error; err != nil || count == 0 {
		return schemaInfo{}, false
	}
	info, ok := parseSchemaName(name)
	if !ok {
		info = schemaInfo{Name: name}
	}
	return info, true
}

// currentNamespace 返回本进程所在环境的标识，**仅用于给人看**（不参与判活）。
//
// 用 hostname：容器里它是容器 ID 的前 12 位，非容器环境是主机名。
// 两者都能帮人认出「这批 schema 是哪台机器/哪个容器留下的」，
// 但**都不能用来判活** —— 容器 ID 每次 docker run 都变，
// 拿它做判据会让回收器变成永久 no-op。
func currentNamespace() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return ""
	}
	return sanitizeSchemaSegment(h)
}

// sanitizeSchemaSegment 把任意标识符收敛成可安全嵌入 schema 名的形式。
//
// 只保留 [a-z0-9]，其余字符转 '_'，并截断到 32 字符：
//   - schema 名有长度限制（PG 63 字节），而名字里还要塞 pid 与 nano；
//   - 大写会变成需要引号的标识符，我们全程不加引号（见 buildSchemaName 注释）。
func sanitizeSchemaSegment(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(b) < 32; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b = append(b, c)
		case c >= 'A' && c <= 'Z':
			b = append(b, c-'A'+'a')
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}

// buildSchemaName 生成隔离 schema 名：test_<namespace>_<pid>_<unixnano>。
//
// 三段各有职责，且**只有 pid/nano 参与格式校验与年龄判定**：
//
//	namespace : 纯人读（哪个环境建的），不参与判活
//	pid       : 纯人读 + 格式校验（排查「哪个进程留下的」）
//	nano      : 年龄兜底的输入
//
// 活主由连接上的 application_name 表达（见 reapStaleSchemas），与这个名字无关。
//
// **全程不加引号**：只由 [a-z0-9_] 组成，PG 会把它当普通标识符。
// 若有人塞进大写/特殊字符，会变成需要引号的标识符，而本文件的 SQL 一律
// 直接拼接（DROP SCHEMA %s CASCADE）—— 那就是注入面。sanitizeSchemaSegment
// 保证不会发生。
func buildSchemaName(namespace string, pid int, now time.Time) string {
	return fmt.Sprintf("test_%s_%d_%d", sanitizeSchemaSegment(namespace), pid, now.UnixNano())
}

// processAlive 判断 pid 是否仍存在（signal 0 只做权限/存在性检查，不真的发信号）。
//
// ⚠ 它只在**同一 pid 命名空间内**有意义。调用方必须先确认 schema 名的
// namespace 段与 currentNamespace() 一致（shouldReapSchema 规则 ②），
// 否则这个判定回答的是「另一个命名空间里的同名 pid 是否存在」，毫无意义。
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

	// ── 先定 schema 名，再建任何连接 ──
	//
	// 顺序很重要：base 连接必须**从第一天起**就带 application_name=<schemaName>，
	// 否则「CREATE SCHEMA 成功」到「标记连接建立」之间有一个窗口，
	// 回收器会在那个窗口里看到「有 schema 但无活主」⇒ 可能被别的进程回收。
	// 把 application_name 放进第一条 DSN，这个窗口就不存在。
	schemaName := buildSchemaName(currentNamespace(), os.Getpid(), time.Now())

	// First connection: create the isolated schema.
	//
	// application_name 是**活主判据**（见 reapStaleSchemas 的文件头）：
	// PG 的 pg_stat_activity 会记录每条连接的这个字段，进程死了连接就断开、
	// 计数归零 —— 这正是「这个 schema 还有没有人在用」的正确判据，
	// 且跨 pid 命名空间、跨容器、跨机器都成立。
	baseDSN := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable application_name=%s",
		host, port, user, pass, dbname, schemaName)

	base, err := gorm.Open(postgres.Open(baseDSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open postgres (base): %v (dsn: host=%s port=%s user=%s dbname=%s)", err, host, port, user, dbname)
	}

	// 回收上一次被 SIGKILL/SIGPIPE 打断留下的 schema（见 reapStaleSchemas 的说明）。
	// 放在建新 schema **之前**：这样残留在本轮就被清掉，而不是滚雪球。
	reapStaleSchemas(base)

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

	// ============ search_path **只放隔离 schema，不放 public**（2026-11-08）============
	//
	// 原写法是 `search_path=<schema>,public`。那半个 `public` 是历史惰性，
	// 而且它是**一整类事故的根源**：隔离 schema 里缺某张表时，查询会**静默
	// 命中 public 里的同名表**，于是症状变成 42P07「表已存在」/ 42P16「列在主键里」
	// 这类完全指错方向的错误（2026-11-08 就发生过：public 被一个跑 37 小时的
	// dev 容器塞满 67 张表，全量 PG 套件红成一片）。
	//
	// 去掉 public 之后，「缺表」就是 ERROR: relation "x" does not exist ——
	// **每条查询都强制隔离**，不再依赖某一次开库检查。
	//
	// 前提已核实（2026-11-08）：
	//   · 重建后的 ehome_test 里 public 对象数 = 0；
	//   · 库里的 extension 只有 plpgsql @ pg_catalog，不在 public；
	//   · 全仓没有真执行的 `public.xxx` SQL（只有注释与测试夹具里的说明文字）。
	//
	// ⚠ 后人若想加回 `public`：先确认上面三条仍然成立，并想清楚为什么
	//    「隔离 schema 缺表时要能读到 public 的表」—— 那通常说明测试依赖了
	//    public 里的残留，而那些残留本身就是别人的 bug。
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable application_name=%s search_path=%s",
		host, port, user, pass, dbname, schemaName, schemaName)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(gormlogger.Silent),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open postgres: %v (dsn: host=%s port=%s user=%s dbname=%s schema=%s)", err, host, port, user, dbname, schemaName)
	}

	// ============ 守卫 A：隔离必须真的生效（2026-11-08）============
	//
	// 为什么必须有这条：search_path 是 DSN 里一个**字符串**，它是否被驱动正确
	// 应用、是否被连接池里某条旧连接覆盖，都是运行期事实。一旦它没生效，
	// 所有建表/DROP 都会落到 public —— 而**症状完全不像「隔离失效」**：
	//
	//	observed: relation "device_configs" already exists (42P07)
	//	          column "timestamp" is in a primary key (42P16)
	//
	// 这两类错误把人引向「AutoMigrate 逻辑有问题 / 模型定义冲突」，
	// 而真正原因是「本用例的 schema 根本没用上」。2026-11-08 就发生过一次
	// （public 被一个跑 37 小时的 dev 容器塞满 67 张表）。
	//
	// 所以建表**之前**先断言 current_schema()：不等就立刻 fail-fast，
	// 并说清「隔离失效 + 请检查 search_path」，把一堆无关报错收敛成一句话。
	if got := currentSchemaOf(t, db); got != schemaName {
		hint := ""
		if got == "public" {
			hint = "\n\t⚠ 落到 public 说明 search_path 没生效；" +
				"若 public 里有残留表（例如有人用生产形状跑过这个库），先清理它。"
		}
		t.Fatalf("PG 测试隔离失效：current_schema() = %q，期望 %q。\n\t"+
			"这会让本用例的建表/删表打到 public，产生 42P07/42P16 等**看似无关**的错误。%s",
			got, schemaName, hint)
	}

	// ============ 守卫 B：public 必须是空的（2026-11-08）============
	//
	// 为什么必须有这条：public 是「不设 search_path 时表默认落到的地方」，
	// 也就是说**任何绕过隔离的写入都会落在这里**。它非空就等于「有东西越界了」。
	//
	// 而这条信号只有在 public 恒空时才可信 —— 2026-11-08 之前它长期非空
	// （被一个跑 37 小时的 dev 容器塞满 67 张表），于是全量 PG 套件红成一片时，
	// 没有任何人能从「public 有表」这条线索看出问题。
	//
	// 顺序很重要：**先让 public 恒空，再加这条断言**。反过来做会先得到一个
	// 天天红的断言，然后被迫加例外，然后就回到「规则有裂缝」的老路
	// （dbschema 包已改为用自己的 dbschema_<pid> schema，不再占 public）。
	//
	// 断言只针对**业务表**（relkind in ('r','p')）：public 里的 extension、
	// 类型、函数一概不管 —— 它们不是「越界写入」的证据。
	assertPublicSchemaEmpty(t, db)

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

// assertPublicSchemaEmpty 断言测试库的 public schema 里**没有业务表**。
//
// ============ 为什么这条断言值得存在 ============
//
// public 是「不设 search_path 时表默认落到的地方」。所以它非空 = **有东西绕过了隔离**。
// 这条信号只有在 public 恒空时才可信 —— 而 2026-11-08 之前它长期非空
// （被一个跑 37 小时的 dev 容器塞满 67 张表），于是全量 PG 套件红成一片时，
// 没有任何人能从「public 有表」这条线索看出问题。
//
// ============ 为什么只查 relkind in ('r','p') ============
//
// 「public 非空」的准确含义是「public 里有**业务表**」，而不是「public 一个对象都没有」：
//
//	· extension 装在 pg_catalog（不在 public），无需管；
//	· 类型/函数/序列可能是某个 extension 正常创建的；
//	· 只有 **表**（r=普通表, p=分区母表）才是「有人往这里建表」的证据。
//
// 把判据放宽到「一个对象都没有」会误伤合法场景，然后就会有人加例外 ——
// 而例外是这条守卫唯一的失效方式。
// schemaGuardTB 是 assertPublicSchemaEmpty 需要的**最小**接口。
//
// ============ 为什么要抽接口而不是直接收 *testing.T ============
//
// 守卫的关键行为是「发现越界就 Fatal」。而 Go 里「期望一次 Fatal」无法用
// t.Run 表达 —— 子测试失败会**连带父测试失败**（实测：收到的正是
// `--- FAIL: TestPublicSchemaGuard_CatchesStrayTable/guard_fires`，父测试也跟着红）。
//
// 所以把「报告失败」抽成接口：生产路径传 *testing.T（真 Fatal），
// 验证路径传一个只记录消息的 fake。这样「守卫会不会触发」变成**可断言的事实**，
// 而不是「跑一次看看会不会红」。
//
// ⚠ 这是本仓对「可执行验证」的一贯做法：判据要么能被断言，要么就没人知道它还在不在。
type schemaGuardTB interface {
	Helper()
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
}

func assertPublicSchemaEmpty(t schemaGuardTB, db *gorm.DB) {
	t.Helper()
	type row struct {
		Name string
		Kind string
	}
	var rows []row
	if err := db.Raw(
		"SELECT c.relname AS name, c.relkind::text AS kind " +
			"FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace " +
			"WHERE n.nspname = 'public' AND c.relkind IN ('r','p') ORDER BY c.relname").
		Scan(&rows).Error; err != nil {
		// 查不到只 Warn 不 Fatal：守卫本身出错不该让所有测试红
		// （与 reapStaleSchemas「清理失败只忽略」同一取向）。
		t.Logf("⚠ 无法检查 public schema 是否为空（守卫失效）: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	t.Fatalf("测试库的 public schema 里有 %d 张业务表：%v\n\t"+
		"public 是「不设 search_path 时表默认落到的地方」——它非空说明**有东西绕过了隔离**。\n\t"+
		"处置：\n\t"+
		"  1) 若这些表是某次污染的残留：确认无人使用后 DROP 掉；\n\t"+
		"  2) 若某包/脚本确实需要持久化的表：给它自己的 schema（形如 dbschema_<pid>），\n\t"+
		"     并让 testutil 的回收器认识那个前缀 —— **不要**占用 public；\n\t"+
		"  3) 若某进程正用生产形状跑这个库（dev 容器 / 手工启动的服务）：先停掉它。\n\t"+
		"为什么必须挡住：「public 非空 = 有东西越界」这条信号只有在 public 恒空时才可信。",
		len(rows), names)
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

// currentSchemaOf 读取连接的 current_schema()（守卫 A 的实现）。
//
// 抽成函数是为了**可注入验证**：测试可以用一个故意不带 search_path 的 DSN
// 打开连接，断言这里返回 "public" —— 即「守卫真的能发现问题」，
// 而不是只证明了「在正常情况守卫不误报」。
func currentSchemaOf(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var s string
	if err := db.Raw("SELECT current_schema()").Scan(&s).Error; err != nil {
		t.Fatalf("读 current_schema() 失败: %v", err)
	}
	return s
}
