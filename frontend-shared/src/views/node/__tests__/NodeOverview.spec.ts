import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NodeOverview from '../NodeOverview.vue'
import source from '../NodeOverview.vue?raw'

// ── hoisted mocks（形状必须与后端真实响应对齐） ──
const { mockGetDetail, mockChannelList, mockGetCapabilities, mockClientGet, mockSubscribe, mockDmaFetch } = vi.hoisted(() => ({
  mockGetDetail: vi.fn(() => Promise.resolve({
    id: 1, node_id: 'F0F5BDFFFE02', name: '机房采集器', model: 'esp32s3', status: 'online',
    firmware_version: '2.5.18', protocol_version: '2.2', connection_type: 'wifi',
    connection_quality: 92, latency_ms: 12, ping_latency_ms: 12,
    wifi_rssi: -62, free_heap_bytes: 153600, uptime_seconds: 187980,
    last_online_time: new Date(Date.now() - 3600_000).toISOString(),
    config_sync_state: 'in_sync', capabilities: {}, config: {},
  })),
  mockChannelList: vi.fn(() => Promise.resolve([
    { id: 1, node_id: 'F0F5BDFFFE02', name: 'I2C 总线', hardware_type: 'i2c', hardware_id: 'I2C0', status: 'ok', config: {} },
    { id: 2, node_id: 'F0F5BDFFFE02', name: 'UART 通道', hardware_type: 'uart', hardware_id: 'UART1', status: 'error', config: {} },
  ])),
  mockGetCapabilities: vi.fn(() => Promise.resolve({
    buses: {
      i2c: [{ id: 'I2C0', enabled: true, mode: 'master', default_sda_pin: 21, default_scl_pin: 22, freq_hz: 100000 }],
      uart: [], spi: [], adc: [], gpio: [], pwm: [],
    },
  })),
  mockClientGet: vi.fn((url: string) => {
    if (url.includes('/status-history')) {
      return Promise.resolve({ data: [
        { id: 1, node_id: 'F0F5BDFFFE02', event_type: 'status', old_status: 'offline', new_status: 'online', created_at: new Date().toISOString() },
        { id: 2, node_id: 'F0F5BDFFFE02', event_type: 'status', old_status: 'online', new_status: 'offline', created_at: new Date(Date.now() - 7200_000).toISOString() },
      ] })
    }
    return Promise.resolve({ data: null })
  }),
  mockSubscribe: vi.fn(() => vi.fn()),
  mockDmaFetch: vi.fn(() => Promise.resolve()),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ back: vi.fn(), push: vi.fn() }),
  useRoute: () => ({ params: { id: '1' } }),
}))
vi.mock('@/api/node', () => ({
  nodeApi: {
    getDetail: mockGetDetail,
    update: vi.fn(() => Promise.resolve()),
    syncConfig: vi.fn(() => Promise.resolve()),
    ping: vi.fn(() => Promise.resolve({ message: 'ping sent' })),
    getCapabilities: mockGetCapabilities,
    scanI2C: vi.fn(() => Promise.resolve({ devices: [] })),
    queryResources: vi.fn(() => Promise.resolve({ request_id: 'query-1' })),
  },
}))
vi.mock('@/api/channel', () => ({ channelApi: { getList: mockChannelList } }))
vi.mock('@/api/client', () => ({ default: { get: mockClientGet } }))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ connected: true, subscribe: mockSubscribe }),
}))
vi.mock('@/stores/dma', () => ({
  useDmaStore: () => ({ mergedChannels: [], toggling: {}, fetch: mockDmaFetch, clearCache: vi.fn(), toggle: vi.fn() }),
}))
vi.mock('@/utils/logger', () => ({ logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() } }))

const stubs = { OTAForm: true, ChannelManager: true }

describe('NodeOverview (生产页)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('挂载后加载节点详情、通道与事件', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.node-overview-page').exists()).toBe(true)
    expect(mockGetDetail).toHaveBeenCalledWith('1')
    expect(mockChannelList).toHaveBeenCalledWith('F0F5BDFFFE02')
    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/nodes/1/status-history', { params: { limit: 50 } })
  })

  it('渲染页头：名称/状态/连接质量/设备ID', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('机房采集器')
    expect(wrapper.text()).toContain('F0F5BDFFFE02')
    expect(wrapper.text()).toContain('在线')
    expect(wrapper.text()).toContain('92%')
    expect(wrapper.text()).toContain('优秀')
  })

  it('渲染五格统计条：型号/固件/最后上线/在线时长/协议版本', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const strip = wrapper.find('.stat-strip')
    expect(strip.exists()).toBe(true)
    const text = strip.text()
    expect(text).toContain('esp32s3')
    expect(text).toContain('2.5.18')
    expect(text).toContain('2.2')
    expect(text).toContain('在线时长')
  })

  it('实时指标卡仅显示真实后端字段（RSSI/堆内存/延迟/固件在线时长）', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const metrics = wrapper.find('.metrics-card')
    expect(metrics.exists()).toBe(true)
    const text = metrics.text()
    expect(text).toContain('WiFi 信号强度')
    expect(text).toContain('-62')
    expect(text).toContain('空闲堆内存')
    expect(text).toContain('150') // 153600 / 1024 = 150 KB
    expect(text).toContain('通信延迟')
    expect(text).toContain('12')
    // 不得残留 demo 假指标
    expect(text).not.toContain('CPU 使用率')
    expect(text).not.toContain('上行速率')
  })

  it('通道健康卡渲染真实通道与状态统计', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const health = wrapper.find('.health-card')
    expect(health.exists()).toBe(true)
    expect(health.text()).toContain('I2C 总线')
    expect(health.text()).toContain('UART 通道')
    // 1 正常 + 1 异常
    const chips = health.findAll('.chip')
    expect(chips.length).toBe(4)
    expect(chips[0].text()).toContain('2') // 总数
    expect(chips[1].text()).toContain('1') // 正常
    expect(chips[2].text()).toContain('1') // 异常
  })

  it('最近事件卡渲染 status-history 真实事件', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const events = wrapper.find('.events-card')
    expect(events.exists()).toBe(true)
    expect(events.text()).toContain('设备上线')
    expect(events.text()).toContain('设备离线')
  })

  it('离线节点：写操作按钮禁用', async () => {
    mockGetDetail.mockResolvedValueOnce({
      id: 2, node_id: 'OFFLINE01', name: '离线节点', model: 'esp32c6', status: 'offline',
      firmware_version: '2.5.0', connection_quality: 0, latency_ms: 0, ping_latency_ms: 0,
      wifi_rssi: 0, free_heap_bytes: 0, uptime_seconds: 0, capabilities: {}, config: {},
    } as any)
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('离线')
    const buttons = wrapper.findAll('.ph-actions .btn')
    const syncBtn = buttons.find(b => b.text().includes('同步配置'))
    const otaBtn = buttons.find(b => b.text().includes('OTA'))
    const pingBtn = buttons.find(b => b.text().includes('测延迟'))
    expect(syncBtn?.attributes('disabled')).toBeDefined()
    expect(otaBtn?.attributes('disabled')).toBeDefined()
    expect(pingBtn?.attributes('disabled')).toBeDefined()
  })

  it('节点不存在时显示错误空态', async () => {
    mockGetDetail.mockRejectedValueOnce(new Error('404'))
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.no-error').exists()).toBe(true)
    expect(wrapper.text()).toContain('节点不存在或加载失败')
  })

  it('切换总线配置后读取真实 capabilities，渲染资源表和右侧详情', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('总线配置'))
    await tab?.trigger('click')
    await flushPromises()

    expect(mockGetCapabilities).toHaveBeenCalledWith('F0F5BDFFFE02')
    expect(wrapper.find('.bus-main-cols').exists()).toBe(true)
    expect(wrapper.text()).toContain('I2C0')
    expect(wrapper.text()).toContain('SDA21 / SCL22')
    expect(wrapper.text()).toContain('100kHz')
    expect(wrapper.text()).toContain('仅在线可编辑')
  })

  it('总线工作台仅展示真实通道能力，并按设计稿保留工具分组与连续右栏', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('总线配置'))
    await tab?.trigger('click')
    await flushPromises()

    expect(wrapper.find('.bus-workbench').exists()).toBe(true)
    expect(wrapper.findAll('.bus-tool-group')).toHaveLength(2)

    const editor = wrapper.find('.bus-create-card')
    expect(editor.exists()).toBe(true)
    expect(editor.text()).toContain('创建表单支持的字段')
    expect(editor.text()).toContain('从机地址')
    expect(editor.text()).toContain('时钟频率')
    expect(editor.text()).toContain('通道名称')
    expect(editor.text()).toContain('启用状态')
    expect(editor.text()).toContain('新建通道')

    // 设计稿中的 mock 表单分区没有对应生产契约，不能伪造到生产工作台。
    expect(editor.text()).not.toContain('运行参数')
    expect(editor.text()).not.toContain('权限提示')
  })

  it('总线工作台为窄桌面与移动端定义堆叠断点，移动端表格保留可滚动宽度', () => {
    expect(source).toContain('@media (max-width: 1440px)')
    expect(source).toContain('.bus-main-cols { flex-direction: column; }')
    expect(source).toContain('.bus-col-right { width: 100%; flex: 0 0 auto; }')
    expect(source).toContain('@media (max-width: 768px)')
    expect(source).toContain('.bus-create-field-grid { grid-template-columns: 1fr; }')
    expect(source).toMatch(/\.bus-table\s*\{[^}]*min-width:\s*680px/)
  })

  it('移动端将页头操作收纳为两列，并让外层 TAB 横向滚动而非挤压裁切', () => {
    expect(source).toContain('.no-breadcrumb { display: none; }')
    expect(source).toMatch(/\.ph-actions\s*\{[^}]*display:\s*grid[^}]*grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)/)
    expect(source).toMatch(/\.ph-actions \.btn\s*\{[^}]*width:\s*100%[^}]*min-width:\s*0[^}]*justify-content:\s*center/)
    expect(source).toMatch(/\.tab-bar\s*\{[^}]*overflow-x:\s*auto[^}]*overscroll-behavior-x:\s*contain[^}]*scrollbar-width:\s*none/)
    expect(source).toContain('.tab-item { flex: 0 0 auto; }')
  })

  it('离线时总线写操作均被门控', async () => {
    mockGetDetail.mockResolvedValueOnce({
      id: 2, node_id: 'OFFLINE01', name: '离线节点', model: 'esp32c6', status: 'offline',
      firmware_version: '2.5.0', connection_quality: 0, latency_ms: 0, ping_latency_ms: 0,
      wifi_rssi: 0, free_heap_bytes: 0, uptime_seconds: 0, capabilities: {}, config: {},
    } as any)
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('总线配置'))
    await tab?.trigger('click')
    await flushPromises()
    expect(wrapper.find('.bus-alert-offline').exists()).toBe(true)
    expect(wrapper.find('.bus-tool-card .btn-primary').attributes('disabled')).toBeDefined()
  })

  // ── 源码契约断言（防回退） ──
  describe('源码契约', () => {
    it('不含 demo 静态 mock 数据', () => {
      expect(source).not.toContain('30EDA0A9A808')
      expect(source).not.toContain('2026-08-08 21:18:42')
      expect(source).not.toContain('EdgeBox-3000')
      expect(source).not.toContain('genSeries')
      expect(source).not.toContain('sparkPath')
    })

    it('已裁剪后端无数据源的 demo 卡片（位置/备注/时区/搜索/通知）', () => {
      expect(source).not.toContain('设备位置')
      expect(source).not.toContain('map-placeholder')
      expect(source).not.toContain('remark-card')
      expect(source).not.toContain('remarkVisible')
      expect(source).not.toContain('设备时区')
      expect(source).not.toContain('topbar-search')
      expect(source).not.toContain('notifVisible')
      expect(source).not.toContain('通知中心')
    })

    it('页面级 CSS token：--no-primary 存在且不动全局 theme', () => {
      expect(source).toContain('--no-primary: #2E6BFF')
      expect(source).toContain('html.dark .node-overview-page')
    })

    it('防竞态：序列号守卫 + 会话代际断言', () => {
      expect(source).toContain('detailSequence')
      expect(source).toContain('assertSessionGeneration')
      expect(source).toContain('route.params.id !== id')
    })

    it('WS 订阅 NODE_STATUS 与 PING_RESULT（延迟经 ping_result 到达）', () => {
      expect(source).toContain('WS_EVENT.NODE_STATUS')
      expect(source).toContain('WS_EVENT.PING_RESULT')
      expect(source).toContain('latency_ms')
    })

    it('写操作离线门控（nodeOffline）', () => {
      expect(source).toContain('nodeOffline')
      expect(source.match(/:disabled="nodeOffline/g)?.length).toBeGreaterThanOrEqual(2)
    })

    it('总线页使用真实能力/DMA/通道组件，不复制 demo mock 表单', () => {
      expect(source).toContain('nodeApi.getCapabilities')
      expect(source).toContain('dmaStore.toggle')
      expect(source).toContain('<ChannelManager')
      expect(source).toContain('resource.enabled === false')
      expect(source).toContain('isDmaRebindable')
      expect(source).not.toContain('i2c0_temp_sensor')
      expect(source).not.toContain('温度传感器采集通道')
    })

    it('总线数据防竞态并区分错误态与资源未上报空态', () => {
      expect(source).toContain('activateTab')
      expect(source).toContain('requestBusResourceRefresh')
      expect(source).toContain('capabilitiesSequence')
      expect(source).toContain('busLoadError')
      expect(source).toContain('该节点未上报此类型总线资源')
      expect(source).toContain('总线资源加载失败')
      expect(source).toContain('resourceQuerying')
      expect(source).toContain('hardwareResourceMatchesChannel')
    })
  })
})
