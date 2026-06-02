import { defineStore } from 'pinia'
import { deviceApi, type DeviceListParams } from '@/api/device'

export const useDeviceStore = defineStore('device', {
  state: () => ({
    devices: [] as any[],
    currentDevice: null as any,
    loading: false
  }),

  actions: {
    async fetchDevices(params?: DeviceListParams) {
      this.loading = true
      try {
        const response = await deviceApi.getList(params)
        this.devices = response.items
      } finally {
        this.loading = false
      }
    },

    async fetchDeviceDetail(id: number) {
      this.currentDevice = await deviceApi.getDetail(id)
    }
  }
})
