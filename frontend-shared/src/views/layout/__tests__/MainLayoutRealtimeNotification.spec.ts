import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises, enableAutoUnmount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import { WS_EVENT } from '@/events/events'

// ── 负债 D-3 回归: 前端必须消费后端 WS 通知事件 ───────────────
// 覆盖:
//   ① 订阅 WS_EVENT.NOTIFICATION —— 收到即插入列表头部 + 未读数 +1
//   ② 按 id 去重 (重复推送 / 与 REST 拉取重叠)
//   ③ 列表上限 20 条
//   ④ automation 3 个自定义事件被消费且给出用户可见反馈
//   ⑤ WS 重连成功 + visibilitychange(visible) 触发刷新 (非轮询)
//   ⑥ markAsRead / markAllAsRead 后本地列表与计数同步

const { mockPush, mockRoute } = vi.hoisted(() => ({
  mockPush: vi.fn(),
  mockRoute: { path: '/data' },
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => mockRoute,
}))

vi.mock('@/stores/user', () => ({
  useUserStore: () => ({
    userInfo: { id: 1, username: 'admin', email: 'admin@test.com' },
    logout: vi.fn(() => Promise.resolve()),
  }),
}))

vi.mock('@/stores/ui', () => ({
  useUIStore: () => ({ sidebarCollapsed: false, toggleSidebar: vi.fn() }),
}))

vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({ fetchNodes: vi.fn(() => Promise.resolve()) }),
}))

vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({ fetchList: vi.fn(() => Promise.resolve()) }),
}))

vi.mock('@/router/routeLoaders', () => ({
  preloadPrimaryRoutes: vi.fn(() => Promise.resolve([])),
}))

const { isMobileRef } = vi.hoisted(() => {
  const { ref } = require('vue')
  return { isMobileRef: ref(false) }
})
vi.mock('@/composables/useResponsive', () => ({
  useResponsive: () => ({
    width: { value: 1440 },
    isMobile: isMobileRef,
    isTablet: { value: false },
    isDesktop: { value: true },
  }),
}))

vi.mock('@/utils/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

// feedback mock —— 断言 automation 事件的用户可见反馈
const { mockFeedbackWarning, mockFeedbackError, mockFeedbackSuccess, mockFeedbackInfo, mockHandleError } = vi.hoisted(() => ({
  mockFeedbackWarning: vi.fn(),
  mockFeedbackError: vi.fn(),
  mockFeedbackSuccess: vi.fn(),
  mockFeedbackInfo: vi.fn(),
  mockHandleError: vi.fn(),
}))
vi.mock('@/utils/feedback', () => ({
  default: {
    success: mockFeedbackSuccess,
    error: mockFeedbackError,
    warning: mockFeedbackWarning,
    info: mockFeedbackInfo,
    handleError: mockHandleError,
    confirmDanger: vi.fn(() => Promise.resolve(true)),
  },
}))

// ElNotification mock —— 断言 pending_confirm 的提示 + 点击跳转
const { mockNotification, mockElMessage } = vi.hoisted(() => ({
  mockNotification: vi.fn(),
  mockElMessage: vi.fn(),
}))
vi.mock('element-plus', () => ({
  ElMessage: Object.assign(mockElMessage, {
    success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn(),
  }),
  ElNotification: mockNotification,
}))

// WebSocket store mock —— 捕获 subscribe 的 (event, handler) 与 onConnected 回调，
// 测试里手动触发，模拟后端推送 / 重连成功。
const { wsHandlers, wsConnectedHandlers, wsSubscribe, wsOnConnected } = vi.hoisted(() => {
  const wsHandlers = new Map<string, (m: any) => void>()
  const wsConnectedHandlers = new Set<() => void>()
  return {
    wsHandlers,
    wsConnectedHandlers,
    wsSubscribe: vi.fn((type: string, handler: (m: any) => void) => {
      wsHandlers.set(type, handler)
      return () => wsHandlers.delete(type)
    }),
    wsOnConnected: vi.fn((handler: () => void) => {
      wsConnectedHandlers.add(handler)
      return () => wsConnectedHandlers.delete(handler)
    }),
  }
})
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    connected: true,
    isAuthenticated: true,
    // 会话失效面（B1/B2/B3）：徽标三态与换连接逻辑依赖它
    sessionInvalidated: false,
    isCurrentTokenInvalidated: () => false,
    authInvalidationNonce: 0,
    reconnectWithFreshToken: vi.fn(),
    connect: vi.fn(),
    disconnect: vi.fn(),
    subscribe: wsSubscribe,
    onConnected: wsOnConnected,
  }),
}))

// notification API mock
//
// 为什么必须显式标注泛型：vi.hoisted 的工厂在类型层面无法从 `() => Promise.resolve([])`
// 推断出元素类型，会把它推成 `never[]`，于是后续 `mockResolvedValue([makeNotification(...)])`
// 全部报 TS2322/TS2345（"not assignable to type 'never'"）。
// 这里用内联结构类型（不是 `any`）把返回形状钉死，与 api/notification.ts 的 Notification 同形。
type MockNotification = {
  id: number
  type: string
  title: string
  description: string
  message: string
  source: string
  source_id: string
  read: boolean
  created_at: string
}
const { mockGetNotifications, mockGetUnreadCount, mockMarkAsRead, mockMarkAllAsRead } = vi.hoisted(() => ({
  mockGetNotifications: vi.fn<() => Promise<MockNotification[]>>(() => Promise.resolve([])),
  mockGetUnreadCount: vi.fn<() => Promise<number>>(() => Promise.resolve(0)),
  mockMarkAsRead: vi.fn<(id: number) => Promise<void>>(() => Promise.resolve()),
  mockMarkAllAsRead: vi.fn<() => Promise<void>>(() => Promise.resolve()),
}))
vi.mock('@/api/notification', () => ({
  getNotifications: mockGetNotifications,
  getUnreadCount: mockGetUnreadCount,
  markAsRead: mockMarkAsRead,
  markAllAsRead: mockMarkAllAsRead,
}))

import MainLayout from '@/views/layout/MainLayout.vue'

// 需要覆盖 test-setup 的通用 stub:
//  - ElPopover: 通用 stub 只渲染 default slot, 铃铛在 #reference 里 → 必须显式渲染
//  - ElBadge:   通用 stub 丢弃 props, 未读数断言需要 value 属性
//  - ElPopover 的 popper 内容 (notification-panel) 通过 default slot 渲染
const stubs = {
  ThemeSwitch: { template: '<div data-testid="theme-switch" />' },
  RouterView: { template: '<div class="router-view" />' },
  ElPopover: { template: '<div class="el-popover"><slot name="reference" /><slot /></div>' },
  ElBadge: {
    props: { value: [String, Number], hidden: Boolean },
    template: '<span class="el-badge" :value="hidden ? undefined : String(value)"><slot /></span>',
  },
}

const makeNotification = (over: Record<string, any> = {}) => ({
  id: 101,
  type: 'warning',
  title: '告警: 测试规则',
  description: '测试规则 temperature 超过阈值 50.00 (当前 60.00)',
  message: '测试规则 temperature 超过阈值 50.00 (当前 60.00)',
  source: 'alert_rule',
  source_id: '7',
  read: false,
  created_at: '2026-09-13T12:00:00+08:00',
  ...over,
})

// 推送回调：handler(message) —— 后端广播形状 {type, payload:{通知实体}}
const push = (event: string, payload: any) => {
  const handler = wsHandlers.get(event)
  if (!handler) throw new Error(`no subscriber for ${event}`)
  handler({ type: event, payload })
}

const mountLayout = async () => {
  const wrapper = mount(MainLayout, { global: { stubs } })
  await flushPromises()
  return wrapper
}

// 未读计数断言：ElBadge stub 在 hidden (count===0) 时不渲染 value 属性。
const unreadCount = (wrapper: any) =>
  wrapper.find('.notification-badge').attributes('value')

// 每个用例挂载的组件在 afterEach 自动卸载: 否则 WS 订阅与 visibilitychange
// 监听会跨用例累积, 让"触发次数"断言不确定。
enableAutoUnmount(afterEach)

describe('MainLayout 实时通知 (负债 D-3)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    wsHandlers.clear()
    wsConnectedHandlers.clear()
    mockGetNotifications.mockResolvedValue([])
    mockGetUnreadCount.mockResolvedValue(0)
  })

  it('订阅 WS_EVENT.NOTIFICATION (不是只靠 mounted 拉一次)', async () => {
    await mountLayout()
    expect(wsSubscribe).toHaveBeenCalledWith(WS_EVENT.NOTIFICATION, expect.any(Function))
  })

  it('收到通知推送 → 列表头部插入该条 + 未读数 +1', async () => {
    const wrapper = await mountLayout()
    expect(wrapper.findAll('.notification-item')).toHaveLength(0)

    push(WS_EVENT.NOTIFICATION, makeNotification({ id: 500, title: '告警: 水泵' }))
    await nextTick()

    const items = wrapper.findAll('.notification-item')
    expect(items).toHaveLength(1)
    expect(items[0].find('.notification-title').text()).toBe('告警: 水泵')
    // value 属性是未读计数
    expect(unreadCount(wrapper)).toBe('1')

    // 第二条插到头部 (最新在前)
    push(WS_EVENT.NOTIFICATION, makeNotification({ id: 501, title: '告警: 第二' }))
    await nextTick()
    const items2 = wrapper.findAll('.notification-item')
    expect(items2).toHaveLength(2)
    expect(items2[0].find('.notification-title').text()).toBe('告警: 第二')
    expect(items2[1].find('.notification-title').text()).toBe('告警: 水泵')
  })

  it('已读通知 (read=true) 插入列表但不加未读数', async () => {
    const wrapper = await mountLayout()
    push(WS_EVENT.NOTIFICATION, makeNotification({ id: 502, read: true }))
    await nextTick()
    expect(wrapper.findAll('.notification-item')).toHaveLength(1)
    expect(unreadCount(wrapper)).toBeUndefined()
  })

  it('按 id 去重: 重复推送同一条不重复插入、不重复计数', async () => {
    const wrapper = await mountLayout()
    push(WS_EVENT.NOTIFICATION, makeNotification({ id: 503 }))
    await nextTick()
    push(WS_EVENT.NOTIFICATION, makeNotification({ id: 503 }))
    push(WS_EVENT.NOTIFICATION, makeNotification({ id: 503 }))
    await nextTick()

    expect(wrapper.findAll('.notification-item')).toHaveLength(1)
    expect(unreadCount(wrapper)).toBe('1')
  })

  it('与 REST 拉取重叠时按 id 去重 (列表里有该 id 则不再插入)', async () => {
    mockGetNotifications.mockResolvedValue([makeNotification({ id: 504, title: 'REST 已有' })])
    mockGetUnreadCount.mockResolvedValue(1)
    const wrapper = await mountLayout()
    expect(wrapper.findAll('.notification-item')).toHaveLength(1)

    push(WS_EVENT.NOTIFICATION, makeNotification({ id: 504 }))
    await nextTick()
    expect(wrapper.findAll('.notification-item')).toHaveLength(1)
    expect(unreadCount(wrapper)).toBe('1')
  })

  it('列表上限 20 条: 超出后丢弃最旧', async () => {
    const wrapper = await mountLayout()
    for (let i = 0; i < 25; i++) {
      push(WS_EVENT.NOTIFICATION, makeNotification({ id: 600 + i, title: `通知-${i}` }))
    }
    await nextTick()
    const items = wrapper.findAll('.notification-item')
    expect(items).toHaveLength(20)
    // 最新在最前，最旧的 5 条被截断
    expect(items[0].find('.notification-title').text()).toBe('通知-24')
    expect(items[19].find('.notification-title').text()).toBe('通知-5')
  })

  it('缺 id 的推送被忽略 (无法去重/无法标记已读)', async () => {
    const wrapper = await mountLayout()
    push(WS_EVENT.NOTIFICATION, { title: '无 id 的通知' })
    await nextTick()
    expect(wrapper.findAll('.notification-item')).toHaveLength(0)
  })

  it('订阅 automation 三个自定义事件并给出用户可见反馈', async () => {
    await mountLayout()
    expect(wsSubscribe).toHaveBeenCalledWith('automation_pending_confirm', expect.any(Function))
    expect(wsSubscribe).toHaveBeenCalledWith('automation_daily_limit', expect.any(Function))
    expect(wsSubscribe).toHaveBeenCalledWith('automation_system_actor_unavailable', expect.any(Function))
  })

  it('automation_pending_confirm: 插入通知 + 提示等待确认 + 点击跳转自动化页', async () => {
    const wrapper = await mountLayout()
    push('automation_pending_confirm', makeNotification({
      id: 700, title: '策略待确认: S2-低电强制断充', source: 'automation_rule',
      detail: { rule_id: 3, rule_name: 'S2-低电强制断充', event_id: 88 },
    }))
    await nextTick()

    expect(wrapper.findAll('.notification-item')).toHaveLength(1)
    expect(unreadCount(wrapper)).toBe('1')
    expect(mockNotification).toHaveBeenCalledTimes(1)
    const opts = mockNotification.mock.calls[0][0]
    expect(opts.message).toContain('S2-低电强制断充')
    expect(opts.message).toContain('等待人工确认')
    // 点击提示 → 跳转自动化策略页 (待确认事件的处理位置)
    opts.onClick()
    expect(mockPush).toHaveBeenCalledWith('/automation')
  })

  it('automation_daily_limit: 插入通知 + warning 反馈', async () => {
    const wrapper = await mountLayout()
    push('automation_daily_limit', makeNotification({
      id: 701, type: 'warning', title: '策略日熔断: 测试策略',
      detail: { rule_id: 9, rule_name: '测试策略', limit: 5 },
    }))
    await nextTick()
    expect(wrapper.findAll('.notification-item')).toHaveLength(1)
    expect(mockFeedbackWarning).toHaveBeenCalledTimes(1)
    expect(mockFeedbackWarning.mock.calls[0][0]).toContain('测试策略')
    expect(mockFeedbackWarning.mock.calls[0][0]).toContain('日执行上限')
  })

  it('automation_system_actor_unavailable: 插入通知 + error 反馈', async () => {
    const wrapper = await mountLayout()
    push('automation_system_actor_unavailable', makeNotification({
      id: 702, type: 'error', title: '自动化执行已禁用: 系统主体用户不可用',
      detail: { reason: 'system actor not found' },
    }))
    await nextTick()
    expect(wrapper.findAll('.notification-item')).toHaveLength(1)
    expect(mockFeedbackError).toHaveBeenCalledTimes(1)
    expect(mockFeedbackError.mock.calls[0][0]).toContain('系统主体用户不可用')
  })

  it('WS 重连成功 (onConnected) 触发重新拉取通知, 不用定时轮询', async () => {
    await mountLayout()
    expect(wsOnConnected).toHaveBeenCalled()
    mockGetNotifications.mockClear()
    mockGetUnreadCount.mockClear()

    // 模拟重连成功
    wsConnectedHandlers.forEach(fn => fn())
    await flushPromises()

    expect(mockGetNotifications).toHaveBeenCalled()
    expect(mockGetUnreadCount).toHaveBeenCalled()
  })

  it('visibilitychange → visible 触发刷新; hidden 不触发', async () => {
    await mountLayout()
    mockGetNotifications.mockClear()
    mockGetUnreadCount.mockClear()

    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(mockGetNotifications).not.toHaveBeenCalled()

    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(mockGetNotifications).toHaveBeenCalledTimes(1)
    expect(mockGetUnreadCount).toHaveBeenCalledTimes(1)
  })

  it('markAsRead 成功后本地列表该项置已读且未读数 -1', async () => {
    mockGetNotifications.mockResolvedValue([makeNotification({ id: 800 })])
    mockGetUnreadCount.mockResolvedValue(1)
    const wrapper = await mountLayout()
    expect(unreadCount(wrapper)).toBe('1')

    await wrapper.find('.notification-item').trigger('click')
    await flushPromises()

    expect(mockMarkAsRead).toHaveBeenCalledWith(800)
    // 已读样式取消
    expect(wrapper.find('.notification-item').classes()).not.toContain('unread')
    expect(unreadCount(wrapper)).toBeUndefined()

    // 重复点击不把计数减成负数
    await wrapper.find('.notification-item').trigger('click')
    await flushPromises()
    expect(unreadCount(wrapper)).toBeUndefined()
  })

  it('markAllAsRead 成功后本地列表全置已读且计数归零', async () => {
    const unread = [makeNotification({ id: 801 }), makeNotification({ id: 802 })]
    mockGetNotifications.mockResolvedValueOnce(unread)
    mockGetNotifications.mockResolvedValue(unread.map(i => ({ ...i, read: true })))
    mockGetUnreadCount.mockResolvedValueOnce(2)
    mockGetUnreadCount.mockResolvedValue(0)
    const wrapper = await mountLayout()
    expect(unreadCount(wrapper)).toBe('2')

    // 「全部已读」按钮
    const buttons = wrapper.findAll('.notification-header .el-button')
    await buttons[0].trigger('click')
    await flushPromises()

    expect(mockMarkAllAsRead).toHaveBeenCalled()
    expect(mockFeedbackSuccess).toHaveBeenCalled()
    wrapper.findAll('.notification-item').forEach(item => {
      expect(item.classes()).not.toContain('unread')
    })
    expect(unreadCount(wrapper)).toBeUndefined()
  })

  it('markAllAsRead 后即使随后的列表刷新失败, 本地列表与计数也已同步归零', async () => {
    mockGetNotifications.mockResolvedValueOnce([makeNotification({ id: 803 }), makeNotification({ id: 804 })])
    mockGetUnreadCount.mockResolvedValueOnce(2)
    // 刷新失败: 不得把本地已收敛的状态又"回滚"成旧数据
    mockGetNotifications.mockRejectedValue(new Error('network down'))
    mockGetUnreadCount.mockRejectedValue(new Error('network down'))
    const wrapper = await mountLayout()
    expect(unreadCount(wrapper)).toBe('2')

    await wrapper.findAll('.notification-header .el-button')[0].trigger('click')
    await flushPromises()

    // fetchNotifications 失败会清空列表（既有行为），但计数必须已由 markAllAsRead 归零，
    // 且不出现负数或旧值。
    expect(mockMarkAllAsRead).toHaveBeenCalled()
    expect(unreadCount(wrapper)).toBeUndefined()
  })

  it('组件卸载后取消全部订阅 (不泄漏 handler)', async () => {
    const wrapper = await mountLayout()
    expect(wsHandlers.size).toBeGreaterThanOrEqual(4)
    wrapper.unmount()
    expect(wsHandlers.size).toBe(0)
    expect(wsConnectedHandlers.size).toBe(0)
  })
})