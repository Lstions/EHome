import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NodeOverview from '../NodeOverview.vue'
import source from '../NodeOverview.vue?raw'

/**
 * 总线配置页门禁（A: 已创建通道列表 / B: DMA 资源可选 / B2: 掩码口径真源）
 *
 * 背景（2026-09-20，实机复现）：用户在「节点详情 → 总线配置」里
 *   A) 看不到自己创建的通道 —— 页面上唯一痕迹是资源表「已挂载通道」列里的**数字**
 *      （resourceMountedChannels(resource).length），而 GET /api/v1/channels 是正常返回的。
 *   B) DMA「只有一个开关，不能选择用哪个 DMA 资源」——
 *      资源行只渲染一个 el-switch，隐式作用于第一条候选。
 *
 * 为什么必须有这个文件：既有测试只断言「函数被调用 / 文本出现」，
 * 都测不到「列表根本没有被渲染」这种**信息架构**缺陷。
 */

// ── B2：掩码真源 ─
//
// 掩码语义的三个独立来源（详见报告），本门禁把前端常量与 ESP32 头文件**钉死**：
//   1. esp32-collector/components/dma_pool/include/dma_pool.h
//        #define DMA_BUS_UART (1 << 0) / DMA_BUS_I2C (1 << 1) / DMA_BUS_SPI (1 << 2)
//   2. esp32-collector/components/dma_pool/dma_pool.c  (compatible_bus & bus_mask)
//   3. docs/archive/v4.0-重建前归档/设计/DMA资源协议设计.md  "bit0=UART, bit1=I2C, bit2=SPI"
//
// 注意：这三者**一致**，所以前端 busTypeToMask 保持 uart=1/i2c=2/spi=4 不变。
// 本门禁的作用是防止有人为了让 i2c "有资源可选" 而把掩码放宽
// （那会给用户一条芯片层面根本不能用的 DMA —— 比"没资源"更糟）。
const REPO = resolve(process.cwd(), '..')

/** 从 ESP32 头文件解析 DMA_BUS_* 位定义。解析不到必须抛错，否则门禁会静默退化为空断言。 */
function esp32BusBits(): Record<string, number> {
  const header = readFileSync(
    resolve(REPO, 'esp32-collector/components/dma_pool/include/dma_pool.h'),
    'utf8',
  )
  const bits: Record<string, number> = {}
  const re = /#define\s+DMA_BUS_(UART|I2C|SPI)\s+\(1\s*<<\s*(\d)\)/g
  let m: RegExpExecArray | null
  while ((m = re.exec(header)) !== null) bits[m[1]] = 1 << Number(m[2])
  if (Object.keys(bits).length !== 3) {
    throw new Error('未能从 dma_pool.h 解析出 3 个 DMA_BUS_* 位定义，实际：' + JSON.stringify(bits))
  }
  return bits
}

/** 从 NodeOverview.vue 源码解析 BUS_TYPE_MASKS 常量。 */
function frontendBusMasks(): Record<string, number> {
  const m = source.match(/const\s+BUS_TYPE_MASKS[^=]*=\s*\{([^}]*)\}/)
  if (!m) throw new Error('未能从 NodeOverview.vue 解析出 BUS_TYPE_MASKS')
  const masks: Record<string, number> = {}
  for (const pair of m[1].split(',')) {
    const kv = pair.match(/(\w+)\s*:\s*(\d+)/)
    if (kv) masks[kv[1]] = Number(kv[2])
  }
  if (Object.keys(masks).length === 0) throw new Error('BUS_TYPE_MASKS 解析为空')
  return masks
}

// ── 渲染用 mock（形状与后端真实响应对齐；DMA 数据取自隔离栈真实快照） ──
const { mockGetDetail, mockChannelList, mockGetCapabilities, mockClientGet, mockSubscribe, mockDmaFetch, mockRouterPush, mockDmaChannelsRef, mockDmaToggle, mockElMessageWarning } = vi.hoisted(() => ({
  mockGetDetail: vi.fn(() => Promise.resolve({
    id: 1, node_id: 'F0F5BDFFFE02', name: '机房采集器', model: 'esp32c6', status: 'online',
    firmware_version: '2.5.18', protocol_version: '2.2', connection_type: 'wifi',
    connection_quality: 92, latency_ms: 12, ping_latency_ms: 12,
    wifi_rssi: -62, free_heap_bytes: 153600, uptime_seconds: 187980,
    last_online_time: new Date(Date.now() - 3600_000).toISOString(),
    config_sync_state: 'in_sync', capabilities: {}, config: {},
  })),
  mockChannelList: vi.fn(() => Promise.resolve([
    { id: 1, node_id: 'F0F5BDFFFE02', name: '光学雨量计', hardware_type: 'UART', hardware_id: 'UART1', status: 'ok', enabled: true, config: {} },
  ])),
  mockGetCapabilities: vi.fn(() => Promise.resolve({
    buses: {
      i2c: [{ id: 'I2C0', enabled: true, mode: 'master', default_sda_pin: 21, default_scl_pin: 22, freq_hz: 1000000 }],
      uart: [{ id: 'UART0', enabled: true, default_tx_pin: 16, default_rx_pin: 17, max_baud: 5000000 },
             { id: 'UART1', enabled: true, default_tx_pin: 20, default_rx_pin: 21, max_baud: 5000000 }],
      spi: [{ id: 'SPI2', enabled: true, default_mosi_pin: 23, default_miso_pin: 19, default_sclk_pin: 18, default_cs_pin: 5, max_freq_hz: 40000000 }],
      adc: [{ id: 'ADC1_CH0', enabled: true, pin: 0, bits: 12 }], gpio: [], pwm: [],
    },
  })),
  mockClientGet: vi.fn(() => Promise.resolve({ data: [] })),
  mockSubscribe: vi.fn(() => vi.fn()),
  mockDmaFetch: vi.fn(() => Promise.resolve()),
  mockRouterPush: vi.fn(),
  mockDmaToggle: vi.fn(() => Promise.resolve()),
  mockElMessageWarning: vi.fn(),
  // 隔离栈真实快照：CH0=4(SPI only) / CH1=5(UART|SPI, 已绑 uart1) / CH2=4(SPI only)
  mockDmaChannelsRef: { value: [
    { dma_id: 0, name: 'GDMA_CH0', dma_type: 0, capabilities: 3, max_burst: 4095, state: 0, bound_to: '', compatible_bus: 4 },
    { dma_id: 1, name: 'GDMA_CH1', dma_type: 0, capabilities: 3, max_burst: 4095, state: 1, bound_to: 'uart/uart1', compatible_bus: 5 },
    { dma_id: 2, name: 'GDMA_CH2', dma_type: 0, capabilities: 3, max_burst: 4095, state: 0, bound_to: '', compatible_bus: 4 },
  ] as any[] },
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ back: vi.fn(), push: mockRouterPush, currentRoute: { value: { path: '/node/1' } } }),
  useRoute: () => ({ params: { id: '1' }, query: {}, name: 'NodeDetail', path: '/node/1' }),
}))
vi.mock('@/api/node', () => ({
  nodeApi: {
    getDetail: mockGetDetail, update: vi.fn(() => Promise.resolve()), syncConfig: vi.fn(() => Promise.resolve()),
    ping: vi.fn(() => Promise.resolve({ message: 'ping sent' })), getCapabilities: mockGetCapabilities,
    scanI2C: vi.fn(() => Promise.resolve({ devices: [] })), queryResources: vi.fn(() => Promise.resolve({ request_id: 'q' })),
    getOTAHistory: vi.fn(() => Promise.resolve([])), cancelOTA: vi.fn(() => Promise.resolve()),
  },
}))
vi.mock('@/api/channel', () => ({ channelApi: { getList: mockChannelList } }))
vi.mock('@/api/client', () => ({ default: { get: mockClientGet } }))
vi.mock('@/stores/websocket', () => ({ useWebSocketStore: () => ({ connected: true, subscribe: mockSubscribe }) }))
vi.mock('@/stores/dma', () => ({
  useDmaStore: () => ({
    mergedChannels: mockDmaChannelsRef.value, loading: false, toggling: {},
    fetch: mockDmaFetch, clearCache: vi.fn(), toggle: mockDmaToggle,
  }),
}))
vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({ fetchList: vi.fn(() => Promise.resolve()), getCachedList: vi.fn(() => ({ items: [], total: 0 })), invalidateLists: vi.fn(), clearCache: vi.fn() }),
}))
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return { ...actual, ElMessage: { ...actual.ElMessage, warning: mockElMessageWarning, success: vi.fn(), info: vi.fn(), error: vi.fn() }, ElMessageBox: { confirm: vi.fn(() => Promise.resolve()) } }
})
vi.mock('@/utils/logger', () => ({ logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() } }))

const stubs = {
  ChannelManager: { name: 'ChannelManager', template: '<div class="cm-stub" />', props: ['modelValue', 'collectorId', 'capabilities', 'presetHardwareType', 'presetHardwareId', 'collectorStatus', 'initialData'] },
  ChannelTerminal: true, LogPanel: true, OTAForm: true, QuickCreateDeviceDialog: true, StatusBadge: true,
}

async function openBusTab() {
  const wrapper = mount(NodeOverview, { global: { stubs } })
  await flushPromises()
  await wrapper.findAll('.tab-item').find(item => item.text().includes('总线配置'))?.trigger('click')
  await flushPromises()
  return wrapper
}
async function selectBus(wrapper: any, label: string) {
  await wrapper.findAll('.bus-subtab').find((b: any) => b.text().includes(label))?.trigger('click')
  await flushPromises()
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
})

// ═══════════════ B2：掩码口径契约（防"为了让 i2c 有资源可选"而放宽） ══════════════
describe('B2 掩码口径必须与 ESP32 设备侧一致（不得放宽）', () => {
  it('前端 busTypeToMask 与 dma_pool.h 的 DMA_BUS_* 位定义逐位相同', () => {
    const bits = esp32BusBits()
    const masks = frontendBusMasks()
    expect(bits, '三份来源一致性前提：bit0=UART/bit1=I2C/bit2=SPI').toEqual({ UART: 1, I2C: 2, SPI: 4 })
    expect(masks.uart).toBe(bits.UART)
    expect(masks.i2c).toBe(bits.I2C)
    expect(masks.spi).toBe(bits.SPI)
  })

  it('分类器自检：把 i2c 掩码改宽（2→6）必须被判定为与设备侧不一致', () => {
    const bits = esp32BusBits()
    // 直接验证判据本身有区分力：6 会额外匹配 SPI-only 的 CH0/CH2
    expect(6 & bits.I2C).toBe(2)          // 6 仍含 I2C 位 ⇒ 单看 & 运算发现不了
    expect(6 !== bits.I2C).toBe(true)     // 但"不相等"这一判据能发现
    const spiOnly = 4
    expect((spiOnly & bits.I2C) !== 0, 'CH0/CH2(SPI only) 在正确掩码下不得匹配 i2c').toBe(false)
    expect((spiOnly & 6) !== 0, '若误放宽为 6，SPI-only 资源会被错误地提供给 i2c 用户').toBe(true)
  })

  it('真实快照下：uart 只有 CH1、i2c 为空、spi 有 3 条（这是设备语义，不是缺陷）', () => {
    const masks = frontendBusMasks()
    const dma = mockDmaChannelsRef.value
    expect(dma.filter((d: any) => (d.compatible_bus & masks.uart) !== 0).map((d: any) => d.name)).toEqual(['GDMA_CH1'])
    expect(dma.filter((d: any) => (d.compatible_bus & masks.i2c) !== 0)).toEqual([])
    expect(dma.filter((d: any) => (d.compatible_bus & masks.spi) !== 0).map((d: any) => d.name)).toEqual(['GDMA_CH0', 'GDMA_CH1', 'GDMA_CH2'])
  })
})

// ═══════════════ A：已创建通道列表 ═══════════════
describe('A：总线配置页必须渲染"已创建通道"列表', () => {
  it('通道以列表行呈现（名称/标识/使能/所属资源），而不是只有一个数字', async () => {
    const wrapper = await openBusTab()
    const card = wrapper.find('[data-created-channels]')
    expect(card.exists(), '总线配置页必须有已创建通道卡').toBe(true)
    const rows = wrapper.findAll('.bus-channel-item')
    expect(rows, '通道必须渲染成列表行').toHaveLength(1)
    const text = rows[0].text()
    expect(text).toContain('光学雨量计')   // 通道名
    expect(text).toContain('UART1')       // 硬件标识
    expect(text).toContain('已启用')       // 使能状态
    expect(text).toContain('资源 UART1')   // 绑定的资源（与资源表同口径）
  })

  it('每行有进入该通道配置的入口，且点击后打开编辑态对话框', async () => {
    const wrapper = await openBusTab()
    const btn = wrapper.find('[data-edit-channel="1"]')
    expect(btn.exists(), '每行必须有配置入口').toBe(true)
    await btn.trigger('click')
    await flushPromises()
    const stub = wrapper.findComponent(stubs.ChannelManager as any)
    expect(stub.props('modelValue')).toBe(true)
    expect((stub.props('initialData') as any)?.id, '必须是该行的通道（编辑态）').toBe(1)
  })

  it('空态必须可区分：无通道时明确说明，而不是整块消失', async () => {
    mockChannelList.mockResolvedValueOnce([] as any)
    const wrapper = await openBusTab()
    expect(wrapper.find('[data-created-channels]').exists()).toBe(true)
    const empty = wrapper.find('[data-created-channels-empty]')
    expect(empty.exists(), '无通道时必须给出空态说明').toBe(true)
    expect(empty.text()).toContain('暂无已创建通道')
    expect(wrapper.findAll('.bus-channel-item')).toHaveLength(0)
  })

  it('通道列表与资源清单是两个层级：资源表照常渲染，二者不得互相替代', async () => {
    const wrapper = await openBusTab()
    await selectBus(wrapper, 'UART')
    expect(wrapper.findAll('.bus-table tbody tr').length, '资源清单仍在').toBe(2)
    expect(wrapper.findAll('.bus-channel-item').length, '通道清单同时存在').toBe(1)
    // 数字列也不得被删（它仍是资源视角的摘要）
    expect(wrapper.findAll('.bus-table tbody tr')[1].text()).toContain('1')
  })
})

// ═══════════════ B1：DMA 资源可选 ═══════════════
describe('B1：DMA 必须可选资源（不是只能开关）', () => {
  it('SPI（3 条候选）渲染选择器，候选完整列出并带 dma_id', async () => {
    const wrapper = await openBusTab()
    await selectBus(wrapper, 'SPI')
    const sel = wrapper.find('[data-dma-select="SPI2"]')
    expect(sel.exists(), '多候选必须给选择器（改前只有一个开关）').toBe(true)
    const labels = sel.findAll('option').map((o: any) => o.text())
    expect(labels).toContain('GDMA_CH0（#0 · SPI）')
    expect(labels).toContain('GDMA_CH1（#1 · UART, SPI）')
    expect(labels).toContain('GDMA_CH2（#2 · SPI）')
    expect(labels, '必须能显式选择"不使用 DMA"').toContain('不使用 DMA')
  })

  it('选中某条 DMA 后按该条调用 store.toggle（指定 bindTo，且目标就是所选那条）', async () => {
    const wrapper = await openBusTab()
    await selectBus(wrapper, 'SPI')
    const sel = wrapper.find('[data-dma-select="SPI2"]')
    await sel.setValue('2')
    await flushPromises()
    expect(mockDmaToggle).toHaveBeenCalledTimes(1)
    const [collector, dma, enabled, bindTo] = mockDmaToggle.mock.calls[0] as any[]
    expect(collector).toBe('F0F5BDFFFE02')
    expect(dma.dma_id, '必须绑定用户选中的那一条，而不是"第一条候选"').toBe(2)
    expect(enabled).toBe(true)
    expect(bindTo).toBe('spi/spi2')
  })

  it('UART（只有 1 条候选 CH1）保留开关形态，不退化也不消失', async () => {
    const wrapper = await openBusTab()
    await selectBus(wrapper, 'UART')
    const rows = wrapper.findAll('.bus-table tbody tr')
    // UART0 候选为空→说明原因；UART1 候选唯一→开关
    const uart1Switches = rows[1].findAll('.el-switch')
    expect(uart1Switches, '单候选仍用开关（形态不变）').toHaveLength(1)
    expect(uart1Switches[0].attributes('aria-label')).toContain('GDMA_CH1')
    expect(rows[1].find('[data-dma-select]').exists()).toBe(false)
  })

  it('候选为 0 时不得留下空控件，且必须给出原因（B3）', async () => {
    const wrapper = await openBusTab()
    await selectBus(wrapper, 'I2C')
    const row = wrapper.findAll('.bus-table tbody tr')[0]
    expect(row.find('[data-dma-select]').exists()).toBe(false)
    expect(row.findAll('.el-switch'), '不得给出点不动的空开关').toHaveLength(0)
    const hint = row.find('[data-dma-none="I2C0"]')
    expect(hint.exists(), '必须显式说明原因').toBe(true)
    expect(hint.text()).toContain('该总线暂无可兼容的 DMA 资源')
  })

  it('B3 反例（分类器自检）：不支持 DMA 的总线给 "—"，不得谎称"暂无可兼容资源"', async () => {
    const wrapper = await openBusTab()
    await selectBus(wrapper, 'ADC')
    // ADC 没有掩码位 ⇒ 走既有的 busSupportsDma 分支渲染 "—"（改前行为，保持不变）
    expect(wrapper.find('[data-dma-none="ADC1_CH0"]').exists()).toBe(false)
    const cell = wrapper.findAll('.bus-table tbody tr')[0].findAll('td')[6]
    expect(cell.text().trim()).toBe('—')
    // 分类器自检的核心：两者措辞必须不同 —— "本来就不支持" ≠ "有这条总线但没兼容资源"
    expect(cell.text()).not.toContain('暂无可兼容')
  })
})

// ═══════════════ 源码级防回退 ═══════════════
describe('源码级防回退（改回旧行为必须变红）', () => {
  it('DMA 单元格不得退回"无条件单个 el-switch"', () => {
    // 候选 >1 时必须走 el-select；若有人把选择器删掉、只留 el-switch，这里会红
    expect(source).toContain('resourceDmaCandidates(resource).length > 1')
    expect(source).toContain('changeResourceDma')
    expect(source).toContain('dmaSelectionFor')
  })
  it('通道列表不得退回"只渲染数字"', () => {
    expect(source).toContain('data-created-channels')
    expect(source).toContain('data-edit-channel')
  })
  it('掩码常量必须仍为 uart:1 / i2c:2 / spi:4（放宽即变红）', () => {
    const masks = frontendBusMasks()
    expect(masks).toEqual({ uart: 1, i2c: 2, spi: 4 })
  })
})
