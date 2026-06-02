/**
 * Parser Store - 解析器状态管理
 */
import { defineStore } from 'pinia'
import { parserApi, type Parser } from '@/api/parser'

export const useParserStore = defineStore('parser', {
  state: () => ({
    parsers: [] as Parser[],
    loading: false
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
    async fetchParsers() {
      this.loading = true
      try {
        this.parsers = await parserApi.getList()
      } catch (error) {
        console.error('获取解析器列表失败', error)
      } finally {
        this.loading = false
      }
    }
  }
})

export default useParserStore
