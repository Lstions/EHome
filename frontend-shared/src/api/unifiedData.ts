import client from './client'

// 统一数据类型
export interface UnifiedData {
  id: number
  device_pk: number
  source_id: number
  category: string
  value: number
  unit: string
  quality: number
  timestamp: string
  created_at: string
}

// 数据类别
export interface DataCategory {
  code: string
  name: string
  unit: string
}

// 查询参数
export interface UnifiedDataQueryParams {
  device_pk?: number
  category?: string
  source_id?: number
  start_time?: string
  end_time?: string
  min_quality?: number
  page?: number
  page_size?: number
  order_by?: string
  order_dir?: 'ASC' | 'DESC'
}

// 聚合数据
export interface AggregatedData {
  time_bucket: string
  avg_value: number
  min_value: number
  max_value: number
  count: number
}

// 统一数据API
export const unifiedDataApi = {
  // 高级查询
  query(params: UnifiedDataQueryParams) {
    return client.get<{ data: UnifiedData[]; total: number }>('/unified-data', { params })
  },

  // 获取最新数据
  getLatest(devicePk: number, category?: string) {
    const params: any = { device_pk: devicePk }
    if (category) {
      params.category = category
    }
    return client.get<{ data: UnifiedData | UnifiedData[] }>('/unified-data/latest', { params })
  },

  // 获取历史数据
  getHistorical(params: {
    device_pk: number
    category?: string
    start_time: string
    end_time: string
  }) {
    return client.get<{ data: UnifiedData[] }>('/unified-data/historical', { params })
  },

  // 获取聚合数据
  getAggregated(params: {
    device_pk: number
    category: string
    interval: 'hour' | 'day' | 'month'
    start_time: string
    end_time: string
  }) {
    return client.get<{ data: AggregatedData[] }>('/unified-data/aggregated', { params })
  },

  // 获取数据类别列表
  getCategories() {
    return client.get<{ data: DataCategory[] }>('/unified-data/categories')
  },
}
