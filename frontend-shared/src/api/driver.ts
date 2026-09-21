import client, { type ApiEnvelope } from './client'

export interface DriverMeta {
  type: string
  model: string
  display_name: string
  oem: string
  category: string
  hardware_types: string[]
  measure_type: string[]
  description: string
}

export interface DriverLeaf {
  type: string
  model: string
  display_name: string
  hardware_types: string[]
  description: string
  /**
   * m-1（2026-09-22）：该型号的受控指令是否把 EdgeDevice.hardware_id 当**物理地址**
   * （Modbus 从站号 / I2C 地址）解析。
   *
   * 后端真源：`driverRequiresTargetAddress`（handler_edge_device.go 的地址门禁与
   * /device-configs/tree 共用同一个判定，不存在第二份口径）。
   *
   * 可选是**刻意的**：老后端不返回该字段。消费方必须把 undefined 当作
   * 「拿不到能力信息」而不是 false —— 后者会让向导对纯 I2C 型号也照旧造地址 1。
   */
  requires_target_address?: boolean
}

export interface DriverTreeNode {
  id: string
  name: string
  children?: DriverTreeNode[]
  drivers?: DriverLeaf[]
}

// 获取驱动层级树
// v2.2: backend now supports /api/v1/device-configs/tree endpoint
export async function getDriverTree(): Promise<DriverTreeNode[]> {
  // 后端统一 envelope: {code, data: [...], message}
  const response = await client.get<unknown, ApiEnvelope<DriverTreeNode[]>>('/api/v1/device-configs/tree')
  return response.data ?? []
}

// 获取驱动列表（扁平）
export async function getDriverList(): Promise<DriverMeta[]> {
  // 后端统一 envelope: {code, data: {items, total, page, page_size}, message}
  // 2026-09-15 方言收敛：该端点曾返回 `list`（全仓 12 个分页端点里唯一的旧方言），
  // 已统一为 `items`；这里必须同步，否则本函数会静默返回空数组。
  const response = await client.get<unknown, ApiEnvelope<{ items?: DriverMeta[] }>>('/api/v1/device-configs')
  return response.data?.items ?? []
}

// 获取驱动详情
export async function getDriverDetail(type: string): Promise<DriverMeta> {
  // 后端统一 envelope: {code, data: DeviceConfig, message}
  const response = await client.get<unknown, ApiEnvelope<DriverMeta>>(`/api/v1/device-configs/${encodeURIComponent(type)}`)
  return response.data ?? ({} as DriverMeta)
}

// Cascader 选项类型
export interface CascaderOption {
  value: string
  label: string
  children?: CascaderOption[]
  hardware_types?: string[]
  description?: string
}

// 转换为 Cascader 格式
// 层级：OEM → 种类 → 型号
export const transformToCascaderOptions = (tree: DriverTreeNode[]): CascaderOption[] => {
  const options: CascaderOption[] = []

  for (const oem of tree) {
    const oemOption: CascaderOption = {
      value: oem.id,
      label: oem.name,
      children: [],
    }

    if (oem.children) {
      for (const category of oem.children) {
        const catOption: CascaderOption = {
          value: category.id,
          label: category.name,
          children: [],
        }

        if (category.drivers) {
          for (const driver of category.drivers) {
            const child: CascaderOption = {
              value: driver.type,
              label: driver.display_name,
              hardware_types: driver.hardware_types,
              description: driver.description,
            }
            catOption.children?.push(child)
          }
        }

        if (catOption.children && catOption.children.length > 0) {
          oemOption.children?.push(catOption)
        }
      }
    }

    if (oemOption.children && oemOption.children.length > 0) {
      options.push(oemOption)
    }
  }

  return options
}

// 扁平化驱动列表（用于 Select）
export const flattenDrivers = (tree: DriverTreeNode[]): DriverLeaf[] => {
  const drivers: DriverLeaf[] = []

  const traverse = (nodes: DriverTreeNode[]) => {
    for (const node of nodes) {
      if (node.drivers) {
        drivers.push(...node.drivers)
      }
      if (node.children) {
        traverse(node.children)
      }
    }
  }

  traverse(tree)
  return drivers
}

/**
 * 按型号 (device_type) 查该型号是否消费设备地址 —— 三分支显式返回。
 *
 * 这是前端**唯一**的能力查询入口（数据源永远是最初的 /device-configs/tree 快照），
 * 调用方不要各自 `find` 一份，否则"字段缺失"的兼容判定会在各处走样。
 */
export function findDriverLeaf(tree: DriverTreeNode[], type: string): DriverLeaf | undefined {
  if (!type) return undefined
  return flattenDrivers(tree).find(driver => driver.type === type)
}

export default {
  getDriverTree,
  getDriverList,
  getDriverDetail,
  transformToCascaderOptions,
  flattenDrivers,
  findDriverLeaf,
}
