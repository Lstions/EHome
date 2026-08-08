/**
 * Parser API - 解析器管理
 */

import client from './client'
import { getDriverTree } from './driver'

export interface Parser {
  id: string              // device_type (DeviceConfig.DeviceType)，非数据库主键；作为解析器标识
  device_config_id?: number // 数据库主键 (后端 DeviceConfig.ID)，0 或 undefined = 无模板
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
  const raw = d.bus_types || d.hardware_types ||
    (d.hardware_type ? [d.hardware_type] :
     d.connection?.bus_type ? [d.connection.bus_type] :
     d.protocol ? [d.protocol] : [])
  const list = Array.isArray(raw) ? raw : [raw]
  // Parser.hardware_types 契约是小写总线名 (EdgeDeviceList/QuickCreateDeviceDialog
  // 都以小写与通道 hardware_type 比较): 统一 toLowerCase + 去重 + 过滤空值
  return [...new Set(list.filter(Boolean).map((h: unknown) => String(h).toLowerCase()))]
}

function normalizeParser(d: any): Parser {
  const deviceConfigId = Number(d.id)
  return {
    id: d.type || d.device_type || '',
    device_config_id: Number.isInteger(deviceConfigId) && deviceConfigId > 0 ? deviceConfigId : undefined,
    name: d.display_name || d.name || (d.type || d.device_type || ''),
    vendor: d.oem || d.vendor || '',
    category: d.category || '',
    hardware_types: resolveHardwareTypes(d),
    measure_types: Array.isArray(d.measure_types)
      ? d.measure_types.filter(Boolean).map((m: unknown) => String(m))
      : d.measure_type ? [String(d.measure_type)] : [],
    description: d.description
  }
}

// 按 type/id 去重: DB 配置优先 (保留 device_config_id)，内置驱动仅作兜底。
// 同时避免同一 type 出现多条 (模板 :key="parser.id" 依赖 id 唯一)。
function dedupeParsers(parsers: Parser[]): Parser[] {
  const byType = new Map<string, Parser>()
  for (const p of parsers) {
    if (!p.id) continue
    const existing = byType.get(p.id)
    if (!existing || (p.device_config_id && !existing.device_config_id)) {
      byType.set(p.id, p)
    }
  }
  return [...byType.values()]
}

/**
 * 将后端驱动树 (OEM → Category → Driver) 展平为 Parser 列表。
 *
 * tree 由 /api/v1/device-configs/tree 合成: driverRegistry 内置驱动 + DB
 * device-configs，空库 (新部署) 时仍包含全部内置驱动。节点名层级语义:
 * depth 0 = OEM (→ vendor)，depth 1 = Category (→ category)，更深忽略。
 * 兼容 OEM 节点直接挂 drivers 的扁平结构。
 *
 * 内置驱动叶子: id=type、name=display_name、hardware_types=hardware_types；
 * vendor/category 从树父节点补齐，拿不到时给通用空值 ('' / '')。
 */
function flattenTreeToParsers(tree: unknown): Parser[] {
  const parsers: Parser[] = []
  const walk = (nodes: unknown, depth: number, vendor: string, category: string): void => {
    if (!Array.isArray(nodes)) return
    for (const node of nodes) {
      if (!node || typeof node !== 'object') continue
      const n = node as any
      const nodeName = typeof n.name === 'string' && n.name !== '' ? n.name : ''
      const effectiveVendor = depth === 0 ? (nodeName || vendor) : vendor
      const effectiveCategory = depth === 1 ? (nodeName || category) : category
      if (Array.isArray(n.drivers)) {
        for (const d of n.drivers) {
          if (!d || typeof d !== 'object') continue
          parsers.push(normalizeParser({
            ...d,
            oem: d.oem || d.vendor || effectiveVendor || '',
            category: d.category || effectiveCategory || '',
          }))
        }
      }
      if (Array.isArray(n.children) && n.children.length > 0) {
        walk(n.children, depth + 1, effectiveVendor, effectiveCategory)
      }
    }
  }
  walk(tree, 0, '', '')
  return parsers
}

export const parserApi = {
  /**
   * 获取所有解析器列表
   *
   * 数据源: DB device-configs (status=active) + 内置驱动树
   * (/api/v1/device-configs/tree, 后端合成 driverRegistry 内置驱动与 DB 配置)。
   * - 空库 (新部署) 时 tree 仍返回内置驱动，保证"选择设备型号"列表非空;
   * - 两源按 type/id 合并去重，DB 配置优先 (保留 device_config_id);
   * - tree 请求失败只影响内置驱动部分，保留 DB 列表兜底；
   *   两源都失败才抛出异常 (保持调用方原有失败语义)。
   */
  async getList(): Promise<Parser[]> {
    let dbParsers: Parser[] = []
    let dbError: unknown = null
    try {
      const response = await client.get('/api/v1/device-configs', { params: { status: 'active' } })
      // Backend returns {code, data: {list: [...], total, ...}, message}
      const envelope = response as any
      const drivers = envelope.data?.list || envelope.data || []
      dbParsers = (Array.isArray(drivers) ? drivers : []).map(normalizeParser)
    } catch (error) {
      dbError = error
      console.warn('[parserApi] 获取设备配置模板列表失败，尝试使用内置驱动兜底', error)
    }

    let treeParsers: Parser[] = []
    let treeError: unknown = null
    try {
      treeParsers = flattenTreeToParsers(await getDriverTree())
    } catch (error) {
      treeError = error
      console.warn('[parserApi] 获取内置驱动树失败，降级为数据库配置列表', error)
    }

    if (dbError && treeError) {
      // 两个数据源都失败: 抛主数据源错误，保持调用方原有失败处理
      throw dbError
    }
    if (treeParsers.length === 0) return dbParsers
    if (dbParsers.length === 0) return treeParsers
    // 合并: 按 type/id 去重, DB 配置优先 (保留 device_config_id)
    return dedupeParsers([...dbParsers, ...treeParsers])
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
