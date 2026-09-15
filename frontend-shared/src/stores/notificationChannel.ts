import { defineStore } from 'pinia'
import { ref } from 'vue'
import {
  notificationChannelApi,
  type NotificationChannel,
  type NotificationChannelListParams,
} from '@/api/notificationChannel'

/**
 * 外发通知通道 store（本轮只做列表 + 删除；创建/编辑/测试留待下一轮）。
 *
 * 与 stores/dataSource.ts 同一纪律：成功后本地同步，失败写 error **并 rethrow**，
 * 由页面决定提示文案（store 不弹 toast，弹窗归页面，便于测试与复用）。
 */
export const useNotificationChannelStore = defineStore('notificationChannel', () => {
  const items = ref<NotificationChannel[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)

  /** 统一把异常文案写入 error（页面负责提示与"重试"入口） */
  function captureError(err: unknown) {
    error.value = err instanceof Error ? err.message : String(err)
  }

  /**
   * 拉取列表（真分页：page / page_size 原样发给后端，绝不本地切片）。
   *
   * items 一律用**响应整体替换**而不是追加：翻页后本地数组必须只代表当前页，
   * 追加会留下上一页的行 —— 那正是"分页器说第 2 页、表格里却有 40 行"的假象来源。
   */
  async function fetchList(params?: NotificationChannelListParams) {
    loading.value = true
    try {
      const res = await notificationChannelApi.list(params)
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

  /**
   * 删除通道。
   *
   * 本地只做"从当前页摘掉这一行"；total 与越界回退交给页面重新 fetchList 取真值 ——
   * 在这里减 total 会让分页器与后端在并发/失败场景下静默错位（见 DataSourceList.vue 的
   * removeSource 注释：本地过滤后 total 仍是删除前的数）。
   */
  async function removeChannel(id: number) {
    loading.value = true
    try {
      await notificationChannelApi.remove(id)
      items.value = items.value.filter((c) => c.id !== id)
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

  return { items, total, loading, error, fetchList, removeChannel, clearError }
})
