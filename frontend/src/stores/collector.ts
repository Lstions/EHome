import { defineStore } from 'pinia'
import { collectorApi, type CollectorListParams } from '@/api/collector'

export const useCollectorStore = defineStore('collector', {
  state: () => ({
    collectors: [] as any[],
    loading: false,
    total: 0
  }),

  actions: {
    async fetchCollectors(params?: CollectorListParams) {
      this.loading = true
      try {
        const list = await collectorApi.getList(params)
        this.collectors = list
        this.total = list.length
      } finally {
        this.loading = false
      }
    },

    async deleteCollector(id: number) {
      await collectorApi.delete(id)
      await this.fetchCollectors()
    }
  }
})
