import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import {
  DEVICE_ADDRESS_MAX,
  DEVICE_ADDRESS_MIN,
  channelHardwareIdIsDeviceAddress,
  isValidDeviceAddress,
  normalizeDeviceAddressInput,
  parseDeviceAddress,
} from '@/utils/deviceAddress'
import wizardSource from '@/components/node/QuickCreateDeviceDialog.vue?raw'

/**
 * 设备地址口径的**前后端契约门禁**（2026-09-20 生产故障防复发）。
 *
 * 故障本质：两层语义（总线标识 vs 设备地址）被无声混用 —— 创建向导把
 * Channel.hardware_id "UART1" 当成 EdgeDevice.hardware_id 存下去，后端每次派发
 * 都在 ParseHardwareAddress 被拒，UI 却一直显示 QUEUED 直到 deadline 过期。
 *
 * 两侧各写一份正则必然再次漂移，所以本文件的策略是：
 *   1. 从**后端源码**解析出真源（上下界/默认地址分支/进制分支/真源函数名），拿不到就抛错
 *      （防止路径写错 ⇒ 空集合 ⇒ 全绿这种假绿）；
 *   2. 用**同一张表**驱动前端判定，断言与后端逐行一致；
 *   3. 分类器自检：合法样本必须判合法、非法样本必须判非法，且两侧都要有
 *      —— 只会说"合法"（或只会说"非法"）的实现无法让本文件通过；
 *   4. 断言向导源码真的接了这个判定（判定正确但没接线 = 故障照样发生）。
 */

// vitest cwd = frontend-shared；后端在同级 backend/ 下。
const BACKEND_DEFINITION_PATH = resolve(process.cwd(), '..', 'backend/internal/deviceaction/definition.go')

function readBackendDefinition(): string {
  try {
    return readFileSync(BACKEND_DEFINITION_PATH, 'utf8')
  } catch (error) {
    throw new Error(
      '无法读取后端地址口径真源 ' + BACKEND_DEFINITION_PATH +
      '；地址门禁会退化成空断言（假绿），因此这里必须硬失败：' + String(error),
    )
  }
}

interface AddressCase {
  name: string
  value: string
  valid: boolean
  address: number | null
  explicit: boolean
}

/**
 * 权威口径表 —— 与后端 backend/internal/deviceaction/definition.go 的
 * ParseHardwareAddress 逐行对应。**这张表就是契约**：改这里必须同时改后端，
 * 否则本文件会红。
 */
const ADDRESS_CASES: AddressCase[] = [
  { name: '空串 = 历史默认地址 1', value: '', valid: true, address: 1, explicit: false },
  { name: '纯空格 = 默认地址 1', value: '   ', valid: true, address: 1, explicit: false },
  { name: '"0" = 默认地址 1', value: '0', valid: true, address: 1, explicit: false },
  { name: '下界 1', value: '1', valid: true, address: 1, explicit: true },
  { name: '上界 254', value: '254', valid: true, address: 254, explicit: true },
  { name: '超界 255', value: '255', valid: false, address: null, explicit: true },
  { name: '十六进制下界 0x01', value: '0x01', valid: true, address: 1, explicit: true },
  { name: '十六进制上界 0xFE', value: '0xFE', valid: true, address: 254, explicit: true },
  { name: '十六进制超界 0xFF', value: '0xFF', valid: false, address: null, explicit: true },
  { name: '十六进制零 0x00', value: '0x00', valid: false, address: null, explicit: true },
  { name: '单位十六进制 0x1', value: '0x1', valid: true, address: 1, explicit: true },
  { name: '大写前缀 0Xfe', value: '0Xfe', valid: true, address: 254, explicit: true },
  { name: '空十六进制 0x', value: '0x', valid: false, address: null, explicit: true },
  { name: '前导零 007', value: '007', valid: true, address: 7, explicit: true },
  { name: '全零 00', value: '00', valid: false, address: null, explicit: true },
  { name: '两侧空格', value: ' 1 ', valid: true, address: 1, explicit: true },
  { name: '尾部空格', value: '1 ', valid: true, address: 1, explicit: true },
  { name: '故障值 UART1（总线名）', value: 'UART1', valid: false, address: null, explicit: true },
  { name: 'I2C 总线名 I2C0', value: 'I2C0', valid: false, address: null, explicit: true },
  { name: '字母 abc', value: 'abc', valid: false, address: null, explicit: true },
  { name: '负数 -1', value: '-1', valid: false, address: null, explicit: true },
  { name: '正号 +1', value: '+1', valid: false, address: null, explicit: true },
  { name: '小数 1.5', value: '1.5', valid: false, address: null, explicit: true },
  { name: '科学计数 1e2', value: '1e2', valid: false, address: null, explicit: true },
  { name: '二进制前缀 0b1', value: '0b1', valid: false, address: null, explicit: true },
  { name: '数字分隔符 1_0', value: '1_0', valid: false, address: null, explicit: true },
  { name: '全角数字', value: '１２', valid: false, address: null, explicit: true },
]

describe('设备地址口径：后端真源可解析（防止门禁假绿）', () => {
  const source = readBackendDefinition()

  it('真源文件存在且包含 ParseHardwareAddress', () => {
    expect(source).toContain('func ParseHardwareAddress(value string) (uint8, error)')
  })

  it('后端上下界与前端常量一致（1 / 254）', () => {
    // 后端：if err != nil || parsed < 1 || parsed > 254
    const bound = source.match(/parsed\s*<\s*(\d+)\s*\|\|\s*parsed\s*>\s*(\d+)/)
    expect(bound, '未能在后端源码中解析出地址上下界；解析失败必须显式失败而不是跳过').not.toBeNull()
    expect(Number(bound![1])).toBe(DEVICE_ADDRESS_MIN)
    expect(Number(bound![2])).toBe(DEVICE_ADDRESS_MAX)
  })

  it('后端仍把空串与 "0" 归一为默认地址 1', () => {
    const branch = source.match(/value == "" \|\| value == "0" \{\s*return (\d+), nil/)
    expect(branch, '未能在后端源码中解析出默认地址分支').not.toBeNull()
    expect(Number(branch![1])).toBe(1)
  })

  it('后端仍同时接受十进制与 0x 前缀两条分支', () => {
    expect(source).toContain('base := 10')
    expect(source).toContain('base = 16')
    expect(source).toMatch(/HasPrefix\(text, "0x"\)\s*\|\|\s*strings\.HasPrefix\(text, "0X"\)/)
  })

  it('后端错误文案仍带 1 to 254（前端提示与之同口径）', () => {
    expect(source).toContain('must be an address from 1 to 254')
  })
})

describe('设备地址口径：前端判定与权威表逐行一致', () => {
  it.each(ADDRESS_CASES)('$name', ({ value, valid, address, explicit }) => {
    const verdict = parseDeviceAddress(value)
    const label = JSON.stringify(value)
    expect(verdict.valid, 'parseDeviceAddress(' + label + ').valid').toBe(valid)
    expect(verdict.address, 'parseDeviceAddress(' + label + ').address').toBe(address)
    expect(verdict.explicit, 'parseDeviceAddress(' + label + ').explicit').toBe(explicit)
    expect(isValidDeviceAddress(value)).toBe(valid)
  })

  it('分类器自检：合法样本全部判合法（防止"永远非法"的实现）', () => {
    const legal = ADDRESS_CASES.filter(c => c.valid)
    expect(legal.length, '合法样本下界 —— 没有合法样本的门禁是空的').toBeGreaterThanOrEqual(8)
    for (const tc of legal) {
      expect(parseDeviceAddress(tc.value).valid, JSON.stringify(tc.value) + ' 应判合法').toBe(true)
    }
  })

  it('分类器自检：非法样本全部判非法（防止"永远合法"的实现 —— 本仓已多次发生此类假绿）', () => {
    const illegal = ADDRESS_CASES.filter(c => !c.valid)
    expect(illegal.length, '非法样本下界').toBeGreaterThanOrEqual(8)
    for (const tc of illegal) {
      expect(parseDeviceAddress(tc.value).valid, JSON.stringify(tc.value) + ' 应判非法').toBe(false)
    }
    // 本次故障值必须在非法集合里
    expect(illegal.map(c => c.value)).toContain('UART1')
  })

  it('分类器自检：能区分"总线名"与"设备地址"（本故障的核心判定）', () => {
    expect(channelHardwareIdIsDeviceAddress('UART1')).toBe(false)
    expect(channelHardwareIdIsDeviceAddress('I2C0')).toBe(false)
    expect(channelHardwareIdIsDeviceAddress('0x01')).toBe(true)
    expect(channelHardwareIdIsDeviceAddress('1')).toBe(true)
    // 空/0 是后端默认地址语义，不算"非法"，但也不算"显式指定了地址"
    expect(channelHardwareIdIsDeviceAddress('')).toBe(true)
    expect(parseDeviceAddress('').explicit).toBe(false)
  })

  it('normalizeDeviceAddressInput 只做 trim，不改数值语义', () => {
    expect(normalizeDeviceAddressInput(' 1 ')).toBe('1')
    expect(normalizeDeviceAddressInput('0x01')).toBe('0x01')
    expect(normalizeDeviceAddressInput(null)).toBe('')
    expect(normalizeDeviceAddressInput(undefined)).toBe('')
    expect(isValidDeviceAddress(normalizeDeviceAddressInput('  0xFE  '))).toBe(true)
  })
})

describe('R1 接线门禁：创建向导必须真正使用地址判定（判定正确但没接线 = 故障照旧）', () => {
  const INCIDENT_PATTERN = /hardware_id\s*:\s*ch\.hardware_id\s*,?/

  it('向导不得再把通道 hardware_id 原样当作设备地址提交', () => {
    expect(
      INCIDENT_PATTERN.test(wizardSource),
      'QuickCreateDeviceDialog 又出现了 "hardware_id: ch.hardware_id" —— 这正是把总线名当设备地址的故障写法',
    ).toBe(false)
  })

  it('向导必须导入并使用共享地址判定', () => {
    expect(wizardSource).toContain('@/utils/deviceAddress')
    expect(wizardSource).toMatch(/\bparseDeviceAddress\b/)
  })

  it('向导必须存在"设备地址"表单项与面向用户的提示文案', () => {
    expect(wizardSource).toContain('设备地址')
    expect(wizardSource).toContain('总线')
  })

  it('接线门禁自检：故障写法样本必须被判红（防止正则写错导致永远绿灯）', () => {
    const brokenSample = 'const payload = { hardware_id: ch.hardware_id, type: "sn3001_rain" }'
    expect(INCIDENT_PATTERN.test(brokenSample)).toBe(true)
    const fixedSample = 'const payload = { hardware_id: resolvedDeviceAddress.value }'
    expect(INCIDENT_PATTERN.test(fixedSample)).toBe(false)
  })
})
