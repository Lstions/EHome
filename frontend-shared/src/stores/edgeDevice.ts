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

// ============================================================
// v2.1 兼容 (6 个月后删除, v2.3)
// 保留 devices/currentDevice 字段名, 供旧组件兼容
// ============================================================

/** @deprecated Use useEdgeDeviceStore instead */
export const useDeviceStore = defineStore('device', {
  state: () => ({
    devices: [] as any[],
    currentDevice: null as any,
    loading: false
  }),

  actions: {
    async fetchDevices(params?: EdgeDeviceListParams) {
      this.loading = true
      try {
        const response = await edgeDeviceApi.getList(params)
        this.devices = response.items
      } finally {
        this.loading = false
      }
    },

    async fetchDeviceDetail(id: number) {
      this.currentDevice = await edgeDeviceApi.getDetail(id)
    }
  }
})
