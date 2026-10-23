import client, { type ApiEnvelope } from './client'

/**
 * 来源总览（数据流架构 v1 §4.1）。
 *
 * 与 `dataSource.ts` 的区别：
 * - `dataSource.ts` 是**旧的 CRUD 语义**（可写、面向运维），已 deprecated；
 * - 本文件是**只读投影语义**（自动派生、面向所有用户）。
 *
 * 后端对应 `GET /api/v1/data-sources/overview` —— 该端点**没有写方法**
 * （POST/PUT/DELETE 返回 405），因此本类型里没有任何 *Request 类型。
 * 这是「数据源只读」在类型层的表达：没有写类型，就没有写调用。
 */

/** 来源类型。取值与后端 source.Kind 逐字一致（门禁校验三向一致）。 */
export type SourceKind = 'direct' | 'primary_backup' | 'composite'

/** 来源运行状态（派生展示用，不参与求值）。 */
export type SourceStatus = 'ok' | 'degraded' | 'unconfigured' | 'disabled'

/** 来源的一个成员设备。 */
export interface SourceMember {
  edge_device_id: number
  edge_device_name: string
  /** 是否当前权威（主备组只有一个；直连恒 true；复合组无权威） */
  active: boolean
  enabled: boolean
}

/** 一个来源（= 一个逻辑设备的一个类别）。 */
export interface SourceOverviewItem {
  logical_device_id: number
  logical_device_name: string
  /** 数据类别 = unified_data.sensor_name 物理名 */
  category: string
  /** 关联的来源组 ID（0 = 无来源组，单设备直连） */
  group_id: number
  group_name: string | null
  kind: SourceKind
  member_count: number
  members: SourceMember[]
  status: SourceStatus
  last_success: string | null
  revision: number

  /**
   * ── 字段元数据（「接入的数据字段要有数据类型、数据描述」）──
   *
   * 三者的来源分层（后端已按此组装，前端只管展示）：
   *   label_zh   ← sensor_field_meta（声明层，可人工登记）
   *   value_type ← sensor_field_meta；未登记时由后端按观测值推断
   *   unit       ← sensor_field_obs（事实层：实际观测到的单位比声明更可信）
   *
   * 全部可能为空串 —— 前端必须降级显示（见 OverviewTab 的 fmtMeta），
   * 不能因为某个字段没登记就让整行不可读。
   */
  label_zh: string
  value_type: SourceValueType | ''
  unit: string

  /**
   * ── 实时数据 ──
   *
   * latest_value 用 number | null 而非 number：**0 是合法读数**
   * （例如告警位为 0、电流为 0），必须与「暂无数据」区分开 ——
   * 后端因此用了 *float64，前端也必须保留 null 语义。
   */
  latest_value: number | null
  latest_at: string | null
  /** 读数年龄（秒）。用于把「实时」与「陈旧」区分开 */
  value_age_sec: number | null
}

/** 数据字段的语义类型。与后端 sensor_field_meta.value_type 词表一致。 */
export type SourceValueType = 'number' | 'boolean' | 'enum' | 'bitmask' | 'string' | 'raw'

export interface SourceOverview {
  items: SourceOverviewItem[]
  total: number
  /** 后端生成该视图的时间 */
  derived_at: string
}

export interface SourceOverviewQuery {
  category?: string
  logical_device_id?: number
}

async function unwrap<T>(p: Promise<ApiEnvelope<T>>): Promise<T> {
  return (await p).data
}

export const sourceOverviewApi = {
  /**
   * 获取来源总览（只读）。
   *
   * 这是数据源页面**唯一**的读取入口。旧 CRUD 的 `dataSourceApi.list()`
   * 不再被页面调用（保留给既有运维脚本兼容）。
   */
  async list(params?: SourceOverviewQuery): Promise<SourceOverview> {
    return unwrap<SourceOverview>(
      client.get<unknown, ApiEnvelope<SourceOverview>>('/api/v1/data-sources/overview', { params }),
    )
  },
}
