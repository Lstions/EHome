/**
 * 缺陷回归：生产库 channels.hardware_type 落库为**大写**（实测只有 'UART'）。
 * ChannelPanel「修改波特率」弹窗的通道下拉此前用 `ch.hardware_type === 'uart'` 过滤
 * ⇒ 大写数据下过滤恒空，下拉没有任何可选通道。
 *
 * 本用例用**大写 fixture** 驱动真实交互（点「改波特率」→ 读弹窗内 <option>），
 * 断言下拉里确实有可选通道，并守住 undefined 不得被归一成 'undefined' 的边界。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type DOMWrapper, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'

const mocks = vi.hoisted(() => ({
  queryResources: vi.fn(),
  getCapabilities: vi.fn(),
  getHardwareConfig: vi.fn(),
  updateHardwareConfig: vi.fn(),
  getChannels: vi.fn(),
  getTemplates: vi.fn(),
  gpioList: vi.fn(),
  gpioCreate: vi.fn(),
  gpioUpdate: vi.fn(),
  gpioDelete: vi.fn(),
  gpioSet: vi.fn(),
  gpioRead: vi.fn(),
  pwmList: vi.fn(),
  pwmCreate: vi.fn(),
  pwmUpdate: vi.fn(),
  pwmDelete: vi.fn(),
  pwmStart: vi.fn(),
  pwmStop: vi.fn(),
  pwmSetDuty: vi.fn(),
  pwmSetFreq: vi.fn(),
  pwmGetState: vi.fn(),
  reconfigure: vi.fn(),
  messageSuccess: vi.fn(),
  messageWarning: vi.fn(),
  messageError: vi.fn(),
}))

vi.mock('@/api/node', () => ({
  nodeApi: {
    queryResources: mocks.queryResources,
    getCapabilities: mocks.getCapabilities,
    getHardwareConfig: mocks.getHardwareConfig,
    updateHardwareConfig: mocks.updateHardwareConfig,
  },
}))
vi.mock('@/api/deviceConfig', () => ({ deviceConfigApi: { getList: mocks.getTemplates } }))
vi.mock('@/api/channel', () => ({
  channelApi: { getList: mocks.getChannels, scan: vi.fn(), reconfigure: mocks.reconfigure },
}))
vi.mock('@/api/periph', () => ({
  gpioApi: {
    list: mocks.gpioList,
    create: mocks.gpioCreate,
    update: mocks.gpioUpdate,
    delete: mocks.gpioDelete,
    set: mocks.gpioSet,
    read: mocks.gpioRead,
  },
  pwmApi: {
    list: mocks.pwmList,
    create: mocks.pwmCreate,
    update: mocks.pwmUpdate,
    delete: mocks.pwmDelete,
    start: mocks.pwmStart,
    stop: mocks.pwmStop,
    setDuty: mocks.pwmSetDuty,
    setFreq: mocks.pwmSetFreq,
    getState: mocks.pwmGetState,
  },
}))
vi.mock('@/stores/dma', () => ({
  useDmaStore: () => ({
    mergedChannels: [],
    toggling: {},
    isSwitchOn: vi.fn(() => false),
    fetch: vi.fn(),
    toggle: vi.fn(),
  }),
}))
vi.mock('@/stores/channel', () => ({
  useChannelStore: () => ({ deleteChannel: vi.fn() }),
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    connected: false,
    connect: vi.fn(),
    disconnect: vi.fn(),
    subscribe: vi.fn(() => vi.fn()),
  }),
}))
vi.mock('@/utils/sessionCache', () => ({
  assertSessionGeneration: vi.fn(),
  getSessionGeneration: vi.fn(() => 1),
}))
vi.mock('@/utils/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))
vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), {
    success: mocks.messageSuccess,
    warning: mocks.messageWarning,
    error: mocks.messageError,
  }),
}))
vi.mock('@/components/channel/ChannelManager.vue', () => ({ default: defineComponent({ template: '<div />' }) }))
vi.mock('@/components/channel/ChannelTerminal.vue', () => ({ default: defineComponent({ template: '<div />' }) }))

import ChannelPanel from '@/components/node/ChannelPanel.vue'

const wrappers: VueWrapper[] = []

afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  vi.useRealTimers()
})

beforeEach(() => {
  vi.clearAllMocks()
  mocks.queryResources.mockRejectedValue(new Error('offline in test'))
  mocks.getCapabilities.mockResolvedValue({ buses: { uart: [{ id: 'UART0', enabled: true }] } })
  mocks.getHardwareConfig.mockResolvedValue({ hardware: { buses: {} } })
  mocks.getChannels.mockResolvedValue([])
  mocks.getTemplates.mockResolvedValue({ items: [] })
  mocks.gpioList.mockResolvedValue([])
  mocks.pwmList.mockResolvedValue([])
  mocks.reconfigure.mockResolvedValue({ data: { status: 'ok' } })
})

/** 挂载并把 refreshBuses 内「等节点上报」的 2s 等待推过去（否则 contentLoading 恒真，模板不渲染）。 */
async function mountReady(): Promise<VueWrapper> {
  vi.useFakeTimers()
  const wrapper = mount(ChannelPanel, {
    props: { collectorId: 7, nodeDeviceId: 'node-1', collectorStatus: 'online' },
  })
  wrappers.push(wrapper)
  await vi.advanceTimersByTimeAsync(2100)
  vi.useRealTimers()
  await flushPromises()
  return wrapper
}

async function openReconfigureDialog(wrapper: VueWrapper): Promise<DOMWrapper<Element>> {
  const button = wrapper.findAll('button').find(candidate => candidate.text().includes('改波特率'))
  if (!button) throw new Error('未渲染「改波特率」按钮：UART 硬件分组或通道标签缺失')
  await button.trigger('click')
  await flushPromises()
  // 用 findAll()[0] 而非 get()：get() 的静态返回类型是 Omit<DOMWrapper,'exists'>，
  // 与 DOMWrapper<Element> 不兼容（TS2740）。findAll 返回完整 DOMWrapper。
  const dialogs = wrapper.findAll('[role="dialog"]')
  const dialog = dialogs[0]
  if (!dialog) throw new Error('未找到「改波特率」弹窗')
  return dialog
}

// 断言必须**限定在「选择通道」那个下拉内**：弹窗里还有「新波特率」下拉
// （9600/19200/38400/57600/115200），若取整个 dialog 的 option，会把波特率选项
// 也算进来 ⇒ 断言恒不成立（实测报 expected [ '39', '9600', … ] to deeply equal [ '39' ]）。
// 这是 H 的用例自身的定位缺陷，不是产品缺陷。
function channelOptions(dialog: DOMWrapper<Element>): DOMWrapper<Element>[] {
  const selects = dialog.findAll('select.el-select')
  const channelSelect = selects[0]
  if (!channelSelect) throw new Error('弹窗内未找到通道下拉')
  return channelSelect.findAll('option.el-option')
}

describe('ChannelPanel 修改波特率弹窗的通道下拉（后端 hardware_type 为大写）', () => {
  it('把大写 UART 通道列为可选项，且不混入非 UART 通道', async () => {
    mocks.getChannels.mockResolvedValue([
      { id: 39, name: 'UART0_9600', hardware_type: 'UART', hardware_id: 'UART0', config: '{"baud_rate":9600}' },
      { id: 1, name: 'I2C0_0x77', hardware_type: 'I2C', hardware_id: 'I2C0', config: '{}' },
    ])
    const wrapper = await mountReady()
    const dialog = await openReconfigureDialog(wrapper)

    const options = channelOptions(dialog)
    expect(options.map(option => option.attributes('value'))).toEqual(['39'])
    expect(options.map(option => option.text()).join('|')).toContain('UART0_9600')
  })

  it('大小写混合都能列出；hardware_type 缺失时不得落成字面量 undefined', async () => {
    mocks.getChannels.mockResolvedValue([
      { id: 39, name: 'upper', hardware_type: 'UART', hardware_id: 'UART0', config: '{}' },
      { id: 40, name: 'missing-type', hardware_id: 'UART0', config: '{}' },
      { id: 41, name: 'lower', hardware_type: 'uart', hardware_id: 'UART0', config: '{}' },
    ])
    const wrapper = await mountReady()
    const dialog = await openReconfigureDialog(wrapper)

    const options = channelOptions(dialog)
    expect(options.map(option => option.attributes('value'))).toEqual(['39', '41'])
    expect(options.map(option => option.text()).join('|')).not.toContain('undefined')
  })
})
