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

  it('进度条颜色取自主题 token，暗色下与亮色不同（回归：静态亮色常量）', async () => {
    document.documentElement.classList.remove('dark')
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    const lightColors = renderedProgressColors(wrapper)
    // 四个进度条：设备在线/离线、节点在线/离线
    expect(lightColors).toEqual(['#67c23a', '#f56c6c', '#67c23a', '#f56c6c'])

    // 切换到暗色（与 stores/theme.ts 一致：html.dark + data-theme）
    document.documentElement.classList.add('dark')
    document.documentElement.setAttribute('data-theme', 'dark')
    // 等待 MutationObserver 回调 + Vue 重新渲染，不依赖固定 sleep
    await vi.waitFor(() => {
      expect(renderedProgressColors(wrapper)).toEqual(['#85ce61', '#f78989', '#85ce61', '#f78989'])
    })

    const darkColors = renderedProgressColors(wrapper)
    expect(darkColors).not.toEqual(lightColors)
    wrapper.unmount()
  })

  it('主题在挂载后切回亮色时颜色跟随恢复', async () => {
    document.documentElement.classList.add('dark')
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(renderedProgressColors(wrapper)).toEqual(['#85ce61', '#f78989', '#85ce61', '#f78989'])

    document.documentElement.classList.remove('dark')
    await vi.waitFor(() => {
      expect(renderedProgressColors(wrapper)).toEqual(['#67c23a', '#f56c6c', '#67c23a', '#f56c6c'])
    })
    wrapper.unmount()
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
