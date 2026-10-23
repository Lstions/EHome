import client, { type ApiEnvelope } from './client'

/**
 * 已接入字段目录（只读）。
 *
 * # 这个接口是什么
 *
 * 回答唯一的问题：*设备解析成功之后，系统里现在有哪些字段、什么类型、
 * 什么单位、最新值多少、多久前更新的。*
 *
 * # 它不是什么（重要）
 *
 * 它**不是**主备来源的配置台账。早期版本把 data_sources（一张主备配置表）
 * 当字段目录渲染，于是前端出现了「来源类型/成员/当前来源/来源组」四列 ——
 * 那些是"多设备里用哪台供数"的配置问题，与"有哪些字段"无关，且本页只读
 * 无法配置。现在本接口只输出字段事实。
 *
 * 来源组（主备/复合）的配置在 source-group 相关接口维护，两页职责不重叠。
 */

/** 数据字段的语义类型。与后端 sensor_field_meta.value_type 词表一致。 */
export type SourceValueType = 'number' | 'boolean' | 'enum' | 'bitmask' | 'string' | 'raw'

/**
 * 数据新鲜度（由读数年龄派生，**不是**配置状态）。
 *
 *   ok      年龄 ≤ 30s
 *   delay   30s < 年龄 ≤ 300s
 *   stale   年龄 > 300s
 *   nodata  从未收到数据
 */
export type SourceFreshness = 'ok' | 'delay' | 'stale' | 'nodata'

/** 字段目录的一行（= 一个逻辑设备的一个数据类别）。 */
export interface SourceOverviewItem {
  logical_device_id: number
  logical_device_name: string
  /** 数据类别 = unified_data.sensor_name 物理名 */
  category: string

  /** 中文描述（空 = 未登记，前端只显示物理名） */
  label_zh: string
  value_type: SourceValueType | ''
  /** 实际观测到的单位（空 = 未知） */
  unit: string

  /**
   * 实时值。
   *
   * 用 number | null 而非 number：**0 是合法读数**
   * （告警位为 0、电流为 0），必须与「暂无数据」区分 ——
   * 后端因此用 *float64，前端必须保留 null 语义。
   */
  latest_value: number | null
  /** 该读数的时间（null = 从未收到数据） */
  updated_at: string | null
  /** 读数年龄（秒） */
  age_sec: number | null

  status: SourceFreshness
}

export interface SourceOverview {
  items: SourceOverviewItem[]
  total: number
  /** 后端生成该视图的时间 */
  generated_at: string
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
   * 获取字段目录（只读）。
   *
   * 这是数据来源页面**唯一**的读取入口。旧 CRUD 的 dataSourceApi.list()
   * 不再被页面调用（保留给既有运维脚本兼容）。
   */
  async list(params?: SourceOverviewQuery): Promise<SourceOverview> {
    return unwrap<SourceOverview>(
      client.get<unknown, ApiEnvelope<SourceOverview>>('/api/v1/data-sources/overview', { params }),
    )
  },
}
