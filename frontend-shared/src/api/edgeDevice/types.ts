/**
 * 边缘设备领域的**类型层**（自 api/edgeDevice.ts 原样搬出：名字、字段、注释一字未改）。
 *
 * 边界依据：本文件只有类型声明，不含任何 HTTP 调用与归一化逻辑。barrel
 * （api/edgeDevice.ts）用 `export *` 全量转出，因此既有
 * `import type { EdgeDevice } from '@/api/edgeDevice'` 之类的写法逐字不变。
 */
import type { OperationDef } from '../deviceConfig'

// M10 fix: Export DeviceStatus type for use across components
// Extended to support health status: active, warning, error, disabled
export type DeviceStatus = 'active' | 'online' | 'offline' | 'warning' | 'error' | 'disabled' | 'pending' | 'initializing' | 'unknown'

/**
 * 边缘设备**关联通道**（后端 GET /edge-devices 每个 item 携带的 channel 对象）。
 * 只声明展示所需的字段；其余字段（bus_config/address/...）按需追加。
 */
/**
 * 单个传感器的「最新值」。
 *
 * 与 EdgeDevice 的 latest_data 不同：后者是**一条**原始记录里的若干物理量
 * （对多命令设备只覆盖最后一条命令），本类型是**跨命令**聚合后的全集。
 */
export interface LatestSensorValue {
  sensor_name: string
  value: number
  unit: string
  /** 该传感器最近一次采样的时刻（各传感器独立，可能相差数秒） */
  timestamp: string
}

/**
 * `GET /api/v1/devices/:id/history` 的单行（后端 models.UnifiedData）。
 *
 * ⚠ **一行 = 一个字段的一次采样**，不是"一次采集"。同一时刻解析出的 N 个物理量
 * 是 N 行，它们的 `timestamp` 完全相同（后端整帧共用一个 now）。需要"一次采集
 * 一行"的视图必须自己按 timestamp 聚合。
 */
export interface UnifiedDataRow {
  id: number
  device_id: number
  sensor_name: string
  value: number
  unit: string
  timestamp: string
  created_at: string
  /** 帧表解码路径会带（= device_id），unified_data 回落路径可能缺省 */
  edge_device_id?: number
}

export interface EdgeDeviceChannel {
  id?: number
  /**
   * 通道所属节点的序列号（后端 channel.node_id，如 'F0F5BDFFFE02'）。
   * 与 EdgeDevice.node_id 同源，仅作展示兜底时的关联依据，不参与判定。
   */
  node_id?: number | string
  /** 总线名字，形如 "UART0" / "I2C1" / "SPI0" —— 才是「设备挂在哪个通道」的权威标识 */
  hardware_id?: string
  /** 总线类型枚举，后端存大写（"UART"/"I2C"/…），历史数据存在小写 */
  hardware_type?: string
  /** 后端 Channel.BusType（json: bus_type），如 "UART" */
  bus_type?: string
}

export interface EdgeDevice {
  id: number
  node_id: number | string
  channel_id: number
  node?: { id: number | string; name: string; firmware_version?: string }
  name: string
  device_type: string
  protocol: string
  hardware_type: string
  /**
   * **设备从站地址**（Modbus 地址 / I2C 地址），如 "1"、"0x76"、"7"。
   *
   * ⚠️ 它**不是**总线名，**不要**用它做「通道」展示 —— 后端 models.EdgeDevice.HardwareID
   * 的语义就是通信地址（空值按遗留默认地址 1 处理，见 backend/internal/deviceaction）。
   * 总线名在关联通道上：见 channel → EdgeDeviceChannel.hardware_id（"UART0"）。
   *
   * 历史坑：normalize() 曾把 d.hardware_id || d.channel?.hardware_id 合并进本字段，
   * 于是设备地址（真值 "1"）**赢过**总线名（"UART1"），让「通道」栏拼出误导性的 "UART 1"。
   * 本字段语义保持不变（其它调用方依赖它是地址）；通道显示一律走 channel_hardware_id。
   */
  hardware_id: string
  /**
   * 关联通道的总线名，直接取自后端 channel.hardware_id（"UART0"）。
   * 取不到时为 undefined（channel 缺失 / 该字段为空）—— 展示层必须回退为
   * UNKNOWN（'—'），不得拿 hardware_id（设备地址）冒充。
   */
  channel_hardware_id?: string
  /**
   * 归一化后的关联通道信息；后端 channel 缺失（老数据 / 紧凑列表）时为 undefined。
   * 需要总线类型（UART/I2C）等更多通道字段时用它，而不是 channel_hardware_id。
   */
  channel?: EdgeDeviceChannel
  config: Record<string, any>
  status: DeviceStatus
  last_data: Record<string, number> | null
  last_data_time: string | null
  last_error_code?: number
  created_at: string
  // 配置版本 (后端 EdgeDevice.ConfigVersion，配置清单版本标识)
  config_version?: string
  /**
   * 后端 MaxCommandsPerEdgeDevice：本设备**同时可轮询**的指令条数上限（2026-10-15）。
   * 来自 GET /edge-devices/:id 的附加字段；PUT .../commands 超过该值会整单 400 拒绝，
   * 前端据此在保存前给出可行动约束（"最多 3 条，当前已启用 5 条"）。
   * 后端未返回时为 undefined → 不展示约束，行为与改动前一致。
   */
  max_polling_commands?: number
  /**
   * 删除设备时勾选了「同时删除历史数据」的待清理标记（后端
   * edge_devices.data_purge_requested）。软删后由后台 purge 任务分批硬删数据、
   * 清标记并硬删设备行；在此之前设备处于「已删除 + 数据待清理」状态。
   * 逻辑设备移除（2026-10-23）后该标记从 logical_devices 迁到本表。
   */
  data_purge_requested?: boolean
  device_config?: { id?: number; protocol?: string; config?: Record<string, any> | string; operations?: Record<string, OperationDef> }
}

/**
 * GET /api/v1/edge-devices/deleted 的列表项（已软删、可恢复的设备）。
 *
 * 除常规身份字段外，额外带四个「值不值得恢复 / 还能不能恢复」的判据：
 *   · last_data_at  —— 最后一次收到数据的时刻（判断历史数据有多"新鲜"）
 *   · row_estimate  —— 该 device_id 名下数据行数估算（后端按 reltuples/有界 COUNT
 *                      估算，可能为 null —— 表示"估算不出来"，**不是 0 行**，
 *                      展示层必须区分二者）
 *   · data_purge_requested —— 删除时勾选了「同时删除历史数据」（恢复会撤回该请求）
 *   · purge_in_progress    —— 后台 DevicePurger 已认领、正在分批硬删数据
 *                             ⇒ 此时 restore 必然 409，前端必须提前禁用入口
 */
export interface DeletedEdgeDevice {
  id: number
  name: string
  /** 后端同时回 type / device_type（同值）；前端统一用 device_type */
  device_type: string
  hardware_id: string
  node_id: number | string
  channel_id: number
  deleted_at: string | null
  last_data_at: string | null
  row_estimate: number | null
  data_purge_requested?: boolean
  purge_in_progress?: boolean
  purge_started_at?: string | null
}

/**
 * GET /api/v1/edge-devices/deleted 的分页信封（2026-10-25 起后端分页）。
 * 旧后端返回裸数组时由封装层归一为「单页、total=当前条数」，不伪造更多页。
 */
export interface DeletedEdgeDevicePage {
  items: DeletedEdgeDevice[]
  total: number
  page: number
  page_size: number
}

/**
 * 批量操作的逐条结果（POST /edge-devices/batch-update 与
 * POST /edge-devices/deleted/batch-delete 共用形状）。
 *
 * 后端逐条独立事务：一条失败不影响其它条；前端必须把 failed>0 的明细展示给用户，
 * 不能只用一句"批量操作完成"掩盖部分失败。
 */
export interface BatchItemResult {
  id: number
  success: boolean
  /** 该条失败原因（来源组规则引用 / 设备不存在 / 未曾软删 等） */
  error?: string
  /** batch-update：该条写入后的 enabled */
  enabled?: boolean
  /** batch-update：该条写入后的 interval_ms */
  interval_ms?: number
  /** deleted/batch-delete：已提交后台清理队列（重复提交幂等也为 true） */
  queued?: boolean
}

export interface BatchUpdateResult {
  total: number
  succeeded: number
  failed: number
  results: BatchItemResult[]
}

export interface BatchDeleteHistoryResult {
  total: number
  succeeded: number
  failed: number
  results: BatchItemResult[]
}

/** POST /api/v1/edge-devices/:id/restore 的请求体（节点与通道必须成对且相容）。 */
export interface RestoreEdgeDeviceParams {
  node_id: string | number
  channel_id: number
  name?: string
  interval_ms?: number
}

/** data-summary 的来源状态：deleted=已软删 / live=存活在役 / missing=无设备行的孤儿历史。 */
export type DataSummarySourceState = 'deleted' | 'live' | 'missing'

/**
 * data-summary 的单个来源条目：该来源有多少行、覆盖什么时间段、处于什么状态。
 *
 * `rows` **可以为 null**：后端估算超时/失败时省略该字段。消费者必须先判空再
 * 格式化 —— 直接 `.toLocaleString()` 在非空分支之外会抛 TypeError。
 * `device_name`/`state` 是新契约字段：state=missing 表示"没有设备行，但可能有孤儿数据"。
 * `device_type`（2026-10-25 产品重做）：合并弹窗据此做「同型号」准入校验/展示。
 */
export interface EdgeDeviceDataSummaryItem {
  source_id: number
  device_name?: string
  device_type?: string
  state?: DataSummarySourceState
  rows: number | null
  first_time: string | null
  last_time: string | null
}

/**
 * GET /api/v1/edge-devices/:id/data-summary 响应（2026-10-25 新口径）。
 *
 * 目标与源**分两组返回**，不再混用：
 *   · target        —— 目标设备自身（删除确认弹窗用）
 *   · sources       —— 每个源设备一项（合并预览用）
 *   · target_rows   —— 目标自身合计（仅目标估算成功时出现）
 *   · source_rows   —— 源合计（**仅当全部源估算成功才出现**；缺失必须显示「—」，
 *                      不能兜底成 0 —— 报一个偏小的合计比不报更危险）
 * 两者绝不相加；「预计迁移行数」只能读 source_rows。
 */
export interface EdgeDeviceDataSummary {
  target: EdgeDeviceDataSummaryItem | null
  sources: EdgeDeviceDataSummaryItem[]
  target_rows?: number | null
  source_rows?: number | null
}

/**
 * merge-history 响应里的单源结果（2026-10-25 产品重做后）。
 *
 * 后端准入已收紧为「存在 + 已软删 + 与目标同型号」，因此正常响应里 state 只会是
 * deleted；unknown 仅用于响应形状异常/旧后端时的防御性回填（不谎报状态）。
 */
export type MergeHistorySourceState = 'deleted' | 'unknown'

export interface MergeHistorySourceResult {
  id: number
  state: MergeHistorySourceState
  /** 源设备型号（后端回传；理论恒等于目标型号，用于展示/自证） */
  device_type?: string
}

/** POST /api/v1/edge-devices/:id/merge-history 响应（幂等：重复调用 rows_migrated=0）。 */
export interface MergeHistoryResult {
  rows_migrated: number
  sources: MergeHistorySourceResult[]
  /** 非致命问题（如人工 data_sources 冲突保留、台账迁移失败、last_data_at 回写失败），可重试补齐 */
  warnings: string[]
}

export interface EdgeDeviceListParams {
  node_id?: number | string
  device_type?: string
  status?: string
  /** 总线类型 (uart/i2c/spi/...), 服务端按 channels.hardware_type 大小写不敏感匹配 */
  hardware_type?: string
  /** 服务端全库检索 (name/type 模糊匹配, 大小写不敏感) */
  search?: string
  page?: number
  page_size?: number
}

/** GET /edge-devices 的分页信封 (架构评估 P1.2: items + total, 非 list)。 */
export interface EdgeDeviceListResponse {
  items: EdgeDevice[]
  /** 过滤后的**全量**条数 (不是当前页条数); 分页器用它算总页数 */
  total: number
  /** 后端自负债 I-11 起回显; 旧后端缺省时由 getList() 以请求值兜底 */
  page?: number
  page_size?: number
}

// 创建参数 — 精确对齐后端 CreateDTO，不继承 Partial<EdgeDevice>
// 后端 DTO: name(*string), type(*string), node_id(*string), channel_id(*uint), hardware_id(*string)
export interface CreateEdgeDeviceParams {
  name?: string
  type?: string
  node_id?: string
  channel_id?: number
  hardware_id?: string
  enabled?: boolean
  interval_ms?: number
  device_config_id?: number
  // F: inline channel creation — when channel_id is 0/absent and channel is
  // provided, the backend creates the channel inside the same transaction.
  channel?: {
    hardware_type: string
    hardware_id?: string
    address?: string
    config?: Record<string, unknown>
    // hex-encoded pin-route payload; the wizard's inline path omits this (no
    // route to validate), a caller that supplies one gets the backend's
    // peripheral pin-conflict gate.
    bus_config?: string
  }
  // EDGE-WIZ-004: 逐指令轮询间隔 (command_id → interval_ms, 0 = 禁用)。
  // 创建接口按最终设备类型校验 schedulable command id, 未知/非轮询 id 会被拒绝;
  // 仅当驱动声明了 schedulable 轮询指令时才应携带。
  command_intervals?: Record<string, number>
}

// ============================================================
// 指令模板（驱动命令模板 / 设备逐指令轮询间隔）
// ============================================================

export interface CommandTemplate {
  id: string
  name: string
  type: string
  cmd_byte: number
  write_data: string
  read_length: number
  delay_ms: number
  interval_ms: number
  schedulable: boolean
  description: string
}

export interface CommandTemplateWithInterval extends CommandTemplate {
  current_interval_ms: number
}
