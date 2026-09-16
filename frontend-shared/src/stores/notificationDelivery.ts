import { defineStore } from 'pinia'
import { ref } from 'vue'
import {
  notificationChannelApi,
  type NotificationDelivery,
  type NotificationDeliveryListParams,
} from '@/api/notificationChannel'

/**
 * 投递审计 store（只读列表：过滤 + 真分页）。
 *
 * 为什么单独开一个 store 而不是塞进 stores/notificationChannel：
 * 两个列表的**分页游标与过滤条件完全独立**（通道页看的是配置，审计页看的是历史投递），
 * 共用一份 items/total 会让"翻审计页把通道页的页码也带偏"成为可能；
 * 而它们共用的是 api 层（同一个功能域，接口聚在 api/notificationChannel.ts）。
 *
 * 与 stores/notificationChannel.ts 同一纪律：失败写 error **并 rethrow**，
 * 由页面决定提示文案（store 不弹 toast，弹窗归页面，便于测试与复用）。
 * 本店没有写操作 —— 审计行是后端投递引擎写的，前端只读（不做删除/导出，规范 §7）。
 */
export const useNotificationDeliveryStore = defineStore('notificationDelivery', () => {
  const items = ref<NotificationDelivery[]>([])
  /** **全量**总数（后端 Count 结果），不是当前页条数 —— 分页器按它算页数 */
  const total = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)

  /** 统一把异常文案写入 error（页面负责提示与"重试"入口） */
  function captureError(err: unknown) {
    error.value = err instanceof Error ? err.message : String(err)
  }

  /**
   * 拉取审计列表（真分页：page / page_size 原样发给后端，绝不本地切片）。
   *
   * items 一律用**响应整体替换**而不是追加：翻页后本地数组必须只代表当前页，
   * 追加会留下上一页的行 —— 那正是"分页器说第 2 页、表格里却有 40 行"的假象来源。
   *
   * 过滤条件也**不在本地过滤**：channel_id / state 是发给后端的查询参数，
   * 本地过滤只能筛当前页，会得到"本页看起来没有失败投递"这种错误的空态。
   */
  async function fetchList(params?: NotificationDeliveryListParams) {
    loading.value = true
    try {
      const res = await notificationChannelApi.listDeliveries(params)
      items.value = Array.isArray(res?.items) ? res.items : []
      total.value = Number.isFinite(res?.total) ? res.total : 0
      error.value = null
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  function clearError() {
    error.value = null
  }

  return { items, total, loading, error, fetchList, clearError }
})
