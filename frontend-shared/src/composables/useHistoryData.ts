import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import client from '@/api/client'

export interface HistoryDataPoint {
  time: string
  value: number
}

export interface HistorySeries {
  name: string
  unit: string
  category: string
  data: HistoryDataPoint[]
}

export interface UseHistoryDataOptions {
  /** Map category → display name */
  nameMap?: Record<string, string>
  /** Map category → unit string */
  unitMap?: Record<string, string>
  /** Categories to exclude from results (e.g. status fields) */
  excludeCategories?: string[]
  /** Max data points per category (server-side downsampling) */
  maxPoints?: number
  /** Error message prefix */
  errorPrefix?: string
  /** Whether to show ElMessage on error (set false when caller handles errors itself) */
  showError?: boolean
}

/**
 * Encapsulates the batch-API → per-category-fallback → item-mapping pattern
 * shared by BmsCellVoltageHistoryChart and HistoryChartSection.
 *
 * Usage:
 *   const { series, loading, fetch } = useHistoryData({ nameMap, unitMap })
 *   await fetch(deviceId, categories, startTime, endTime)
 */
export function useHistoryData(options: UseHistoryDataOptions = {}) {
  const {
    nameMap = {},
    unitMap = {},
    excludeCategories = [],
    maxPoints = 500,
    errorPrefix = '获取历史数据失败',
    showError = true,
  } = options

  const series = ref<HistorySeries[]>([])
  const loading = ref(false)

  function mapItem(item: any): HistoryDataPoint {
    return { time: item.created_at || item.timestamp, value: item.value }
  }

  function buildSeries(cat: string, data: any[]): HistorySeries | null {
    if (excludeCategories.includes(cat)) return null
    const items = (data || []).map(mapItem)
    if (items.length === 0) return null
    return {
      name: nameMap[cat] || cat,
      unit: unitMap[cat] || '',
      category: cat,
      data: items,
    }
  }

  async function fetch(
    deviceId: number | string,
    categories: string[],
    startTime: Date,
    endTime: Date,
  ): Promise<HistorySeries[]> {
    if (!deviceId || categories.length === 0) return []
    loading.value = true
    const result: HistorySeries[] = []
    try {
      try {
        // Batch API: single request for all categories
        const batchRes = await client.get<unknown, any>('/api/v1/unified-data/historical-batch', {
          params: {
            device_pk: deviceId,
            categories: categories.join(','),
            start_time: startTime.toISOString(),
            end_time: endTime.toISOString(),
            max_points: maxPoints,
          },
        })
        const batchData = batchRes?.data || batchRes || []
        if (Array.isArray(batchData)) {
          for (const entry of batchData) {
            const cat = entry.category
            if (!cat) continue
            const s = buildSeries(cat, entry.data || [])
            if (s) result.push(s)
          }
        }
      } catch {
        // Fallback: per-category requests
        const fallbackResults = await Promise.all(
          categories.map(cat =>
            client.get<unknown, any>('/api/v1/unified-data/historical', {
              params: {
                device_pk: deviceId,
                category: cat,
                start_time: startTime.toISOString(),
                end_time: endTime.toISOString(),
                max_points: maxPoints,
              },
            })
              .then((res: any) => ({ cat, data: (res.data || res || []) as any[] }))
              .catch(() => ({ cat, data: [] as any[] })),
          ),
        )
        for (const r of fallbackResults) {
          const s = buildSeries(r.cat, r.data)
          if (s) result.push(s)
        }
      }
      series.value = result
    } catch {
      if (showError) ElMessage.error(errorPrefix)
    } finally {
      loading.value = false
    }
    return result
  }

  return { series, loading, fetch }
}
