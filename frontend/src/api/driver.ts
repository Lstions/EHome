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
interface ApiResponse<T> {
  code: number
  data: T
  message: string
}

// 获取驱动层级树
export const getDriverTree = async (): Promise<DriverTreeNode[]> => {
  const response = await client.get<any, ApiResponse<{ tree: DriverTreeNode[] }>>('/api/v1/drivers/tree')
  return (response as any).data?.tree || []
}

// 获取驱动列表（扁平）
export const getDriverList = async (): Promise<DriverMeta[]> => {
  const response = await client.get<any, ApiResponse<DriverMeta[]>>('/api/v1/drivers')
  return (response as any).data
}

// 获取驱动详情
export const getDriverDetail = async (type: string) => {
  const response = await client.get<any, ApiResponse<any>>(`/api/v1/drivers/${encodeURIComponent(type)}`)
  return (response as any).data
}

// 转换为 Cascader 格式
// 层级：OEM → 种类 → 型号
export const transformToCascaderOptions = (tree: DriverTreeNode[]): CascaderOption[] => {
  const options: CascaderOption[] = []

  for (const oem of tree) {
    const oemOption: CascaderOption = {
      value: oem.id,
      label: oem.name,
      children: []
    }

    if (oem.children) {
      for (const category of oem.children) {
        const catOption: CascaderOption = {
          value: category.id,
          label: category.name,
          children: []
        }

        if (category.drivers) {
          for (const driver of category.drivers) {
            catOption.children!.push({
              value: driver.type,
              label: driver.display_name,
              hardware_types: driver.hardware_types,
              description: driver.description
            } as any)
          }
        }

        if (catOption.children && catOption.children.length > 0) {
          oemOption.children!.push(catOption)
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

// Cascader 选项类型
export interface CascaderOption {
  value: string
  label: string
  children?: CascaderOption[]
  hardware_types?: string[]
  description?: string
}

export default {
  getDriverTree,
  getDriverList,
  getDriverDetail,
  transformToCascaderOptions,
  flattenDrivers
}
