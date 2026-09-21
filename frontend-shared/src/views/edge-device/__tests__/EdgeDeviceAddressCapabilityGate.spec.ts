import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'
import listSource from '@/views/edge-device/EdgeDeviceList.vue?raw'
import { edgeDeviceApi } from '@/api/edgeDevice'

/**
 * m-1（2026-09-22）—— 列表页向导的设备地址兜底必须**按型号能力**分流。
 *
 * 缺陷（审核报告 m-1）：后端 validateEdgeDeviceAddress 只对
 * driverRequiresTargetAddress 为真的型号校验 1-254，纯 I2C/SPI 型号原样接受任何
 * 标识；而 resolveInlineDeviceAddress 按**字符串形态**判断，把 "I2C0"/"UART0"
 * 一律替换成默认地址 1 —— 给一个从不消费地址的型号凭空写了一个地址。
 *
 * 修复后的三分支（每个分支一个用例）：
 *   · requires_target_address === true      → 非法值兜底默认地址 1 + 显式提示；
 *   · requires_target_address === false     → **不造地址**，提交空串且不提示；
 *   · 字段缺失（老后端）                    → 保持改前行为（默认地址 1 + 提示）。
 *
 * 数据通路是完整的真链路：/device-configs/tree 的响应（本文件 mock 的是
 * @/api/client 这一层）→ api/driver.getDriverTree → 向导取型号能力 → 提交值。
 * 不通过直接给组件塞一个内部 ref 来"制造"能力，否则测的是注入而不是接线。
 *
 * 每个用例都写明"改坏哪里会变红"，最终报告给出变异自证（后端新字段恒 false ⇒
 * 用例 2 变红）。
 */

const { mockEdgeDeviceGetList, mockGetLogicalDeviceInfo, mockGetDriverCommands, mockGetCapabilities, mockClientGet, mockWarning } = vi.hoisted(() => ({
  mockClientGet: vi.fn(),
  mockWarning: vi.fn(),
  mockGetCapabilities: vi.fn((..._args: any[]) => Promise.resolve({ buses: {} })),
  mockEdgeDeviceGetList: vi.fn((..._args: any[]) => Promise.resolve({ items: [], total: 0 })),
  mockGetLogicalDeviceInfo: vi.fn(() => Promise.resolve({
    edge_device_id: 1, name: 'Logic-A', logical_device_id: 11, retention_days: 30, instance_count: 2, row_estimate: 500,
  })),
  mockGetDriverCommands: vi.fn((..._args: any[]) => Promise.resolve([])),
}))

vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: mockWarning, info: vi.fn() }),
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: vi.fn(() => Promise.resolve()),
    getCachedList: vi.fn(() => ({ items: [{ id: 1, node_id: 'node-1', name: 'Collector-A', status: 'online' }], total: 1 })),
  }),
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ connected: false, connect: vi.fn(), subscribe: vi.fn(() => vi.fn()) }),
}))
vi.mock('@/api/channel', () => ({
  compactChannelList: (items: unknown[]) => (Array.isArray(items) ? items.filter(i => i && typeof i === 'object') : []),
  channelApi: { getList: vi.fn(() => Promise.resolve([])), update: vi.fn() },
}))
vi.mock('@/api/deviceConfig', () => ({ deviceConfigApi: { getList: vi.fn(() => Promise.resolve({ list: [], items: [] })) } }))
vi.mock('@/api/parser', () => ({ parserApi: { getList: vi.fn(() => Promise.resolve([])) } }))
vi.mock('@/api/node', () => ({ nodeApi: { getCapabilities: mockGetCapabilities } }))
// 唯一被拦截的是 HTTP 出口：/device-configs/tree 的响应从这里进，经真实的
// api/driver.getDriverTree → 向导能力查询 → 提交值。若这里不 mock，happy-dom 会
// 真的去连 127.0.0.1:3000 并 ECONNREFUSED，用例会"红得毫无意义"（测的是没网）。
vi.mock('@/api/client', () => ({ default: { get: mockClientGet } }))
vi.mock('@/api/edgeDevice', () => ({
  compactEdgeDeviceList: (items: unknown[]) => (Array.isArray(items) ? items.filter(i => i && typeof i === 'object' && 'id' in i) : []),
  edgeDeviceApi: {
    getList: mockEdgeDeviceGetList,
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    getLogicalDeviceInfo: mockGetLogicalDeviceInfo,
    getCandidates: vi.fn(() => Promise.resolve([])),
    getDriverCommands: mockGetDriverCommands,
  },
}))

/** 驱动树叶子（与后端 DriverLeaf 同形，含 m-1 新增字段）。 */
const leaf = (type: string, requiresTargetAddress?: boolean) => ({
  type,
  model: type,
  display_name: type,
  hardware_types: ['uart', 'i2c'],
  description: '',
  // undefined ⇒ 序列化后该 key 消失 ⇒ 精确模拟"老后端不返回该字段"
  ...(requiresTargetAddress === undefined ? {} : { requires_target_address: requiresTargetAddress }),
})

/** OEM → Category → Driver 树；true / false 两种型号同时在册（分类器自检的下界）。 */
const driverTree = (leaves: ReturnType<typeof leaf>[]) => ([
  { id: '通用', name: '通用', children: [{ id: '采集', name: '采集', drivers: leaves }] },
])

const CAPS_FIXTURE = { buses: { uart: [{ id: 'UART0' }], i2c: [{ id: 'I2C0' }] } }

const stubs = {
  SkeletonCard: { template: '<div />' },
  EmptyState: { template: '<div />' },
  CountUp: { template: '<span>{{ $attrs.value }}</span>' },
}

const mountList = () => mount(EdgeDeviceList, { global: { stubs } })

/**
 * 走完整向导路径提交一次：打开向导（触发 /device-configs/tree 拉取）→ 选型号 →
 * 选已有通道 → 提交。返回 create 的入参。
 */
const submitWizard = async (parser: Record<string, unknown>, channelHardwareId: string) => {
  const wrapper = mountList()
  await flushPromises()
  const createBtn = wrapper.findAll('button').find((b: any) => b.text().includes('创建边缘设备'))
  await createBtn!.trigger('click')
  await flushPromises()
  const vm = wrapper.vm as any
  // 型号来自"选择设备型号"步骤（与 EdgeDeviceList.spec.ts 同一路径）
  vm.selectedParser = parser
  await vm.$nextTick()
  await flushPromises()
  vm.deviceForm.node_id = 'node-1'
  vm.handleNodeChange()
  await flushPromises()
  vm.deviceForm.name = 'm-1 设备'
  vm.channelTab = 'existing'
  vm.selectedChannel = { id: 5, hardware_type: 'uart', hardware_id: channelHardwareId, config: { device_type: parser.id } }
  vm.deviceFormRef = { validate: () => Promise.resolve(), resetFields: () => {} }
  await vm.handleCreate()
  await flushPromises()
  return { vm, wrapper }
}

describe('m-1 — 型号 requires_target_address=true：合法地址沿用，非法值兜底默认地址 1 并提示', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
    mockGetCapabilities.mockResolvedValue({ buses: CAPS_FIXTURE } as any)
    mockClientGet.mockResolvedValue({ code: 0, message: '', data: driverTree([leaf('sn_modbus', true), leaf('bmp280', false)]) } as any)
  })

  it('总线名 "UART0" ⇒ 提交默认地址 1（且必须提示用户去确认实际地址）', async () => {
    // 改坏哪里会变红：把 requires=true 分支的结果改成空串，或把 warned 置 false。
    await submitWizard({ id: 'sn_modbus', name: 'Modbus 采集', hardware_types: ['uart'] }, 'UART0')

    // 下界断言：能力真的来自 /device-configs/tree 这次请求（否则上面的结果只是
    // "拿不到能力"的兼容分支，用例会假绿）。
    expect(mockClientGet.mock.calls.map((c: any[]) => c[0])).toContain('/api/v1/device-configs/tree')
    expect(edgeDeviceApi.create).toHaveBeenCalledTimes(1)
    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg.hardware_id).toBe('1')
    expect(mockWarning).toHaveBeenCalledTimes(1)
    expect(String(mockWarning.mock.calls[0][0])).toContain('总线名称')
  })

  it('地址形态合法（"0x01"）⇒ 原样沿用，不提示、不被改写成默认值', async () => {
    await submitWizard({ id: 'sn_modbus', name: 'Modbus 采集', hardware_types: ['uart'] }, '0x01')
    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg.hardware_id).toBe('0x01')
    expect(mockWarning).not.toHaveBeenCalled()
  })
})

describe('m-1 — 型号 requires_target_address=false：不得凭空造地址', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
    mockGetCapabilities.mockResolvedValue({ buses: CAPS_FIXTURE } as any)
    mockClientGet.mockResolvedValue({ code: 0, message: '', data: driverTree([leaf('sn_modbus', true), leaf('bmp280', false)]) } as any)
  })

  it('纯 I2C 型号 + 通道是总线名 "I2C0" ⇒ 提交空串（无地址语义），且不弹无意义提示', async () => {
    // 改坏哪里会变红：
    //   ① 后端新字段恒置 false ⇒ 这个型号会走进 true 分支（不，恒 false 会让本用例仍绿）
    //      ——真正的变异是"后端新字段恒置 false"打在下述对照用例上（见下一 describe）；
    //   ② 前端把 undefined/false 压成同一条路径（删掉 requiresTargetAddress === false
    //      分支）⇒ 本用例立刻拿到 '1' 而变红。这是本用例守的缺陷。
    await submitWizard({ id: 'bmp280', name: 'BMP280', hardware_types: ['i2c'] }, 'I2C0')

    expect(edgeDeviceApi.create).toHaveBeenCalledTimes(1)
    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg.hardware_id).toBe('')
    expect(mockWarning).not.toHaveBeenCalled()
  })

  it('分类器自检：同一棵树里 true / false 两个型号必须得到**不同**结果（否则标志根本没被消费）', async () => {
    // 反向自检：如果能力字段被忽略（例如只按字符串形态判断），两个型号会拿到同一个
    // 结果，这条断言当场变红。
    await submitWizard({ id: 'sn_modbus', name: 'Modbus 采集', hardware_types: ['uart'] }, 'UART0')
    const addressedResult = (edgeDeviceApi.create as any).mock.calls[0][0].hardware_id
    ;(edgeDeviceApi.create as any).mockClear()

    await submitWizard({ id: 'bmp280', name: 'BMP280', hardware_types: ['i2c'] }, 'UART0')
    const addresslessResult = (edgeDeviceApi.create as any).mock.calls[0][0].hardware_id

    expect(addressedResult).toBe('1')
    expect(addresslessResult).toBe('')
    expect(addressedResult).not.toBe(addresslessResult)
  })
})

describe('m-1 — 字段缺失（老后端）：保持改前行为（默认地址 1 + 提示）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
    mockGetCapabilities.mockResolvedValue({ buses: CAPS_FIXTURE } as any)
    // 老后端：叶子没有 requires_target_address 键
    mockClientGet.mockResolvedValue({ code: 0, message: '', data: driverTree([leaf('legacy_model')]) } as any)
  })

  it('拿不到能力字段 ⇒ 仍按默认地址 1 + 提示（向后兼容，不能静默放行总线名）', async () => {
    // 改坏哪里会变红：把 resolveInlineDeviceAddress 的 undefined 分支并到
    // "不造地址"分支（undefined === false 处理）⇒ 提交值变成 ''，本用例变红。
    await submitWizard({ id: 'legacy_model', name: '老后端型号', hardware_types: ['uart'] }, 'UART0')

    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg.hardware_id).toBe('1')
    expect(mockWarning).toHaveBeenCalledTimes(1)
  })

  it('驱动树整体拉取失败（请求报错）⇒ 同样退回兼容分支，向导其余功能不受影响', async () => {
    mockClientGet.mockRejectedValue(new Error('network down'))
    await submitWizard({ id: 'legacy_model', name: '老后端型号', hardware_types: ['uart'] }, 'UART0')

    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg.hardware_id).toBe('1')
    expect(mockWarning).toHaveBeenCalledTimes(1)
  })
})

describe('m-1 — 接线门禁（判定正确但没接线 = 故障照旧）', () => {
  it('两个提交点都必须带上型号能力实参，且能力只有一个来源函数', () => {
    // 与 EdgeDeviceAddressSemanticsGate.spec.ts 的源码级门禁互补：那里钉"没有绕过
    // 解析函数"，这里钉"解析函数拿到了型号能力"。
    const callSites = listSource.match(/resolveInlineDeviceAddress\([\s\S]{0,220}?\)/g) ?? []
    const withCapability = callSites.filter(call => call.includes('resolveModelRequiresTargetAddress'))
    expect(withCapability, '解析调用点未接入型号能力（两个提交点都必须在册）').toHaveLength(2)
    // 单一来源：能力查询只允许从 /device-configs/tree 的叶子字段读取
    expect(listSource).toContain("requires_target_address")
  })
})
