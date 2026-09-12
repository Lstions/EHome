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
