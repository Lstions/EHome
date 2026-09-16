/**
 * 缺陷回归：后端 channels.hardware_type 为大写（'UART'/'SPI'/'I2C'）。
 * ChannelTerminal 的波特率 tag 与「读长度」控件此前按 `=== 'uart'` / `=== 'spi'` 判断
 * ⇒ 大写数据下 UI 信息缺失。
 * 本用例用**大写 fixture** 驱动，断言这些控件确实渲染。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ChannelTerminal from '../ChannelTerminal.vue'
import type { Channel } from '@/api/channel'

vi.mock('@/api/channel', () => ({
  channelApi: { getList: vi.fn().mockResolvedValue([]), terminalWrite: vi.fn() },
}))

vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    connected: false,
    connect: vi.fn(),
    subscribe: vi.fn(() => () => {}),
    send: vi.fn(),
  }),
}))

vi.mock('@/utils/logger', () => ({
  logger: { error: vi.fn(), info: vi.fn(), warn: vi.fn(), debug: vi.fn() },
}))

const mkChannel = (id: number, hardwareType: string | undefined, hardwareId: string, busConfig?: string): Channel => ({
  id,
  node_id: 'TESTNODE001',
  name: `ch-${id}`,
  hardware_type: hardwareType as Channel['hardware_type'],
  hardware_id: hardwareId,
  bus_config: busConfig,
  config: {},
})

// 用组件真实 props 形状，避免把宽 Record 传给 mount 的 props（TS2322）。
// 这些字段是本 spec 实际用到的子集，缺省值保证各用例只传需要覆盖的键。
interface TerminalProps {
  collectorId: number | string
  nodeDeviceId?: string
  channels?: Channel[]
  initialChannelId?: number
}

const baseProps = (overrides: Partial<TerminalProps> = {}): TerminalProps => ({
  collectorId: 1,
  ...overrides,
})

const mountTerminal = async (props: TerminalProps) => {
  const wrapper = mount(ChannelTerminal, {
    props,
    global: { plugins: [createPinia()] },
    attachTo: document.body,
  })
  await flushPromises()
  return wrapper
}

describe('ChannelTerminal 硬件类型判断（后端 hardware_type 为大写）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('大写 UART 通道显示波特率 tag（值取自 bus_config）', async () => {
    const channels = [mkChannel(101, 'UART', 'UART0', '000000002580')]
    const wrapper = await mountTerminal(baseProps({ channels, initialChannelId: 101 }))

    const baudTag = wrapper.findAll('.el-tag').find(tag => tag.text().includes('baud'))
    expect(baudTag?.text()).toBe('9600 baud')
  })

  it('小写 uart 仍显示波特率 tag（不回归）', async () => {
    const channels = [mkChannel(102, 'uart', 'UART0', '000000002580')]
    const wrapper = await mountTerminal(baseProps({ channels, initialChannelId: 102 }))

    const baudTag = wrapper.findAll('.el-tag').find(tag => tag.text().includes('baud'))
    expect(baudTag?.text()).toBe('9600 baud')
  })

  it('大写 SPI/I2C 通道显示「读长度」控件，切换通道后同样显示', async () => {
    const channels = [
      mkChannel(201, 'SPI', 'SPI0'),
      mkChannel(202, 'I2C', 'I2C0'),
      mkChannel(203, 'UART', 'UART0'),
    ]
    const wrapper = await mountTerminal(baseProps({ channels, initialChannelId: 201 }))
    expect(wrapper.find('input.el-input-number').exists()).toBe(true)

    await wrapper.find('select.el-select').setValue('202')
    await flushPromises()
    expect(wrapper.find('input.el-input-number').exists()).toBe(true)
  })

  it('UART 通道不显示「读长度」控件（大写下同样成立）', async () => {
    const channels = [mkChannel(204, 'UART', 'UART0', '000000002580')]
    const wrapper = await mountTerminal(baseProps({ channels, initialChannelId: 204 }))
    expect(wrapper.find('input.el-input-number').exists()).toBe(false)
  })
})
