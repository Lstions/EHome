import { describe, expect, it, beforeEach, vi } from 'vitest'

/**
 * m-1 数据通路（第 2 段）：/device-configs/tree 的能力字段必须**三态透传**到
 * 前端两个消费面 —— api/driver 的叶子查询与 api/parser 的 Parser。
 *
 * 为什么单独钉三态：向导的分支是「true / false / 拿不到」，而 TypeScript 的
 * 布尔化非常容易被写成 `!!d.requires_target_address`。那样"字段缺失"会被压成
 * false（= 明确不需要地址），老后端下的总线名会被静默放行 —— 本任务修的缺陷
 * 会以相反方向复现。所以缺失必须落在 undefined，而不是 false。
 */

const { mockClientGet } = vi.hoisted(() => ({ mockClientGet: vi.fn() }))
vi.mock('@/api/client', () => ({ default: { get: mockClientGet } }))

import { findDriverLeaf, flattenDrivers, type DriverTreeNode } from '@/api/driver'
import { parserApi } from '@/api/parser'

const leaf = (type: string, requiresTargetAddress?: boolean) => ({
  type,
  model: type,
  display_name: type,
  hardware_types: ['i2c'],
  description: '',
  ...(requiresTargetAddress === undefined ? {} : { requires_target_address: requiresTargetAddress }),
})

const TREE: DriverTreeNode[] = [
  { id: '通用', name: '通用', children: [{ id: '采集', name: '采集', drivers: [leaf('sn_modbus', true), leaf('bmp280', false), leaf('legacy_only')] }] },
]

describe('api/driver：型号能力的唯一查询入口', () => {
  beforeEach(() => vi.clearAllMocks())

  it('findDriverLeaf 按 device_type 取叶子，未知型号返回 undefined（而不是空对象）', () => {
    // 改坏哪里会变红：把 findDriverLeaf 改成"找不到就返回 {}" ⇒ 未知型号会被
    // 当成"有叶子但没有能力字段"，语义上虽然也走兼容分支，但调用方再也无法
    // 区分"型号不在树里"与"树里没有该字段"。
    expect(findDriverLeaf(TREE, 'sn_modbus')?.requires_target_address).toBe(true)
    expect(findDriverLeaf(TREE, 'bmp280')?.requires_target_address).toBe(false)
    expect(findDriverLeaf(TREE, 'legacy_only')?.requires_target_address).toBeUndefined()
    expect(findDriverLeaf(TREE, 'not_in_tree')).toBeUndefined()
    expect(findDriverLeaf(TREE, '')).toBeUndefined()
  })

  it('分类器自检：同一棵树的三种型号必须给出 true / false / undefined 三种结果', () => {
    const values = ['sn_modbus', 'bmp280', 'legacy_only'].map(t => findDriverLeaf(TREE, t)?.requires_target_address)
    expect(values).toEqual([true, false, undefined])
    // 下界：叶子必须真的被扫到（否则上面的断言是在空集合上通过的）
    expect(flattenDrivers(TREE)).toHaveLength(3)
  })
})

describe('api/parser：能力字段三态透传到 Parser（树来源）', () => {
  beforeEach(() => vi.clearAllMocks())

  it('tree 叶子带 true / false / 缺字段 ⇒ Parser 分别是 true / false / undefined', async () => {
    // 两个数据源：DB 列表（返回空）+ tree（返回上面那棵树）。
    mockClientGet.mockImplementation((url: string) => {
      if (url === '/api/v1/device-configs/tree') {
        return Promise.resolve({ code: 0, message: '', data: TREE })
      }
      return Promise.resolve({ code: 0, message: '', data: { items: [] } })
    })

    const parsers = await parserApi.getList()
    const byType = new Map(parsers.map(p => [p.id, p]))

    expect(byType.get('sn_modbus')?.requires_target_address).toBe(true)
    expect(byType.get('bmp280')?.requires_target_address).toBe(false)
    // 关键：缺失必须保持 undefined。写成 !!d.requires_target_address 会让这条变红
    // （false 会被当成"明确不需要地址"）。
    expect(byType.get('legacy_only')?.requires_target_address).toBeUndefined()
    expect(Object.prototype.hasOwnProperty.call(byType.get('legacy_only'), 'requires_target_address')).toBe(true)
  })

  it('DB 列表来源（GET /device-configs）不提供该字段时，Parser 同样是 undefined', async () => {
    mockClientGet.mockImplementation((url: string) => {
      if (url === '/api/v1/device-configs/tree') return Promise.reject(new Error('tree down'))
      return Promise.resolve({
        code: 0,
        message: '',
        data: { items: [{ id: 7, type: 'db_only', display_name: 'DB 型号', hardware_type: 'i2c' }] },
      })
    })

    const parsers = await parserApi.getList()
    const dbOnly = parsers.find(p => p.id === 'db_only')
    expect(dbOnly, 'DB 型号必须仍在列表里（tree 挂掉不应让整个列表为空）').toBeDefined()
    expect(dbOnly?.requires_target_address).toBeUndefined()
  })
})
