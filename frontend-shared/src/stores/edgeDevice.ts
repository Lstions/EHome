import { defineStore } from 'pinia'
import { edgeDeviceApi, type EdgeDeviceListParams } from '@/api/edgeDevice'

export const useEdgeDeviceStore = defineStore('edgeDevice', {
  state: () => ({
    edgeDevices: [] as any[],
    currentEdgeDevice: null as any,
    loading: false
  }),

  actions: {
    async fetchEdgeDevices(params?: EdgeDeviceListParams) {
      this.loading = true
      try {
        const response = await edgeDeviceApi.getList(params)
        this.edgeDevices = response.items
      } finally {
        this.loading = false
      }
    },

    async fetchEdgeDeviceDetail(id: number) {
      this.currentEdgeDevice = await edgeDeviceApi.getDetail(id)
    }
  }
})


