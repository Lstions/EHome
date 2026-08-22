import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ChannelTerminal from '../ChannelTerminal.vue'
import source from '../ChannelTerminal.vue?raw'

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

describe('ChannelTerminal collector isolation', () => {
  it('invalidates local channel requests and terminal state when collector changes', () => {
    expect(source).toContain('channelRequestGeneration++')
    expect(source).toContain('props.collectorId !== collectorId')
    expect(source).toContain('props.nodeDeviceId !== nodeDeviceId')
    expect(source).toContain('watch(() => [props.collectorId, props.nodeDeviceId]')
    expect(source).toContain('selectedChannelId.value = undefined')
    expect(source).toContain('localChannels.value = []')
    expect(source).toContain('clearLog()')
    expect(source).toContain('generation !== channelRequestGeneration')
    expect(source).toContain('sending.value = false')
  })
})

// 通道终端初始预选（initialChannelId）行为测试
const mkChannel = (id: number, hardwareType: 'uart' | 'i2c' | 'spi' | 'adc' = 'uart') => ({
  id,
  node_id: 'TESTNODE001',
  name: `ch-${id}`,
  hardware_type: hardwareType,
  hardware_id: `UART0`,
  config: {},
})

const baseProps = (overrides: Record<string, unknown> = {}) => ({
  collectorId: 1,
  ...overrides,
})

const mountTerminal = async (props: Record<string, unknown>) => {
  const wrapper = mount(ChannelTerminal, {
    props,
    global: { plugins: [createPinia()] },
    attachTo: document.body,
  })
  await flushPromises()
  return wrapper
}

describe('ChannelTerminal initialChannelId pre-selection', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('preselects the channel when initialChannelId exists in channels', async () => {
    const channels = [mkChannel(101), mkChannel(102), mkChannel(103)]
    const wrapper = await mountTerminal(baseProps({ channels, initialChannelId: 102 }))
    const select = wrapper.find('select.el-select')
    expect(select.exists()).toBe(true)
    expect((select.element as HTMLSelectElement).value).toBe('102')
  })

  it('does not crash with a non-existent id and stays on default (no selection)', async () => {
    const channels = [mkChannel(101), mkChannel(102)]
    const wrapper = await mountTerminal(baseProps({ channels, initialChannelId: 999 }))
    const select = wrapper.find('select.el-select')
    expect(select.exists()).toBe(true)
    // 无效 id：不崩溃、不强行选中，保持默认空选
    expect((select.element as HTMLSelectElement).value).toBe('')
  })

  it('keeps existing behavior when initialChannelId is not passed', async () => {
    const channels = [mkChannel(101), mkChannel(102)]
    const wrapper = await mountTerminal(baseProps({ channels }))
    const select = wrapper.find('select.el-select')
    expect(select.exists()).toBe(true)
    // 与既有行为一致：无预选时保持默认空选
    expect((select.element as HTMLSelectElement).value).toBe('')
  })

  it('preselects once when channels arrive asynchronously, then follows subsequent initialChannelId updates', async () => {
    const wrapper = await mountTerminal(baseProps({ channels: [], initialChannelId: 101 }))
    let select = wrapper.find('select.el-select')
    expect(select.exists()).toBe(true)
    // channels 为空数组时：暂不预选
    expect((select.element as HTMLSelectElement).value).toBe('')

    // 异步 channels 到达（含 101）→ 预选生效
    await wrapper.setProps({ channels: [mkChannel(100), mkChannel(101)] })
    await flushPromises()
    select = wrapper.find('select.el-select')
    expect((select.element as HTMLSelectElement).value).toBe('101')

    // 模拟用户手动切换通道 → 后续 channels 变化不再覆盖用户选择（只预选一次）
    await wrapper.setProps({ channels: [mkChannel(100), mkChannel(101)], initialChannelId: 101 })
    await wrapper.find('select.el-select').setValue('100')
    await flushPromises()
    select = wrapper.find('select.el-select')
    expect((select.element as HTMLSelectElement).value).toBe('100')
    await wrapper.setProps({ channels: [mkChannel(100), mkChannel(101), mkChannel(103)] })
    await flushPromises()
    select = wrapper.find('select.el-select')
    expect((select.element as HTMLSelectElement).value).toBe('100')

    // 换一个有效 initialChannelId → 跟随更新
    await wrapper.setProps({ channels: [mkChannel(100), mkChannel(102)], initialChannelId: 102 })
    await flushPromises()
    select = wrapper.find('select.el-select')
    expect((select.element as HTMLSelectElement).value).toBe('102')
  })

  it('re-applies when initialChannelId changes to a valid id after channels are already loaded', async () => {
    const channels = [mkChannel(101), mkChannel(102)]
    const wrapper = await mountTerminal(baseProps({ channels, initialChannelId: 101 }))
    let select = wrapper.find('select.el-select')
    expect((select.element as HTMLSelectElement).value).toBe('101')

    // initialChannelId 变为另一有效 id → 跟随更新（不依赖 channels 重新到达）
    await wrapper.setProps({ initialChannelId: 102 })
    await flushPromises()
    select = wrapper.find('select.el-select')
    expect((select.element as HTMLSelectElement).value).toBe('102')
  })

  it('ignores invalid initialChannelId updates without clearing the current selection', async () => {
    const channels = [mkChannel(101), mkChannel(102)]
    const wrapper = await mountTerminal(baseProps({ channels, initialChannelId: 101 }))
    let select = wrapper.find('select.el-select')
    expect((select.element as HTMLSelectElement).value).toBe('101')

    // 无效更新（undefined）→ 不动作、不清空当前选中
    await wrapper.setProps({ initialChannelId: undefined })
    await flushPromises()
    select = wrapper.find('select.el-select')
    expect((select.element as HTMLSelectElement).value).toBe('101')

    // 无效更新（不存在的 id）→ 同样不动作
    await wrapper.setProps({ initialChannelId: 404 })
    await flushPromises()
    select = wrapper.find('select.el-select')
    expect((select.element as HTMLSelectElement).value).toBe('101')
  })

  // MAJOR 回归证明：self-fetch 模式（不传 channels prop）下，getList 异步返回后预选必须生效
  it('preselects after self-fetch resolves when channels prop is not provided', async () => {
    const { channelApi } = await import('@/api/channel')
    ;(channelApi.getList as any).mockResolvedValueOnce([mkChannel(100), mkChannel(101)])
    // 不传 channels prop → 走 self-fetch 分支（loadChannels 写 localChannels）
    const wrapper = await mountTerminal(baseProps({ initialChannelId: 101 }))
    await flushPromises()
    const select = wrapper.find('select.el-select')
    expect(select.exists()).toBe(true)
    expect((select.element as HTMLSelectElement).value).toBe('101')
  })
})
