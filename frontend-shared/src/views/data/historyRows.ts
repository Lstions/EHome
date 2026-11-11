/**
 * 数据面板「历史数据」表格的行构造（纯函数，与 SFC 分开以便单测）。
 *
 * ============ 为什么需要它（2026-11-11 原始数据退役）============
 *
 * 表格数据源从 `GET /edge-devices/:id/data` 换成了
 * `GET /devices/:id/history`（device_data 整表删除）。两者的**行语义不同**：
 *
 *   device_data 一行   = 一次采集（一帧解析出的全部物理量）
 *   UnifiedData  一行   = **一个字段**的一次采样
 *
 * 所以要把 N 行字段「转置」回一行采集。这一步是本页最容易写错的地方，
 * 放在 SFC 里只能靠渲染断言间接覆盖，故单独成模块。
 *
 * ============ 聚合键为什么是「timestamp 严格相等」而不是「邻近 2 秒」============
 *
 * 后端 SensorParserConsumer 对一次解析只取一个 `now := time.Now()`
 * （internal/databus/consumers_heavy.go:412），这一帧解析出的 N 个物理量
 * **共用同一个时间戳**写进帧表；`/devices/:id/history` 解码时又原样使用
 * 每帧的 ts（internal/api/history_source.go 的 HistoryPoint.Timestamp）。
 * 因此同一帧的 N 行 timestamp **逐字符相同**，严格相等是精确、无歧义的聚合键。
 *
 * 生产实测（2026-11-11，device 14）：连续帧的 ts 形如
 * `06:41:08.074838 / 06:41:07.068385 / 06:41:06.065424` —— 帧与帧之间差异在
 * 微秒级且**互不相同**，而同帧字段间差异为 0。
 *
 * 刻意**不**用「相差 < 2 秒合并」：BMS 等设备以 1Hz 连续上报，2 秒阈值会把
 * 两次真实采集并成一行，表格会莫名少行，且丢数据这件事完全不可见。
 *
 * 逆变器（Techfine，一个轮询周期 9 条命令、每条各是一次解析）会得到 9 组时间戳
 * ⇒ 表格里是 9 行。这是**正确**的：它确实发生了 9 次独立解析。
 */

import type { UnifiedDataRow } from '@/api/edgeDevice'

/** 聚合后的表格行：一次采集（同一时间戳的全部字段）。 */
export interface CollectedRow {
  /**
   * 该次采集里最小的后端行 id。
   *
   * 后端 id 由 (device_id, sensor_name, timestamp) 稳定哈希合成
   * （internal/api/device_history_frames.go 的 stableHistoryRowID），
   * 所以同一个采集跨请求拿到同一个 id —— 可直接当 v-for 的 key。
   */
  id: number
  /** 采集时刻（RFC3339Nano，原样保留以便展示时区换算） */
  timestamp: string
  /** 同一时刻（帧表路径下与 timestamp 相等） */
  created_at: string
  /**
   * 兼容字段：WS 推送（channel_data/data_update）与旧行可能带 collected_at。
   *
   * 历史行恒为 undefined（/devices/:id/history 没有这个键）。保留下沉到类型里，
   * 是因为展示层原本就用 `timestamp || created_at || collected_at` 三段兜底，
   * 删掉第三段会让"只有 collected_at 的实时行"时间显示成「—」。
   */
  collected_at?: string
  /** 指标名 → 读数。只收数值：`/devices/:id/history` 的 value 就是 float64 */
  data: Record<string, number>
}

/**
 * 把「一字段一行」的 UnifiedData 行聚合成「一次采集一行」。
 *
 * 返回值按 timestamp **倒序**（最新在前）—— 与改造前表格的
 * `ORDER BY created_at DESC` 一致；前端分页直接切片即可。
 *
 * 容错口径（规范 §3.2.5 不得以本地默认值伪造事实）：
 *   · 缺 timestamp 的行丢弃（无法定位到某次采集，硬塞会制造假采集）；
 *   · value 非有限数值的行丢弃（后端契约是 float64，出现非数值说明响应形状异常，
 *     此时宁可少一个指标，也不要把 NaN/undefined 渲染成 0）；
 *   · 同一次采集里同名字段出现多次 ⇒ 后者覆盖（后端一帧里一个字段只有一行）。
 */
export function aggregateHistoryRows(rows: UnifiedDataRow[]): CollectedRow[] {
  if (!Array.isArray(rows) || rows.length === 0) return []

  const byTimestamp = new Map<string, CollectedRow>()
  for (const row of rows) {
    if (!row || typeof row !== 'object') continue
    const timestamp = typeof row.timestamp === 'string' ? row.timestamp : ''
    const sensorName = typeof row.sensor_name === 'string' ? row.sensor_name : ''
    const value = typeof row.value === 'number' && Number.isFinite(row.value) ? row.value : null
    if (timestamp === '' || sensorName === '' || value === null) continue

    let bucket = byTimestamp.get(timestamp)
    if (!bucket) {
      bucket = {
        id: Number.isFinite(row.id) ? row.id : 0,
        timestamp,
        created_at: typeof row.created_at === 'string' && row.created_at !== '' ? row.created_at : timestamp,
        data: {},
      }
      byTimestamp.set(timestamp, bucket)
    } else if (Number.isFinite(row.id) && row.id < bucket.id) {
      // 同一次采集里 id 最小的那行当行 key（后端按 (ts, sensor_name) 排序，
      // sensor_name 升序时首行就是 id 最小者；显式取 min 以免排序口径变化）
      bucket.id = row.id
    }
    bucket.data[sensorName] = value
  }

  return [...byTimestamp.values()].sort((a, b) => (a.timestamp < b.timestamp ? 1 : a.timestamp > b.timestamp ? -1 : 0))
}

/**
 * 判断这次响应是否被服务端降采样过。
 *
 * ============ 为什么这个判断必须做 ============
 *
 * `/devices/:id/history` 的降采样是**按行**（一字段一行）抽的：
 * `step = 总行数 / max_points`，每隔 step 行取 1 行。行是一字段一行，所以
 * **抽样越狠，每条采集剩下的指标越少** —— step=42 时一条采集只剩 1 个指标，
 * 表格的「数据」列会显示成"只有 total_voltage"这种残行。
 *
 * 这不是前端的 bug，但**用户看不出来**：它长得和"设备只上报了一个字段"一模一样。
 * 所以必须在界面上明说"本表已降采样"，否则排障方向会被带偏。
 *
 * 判据：响应行数达到请求的 max_points（服务端正是"多到装不下才抽"）。
 * 允许 1% 的误差：服务端的选择规则是「首行 + 每 step 行 + 末行」，实际条数
 * 可能比 max_points 少一两个。
 */
export function looksDownsampled(rowCount: number, maxPoints: number): boolean {
  if (maxPoints <= 0) return false
  return rowCount >= maxPoints * 0.99
}
