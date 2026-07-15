// src/stores/channel.ts
import { defineStore } from 'pinia'
import { channelApi, type Channel } from '@/api/channel'
import { registerSessionCacheClearer } from '@/utils/sessionCache'

export const useChannelStore = defineStore('channel', {
  state: () => ({
    channels: [] as Channel[],
    loading: false,
    cacheEpoch: 0,
    requestSequence: 0,
    sessionGeneration: 0,
  }),

  getters: {
    getByNodeId: (state) => (nodeId: number) => {
      return state.channels.filter(ch => ch.node_id === nodeId)
    },

    getById: (state) => (id: number) => {
      return state.channels.find(ch => ch.id === id)
    }
  },

  actions: {
    async fetchChannels(nodeId?: number, throwOnError = false) {
      this.loading = true
      const sequence = ++this.requestSequence
      const requestEpoch = this.cacheEpoch
      try {
        const res = await channelApi.getList(nodeId)
        if (requestEpoch !== this.cacheEpoch || sequence !== this.requestSequence) return
        // API returns paginated response: { items: Channel[], total, page, page_size }
        // or when filtered by collector_id: { items: Channel[] } or Channel[]
        if (Array.isArray(res)) {
          this.channels = res
        } else if (res && typeof res === 'object' && 'items' in res) {
          this.channels = (res as any).items || []
        } else if (res && typeof res === 'object' && 'data' in res) {
          // nested wrap: { code, data: { items } }
          const inner = (res as any).data
          this.channels = (inner && inner.items) ? inner.items : []
        } else {
          this.channels = []
        }
      } catch (error) {
        if (requestEpoch !== this.cacheEpoch || sequence !== this.requestSequence) return
        console.error('获取通道列表失败', error)
        this.channels = []
        if (throwOnError) throw error
      } finally {
        if (sequence === this.requestSequence) this.loading = false
      }
    },

    async createChannel(data: Partial<Channel>) {
      const generation = this.sessionGeneration
      const ch = await channelApi.create(data)
      if (generation !== this.sessionGeneration) throw new Error('会话已变更')
      this.channels.push(ch)
      return ch
    },

    async updateChannel(id: number, data: Partial<Channel>) {
      const generation = this.sessionGeneration
      await channelApi.update(id, data)
      if (generation !== this.sessionGeneration) throw new Error('会话已变更')
      const idx = this.channels.findIndex(ch => ch.id === id)
      if (idx >= 0) {
        this.channels[idx] = { ...this.channels[idx], ...data }
      }
    },

    async deleteChannel(id: number) {
      const generation = this.sessionGeneration
      await channelApi.delete(id)
      if (generation !== this.sessionGeneration) throw new Error('会话已变更')
      this.channels = this.channels.filter(ch => ch.id !== id)
    },

    clearCache() {
      this.sessionGeneration++
      this.cacheEpoch++
      this.requestSequence++
      this.channels = []
      this.loading = false
    }
  }
})

registerSessionCacheClearer(() => useChannelStore().clearCache())
