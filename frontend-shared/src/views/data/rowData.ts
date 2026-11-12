/**
 * 数据面板「行数据解析」（纯函数，**本页唯一**的取数入口）。
 *
 * 为什么单独成模块（2026-11-12 从 DataPanel.vue 的非 setup script 块搬出）：
 * 模板、统计卡（dynamicStats）、趋势兜底、CSV 导出、实时条目，以及历史表格 / 移动端卡片 /
 * 详情弹层三个子组件都要取同一份数据，散落多处必然再次漂移（历史缺陷正是"表格读
 * parsed_data、接口只给 data_json"）。搬出 SFC 后这些消费者共用同一个实现；
 * DataPanel.vue 仍具名再导出本函数，DataPanelRowData.spec.ts 的读取路径与断言文本
 * 一字未改（搬迁只改实现位置，不改对外契约）。
 *
 * 为什么不用"把函数 defineExpose 出去、让测试调它"的既有范式：缺陷的回归点是
 * **表格列恒显示 —**，只暴露函数等于不守渲染路径；测试必须打到真实渲染出的单元格。
 *
 * 契约（规范 §3.2.5 不得以本地默认值伪造事实）：
 *   · 取不到 / 形状不认识 ⇒ values = null ⇒ 界面显示「—」；
 *   · 值是 0 ⇒ values = { rainfall: 0 } ⇒ 界面显示「rainfall: 0.00」。
 *   二者绝不互相顶替，尤其**不得**把解析失败落成 0。
 *
 * 行形状的**唯一真源**见 ./historyRows.ts —— 历史行的聚合规则写在那里。
 */
/** parseRowData 的返回：数值指标。 */
export interface RowData {
  /**
   * 指标名 → **展示值**（数值或字符串）；解析不出来时为 null
   * （不是空对象 —— 空对象会与"真的没有指标"混淆）。
   *
   * 允许字符串是因为部分传感器的真值本就是字符串：后端 SensorData 有
   * `StringValue` 字段（drivers/registry.go:13），此时 `Value` 恒为 0 而真值在
   * StringValue 里 —— 典型是 BMS 的 hardware_version（"V19"）与 serial_number。
   * 只取 Value 会把它显示成 **0**，比「—」更糟：0 看起来像真实读数。
   *
   * ⚠ 2026-11-11 原始数据退役后，**本页已不可能收到字符串读数**：
   * 唯一承载 StringValue 的 device_data.data_json.str_values 随表删除，
   * 而 /devices/:id/history 只返回 float64 的 value。类型保留字符串分支，
   * 是因为实时 WS 推送（handleDataUpdate）仍可能带上字符串字段，
   * 且行解析不该对"值是不是数字"做两次不同的假设。
   */
  values: Record<string, number | string> | null
  /**
   * 指标名 → **纯数值**，供统计卡 / 趋势图 / 数值专用消费者使用。
   *
   * 单独给出是为了不让字符串读数污染数值计算：若把 "V19" 混进趋势，
   * 它会被当成 0 ⇒ 拉低均值并在图上画出掉到 0 的假线。
   */
  numbers: Record<string, number> | null
}

/**
 * 表格行的形状。
 *
 * 两个来源，**形状已经统一**（无需再按响应格式分支）：
 *   · 历史行  —— aggregateHistoryRows() 的产物（一次采集一行，指标在 data 里）；
 *   · 实时行  —— handleDataUpdate() 由 WS payload 构造，同样把指标放进 data。
 */
interface DataRow {
  data?: unknown
}

/**
 * 只认 number（并兼容既有的 { value } 包装）。刻意**不**做 Number(str)：
 * "0.5" / "abc" 这类字符串被静默转成 0.5 / NaN 都属于"以本地推断伪造事实"。
 */
const asNumber = (value: unknown): number | null => {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (value && typeof value === 'object' && 'value' in value) {
    return asNumber((value as { value?: unknown }).value)
  }
  return null
}

/**
 * 行内单个字段的展示值。
 *
 * 口径与本仓既有正确范式一致（views/edge-device/shared/DeviceControlPanel.vue:76
 * 的 `value.string_value || value.value`）：**字符串非空时优先**，否则取数值。
 *
 * 为什么保留字符串分支：实时 WS 推送的 payload 可能带字符串型读数
 * （device_data 时代是 StringValue；退役后仍有设备上报非数值文本字段）。
 * 只认数字会把真实文本读数丢掉，显示成「—」。
 * 刻意**不**做 Number(str)：把 "0.5" 静默转成数字属于以本地推断伪造事实。
 * 返回 null 表示"这个字段没有可信值"，与"值是 0"区分。
 */
const asDisplayRowValue = (value: unknown): number | string | null => {
  if (typeof value === 'string') return value.trim() !== '' ? value : null
  return asNumber(value)
}

/**
 * 非指标字段。扁平兜底分支必须排除它们，否则会把 id / device_id 这类
 * **元数据数字**当成传感器读数渲染出来（实测真实行就带这两个数字键 ——
 * 本文件新增用例抓到过）。
 *
 * 2026-10-23（逻辑设备移除）：原来这里还有一个 logical_device_id，
 * 后端已删除该列（契约 §2），故从排除表里一并去掉 —— 保留它等于给一个
 * 不可能再出现的键留位。
 *
 * 2026-11-11（原始数据退役）：raw_data / raw_hex / data_json / parsed_data
 * 一并从排除表里删掉。它们原本是 device_data 行上的字段，现在行来自
 * /devices/:id/history（一字段一行）或 WS 推送，都不再有这些键；
 * 留着一串永不出现的名字只会让后来者以为还存在那种形状。
 */
const NON_METRIC_KEYS = new Set([
  'id', 'device_id', 'node_id', 'channel_id',
  'timestamp', 'created_at', 'collected_at', 'updated_at',
])

/**
 * 行数据解析（**本页唯一**的取数入口）。
 *
 * 契约（规范 §3.2.5 不得以本地默认值伪造事实）：
 *   · 取不到 / 形状不认识 ⇒ values = null ⇒ 界面显示「—」；
 *   · 值是 0 ⇒ values = { rainfall: 0 } ⇒ 界面显示「rainfall: 0.00」。
 *   二者绝不互相顶替，尤其**不得**把解析失败落成 0。
 *
 * 2026-11-11 原始数据退役后**大幅简化**：原来有两条分支（解析 data_json 的
 * sensors / 扁平兜底），现在历史行与实时行的形状已经统一 —— 指标都直接放在
 * `data` 里（历史行由 aggregateHistoryRows 产出，实时行由 WS payload 构造）。
 * data_json 分支随 device_data 表一起删除，坏 JSON 的处理也就没有了对象。
 */
export function parseRowData(row: unknown): RowData {
  if (!row || typeof row !== 'object') return { values: null, numbers: null }
  const r = row as DataRow

  let values: Record<string, number | string> | null = null

  // ① 首选 { data: {...} }（历史行与实时行都是这个形状）
  // ② 兼容扁平 {...}（既有用例的 fixture 走这里）
  const flat = r.data && typeof r.data === 'object' && !Array.isArray(r.data)
    ? r.data as Record<string, unknown>
    : r
  const entries = Object.entries(flat)
    .filter(([key]) => !NON_METRIC_KEYS.has(key))
    .map(([key, value]) => [key, asDisplayRowValue(value)] as const)
    .filter((entry): entry is readonly [string, number | string] => entry[1] !== null)
  if (entries.length > 0) values = Object.fromEntries(entries)

  return { values, numbers: numericOnly(values) }
}

/** 从展示值里挑出纯数值子集（统计卡 / 趋势图专用），字符串读数不得进入。 */
function numericOnly(values: Record<string, number | string> | null): Record<string, number> | null {
  if (!values) return null
  const out: Record<string, number> = {}
  for (const [key, value] of Object.entries(values)) {
    if (typeof value === 'number') out[key] = value
  }
  return Object.keys(out).length > 0 ? out : null
}
