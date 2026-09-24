import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import DataPanel from '@/views/data/DataPanel.vue'
import dataPanelSource from '@/views/data/DataPanel.vue?raw'
import { edgeDeviceApi } from '@/api/edgeDevice'

// ── Mock API module (correct path: @/api/edgeDevice, NOT @/api/modules/data) ──
vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: vi.fn(() =>
      Promise.resolve({
        total: 0,
        items: [],
      })
    ),
    getDetail: vi.fn(() => Promise.resolve({})),
    create: vi.fn(() => Promise.resolve({ id: 1 })),
    update: vi.fn(() => Promise.resolve()),
    delete: vi.fn(() => Promise.resolve()),
    getHistoryData: vi.fn(() => Promise.resolve({ items: [], total: 0 })),
    getLatestData: vi.fn(() => Promise.resolve(null)),
  },
  // type EdgeDevice is erased at runtime; no mock needed
}))

// ── Mock edge device store ──
vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({
    list: [{ id: 1, name: '设备 1' }, { id: 42, name: '设备 42' }],
    listTotal: 0,
    listLoading: false,
    fetchList: vi.fn(() => Promise.resolve()),
    getCachedList: vi.fn(() => ({
      items: [{ id: 1, name: '设备 1' }, { id: 42, name: '设备 42' }],
    })),
    deleteDevice: vi.fn(() => Promise.resolve()),
    clearCache: vi.fn(),
    isFresh: vi.fn(() => false),
  }),
}))

// ── Mock websocket store ──
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    subscribe: vi.fn(() => vi.fn()),
    isConnected: false,
    statusMessage: '',
  }),
}))

const { mockClientGet } = vi.hoisted(() => ({
  mockClientGet: vi.fn<(url: string, config?: unknown) => Promise<unknown>>(() => Promise.resolve([])),
}))

// A5：本页现在消费 `?device=` / `?range=` 深链，需要 vue-router 替身。
// 真实 vue-router 的 route 一定有 query；替身必须给全，否则 route.query 取值为 undefined。
const { mockRouteQuery } = vi.hoisted(() => ({ mockRouteQuery: { value: {} as Record<string, unknown> } }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: mockRouteQuery.value, params: {}, name: 'DataPanel', path: '/data' }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

vi.mock('@/api/client', () => ({
  default: {
    get: mockClientGet,
    post: vi.fn(() => Promise.resolve({ data: {} })),
    put: vi.fn(() => Promise.resolve()),
    delete: vi.fn(() => Promise.resolve()),
  },
}))

// ── Mock utility modules ──
vi.mock('@/utils/exportData', () => ({
  exportCSV: vi.fn(),
  exportJSON: vi.fn(),
}))

vi.mock('@/utils/feedback', () => ({
  default: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
}))

vi.mock('@/utils/downsample', () => ({
  downsampleData: vi.fn((data: any[]) => data),
}))

vi.mock('@/utils/sensor', () => ({
  sensorNameMap: {},
  sensorUnitMap: {},
}))

vi.mock('@/utils/errorCode', () => ({
  getErrorInfo: vi.fn(() => ({ type: 'info', label: '正常' })),
}))

vi.mock('@/utils/logger', () => ({
  logger: {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  },
}))

// ── Stub Element Plus components and local components ──
/**
 * 取 getHistoryData 最后一次调用的实参。
 * 用索引而非 Array.prototype.at —— tsconfig 的 lib 未含 ES2022。
 */
function lastHistoryCall() {
  const calls = vi.mocked(edgeDeviceApi.getHistoryData).mock.calls
  return calls[calls.length - 1]
}

const stubs = {
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-form': { template: '<div class="el-form"><slot /></div>' },
  'el-form-item': { template: '<div class="el-form-item"><slot /></div>' },
  // el-select / el-option 使用 test-setup.ts 全局 stub（真实 <select>/<option>），
  // 本地 stub 会覆盖全局注册并导致 select.el-select 断言失败。
  'el-button': {
    template: '<button class="el-button" :disabled="disabled" @click="$emit(\'click\')"><slot /><slot name="icon" /></button>',
    props: ['disabled', 'loading', 'type', 'size'],
  },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-row': { template: '<div class="el-row"><slot /></div>' },
  'el-col': { template: '<div class="el-col"><slot /></div>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  'el-switch': { template: '<div class="el-switch" />' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-empty': { template: '<div class="el-empty"><slot /></div>' },
  'el-table': { template: '<table class="el-table"><slot /></table>' },
  'el-table-column': { template: '<col />' },
  // 不再本地 stub 'el-pagination': 原先的 { template: '<div class="el-pagination" />' }
  // 是**惰性**替身 (不渲染 total、不 emit current-change), 使「翻页」在测试里根本无法发生,
  // 也就无法断言"筛选变化重置页码"。改用 src/test-setup.ts 的全局 ElPagination stub ——
  // 它点击时 emit update:currentPage + current-change, 与真实组件契约一致。
  'el-checkbox-group': { template: '<div class="el-checkbox-group"><slot /></div>' },
  'el-checkbox': { template: '<label class="el-checkbox"><slot /></label>' },
  PageHeader: { template: '<div class="page-header"><slot /><slot name="extra" /></div>' },
  LineChart: { template: '<div class="line-chart" />' },
  // Icon components from @element-plus/icons-vue
  Download: { template: '<i />' },
  Connection: { template: '<i />' },
}

describe('DataPanel', () => {
  it('reads devices from the requested parameter cache', () => {
    expect(dataPanelSource).toContain('edgeDeviceStore.getCachedList(params)')
  })

  const getMounted = () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    return mount(DataPanel, {
      global: { plugins: [pinia], stubs },
    })
  }

  beforeEach(() => {
    vi.clearAllMocks()
    // query 是可变对象，clearAllMocks 不重置它 —— 不重置会让 A5 用例相互污染
    mockRouteQuery.value = {}
  })

  it('renders the .data-panel root element', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.data-panel').exists()).toBe(true)
  })

  it('renders the page header', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.page-header').exists()).toBe(true)
  })

  it('renders the query form (el-form)', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.el-form').exists()).toBe(true)
  })

  it('renders the el-card container', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.el-card').exists()).toBe(true)
  })

  it('does not show stat-card when no device is selected and no data', async () => {
    const wrapper = getMounted()
    await flushPromises()
    // stat-card is v-if="queryForm.deviceId && historyData.length > 0"
    // With no device selected and empty data, it should NOT render
    expect(wrapper.find('.stat-card').exists()).toBe(false)
  })

  it('renders export CSV button', async () => {
    const wrapper = getMounted()
    await flushPromises()
    const buttons = wrapper.findAll('button')
    // The export button contains text "导出CSV"
    const exportBtn = buttons.find(b => b.text().includes('导出CSV'))
    expect(exportBtn).toBeDefined()
    expect(exportBtn!.exists()).toBe(true)
  })

  it('export button is disabled when no device selected', async () => {
    const wrapper = getMounted()
    await flushPromises()
    const buttons = wrapper.findAll('button')
    const exportBtn = buttons.find(b => b.text().includes('导出CSV'))
    expect(exportBtn).toBeDefined()
    // disabled because !queryForm.deviceId || !historyData || historyData.length === 0
    expect(exportBtn!.attributes('disabled')).toBeDefined()
  })

  it('handles empty device list gracefully', async () => {
    const wrapper = getMounted()
    await flushPromises()
    // Component should still render without errors
    expect(wrapper.find('.data-panel').exists()).toBe(true)
    // No stat cards when empty
    expect(wrapper.find('.stat-card').exists()).toBe(false)
  })

  it('filters invalid category API rows before rendering selected-device metrics', async () => {
    mockClientGet.mockResolvedValueOnce([
      { code: 'temperature', unit: '°C' },
      { code: '', unit: 'ignored' },
    ])

    const wrapper = getMounted()
    await flushPromises()
    // 通过设备 select 的 v-model 用户交互选中设备，触发 loadDeviceCategories。
    const selects = wrapper.findAll('select.el-select')
    await selects[0].setValue('1')
    await wrapper.findAll('button').find(button => button.text().includes('查询'))!.trigger('click')
    await flushPromises()

    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/unified-data/categories', {
      params: { device_pk: 1 },
    })
  })

  it('uses the selected device categories instead of a global hardcoded list', async () => {
    ;(mockClientGet as any).mockImplementation(((url: string) => {
      if (url === '/api/v1/unified-data/categories') {
        return Promise.resolve([{ code: 'battery_voltage', unit: 'V' }])
      }
      return Promise.resolve([])
    }) as any)

    const wrapper = getMounted()
    await flushPromises()
    const selects = wrapper.findAll('select.el-select')
    await selects[0].setValue('42')
    await wrapper.findAll('button').find(button => button.text().includes('查询'))!.trigger('click')
    await flushPromises()

    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/unified-data/categories', {
      params: { device_pk: 42 },
    })
    // 真实渲染中只出现 API 返回的类别，不出现全局默认类别名称。
    expect(dataPanelSource).not.toContain("['temperature', 'humidity']")
  })

  // 后端契约变更回归：/unified-data/{categories,historical-batch,historical}
  // 现返回 {code,data,message} envelope；若仍按裸数组解包会静默走空数组路径。
  const triggerQuery = async (wrapper: ReturnType<typeof getMounted>, deviceId: string) => {
    const selects = wrapper.findAll('select.el-select')
    await selects[0].setValue(deviceId)
    await wrapper.findAll('button').find(button => button.text().includes('查询'))!.trigger('click')
    await flushPromises()
    await flushPromises()
  }

  const mockHistoryWithOneRow = () => {
    vi.mocked(edgeDeviceApi.getHistoryData).mockResolvedValue({
      items: [{ id: 1, collected_at: '2024-01-01T00:00:00Z', data: { temperature: 21 } }],
      total: 1,
    })
  }

  it('unwraps envelope categories + historical-batch to render chart series', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation((url: string) => {
      if (url === '/api/v1/unified-data/categories') {
        return Promise.resolve({ code: 200, data: [{ code: 'temperature', unit: '°C' }], message: 'ok' })
      }
      if (url === '/api/v1/unified-data/historical-batch') {
        return Promise.resolve({
          code: 200,
          data: [{ category: 'temperature', data: [{ timestamp: '2024-01-01T00:00:00Z', value: 21 }] }],
          message: 'ok',
        })
      }
      return Promise.resolve({ code: 200, data: [], message: 'ok' })
    })

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    // categories envelope 被解包 → 批量请求带上真实 category（而非空类别短路）
    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/unified-data/historical-batch', {
      params: expect.objectContaining({ categories: 'temperature' }),
    })
    // historical-batch envelope 被解包 → 图表真实渲染
    expect(wrapper.find('.line-chart').exists()).toBe(true)
  })

  it('still builds chart series when APIs return bare arrays (backward compatible)', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation((url: string) => {
      if (url === '/api/v1/unified-data/categories') {
        return Promise.resolve([{ code: 'temperature', unit: '°C' }])
      }
      if (url === '/api/v1/unified-data/historical-batch') {
        return Promise.resolve([{ category: 'temperature', data: [{ timestamp: '2024-01-01T00:00:00Z', value: 21 }] }])
      }
      return Promise.resolve([])
    })

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/unified-data/historical-batch', {
      params: expect.objectContaining({ categories: 'temperature' }),
    })
    expect(wrapper.find('.line-chart').exists()).toBe(true)
  })

  it('falls back to empty trend state when categories envelope data is empty', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation(() => Promise.resolve({ code: 200, data: [], message: 'ok' }))

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/unified-data/categories', {
      params: { device_pk: 1 },
    })
    // 空类别 → buildChartSeries 提前返回，不再请求批量历史，图表走空态
    expect(mockClientGet).not.toHaveBeenCalledWith('/api/v1/unified-data/historical-batch', expect.anything())
    expect(wrapper.find('.line-chart').exists()).toBe(false)
  })

  // ─── 查询范围变化重置页码 (U-4 / §3.2.6 MUST) ───
  // 断言真实请求参数: 审计实测「翻到第 3 页后换设备/时间范围, 新查询仍发 page=3」。

  it('查询按钮重置页码: 翻页后换设备再查询, 请求的 page 参数为 1', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation(() => Promise.resolve({ code: 200, data: [], message: 'ok' }))

    const wrapper = getMounted()
    await flushPromises()

    // 第 1 次查询 (设备 1)
    await triggerQuery(wrapper, '1')
    const first = lastHistoryCall()
    expect((first[1] as Record<string, unknown>).page).toBe(1)

    // 翻到第 3 页: 直接点分页器两次 (真实分页组件的 current-change)
    const pagination = wrapper.find('.el-pagination')
    expect(pagination.exists()).toBe(true)
    await pagination.trigger('click')
    await flushPromises() // 两次点击之间必须 flush: 否则第二次仍读到旧 props.currentPage
    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()
    const paged = lastHistoryCall()
    expect((paged[1] as Record<string, unknown>).page).toBe(3)

    // 换设备后点「查询」→ 必须以 page=1 发出 (改前会继续发 page=3)
    await triggerQuery(wrapper, '42')
    const afterDevice = lastHistoryCall()
    expect(afterDevice[0]).toBe(42)
    expect((afterDevice[1] as Record<string, unknown>).page).toBe(1)
  })

  it('查询按钮重置页码: 换时间范围后请求的 page 参数为 1', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation(() => Promise.resolve({ code: 200, data: [], message: 'ok' }))

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    const pagination = wrapper.find('.el-pagination')
    await pagination.trigger('click')
    await flushPromises()
    expect((lastHistoryCall()[1] as Record<string, unknown>).page).toBe(2)

    // 换时间范围 (第二个 select) 后再查询
    const selects = wrapper.findAll('select.el-select')
    await selects[1].setValue('7d')
    await wrapper.find('[data-test="query"]').trigger('click')
    await flushPromises()

    const params = lastHistoryCall()[1] as Record<string, unknown>
    expect(params.page).toBe(1)
  })

  // ─── 统计卡范围标注（F14 / §4.3 统计卡 MUST） ───
  // 改前「本次数据点」「采集覆盖时长」都无范围词，且与分页器「共 N 条」并排：
  // 「本次数据点」是服务端按筛选条件的全量总数，而「采集覆盖时长」只由当前页 20 行算出。
  // 两种范围混排且都不标注 ⇒ 用户会把「本页 9 分钟」读成「30 天只采到 9 分钟」。

  const SCALE_WORDS = /本页|当前筛选|全局/

  const mockMultiPageHistory = () => {
    // 关键：total(137) 远大于本页行数(3)，复现审计现场「共 137 条」与「本次数据点」并排的歧义
    vi.mocked(edgeDeviceApi.getHistoryData).mockResolvedValue({
      items: [
        { id: 1, collected_at: '2024-01-01T00:00:00Z', data: { temperature: 21 } },
        { id: 2, collected_at: '2024-01-01T00:05:00Z', data: { temperature: 22 } },
        { id: 3, collected_at: '2024-01-01T00:20:00Z', data: { temperature: 23 } },
      ],
      total: 137,
    })
  }

  it('每张统计卡都带范围词，且范围与实际计算口径一致', async () => {
    mockMultiPageHistory()
    mockClientGet.mockImplementation((url: string) => {
      if (url === '/api/v1/unified-data/categories') {
        return Promise.resolve([{ code: 'temperature', unit: '°C' }])
      }
      return Promise.resolve([])
    })

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    const cards = wrapper.findAll('.stat-card')
    expect(cards.length).toBeGreaterThan(0)

    // ① 每张卡都必须能读出范围词（§4.3.1 MUST）
    const labels = cards.map(c => c.find('.stat-label').text())
    for (let i = 0; i < labels.length; i++) {
      expect(labels[i], '第 ' + i + ' 张统计卡缺少范围词: ' + labels[i]).toMatch(SCALE_WORDS)
    }

    // ② 范围口径必须与真实数据来源一致（§4.3.4 数值必须与实际记录一致）
    const totalCard = wrapper.find('[data-testid="stat-total-points"]')
    expect(totalCard.exists()).toBe(true)
    // 「数据点总数」= 服务端全量 total（137），所以范围词必须是「当前筛选」而不是「本页」
    expect(totalCard.text()).toBe('137')
    expect(cards.find(c => c.text().includes('数据点总数'))!.find('.stat-label').text()).toContain('当前筛选')

    // ③「采集覆盖时长」只由当前页 3 行算出，范围词必须是「本页」
    const durationCard = wrapper.find('[data-testid="stat-duration"]')
    expect(durationCard.exists()).toBe(true)
    expect(cards.find(c => c.text().includes('采集覆盖时长'))!.find('.stat-label').text()).toContain('本页')
    // 本页 3 行跨 20 分钟 → 数值确由当前页算出（而非 137 条的全量跨度）
    expect(durationCard.text()).toBe('20分钟')
  })

  it('范围词与分页器总数并排时不再产生「全量」歧义', async () => {
    mockMultiPageHistory()
    mockClientGet.mockImplementation(() => Promise.resolve([]))

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    // 分页器显示全量 137 条；相邻的「采集覆盖时长」必须自带「本页」限定，
    // 否则两者并排会让用户误以为 137 条只覆盖了 20 分钟。
    const pagination = wrapper.find('.el-pagination')
    expect(pagination.text()).toContain('137')
    const durationLabel = wrapper.findAll('.stat-card')
      .find(c => c.text().includes('采集覆盖时长'))!
      .find('.stat-label').text()
    expect(durationLabel).toContain('本页')
    // 不能只写「覆盖时长」——范围词必须真实出现，而不是靠上下文暗示
    expect(durationLabel).not.toBe('采集覆盖时长')
  })

  it('本页无数据时时长显示统一未知占位符 —，而不是 0 或 --', async () => {
    // 空数组 = 「本页没有数据」，不是「时长 0 分钟」
    vi.mocked(edgeDeviceApi.getHistoryData).mockResolvedValue({ items: [], total: 0 })
    mockClientGet.mockImplementation(() => Promise.resolve([]))

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    // historyData 为空 ⇒ 统计卡整组不渲染（v-if），因此这里断言的是"不得出现伪造的 0/--"
    expect(wrapper.find('[data-testid="stat-duration"]').exists()).toBe(false)
    expect(dataPanelSource).not.toContain("duration = '--'")
    expect(dataPanelSource).not.toMatch(/duration:\s*'--'/)
  })

  // 移动端标签逐字竖排护栏（§4.2.5）
  // 实测根因：165px 卡里 图标48 + gap12 + 卡片内边距40 只给标签留 63px，
  // 「数据点总数」「采集覆盖时长」(72px) 折成末行仅 1 字的「……数」/「……长」。
  // 这里的断言锁定"给标签留出足够宽度"的三条 CSS 依据 —— 三者缺一即退回孤字换行。
  it('移动端为统计标签预留足够宽度，避免末行只剩 1 个字（§4.2.5 防逐字竖排）', () => {
    const css = dataPanelSource.replace(/\/\*[\s\S]*?\*\//g, '')
    const mobile = css.slice(css.indexOf('@media (max-width: 768px)'))

    // ① 图标收窄到 32px（48px 会吃掉标签空间）
    expect(mobile).toMatch(/\.stat-icon\s*\{[^}]*width:\s*32px/)
    // ② 图标与文字间距收到 8px
    expect(mobile).toMatch(/\.stat-content\s*\{[^}]*gap:\s*8px/)
    // ③ 字号必须保持 12px：不得靠"缩小到不可读"来塞下标签（§4.4.1）
    expect(mobile).not.toMatch(/\.stat-label\s*\{[^}]*font-size:\s*(?:[0-9]|10|11)px/)
    // ④ 卡片内边距收窄：360px 档（规范 §4.4.1 核心下限）实测 20px 内边距
    //    仍会让「采集覆盖时长」折成末行只剩「长」一个字，16px 才够
    expect(mobile).toMatch(/\.stat-card\s*:deep\(\.el-card__body\)\s*\{[^}]*padding:\s*16px/)
    // ⑤ 范围词 chip 仍是独立元素且允许换行到下一行
    expect(mobile).toMatch(/\.stat-scope\s*\{[^}]*margin-top/)
  })

  it('源码不再残留半角连字符占位符（§3.4.5 统一为 —）', () => {
    // 表格「原始数据」列的空值分支改前渲染字面量 '-'，与全站 '—' 不一致
    expect(dataPanelSource).not.toMatch(/>\s*-\s*<\/span>/)
    expect(dataPanelSource).not.toMatch(/return\s+'-'/)
    expect(dataPanelSource).not.toMatch(/\?\s*'[^']*'\s*:\s*'-'/)
  })

  it('翻页本身不重置页码 (分页控件入口不得被打回第 1 页)', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation(() => Promise.resolve({ code: 200, data: [], message: 'ok' }))

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    const pagination = wrapper.find('.el-pagination')
    await pagination.trigger('click')
    await flushPromises()
    expect((lastHistoryCall()[1] as Record<string, unknown>).page).toBe(2)

    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()
    expect((lastHistoryCall()[1] as Record<string, unknown>).page).toBe(3)
  })

  // ── A5：`?device=` / `?range=` 深链预填 ──
  //
  // 改前 /data 的 route.query 零消费：从设备详情/图表"查看历史"跳进来，
  // 用户还得手动选设备+时间范围（4 步），URL 参数被完全忽略。
  // 注意：预填只在**设备列表里真实存在**该 id 时才生效（否则 el-select 显示空白，
  // 看起来像深链没生效）。本 spec 的设备列表是 [1, 42]。
  describe('A5 深链预填', () => {
    /**
     * 断言走**行为面**而非 `wrapper.vm`：`<script setup>` 不暴露内部状态
     * （本仓惯例见 Profile.spec.ts:95），且 vm 断言在 vue-tsc 下是 TS2339。
     * 这里直接看真实请求——deviceId 是 getHistoryData 的第 1 个实参。
     */
    const deviceIdOf = (call: unknown[] | undefined) => call?.[0]

    it('?device=42&range=7d 挂载后按该设备自动发起一次查询', async () => {
      mockRouteQuery.value = { device: '42', range: '7d' }
      getMounted()
      await flushPromises()
      // 复用 handleQuery（与点「查询」同入口），不是另写一条取数路径
      expect(lastHistoryCall()).toBeTruthy()
      expect(deviceIdOf(lastHistoryCall())).toBe(42)
    })

    it('?device=abc（非法）被忽略，且不发起查询', async () => {
      mockRouteQuery.value = { device: 'abc' }
      getMounted()
      await flushPromises()
      expect(vi.mocked(edgeDeviceApi.getHistoryData)).not.toHaveBeenCalled()
    })

    it('?device=999（不在设备列表里）被忽略 —— 避免选中态空白', async () => {
      mockRouteQuery.value = { device: '999' }
      getMounted()
      await flushPromises()
      expect(vi.mocked(edgeDeviceApi.getHistoryData)).not.toHaveBeenCalled()
    })

    it('?range=99d（非法枚举）被忽略：不发查询，时间范围保持默认', async () => {
      mockRouteQuery.value = { range: '99d' }
      const wrapper = getMounted()
      await flushPromises()
      expect(vi.mocked(edgeDeviceApi.getHistoryData)).not.toHaveBeenCalled()
      // 时间范围下拉的当前值：替身是原生 <select>，读它的 value
      const rangeSelect = wrapper.findAll('select')[1]
      expect(rangeSelect?.element.value).toBe('24h')
    })

    it('?range=1h 是合法值（模板存在该选项，常量表不得漏项）', async () => {
      mockRouteQuery.value = { device: '42', range: '1h' }
      const wrapper = getMounted()
      await flushPromises()
      const rangeSelect = wrapper.findAll('select')[1]
      expect(rangeSelect?.element.value).toBe('1h')
    })
  })
})

