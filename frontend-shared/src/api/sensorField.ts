import client from './client'

/**
 * 传感器字段目录 / 选择器解析（后端 `sensorfield` 包的 HTTP 面，§8.1–§8.4）。
 *
 * 为什么需要这一层：自动化规则的 `trigger_sensor_name` 是 varchar(64)，但它的真实语义是
 * 「字段选择器」，四类文法互斥（后端 `sensorfield/doc.go:5-13`、`selector.go:46-51`）：
 *
 *   | 形态       | 例子                 | 何时产出                 |
 *   |-----------|----------------------|--------------------------|
 *   | 字面量     | `temperature_1`      | 选中具体字段 / 手输物理名 |
 *   | 字段族+聚合 | `cell_voltage_*:max` | 选族 + 聚合下拉           |
 *   | 语义别名   | `@soc`               | 选 /schema.aliases 里的项 |
 *   | 派生量     | `virtual:cell_delta` | 本期不在选择器里开放       |
 *
 * 规则里写错一个字母 ⇒ 保存成功、永不触发、没有任何报错（`doc.go:5-8` 原话）。
 * 因此候选、解析预检、聚合词表全部由后端下发，前端**不得另存一份词表**：
 * 一旦后端扩展了聚合/别名，前端硬编码的表就会静默漂移。
 *
 * 三个只读端点 + 一条降级用端点：
 *   GET /api/v1/sensor-fields?edge_device_id=<id>                → {edge_device_id,device_type,truncated,fields[]}
 *   GET /api/v1/sensor-fields/resolve?edge_device_id=&selector=  → {selector,status,kind,aggregate,matched,ambiguous,reason}
 *   GET /api/v1/sensor-fields/schema                             → {aggregates,aggregate_default,virtual_fns,error_codes,aliases,limits}
 *   GET /api/v1/unified-data/categories?device_pk=<id>           → [{code,unit}]（降级链第二级：无类型、无中文名）
 *
 * envelope 约定：axios 响应拦截器返回**整个 envelope**（见 `client.ts:12-19`），
 * 所以取值必须读 `envelope.data`，这里沿用 `api/automation.ts` 的 `unwrap` 写法。
 *
 * 错误约定：语法错误 / 未知语义 / 未知派生 ⇒ HTTP 400 + 响应体 `{code,error_code,message}`，
 * 拦截器把它转成 `ApiError`（`client.ts:100-101`），调用方据此区分「400 可翻译」与「服务不可用」。
 */

/** value_type 取值；空串 = 未知（设备未声明且无观测元数据）。 */
export type SensorFieldValueType = 'number' | 'boolean' | 'enum' | 'bitmask' | 'string' | 'raw' | ''

/** 字段来源：decl=设备声明（DeviceConfig/驱动），obs=实际观测到过（sensor_field_obs 投影）。 */
export type SensorFieldSource = 'decl' | 'obs'

/**
 * 字段目录中的单项。
 *
 * `last_seen` 是选择器里最容易被忽略、也最有用的信息：它区分
 * 「字段存在但设备现在不发了」（有值、时间很旧）与「字段根本不存在」（从未上报）。
 */
export interface SensorField {
  name: string
  unit: string
  /** 中文名；生产 71 行中大量为空 ⇒ 展示时必须回落 name */
  label_zh: string
  value_type: SensorFieldValueType
  /** 小数位；生产全为空（0）= 未知，调用方按 3 位兜底 */
  precision: number
  sources: SensorFieldSource[]
  /** 语义标签（后端 SemanticsOf），如 ['soc'] —— 是 `@别名` 的另一个来源 */
  semantics: string[]
  first_seen: string | null
  /** null = 从未上报（后端只在有 obs 行时给值） */
  last_seen: string | null
  samples: number
  /** 后端已弃用标记（默认请求不会返回，只有 include_deprecated=true 才有） */
  deprecated: boolean
}

/** GET /api/v1/sensor-fields 的 data。 */
export interface SensorFieldCatalog {
  edge_device_id: number
  /** 设备类型（声明归属依据）；空串表示后端也没解析出来 */
  device_type: string
  /** 后端达到单次返回上限（sensorFieldsMaxFields）而截断 */
  truncated: boolean
  fields: SensorField[]
}

/** 选择器解析形态（与后端 sensorfield.Kind* 对齐）。 */
export type SensorSelectorKind = 'literal' | 'family' | 'semantic' | 'virtual'

/**
 * GET /api/v1/sensor-fields/resolve 的 data。
 *
 * 200 只有 resolved / unbound 两种 status：
 * unbound = 语法合法但当前解析不到（软失败，**允许保存**，与后端 warnings 口径一致）；
 * 语法/未知语义/未知派生一律 400 + error_code，不会出现在这里。
 */
export interface SensorFieldResolution {
  selector: string
  status: 'resolved' | 'unbound'
  kind: SensorSelectorKind
  /** 字段族实际生效的聚合；非族选择器为空串 */
  aggregate: string
  /** 解析出的物理名（已去重、已排序） */
  matched: string[]
  /** 命中多个候选（别名归一/族重叠）；true 时 UI 展示「已归一为 <首候选>」 */
  ambiguous: boolean
  /** 后端给的原因串（unbound 时是 resolution_unbound 等人话/码） */
  reason: string
}

/** /schema.error_codes 单项：对外错误码契约（§8.6）。 */
export interface SensorFieldErrorCode {
  code: string
  http: number
}

/** /schema.aliases 单项：语义别名 → 候选物理名（来自 field_aliases.yaml）。 */
export interface SensorFieldAlias {
  semantic: string
  candidates: string[]
  /** 期望单位；别名归一后单位不一致会被后端标出，UI 可提示 */
  expected_unit?: string
}

/** 派生量函数描述（本期选择器不开放 virtual:，仅透传留档）。 */
export interface SensorFieldVirtualFn {
  name: string
  arity: { min: number; max?: number }
  arg_kind?: 'family'
  has_scale?: boolean
}

/** /schema.limits：后端对选择器的硬限制，前端做输入提示时可引用。 */
export interface SensorFieldLimits {
  max_selector_chars: number
  max_family_size: number
  max_virtual_depth: number
}

/**
 * GET /api/v1/sensor-fields/schema 的 data。
 *
 * `aggregates` / `aggregate_default` 是**聚合候选与默认值的唯一来源**
 * （设计文档 §8.4 明令「禁止在代码里另存一份」）；`error_codes` 是错误码人话翻译的
 * 白名单（表外的码属于前端不认识的契约扩展，只能展示后端 message）。
 */
export interface SensorFieldSchema {
  selector_version: number
  aggregates: string[]
  aggregate_default: string
  virtual_fns: SensorFieldVirtualFn[]
  error_codes: SensorFieldErrorCode[]
  aliases: SensorFieldAlias[]
  limits: SensorFieldLimits
}

/**
 * POST /api/v1/sensor-fields/preview 的请求体（与表单字段一一对应）。
 *
 * `window_hours` 省略时后端按 24h；`duration_sec` 与规则的 trigger_duration_sec 同义，
 * 0 = 不要求连续满足（后端据此跳过连续判定，响应里 duration_would_fire 恒为 false，
 * 调用方不能把它读成"不会触发"）。
 */
export interface SensorFieldPreviewRequest {
  edge_device_id: number
  selector: string
  /**
   * 与后端 sensorfield.CmpOp 的合法值一致；前端下拉的 AutomationComparator 是它的子集。
   * 语法错误会 400 + error_code，不会静默降级成某个默认比较符。
   */
  comparator: 'gt' | 'gte' | 'lt' | 'lte' | 'eq' | 'neq' | string
  threshold: number
  window_hours?: number
  duration_sec?: number
}

/**
 * 试算逐帧求值结果中的一帧。
 *
 * `ok=false` 是"本帧解析不到 / 缺员"（不是阈值不满足）；`satisfied` 只在 ok=true 时有意义。
 * `value` 是参与比较的值（reducer 为归约值），unbound 帧为 0。
 */
export interface SensorFieldPreviewFrame {
  /** 设备落库时间（RFC3339）；连续段判定按相邻帧时间差计算 */
  timestamp: string
  ok: boolean
  satisfied: boolean
  value: number
  reason: string
  /** min/max 在缺已知成员时仍按本帧求值（后端 degraded 标记） */
  degraded: boolean
}

/** aggregation_trace 单项：最近一帧的逐字段比较（展示层，不是判定口径）。 */
export interface SensorFieldPreviewTraceItem {
  field: string
  value: number
  valid: boolean
  /** 该字段与阈值的比较结果（valid=false 时无意义） */
  cmp: boolean
  reason?: string
}

/**
 * POST /api/v1/sensor-fields/preview 的 data（§8.3）。
 *
 * 契约要点（与后端 `previewSensorField` 逐项对齐）：
 *   - `status='unbound'` 是**正常响应**：没有可试算的字段，`frames` 为空数组；
 *   - `samples_considered` 是窗口内行数（不是帧数），`frames.length` 才是判定过的帧数；
 *   - `duration_would_fire` 只回答"连续满足是否达到 duration_sec"；duration_sec=0 时
 *     后端恒为 false（见请求体的说明）；
 *   - `truncated=true` = 命中帧预算（由旧到新截断），结论偏乐观；
 *   - `data_source` 决定 `frame_semantics` 的口径（帧表 vs unified_data 过渡回落）。
 */
export interface SensorFieldPreview {
  selector: string
  status: 'resolved' | 'unbound'
  kind: SensorSelectorKind
  aggregate: string
  matched: string[]
  samples_considered: number
  truncated: boolean
  aggregation_trace: SensorFieldPreviewTraceItem[]
  /** reducer 型选择器的归约值；非 reducer / 最近一帧无法求值时为 null */
  reduced: number | null
  /** reducer 型选择器在最近一帧缺失的已知成员（unified_data 口径下 ≠ 设备缺报） */
  missing_known: string[]
  /** 逐帧求值；unbound / 无数据时为空数组 */
  frames: SensorFieldPreviewFrame[]
  duration_sec: number
  duration_would_fire: boolean
  /** 连续判定受采样周期近似（duration_sec>0 时为 true） */
  approx: boolean
  sparse_frames: boolean
  frame_semantics: string
  data_source: 'telemetry_frame' | 'unified_data'
}

/** 降级链第二级的候选：只有 code + unit，无类型、无中文名、无最近上报。 */
export interface SensorFieldFallbackCategory {
  code: string
  unit: string
}

/** 拦截器返回整只 envelope {code,data,message}，用双跳转取 data（同 api/automation.ts）。 */
type Envelope<T> = { code: number; data: T; message: string }

async function unwrap<T>(p: Promise<unknown>): Promise<T> {
  const res = (await p) as unknown as Envelope<T> | T
  if (res && typeof res === 'object' && 'data' in (res as Envelope<T>)) {
    return (res as Envelope<T>).data
  }
  return res as T
}

export const sensorFieldApi = {
  /**
   * 设备字段目录（声明 ∪ 观测，meta 只补展示元数据）。
   *
   * 不传 `include_deprecated`：默认就排除已弃用字段，避免用户把废弃物理名配进新规则；
   * 若将来要为存量规则补显示，再加参数而不是默认拉全量。
   */
  async listFields(edgeDeviceId: number): Promise<SensorFieldCatalog> {
    return unwrap<SensorFieldCatalog>(
      client.get('/api/v1/sensor-fields', { params: { edge_device_id: edgeDeviceId } }),
    )
  },
  /**
   * 保存前解析预检（只读）。
   *
   * 400 会抛 ApiError（带 errorCode），调用方据此把 error_code 翻成人话；
   * 200 的 unbound 是**软失败**，不得当成错误阻止输入 —— 后端保存期同样只给 warning。
   */
  async resolve(edgeDeviceId: number, selector: string): Promise<SensorFieldResolution> {
    return unwrap<SensorFieldResolution>(
      client.get('/api/v1/sensor-fields/resolve', {
        params: { edge_device_id: edgeDeviceId, selector },
      }),
    )
  },
  /**
   * 保存前试算（**只读**）：用最近 `window_hours`（默认 24）的真实数据逐帧重放这条规则。
   *
   * 为什么是 POST 且必须由用户显式触发：后端要扫帧表（24h 窗口在生产可达数十万帧），
   * 每次输入阈值都打一次 = 把"填表单"变成"压测"。UI 只在点「用近 24h 数据试算」时调用。
   *
   * 只读、**不落审计**（后端 handler 注释：§8.3 末句）：无论结论是什么都不得阻止保存 ——
   * 阈值先填、设备后接入是正常流程。unbound 也是正常响应（frames 为空），不是错误；
   * 语法错误 400 + error_code、帧预算超限 413，调用方据此区分"配置问题"与"数据量问题"。
   */
  async preview(req: SensorFieldPreviewRequest): Promise<SensorFieldPreview> {
    return unwrap<SensorFieldPreview>(client.post('/api/v1/sensor-fields/preview', req))
  },
  /** 选择器契约表（聚合词表 / 默认聚合 / 别名 / 错误码 / 上限）。与设备无关，可全局缓存。 */
  async getSchema(): Promise<SensorFieldSchema> {
    return unwrap<SensorFieldSchema>(client.get('/api/v1/sensor-fields/schema'))
  },
  /**
   * 降级链第二级：设备已上报的测量类别。
   *
   * 为什么降级到这里而不是直接手输：这个端点在数据面板里长期在用（DataPanel.vue:762），
   * 可用性远高于新的 /sensor-fields；哪怕只有 code+unit，也足以让用户选到真实存在的
   * 物理名，而不是凭记忆手敲（拼错 = 永不触发）。
   *
   * 注意响应是 data 数组本身（不是 {items}），部分旧后端会回裸数组 —— unwrap 两种都吃。
   */
  async listCategories(devicePk: number): Promise<SensorFieldFallbackCategory[]> {
    const data = await unwrap<SensorFieldFallbackCategory[] | null>(
      client.get('/api/v1/unified-data/categories', { params: { device_pk: devicePk } }),
    )
    return Array.isArray(data) ? data : []
  },
}
