/**
 * Parser Store - 解析器状态管理
 */
import { defineStore } from 'pinia'
import { parserApi, type Parser } from '@/api/parser'
import { registerSessionCacheClearer } from '@/utils/sessionCache'

export const useParserStore = defineStore('parser', {
  state: () => ({
    parsers: [] as Parser[],
    loading: false,
    cacheEpoch: 0,
    requestSequence: 0,
  }),

  getters: {
    // 按厂商分组
    parsersByVendor(state): Record<string, Parser[]> {
      const grouped: Record<string, Parser[]> = {}
      for (const p of state.parsers) {
        if (!grouped[p.vendor]) {
          grouped[p.vendor] = []
        }
        grouped[p.vendor].push(p)
      }
      return grouped
    },

    // 获取单个解析器
    getById(state) {
      return (id: string) => state.parsers.find(p => p.id === id)
    }
  },

  actions: {
    async fetchParsers(throwOnError = false) {
      this.loading = true
      const sequence = ++this.requestSequence
      const requestEpoch = this.cacheEpoch
      try {
        const parsers = await parserApi.getList()
        if (requestEpoch !== this.cacheEpoch || sequence !== this.requestSequence) return
        this.parsers = parsers
      } catch (error) {
        if (requestEpoch !== this.cacheEpoch || sequence !== this.requestSequence) return
        console.error('获取解析器列表失败', error)
        if (throwOnError) throw error
      } finally {
        if (sequence === this.requestSequence) this.loading = false
      }
    },

    clearCache() {
      this.cacheEpoch++
      this.requestSequence++
      this.parsers = []
      this.loading = false
    }
  }
})

registerSessionCacheClearer(() => useParserStore().clearCache())

export default useParserStore
