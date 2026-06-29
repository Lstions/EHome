import { defineStore } from 'pinia'
import { ref } from 'vue'
import { nodeApi, type NodeListParams, type Node } from '@/api/node'

const DETAIL_CACHE_TTL = 15_000 // 15s

export const useNodeStore = defineStore('node', () => {
  const nodes = ref<Node[]>([])
  const total = ref(0)
  const loading = ref(false)
  let listFetchedAt = 0

  // Detail cache (node_id string → { node, ts })
  const detailCache = ref<Map<string, { node: Node; ts: number }>>(new Map())

  function isListFresh(): boolean {
    return Date.now() - listFetchedAt < 15_000
  }

  async function fetchNodes(params?: NodeListParams, force = false) {
    if (!force && isListFresh()) return
    loading.value = true
    try {
      const response = await nodeApi.getList(params)
      nodes.value = response.items
      total.value = response.total
      listFetchedAt = Date.now()
    } finally {
      loading.value = false
    }
  }

  async function fetchDetail(id: string | number, force = false) {
    const key = String(id)
    const cached = detailCache.value.get(key)
    if (!force && cached && Date.now() - cached.ts < DETAIL_CACHE_TTL) {
      return cached.node
    }
    const node = await nodeApi.getDetail(id)
    detailCache.value.set(key, { node, ts: Date.now() })
    return node
  }

  function getCachedDetail(id: string | number): Node | undefined {
    return detailCache.value.get(String(id))?.node
  }

  async function updateNode(id: string | number, data: { name?: string }) {
    await nodeApi.update(id, data)
    // Update local cache
    const key = String(id)
    const cached = detailCache.value.get(key)
    if (cached) {
      Object.assign(cached.node, data)
      cached.ts = Date.now()
    }
    // Update list cache
    const idx = nodes.value.findIndex(n => String(n.id) === key || String(n.node_id) === key)
    if (idx >= 0) {
      Object.assign(nodes.value[idx], data)
    }
  }

  async function deleteNode(id: number | string) {
    await nodeApi.delete(id)
    // Optimistic removal
    nodes.value = nodes.value.filter(n => n.id !== id && n.node_id !== String(id))
    total.value = Math.max(0, total.value - 1)
    detailCache.value.delete(String(id))
  }

  function clearCache() {
    nodes.value = []
    total.value = 0
    listFetchedAt = 0
    detailCache.value.clear()
  }

  return {
    nodes,
    total,
    loading,
    fetchNodes,
    fetchDetail,
    getCachedDetail,
    updateNode,
    deleteNode,
    clearCache,
  }
})
