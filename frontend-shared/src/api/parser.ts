/**
 * Parser API - 解析器管理
 */

import client from './client'

export interface Parser {
  id: string              // 唯一标识: "bosch.bmp280"
  device_config_id?: number // 后端 device_configs.id，用于创建设备时建立外键关联
  name: string            // 显示名称: "BMP280 温度气压传感器"
  vendor: string          // 厂商: "博世"
  category: string          // 类别: "温度气压传感器"
  hardware_types: string[]     // 支持的硬件类型: ["i2c", "spi"]
  measure_types: string[] // 测量类型: ["temperature", "pressure"]
  description?: string    // 描述
}

// S8 fix: Extract 4-level hardware_types fallback chain into a helper function
// (was duplicated in getList() and getById())
function resolveHardwareTypes(d: any): string[] {
  return d.bus_types || d.hardware_types ||
    (d.hardware_type ? [d.hardware_type.toLowerCase()] :
     d.connection?.bus_type ? [d.connection.bus_type.toLowerCase()] :
     d.protocol ? [d.protocol.toLowerCase()] : [])
}

function normalizeParser(d: any): Parser {
  const deviceConfigId = Number(d.id)
  return {
    id: d.type || d.device_type,
    device_config_id: Number.isInteger(deviceConfigId) && deviceConfigId > 0 ? deviceConfigId : undefined,
    name: d.display_name || d.name,
    vendor: d.oem || d.vendor || '',
    category: d.category || '',
    hardware_types: resolveHardwareTypes(d),
    measure_types: d.measure_type ? [d.measure_type] : [],
    description: d.description
  }
}

export const parserApi = {
  /**
   * 获取所有解析器列表
   */
  async getList(): Promise<Parser[]> {
    const response = await client.get('/api/v1/device-configs', { params: { status: 'active' } })
    // Backend returns {code, data: {list: [...], total, ...}, message}
    const envelope = response as any
    const drivers = envelope.data?.list || envelope.data || []
    return drivers.map(normalizeParser)
  },

  /**
   * 获取单个解析器详情
   */
  async getById(id: string): Promise<Parser> {
    const response = await client.get(`/api/v1/device-configs/${id}`)
    // Backend returns {code, data: DeviceConfig, message}
    const envelope = response as any
    return normalizeParser(envelope.data)
  }
}

export default parserApi
