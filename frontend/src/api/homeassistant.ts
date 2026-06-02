import client from './client'

export const haApi = {
  /**
   * 同步设备到HomeAssistant
   */
  async syncDevice(deviceId: number): Promise<void> {
    await client.post(`/api/v1/ha/sync/${deviceId}`)
  },

  /**
   * 同步采集器下所有设备到HomeAssistant
   */
  async syncCollector(collectorId: number): Promise<void> {
    await client.post(`/api/v1/ha/sync/collector/${collectorId}`)
  },

  /**
   * 同步所有设备到HomeAssistant
   */
  async syncAll(): Promise<void> {
    await client.post('/api/v1/ha/sync/all')
  },

  /**
   * 从HomeAssistant移除设备
   */
  async removeDevice(deviceId: number): Promise<void> {
    await client.delete(`/api/v1/ha/device/${deviceId}`)
  }
}

export default haApi
