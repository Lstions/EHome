# DROP 后重启会重建 unified_data —— 实测证据（2026-11-08）

## 结论

**用当前生产镜像 `ehome-new:main-afd66df1` 执行 `DROP TABLE unified_data` 后重启，表会复活。**

复原路径已定位到**具体一行**（不是推测）：

```
afd66df1 的 backend/cmd/server/main.go:183-188
    startupTracker.Begin(bootstrap.StepPartitionUnifiedData)
    if err := datalifecycle.MigrateUnifiedDataToPartitioned(db); err != nil {
        startupTracker.Fail(...)
    }
    startupTracker.Done(...)
```

`MigrateUnifiedDataToPartitioned` → `MigrateTableToPartitioned` 的判定链：
```
IsTablePartitioned(relkind='p')  → 否（表不存在）
flatExists(relkind='r')          → 否（表不存在）
⇒ 「原表不存在（全新部署）」分支 → createPartitionedParent → CREATE TABLE ... PARTITION BY RANGE
```

即：**「表不存在」被解释成「全新部署」**，于是重建一张 5 个月分区、0 行的空表。

## 实测过程（scratch 库 `ehome_droptest`，主机 127.0.0.1，不影响生产）

```
=== 1. 首次启动 ===
[first] readyz=200
INFO migrate_partitioned: no legacy flat table, creating fresh partitioned parent table=unified_data
INFO partition_mgr: created partition unified_data_202610 ... 202702

=== 2. DROP TABLE unified_data CASCADE ===
DROP TABLE
exists_after_drop=0

=== 3. 第二次启动 ===
[second] readyz=200
INFO migrate_partitioned: no legacy flat table, creating fresh partitioned parent table=unified_data   ← 复活
INFO partition_mgr: created partition unified_data_202610 ... 202702

exists_after_restart=1        ← 必须为 0 的判据，实测为 1
```

## 关键点：**当前工作树已经修好了这条路径，但还没部署**

```
工作树 backend/cmd/server/main.go:189-191
    //	startupTracker.Begin(bootstrap.StepPartitionUnifiedData)
    //	if err := datalifecycle.MigrateUnifiedDataToPartitioned(db); err != nil { ... abortStartup }
    //	startupTracker.Done(bootstrap.StepPartitionUnifiedData, "unified_data 分区就绪")
```

`bootstrap/steps.go` 的 `StepPartitionUnifiedData` 也已移除，权重重新配平到 100。

**所以 DROP 的前置条件不是「数据层安全」，而是三件事同时成立：**

1. 数据层：帧表覆盖 unified_data 全部采样时刻 —— **已实测通过**（33 个日全 OK，见 DROP门禁实测）
2. 代码层：13 处生产读点清干净 —— **进行中（task-31）**
3. **部署层：含 ⑦ 的镜像已上生产并验证 restart 不重建** —— **未完成**

只做第 1 条就 DROP，会得到：表被重建、旧数据永久丢失、新表永远空 ——
**磁盘没省，数据没了，而且看起来一切正常**（这正是 bootstrap/doc.go:36 早就写下的判断）。

## 判据（必须可执行为命令）

部署后执行，两项都要满足才允许 DROP：

```bash
# ① 重启前 unified_data 存在（有数据）
# ② DROP 后重启
# ③ 重启后必须为 0
psql -Atc "select count(*) from pg_class c join pg_namespace n on n.oid=c.relnamespace
           where n.nspname='public' and c.relname='unified_data'"
```

**只验证「重启后服务 ready」不算。** readyz=200 在「表被重建」和「表不存在」两种情况下都成立 ——
这是本次实测最直接的反例：两次启动 readyz 都是 200。
