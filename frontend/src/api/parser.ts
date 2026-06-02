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
    const response = await client.get('/api/v1/drivers')
    const drivers = response.data.data || []
    return drivers.map((d: any) => ({
      id: d.type,
      name: d.display_name,
      vendor: d.oem,
      category: d.category,
      hardware_types: d.bus_types || [],
      measure_types: d.measure_type ? [d.measure_type] : [],
      description: d.description
    }))
  },

  /**
   * 获取单个解析器详情
   */
  async getById(id: string): Promise<Parser> {
    const response = await client.get(`/api/v1/drivers/${id}`)
    const d = response.data.data
    return {
      id: d.meta.type,
      name: d.meta.display_name,
      vendor: d.meta.oem,
      category: d.meta.category,
      hardware_types: d.meta.bus_types || [],
      measure_types: d.meta.measure_type ? [d.meta.measure_type] : [],
      description: d.meta.description
    }
  }
}

export default parserApi
