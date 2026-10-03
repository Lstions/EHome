import request from './client'

export interface MetricsSummary {
  timestamp: number
  http: {
    requests_total: number
    requests_in_flight: number
  }
  mqtt: {
    messages_received: number
    messages_sent: number
    connection_errors: number
  }
  device: {
    online: number
    offline: number
    /**
     * 已创建但尚未收到任何数据的设备（后端 EdgeDeviceStatusPending）。
     * 2026-10-03：后端此前把这类设备计入 offline（status <> active 兜底），
     * 会把刚建好的设备报成故障。三态分开后这里也必须有对应字段，
     * 且总数计算要把 pending 算进去，否则百分比的分母会漏掉一部分设备。
     * 旧后端不返回该字段时为 undefined，按 0 处理。
     */
    pending?: number
  }
  node: {
    online: number
    offline: number
  }
  data: {
    points_collected: number
    points_stored: number
  }
  ota: {
    upgrades_total: number
  }
  websocket: {
    connections_active: number
    messages_total: number
  }
  control: {
    operations_total: number
    active: number
    queued: number
    succeeded: number
    failed: number
    unknown: number
    unresolved_unknown: number
    cancelled: number
    outbox_pending: number
    outbox_leased: number
    capability_stale_nodes: number
    audit_write_failures: number
  }
}

export interface MetricsResponse {
  code: number
  data: MetricsSummary
}

/**
 * 获取系统指标摘要
 * 注意：响应拦截器已解包 envelope，故用 <unknown, MetricsResponse> 双泛型
 * （与 api/node.ts 的 ApiResponse 模式一致），避免 axios 把返回值建模为 AxiosResponse。
 */
export function getMetricsSummary() {
  return request.get<unknown, MetricsResponse>('/api/v1/metrics/summary')
}

/**
 * 获取完整指标数据
 */
export function getMetrics() {
  return request.get('/api/v1/metrics')
}
