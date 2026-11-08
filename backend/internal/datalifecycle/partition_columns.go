package datalifecycle

// partition_columns.go — 分区母表的**列校正**（2026-11-07 生产事故的根治）。
//
// ============ 事故经过 ============
//
// telemetry_rollup_1m 的分区母表由 partitionParentDDL 的裸 DDL 建，而
// models.TelemetryRollup1m 多一个 UpdatedAt 列。生产实测每分钟：
//
//	ERROR: column "updated_at" of relation "telemetry_rollup_1m" does not exist (SQLSTATE 42703)
//
// 结果 L3 rollup 写入**全线失败**（表 0 行）。而 SQLite 单测走 AutoMigrate
// （模型字段一定被建出来）⇒ **用例全绿**。
//
// 这是最危险的一类假绿：测试覆盖了「读写语义」，却没覆盖「schema 本身」。
//
// ============ 为什么做成通用校正器而不是给 rollup 打补丁 ============
//
// 只给 rollup 的 DDL 补一列治标：
//   - 下次给任何一张分区表加列会再炸一次；
//   - 而且「存量库已按旧 DDL 建好」这个事实不会因为改模板而改变
//     （改模板只影响新建的库，生产库早就建好了）。
//
// 因此：
//   - 真源 = partitionParentDDL（与建表路径同一份模板，不引入第二处列清单）；
//   - 逐列对比 information_schema.columns；
//   - 缺失的列发 ALTER TABLE ... ADD COLUMN IF NOT EXISTS <原样列定义>。
//
// 四张分区表（unified_data / device_data / telemetry_rollup_1m / telemetry_frame）
// 全部受益，存量生产表启动时自动补列。
//
// PG 11+ 对分区母表 ADD COLUMN 是**元数据操作**并自动传播到全部分区，秒级、
// 不重写数据；带 DEFAULT 时存量行按默认值读出（PG 11+ fast default）。
//
// ============ ⚠ detached 分区会漏补（Lead 2026-11-07 点名，必须知情）============
//
// ADD COLUMN 的传播是**继承**语义：只有仍然 ATTACH 在母表上的分区会跟着加列。
// 若某个分区因历史原因被 DETACH（人为操作、迁移中断、PG 早期版本的手工维护），
// 它**不会**被本函数补到 —— 它已经不是母表的一部分了。
//
// 本函数**刻意不遍历 detached 分区**，三条理由：
//   1. detached 分区对母表查询不可见，补列不会让任何读路径变好；
//   2. 盲目补列/重新 ATTACH 可能把一张**形状不对的旧表**挂回母表（那是数据事故，
//      比缺列严重得多），而本函数无法判断那张表到底是什么；
//   3. 处置是明确的运维动作：要么它是废弃残留（DROP 即可），要么需要 ATTACH 回来
//      （此时母表已有该列，ATTACH 会校验形状并报出差异）。
//
// 怀疑有 detached 分区时，用下面的查询核对（inhparent 为空且名字符合 <table>_YYYYMM）：
//
//	SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
//	WHERE n.nspname = current_schema() AND c.relname LIKE '<table>\_%'
//	  AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid);
//
// ============ 失败语义：Warn 不阻断（Lead 2026-11-07 裁决）============
//
// 补列失败**不该让服务起不来**：它修的是「schema 漂移」，而漂移的后果是某张表写入
// 失败，不是服务不可用。挡启动等于把小故障放大成停机。
//
// 真正的拦截点在开发期：TestPartitionDDLMatchesModels 是**纯源码门禁**（不需要 PG、
// 不依赖任何环境变量），DDL 与 model 一漂移就在 CI 红，不用等生产。
// 两者分工：源码门禁挡住「新写的漂移」，本校正器兜住「已经漂移出去的存量库」。
//
// 边界（诚实声明）:
//   - 只**加列**，不删列、不改类型。改类型在分区表上代价高且方向不确定
//     （是模板错了还是模型错了？），交由人判断；
//   - 依赖 DDL 模板可解析（每行一个列定义）。解析失败**不静默通过**：返回 error
//     让调用方 Warn —— 解析器静默失效会让本函数变成永远的安全假象。

import (
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"
)

// ensurePartitionParentColumns 把 partitionParentDDL 里声明、而库里缺失的列补到
// 已存在的分区母表上（幂等；非 PG 方言 no-op）。
func ensurePartitionParentColumns(db *gorm.DB, table string) error {
	if !isPostgres(db) {
		return nil // SQLite 由 AutoMigrate 建表，schema 与模型天然一致
	}
	ddl, ok := partitionParentDDL[table]
	if !ok {
		return nil // 未登记表：createPartitionedParent 会回退到 unified_data，列校正无从谈起
	}
	want, err := parseDDLColumns(ddl)
	if err != nil {
		return fmt.Errorf("parse partitionParentDDL[%s]: %w", table, err)
	}
	if len(want) == 0 {
		return fmt.Errorf("parse partitionParentDDL[%s]: parsed 0 columns (template format changed? refusing to pass silently)", table)
	}
	have, err := existingColumns(db, table)
	if err != nil {
		return err
	}
	for _, col := range want {
		if have[col.Name] {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s", table, col.Name, col.Def)
		slog.Warn("partition_mgr: partitioned parent is missing a column declared in partitionParentDDL, adding it "+
			"(schema drift between bare DDL and models — this is the 2026-11-07 rollup updated_at class of bug)",
			"table", table, "column", col.Name, "statement", stmt)
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("add missing column %s.%s: %w", table, col.Name, err)
		}
	}
	return nil
}

// ddlColumn 是 partitionParentDDL 里的一列：列名 + 原样保留的定义文本。
//
// Def 保留 "NOT NULL DEFAULT now()" 这类后缀（ADD COLUMN 需要完整定义，不能只给类型）。
// PRIMARY KEY 由建表语句的独立子句声明，进不了本结构（parseDDLColumns 会跳过表级约束）。
type ddlColumn struct {
	Name string
	Def  string
}

// parseDDLColumns 从 CREATE TABLE 模板里抽出列定义（跳过 PRIMARY KEY 等表级约束）。
//
// 只支持本仓 partitionParentDDL 的实际写法（每行一列），不做通用 SQL 解析 ——
// 但**解析不出来就返回 error**（见文件头的「不要静默通过」）。
func parseDDLColumns(ddl string) ([]ddlColumn, error) {
	open := strings.IndexByte(ddl, '(')
	if open < 0 {
		return nil, fmt.Errorf("no column list found in DDL")
	}
	// 必须按**括号配对**找到第一个 '(' 对应的 ')'，不能用 LastIndexByte：
	// 模板尾部还有 "PARTITION BY RANGE (bucket)"，取最后一个 ')' 会把
	// "bucket)" 之类也算进列清单（实测踩过，报 unexpected column definition "ts"）。
	depth := 0
	closeIdx := -1
	for i := open; i < len(ddl); i++ {
		switch ddl[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				closeIdx = i
			}
		}
		if closeIdx >= 0 {
			break
		}
	}
	if closeIdx <= open {
		return nil, fmt.Errorf("unbalanced parentheses in DDL")
	}
	body := ddl[open+1 : closeIdx]
	var out []ddlColumn
	// 只能按**顶层**逗号切分：`PRIMARY KEY (id, timestamp)`、`VARCHAR(16)` 里的逗号
	// 不是列分隔符。实测踩过：直接 strings.Split(body, ",") 会把 "PRIMARY KEY (id"
	// 与 " timestamp)" 切成两块 —— 前者被关键词检查跳过，后者报
	// `unexpected column definition "timestamp)"`。
	for _, raw := range splitTopLevelCommas(body) {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		// 表级约束：PRIMARY KEY / FOREIGN KEY / CHECK / UNIQUE / CONSTRAINT / EXCLUDE。
		skip := false
		for _, kw := range []string{"PRIMARY KEY", "FOREIGN KEY", "CHECK", "UNIQUE", "CONSTRAINT", "EXCLUDE"} {
			if strings.HasPrefix(upper, kw) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("unexpected column definition %q", line)
		}
		out = append(out, ddlColumn{
			Name: strings.ToLower(fields[0]),
			Def:  strings.Join(fields[1:], " "),
		})
	}
	return out, nil
}

// splitTopLevelCommas 按**不在括号内**的逗号切分（SQL 列定义的标准切法）。
//
// 为什么必须这样切：本仓 DDL 里有 `PRIMARY KEY (id, timestamp)`、`VARCHAR(16)`
// 这类含括号/逗号的定义。朴素 strings.Split(body, ",") 会把表级约束切成两半，
// 后半截（如 ` timestamp)`）被当成列定义，报 `unexpected column definition`。
func splitTopLevelCommas(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// existingColumns 返回 table 当前在库里的列名集合（小写）。
func existingColumns(db *gorm.DB, table string) (map[string]bool, error) {
	var names []string
	if err := db.Raw(
		"SELECT column_name FROM information_schema.columns "+
			"WHERE table_schema = current_schema() AND table_name = ?", table,
	).Scan(&names).Error; err != nil {
		return nil, fmt.Errorf("list columns of %s: %w", table, err)
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[strings.ToLower(n)] = true
	}
	return out, nil
}
