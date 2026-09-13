import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Dashboard from '@/views/dashboard/Dashboard.vue'
// EmptyState 使用真实实现：它的 props 契约已支持 kind="error"，
// 失败态用例必须验证真实契约被接线，而不是被 stub 吞掉。
// El* 组件沿用 test-setup.ts 的全局 stub（ElAlert 渲染 title slot，ElButton 真实 emit click）。
import RealEmptyState from '@/components/common/EmptyState.vue'

// Mock vue-router
const mockPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
}))

// Mock dataApi
vi.mock('@/api/data', () => ({
  dataApi: {
    getOverview: vi.fn(() =>
      Promise.resolve({
        nodes: { total: 5, online: 3, offline: 2 },
        edge_devices: { total: 10, online: 7, offline: 3 },
        latest_data: [],
      })
    ),
  },
}))

// Mock api client
vi.mock('@/api/client', () => ({
  default: { get: vi.fn(() => Promise.resolve({ data: [] })) },
}))

// Mock websocket store。subscribe 记录回调，使"WS 触发的静默刷新失败"路径也可测。
const wsHandlers: Record<string, (msg: unknown) => void> = {}
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    subscribe: vi.fn((event: string, handler: (msg: unknown) => void) => {
      wsHandlers[event] = handler
      return vi.fn()
    }),
    connected: false,
  }),
}))

// Mock logger
vi.mock('@/utils/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

// Mock sensor utils
vi.mock('@/utils/sensor', () => ({
  sensorNameMap: { temperature: '温度', humidity: '湿度' },
  sensorUnitMap: { temperature: '°C', humidity: '%' },
  SENSOR_ORDER: ['temperature', 'humidity'],
}))

// Stub child components
const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /></div>' },
  SkeletonCard: true,
  EmptyState: true,
  LineChart: true,
  'el-row': { template: '<div class="el-row"><slot /></div>' },
  'el-col': { template: '<div class="el-col"><slot /></div>' },
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-button': { template: '<button class="el-button"><slot /></button>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  'el-table': { template: '<div class="el-table"><slot /></div>' },
  'el-table-column': { template: '<div />' },
  'el-radio-group': { template: '<div class="el-radio-group"><slot /></div>' },
  'el-radio-button': { template: '<div><slot /></div>' },
  'el-select': { template: '<div><slot /></div>' },
  'el-option': { template: '<div />' },
  'el-timeline': { template: '<div class="el-timeline"><slot /></div>' },
  'el-timeline-item': { template: '<div class="el-timeline-item"><slot /></div>' },
  'el-empty': { template: '<div class="el-empty" />' },
  'router-link': { template: '<a><slot /></a>' },
}

describe('Dashboard.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders dashboard container', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.dashboard').exists()).toBe(true)
  })

  it('loads overview data on mount', async () => {
    const { dataApi } = await import('@/api/data')
    mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(dataApi.getOverview).toHaveBeenCalled()
  })

  it('displays stat cards after loading', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    const statCards = wrapper.findAll('.stat-card')
    expect(statCards.length).toBe(4)
  })

  it('uses the responsive KPI grid instead of fixed 24-column spans', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.dashboard-stats').exists()).toBe(true)
  })

  it('makes dashboard KPI navigation cards keyboard-operable', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    const firstCard = wrapper.findAll('.stat-card')[0]

    expect(firstCard.attributes('role')).toBe('link')
    expect(firstCard.attributes('tabindex')).toBe('0')
    expect(firstCard.attributes('aria-label')).toBe('查看节点总数')

    await firstCard.trigger('keydown.enter')
    expect(mockPush).toHaveBeenCalledWith('/node')

    await firstCard.trigger('keydown.space')
    expect(mockPush).toHaveBeenLastCalledWith('/node')
  })

  it('does not render a simulated status timeline as operational data', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('模拟数据')
  })

  it('loads real status history from the API', async () => {
    const client = (await import('@/api/client')).default
    mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(client.get).toHaveBeenCalledWith('/api/v1/nodes/status-history', { params: { limit: 20 } })
  })

  it('computes offline nodes correctly', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    // overview: total=5, online=3 → offline=2；通过告警卡的渲染值验证
    expect(wrapper.find('.alert-item .alert-value').text()).toBe('2')
  })

  it('computes offline devices correctly', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    // 第二个告警值：total=10, online=7 → offline=3
    const values = wrapper.findAll('.alert-item .alert-value')
    expect(values[1].text()).toBe('3')
  })

  it('navigates to node list on stat card click', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    await wrapper.findAll('.stat-card')[0].trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/node')
  })

  it('handles overview fetch error gracefully', async () => {
    const { dataApi } = await import('@/api/data')
    vi.mocked(dataApi.getOverview).mockRejectedValueOnce(new Error('Network error'))

    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    // 出错后不显示 loading skeleton，页面仍保留 dashboard 容器
    expect(wrapper.find('.dashboard').exists()).toBe(true)
    expect(wrapper.find('.el-skeleton').exists()).toBe(false)
  })

  it('shows the default trend range label', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('最近 24 小时趋势')
  })

  // 真 bug 回归（2026-09-11 类型门禁落地）：后端 latestEntry 缺 error_code 字段
  // 导致 dataErrorCount 恒 0、"采集错误"告警维度永不触发。后端已补字段，
  // 前端类型已同步；本用例证明 error_code>0 时告警渲染。
  it('renders the data-error alert dimension when latest_data contains error_code > 0', async () => {
    const { dataApi } = await import('@/api/data')
    vi.mocked(dataApi.getOverview).mockResolvedValueOnce({
      nodes: { total: 5, online: 5, offline: 0 },
      edge_devices: { total: 10, online: 10, offline: 0 },
      latest_data: [
        { device_id: 1, device_name: 'a', node_name: 'n', data: {}, collected_at: '', error_code: 2 },
        { device_id: 2, device_name: 'b', node_name: 'n', data: {}, collected_at: '', error_code: 0 },
        { device_id: 3, device_name: 'c', node_name: 'n', data: {}, collected_at: '' },
      ],
    })
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    const alertLabels = wrapper.findAll('.alert-item .alert-label').map(n => n.text())
    expect(alertLabels).toContain('采集错误（近 1h）')
    const values = wrapper.findAll('.alert-item .alert-value').map(n => n.text())
    expect(values).toContain('1')
  })
})

// ============================================================================
// 失败态范式（P0）：概览接口 500 时，仪表盘必须呈现「未知 + 可重试」，
// 不得把接口失败伪装成 0 节点在线 / 「运行正常」。
//
// 这些用例使用真实组件渲染失败态：
//   - EmptyState 用真实实现（它的 props 契约已支持 kind="error"，
//     本页此前既没接线、也没有任何用例守护）
//   - El* 用 test-setup.ts 注册的全局 stub（ElAlert 渲染 title slot、
//     ElButton 真实 emit click、ElSkeleton 渲染占位），因此 DOM 断言
//     针对的是页面的真实结构，而不是测试里另写的空壳。
// 只对本次修复相关的元素下断言。
// ============================================================================

const errorStubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /></div>' },
  SkeletonCard: true,
  LineChart: true,
  'router-link': { template: '<a><slot /></a>' },
}

function mountWithRealChildren() {
  return mount(Dashboard, {
    global: {
      stubs: { ...errorStubs, EmptyState: RealEmptyState },
    },
  })
}

describe('Dashboard.vue — 接口失败态', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('概览接口失败时不渲染「运行正常」，而是常驻错误态 + 重试入口，KPI 显示未知', async () => {
    const { dataApi } = await import('@/api/data')
    vi.mocked(dataApi.getOverview).mockRejectedValueOnce(new Error('Request failed with status code 500'))

    const wrapper = mountWithRealChildren()
    await flushPromises()

    // 1) 禁止把失败伪装成健康态
    expect(wrapper.text()).not.toContain('运行正常')
    expect(wrapper.text()).not.toContain('节点与设备均在线，暂无采集错误')

    // 2) 必须有常驻错误态与重试入口
    const alert = wrapper.find('[data-test="dashboard-error"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('获取概览数据失败')
    expect(alert.text()).toContain('500')
    const retry = wrapper.find('[data-test="dashboard-retry"]')
    expect(retry.exists()).toBe(true)
    expect(retry.text()).toBe('重试')

    // 3) 摘要卡片必须显式声明未知，而不是渲染绿色对勾
    expect(wrapper.find('[data-test="dashboard-summary-error"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="dashboard-summary-tag"]').text()).toBe('状态未知')

    // 4) KPI 必须是未知占位 '—'，绝不能是 0
    const values = wrapper.findAll('.stat-value').map(n => n.text())
    expect(values).toEqual(['—', '—', '—', '—'])

    // 5) 失败时不得出现任何「暂无数据」式的空态结论
    expect(wrapper.text()).not.toContain('暂无数据')

    // 6) 重试入口真实可用：第二次成功后错误态消失并恢复真实数值
    vi.mocked(dataApi.getOverview).mockResolvedValueOnce({
      nodes: { total: 5, online: 3, offline: 2 },
      edge_devices: { total: 10, online: 7, offline: 3 },
      latest_data: [],
    })
    await retry.trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="dashboard-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="dashboard-summary-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="dashboard-summary-tag"]').text()).toBe('需关注')
    expect(wrapper.findAll('.stat-value').map(n => n.text())).toEqual(['5', '3', '10', '7'])
    expect(dataApi.getOverview).toHaveBeenCalledTimes(2)
  })

  it('概览失败时趋势图与最新数据区渲染 kind="error" 的空态（而非「暂无数据」）', async () => {
    const { dataApi } = await import('@/api/data')
    vi.mocked(dataApi.getOverview).mockRejectedValueOnce(new Error('boom'))

    const wrapper = mountWithRealChildren()
    await flushPromises()

    const empties = wrapper.findAllComponents(RealEmptyState)
    const kinds = empties.map(e => e.props('kind'))
    const titles = empties.map(e => e.props('title'))

    // 两个错误区（趋势 + 最新数据）都必须走错误态契约
    expect(kinds).toContain('error')
    expect(kinds.filter(k => k === 'error')).toHaveLength(2)
    expect(titles).toContain('趋势数据加载失败')
    expect(titles).toContain('最新数据加载失败')
    // 明确的数据缺失结论在接口失败时是不允许出现的
    expect(titles).not.toContain('暂无趋势数据')
    expect(titles).not.toContain('暂无数据')

    // 错误态自带重试动作（范式要求：可重试）
    const errorStates = empties.filter(e => e.props('kind') === 'error')
    for (const state of errorStates) {
      expect(state.props('quickActions')).toEqual([
        expect.objectContaining({ label: '重试', type: 'primary' }),
      ])
    }
  })

  it('概览成功时保持原有健康态渲染（错误态不得误伤正常路径）', async () => {
    const wrapper = mountWithRealChildren()
    await flushPromises()

    expect(wrapper.find('[data-test="dashboard-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="dashboard-summary-error"]').exists()).toBe(false)
    const values = wrapper.findAll('.stat-value').map(n => n.text())
    expect(values).toEqual(['5', '3', '10', '7'])
    expect(wrapper.findAllComponents(RealEmptyState).map(e => e.props('kind'))).not.toContain('error')
  })
  it('WS 触发的静默刷新失败同样进入错误态：不得保留上一份数据继续显示「运行正常」', async () => {
    vi.useFakeTimers()
    try {
      const { dataApi } = await import('@/api/data')
      const wrapper = mountWithRealChildren()
      await flushPromises()

      // 首次加载成功：健康态
      expect(wrapper.find('[data-test="dashboard-error"]').exists()).toBe(false)
      expect(wrapper.find('[data-test="dashboard-summary-tag"]').text()).toBe('需关注')

      // 后台刷新失败
      vi.mocked(dataApi.getOverview).mockRejectedValueOnce(new Error('刷新时接口 500'))
      const handler = wsHandlers.node_status
      expect(typeof handler).toBe('function')
      handler({ payload: { node_id: 'n1' } })

      // scheduleOverviewRefresh 有 1s 防抖
      await vi.advanceTimersByTimeAsync(1200)
      await flushPromises()

      expect(wrapper.find('[data-test="dashboard-error"]').exists()).toBe(true)
      expect(wrapper.find('[data-test="dashboard-error"]').text()).toContain('刷新时接口 500')
      expect(wrapper.text()).not.toContain('运行正常')
      expect(wrapper.find('[data-test="dashboard-summary-error"]').exists()).toBe(true)
      expect(wrapper.findAll('.stat-value').map(n => n.text())).toEqual(['—', '—', '—', '—'])
    } finally {
      vi.useRealTimers()
    }
  })
})