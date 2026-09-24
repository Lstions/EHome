import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { reactive } from 'vue'
import NodeOverview from '../NodeOverview.vue'
import source from '../NodeOverview.vue?raw'

// ── hoisted mocks（形状必须与后端真实响应对齐） ──
const { mockGetDetail, mockChannelList, mockGetCapabilities, mockClientGet, mockSubscribe, mockDmaFetch, mockRouterPush, mockFetchDevices, mockGetCachedList, mockInvalidateLists, mockGetOTAHistory, mockCancelOTA, mockElMessageBoxConfirm, mockDmaChannelsRef, mockApplyRuntimeLevel, mockApplyRuntimeState, mockPeriphReload, mockRouteQuery } = vi.hoisted(() => ({
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
  mockDmaFetch: vi.fn((..._args: any[]) => Promise.resolve()),
  mockRouterPush: vi.fn(),
  mockFetchDevices: vi.fn((..._args: any[]) => Promise.resolve()),
  mockGetCachedList: vi.fn((..._args: any[]): { items: any[]; total: number } => ({ items: [], total: 0 })),
  mockInvalidateLists: vi.fn(),
  mockGetOTAHistory: vi.fn((..._args: any[]): Promise<any[]> => Promise.resolve([])),
  mockCancelOTA: vi.fn((..._args: any[]) => Promise.resolve()),
  mockElMessageBoxConfirm: vi.fn((..._args: any[]): Promise<any> => Promise.resolve()),
  // 可变 DMA store 数据（测试可注入）
  mockDmaChannelsRef: { value: [] as any[] },
  // 外设直控回填断言 + 可变 route.query（?tab= 深链用例需要按用例改 query）
  mockApplyRuntimeLevel: vi.fn(),
  mockApplyRuntimeState: vi.fn(),
  mockPeriphReload: vi.fn(),
  mockRouteQuery: { value: {} as Record<string, unknown> },
}))

/** 派发一次 WS periph_result：真实链路是 wsStore.subscribe 注册的回调被推送时触发。 */
function emitPeriphResult(payload: Record<string, unknown>) {
  // mockSubscribe 是 vi.fn()，其推断签名不含参数，故这里显式取调用记录并收窄。
  const calls = mockSubscribe.mock.calls as unknown as Array<[string, (m: unknown) => void]>
  const call = calls.find(c => c[0] === 'periph_result')
  const handler = call?.[1]
  if (typeof handler === 'function') handler({ event: 'periph_result', payload })
}

// D1 门禁需要"目标 vs 当前页"的语义比较，而不是"push 被调用过"。
// 用一个**会真的改变当前路由**的假 router：push 同时写 pushHistory 与 currentRoute，
// 这样测试才可能在"目标与当前页歧义/相同"时失败 —— 只记录调用次数的话，
// 推当前页与推有效目标在断言上完全等价（这正是 D1 藏了这么久的机制）。
const { mockRouterState } = vi.hoisted(() => ({
  mockRouterState: {
    /** 当前路由（初始即 NodeOverview 自身：name=NodeDetail, path=/node/1） */
    current: { name: 'NodeDetail', path: '/node/1' } as { name?: string; path?: string },
    pushes: [] as any[],
    /** 可变 route.params —— 跨节点切换用例需要改它来触发 watch(route.params.id) */
    params: { id: '1' } as Record<string, string>,
    /**
     * params 的 reactive 代理（首次 useRoute 时创建）。
     *
     * 必须走代理写入：组件内部是 watch(() => route.params.id) —— 直接改原始对象
     * 不会触发依赖，用例就会「假装测了节点切换但 watch 从未运行」（本文件已因此
     * 漏过一次变异：删掉代际校验后测试仍然全绿）。
     */
    reactiveParams: null as Record<string, string> | null,
  },
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({
    back: vi.fn(),
    push: mockRouterPush,
    currentRoute: { value: mockRouterState.current },
  }),
  useRoute: () => {
    // 惰性建 reactive 代理，使 watch(() => route.params.id) 能真正被触发
    if (!mockRouterState.reactiveParams) mockRouterState.reactiveParams = reactive(mockRouterState.params)
    return { params: mockRouterState.reactiveParams, query: mockRouteQuery.value, name: 'NodeDetail', path: '/node/1' }
  },
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
    getOTAHistory: mockGetOTAHistory,
    cancelOTA: mockCancelOTA,
  },
}))
vi.mock('@/api/channel', () => ({ channelApi: { getList: mockChannelList } }))
vi.mock('@/api/client', () => ({ default: { get: mockClientGet } }))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ connected: true, subscribe: mockSubscribe }),
}))
vi.mock('@/stores/dma', () => ({
  useDmaStore: () => ({ mergedChannels: mockDmaChannelsRef.value, loading: false, toggling: {}, fetch: mockDmaFetch, clearCache: vi.fn(), toggle: vi.fn() }),
}))
vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({
    fetchList: mockFetchDevices,
    getCachedList: mockGetCachedList,
    invalidateLists: mockInvalidateLists,
    clearCache: vi.fn(),
  }),
}))
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessageBox: { confirm: mockElMessageBoxConfirm },
  }
})
vi.mock('@/utils/logger', () => ({ logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() } }))

// push 的语义后果：真实 vue-router 会让"当前路由"变成目标（并解析 {name,query}）。
// 这里复刻该行为，使"目标 !== 当前页"成为可失败的断言。
mockRouterPush.mockImplementation((target: any) => {
  mockRouterState.pushes.push(target)
  if (typeof target === 'string') {
    mockRouterState.current = { path: target }
  } else if (target && typeof target === 'object') {
    const query = target.query
      ? '?' + Object.entries(target.query).map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`).join('&')
      : ''
    const resolved = target.name === 'ChannelList' ? '/channel' : (target.path || '/node/1')
    mockRouterState.current = { name: target.name, path: resolved + query }
  }
  return Promise.resolve()
})

// ChannelManager 需要可查询的 props（D1 门禁要断言 initial-data = 本行通道），
// 因此用一个最小 stub 而不是 true（true 渲染成匿名组件，拿不到 props）。
const ChannelManagerStub = {
  name: 'ChannelManager',
  template: '<div class="channel-manager-stub" />',
  props: ['modelValue', 'collectorId', 'capabilities', 'presetHardwareType', 'presetHardwareId', 'collectorStatus', 'initialData'],
}

const stubs = {
  OTAForm: true,
  ChannelManager: ChannelManagerStub,
  ChannelTerminal: {
    template: '<div class="channel-terminal-stub"><slot /><span v-if="collectorId">collector={{ collectorId }}</span><span v-if="nodeDeviceId">device={{ nodeDeviceId }}</span><span v-if="channels">channels={{ channels.length }}</span></div>',
    props: ['collectorId', 'nodeDeviceId', 'channels'],
  },
  LogPanel: {
    template: '<div class="log-panel-stub"><span v-if="collectorId">collector={{ collectorId }}</span><span v-if="nodeDeviceId">device={{ nodeDeviceId }}</span></div>',
    props: ['collectorId', 'nodeDeviceId'],
  },
  QuickCreateDeviceDialog: {
    name: 'QuickCreateDeviceDialog',
    template: '<div class="quick-create-stub"><span v-if="modelValue">open</span></div>',
    props: ['modelValue', 'nodeId', 'nodeName', 'channels', 'channelsLoading'],
  },
  StatusBadge: {
    template: '<span class="status-badge-stub">{{ status }}</span>',
    props: ['status'],
  },
}

/**
 * 剥掉 CSS 注释后再做样式断言。
 *
 * 为什么必须剥：本文件的样式用例既要断言"新规则存在"，又要断言"旧规则已消失"，
 * 而改动的说明性注释里会**引用被删除的旧规则原文**（如 "改前 .no-breadcrumb{display:none}"）。
 * 若带着注释做 not.toMatch，断言会被自己的文档文字误伤 —— 这是"源码字符串断言"的经典陷阱。
 */
function stripCssComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '')
}

describe('NodeOverview (生产页)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    // 每个用例都从"当前就在 NodeOverview 自身"这一真实前提开始
    mockRouterState.current = { name: 'NodeDetail', path: '/node/1' }
    mockRouterState.pushes.length = 0
    // query 是可变对象，clearAllMocks 不会重置它 —— 不重置会让 ?tab= 用例相互污染
    mockRouteQuery.value = {}
    if (mockRouterState.reactiveParams) mockRouterState.reactiveParams.id = '1'
    else mockRouterState.params.id = '1'
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

  // ─── D1（本任务的核心门禁）：点通道行必须是**有效导航** ───
  //
  // 为什么旧测试测不出来：它只断言 mockRouterPush 被调用过。而改前的实现是
  //   goToDetail() { router.push(\`/node/\${nodeSerial.value}\`) }
  // —— 本页路由就是 /node/:id，于是「调用过」与「原地打转」在断言上完全等价。
  // 这里改为断言**目标的语义有效性**：目标既不能等于当前路由（by name、by path），
  // 也不能落在本页自身的路径族里，且必须真的携带该节点的过滤参数。
  it('D1：点通道行的导航目标不是当前页，而是 /channel?node=<序列号>', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const rows = wrapper.findAll('.health-card .chan-row')
    expect(rows.length, '前置条件：通道健康卡必须有可点的通道行').toBeGreaterThan(0)
    const currentPath = mockRouterState.current.path
    const currentName = mockRouterState.current.name
    expect(currentName, '前置条件：当前页就是 NodeOverview 自身').toBe('NodeDetail')

    await rows[0].trigger('click')
    await flushPromises()

    expect(mockRouterState.pushes).toHaveLength(1)
    const target = mockRouterState.pushes[0]
    expect(typeof target, '目标必须是可解析的路由对象（不是拼接字符串，避免丢参）').toBe('object')
    expect(target.name).toBe('ChannelList')
    expect(target.query?.node).toBe('F0F5BDFFFE02')

    // ★ 有效性断言（这一条才是本次缺陷的判据）
    expect(target.name, '目标路由名不得等于当前路由名（否则就是原地打转）').not.toBe(currentName)
    expect(target.path ?? '', '目标不得推当前页路径').not.toBe(currentPath)
    // 反向守卫：模拟 push 之后的当前路由必须真的离开了本页
    expect(mockRouterState.current.path, '导航后必须离开当前页').not.toBe(currentPath)
    expect(mockRouterState.current.path).toContain('/channel')

    // 分类器自检：把「推当前页」这个旧行为喂给同一套判据，必须判为无效 ——
    // 否则这段断言可能因为判据写错而永远绿（本仓已多次发生此类假绿）。
    const verdict = (t: any) => {
      const resolvesToSelf = t?.name === currentName || (t?.path || '') === currentPath
      const leavesNodeDetail = String(t?.path ?? '/channel').includes('/channel')
      return !resolvesToSelf && leavesNodeDetail
    }
    expect(verdict({ name: 'NodeDetail', path: '/node/1' }), '旧实现（推当前页）必须被判为无效目标').toBe(false)
    expect(verdict({ name: 'ChannelList', path: '/channel?node=F0F5BDFFFE02' })).toBe(true)
  })

  it('D1：通道行键盘可达（role/tabindex/Enter/Space），旧契约不得复活', () => {
    // 原生可聚焦元素之外的可点区域必须四件套齐全，否则键盘用户到不了这个入口；
    // 全站门禁 IconActionAccessibilityGate.spec.ts 会在缺件时报红。
    const start = source.indexOf('class="chan-row"')
    expect(start, '源码里必须还有 .chan-row').toBeGreaterThan(0)
    const rowTag = source.slice(start, start + 560)
    expect(rowTag).toContain('role="button"')
    expect(rowTag).toContain('tabindex="0"')
    expect(rowTag).toContain('@keydown.enter.prevent="navigateToNodeChannels"')
    expect(rowTag).toContain('@keydown.space.prevent="navigateToNodeChannels"')
    // 旧契约（推当前页）不得复活。
    // 判据只作用于**可执行代码**：源码注释里会引用旧写法作为修复说明，
    // 不剥注释就会被自己的文档文字误伤（同 OTAFormStatusCoverage.spec.ts 的陷阱）。
    const codeOnly = source
      .replace(/<!--[\s\S]*?-->/g, '')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/^\s*\/\/.*$/gm, '')
    expect(codeOnly, '模板里不得再有 goToDetail 点击').not.toContain('@click="goToDetail"')
    expect(codeOnly, '函数 goToDetail 不得复活').not.toContain('function goToDetail')
    expect(codeOnly).not.toContain('router.push(`/node/')
    expect(codeOnly).toContain('function navigateToNodeChannels()')
  })

  // ─── D4：点「查看」必须把资源详情切换到**该行** ───
  it('D4：逐行点「查看」，右侧资源详情切换为该行资源（UART0 → UART1 → 回到 UART0）', async () => {
    mockGetCapabilities.mockResolvedValueOnce({
      buses: {
        uart: [
          { id: 'UART0', enabled: true, default_tx_pin: 16, default_rx_pin: 17, max_baud: 115200 },
          { id: 'UART1', enabled: true, default_tx_pin: 20, default_rx_pin: 21, max_baud: 5000000 },
        ],
        i2c: [], spi: [], adc: [], gpio: [], pwm: [],
      },
    } as any)
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    await wrapper.findAll('.tab-item').find(item => item.text().includes('总线配置'))?.trigger('click')
    await flushPromises()
    await wrapper.findAll('.bus-subtab').find(b => b.text().includes('UART'))?.trigger('click')
    await flushPromises()

    const rows = wrapper.findAll('.bus-table tbody tr')
    expect(rows).toHaveLength(2)
    const detailText = () => wrapper.find('[data-bus-detail]').text()
    const viewBtn = (i: number) => rows[i].findAll('button').find(b => b.text().includes('查看'))!

    await viewBtn(0).trigger('click')
    await flushPromises()
    const afterRow0 = detailText()
    expect(afterRow0).toContain('UART0')
    expect(afterRow0, '详情必须切换成该行的引脚参数').toContain('TX16 / RX17')

    // 第 2 行：点「查看」→ 详情必须**变化**成 UART1（这正是改前失败的地方）
    await viewBtn(1).trigger('click')
    await flushPromises()
    const afterRow1 = detailText()
    expect(afterRow1, '点第 2 行后详情内容必须发生变化').not.toBe(afterRow0)
    expect(afterRow1).toContain('UART1')
    expect(afterRow1).toContain('TX20 / RX21')
    expect(afterRow1, '详情不得残留上一行的资源').not.toContain('UART0')

    // 点回第 1 行同样必须切回（单向切换不算修好）
    await viewBtn(0).trigger('click')
    await flushPromises()
    expect(detailText()).toContain('UART0')
    expect(detailText()).not.toContain('UART1')
  })

  // ─── D2：计数与列表必须同源同过滤，截断必须显式 ───
  it('D1：行内「编辑」打开该行通道的配置对话框（initial-data ⇒ 编辑态）', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const rows = wrapper.findAll('.health-card .chan-row')
    expect(rows.length).toBeGreaterThan(0)
    const editBtn = rows[0].find('button.chan-edit')
    expect(editBtn.exists(), '通道行必须有行内编辑入口').toBe(true)
    await editBtn.trigger('click')
    await flushPromises()
    const stub = wrapper.findComponent(ChannelManagerStub)
    expect(stub.exists()).toBe(true)
    expect(stub.props('modelValue'), '点编辑必须打开对话框').toBe(true)
    expect((stub.props('initialData') as any)?.id, 'initial-data 必须是本行通道（编辑态而非新建态）').toBe(1)
  })
  it('D2：总数/正常/异常由同一份 channels 派生，列表截断显式可见', async () => {
    const many = Array.from({ length: 7 }, (_, i) => ({
      id: i + 1, node_id: 'F0F5BDFFFE02', hardware_type: 'uart', hardware_id: 'UART' + i,
      status: i === 0 ? 'error' : 'ok', config: {},
    }))
    mockChannelList.mockResolvedValueOnce(many as any)
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const health = wrapper.find('.health-card')
    const chips = health.findAll('.chip').map(c => c.text())
    expect(chips[0]).toContain('7')
    expect(chips[1]).toContain('6')
    expect(chips[2]).toContain('1')
    // 列表只渲染前 6 条 ⇒ 第 7 条必须被显式说明，而不是静默消失
    expect(health.findAll('.chan-row')).toHaveLength(6)
    const rangeLine = health.find('[data-chan-range]')
    expect(rangeLine.exists(), '截断必须有一行常驻的范围说明').toBe(true)
    expect(rangeLine.text()).toContain('共 7 条')
    expect(rangeLine.text()).toContain('此处显示前 6 条')
    expect(rangeLine.text()).toContain('还有 1 条未显示')

    // 反例（分类器自检）：恰好 6 条时不得出现截断提示
    mockChannelList.mockResolvedValueOnce(many.slice(0, 6) as any)
    const wrapper2 = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const rangeLine2 = wrapper2.find('.health-card [data-chan-range]')
    expect(rangeLine2.exists(), '未截断时范围说明仍常驻（用户始终知道列表范围）').toBe(true)
    expect(rangeLine2.text()).toContain('共 6 条')
    expect(rangeLine2.text()).not.toContain('未显示')
    expect(wrapper2.findAll('.health-card .chan-row')).toHaveLength(6)
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
    expect(source).toMatch(/\.ph-actions\s*\{[^}]*display:\s*grid[^}]*grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)/)
    expect(source).toMatch(/\.ph-actions \.btn\s*\{[^}]*width:\s*100%[^}]*min-width:\s*0[^}]*justify-content:\s*center/)
    expect(source).toMatch(/\.tab-bar\s*\{[^}]*overflow-x:\s*auto[^}]*overscroll-behavior-x:\s*contain[^}]*scrollbar-width:\s*none/)
    expect(source).toContain('.tab-item { flex: 0 0 auto; }')
  })

  // ─── 未知值占位符统一为 —（§3.4.5 MUST） ───
  // 改前五格统计条与基本信息表的 6 处空值都渲染半角连字符 '-'，
  // 与全站其余页面的 '—' 不一致，也和「减号/分隔符」同形难以辨读。

  it('型号/固件版本/协议版本缺失时渲染 — 而不是半角 -', async () => {
    mockGetDetail.mockResolvedValueOnce({
      id: 3, node_id: 'NOSPEC01', name: '', status: 'offline',
      // 三个可选标识字段全部缺失 —— 后端确实可能不返回
      model: '', firmware_version: '', protocol_version: '',
      connection_quality: 0, latency_ms: 0, ping_latency_ms: 0,
      wifi_rssi: 0, free_heap_bytes: 0, uptime_seconds: 0, capabilities: {}, config: {},
    } as any)
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()

    // 五格统计条：型号 / 固件版本 / 协议版本 三格必须显示 —
    const strip = wrapper.find('.stat-strip')
    expect(strip.exists()).toBe(true)
    const values = strip.findAll('.stat-value').map(v => v.text().trim())
    expect(values).toContain('—')
    expect(values.filter(v => v === '—').length).toBeGreaterThanOrEqual(3)
    // 且绝不能出现半角连字符占位
    expect(values).not.toContain('-')

    // 基本信息表：节点名称/型号/固件版本 同样显示 —
    const infoVals = wrapper.findAll('.info-card .info-val').map(v => v.text().trim())
    expect(infoVals.filter(v => v === '—').length).toBeGreaterThanOrEqual(3)
    expect(infoVals).not.toContain('-')
  })

  it('源码不再用半角连字符作未知占位（§3.4.5）', () => {
    const css = stripCssComments(source)
    // || '-' 形态的模板占位必须全部消失
    expect(css).not.toMatch(/\|\|\s*'-'/)
    expect(css).not.toMatch(/\?\s*'-'\s*:/)
    expect(css).not.toMatch(/:\s*'-'/)
    // 统一走 format.ts 导出的常量，而不是各页各写一个字面量
    expect(css).toContain("import { UNKNOWN, formatTime } from '@/utils/format'")
  })

  // ─── 移动端层级线索（F10 / §4.1.2 MUST） ───
  // 改前移动端同时丢失两条线索：本页面包屑 display:none（旧断言见上一用例）
  // + MainLayout 顶栏面包屑 display:none，而「返回列表」只在错误分支渲染。
  // 这里的断言分两层：① DOM 里三条层级线索都真实存在；
  // ② 移动端断点里**不再**隐藏面包屑，而是压缩为两段并给出可滚动的溢出兜底。

  it('渲染面包屑层级线索：首页 / 节点管理 / 当前实体，且「节点管理」是真实返回入口', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()

    const crumb = wrapper.find('[data-testid="node-breadcrumb"]')
    expect(crumb.exists()).toBe(true)

    // 三段层级：首页 -> 节点管理 -> 当前实体（§4.1.2「至少表达列表 -> 当前实体」）。
    // 这里按**子元素顺序与文本**断言，不依赖 Element Plus 内部 class 名 ——
    // test-setup.ts 的通用 stub 渲染的是 elbreadcrumbitem（无连字符），
    // 用 .el-breadcrumb-item 选择器会得到 0 个匹配的假绿/假红。
    const items = Array.from(crumb.element.children) as HTMLElement[]
    expect(items.map(el => el.textContent?.trim())).toEqual(['首页', '节点管理', '机房采集器'])

    // 前两段带 to（可跳转），最后一段没有 —— 这正是「移动端裁掉根级后仍保留返回入口」的依据
    expect(items[0].getAttribute('to')).not.toBeNull()
    expect(items[1].getAttribute('to'), '「节点管理」必须是真实可跳转的上一级入口').not.toBeNull()
    expect(items[2].getAttribute('to'), '当前实体不应是可跳转链接').toBeNull()
    expect(wrapper.find('[data-testid="node-breadcrumb-current"]').text()).toBe('机房采集器')
  })

  it('移动端不再隐藏面包屑，而是压缩为「上一级 / 当前」两段并做容器内滚动', () => {
    // 断言必须只看真实 CSS：源码注释里为了说明「改前行为」写着
    // "改前 .no-breadcrumb{display:none}"，若带注释一起 match，
    // not.toMatch 会被自己的说明文字误伤（本用例第一版就踩了这个坑）。
    const css = stripCssComments(source)

    // ① 旧行为必须消失：整条 display:none 会让移动端只剩标题
    expect(css).not.toMatch(/\.no-breadcrumb\s*\{[^}]*display:\s*none/)

    // ② 新行为：移动端断点内确实重定义了面包屑布局
    const mobileBlock = css.slice(css.indexOf('@media (max-width: 768px)'))
    const crumbRule = mobileBlock.slice(mobileBlock.indexOf('.no-breadcrumb {'), mobileBlock.indexOf('.no-breadcrumb::-webkit-scrollbar'))
    expect(crumbRule).toMatch(/display:\s*flex/)
    // 用 flex 而非 EP 默认的 float:left —— float 不参与父容器 overflow 计算
    expect(crumbRule).toMatch(/overflow-x:\s*auto/)
    expect(crumbRule).toMatch(/flex-wrap:\s*nowrap/)
    // 只保留两段：根级「首页」在窄屏被裁掉
    expect(mobileBlock).toMatch(/\.no-breadcrumb\s*:deep\(\.el-breadcrumb__item:first-child\)\s*\{\s*display:\s*none/)
    // 当前实体允许收缩 + 省略号，避免把胶囊撑成逐字竖排（§4.2.5）
    expect(mobileBlock).toMatch(/\.no-breadcrumb\s*:deep\(\.el-breadcrumb__item:last-child\)\s*\{[^}]*flex:\s*0 1 auto/)
    expect(mobileBlock).toMatch(/word-break:\s*keep-all/)
  })

  it('F10：移动端唯一的上层级入口（「节点管理」链接）在 ≤768px 有 ≥44px 触控热区', () => {
    const css = stripCssComments(source)
    const mobileBlock = css.slice(css.indexOf('@media (max-width: 768px)'))
    // 改前这里写 min-height:20px，注释却声称 44px —— 390px 真浏览器实测
    // 该链接布局盒只有 48x20（elementFromPoint 命中，但热区不足 §4.4.5 MUST）。
    // 它是移动端**唯一**的「退回上一层」入口（顶栏面包屑 display:none、
    // 「返回列表」按钮不在渲染路径上），所以热区必须真的补足。
    const linkRule = mobileBlock.match(/\.no-breadcrumb\s*:deep\(\.el-breadcrumb__inner\.is-link\)\s*\{[^}]*\}/)
    expect(linkRule, '缺少移动端面包屑链接的触控规则').not.toBeNull()
    expect(linkRule![0]).toMatch(/min-height:\s*44px/)
    // 反例守卫：不得回退到 <44px
    expect(linkRule![0]).not.toMatch(/min-height:\s*(1?[0-9]|2[0-9]|3[0-9]|4[0-3])px/)
  })

  it('移动端面包屑容器横向滚动有上限：min-width:0 + 滚动条隐藏', () => {
    const css = stripCssComments(source)
    const mobileBlock = css.slice(css.indexOf('@media (max-width: 768px)'))
    const crumbRule = mobileBlock.slice(mobileBlock.indexOf('.no-breadcrumb {'), mobileBlock.indexOf('.no-breadcrumb::-webkit-scrollbar'))
    // min-width:0 是 flex 子项能被压缩、进而触发 overflow-x 的前提
    expect(crumbRule).toMatch(/min-width:\s*0/)
    expect(mobileBlock).toContain('.no-breadcrumb::-webkit-scrollbar { display: none; }')
    // 每段固定不收缩，收缩只发生在最后一段（保证「上一级」永远可读可点）
    expect(mobileBlock).toMatch(/\.no-breadcrumb\s*:deep\(\.el-breadcrumb__item\)\s*\{\s*flex:\s*0 0 auto/)
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

  // ── 新 TAB：DMA 通道 / 关联设备 / OTA 历史 / 系统日志 / 通道终端 ──

  it('DMA 通道 TAB：进入时拉取 store 数据并渲染卡片（名称/状态/绑定），空数据时空态', async () => {
    // 先验证空数组 → 空态 + 触发 fetch
    mockDmaChannelsRef.value = []
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('DMA 通道'))
    await tab?.trigger('click')
    await flushPromises()
    expect(mockDmaFetch).toHaveBeenCalledWith('F0F5BDFFFE02')
    // D5 修复后空态**必须区分两种成因**（改前统一说"暂无 DMA 通道"，用户会把
    // "设备没上报这个能力"读成"这台设备确实没有 DMA"）：
    //   · 能力上报里没有 dma 项 ⇒ 未上报（本用例的 fixture 正是这种：buses 无 dma）
    //   · 上报了但一条不可用 ⇒ 已上报 N 条
    const emptyText = wrapper.find('.dma-card .card-empty').text()
    expect(emptyText).toContain('未上报 DMA 资源')
    expect(emptyText).not.toContain('暂无 DMA 通道')

    // 反例（分类器自检）：能力上报里**有** dma 项 ⇒ 不得再说"未上报"
    mockGetCapabilities.mockResolvedValueOnce({
      buses: { i2c: [], uart: [], spi: [], adc: [], gpio: [], pwm: [], dma: [{ id: 'GDMA_CH0', channel: 0 }] },
    } as any)
    const wrapperReported = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tabReported = wrapperReported.findAll('.tab-item').find(item => item.text().includes('DMA 通道'))
    await tabReported?.trigger('click')
    await flushPromises()
    const reportedText = wrapperReported.find('.dma-card .card-empty').text()
    expect(reportedText).not.toContain('未上报 DMA 资源')
    expect(reportedText).toContain('1 条 DMA 资源')

    // 再验证有数据 → 渲染卡片（重新 mount）
    mockDmaChannelsRef.value = [
      { dma_id: 0, name: 'DMA0', dma_type: 0, capabilities: 3, max_burst: 128, state: 0, bound_to: '', compatible_bus: 3 },
      { dma_id: 1, name: 'DMA1', dma_type: 1, capabilities: 1, max_burst: 64, state: 1, bound_to: 'i2c/i2c0', compatible_bus: 2 },
    ] as any[]
    const wrapper2 = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab2 = wrapper2.findAll('.tab-item').find(item => item.text().includes('DMA 通道'))
    await tab2?.trigger('click')
    await flushPromises()
    const card = wrapper2.find('.dma-card')
    const items = card.findAll('.dma-item')
    expect(items).toHaveLength(2)
    expect(items[0].text()).toContain('GDMA')
    expect(items[0].text()).toContain('TX, RX')
    expect(items[0].text()).toContain('UART, I2C') // 兼容总线 3 = UART(1) + I2C(2)
    expect(items[0].text()).toContain('128')
    expect(items[0].text()).toContain('未绑定')
    expect(items[1].text()).toContain('i2c/i2c0')
  })

  it('关联设备 TAB：渲染设备名称/类型/地址，点查看跳转 edge-device', async () => {
    const serial = 'F0F5BDFFFE02'
    const devices = [
      { id: 11, name: '温湿度传感器', device_type: 'sensor.temp_humidity', hardware_id: 'I2C0-01', channel_id: 1, status: 'online', last_data: { temperature: 25.6, humidity: 60.2 }, last_data_time: new Date().toISOString(), config: {}, node_id: serial },
      { id: 12, name: '电表', device_type: 'meter', hardware_id: 'UART1-02', channel_id: 2, status: 'offline', last_data: null, last_data_time: null, config: {}, node_id: serial },
    ] as any[]
    mockGetCachedList.mockReturnValue({ items: devices, total: 2 } as any)
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('关联设备'))
    await tab?.trigger('click')
    await flushPromises()
    const card = wrapper.find('.device-card')
    expect(mockFetchDevices).toHaveBeenCalledWith({ node_id: serial, page: 1, page_size: 100 }, true)
    expect(card.text()).toContain('温湿度传感器')
    const row = card.findAll('.device-row')[0]
    expect(row.text()).toContain('I2C0-01')
    expect(row.text()).toContain('I2C I2C0') // 通道列：hardware_type + hardware_id
    // 最新一条数据列（last_data → formatLastData；>=10 的数 toFixed(0)）
    expect(row.text()).toContain('温度')
    expect(row.text()).toContain('26')
    // 状态徽标（StatusBadge stub）
    expect(row.find('.status-badge-stub').text()).toBe('online')
    // 点「查看」→ push
    await row.findAll('button').find(b => b.text().includes('查看'))?.trigger('click')
    expect(mockRouterPush).toHaveBeenCalledWith('/edge-device/11')
  })

  it('关联设备 TAB：列表/卡片切换，卡片视图渲染读数与离线态，点卡片跳转', async () => {
    const serial = 'F0F5BDFFFE02'
    const devices = [
      { id: 11, name: '温湿度传感器', device_type: 'sensor.temp_humidity', hardware_id: 'I2C0-01', channel_id: 1, status: 'online', last_data: { temperature: 25.6 }, last_data_time: new Date().toISOString(), config: {}, node_id: serial },
      { id: 12, name: '电表', device_type: 'meter', hardware_id: 'UART1-02', channel_id: 2, status: 'offline', last_data: null, last_data_time: null, config: {}, node_id: serial },
    ] as any[]
    mockGetCachedList.mockReturnValue({ items: devices, total: 2 } as any)
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('关联设备'))
    await tab?.trigger('click')
    await flushPromises()
    // 默认列表视图
    expect(wrapper.find('.chan-list').exists()).toBe(true)
    expect(wrapper.find('.device-grid').exists()).toBe(false)
    // 切到卡片视图
    const switchBtns = wrapper.findAll('.view-switch-btn')
    await switchBtns.find(b => b.text() === '卡片')?.trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.device-grid').exists()).toBe(true)
    expect(wrapper.find('.chan-list').exists()).toBe(false)
    const tiles = wrapper.findAll('.device-tile')
    expect(tiles).toHaveLength(2)
    // 在线设备卡片：读数区有数据
    expect(tiles[0].text()).toContain('温湿度传感器')
    expect(tiles[0].text()).toContain('最新读数')
    expect(tiles[0].text()).toContain('温度')
    // 离线设备卡片：is-offline 类 + 无数据占位
    expect(tiles[1].classes()).toContain('is-offline')
    expect(tiles[1].text()).toContain('等待首条采集数据')
    // 点卡片跳转
    await tiles[0].trigger('click')
    expect(mockRouterPush).toHaveBeenCalledWith('/edge-device/11')
    // 切回列表
    await switchBtns.find(b => b.text() === '列表')?.trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.chan-list').exists()).toBe(true)
  })

  it('关联设备 TAB：node 无 node_id 时创建设备按钮禁用', async () => {
    mockGetDetail.mockResolvedValueOnce({
      id: 9, node_id: '', name: '无序列号节点', model: 'esp32s3', status: 'online', firmware_version: '1.0.0',
      connection_quality: 0, latency_ms: 0, ping_latency_ms: 0, wifi_rssi: 0, free_heap_bytes: 0,
      uptime_seconds: 0, capabilities: {}, config: {},
    } as any)
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('关联设备'))
    await tab?.trigger('click')
    await flushPromises()
    const createBtn = wrapper.find('.device-card .btn-primary')
    expect(createBtn.attributes('disabled')).toBeDefined()
  })

  it('OTA 历史 TAB：渲染版本/状态中文，pending 行有取消，success 行没有，点取消走 confirm+cancelOTA', async () => {
    mockGetOTAHistory.mockResolvedValueOnce([
      { id: 1, node_id: 1, firmware_id: 1, from_version: '2.5.18', to_version: '2.6.0', status: 'success', progress: 100, created_at: new Date().toISOString(), completed_at: new Date().toISOString() },
      { id: 2, node_id: 1, firmware_id: 2, from_version: '2.5.0', to_version: '2.5.18', status: 'pending', progress: 0, created_at: new Date().toISOString() },
      { id: 3, node_id: 1, firmware_id: 3, from_version: '2.4.0', to_version: '2.5.0', status: 'downloading', progress: 45, created_at: new Date().toISOString() },
    ] as any[])
    // 取消 OTA 后 fetchOTAHistory 会再次调用 —— 让它返回空避免额外干扰
    mockGetOTAHistory.mockResolvedValueOnce([])
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('OTA 历史'))
    await tab?.trigger('click')
    await flushPromises()
    const card = wrapper.find('.ota-card')
    expect(mockGetOTAHistory).toHaveBeenCalledWith('1')
    expect(card.text()).toContain('2.5.18 → 2.6.0')
    expect(card.text()).toContain('成功')
    expect(card.text()).toContain('等待中')
    expect(card.text()).toContain('下载中')
    expect(card.text()).toContain('45%')
    // pending 行有取消按钮
    const rows = card.findAll('.bus-table tbody tr')
    const pendingRow = rows.find(r => r.text().includes('等待中'))
    expect(pendingRow?.find('button').text()).toContain('取消')
    const successRow = rows.find(r => r.text().includes('成功'))
    expect(successRow?.find('button').exists()).toBe(false)
    // 点取消 → confirm → cancelOTA
    mockElMessageBoxConfirm.mockResolvedValueOnce('confirm')
    await pendingRow?.find('button').trigger('click')
    await flushPromises()
    expect(mockElMessageBoxConfirm).toHaveBeenCalled()
    expect(mockCancelOTA).toHaveBeenCalledWith('1', 2)
  })

  it('OTA 历史 TAB：离线时取消按钮禁用', async () => {
    mockGetDetail.mockResolvedValueOnce({
      id: 2, node_id: 'OFFLINE01', name: '离线节点', model: 'esp32c6', status: 'offline',
      firmware_version: '2.5.0', connection_quality: 0, latency_ms: 0, ping_latency_ms: 0,
      wifi_rssi: 0, free_heap_bytes: 0, uptime_seconds: 0, capabilities: {}, config: {},
    } as any)
    mockGetOTAHistory.mockResolvedValueOnce([
      { id: 1, node_id: 2, firmware_id: 1, from_version: '2.5.0', to_version: '2.6.0', status: 'pending', progress: 10, created_at: new Date().toISOString() },
    ] as any[])
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('OTA 历史'))
    await tab?.trigger('click')
    await flushPromises()
    const cancelBtn = wrapper.find('.ota-card tbody tr button')
    expect(cancelBtn.text()).toContain('取消')
    expect(cancelBtn.attributes('disabled')).toBeDefined()
  })

  it('OTA 历史 TAB：timeout/needs_retry/verifying 三态渲染中文文案与红/橙/蓝标签', async () => {
    // 三态此前完全未映射：文案走 `texts[status] || status` ⇒ 中文界面原样显示英文，
    // 颜色回退 'bus-tag-gray' ⇒「超时」「需要重试」与「等待中」视觉无异。
    // 必须先清掉上一次用例可能残留的 once 队列（mockClear 不清队列），否则会取到别人的 fixture
    mockGetOTAHistory.mockReset()
    // 本用例只会触发 1 次 fetchOTAHistory（点 TAB）；多排的值会泄漏给后续用例
    mockGetOTAHistory.mockResolvedValueOnce([
      { id: 11, node_id: 1, firmware_id: 1, from_version: '2.6.0', to_version: '2.6.1', status: 'timeout', progress: 60, created_at: new Date().toISOString() },
      { id: 12, node_id: 1, firmware_id: 2, from_version: '2.6.0', to_version: '2.6.2', status: 'needs_retry', progress: 0, created_at: new Date().toISOString() },
      { id: 13, node_id: 1, firmware_id: 3, from_version: '2.6.1', to_version: '2.6.2', status: 'verifying', progress: 100, created_at: new Date().toISOString() },
    ] as any[])
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('OTA 历史'))
    await tab?.trigger('click')
    await flushPromises()
    const card = wrapper.find('.ota-card')
    expect(card.text()).toContain('超时')
    expect(card.text()).toContain('需要重试')
    expect(card.text()).toContain('校验中')
    // 漏映射时状态名会原样露出英文
    expect(card.text()).not.toContain('needs_retry')
    expect(card.text()).not.toContain('verifying')

    const rows = card.findAll('.bus-table tbody tr')
    expect(rows).toHaveLength(3)
    const tagOf = (text: string) => rows.find(r => r.text().includes(text))!.find('.bus-tag')
    // 问题态绝不能落中性灰
    expect(tagOf('超时').classes()).toContain('bus-tag-red')
    expect(tagOf('超时').classes()).not.toContain('bus-tag-gray')
    expect(tagOf('需要重试').classes()).toContain('bus-tag-orange')
    expect(tagOf('需要重试').classes()).not.toContain('bus-tag-gray')
    expect(tagOf('校验中').classes()).toContain('bus-tag-blue')
    // 取消只允许 pending/downloading：这三态都不该有取消按钮
    expect(card.find('.bus-table tbody button').exists()).toBe(false)
  })

  it('OTA 历史 TAB：from_version 缺失时版本列只渲染 to_version（后端 models.OTATask 无该字段）', async () => {
    mockGetOTAHistory.mockReset()
    mockGetOTAHistory.mockResolvedValueOnce([
      { id: 21, node_id: 1, firmware_id: 1, from_version: '2.5.18', to_version: '2.6.0', status: 'success', progress: 100, created_at: new Date().toISOString(), completed_at: new Date().toISOString() },
      // 后端真实响应形态：api/node.ts 从不返回 from_version
      { id: 22, node_id: 1, firmware_id: 2, to_version: '2.6.3', status: 'failed', progress: 80, created_at: new Date().toISOString(), completed_at: new Date().toISOString() },
    ] as any[])
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('OTA 历史'))
    await tab?.trigger('click')
    await flushPromises()
    const rows = wrapper.findAll('.ota-card .bus-table tbody tr')
    expect(rows).toHaveLength(2)
    const cells = rows.map(r => r.findAll('td')[0])
    // 旧数据/兼容形态仍能正确渲染（既有断言 NodeOverview.spec.ts:544 也压着这条）
    expect(cells[0].text()).toBe('2.5.18 → 2.6.0')
    // 修复前是 `{{ from_version }} → {{ to_version }}` ⇒ 这里渲染成 " → 2.6.3"（前半段空白）
    expect(cells[1].text()).toBe('2.6.3')
  })

  it('系统日志 TAB：渲染 LogPanel 并传递 collector-id / node-device-id', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('系统日志'))
    await tab?.trigger('click')
    await flushPromises()
    const panel = wrapper.find('.log-panel-stub')
    expect(panel.exists()).toBe(true)
    expect(panel.text()).toContain('collector=F0F5BDFFFE02')
    expect(panel.text()).toContain('device=F0F5BDFFFE02')
  })

  it('通道终端 TAB：渲染 ChannelTerminal 并传递 channels 全量', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('通道终端'))
    await tab?.trigger('click')
    await flushPromises()
    const terminal = wrapper.find('.channel-terminal-stub')
    expect(terminal.exists()).toBe(true)
    expect(terminal.text()).toContain('collector=F0F5BDFFFE02')
    expect(terminal.text()).toContain('device=F0F5BDFFFE02')
    expect(terminal.text()).toContain('channels=2')
  })

  it('关联设备 TAB：创建设备弹窗打开后 created 触发后刷新列表', async () => {
    const wrapper = mount(NodeOverview, { global: { stubs } })
    await flushPromises()
    const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('关联设备'))
    await tab?.trigger('click')
    await flushPromises()
    const createBtn = wrapper.find('.device-card .btn-primary')
    await createBtn.trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.quick-create-stub').text()).toContain('open')
    // emit created → invalidateLists + 重新 fetch
    const dialog = wrapper.findComponent({ name: 'QuickCreateDeviceDialog' })
    dialog.vm.$emit('created')
    await flushPromises()
    expect(mockInvalidateLists).toHaveBeenCalled()
    expect(mockFetchDevices).toHaveBeenCalled()
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

    /**
     * F32：语义色不得用内联字面量 hex —— 那些值不随主题变化。
     *
     * 实测缺陷（修复前，真实 Chromium 1440x900 节点详情页）：以下元素在亮/暗两主题下
     * 计算色**完全相同**，而它们与 theme.css 的语义 token 是两套调色板：
     *   chip-dot   rgb(138,147,163) / rgb(34,197,94) / rgb(245,158,11)
     *   metric-icon rgb(22,163,74) / rgb(139,92,246) / rgb(46,107,255)
     * 本层只守护「源码不再写这些字面量」；「两主题计算色确实不同」由浏览器探针覆盖
     * （.tmp-probe/lead-nodehex.mjs），因为 happy-dom 不做 var() 解析。
     */
    it('F32：语义色走页面 token，不再用不随主题变化的内联 hex', () => {
      // 反例守卫：**使用点**不得出现内联字面量 hex（token 定义行与注释里的不算）。
      // 判据：只看 `style="..."` 里的字面量与 qualityColor 的 return 字面量 ——
      // 这两类才是「不随主题变化」的来源；`--no-*: #xxxxxx` 是 token 定义，本就该有字面量。
      const styleAttrs = [...source.matchAll(/style="[^"]*"/g)].map((m) => m[0])
      expect(styleAttrs.length, '分母为 0：没抓到任何内联 style，断言会假绿').toBeGreaterThan(0)
      for (const m of styleAttrs) {
        const lit = m.match(/#[0-9a-fA-F]{3,8}/g)
        expect(lit, '内联 style 里出现字面量 hex（不随主题变化）：' + m).toBeNull()
      }
      // qualityColor 的 return 必须是 token 引用而非字面量
      const returns = [...source.matchAll(/return '(var\(--no-[a-z-]+\)|#[0-9a-fA-F]{3,8})'/g)].map((m) => m[1])
      expect(returns.length, '分母为 0：没抓到 qualityColor 的 return，断言会假绿').toBeGreaterThan(0)
      for (const r of returns) {
        expect(r.startsWith('var(--no-'), 'qualityColor 返回了字面量 hex：' + r).toBe(true)
      }
      // 正向守卫：必须真的用了 token（否则上面的 not.toContain 在「整个删掉样式」时也会通过）
      expect(source).toContain('var(--no-success-text)')
      expect(source).toContain('var(--no-warning-text)')
      expect(source).toContain('var(--no-success)')
      expect(source).toContain('var(--no-warning)')
      expect(source).toContain('var(--no-text-muted)')
      expect(source).toContain('var(--no-text-faint)')
      expect(source).toContain('var(--no-accent)')
      // 新增的页面级 token 必须亮/暗都有定义（否则等于没修）
      expect(source).toContain('--no-accent: #8B5CF6')
      expect(source).toContain('--no-accent: #A78BFA')
      // qualityColor 的 JS 返回值也走 token（此前返回字面量 hex）
      expect(source).toContain("if (q >= 80) return 'var(--no-success-text)'")
      expect(source).toContain("return 'var(--no-danger)'")
    })

    it('字体层级使用稳定的页面 token，而不是让标题、字段和弱提示退化为同一层级', () => {
      expect(source).toContain('--no-text-secondary: #526072')
      expect(source).toContain('--no-text-muted: #69778B')
      expect(source).toContain('--no-text-faint: #A7B1BF')
      expect(source).toContain('--no-text-secondary: var(--text-color-regular, #C0C6D0)')
      expect(source).toContain('--no-text-muted: var(--text-color-secondary, #8A93A3)')

      expect(source).toContain('.card-title { color: var(--no-text); font-size: 16px; font-weight: 600; line-height: 24px; }')
      expect(source).toContain('.stat-label { font-size: 12px; line-height: 18px; color: var(--no-text-secondary); }')
      expect(source).toContain('.stat-value { font-size: 14px; font-weight: 500; line-height: 20px;')
      expect(source).toContain('.btn {\n  height: 36px; padding: 0 16px; border-radius: 6px; font-size: 13px; font-weight: 500; line-height: 20px;')
    })

    it('总线工作台文本层级与响应式关键规则由 scoped CSS 明确约束', () => {
      expect(source).toContain('.bus-subtab.active { border-bottom-color: var(--no-primary); font-weight: 600; }')
      expect(source).toContain('.bus-desc { margin: 12px 0 16px; color: var(--no-text-secondary); font-size: 12px; line-height: 18px; }')
      expect(source).toContain('.bus-stat-label { color: var(--no-text-secondary); font-size: 12px; line-height: 18px; white-space: nowrap; }')
      expect(source).toContain('.bus-stat-value { color: var(--no-text); font-size: 16px; font-weight: 600; line-height: 22px; }')
      expect(source).toContain('.bus-table th { height: 42px; padding: 0 6px; color: var(--no-text-secondary); text-align: left; font-size: 12px; font-weight: 500; line-height: 18px;')
      expect(source).toContain('.bus-table td { height: 44px; padding: 0 6px; color: var(--no-text); font-size: 13px; font-weight: 400; line-height: 20px;')
      expect(source).toContain('.bus-tool-group-title { color: var(--no-text); font-size: 16px; font-weight: 600; line-height: 24px; }')
      expect(source).toContain('.bus-create-field span { color: var(--no-text-secondary); font-size: 12px; line-height: 18px; }')
      expect(source).toContain('.bus-create-field b { color: var(--no-text); font-size: 13px; font-weight: 500; line-height: 20px; }')
      expect(source).toContain('.tab-bar { overflow-x: auto; overscroll-behavior-x: contain; scrollbar-width: none;')
      expect(source).toContain('.bus-table { min-width: 680px; table-layout: auto; }')
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

  // ── 外设直控接线（FR-I1/I2/I3 的生产入口；改前只在不可达的 NodeDetail→ChannelPanel 链里）──
  //
  // 这些用例证明的是「能力真的接到了可达入口」与「写后回填真的接通」，
  // 而不只是源码里出现了某个字符串——后者在本仓是出了名的假绿来源。
  describe('外设控制 TAB（GPIO/PWM 直控接线）', () => {
    const periphStub = {
      name: 'PeripheralControl',
      template: '<div class="peripheral-control-stub" />',
      props: ['nodeId', 'offline', 'registerPendingGpio', 'registerPendingPwm'],
      methods: {
        applyRuntimeLevel: (...args: any[]) => mockApplyRuntimeLevel(...args),
        applyRuntimeState: (...args: any[]) => mockApplyRuntimeState(...args),
        reload: (...args: any[]) => mockPeriphReload(...args),
      },
    }
    const periphStubs = { ...stubs, PeripheralControl: periphStub }

    it('TAB 栏含「外设控制」，点击后渲染 PeripheralControl 并传入节点身份', async () => {
      const wrapper = mount(NodeOverview, { global: { stubs: periphStubs } })
      await flushPromises()
      const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('外设控制'))
      expect(tab, 'TAB 栏必须有「外设控制」——否则 GPIO/PWM 仍无生产入口').toBeTruthy()
      await tab!.trigger('click')
      await flushPromises()
      const periph = wrapper.find('.peripheral-control-stub')
      expect(periph.exists()).toBe(true)
      // nodeId 用物理序列号（与后端 /nodes/:id/gpio 的 :id 口径一致）
      expect(wrapper.findComponent({ name: 'PeripheralControl' }).props('nodeId')).toBe('F0F5BDFFFE02')
    })

    it('registerPending 两个回调都已传入（缺一个即退回「写完不刷新」的降级态）', async () => {
      const wrapper = mount(NodeOverview, { global: { stubs: periphStubs } })
      await flushPromises()
      const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('外设控制'))
      await tab!.trigger('click')
      await flushPromises()
      const periph = wrapper.findComponent({ name: 'PeripheralControl' })
      expect(typeof periph.props('registerPendingGpio')).toBe('function')
      expect(typeof periph.props('registerPendingPwm')).toBe('function')
    })

    it('注册后收到 periph_result 会按 request_id 回填运行态（GPIO 读回）', async () => {
      const wrapper = mount(NodeOverview, { global: { stubs: periphStubs } })
      await flushPromises()
      const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('外设控制'))
      await tab!.trigger('click')
      await flushPromises()
      const periph = wrapper.findComponent({ name: 'PeripheralControl' })

      // 模拟行控件写入前登记：requestId=101, pin=7, action=2（读电平）
      const accepted = periph.props('registerPendingGpio')!({ requestId: 101, pin: 7, action: 2 })
      expect(accepted, '返回 true 才表示「已登记、由 WS 回填」').toBe(true)

      // 派发对应 WS 事件（periph_result）：成功、value=1
      emitPeriphResult({ node_id: 'F0F5BDFFFE02', request_id: 101, success: true, periph_type: 1, pin: 7, value: 1, action: 2 })
      await flushPromises()
      expect(mockApplyRuntimeLevel).toHaveBeenCalledWith(7, 1)
    })

    it('未登记的 request_id 不得回填（防串台）', async () => {
      mount(NodeOverview, { global: { stubs: periphStubs } })
      await flushPromises()
      emitPeriphResult({ node_id: 'F0F5BDFFFE02', request_id: 999, success: true, periph_type: 1, pin: 7, value: 1, action: 2 })
      await flushPromises()
      expect(mockApplyRuntimeLevel).not.toHaveBeenCalled()
    })

    it('action/资源身份不匹配时不得回填（同一 request_id 也不能错配）', async () => {
      const wrapper = mount(NodeOverview, { global: { stubs: periphStubs } })
      await flushPromises()
      const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('外设控制'))
      await tab!.trigger('click')
      await flushPromises()
      const periph = wrapper.findComponent({ name: 'PeripheralControl' })
      periph.props('registerPendingGpio')!({ requestId: 202, pin: 7, action: 2 })

      // action 不一致（登记的是读=2，回的是写=0）⇒ 必须拒绝
      emitPeriphResult({ node_id: 'F0F5BDFFFE02', request_id: 202, success: true, periph_type: 1, pin: 7, value: 1, action: 0 })
      await flushPromises()
      expect(mockApplyRuntimeLevel).not.toHaveBeenCalled()

      // 资源身份不一致（同 request_id 但 pin 不同）⇒ 必须拒绝
      emitPeriphResult({ node_id: 'F0F5BDFFFE02', request_id: 202, success: true, periph_type: 1, pin: 8, value: 1, action: 2 })
      await flushPromises()
      expect(mockApplyRuntimeLevel).not.toHaveBeenCalled()
    })

    it('节点切换后旧 request_id 的 ACK 不得回填（跨节点串台防护）', async () => {
      const wrapper = mount(NodeOverview, { global: { stubs: periphStubs } })
      await flushPromises()
      const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('外设控制'))
      await tab!.trigger('click')
      await flushPromises()
      const periph = wrapper.findComponent({ name: 'PeripheralControl' })
      // 在旧节点上登记一个在途请求
      periph.props('registerPendingGpio')!({ requestId: 404, pin: 7, action: 2 })

      // 切到另一个节点：组件 watch(route.params.id) 会自增代际并清空 pending
      mockRouterState.reactiveParams!.id = '2'
      await flushPromises()

      // 旧节点的 ACK 此时才到达 —— 必须被拒（否则会写进新节点的 UI）
      emitPeriphResult({ node_id: 'F0F5BDFFFE02', request_id: 404, success: true, periph_type: 1, pin: 7, value: 1, action: 2 })
      await flushPromises()
      expect(mockApplyRuntimeLevel).not.toHaveBeenCalled()
    })

    it('PWM 回填走 applyRuntimeState，失败时 running=null 表示状态未知', async () => {
      const wrapper = mount(NodeOverview, { global: { stubs: periphStubs } })
      await flushPromises()
      const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('外设控制'))
      await tab!.trigger('click')
      await flushPromises()
      const periph = wrapper.findComponent({ name: 'PeripheralControl' })
      periph.props('registerPendingPwm')!({ requestId: 303, hardwareId: 'PWM0', action: 1 })

      emitPeriphResult({ node_id: 'F0F5BDFFFE02', request_id: 303, success: false, periph_type: 2, hardware_id: 'PWM0', action: 1 })
      await flushPromises()
      expect(mockApplyRuntimeState).toHaveBeenCalledWith('PWM0', null, undefined)
    })

    it('「配置/编辑」必须真的打开配置表单，且**不得**指向做不到的页面', async () => {
      // ElMessage 未整体 mock（会破坏 feedback 的其它出口），故对真实模块装 spy。
      const { ElMessage } = await import('element-plus')
      const warningSpy = vi.spyOn(ElMessage, 'warning')

      const wrapper = mount(NodeOverview, { global: { stubs: periphStubs } })
      await flushPromises()
      const tab = wrapper.findAll('.tab-item').find(item => item.text().includes('外设控制'))
      await tab!.trigger('click')
      await flushPromises()

      // 历史三段（都写在断言里，防止退回）：
      //   ① 最初：点了没反应（无任何反馈）；
      //   ② E1 第一版：切到「总线配置」TAB —— 但该 TAB 对 gpio/pwm 的
      //      busSupportsChannels=false（:1030 只含 uart/i2c/spi/adc），按钮显示
      //      「此资源不支持通道」且 disabled ⇒ 跳过去**仍然无处可配**；
      //   ③ E1 第二版：如实弹「尚未接线」提示（不假装有路，但能力仍是断的）；
      //   ④ **现在**：配置表单已由 PeripheralControl 自带（共享组件
      //      PeripheralConfigDialog），点击即真的能配置。
      //
      // 因此本用例的契约升级为：**不得**再弹"尚未接线"这类提示
      //（否则用户会先看到对话框、再看到一句说它不存在的提示，自相矛盾）。
      const warningCalls = warningSpy.mock.calls.map(call => String(call[0]))
      expect(
        warningCalls.some(msg => msg.includes('尚未接线')),
        '配置入口已接线，不得再宣称"尚未接线"',
      ).toBe(false)

      // 关键：仍停在「外设控制」TAB，不能跳到做不到的页面
      expect(wrapper.find('.peripheral-control-stub').exists()).toBe(true)
      warningSpy.mockRestore()
    })

    it('源码层面：外设控制 TAB 存在且回填链路完整', () => {
      expect(source).toContain("{ label: '外设控制'")
      expect(source).toContain('PeripheralControl')
      expect(source).toContain('WS_EVENT.PERIPH_RESULT')
      expect(source).toContain('registerPendingPeripheral')
      expect(source).toContain('registerPendingPWM')
      expect(source).toContain('periphGeneration')
    })
  })

  // ── 列表页快捷入口的 ?tab= 深链（改前 NodeList 推 ?tab=config 但本页无消费者）──
  describe('?tab= 深链消费', () => {
    it('?tab=config 落在「总线配置」TAB', async () => {
      mockRouteQuery.value = { tab: 'config' }
      const wrapper = mount(NodeOverview, { global: { stubs } })
      await flushPromises()
      expect(wrapper.find('.tab-item.active').text()).toContain('总线配置')
    })

    it('未知 tab 值不得报错也不得改变默认 TAB', async () => {
      mockRouteQuery.value = { tab: 'not-a-real-tab' }
      const wrapper = mount(NodeOverview, { global: { stubs } })
      await flushPromises()
      expect(wrapper.find('.tab-item.active').text()).toContain('基本信息')
    })
  })

  // ── 从 NodeDetail.vue 迁移过来的断言（该文件已删除，见 C5 死代码清理）──────
  //
  // 迁移原则：只搬"守卫的是**用户可见行为**"的断言，不搬"锁死死文件实现细节"的。
  // 死文件的模板结构断言（:column="descColumns"、mobile-table-wrapper 数量等）
  // **不迁移** —— 它们本就是"源码字符串锁实现"，NodeOverview 用 CSS 断点 +
  // .bus-table-wrap 实现了等价的移动端横滑，锁 class 名只会阻碍将来的正常重构。
  describe('生存页面的展示映射守卫（迁移自 NodeDetail.spec）', () => {
    it('离线节点不得显示"同步中"：离线覆盖优先于后端快照', () => {
      // 这条是**真实缺陷守卫**：后端快照可能仍是 syncing，而设备已离线，
      // 直接透传会让用户看到"离线设备正在同步"的矛盾状态。
      expect(source).toMatch(/if \(nodeOffline\.value\) return '离线'/)
      expect(source).toContain('configSyncStateLabel')
      // 反证：不得把后端状态直接透传（那就丢了离线覆盖）
      expect(source).not.toMatch(/syncStateLabel = computed\(\(\) => configSyncStateLabel\(/)
    })

    it('关联设备读的是所请求的缓存（不是全量/上一页残留）', () => {
      expect(source).toContain('edgeDeviceStore.getCachedList(')
    })
  })
})
