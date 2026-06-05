/**
 * Parser API - 解析器管理
 */

import client from './client'

export interface Parser {
  id: string              // 唯一标识: "bosch.bmp280"
  name: string            // 显示名称: "BMP280 温度气压传感器"
  vendor: string          // 厂商: "博世"
  category: string          // 类别: "温度气压传感器"
  hardware_types: string[]     // 支持的硬件类型: ["i2c", "spi"]
  measure_types: string[] // 测量类型: ["temperature", "pressure"]
  description?: string    // 描述
}

export const parserApi = {
  /**
   * 获取所有解析器列表
   */
  async getList(): Promise<Parser[]> {
    const response = await client.get('/api/v1/device-configs')
    // Backend returns {code, data: {list: [...], total, ...}, message}
    const envelope = response as any
    const drivers = envelope.data?.list || envelope.data || []
    return drivers.map((d: any) => ({
      id: d.type || d.device_type,
      name: d.display_name || d.name,
      vendor: d.oem,
      category: d.category,
      hardware_types: d.bus_types || d.hardware_types || [],
      measure_types: d.measure_type ? [d.measure_type] : [],
      description: d.description
    }))
  },

  /**
   * 获取单个解析器详情
   */
  async getById(id: string): Promise<Parser> {
    const response = await client.get(`/api/v1/device-configs/${id}`)
    // Backend returns {code, data: DeviceConfig, message}
    const envelope = response as any
    const d = envelope.data
    return {
      id: d.type || d.device_type,
      name: d.display_name || d.name,
      vendor: d.oem,
      category: d.category,
      hardware_types: d.bus_types || d.hardware_types || [],
      measure_types: d.measure_type ? [d.measure_type] : [],
      description: d.description
    }
  }
}

export default parserApi
