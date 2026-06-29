import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { edgeDeviceApi, type EdgeDevice, type EdgeDeviceListParams } from '@/api/edgeDevice'

const LIST_CACHE_TTL = 30_000 // 30s — list data is shared across views
const DETAIL_CACHE_TTL = 10_000 // 10s — detail may have fresher last_data

export const useEdgeDeviceStore = defineStore('edgeDevice', () => {
  // ── List state ──
  const list = ref<EdgeDevice[]>([])
  const listTotal = ref(0)
  const listLoading = ref(false)
  let listFetchedAt = 0
  let lastListParams = ''

  // ── Detail cache (id → device) ──
  const detailCache = ref<Map<number, { device: EdgeDevice; ts: number }>>(new Map())

  // ── Getters ──
  const onlineCount = computed(() => list.value.filter(d => d.status === 'online' || d.status === 'active').length)
  const offlineCount = computed(() => list.value.filter(d => d.status === 'offline').length)

  function isListFresh(): boolean {
    return Date.now() - listFetchedAt < LIST_CACHE_TTL
  }

  function isDetailFresh(id: number): boolean {
    const entry = detailCache.value.get(id)
    return !!entry && Date.now() - entry.ts < DETAIL_CACHE_TTL
  }

  // ── Actions ──

  /**
   * Fetch edge device list. Uses cache when params match and TTL hasn't expired.
   * Pass force=true to bypass cache.
   */
  async function fetchList(params?: EdgeDeviceListParams, force = false) {
    const paramsKey = JSON.stringify(params || {})
    if (!force && isListFresh() && paramsKey === lastListParams) {
      return // cache hit
    }
    listLoading.value = true
    try {
      const response = await edgeDeviceApi.getList(params)
      list.value = response.items
      listTotal.value = response.total
      listFetchedAt = Date.now()
      lastListParams = paramsKey
    } finally {
      listLoading.value = false
    }
  }

  /**
   * Fetch single edge device detail. Uses cache when available.
   */
  async function fetchDetail(id: number, force = false) {
    if (!force && isDetailFresh(id)) {
      return detailCache.value.get(id)!.device
    }
    const device = await edgeDeviceApi.getDetail(id)
    detailCache.value.set(id, { device, ts: Date.now() })
    return device
  }

  /**
   * Get cached detail without fetching. Returns undefined if not cached.
   */
  function getCachedDetail(id: number): EdgeDevice | undefined {
    return detailCache.value.get(id)?.device
  }

  /**
   * Optimistic delete: remove from local list immediately, then call API.
   * On failure, re-fetch to restore.
   */
  async function deleteDevice(id: number) {
    const prevList = [...list.value]
    // Optimistic removal
    list.value = list.value.filter(d => d.id !== id)
    detailCache.value.delete(id)
    listTotal.value = Math.max(0, listTotal.value - 1)
    try {
      await edgeDeviceApi.delete(id)
    } catch (err) {
      // Rollback on failure
      list.value = prevList
      listTotal.value = prevList.length
      throw err
    }
  }

  /**
   * Update a device in local cache (after edit operations).
   */
  function updateLocal(device: EdgeDevice) {
    const idx = list.value.findIndex(d => d.id === device.id)
    if (idx >= 0) {
      list.value[idx] = device
    }
    detailCache.value.set(device.id, { device, ts: Date.now() })
  }

  /**
   * Clear all caches (on logout, etc).
   */
  function clearCache() {
    list.value = []
    listTotal.value = 0
    listFetchedAt = 0
    lastListParams = ''
    detailCache.value.clear()
  }

  return {
    // State
    list,
    listTotal,
    listLoading,
    // Getters
    onlineCount,
    offlineCount,
    // Actions
    fetchList,
    fetchDetail,
    getCachedDetail,
    deleteDevice,
    updateLocal,
    clearCache,
    isListFresh,
  }
})
