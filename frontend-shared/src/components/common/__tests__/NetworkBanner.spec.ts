import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { WarningFilled, CircleCloseFilled, Link } from '@element-plus/icons-vue'
import NetworkBanner from '@/components/common/NetworkBanner.vue'

// 真 bug 回归（2026-09-11 类型门禁落地）：原实现读 wsStore.lastError ——
// websocket store 根本没有该字段，条件恒 falsy，警告图标永不渲染。
// 修复后：浏览器在线但 WebSocket 断开时，banner 显示警告（WarningFilled）图标。

const wsState = vi.hoisted(() => ({
  connected: false,
  isAuthenticated: true,
  connect: vi.fn(),
  onConnected: vi.fn(() => vi.fn()),
}))

vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => wsState,
}))

const stubs = {
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-button': { template: '<button class="el-button"><slot /></button>' },
}

function setOnline(onLine: boolean) {
  Object.defineProperty(window.navigator, 'onLine', { value: onLine, configurable: true })
}

describe('NetworkBanner.vue 图标语义', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    wsState.connected = false
    wsState.isAuthenticated = true
    setOnline(true)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('浏览器在线 + WS 断开 → 定时检查弹出横幅并显示警告图标（lastError 断头字段修复回归）', async () => {
    vi.useFakeTimers()
    const wrapper = mount(NetworkBanner, { global: { stubs } })
    await flushPromises()
    // 组件内部 10s 定时检查：已认证 + WS 断开 → show('正在尝试重新连接...')
    vi.advanceTimersByTime(10_000)
    await flushPromises()
    expect(wrapper.text()).toContain('正在尝试重新连接')
    expect(wrapper.findComponent(WarningFilled).exists()).toBe(true)
    expect(wrapper.findComponent(Link).exists()).toBe(false)
  })

  it('WS 已连接 → 定时检查不弹横幅、不显示警告图标', async () => {
    wsState.connected = true
    vi.useFakeTimers()
    const wrapper = mount(NetworkBanner, { global: { stubs } })
    await flushPromises()
    vi.advanceTimersByTime(10_000)
    await flushPromises()
    expect(wrapper.findComponent(WarningFilled).exists()).toBe(false)
    expect(wrapper.findComponent(Link).exists()).toBe(false) // 横幅未显示，无图标
  })

  it('浏览器离线 → 离线事件横幅显示 CircleCloseFilled 而非警告图标', async () => {
    setOnline(false)
    const wrapper = mount(NetworkBanner, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('请检查您的网络连接')
    expect(wrapper.findComponent(CircleCloseFilled).exists()).toBe(true)
    expect(wrapper.findComponent(WarningFilled).exists()).toBe(false)
  })
})
