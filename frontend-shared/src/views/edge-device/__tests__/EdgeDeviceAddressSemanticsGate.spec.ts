import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'
import listSource from '@/views/edge-device/EdgeDeviceList.vue?raw'
import apiSource from '@/api/edgeDevice.ts?raw'
import { edgeDeviceApi } from '@/api/edgeDevice'
import { parseDeviceAddress, isValidDeviceAddress } from '@/utils/deviceAddress'

/**
 * task-10 / G3 + G4 —— 边缘设备地址语义的**前端剩余缺口**回归门禁。
 *
 * 背景（2026-09-20 生产故障根因族）：Channel.hardware_id 是**总线资源标识**
 * （"UART1"/"I2C0"），EdgeDevice.hardware_id 是**设备从站地址**（1-254）。
 * 两者被无声混用后，每次派发都在后端 ParseHardwareAddress 被拒，UI 只显示
 * QUEUED，约 120s 后变 FAILED（"deadline expired before dispatch"）。
 *
 * 本文件钉死两条改前**仍然存在**的缺口：
 *   G3 —— 列表页创建向导的「选择已有通道」分支（EdgeDeviceList.vue 约 1579/1591）
 *         直接 hardware_id: targetChannel.hardware_id，绕过 resolveInlineDeviceAddress；
 *         只有 inline-create 分支（约 1522）走了解析。
 *   G4 —— api/edgeDevice.ts normalize 用 channel 兜底给空设备地址填总线名，
 *         于是「打开编辑框 → 点保存」把总线名写回后端。
 *
 * 这些用例在设计上**对改前代码必须变红**（见最终报告里的变异自证）。
 */

const { mockEdgeDeviceGetList, mockGetLogicalDeviceInfo, mockGetDriverCommands, mockGetCapabilities, mockClientGet } = vi.hoisted(() => ({
  // G4 的 normalize 行为断言要打到**真实实现**上：本文件用 importActual 取真模块，
  // 只把它的 HTTP 出口 client.get 换成这个 mock（否则 normalize 根本没被执行）。
  mockClientGet: vi.fn(() => Promise.resolve({ data_count_today: 0 })),
  mockGetCapabilities: vi.fn((..._args: any[]) => Promise.resolve({ buses: {} })),
  mockEdgeDeviceGetList: vi.fn((..._args: any[]) => Promise.resolve({
    items: [
      { id: 1, name: 'Device A', status: 'active', device_type: 'temp_humidity', hardware_type: 'uart', logical_device_id: 11 },
    ],
    total: 1,
  })),
  mockGetLogicalDeviceInfo: vi.fn(() => Promise.resolve({
    edge_device_id: 1, name: 'Logic-A', logical_device_id: 11, retention_days: 30, instance_count: 2, row_estimate: 500,
  })),
  mockGetDriverCommands: vi.fn((..._args: any[]) => Promise.resolve([])),
}))

vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
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
vi.mock('@/api/deviceConfig', () => ({ deviceConfigApi: { getList: vi.fn(() => Promise.resolve({ list: [] })) } }))
vi.mock('@/api/parser', () => ({ parserApi: { getList: vi.fn(() => Promise.resolve([])) } }))
vi.mock('@/api/client', () => ({ default: { get: mockClientGet } }))
vi.mock('@/api/node', () => ({ nodeApi: { getCapabilities: mockGetCapabilities } }))
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

const stubs = {
  SkeletonCard: { template: '<div />' },
  EmptyState: { template: '<div />' },
  CountUp: { template: '<span>{{ $attrs.value }}</span>' },
}

const mountList = () => mount(EdgeDeviceList, { global: { stubs } })

/** 打开创建向导并选中型号（与 EdgeDeviceList.spec.ts 同一路径）。 */
const openWizardWithParser = async (wrapper: any, parser: any) => {
  const createBtn = wrapper.findAll('button').find((b: any) => b.text().includes('创建边缘设备'))
  await createBtn!.trigger('click')
  await flushPromises()
  wrapper.vm.selectedParser = parser
  await wrapper.vm.$nextTick()
  await flushPromises()
  return wrapper.vm
}

const CAPS_FIXTURE = {
  buses: { uart: [{ id: 'UART0' }, { id: 'UART1' }], i2c: [{ id: 'I2C0' }], spi: [{ id: 'SPI2' }] },
}

describe('G3 — 列表页创建向导「选择已有通道」分支的设备地址语义', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('源码级：创建脚本里再无 hardware_id: targetChannel.hardware_id（两处都必须消失）', () => {
    // 改前 EdgeDeviceList.vue:1579 与 :1591 各有一处直接复制总线名。
    // 反向钉死：这个字面量在任何形态下都不得复活。
    expect(listSource).not.toContain('hardware_id: targetChannel.hardware_id')
    // 两处提交点共用同一个解析结果（同一请求内只解析一次）。
    // m-1 后调用点多了一个「型号能力」实参，断言按调用本身匹配而不是按整行文本，
    // 否则格式化换行会让门禁变成"测缩进"。
    expect(listSource).toMatch(/const existingChannelDeviceAddress = resolveInlineDeviceAddress\(\s*targetChannel\.hardware_id,/)
    expect((listSource.match(/hardware_id: existingChannelDeviceAddress\.value/g) ?? [])).toHaveLength(2)
  })

  it('总线名通道（"UART0"）⇒ 提交的 hardware_id 必须是默认地址 1，绝不是总线名', async () => {
    mockGetCapabilities.mockResolvedValue({ buses: CAPS_FIXTURE } as any)
    const wrapper = mountList()
    await flushPromises()
    const vm = await openWizardWithParser(wrapper, { id: 'generic_modbus', name: '通用 Modbus', hardware_types: ['uart'] })
    vm.deviceForm.node_id = 'node-1'
    vm.handleNodeChange()
    await flushPromises()
    vm.deviceForm.name = '已有通道设备'
    vm.channelTab = 'existing'
    vm.selectedChannel = { id: 5, hardware_type: 'uart', hardware_id: 'UART0', config: { device_type: 'generic_modbus' } }
    vm.deviceFormRef = { validate: () => Promise.resolve(), resetFields: () => {} }
    await vm.handleCreate()
    await flushPromises()

    expect(edgeDeviceApi.create).toHaveBeenCalledTimes(1)
    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg.hardware_id).not.toBe('UART0')
    expect(arg.hardware_id).toBe('1')
    // 通道 id 走的是通道语义，不受影响
    expect(arg.channel_id).toBe(5)
    // 提交值必须真的是一个后端可接受的设备地址（同一口径判定，不是硬编码比较）
    expect(isValidDeviceAddress(arg.hardware_id)).toBe(true)
    expect(parseDeviceAddress(arg.hardware_id).explicit).toBe(true)
  })

  it('反例（分类器自检）：通道值本身是合法地址（"0x01"）时必须原样沿用，不被改写成默认值', async () => {
    mockGetCapabilities.mockResolvedValue({ buses: CAPS_FIXTURE } as any)
    const wrapper = mountList()
    await flushPromises()
    const vm = await openWizardWithParser(wrapper, { id: 'generic_modbus', name: '通用 Modbus', hardware_types: ['uart'] })
    vm.deviceForm.node_id = 'node-1'
    vm.handleNodeChange()
    await flushPromises()
    vm.deviceForm.name = '已有通道设备'
    vm.channelTab = 'existing'
    vm.selectedChannel = { id: 5, hardware_type: 'uart', hardware_id: '0x01', config: { device_type: 'generic_modbus' } }
    vm.deviceFormRef = { validate: () => Promise.resolve(), resetFields: () => {} }
    await vm.handleCreate()
    await flushPromises()

    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg.hardware_id).toBe('0x01')
  })

  it('device_config_id 分支（另一条 create 调用点）同样不得复制总线名', async () => {
    mockGetCapabilities.mockResolvedValue({ buses: CAPS_FIXTURE } as any)
    const wrapper = mountList()
    await flushPromises()
    const vm = await openWizardWithParser(wrapper, { id: 'generic_modbus', name: '通用 Modbus', hardware_types: ['uart'], device_config_id: 42 })
    vm.deviceForm.node_id = 'node-1'
    vm.handleNodeChange()
    await flushPromises()
    vm.deviceForm.name = '模板设备'
    vm.channelTab = 'existing'
    vm.selectedChannel = { id: 5, hardware_type: 'uart', hardware_id: 'UART1', config: { device_type: 'generic_modbus' } }
    vm.deviceFormRef = { validate: () => Promise.resolve(), resetFields: () => {} }
    await vm.handleCreate()
    await flushPromises()

    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg.device_config_id).toBe(42)
    expect(arg.hardware_id).not.toBe('UART1')
    expect(arg.hardware_id).toBe('1')
  })

  it('分类器自检：源码断言真的能识别改前的字面量（否则上面的 not.toContain 是空断言）', () => {
    const revertedShape = 'hardware_id: targetChannel.hardware_id'
    expect(revertedShape).toContain('targetChannel.hardware_id')
    // 把改前形态拼进被测文本再检查一次，证明判据本身有效
    const simulatedLegacySource = 'foo\n  ' + revertedShape + '\nbar'
    expect(simulatedLegacySource).toContain('hardware_id: targetChannel.hardware_id')
    expect(listSource).not.toContain(revertedShape)
  })
})

describe('G4 — normalize 不得用通道总线名填充设备地址', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('源码级：normalize 里 hardware_id 只取 d.hardware_id，不再有 channel 兜底', () => {
    // 判据是**赋值语句本身**（'hardware_id: ... || d.channel'），不是任意子串 ——
    // 注释里为说明历史仍会提到旧写法，不能把注释也算成缺陷。
    expect(apiSource).not.toMatch(/hardware_id:\s*d\.hardware_id\s*\|\|\s*d\.channel/)
    expect(apiSource).toContain("hardware_id: d.hardware_id || ''")
    // 总线名的唯一出口保留
    expect(apiSource).toContain('channel_hardware_id: d.channel?.hardware_id || undefined')
  })

  // 注意：本文件的 @/api/edgeDevice 被 mock 了（供组件用例注入 spy），
  // 所以 normalize 的行为断言必须 importActual 取**真实模块**，否则测到的是替身。
  const realApi = async () => (await vi.importActual<typeof import('@/api/edgeDevice')>('@/api/edgeDevice')).edgeDeviceApi

  it('设备地址为空 + 通道是总线名 ⇒ hardware_id 为空，总线名只出现在 channel_hardware_id', async () => {
    mockClientGet.mockResolvedValue({
      data: { items: [{ id: 1, name: 'X', hardware_id: '', channel: { hardware_type: 'uart', hardware_id: 'UART1' } }], total: 1 },
    } as any)
    const res = await (await realApi()).getList()
    expect(res.items).toHaveLength(1)
    expect(res.items[0].hardware_id).toBe('')
    expect(res.items[0].channel_hardware_id).toBe('UART1')
    expect(res.items[0].hardware_id).not.toBe('UART1')
  })

  it('真实设备地址（"1"）照旧保留，不被通道值遮蔽', async () => {
    mockClientGet.mockResolvedValue({
      data: { items: [{ id: 1, name: 'X', hardware_id: '1', channel: { hardware_type: 'uart', hardware_id: 'UART1' } }], total: 1 },
    } as any)
    const res = await (await realApi()).getList()
    expect(res.items[0].hardware_id).toBe('1')
    expect(res.items[0].channel_hardware_id).toBe('UART1')
  })
})

describe('G4 — 编辑-保存回路：地址为空时保存不得产生写入', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('打开编辑框（设备地址为空）→ 直接保存 ⇒ update 的 payload 里没有 hardware_id', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.handleEdit({
      id: 7, name: '无地址设备', node_id: 'node-1', device_type: 'sn3001_rain',
      hardware_type: 'uart', hardware_id: '', interval_ms: 1000, config: { interval_ms: 1000 },
    })
    await flushPromises()
    vm.deviceFormRef = { validate: () => Promise.resolve(), resetFields: () => {} }
    await vm.handleCreate()
    await flushPromises()

    expect(edgeDeviceApi.update).toHaveBeenCalledTimes(1)
    const [id, payload] = (edgeDeviceApi.update as any).mock.calls[0]
    expect(id).toBe(7)
    expect(Object.prototype.hasOwnProperty.call(payload, 'hardware_id')).toBe(false)
    // 其它字段照常发出（不是"整个保存被吞掉"）
    expect(payload.name).toBe('无地址设备')
    expect(payload.node_id).toBe('node-1')
  })

  it('对照：地址已有值但用户没改动 ⇒ 同样不发 hardware_id（不产生写入）', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.handleEdit({
      id: 8, name: 'D', node_id: 'node-1', device_type: 'sn3001_rain',
      hardware_type: 'uart', hardware_id: '1', interval_ms: 1000, config: { interval_ms: 1000 },
    })
    await flushPromises()
    vm.deviceFormRef = { validate: () => Promise.resolve(), resetFields: () => {} }
    await vm.handleCreate()
    await flushPromises()
    const [, payload] = (edgeDeviceApi.update as any).mock.calls[0]
    expect(Object.prototype.hasOwnProperty.call(payload, 'hardware_id')).toBe(false)
  })

  it('防修复过度：地址真的被改动时才发出（用户改地址仍要能保存）', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.handleEdit({
      id: 9, name: 'D', node_id: 'node-1', device_type: 'sn3001_rain',
      hardware_type: 'uart', hardware_id: '1', interval_ms: 1000, config: { interval_ms: 1000 },
    })
    await flushPromises()
    // 模拟"用户在编辑框里把地址改成 0x02"（当前表单无该输入项，直接改 ref 等价）
    vm.deviceForm.hardware_id = '0x02'
    vm.deviceFormRef = { validate: () => Promise.resolve(), resetFields: () => {} }
    await vm.handleCreate()
    await flushPromises()
    const [, payload] = (edgeDeviceApi.update as any).mock.calls[0]
    expect(payload.hardware_id).toBe('0x02')
  })

  it('源码级：编辑分支不得再用无条件 hardware_id 字段', () => {
    expect(listSource).not.toContain('hardware_id: frozenDeviceForm.hardware_id')
    expect(listSource).toContain('editingDeviceAddressSnapshot')
  })
})
