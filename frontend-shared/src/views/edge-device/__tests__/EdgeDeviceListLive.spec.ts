import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'
import { useEdgeDeviceLiveStore } from '@/stores/edgeDeviceLive'
import { WS_EVENT } from '@/events/events'

/**
 * 页面 × 实时层的集成用例。
 *
 * 与 EdgeDeviceList.spec.ts 的区别：那个文件把 @/api/edgeDevice 整体 mock 掉，
 * 只验证页面行为；本文件**不 mock 该模块**（用真实 normalize / mapStatus /
 * getList），只 mock 最底层的 HTTP client 与周边 API —— 这样断言的是
 * 「WS 补丁 → 展示数据 → 卡片/统计」这条真实链路。
 */

const mockGet = vi.hoisted(() => vi.fn())

vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))

vi.mock('@/api/client', () => ({ default: { get: mockGet } }))

vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: vi.fn(() => Promise.resolve()),
    getCachedList: vi.fn(() => ({ items: [{ id: 1, node_id: 'node-1', name: 'Collector-A', status: 'online' }], total: 1 })),
  }),
}))

vi.mock('@/api/driver', () => ({
  getDriverTree: vi.fn(() => Promise.resolve([])),
  getDriverList: vi.fn(() => Promise.resolve([])),
  findDriverLeaf: vi.fn(() => undefined),
  default: {},
}))
vi.mock('@/api/channel', () => ({
  compactChannelList: (items: unknown) => (Array.isArray(items) ? items : []),
  channelApi: { getList: vi.fn(() => Promise.resolve([])), update: vi.fn() },
}))
vi.mock('@/api/deviceConfig', () => ({ deviceConfigApi: { getList: vi.fn(() => Promise.resolve({ list: [] })) } }))
vi.mock('@/api/parser', () => ({ parserApi: { getList: vi.fn(() => Promise.resolve([])) } }))
vi.mock('@/api/node', () => ({ nodeApi: { getCapabilities: vi.fn(() => Promise.resolve({ buses: {} })) } }))

const DEVICES = [
  { id: 1, name: '温湿度-A', status: 'active', device_type: 'temp_humidity', hardware_type: 'uart', channel_id: 3, last_data: { temperature: 20 }, last_data_at: '2026-11-19T10:00:00.000Z' },
  { id: 2, name: '风速-B', status: 'offline', device_type: 'wind_speed', hardware_type: 'i2c' },
]

function installMockClient() {
  mockGet.mockImplementation(async (url: string) => {
    if (url === '/api/v1/overview') return { data: { data_count_today: 7 } }
    if (url === '/api/v1/edge-devices') {
      return { data: { items: DEVICES, total: DEVICES.length, page: 1, page_size: 24 } }
    }
    return { data: {} }
  })
}

function listCalls(): number {
  return mockGet.mock.calls.filter(([url]) => url === '/api/v1/edge-devices').length
}

const stubs = {
  SkeletonCard: { template: '<div data-testid="skeleton-card" />' },
  EmptyState: { template: '<div data-testid="empty-state" />' },
  CountUp: { template: '<span data-testid="count-up">{{ $attrs.value }}</span>' },
}

function mountList() {
  return mount(EdgeDeviceList, { global: { stubs } })
}

function frame(deviceId: number, data: Record<string, number>) {
  return {
    type: WS_EVENT.DATA_UPDATE,
    payload: { edge_device_id: deviceId, data, collected_at: new Date().toISOString() },
  }
}

describe('EdgeDeviceList × 实时层（1Hz 自动刷新）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    // 只假 setInterval 与 Date：setTimeout 保持真实，flushPromises 才不会卡住。
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(new Date('2026-11-19T10:00:00.000Z'))
    installMockClient()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('WS 推送的读数会刷新卡片，且**不**重新请求列表', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(listCalls()).toBe(1)
    expect(wrapper.text()).toContain('20.0') // 快照里的 temperature=20

    const live = useEdgeDeviceLiveStore()
    live.ingest(WS_EVENT.DATA_UPDATE, frame(1, { temperature: 26.4, humidity: 61 }))
    // 节拍未到：不得提前渲染（1Hz 契约）
    expect(wrapper.text()).not.toContain('26.4')

    vi.advanceTimersByTime(1000)
    await flushPromises()

    expect(wrapper.text()).toContain('26.4')
    expect(wrapper.text()).toContain('61.0')
    // 关键：读数更新走 WS，不该顺带打列表接口
    expect(listCalls()).toBe(1)
  })

  it('状态事件同时驱动卡片状态与顶部统计（不再需要手动刷新）', async () => {
    const wrapper = mountList()
    await flushPromises()

    const cards = () => wrapper.findAll('.device-card')
    expect(cards()[1].find('.status-indicator').text()).toContain('离线')
    // 初始：1 在线 + 1 离线
    const statValues = () => wrapper.findAll('[data-testid="count-up"]').map(node => node.text())
    expect(statValues()[0]).toBe('2')
    expect(statValues()[1]).toBe('1')

    const live = useEdgeDeviceLiveStore()
    live.ingest(WS_EVENT.EDGE_DEVICE_STATUS, {
      type: WS_EVENT.EDGE_DEVICE_STATUS,
      payload: { edge_device_id: 2, device_id: 2, status: 'active' },
    })
    vi.advanceTimersByTime(1000)
    await flushPromises()

    expect(cards()[1].find('.status-indicator').text()).toContain('在线')
    expect(statValues()[1]).toBe('2')
    expect(listCalls()).toBe(1)
  })

  it('实时到秒：刚采集的数据显示「刚刚/秒前」，而不是一成不变的分钟级文案', async () => {
    const wrapper = mountList()
    await flushPromises()

    const live = useEdgeDeviceLiveStore()
    live.ingest(WS_EVENT.DATA_UPDATE, frame(1, { temperature: 25 }))
    vi.advanceTimersByTime(1000)
    await flushPromises()
    expect(wrapper.find('.card-updated').text()).toContain('刚刚')

    // 采集发生在 t0、首次 flush 在 t0+1s（此后又过 5 秒）⇒ 文案是 6 秒前。
    // 钉住"秒级"本身：旧实现的分钟级文案在这里只会是「刚刚」。
    vi.advanceTimersByTime(5000)
    await flushPromises()
    expect(wrapper.find('.card-updated').text()).toContain('6秒前')
  })

  it('WS 不可用时降级为 3s 轮询；1s 节拍本身不发请求', async () => {
    mountList()
    await flushPromises()
    expect(listCalls()).toBe(1)

    vi.advanceTimersByTime(1000)
    await flushPromises()
    expect(listCalls()).toBe(1) // 第 1 拍：距离上次请求 < 3s，跳过

    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(listCalls()).toBe(2) // 第 3 拍：降级轮询生效（初始 connected=false）
  })
})
