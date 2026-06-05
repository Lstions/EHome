// src/stores/channel.ts
import { defineStore } from 'pinia'
import { channelApi, type Channel } from '@/api/channel'

export const useChannelStore = defineStore('channel', {
  state: () => ({
    channels: [] as Channel[],
    loading: false
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
    async fetchChannels(nodeId?: number) {
      this.loading = true
      try {
        const res = await channelApi.getList(nodeId)
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
        console.error('获取通道列表失败', error)
        this.channels = []
      } finally {
        this.loading = false
      }
    },

    async createChannel(data: Partial<Channel>) {
      const ch = await channelApi.create(data)
      this.channels.push(ch)
      return ch
    },

    async updateChannel(id: number, data: Partial<Channel>) {
      await channelApi.update(id, data)
      const idx = this.channels.findIndex(ch => ch.id === id)
      if (idx >= 0) {
        this.channels[idx] = { ...this.channels[idx], ...data }
      }
    },

    async deleteChannel(id: number) {
      await channelApi.delete(id)
      this.channels = this.channels.filter(ch => ch.id !== id)
    }
  }
})
