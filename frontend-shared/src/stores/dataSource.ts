import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
  dataSourceApi,
  type DataSource,
  type DataSourceHealth,
  type DataSourceListParams,
  type FailoverLog,
  type CreateDataSourceRequest,
  type UpdateDataSourceRequest,
} from '@/api/dataSource'

/**
 * 数据源主备 store (设计 §8, API 契约 §7)
 * 列表/CRUD/状态操作 + 健康记录与切换日志; 成功后本地同步, 失败写 error 并 rethrow (页面提示)。
 */
export const useDataSourceStore = defineStore('dataSource', () => {
  // ── 来源列表 ──
  const items = ref<DataSource[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)

  // ── 健康记录 / 切换日志 ──
  const health = ref<DataSourceHealth[]>([])
  const healthLoading = ref(false)
  const failoverLogs = ref<FailoverLog[]>([])
  const failoverLoading = ref(false)

  const activeCount = computed(() => items.value.filter(s => s.status === 'active').length)
  const standbyCount = computed(() => items.value.filter(s => s.status === 'standby').length)
  const errorCount = computed(() => items.value.filter(s => s.status === 'error').length)
  const disabledCount = computed(() => items.value.filter(s => s.status === 'disabled').length)

  /** 统一把异常文案写入 error (页面负责提示) */
  function captureError(err: unknown) {
    error.value = err instanceof Error ? err.message : String(err)
  }

  /** 用后端返回值替换本地对应项 (update 与状态操作共用) */
  function replaceItem(updated: DataSource) {
    const idx = items.value.findIndex(s => s.id === updated.id)
    if (idx >= 0) items.value[idx] = updated
  }

  /** 拉取来源列表 (分页 + 过滤) */
  async function fetchList(params?: DataSourceListParams) {
    loading.value = true
    try {
      const res = await dataSourceApi.list(params)
      items.value = res.items
      total.value = res.total
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  /** 新建来源; 成功后本地前插 */
  async function createSource(req: CreateDataSourceRequest) {
    loading.value = true
    try {
      const created = await dataSourceApi.create(req)
      items.value = [created, ...items.value]
      return created
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  /** 更新来源; 成功后本地替换 */
  async function updateSource(id: number, req: UpdateDataSourceRequest) {
    loading.value = true
    try {
      const updated = await dataSourceApi.update(id, req)
      replaceItem(updated)
      return updated
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  /** 删除来源; 成功后本地过滤 */
  async function removeSource(id: number) {
    loading.value = true
    try {
      await dataSourceApi.remove(id)
      items.value = items.value.filter(s => s.id !== id)
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  /** 手动切换为权威来源; 成功后用返回值替换本地项 */
  async function activateSource(id: number) {
    loading.value = true
    try {
      const updated = await dataSourceApi.activate(id)
      replaceItem(updated)
      return updated
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  /** 停用来源; 成功后用返回值替换本地项 */
  async function deactivateSource(id: number) {
    loading.value = true
    try {
      const updated = await dataSourceApi.deactivate(id)
      replaceItem(updated)
      return updated
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  /** 重置熔断来源; 成功后用返回值替换本地项 */
  async function resetSource(id: number) {
    loading.value = true
    try {
      const updated = await dataSourceApi.reset(id)
      replaceItem(updated)
      return updated
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      loading.value = false
    }
  }

  /** 拉取来源健康记录 */
  async function fetchHealth(id: number, limit?: number) {
    healthLoading.value = true
    try {
      health.value = await dataSourceApi.getHealth(id, limit)
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      healthLoading.value = false
    }
  }

  /** 拉取设备切换日志 */
  async function fetchFailoverLogs(deviceId: number, params?: { limit?: number; category?: string }) {
    failoverLoading.value = true
    try {
      failoverLogs.value = await dataSourceApi.getFailoverLogs(deviceId, params)
    } catch (err) {
      captureError(err)
      throw err
    } finally {
      failoverLoading.value = false
    }
  }

  /** 清空错误信息 (页面提示后调用) */
  function clearError() {
    error.value = null
  }

  return {
    items,
    total,
    loading,
    error,
    health,
    healthLoading,
    failoverLogs,
    failoverLoading,
    activeCount,
    standbyCount,
    errorCount,
    disabledCount,
    fetchList,
    createSource,
    updateSource,
    removeSource,
    activateSource,
    deactivateSource,
    resetSource,
    fetchHealth,
    fetchFailoverLogs,
    clearError,
  }
})
