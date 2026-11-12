/**
 * 数据面板「多设备对比」（原 DataPanel.vue 的 compare* 段原样搬出）。
 *
 * 边界依据（为什么整块搬、不与趋势图合并）：
 *   · 对比是**独立的一条取数链**：/unified-data/categories 求各设备类别交集 →
 *     /unified-data/historical 逐设备取点 → 拼成 compareSeries。它不读历史行、不读分页、
 *     不读趋势序列，与页面的历史表/趋势图没有任何共享状态；
 *   · 两个 watch（所选设备变化 → 重算可比较类别；类别变化 → 重新取点）与这三个 ref
 *     是同一生命周期，拆开会出现"改了对比设备但类别没重算"的空窗口。
 *
 * 写入 queryForm.compareCategory 是有意的：类别交集变化后若当前选中项不在交集里，
 * 必须回到交集的第一项，否则下拉显示一个取不到数的类别（原实现即如此）。
 */
import { ref, watch, type Ref } from 'vue'
import client from '@/api/client'
import { logger } from '@/utils/logger'
import type { EdgeDevice } from '@/api/edgeDevice'
import { unwrapList } from '../envelope'
import type { MeasurementCategory, MeasurementPoint } from '../types'

/** 对比链的输入：设备列表（选项）与页面筛选表单（对比类别是双向的）。 */
export interface DataPanelCompareSource {
  deviceList: Ref<EdgeDevice[]>
  /** 只用到 compareCategory：读它取数，也写它（交集变化时回到第一项）。 */
  queryForm: { compareCategory: string }
}

export function useDataPanelCompare(source: DataPanelCompareSource) {
  // 多设备对比
  const compareMode = ref(false)
  const compareDevices = ref<number[]>([])
  const compareSeries = ref<any[]>([])
  const compareCategories = ref<MeasurementCategory[]>([])

  const loadCompareCategories = async () => {
    if (compareDevices.value.length < 2) {
      compareCategories.value = []
      return
    }
    try {
      const categoryLists = await Promise.all(compareDevices.value.map(async (deviceId) => {
        const response = await client.get<unknown, MeasurementCategory[]>('/api/v1/unified-data/categories', { params: { device_pk: deviceId } })
        return unwrapList<MeasurementCategory>(response)
      }))
      const [first, ...rest] = categoryLists
      compareCategories.value = (first || []).filter((candidate: MeasurementCategory) =>
        rest.every(list => list.some((item: MeasurementCategory) => item.code === candidate.code && item.unit === candidate.unit)),
      )
      if (!compareCategories.value.some(category => category.code === source.queryForm.compareCategory)) {
        source.queryForm.compareCategory = compareCategories.value[0]?.code || ''
      }
    } catch (error) {
      logger.warn('获取可比较指标失败', { error: String(error) })
      compareCategories.value = []
      source.queryForm.compareCategory = ''
    }
  }

  // 获取多设备对比数据
  const fetchCompareData = async () => {
    if (compareDevices.value.length < 2) return

    compareSeries.value = []
    const endTime = new Date()
    const startTime = new Date(endTime.getTime() - 24 * 60 * 60 * 1000) // 固定 24h

    try {
      const promises = compareDevices.value.map(async (deviceId) => {
        const response = await client.get<unknown, MeasurementPoint[]>(`/api/v1/unified-data/historical`, {
          params: {
            device_pk: deviceId,
            category: source.queryForm.compareCategory,
            start_time: startTime.toISOString(),
            end_time: endTime.toISOString(),
            max_points: 500
          }
        })
        return {
          deviceId,
          deviceName: source.deviceList.value.find(d => d.id === deviceId)?.name || String(deviceId),
          data: unwrapList<MeasurementPoint>(response)
        }
      })

      const results = await Promise.all(promises)

      compareSeries.value = results.map(r => ({
        name: r.deviceName,
        unit: '',
        data: r.data
          .filter((item) => {
            const t = item.timestamp || item.created_at
            return t && !t.startsWith('0001-01-01')
          })
          .map((item) => ({
            time: item.timestamp || item.created_at,
            value: item.value
          }))
      }))
    } catch (error) {
      logger.error('获取对比数据失败', { error: String(error) })
    }
  }

  watch(compareDevices, async () => {
    if (compareMode.value && compareDevices.value.length >= 2) {
      await loadCompareCategories()
      if (source.queryForm.compareCategory) await fetchCompareData()
    }
  })

  watch(() => source.queryForm.compareCategory, () => {
    if (compareMode.value && compareDevices.value.length >= 2) {
      fetchCompareData()
    }
  })

  return { compareMode, compareDevices, compareSeries, compareCategories }
}
