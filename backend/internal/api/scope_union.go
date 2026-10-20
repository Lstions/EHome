package api

// PostgreSQL 走索引的关键前提 (BMS 页慢修复-实施契约-2026-10-20 §1.6,
// 2026-10-20 22:57 verifier 在 22.4M 行 ehome_bench 上实测追加):
//
// dataScopeCond 生成的 OR 条件
//
//	(logical_device_id IN (...) OR (logical_device_id IS NULL AND device_id IN (...)))
//
// 把 logical_device_id 从"索引前导列的等值/IN 条件"降级成 Filter —— PostgreSQL
// 无法据此选择 (logical_device_id, sensor_name, timestamp) /
// (logical_device_id, created_at DESC) 复合索引, 于是:
//   - 24h 单测点 avg 仍 Parallel Index Scan timestamp_idx, 每 worker
//     Rows Removed by Filter 806,400;
//   - protection 30d change 仍 sensor_name bitmap + lossy recheck 556 万行 + 外部排序;
//   - latest-data 仍 Parallel Seq Scan device_data 200 万行。
//
// 修法: 把 scope 拆成两个**互斥且各自带干净前导列**的分支, 再 UNION ALL:
//
//	A: logical_device_id IN (...)
//	B: logical_device_id IS NULL AND device_id IN (...)
//
// 分支 A 可走 (logical_device_id, sensor_name, timestamp) / (logical_device_id,
// created_at DESC); 分支 B 的前导条件是 logical_device_id IS NULL, 同样是 btree
// 前导列的可索引条件 (backfill 完成后分支 B 实际 0 行, 索引扫描立即结束)。
//
// 只有一种 scope (仅 LogicalIDs / 仅 InstanceIDs / backfill 前的 device_id 回退)
// 时只生成单分支, 不拼 UNION。SQLite 永远不会进入本文件 (调用方先做方言判断),
// 旧路径 (dataScopeCond + OR) 仍服务 SQLite 与 agg=raw。

import (
	"fmt"
	"strings"

	"ehome/backend/internal/datalifecycle"
)

// scopeBranch is one index-friendly branch of the data scope condition.
type scopeBranch struct {
	cond string
	args []interface{}
}

// dataScopeBranches splits a resolved data query scope (§六) into UNION ALL
// branches, each carrying an indexable leading condition:
//
//   - logical branch:  logical_device_id IN (LogicalIDs)
//   - fallback branch: logical_device_id IS NULL AND device_id IN (InstanceIDs)
//
// 语义与 datalifecycle.Scope.Cond() 等价 (两者互斥, AND/OR 分配律展开), 但
// 每支的前导列都是干净的索引条件。backfill 前的旧数据 (无逻辑身份) 保持
// dataScopeCond 的 device_id 回退语义。
func dataScopeBranches(qs *datalifecycle.DataQueryScope) []scopeBranch {
	if qs == nil {
		return nil
	}
	if qs.LogicalID == 0 || qs.Scope == nil {
		// 兼容 backfill 前旧数据 (与 dataScopeCond 的回落分支一致)。
		return []scopeBranch{{cond: "device_id = ?", args: []interface{}{qs.FallbackDeviceID}}}
	}
	branches := make([]scopeBranch, 0, 2)
	if len(qs.Scope.LogicalIDs) > 0 {
		branches = append(branches, scopeBranch{
			cond: "logical_device_id IN ?",
			args: []interface{}{qs.Scope.LogicalIDs},
		})
	}
	if len(qs.Scope.InstanceIDs) > 0 {
		branches = append(branches, scopeBranch{
			cond: "logical_device_id IS NULL AND device_id IN ?",
			args: []interface{}{qs.Scope.InstanceIDs},
		})
	}
	return branches
}

// historyUnionSQL renders the UNION ALL scope source for unified_data with the
// given SELECT list and an optional per-branch extra condition:
//
//	(SELECT <columns> FROM unified_data WHERE A [AND extra] [branchOrder]
//	 UNION ALL
//	 SELECT <columns> FROM unified_data WHERE B [AND extra] [branchOrder])
//
// branchOrder is used by change 模式: 每支各自 ORDER BY "timestamp" 后, PG 用
// Merge Append 把有序输入喂给 WindowAgg, 避免把 697k 行做 external merge sort
// (bench 实测 execution 459ms -> 358ms, 无临时文件)。avg 模式不需要。
//
// It returns "" when the scope matches no rows (callers then return an empty
// result without touching the database). Args follow the exact placeholder
// order of the returned SQL: for each branch, branch args then extra args.
func historyUnionSQL(qs *datalifecycle.DataQueryScope, columns, extraCond string, extraArgs []interface{}, branchOrder string) (string, []interface{}) {
	branches := dataScopeBranches(qs)
	if len(branches) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(branches))
	args := make([]interface{}, 0, len(branches)*(1+len(extraArgs)))
	for _, branch := range branches {
		where := branch.cond
		if extraCond != "" {
			where += " AND " + extraCond
		}
		part := fmt.Sprintf("SELECT %s FROM unified_data WHERE %s", columns, where)
		if branchOrder != "" {
			// UNION 分支内的 ORDER BY 必须放在括号里, 否则 PG 语法错误
			// (ORDER BY 会被解析成整个 union 的排序, 后面的 UNION ALL 非法)
			// ⇒ batch 层静默返回空 data (实测 30d protection 返回 79 B 空数组)。
			part = "(" + part + " " + branchOrder + ")"
		}
		parts = append(parts, part)
		args = append(args, branch.args...)
		if extraCond != "" {
			args = append(args, extraArgs...)
		}
	}
	return "(" + strings.Join(parts, " UNION ALL ") + ")", args
}

// historyAggregateSQL renders the complete avg/change SQL on top of the
// two-branch scope source. widthSeconds is inlined as an integer literal (it is
// derived from int64/int inputs only) so the placeholder order stays
// unambiguous: bucket expression then branch A then branch B.
func historyAggregateSQL(q historyPointQuery, widthSeconds int64) (string, []interface{}) {
	// DedupNeeded 需要 id (MAX(id) 胜出) 与 sensor_name (分区键) 先取出来,
	// 去重必须在 union 之后、聚合之前 (契约 §1.3/§1.6)。
	columns := `"timestamp", "value"`
	if q.DedupNeeded {
		columns = `"timestamp", "value", "id", "sensor_name"`
	}
	// change 模式给每支加 ORDER BY "timestamp": WindowAgg 走 Merge Append 有序
	// 输入, 免掉 30d protection 的 external merge sort (Lead bench 实测)。
	branchOrder := ""
	if q.Mode == AggModeChange {
		branchOrder = `ORDER BY "timestamp"`
	}
	union, args := historyUnionSQL(q.Scope, columns,
		`sensor_name = ? AND "timestamp" BETWEEN ? AND ?`,
		[]interface{}{q.Category, q.Start, q.End}, branchOrder)
	if union == "" {
		return "", nil
	}
	if q.Mode == AggModeChange {
		return historyChangeSQL(union, q.DedupNeeded), args
	}
	return historyAvgSQL(union, q.DedupNeeded, widthSeconds), args
}

// historyAvgSQL builds the avg-mode aggregate on the scope source (契约 §1.2):
//
//	SELECT to_timestamp(floor(extract(epoch FROM "timestamp") / W) * W) AS "timestamp",
//	       avg("value") AS "value"
//	FROM <union> AS u
//	GROUP BY 1 ORDER BY 1
//
// With DedupNeeded the 保形去重 (ROW_NUMBER ... MAX(id)) wraps the union first,
// then avg runs on the deduplicated rows.
func historyAvgSQL(union string, dedupNeeded bool, widthSeconds int64) string {
	bucket := fmt.Sprintf(
		`to_timestamp(floor(extract(epoch FROM "timestamp") / %d) * %d) AS "timestamp", avg("value") AS "value"`,
		widthSeconds, widthSeconds)
	if dedupNeeded {
		return fmt.Sprintf(`SELECT %s FROM (
	SELECT "timestamp", "value", sensor_name,
	       ROW_NUMBER() OVER (PARTITION BY sensor_name, "timestamp" ORDER BY id DESC) AS dedup_rn
	FROM %s AS u
) AS d WHERE dedup_rn = 1 GROUP BY 1 ORDER BY 1`, bucket, union)
	}
	return fmt.Sprintf(`SELECT %s FROM %s AS u GROUP BY 1 ORDER BY 1`, bucket, union)
}

// historyChangeSQL builds the change-point query on the scope source
// (契约 §1.2/§1.6):
//
//	SELECT s."timestamp", s."value" FROM (
//	  SELECT d."timestamp", d."value",
//	         lag(d."value") OVER (ORDER BY d."timestamp") AS prev
//	  FROM <union> AS d
//	) AS s
//	WHERE s.prev IS DISTINCT FROM s."value"
//	ORDER BY s."timestamp"
//
// prev IS DISTINCT FROM value keeps the first point (prev NULL) and both
// directions of every transition, including 1->0 (the pulse end marker); no
// non-zero filtering is allowed here (契约 §1.2 明令).
func historyChangeSQL(union string, dedupNeeded bool) string {
	src := union
	if dedupNeeded {
		src = fmt.Sprintf(`(SELECT "timestamp", "value" FROM (
	SELECT "timestamp", "value", sensor_name,
	       ROW_NUMBER() OVER (PARTITION BY sensor_name, "timestamp" ORDER BY id DESC) AS dedup_rn
	FROM %s AS u
) AS dd WHERE dedup_rn = 1)`, union)
	}
	return fmt.Sprintf(`SELECT s."timestamp", s."value" FROM (
	SELECT d."timestamp", d."value",
	       lag(d."value") OVER (ORDER BY d."timestamp") AS prev
	FROM %s AS d
) AS s
WHERE s.prev IS DISTINCT FROM s."value"
ORDER BY s."timestamp"`, src)
}

// latestDeviceDataQuery is one ordered branch read of the PG latest-data
// two-stage query. Branch order matters: logical branch first (post-backfill
// rows are the newest), NULL-logical fallback branch only when the first
// returns nothing.
type latestDeviceDataQuery struct {
	// SQL selects at most one device_data row for one scope branch.
	SQL  string
	Args []interface{}
}

// latestDeviceDataQueries renders the PG latest-data reads (契约 §1.6, 2026-10-20
// 23:09 Lead 补充「两段式」): 先查 A 分支 LIMIT 1, 命中即止; 未命中再查 B 分支。
// 每支各自 `ORDER BY created_at DESC LIMIT 1`:
//
//   - A 分支走 (logical_device_id, created_at DESC) 复合索引;
//   - B 分支 (logical_device_id IS NULL) 走部分索引
//     idx_devdata_legacy_null_created。
//
// 旧实现是一条 OR scope 查询, PG 无法用任何前导列索引 ⇒ device_data 200 万行
// Parallel Seq Scan + top-N。两段式在 A 命中时零 B 成本 (B 在生产 backfill 完成
// 后恒 0 行; 新增行只写 A 分支且 created_at 更新, 故 A 命中即最新)。
func latestDeviceDataQueries(qs *datalifecycle.DataQueryScope) []latestDeviceDataQuery {
	branches := dataScopeBranches(qs)
	queries := make([]latestDeviceDataQuery, 0, len(branches))
	for _, branch := range branches {
		queries = append(queries, latestDeviceDataQuery{
			SQL: fmt.Sprintf(
				"SELECT * FROM device_data WHERE %s ORDER BY created_at DESC LIMIT 1", branch.cond),
			Args: branch.args,
		})
	}
	return queries
}
