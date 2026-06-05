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


