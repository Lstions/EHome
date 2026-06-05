import client from './client'

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
}

export interface DriverTreeNode {
  id: string
  name: string
  children?: DriverTreeNode[]
  drivers?: DriverLeaf[]
}

// 获取驱动层级树
// Note: Backend v2.2 does not have /device-configs/tree endpoint
// This function returns empty array until backend adds tree support
export async function getDriverTree(): Promise<DriverTreeNode[]> {
  // TODO: Backend needs to add /api/v1/device-configs/tree endpoint
  // For now, return empty tree and use getDriverList() instead
  return []
}

// 获取驱动列表（扁平）
export async function getDriverList(): Promise<DriverMeta[]> {
  const response = await client.get('/api/v1/device-configs')
  // Backend returns {code, data: {list: [...], total, page, page_size}, message}
  const envelope = response as any
  const list = envelope.data?.list || envelope.data || []
  return Array.isArray(list) ? list : []
}

// 获取驱动详情
export async function getDriverDetail(type: string): Promise<DriverMeta> {
  const response = await client.get(`/api/v1/device-configs/${encodeURIComponent(type)}`)
  // Backend returns {code, data: DeviceConfig, message}
  const envelope = response as any
  return envelope.data || ({} as DriverMeta)
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

export default {
  getDriverTree,
  getDriverList,
  getDriverDetail,
  transformToCascaderOptions,
  flattenDrivers,
}
