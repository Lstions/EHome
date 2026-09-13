import { defineStore } from 'pinia'
import { ref } from 'vue'
import { nodeApi, type NodeListParams, type Node } from '@/api/node'
import { registerSessionCacheClearer } from '@/utils/sessionCache'

const DETAIL_CACHE_TTL = 15_000 // 15s
const LIST_CACHE_TTL = 15_000

// 缓存键必须**逐个枚举**所有"视图层真的会传且影响结果"的字段。
//
// 为什么不能 `...params` 展开: 展开会把语义相同但拼写不同的参数带进来
// (如 {page:1} 与 {page:1, status: undefined} 会产生两个键), 缓存永不命中,
// 每次翻页/挂载都重新打请求。所以保持显式列举 —— 但**每新增一个筛选参数都必须
// 同步加进这个键**。
//
// 为什么"键不全"是**独立缺陷**而不只是白名单缺口: 只要视图层传了 search 而这里
// 不认, 两个不同检索就会落到**同一个键**上 —— fetchNodes 命中新鲜缓存直接 return,
// 页面显示上一次的结果、**且从不发请求**。这是"看起来在工作、实际返回别人的数据"。
// 跨视图后果更重: ChannelList.vue:366 用 getCachedList({page:1,page_size:20}) 做
// **节点名映射**, 带筛选的结果一旦写进这个"无筛选"键, 通道页的名称映射就会被打空/打错。
function listCacheKey(params?: NodeListParams): string {
  return JSON.stringify({
    status: params?.status || '',
    model: params?.model || '',
    search: params?.search || '',
    page: params?.page || 1,
    page_size: params?.page_size || 20,
  })
}

export const useNodeStore = defineStore('node', () => {
  const nodes = ref<Node[]>([])
  const total = ref(0)
  const loading = ref(false)
  const listCache = new Map<string, { nodes: Node[]; total: number; ts: number }>()
  const inFlight = new Map<string, { promise: Promise<void>; sequence: number }>()
  const latestRequestByKey = new Map<string, number>()
  let activeListKey = ''
  let activeRequestSequence = 0
  let requestSequence = 0
  let cacheEpoch = 0
  let sessionGeneration = 0
  let pendingListRequests = 0

  // Detail cache (node_id string → { node, ts })
  const detailCache = ref<Map<string, { node: Node; ts: number }>>(new Map())
  const latestDetailRequest = new Map<string, number>()
  let detailRequestSequence = 0
  let detailEpoch = 0

  function hasFreshList(params?: NodeListParams): boolean {
    const cached = listCache.get(listCacheKey(params))
    return !!cached && Date.now() - cached.ts < LIST_CACHE_TTL
  }

  function hasCachedList(params?: NodeListParams): boolean {
    return listCache.has(listCacheKey(params))
  }

  function getCachedList(params?: NodeListParams): { items: Node[]; total: number } | undefined {
    const cached = listCache.get(listCacheKey(params))
    return cached ? { items: cached.nodes, total: cached.total } : undefined
  }

  async function fetchNodes(params?: NodeListParams, force = false) {
    const key = listCacheKey(params)
    activeListKey = key
    const cached = listCache.get(key)
    if (!force && cached && Date.now() - cached.ts < LIST_CACHE_TTL) {
      nodes.value = cached.nodes
      total.value = cached.total
      return
    }
    const existing = !force ? inFlight.get(key) : undefined
    if (existing) {
      activeRequestSequence = existing.sequence
      return existing.promise
    }

    const sequence = ++requestSequence
    const requestEpoch = cacheEpoch
    latestRequestByKey.set(key, sequence)
    activeRequestSequence = sequence
    pendingListRequests++
    loading.value = true
    let requestReleased = false
    const request = (async () => {
      const response = await nodeApi.getList(params)
      if (cacheEpoch !== requestEpoch || latestRequestByKey.get(key) !== sequence) return
      listCache.set(key, { nodes: response.items, total: response.total, ts: Date.now() })
      if (activeListKey === key && activeRequestSequence === sequence) {
        nodes.value = response.items
        total.value = response.total
      }
    })().finally(() => {
      if (inFlight.get(key)?.promise === request) inFlight.delete(key)
      if (cacheEpoch === requestEpoch && !requestReleased) pendingListRequests--
      requestReleased = true
      loading.value = pendingListRequests > 0
    })
    // Keep the latest request (including forced refreshes) joinable so a
    // concurrent page mount does not attach to an older superseded request.
    inFlight.set(key, { promise: request, sequence })
    return request
  }

  async function fetchDetail(id: string | number, force = false) {
    const key = String(id)
    const cached = detailCache.value.get(key)
    if (!force && cached && Date.now() - cached.ts < DETAIL_CACHE_TTL) {
      return cached.node
    }
    const sequence = ++detailRequestSequence
    const requestEpoch = detailEpoch
    latestDetailRequest.set(key, sequence)
    const node = await nodeApi.getDetail(id)
    if (detailEpoch === requestEpoch && sequence === detailRequestSequence && latestDetailRequest.get(key) === sequence) {
      const entry = { node, ts: Date.now() }
      detailCache.value.set(key, entry)
      detailCache.value.set(String(node.id), entry)
      detailCache.value.set(String(node.node_id), entry)
    }
    return node
  }

  function getCachedDetail(id: string | number): Node | undefined {
    return detailCache.value.get(String(id))?.node
  }

  async function updateNode(id: string | number, data: { name?: string }) {
    const generation = sessionGeneration
    await nodeApi.update(id, data)
    if (generation !== sessionGeneration) throw new Error('会话已变更')
    detailEpoch++
    // Update local cache
    const key = String(id)
    latestDetailRequest.clear()
    for (const cached of new Set(detailCache.value.values())) {
      if (String(cached.node.id) === key || String(cached.node.node_id) === key) {
        Object.assign(cached.node, data)
        cached.ts = Date.now()
      }
    }
    // Update list cache
    const idx = nodes.value.findIndex(n => String(n.id) === key || String(n.node_id) === key)
    if (idx >= 0) {
      Object.assign(nodes.value[idx], data)
    }
    for (const cached of listCache.values()) {
      const cachedNode = cached.nodes.find(n => String(n.id) === key || String(n.node_id) === key)
      if (cachedNode) Object.assign(cachedNode, data)
    }
  }

  async function deleteNode(id: number | string) {
    const generation = sessionGeneration
    await nodeApi.delete(id)
    if (generation !== sessionGeneration) throw new Error('会话已变更')
    cacheEpoch++
    detailEpoch++
    // Optimistic removal
    nodes.value = nodes.value.filter(n => n.id !== id && n.node_id !== String(id))
    total.value = Math.max(0, total.value - 1)
    for (const [cacheKey, cached] of detailCache.value.entries()) {
      if (String(cached.node.id) === String(id) || String(cached.node.node_id) === String(id)) {
        detailCache.value.delete(cacheKey)
      }
    }
    latestDetailRequest.clear()
    listCache.clear()
    inFlight.clear()
    latestRequestByKey.clear()
    activeListKey = ''
    activeRequestSequence = 0
  }

  function clearCache() {
    sessionGeneration++
    nodes.value = []
    total.value = 0
    loading.value = false
    pendingListRequests = 0
    listCache.clear()
    inFlight.clear()
    latestRequestByKey.clear()
    activeListKey = ''
    activeRequestSequence = 0
    cacheEpoch++
    detailEpoch++
    latestDetailRequest.clear()
    detailCache.value.clear()
  }

  return {
    nodes,
    total,
    loading,
    hasCachedList,
    hasFreshList,
    getCachedList,
    fetchNodes,
    fetchDetail,
    getCachedDetail,
    updateNode,
    deleteNode,
    clearCache,
  }
})

registerSessionCacheClearer(() => useNodeStore().clearCache())
