import { defineStore } from 'pinia'
import { ref } from 'vue'
import { firmwareApi, type Firmware, type FirmwareListParams } from '@/api/firmware'
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

  async function update(id: number, data: { version?: string; changelog?: string; target_model?: string; stable?: boolean }) {
    const generation = sessionGeneration
    await firmwareApi.update(id, data)
    if (generation !== sessionGeneration) throw new Error('会话已变更')
    // Update local cache
    const idx = list.value.findIndex(f => f.id === id)
    if (idx >= 0) {
      Object.assign(list.value[idx], data)
    }
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
    deleteFirmware,
    clearCache,
    isFresh,
  }
})

registerSessionCacheClearer(() => useFirmwareStore().clearCache())
