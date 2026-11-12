/**
 * 数据面板子模块共享的响应类型（原样搬自 DataPanel.vue 内的本地 interface）。
 *
 * 为什么抽成独立模块（2026-11-12 拆分）：
 *   · composables/ 里的趋势图、多设备对比两条取数链要解析同一批 /api/v1/unified-data/*
 *     响应（envelope 与裸数组两种形态）；
 *   · components/ 里的历史表格 / 卡片 / 详情弹层只消费行，不碰接口。
 * 每个文件各写一份形状，后端字段一变就会只改一处、另一处静默漂移 ——
 * 与本仓 historyRows.ts 单独成模块是同一条纪律。
 */

/** /unified-data/categories 的返回项：设备可用的指标类别与单位。 */
export interface MeasurementCategory {
  code: string
  unit: string
}

/** /unified-data/historical(/-batch) 的返回点。 */
export interface MeasurementPoint {
  timestamp?: string
  created_at?: string
  value: number
}

/** /unified-data/historical-batch 的返回元素：一个类别一段序列。 */
export interface MeasurementBatch {
  category: string
  data: MeasurementPoint[]
}

/** WS channel_data/data_update 的 payload（本页只消费到指标与采集时刻这一层）。 */
export interface RealtimeDataPayload {
  edge_device_id?: number
  collected_at?: string
  data?: Record<string, unknown>
  sensors?: Record<string, unknown>
  // ⚠ 不再声明 raw_hex / raw_data：WS 帧仍可能带这两个字段（stores/websocket.ts
  // 的 payload 契约由 ChannelTerminal 消费），但**本页已不消费它们** ——
  // 「原始数据」列随 device_data 退役已删除。声明一个从不读取的可选字段
  // 只会让后来者以为页面上还存在那种展示。
}
