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
}

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
