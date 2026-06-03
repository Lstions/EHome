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

// 后端返回格式: { code: 200, data: { tree: [...] }, message: "success" }
interface ApiEnvelope<T> {
  code: number
  data: T
  message: string
}

interface DriverTreeEnvelope extends ApiEnvelope<{ tree: DriverTreeNode[] }> {}

/** 统一拆信封，兼容后端两种返回形态 */
function unwrap<T>(response: unknown, fallback: T): T {
  if (response && typeof response === 'object') {
    const r = response as { data?: unknown }
    if (r.data !== undefined) {
      // 形如 { code, data, message }
      const inner = r.data as { data?: unknown }
      if (inner && typeof inner === 'object' && 'data' in inner) {
        return (inner.data as T) ?? fallback
      }
      return (r.data as T) ?? fallback
    }
  }
  return fallback
}

// 获取驱动层级树
export async function getDriverTree(): Promise<DriverTreeNode[]> {
  const response = await client.get<DriverTreeEnvelope>('/api/v1/drivers/tree')
  return unwrap<DriverTreeNode[]>(response, [])
}

// 获取驱动列表（扁平）
export async function getDriverList(): Promise<DriverMeta[]> {
  const response = await client.get<ApiEnvelope<DriverMeta[]>>('/api/v1/drivers')
  return unwrap<DriverMeta[]>(response, [])
}

// 获取驱动详情
export async function getDriverDetail(type: string): Promise<DriverMeta> {
  const response = await client.get<ApiEnvelope<DriverMeta>>(`/api/v1/drivers/${encodeURIComponent(type)}`)
  return unwrap<DriverMeta>(response, {} as DriverMeta)
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
