import { defineStore } from 'pinia'
import { ref } from 'vue'
import { firmwareApi, type Firmware, type FirmwareListParams } from '@/api/firmware'

const LIST_CACHE_TTL = 60_000 // 60s — firmware list rarely changes

export const useFirmwareStore = defineStore('firmware', () => {
  const list = ref<Firmware[]>([])
  const total = ref(0)
  const loading = ref(false)
  let fetchedAt = 0
  let lastParams = ''

  function isFresh(): boolean {
    return Date.now() - fetchedAt < LIST_CACHE_TTL
  }

  async function fetchList(params?: FirmwareListParams, force = false) {
    const paramsKey = JSON.stringify(params || {})
    if (!force && isFresh() && paramsKey === lastParams) {
      return // cache hit
    }
    loading.value = true
    try {
      const response = await firmwareApi.getList(params)
      list.value = response.list
      total.value = response.total
      fetchedAt = Date.now()
      lastParams = paramsKey
    } finally {
      loading.value = false
    }
  }

  async function upload(formData: FormData) {
    const fw = await firmwareApi.upload(formData)
    // Invalidate cache — new firmware added
    fetchedAt = 0
    return fw
  }

  async function update(id: number, data: { version?: string; changelog?: string; target_model?: string; stable?: boolean }) {
    await firmwareApi.update(id, data)
    // Update local cache
    const idx = list.value.findIndex(f => f.id === id)
    if (idx >= 0) {
      Object.assign(list.value[idx], data)
    }
  }

  async function deleteFirmware(id: number) {
    const prevList = [...list.value]
    list.value = list.value.filter(f => f.id !== id)
    total.value = Math.max(0, total.value - 1)
    try {
      await firmwareApi.delete(id)
    } catch (err) {
      list.value = prevList
      total.value = prevList.length
      throw err
    }
  }

  function clearCache() {
    list.value = []
    total.value = 0
    fetchedAt = 0
    lastParams = ''
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
