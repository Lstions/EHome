# 帧表 vs unified_data 逐日对账（2026-11-07）

> 目的：回答「删掉老 `unified_data`（12 GB）会不会丢数据」。

---

## 0. 结论

**不会丢。10 月每一天每一设备的帧数完全对齐（差异 0）。**
11 月帧表反而更多 —— 因为 L2 是实时主路径而 `unified_data` 受 L1 门控。

---

## 1. 对账方法

```sql
WITH u AS (SELECT date_trunc('day',"timestamp")::date dt, device_id, count(DISTINCT "timestamp") frames
           FROM unified_data GROUP BY 1,2),
     f AS (SELECT date_trunc('day',ts)::date dt, device_id, count(*) frames
           FROM telemetry_frame GROUP BY 1,2)
SELECT ..., coalesce(f.frames,0) - coalesce(u.frames,0) AS diff
FROM u FULL OUTER JOIN f ON u.dt=f.dt AND u.device_id=f.device_id
WHERE diff <> 0;
```

**必须 `FULL OUTER JOIN` + 逐日逐设备**：
随便挑一个时间窗口验证会得出完全错误的结论 —— 我第一次挑了 10-15 02:00，结果 0 点，
查库才发现那天 device14 本来就离线（两张表都是 0 行）。**读路径没坏，是我选错了窗口。**

---

## 2. 结果

**只有 11-06 / 11-07 有差异，10 月全部为 0：**

| 日期 | 设备 | unified 帧 | frame 帧 | 差 |
|---|---:|---:|---:|---:|
| 2026-11-06 | 14 | 85,726 | 86,142 | +416 |
| 2026-11-06 | 15 | 52,200 | 55,946 | **+3,746** |
| 2026-11-07 | 14 | 34,592 | 35,671 | +1,079 |
| 2026-11-07 | 15 | 18,600 | 23,182 | **+4,582** |

总计：`unified_data` distinct 帧 3,468,898 / `telemetry_frame` 3,478,769（多 9,871）。

---

## 3. 为什么 11 月有正差 —— 正确行为，不是缺陷

```
L2（帧表）从 2026-11-06 19:24 起是实时主路径：每解析一帧写一行
unified_data 受 L1 门控：值不变且未到心跳 ⇒ 不写
```

⇒ **帧表是 `unified_data` 的超集**，差额正是「L1 跳过但帧表仍记录」的帧。

数据完整性角度：帧表 ⊇ unified_data。

---

## 4. 但删之前还有一道关：读路径

| 窗口 | 数据源 | 删 unified 后 |
|---|---|---|
| ≤1h | 帧表解码 | ✅ 不受影响 |
| 1h ~ 6h | unified_data SQL | ❌ 会缺 |
| >6h | rollup（当前开关关闭）→ 回落 SQL | ❌ 会缺 |

⇒ 必须先完成 **rollup 历史回填**（task-22）并验证 1d/7d/30d 图表，才能 DROP。

---

## 5. 附带确认

读路径在 10 月数据上工作正常：
```
10 月 7d 窗口 → 60 点 / 0.34s / min=49.09 max=54.40
```
（1h / 24h 窗口返回 0 点是因为那两个窗口落在设备离线区间，不是缺陷。）
