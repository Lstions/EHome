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
  collector: {
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
}

export interface MetricsResponse {
  code: number
  data: MetricsSummary
}

/**
 * 获取系统指标摘要
 */
export function getMetricsSummary() {
  return request.get<MetricsResponse>('/api/v1/metrics/summary')
}

/**
 * 获取完整指标数据
 */
export function getMetrics() {
  return request.get('/api/v1/metrics')
}
