import { defineStore } from 'pinia'
import { ref } from 'vue'
import { firmwareApi, type Firmware, type FirmwareListParams, type FirmwareUpdatePayload } from '@/api/firmware'
import { registerSessionCacheClearer } from '@/utils/sessionCache'

const LIST_CACHE_TTL = 60_000 // 60s — firmware list rarely changes

export const useFirmwareStore = defineStore('firmware', () => {
  const list = ref<Firmware[]>([])
  const total = ref(0)
  const loading = ref(false)
  let fetchedAt = 0
  let lastParams = ''
  let cacheEpoch = 0
  let requestSequence = 0
  let sessionGeneration = 0
  const deletingIds = new Set<number>()
  const deletePromises = new Map<number, Promise<void>>()

  function isFresh(): boolean {
    return Date.now() - fetchedAt < LIST_CACHE_TTL
  }

  async function fetchList(params?: FirmwareListParams, force = false) {
    const paramsKey = JSON.stringify(params || {})
    if (!force && isFresh() && paramsKey === lastParams) {
      return // cache hit
    }
    loading.value = true
    const sequence = ++requestSequence
    const requestEpoch = cacheEpoch
    try {
      const response = await firmwareApi.getList(params)
      if (requestEpoch !== cacheEpoch || sequence !== requestSequence) return
      list.value = response.list
      total.value = response.total
      fetchedAt = Date.now()
      lastParams = paramsKey
    } finally {
      if (sequence === requestSequence) loading.value = false
    }
  }

  async function upload(formData: FormData) {
    const generation = sessionGeneration
    const fw = await firmwareApi.upload(formData)
    if (generation !== sessionGeneration) throw new Error('会话已变更')
    // Invalidate cache — new firmware added
    fetchedAt = 0
    return fw
  }

  /**
   * 编辑（契约：只允许 changelog / channel / min_from_version）。
   * version 由镜像决定、target_model/stable 列已删 —— 传了后端也不会接受。
   */
  async function update(id: number, data: FirmwareUpdatePayload) {
    const generation = sessionGeneration
    await firmwareApi.update(id, data)
    if (generation !== sessionGeneration) throw new Error('会话已变更')
    // Update local cache
    const idx = list.value.findIndex(f => f.id === id)
    if (idx >= 0) {
      Object.assign(list.value[idx], data)
    }
  }

  /**
   * 撤回（有在途任务 → 409 FIRMWARE_IN_USE，由调用方展示原因）。
   *
   * 不做乐观更新：撤回是可恢复态（withdrawn_at），失败时回滚成本高于直接重取；
   * 成功后把缓存标记为过期，由页面重新拉列表拿到服务端权威状态。
   */
  async function withdraw(id: number) {
    const generation = sessionGeneration
    await firmwareApi.withdraw(id)
    if (generation !== sessionGeneration) throw new Error('会话已变更')
    fetchedAt = 0
  }

  function deleteFirmware(id: number): Promise<void> {
    const existing = deletePromises.get(id)
    if (existing) return existing
    const operation = deleteFirmwareOnce(id)
    deletePromises.set(id, operation)
    operation.finally(() => {
      if (deletePromises.get(id) === operation) deletePromises.delete(id)
    }).catch(() => undefined)
    return operation
  }

  async function deleteFirmwareOnce(id: number) {
    const generation = sessionGeneration
    const removed = list.value.find(f => f.id === id)
    const removedIndex = list.value.findIndex(f => f.id === id)
    deletingIds.add(id)
    if (removedIndex >= 0) {
      list.value.splice(removedIndex, 1)
      total.value = Math.max(0, total.value - 1)
    }
    try {
      await firmwareApi.delete(id)
      if (generation !== sessionGeneration) throw new Error('会话已变更')
    } catch (err) {
      if (generation !== sessionGeneration) throw err
      if (removed && !list.value.some(f => f.id === id)) {
        list.value.splice(Math.min(removedIndex, list.value.length), 0, removed)
        total.value++
      }
      throw err
    } finally {
      deletingIds.delete(id)
    }
  }

  function clearCache() {
    sessionGeneration++
    cacheEpoch++
    requestSequence++
    list.value = []
    total.value = 0
    loading.value = false
    fetchedAt = 0
    lastParams = ''
    deletingIds.clear()
    deletePromises.clear()
  }

  return {
    list,
    total,
    loading,
    fetchList,
    upload,
    update,
    withdraw,
    deleteFirmware,
    clearCache,
    isFresh,
  }
})

registerSessionCacheClearer(() => useFirmwareStore().clearCache())
