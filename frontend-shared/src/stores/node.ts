import { defineStore } from 'pinia'
import { nodeApi, type NodeListParams } from '@/api/node'

export const useNodeStore = defineStore('node', {
  state: () => ({
    nodes: [] as any[],
    loading: false,
    total: 0
  }),

  actions: {
    async fetchNodes(params?: NodeListParams) {
      this.loading = true
      try {
        const response = await nodeApi.getList(params)
        this.nodes = response.items
        this.total = response.total
      } finally {
        this.loading = false
      }
    },

    async deleteNode(id: number) {
      await nodeApi.delete(id)
      await this.fetchNodes()
    }
  }
})

// ============================================================
// v2.1 兼容 (6 个月后删除, v2.3)
// 保留 collectors/total 字段名, 供旧组件兼容
// ============================================================

/** @deprecated Use useNodeStore instead */
export const useCollectorStore = defineStore('collector', {
  state: () => ({
    collectors: [] as any[],
    loading: false,
    total: 0
  }),

  actions: {
    async fetchCollectors(params?: NodeListParams) {
      this.loading = true
      try {
        const response = await nodeApi.getList(params)
        this.collectors = response.items
        this.total = response.total
      } finally {
        this.loading = false
      }
    },

    async deleteCollector(id: number) {
      await nodeApi.delete(id)
      await this.fetchCollectors()
    }
  }
})
