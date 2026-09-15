import { defineStore } from 'pinia'
import { ref } from 'vue'
import {
  notificationChannelApi,
  type NotificationChannel,
  type NotificationChannelCreatePayload,
  type NotificationChannelListParams,
  type NotificationChannelUpdatePayload,
} from '@/api/notificationChannel'

/**
 * 外发通知通道 store（列表 + 删除 + 创建/编辑 + 测试投递）。
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

  /**
   * 创建通道。成功后把后端回读的视图**插到当前页首**（列表按 id DESC 排序，
   * 新建的 id 最大 ⇒ 它本来就该在第 1 页第一行）。total 由页面重新 fetchList 取真值。
   */
  async function createChannel(payload: NotificationChannelCreatePayload) {
    loading.value = true
    try {
      const created = await notificationChannelApi.create(payload)
      items.value = [created, ...items.value]
      error.value = null
      return created
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  /**
   * 更新通道：局部替换列表里的对应行（用后端回读的视图，而不是本地拼的期望值）。
   *
   * secret 三态**完全由 payload 决定**（见 api/notificationChannel.ts 的类型注释）：
   * store 不碰 secret 字段，绝不做"从既有行里补一个 secret 回去"这类自作主张 ——
   * 既有行里只有 secret_hint，把它补进请求体就等于用"末 4 位"覆盖真实密钥。
   */
  async function updateChannel(id: number, payload: NotificationChannelUpdatePayload) {
    loading.value = true
    try {
      const updated = await notificationChannelApi.update(id, payload)
      items.value = items.value.map((c) => (c.id === id ? updated : c))
      error.value = null
      return updated
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  /**
   * 发送测试消息。**不写 loading**：测试按钮有自己的 per-row actingId 状态，
   * 复用全局 loading 会让整张表在测试期间一起转圈（表级 loading 遮罩），
   * 掩盖用户正在操作的是哪一行。
   */
  async function testChannel(id: number) {
    return notificationChannelApi.test(id)
  }

  function clearError() {
    error.value = null
  }

  return {
    items, total, loading, error,
    fetchList, removeChannel, createChannel, updateChannel, testChannel, clearError,
  }
})
