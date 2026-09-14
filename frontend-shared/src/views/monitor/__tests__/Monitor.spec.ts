import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import Monitor from '../Monitor.vue'

// Mock API
vi.mock('@/api/monitor', () => ({
  getMetricsSummary: vi.fn(() =>
    Promise.resolve({
      code: 200,
      data: {
        http: { requests_total: 100, requests_in_flight: 2 },
        mqtt: { messages_received: 50, messages_sent: 30, connection_errors: 0 },
        device: { online: 3, offline: 1 },
        node: { online: 2, offline: 0 },
        data: { points_collected: 5000, points_stored: 4990 },
        websocket: { connections_active: 4, messages_total: 200 },
        control: {
          operations_total: 20, active: 2, queued: 1, succeeded: 15, failed: 2,
          unknown: 1, unresolved_unknown: 1, cancelled: 0, outbox_pending: 1,
          outbox_leased: 0, capability_stale_nodes: 1, audit_write_failures: 0,
        },
      },
    })
  ),
}))

// Mock router
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

/** el-progress 替身：把 :color 写到 data-color 上，便于断言真实取到的主题色。 */
const ProgressStub = defineComponent({
  name: 'ElProgress',
  props: {
    percentage: { type: Number, default: 0 },
    strokeWidth: { type: Number, default: 6 },
    color: { type: [String, Array, Function], default: '' },
  },
  setup(props) {
    return () => h('div', {
      class: 'el-progress',
      'data-color': typeof props.color === 'string' ? props.color : '',
    })
  },
})

// Stub Element Plus components
const stubs = {
  PageHeader: { template: '<div class="page-header"><slot /></div>' },
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-row': { template: '<div class="el-row"><slot /></div>' },
  'el-col': { template: '<div class="el-col"><slot /></div>' },
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
  'el-select': { template: '<select class="el-select" @change="$emit(\'change\')"><slot /></select>' },
  'el-option': { template: '<option />' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-descriptions': { template: '<div class="el-descriptions"><slot /></div>' },
  'el-descriptions-item': { template: '<div class="el-desc-item"><slot /></div>' },
  // color 透传到 DOM：用于断言主题切换时进度条颜色随之变化（真实取值，非源码字符串断言）
  'el-progress': ProgressStub,
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-alert': { props: ['title'], template: '<div class="el-alert">{{ title }}</div>' },
}

/**
 * 主题 token 测试夹具：与 src/styles/theme.css 的 :root / html.dark 取值一致
 * （--color-success 亮 #67c23a / 暗 #85ce61，--color-danger 亮 #f56c6c / 暗 #f78989）。
 */
const THEME_STYLE_ID = 'monitor-spec-theme-tokens'
function installThemeTokens() {
  document.getElementById(THEME_STYLE_ID)?.remove()
  const style = document.createElement('style')
  style.id = THEME_STYLE_ID
  style.textContent = [
    ':root { --color-primary: #409eff; --color-success: #67c23a; --color-warning: #e6a23c; --color-danger: #f56c6c; --color-info: #909399; }',
    'html.dark { --color-primary: #409eff; --color-success: #85ce61; --color-warning: #ebb563; --color-danger: #f78989; --color-info: #a6a9ad; }',
  ].join('\n')
  document.head.appendChild(style)
}

/** 读取四个进度条实际渲染出的颜色（外层用 stub 透传 :color）。 */
function renderedProgressColors(wrapper: ReturnType<typeof mount>): string[] {
  return wrapper.findAll('.el-progress').map(el => el.attributes('data-color') || '')
}

describe('Monitor.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    document.documentElement.classList.remove('dark')
    installThemeTokens()
  })

  afterEach(() => {
    document.documentElement.classList.remove('dark')
    document.getElementById(THEME_STYLE_ID)?.remove()
  })

  it('renders monitor container', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.monitor-container').exists()).toBe(true)
  })

  it('renders toolbar with title', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.toolbar').exists()).toBe(true)
    expect(wrapper.text()).toContain('系统监控')
  })

  it('uses text and an icon rather than an emoji-only heading', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.toolbar h2').text()).toBe('系统监控')
  })

  it('renders refresh button', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    const btn = wrapper.find('button')
    expect(btn.exists()).toBe(true)
    expect(btn.text()).toContain('刷新')
  })

  it('renders stat cards section', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.stat-cards').exists()).toBe(true)
  })

  it('loads metrics on mount', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    // After API resolves, stat values should be visible
    expect(wrapper.text()).toContain('100') // http.requests_total
    expect(wrapper.text()).toContain('3')   // device.online
  })

  it('displays HTTP requests total', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('HTTP')
    expect(wrapper.text()).toContain('请求总数')
  })

  it('displays device online/offline status', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('设备在线状态')
  })

  it('displays node online/offline status', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('节点在线状态')
  })

  it('renders detail panels section', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.detail-panels').exists()).toBe(true)
  })

  it('shows durable control health and attention counts', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('控制面健康')
    expect(wrapper.text()).toContain('未处置 UNKNOWN')
    expect(wrapper.text()).toContain('能力快照过期')
    expect(wrapper.find('.control-alert').text()).toContain('2 项需要关注')
  })

  it('renders HTTP monitoring panel', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('HTTP 监控')
  })

  it('renders MQTT monitoring panel', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('MQTT 监控')
  })

  it('renders WebSocket monitoring panel', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('WebSocket')
  })

  it('renders footer with last update time', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.footer-info').exists()).toBe(true)
  })

  it('formats large numbers with K/M suffix', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    // 5000 points → "5.00K"
    expect(wrapper.text()).toContain('K')
  })

  it('进度条颜色是语义 token 引用，不随主题重新计算也不含硬编码色值（F17）', async () => {
    document.documentElement.classList.remove('dark')
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    const lightColors = renderedProgressColors(wrapper)
    // 四个进度条：设备在线/离线、节点在线/离线
    expect(lightColors).toEqual([
      'var(--color-success)', 'var(--color-danger)',
      'var(--color-success)', 'var(--color-danger)',
    ])

    // 切换到暗色（与 stores/theme.ts 一致：html.dark + data-theme）
    document.documentElement.classList.add('dark')
    document.documentElement.setAttribute('data-theme', 'dark')
    await flushPromises()

    // 契约（规范 §3.6.2）：传给 el-progress 的是 var(--color-*) 字符串，
    // 由**浏览器**按当前主题解析 —— 组件无需在主题切换时重新计算，
    // 这比「JS 解析 + MutationObserver」更强：取值不经 JS 中转，
    // 不可能出现「JS 与 CSS 各算一套」的偏差。
    expect(renderedProgressColors(wrapper)).toEqual(lightColors)

    // 守卫：将来有人改回静态十六进制常量，这里必须变红。
    // 分工：happy-dom 不做 var() 替换，故本层只能断言「props 是 token 引用」；
    // 「该 token 在亮暗下确实取不同值」由 src/styles/__tests__/SidebarThemeTokens.spec.ts
    // 的 token 解析断言与 .tmp-probe 下的真实浏览器探针覆盖。
    for (const c of lightColors) {
      expect(c, '进度条配色必须是 var(--color-*) 语义 token').toMatch(/^var\(--color-[a-z]+\)$/)
      expect(c).not.toMatch(/^#|^rgb/)
    }
    wrapper.unmount()
  })

  it('暗色下挂载、再切回亮色，进度条配色仍是同一组 token 引用', async () => {
    document.documentElement.classList.add('dark')
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(renderedProgressColors(wrapper)).toEqual([
      'var(--color-success)', 'var(--color-danger)',
      'var(--color-success)', 'var(--color-danger)',
    ])

    document.documentElement.classList.remove('dark')
    document.documentElement.removeAttribute('data-theme')
    await flushPromises()
    // 组件不持有主题状态副本，故不依赖任何回调时序：token 引用恒定。
    expect(renderedProgressColors(wrapper)).toEqual([
      'var(--color-success)', 'var(--color-danger)',
      'var(--color-success)', 'var(--color-danger)',
    ])
    wrapper.unmount()
  })

  it('源码不再引用 getThemeColors/THEME_COLORS（F17：进度条配色已完全交给 CSS 变量）', async () => {
    const src = (await import('../Monitor.vue?raw')).default as string
    expect(src).not.toContain('getThemeColors')
    expect(src).not.toContain('THEME_COLORS')
    // 反向守卫：必须真的用了语义 token，否则上面的 not.toContain 在「整个删掉配色」时也会通过
    expect(src.match(/var\(--color-[a-z]+\)/g)?.length ?? 0).toBeGreaterThanOrEqual(4)
  })

  // ─── KPI 范围标注（审计 Q3 三页均无范围标注 / §4.3 统计卡 MUST） ───

  it('四个 KPI 都带范围词，且范围与后端统计口径一致', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()

    const cards = wrapper.findAll('.stat-card')
    expect(cards).toHaveLength(4)

    // ① 每张卡都必须能读出范围词（§4.3.1 MUST）
    for (const card of cards) {
      expect(card.find('.stat-label').text(), '缺少范围词: ' + card.find('.stat-label').text())
        .toMatch(/本页|当前筛选|全局|进程启动以来/)
    }

    // ② 口径校验：计数器类 KPI 是 Prometheus Counter（进程启动以来累计），
    //    设备/节点在线数是全表 COUNT（全局）。范围词必须与之一致，否则是"标注了但标错"。
    const labelOf = (text: string) => cards.find(c => c.text().includes(text))!.find('.stat-label').text()
    expect(labelOf('HTTP 请求总数')).toContain('进程启动以来')
    expect(labelOf('数据点采集总数')).toContain('进程启动以来')
    expect(labelOf('设备在线状态')).toContain('全局')
    expect(labelOf('节点在线状态')).toContain('全局')
  })

  it('范围词不与数值/标签重叠：是标签内的独立元素而非拼接字符串', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()

    // 范围词必须是可单独取样式与断行的元素（窄屏要换行，§4.2.5 防逐字竖排），
    // 因此不能只把文字塞进标签字符串里。
    const scopes = wrapper.findAll('.stat-scope')
    expect(scopes).toHaveLength(4)
    for (const s of scopes) {
      expect(s.text().length).toBeGreaterThan(0)
      // 范围词不能为空标签（空 chip 是"渲染失败"的观感）
      expect(s.text().trim()).not.toBe('')
    }
  })

  it('源码不再用 -- 作未知占位（§3.4.5 统一为 —）', async () => {
    const monitorSource = (await import('../Monitor.vue?raw')).default as string
    expect(monitorSource).not.toMatch(/ref\('--'\)/)
    expect(monitorSource).not.toMatch(/lastUpdateTime\s*=\s*'--'/)
    // 未拉取到指标前，「最后更新」不得伪造成一个具体时刻
    expect(monitorSource).toContain("ref(UNKNOWN)")
  })

  it('restarts polling when the refresh interval changes', async () => {
    const setIntervalSpy = vi.spyOn(globalThis, 'setInterval')
    const wrapper = mount(Monitor, { global: { stubs } })
    const callsAfterMount = setIntervalSpy.mock.calls.length

    await wrapper.find('.el-select').trigger('change')

    expect(setIntervalSpy.mock.calls.length).toBeGreaterThan(callsAfterMount)
    setIntervalSpy.mockRestore()
  })
})
