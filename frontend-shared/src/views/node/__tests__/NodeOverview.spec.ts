import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NodeOverview from '../NodeOverview.vue'
import source from '../NodeOverview.vue?raw'

// ── hoisted mocks（形状必须与后端真实响应对齐） ──
const { mockGetDetail, mockChannelList, mockGetCapabilities, mockClientGet, mockSubscribe, mockDmaFetch, mockRouterPush, mockFetchDevices, mockGetCachedList, mockInvalidateLists, mockGetOTAHistory, mockCancelOTA, mockElMessageBoxConfirm, mockDmaChannelsRef } = vi.hoisted(() => ({
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
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ back: vi.fn(), push: mockRouterPush }),
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

const stubs = {
  OTAForm: true,
  ChannelManager: true,
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
    expect(wrapper.find('.dma-card').text()).toContain('该节点暂无 DMA 通道')

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
})
