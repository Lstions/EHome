import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { edgeDeviceApi, type EdgeDevice, type EdgeDeviceListParams } from '@/api/edgeDevice'
import { registerSessionCacheClearer } from '@/utils/sessionCache'

const LIST_CACHE_TTL = 30_000 // 30s — list data is shared across views
const DETAIL_CACHE_TTL = 10_000 // 10s — detail may have fresher last_data

function listCacheKey(params?: EdgeDeviceListParams): string {
  return JSON.stringify({
    node_id: params?.node_id ?? '',
    device_type: params?.device_type || '',
    status: params?.status || '',
    page: params?.page || 1,
    page_size: params?.page_size || 20,
  })
}

export const useEdgeDeviceStore = defineStore('edgeDevice', () => {
  // ── List state ──
  const list = ref<EdgeDevice[]>([])
  const listTotal = ref(0)
  const listLoading = ref(false)
  const listCache = new Map<string, { items: EdgeDevice[]; total: number; ts: number }>()
  const inFlight = new Map<string, { promise: Promise<void>; sequence: number }>()
  const latestRequestByKey = new Map<string, number>()
  let activeListKey = ''
  let activeRequestSequence = 0
  let requestSequence = 0
  let cacheEpoch = 0
  let sessionGeneration = 0
  let pendingListRequests = 0

  // ── Detail cache (id → device) ──
  const detailCache = ref<Map<number, { device: EdgeDevice; ts: number }>>(new Map())
  const latestDetailRequest = new Map<number, number>()
  let detailRequestSequence = 0

  // ── Getters ──
  const onlineCount = computed(() => list.value.filter(d => d.status === 'online' || d.status === 'active').length)
  const offlineCount = computed(() => list.value.filter(d => d.status === 'offline').length)

  function hasFreshList(params?: EdgeDeviceListParams): boolean {
    const paramsKey = listCacheKey(params)
    const cached = listCache.get(paramsKey)
    return !!cached && Date.now() - cached.ts < LIST_CACHE_TTL
  }

  function hasCachedList(params?: EdgeDeviceListParams): boolean {
    return listCache.has(listCacheKey(params))
  }

  function getCachedList(params?: EdgeDeviceListParams): { items: EdgeDevice[]; total: number } | undefined {
    const cached = listCache.get(listCacheKey(params))
    return cached ? { items: cached.items, total: cached.total } : undefined
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
    const paramsKey = listCacheKey(params)
    activeListKey = paramsKey
    if (!force && hasFreshList(params)) {
      const cached = listCache.get(paramsKey)!
      list.value = cached.items
      listTotal.value = cached.total
      return // cache hit
    }
    const existing = !force ? inFlight.get(paramsKey) : undefined
    if (existing) {
      activeRequestSequence = existing.sequence
      return existing.promise
    }

    const sequence = ++requestSequence
    const requestEpoch = cacheEpoch
    latestRequestByKey.set(paramsKey, sequence)
    activeRequestSequence = sequence
    pendingListRequests++
    listLoading.value = true
    let requestReleased = false
    const request = (async () => {
      const response = await edgeDeviceApi.getList(params)
      if (cacheEpoch !== requestEpoch || latestRequestByKey.get(paramsKey) !== sequence) return
      listCache.set(paramsKey, { items: response.items, total: response.total, ts: Date.now() })
      if (activeListKey === paramsKey && activeRequestSequence === sequence) {
        list.value = response.items
        listTotal.value = response.total
      }
    })().finally(() => {
      if (inFlight.get(paramsKey)?.promise === request) inFlight.delete(paramsKey)
      if (cacheEpoch === requestEpoch && !requestReleased) pendingListRequests--
      requestReleased = true
      listLoading.value = pendingListRequests > 0
    })
    // Keep the latest request (including forced refreshes) joinable so a
    // concurrent page mount does not attach to an older superseded request.
    inFlight.set(paramsKey, { promise: request, sequence })
    return request
  }

  /**
   * Fetch single edge device detail. Uses cache when available.
   */
  async function fetchDetail(id: number, force = false) {
    if (!force && isDetailFresh(id)) {
      return detailCache.value.get(id)!.device
    }
    const sequence = ++detailRequestSequence
    const requestEpoch = cacheEpoch
    latestDetailRequest.set(id, sequence)
    const device = await edgeDeviceApi.getDetail(id)
    if (cacheEpoch === requestEpoch && latestDetailRequest.get(id) === sequence) {
      detailCache.value.set(id, { device, ts: Date.now() })
    }
    return device
  }

  /**
   * Get cached detail without fetching. Returns undefined if not cached.
   */
  function getCachedDetail(id: number): EdgeDevice | undefined {
    return detailCache.value.get(id)?.device
  }

  /** Delete remotely, then consistently remove it from all local list entries. */
  async function deleteDevice(id: number) {
    const generation = sessionGeneration
    await edgeDeviceApi.delete(id)
    if (generation !== sessionGeneration) throw new Error('会话已变更')
    cacheEpoch++
    inFlight.clear()
    latestRequestByKey.clear()
    activeRequestSequence = 0
    list.value = list.value.filter(d => d.id !== id)
    detailCache.value.delete(id)
    latestDetailRequest.delete(id)
    listTotal.value = Math.max(0, listTotal.value - 1)
    for (const cached of listCache.values()) {
      const containedDeletedDevice = cached.items.some(d => d.id === id)
      if (containedDeletedDevice) {
        cached.items = cached.items.filter(d => d.id !== id)
      }
      // Exact total/page membership is unknowable for filtered and paginated
      // queries. Keep stale values only as display fallback and force revalidation.
      cached.ts = 0
    }
  }

  /**
   * Update a device in local cache (after edit operations).
   */
  function updateLocal(device: EdgeDevice) {
    latestDetailRequest.set(device.id, ++detailRequestSequence)
    const idx = list.value.findIndex(d => d.id === device.id)
    if (idx >= 0) {
      list.value[idx] = device
    }
    for (const cached of listCache.values()) {
      const cachedIndex = cached.items.findIndex(d => d.id === device.id)
      if (cachedIndex >= 0) cached.items[cachedIndex] = device
    }
    detailCache.value.set(device.id, { device, ts: Date.now() })
  }

  function invalidateDetail(id: number) {
    latestDetailRequest.set(id, ++detailRequestSequence)
    detailCache.value.delete(id)
  }

  function invalidateLists() {
    cacheEpoch++
    inFlight.clear()
    latestRequestByKey.clear()
    activeRequestSequence = 0
    for (const cached of listCache.values()) cached.ts = 0
  }

  /**
   * Clear all caches (on logout, etc).
   */
  function clearCache() {
    sessionGeneration++
    list.value = []
    listTotal.value = 0
    listLoading.value = false
    pendingListRequests = 0
    listCache.clear()
    inFlight.clear()
    latestRequestByKey.clear()
    activeListKey = ''
    activeRequestSequence = 0
    cacheEpoch++
    latestDetailRequest.clear()
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
    invalidateDetail,
    invalidateLists,
    clearCache,
    hasCachedList,
    hasFreshList,
    getCachedList,
  }
})

registerSessionCacheClearer(() => useEdgeDeviceStore().clearCache())
