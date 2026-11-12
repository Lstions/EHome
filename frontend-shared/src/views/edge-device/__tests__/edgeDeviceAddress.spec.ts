import { describe, expect, it } from 'vitest'
import type { DriverLeaf, DriverTreeNode } from '@/api/driver'
import { DEVICE_ADDRESS_DEFAULT } from '@/utils/deviceAddress'
import {
  resolveInlineDeviceAddress,
  resolveModelRequiresTargetAddress,
} from '@/views/edge-device/edgeDeviceAddress'

/**
 * edgeDeviceAddress.ts 的单元测试（2026-11-12 第三轮子域拆分）。
 *
 * 这个模块是从 composables/useEdgeDeviceCreate.ts 原样搬出来的**纯判定**：
 *   · resolveModelRequiresTargetAddress —— 型号是否消费设备地址（真源 = 驱动叶子字段）；
 *   · resolveInlineDeviceAddress      —— 把「通道的总线资源标识」解析成可提交的设备地址。
 *
 * 组件级行为（提交体里到底发了什么）已由 EdgeDeviceAddressCapabilityGate /
 * EdgeDeviceAddressSemanticsGate 端到端钉住；本文件只把这两条判定的**分支边界**
 * 直接压成单测，好处是改错时能立刻定位到"哪一支"，不必挂载整个页面。
 *
 * 每个 describe 末尾都有**分类器自检**：同一输入在 true / false 两种能力下必须给出
 * 不同结论。没有这条，把 requiresTargetAddress 整个忽略掉时上面的用例仍可能全绿
 * （历史教训：m-1 之前按字符串形态判断，正是"看起来对"的一类缺陷）。
 */

/** 驱动树叶子（与后端 DriverLeaf 同形；undefined 时该 key 直接消失，精确模拟老后端）。 */
const leaf = (type: string, requiresTargetAddress?: boolean): DriverLeaf => ({
  type,
  model: type,
  display_name: type,
  hardware_types: ['uart', 'i2c'],
  description: '',
  ...(requiresTargetAddress === undefined ? {} : { requires_target_address: requiresTargetAddress }),
})

/** OEM → 种类 → 型号 三层树（与 /device-configs/tree 同形）。 */
const tree = (leaves: DriverLeaf[]): DriverTreeNode[] => [
  { id: '通用', name: '通用', children: [{ id: '采集', name: '采集', drivers: leaves }] },
]

describe('resolveModelRequiresTargetAddress — 型号地址能力（三分支必须可区分）', () => {
  it('requires_target_address = true ⇒ true', () => {
    expect(resolveModelRequiresTargetAddress(tree([leaf('sn_modbus', true)]), 'sn_modbus')).toBe(true)
  })

  it('requires_target_address = false ⇒ false（不能与 undefined 混为一谈）', () => {
    expect(resolveModelRequiresTargetAddress(tree([leaf('bmp280', false)]), 'bmp280')).toBe(false)
  })

  it('字段缺失（老后端 / 树未加载完）⇒ undefined，而不是 false', () => {
    expect(resolveModelRequiresTargetAddress(tree([leaf('legacy')]), 'legacy')).toBeUndefined()
  })

  it('型号名前后空白先 trim 再匹配（表单/模板来源可能带空格）', () => {
    expect(resolveModelRequiresTargetAddress(tree([leaf('sn_modbus', true)]), '  sn_modbus  ')).toBe(true)
  })

  it('未知型号 / 空串 / 纯空白 ⇒ undefined（绝不猜成 false）', () => {
    const t = tree([leaf('sn_modbus', true)])
    expect(resolveModelRequiresTargetAddress(t, 'not_in_tree')).toBeUndefined()
    expect(resolveModelRequiresTargetAddress(t, '')).toBeUndefined()
    expect(resolveModelRequiresTargetAddress(t, '   ')).toBeUndefined()
    expect(resolveModelRequiresTargetAddress(t, undefined)).toBeUndefined()
  })

  it('空树 ⇒ undefined（树没加载完时不能把型号判成"不要求地址"）', () => {
    expect(resolveModelRequiresTargetAddress([], 'sn_modbus')).toBeUndefined()
  })

  it('分类器自检：同一棵树里 true / false 两个型号必须得到不同结果', () => {
    const t = tree([leaf('sn_modbus', true), leaf('bmp280', false)])
    expect(resolveModelRequiresTargetAddress(t, 'sn_modbus')).not.toBe(resolveModelRequiresTargetAddress(t, 'bmp280'))
    expect(resolveModelRequiresTargetAddress(t, 'sn_modbus')).toBe(true)
    expect(resolveModelRequiresTargetAddress(t, 'bmp280')).toBe(false)
  })
})

describe('resolveInlineDeviceAddress — 通道总线标识 → 设备地址（三分支）', () => {
  it('requires = true + 总线名（"UART0"）⇒ 默认地址 1，且必须标记警告', () => {
    expect(resolveInlineDeviceAddress('UART0', true)).toEqual({ value: String(DEVICE_ADDRESS_DEFAULT), warned: true })
  })

  it('requires = true + 合法地址（"0x01"）⇒ 原样沿用，不提示', () => {
    expect(resolveInlineDeviceAddress('0x01', true)).toEqual({ value: '0x01', warned: false })
  })

  it('requires = true + 十进制地址（"1"）⇒ 原样沿用', () => {
    expect(resolveInlineDeviceAddress('1', true)).toEqual({ value: '1', warned: false })
  })

  it('requires = true + 空值 ⇒ 提交空串（后端把空值当"无地址/默认"），不弹无意义提示', () => {
    expect(resolveInlineDeviceAddress('', true)).toEqual({ value: '', warned: false })
    expect(resolveInlineDeviceAddress(undefined, true)).toEqual({ value: '', warned: false })
  })

  it('requires = true + 超界（"0xFF"）⇒ 默认地址 1 + 警告（与总线名同一兜底）', () => {
    expect(resolveInlineDeviceAddress('0xFF', true)).toEqual({ value: String(DEVICE_ADDRESS_DEFAULT), warned: true })
  })

  it('requires = false + 总线名（"UART0"）⇒ 不造地址：空串且不提示', () => {
    expect(resolveInlineDeviceAddress('UART0', false)).toEqual({ value: '', warned: false })
  })

  it('requires = false + 看起来合法的地址（"0x44"）⇒ 同样空串（该型号根本不消费地址）', () => {
    expect(resolveInlineDeviceAddress('0x44', false)).toEqual({ value: '', warned: false })
  })

  it('requires = undefined（老后端 / 树未加载）⇒ 兼容分支：默认地址 1 + 警告', () => {
    expect(resolveInlineDeviceAddress('UART0', undefined)).toEqual({ value: String(DEVICE_ADDRESS_DEFAULT), warned: true })
  })

  it('requires = undefined + 合法地址 ⇒ 原样沿用（兼容分支不覆盖用户填的真地址）', () => {
    expect(resolveInlineDeviceAddress('0x01', undefined)).toEqual({ value: '0x01', warned: false })
  })

  it('分类器自检：同一输入在 true / false 下必须给出不同结论', () => {
    const withCapability = resolveInlineDeviceAddress('UART0', true)
    const withoutCapability = resolveInlineDeviceAddress('UART0', false)
    expect(withCapability).not.toEqual(withoutCapability)
    expect(withCapability.warned).toBe(true)
    expect(withoutCapability.warned).toBe(false)
  })
})
