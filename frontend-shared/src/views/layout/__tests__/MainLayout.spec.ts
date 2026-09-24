import { describe, it, expect, beforeEach, vi } from 'vitest'
import { nextTick } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MainLayout from '@/views/layout/MainLayout.vue'
import layoutSource from '../MainLayout.vue?raw'
import { expectedGroupTitles, expectedNavPaths, expectedNavPathsInOrder } from '../menuModel'
import { collectNavPaths, collectVisibleNavPaths, expandAllSubMenus, hasSubMenu } from '../menuDom'

// theme.css 通过同目录原始文本副本验证；happy-dom/Vitest 的 CSS ?raw 在该配置下为空。

// ── Mocks ──────────────────────────────────────────────

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
  useUIStore: () => ({
    sidebarCollapsed: false,
    toggleSidebar: vi.fn(),
  }),
}))

vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    connected: false,
    connect: vi.fn(),
    disconnect: vi.fn(),
    isAuthenticated: true,
    // 会话失效面（B1/B2/B3）：徽标三态与换连接逻辑依赖它
    sessionInvalidated: false,
    isCurrentTokenInvalidated: () => false,
    authInvalidationNonce: 0,
    reconnectWithFreshToken: vi.fn(),
    subscribe: vi.fn(() => vi.fn()),
    onConnected: vi.fn(() => vi.fn()),
  }),
}))

vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: vi.fn(() => Promise.resolve()),
  }),
}))

vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({
    fetchList: vi.fn(() => Promise.resolve()),
  }),
}))

vi.mock('@/api/notification', () => ({
  getNotifications: vi.fn(() => Promise.resolve([])),
  getUnreadCount: vi.fn(() => Promise.resolve(0)),
  markAsRead: vi.fn(() => Promise.resolve()),
  markAllAsRead: vi.fn(() => Promise.resolve()),
}))

vi.mock('@/router/routeLoaders', () => ({
  preloadPrimaryRoutes: vi.fn(() => Promise.resolve([])),
}))

vi.mock('@/utils/feedback', () => ({
  default: {
    success: vi.fn(),
    info: vi.fn(),
    warning: vi.fn(),
    handleError: vi.fn(),
    confirmDanger: vi.fn(() => Promise.resolve(true)),
  },
}))

vi.mock('@/utils/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

// Mock element-plus ElMessage
vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))

// Mock useResponsive composable
vi.mock('@/composables/useResponsive', () => ({
  useResponsive: () => ({
    width: { value: 375 },
    isMobile: { value: true },
    isTablet: { value: false },
    isDesktop: { value: false },
  }),
}))

// 全局 Element Plus stub 已在 src/test-setup.ts 注册。
// 仅 stub 项目内组件 ThemeSwitch 和 RouterView。
const stubs = {
  ThemeSwitch: { template: '<div data-testid="theme-switch" />' },
  RouterView: { template: '<div class="router-view" />' },
}

// ── Tests ──────────────────────────────────────────────

describe('MainLayout.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    // Simulate mobile viewport
    Object.defineProperty(window, 'innerWidth', {
      writable: true,
      configurable: true,
      value: 375,
    })
    window.dispatchEvent(new Event('resize'))
  })

  it('renders all 14 admin menu items', async () => {
    const wrapper = mount(MainLayout, {
      global: {
        stubs,
        components: {},
      },
    })
    await flushPromises()

    // Phase 0.3 改造：原断言是 `findAll('.el-menu-item')` 恰为 14 且顺序逐项相等。
    // 这把它绑死在**平铺**布局上 —— Phase 2 一旦改成 el-sub-menu 分组，子项在折叠态
    // 不在 DOM，该断言会整批变红且无法区分"真回归"与"布局改了"（审计 H3）。
    // 现改为两步，两者都与布局形态无关：
    //   ① 模型面：导航目标集合 = menuModel 的真值源（覆盖，不看顺序/层级）；
    //   ② DOM 面：每个导航目标都真实渲染出可定位的菜单项（先展开分组再收集）。
    // 保留"多一项/少一项必须红"的牙：集合比较对增删都敏感（见 MenuGateHelpers 自证）。
    // 本 spec 的 useResponsive mock 固定 isMobile=true ⇒ 桌面 `.sidebar`（v-if="!isMobile"）
    // 不渲染，菜单在**移动端抽屉**里（原断言 findAll('.el-menu-item') 数的正是它）。
    // 故这里定位抽屉容器；用 .mobile-sidebar-drawer，缺它时断言会明确失败而非静默数 0。
    const drawerEl = wrapper.find('.mobile-sidebar-drawer').element as HTMLElement
    expandAllSubMenus(drawerEl)
    await nextTick()
    const renderedPaths = collectNavPaths(drawerEl)
    expect(new Set(renderedPaths)).toEqual(new Set(expectedNavPaths()))

    // 平铺布局下仍应保持阅读顺序（分组后此断言不再适用，届时按组顺序另立断言）。
    if (!hasSubMenu(drawerEl)) {
      expect(renderedPaths).toEqual(expectedNavPathsInOrder())
    }
  })

  it('Phase 2.4：菜单按 NAV_GROUPS 分组渲染（5 组），且叶子仍覆盖全部导航目标', async () => {
    // 这条断言存在的前提是 test-setup.ts 注册了 ElSubMenu stub。
    // 没有该 stub 时 `<el-sub-menu>` 被当未知元素、只渲染插槽 ⇒ 分组在测试里不可见
    // （实测过 GROUP_TITLES=0 而叶子=14），门禁会假绿。故这里同时断言组标题数。
    const wrapper = mount(MainLayout, { global: { stubs } })
    await flushPromises()
    await nextTick()

    const drawerEl = wrapper.find('.mobile-sidebar-drawer').element as HTMLElement
    expect(hasSubMenu(drawerEl), '未渲染 el-sub-menu —— 分组丢失或 stub 缺失').toBe(true)

    const titles = Array.from(drawerEl.querySelectorAll('.el-sub-menu__title'))
      .map(t => (t.textContent || '').trim())
    // ⚠️ 期望值**写死为产品契约**（14 项 → 这 5 组），不能引用 expectedGroupTitles()。
    // 理由（实测教训）：初版写 `expect(titles).toEqual(expectedGroupTitles())`，而该函数读的是
    // **同一个 menuModel** ⇒「渲染结果 == 模型」恒成立，把 5 组并成 4 组的变异**照样全绿**
    // （自指断言 = 假绿）。分组数量与命名是产品决策（审计 §4.4 D1），必须由测试钉住。
    expect(titles).toEqual(['总览', '设备与通道', '数据', '自动化与通知', '运维'])
    // 再校验模型与契约一致（防"改了模型没改契约"或反之）
    expect(expectedGroupTitles()).toEqual(titles)

    // 分组只是层级变化，**成员不得变化**（这正是 Phase 0.3 把门禁改成集合比较的意义）
    expect(new Set(collectNavPaths(drawerEl))).toEqual(new Set(expectedNavPaths()))

    // 默认全部展开：折叠会让子项 display:none，而侧栏 roving 序列只含叶子项 ⇒
    // 折叠后键盘无法到达子项。故"默认展开"是可访问性要求，不是审美选择。
    const visible = collectVisibleNavPaths(drawerEl)
    expect(new Set(visible), '分组默认未展开 ⇒ 子项对键盘/可见性断言不可达').toEqual(new Set(expectedNavPaths()))
  })

  it('marks /data as the active menu', async () => {
    mount(MainLayout, {
      global: { stubs },
    })
    await flushPromises()
    // The el-menu stub receives default-active via attrs (not passed through in stub)
    // Verify from source that activeMenu is computed from route.path
    expect(layoutSource).toContain('activeMenu')
    expect(layoutSource).toContain("route.path")
    // route is mocked to /data
    expect(mockRoute.path).toBe('/data')
  })

  it('declares mobile drawer structure and drawer open/close interactions', () => {
    expect(layoutSource).toContain('class="mobile-drawer-body"')
    expect(layoutSource).toContain('class="mobile-logo-area"')
    expect(layoutSource).toContain('class="mobile-logo-text"')
    expect(layoutSource).toContain('class="mobile-sidebar-menu"')
    expect(layoutSource).toContain('class="mobile-sidebar-footer"')
    expect(layoutSource).toContain('class="mobile-version-info"')
    expect(layoutSource).toContain('aria-label="打开导航菜单"')
    expect(layoutSource).toContain('mobileDrawerVisible = true')
  })

  it('closes the drawer and navigates to /dashboard when logo is clicked', async () => {
    const wrapper = mount(MainLayout, {
      global: { stubs },
    })
    await flushPromises()

    // Open the mobile drawer
    const hamburger = wrapper.find('[aria-label="打开导航菜单"]')
    expect(hamburger.exists()).toBe(true)
    await hamburger.trigger('click')
    await flushPromises()

    // Click logo area
    const logoArea = wrapper.find('.mobile-logo-area')
    expect(logoArea.exists()).toBe(true)
    await logoArea.trigger('click')

    // Should navigate to /dashboard
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  it('closes the drawer when a menu item is selected', async () => {
    mount(MainLayout, {
      global: { stubs },
    })
    await flushPromises()

    // Verify from source that @select closes the drawer
    expect(layoutSource).toContain('@select="mobileDrawerVisible = false"')
  })

  it('does not leak dark sidebar styles to the mobile drawer', async () => {
    // Verify from source: desktop styles are scoped to .sidebar
    expect(layoutSource).toContain('.sidebar .logo-text')
    expect(layoutSource).toContain('.sidebar :deep(.el-menu-item)')
    // Mobile drawer uses :global styles with Element Plus tokens
    expect(layoutSource).toContain(':global(.mobile-sidebar-drawer')
    expect(layoutSource).toContain('var(--el-text-color-primary)')
    expect(layoutSource).toContain('var(--el-text-color-regular)')
    expect(layoutSource).toContain('var(--el-color-primary-light-9)')
    // No bare .logo-text or :deep(.el-menu-item) outside .sidebar
    expect(layoutSource).not.toMatch(/^\s*\.logo-text\s*{/m)
    expect(layoutSource).not.toMatch(/^\s*:deep\(\.el-menu-item\)\s*{/m)
  })

  it('includes version footer in the mobile drawer', async () => {
    const wrapper = mount(MainLayout, {
      global: { stubs },
    })
    await flushPromises()

    // Open the mobile drawer
    const hamburger = wrapper.find('[aria-label="打开导航菜单"]')
    await hamburger.trigger('click')
    await flushPromises()

    const footer = wrapper.find('.mobile-sidebar-footer')
    expect(footer.exists()).toBe(true)
    const versionText = footer.find('.mobile-version-info')
    expect(versionText.exists()).toBe(true)
    expect(versionText.text()).toContain('v')
  })

  // ── 主题 token / 深色模式精确覆盖 ──────────────────────

  it('移动端抽屉 body 背景使用 var(--el-bg-color) 而非硬编码白底', () => {
    // 抽屉 body 背景
    expect(layoutSource).toMatch(/\.mobile-sidebar-drawer \.el-drawer__body[^}]*background:\s*var\(--el-bg-color\)/)
    // 不应出现硬编码 #fff / #ffffff / white 作为背景
    expect(layoutSource).not.toMatch(/\.mobile-sidebar-drawer[^}]*background:\s*#fff\b/)
    expect(layoutSource).not.toMatch(/\.mobile-sidebar-drawer[^}]*background:\s*#ffffff\b/)
    expect(layoutSource).not.toMatch(/\.mobile-sidebar-drawer[^}]*background:\s*white\b/)
  })

  it('移动端抽屉菜单背景使用 var(--el-bg-color) 而非硬编码白底', () => {
    expect(layoutSource).toMatch(/\.mobile-sidebar-drawer \.mobile-sidebar-menu[^}]*background:\s*var\(--el-bg-color\)/)
    expect(layoutSource).not.toMatch(/\.mobile-sidebar-drawer \.mobile-sidebar-menu[^}]*background:\s*#fff\b/)
  })

  it('移动端抽屉所有颜色均使用 Element Plus CSS 变量 token', () => {
    // logo 文字 → var(--el-text-color-primary)
    expect(layoutSource).toMatch(/\.mobile-logo-text[^}]*color:\s*var\(--el-text-color-primary\)/)
    // 普通菜单项 → var(--el-text-color-regular)
    expect(layoutSource).toMatch(/\.mobile-sidebar-drawer \.el-menu-item[^}]*color:\s*var\(--el-text-color-regular\)/)
    // hover → var(--el-fill-color-light)
    expect(layoutSource).toMatch(/\.el-menu-item:hover[^}]*background:\s*var\(--el-fill-color-light\)/)
    // hover 文字 → var(--el-text-color-primary)
    expect(layoutSource).toMatch(/\.el-menu-item:hover[^}]*color:\s*var\(--el-text-color-primary\)/)
    // active → var(--el-color-primary) + var(--el-color-primary-light-9)
    expect(layoutSource).toMatch(/\.el-menu-item\.is-active[^}]*color:\s*var\(--el-color-primary\)/)
    expect(layoutSource).toMatch(/\.el-menu-item\.is-active[^}]*background:\s*var\(--el-color-primary-light-9\)/)
    // footer border → var(--el-border-color-lighter)
    expect(layoutSource).toMatch(/\.mobile-sidebar-footer[^}]*border-top:.*var\(--el-border-color-lighter\)/)
    // logo area border → var(--el-border-color-lighter)
    expect(layoutSource).toMatch(/\.mobile-logo-area[^}]*border-bottom:.*var\(--el-border-color-lighter\)/)
    // version info → var(--el-text-color-placeholder)
    expect(layoutSource).toMatch(/\.mobile-version-info[^}]*color:\s*var\(--el-text-color-placeholder\)/)
  })

  it('桌面端深色侧栏样式全部限定在 .sidebar 选择器内', () => {
    // logo-text 白色仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.logo-text[^}]*color:\s*#fff/)
    expect(layoutSource).not.toMatch(/^\s*\.logo-text\s*\{[^}]*color:\s*#fff/m)
    // el-menu-item 深色仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar :deep\(\.el-menu-item\)/)
    expect(layoutSource).not.toMatch(/^\s*:deep\(\.el-menu-item\)\s*\{/m)
    // sidebar-menu 仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.sidebar-menu/)
    expect(layoutSource).not.toMatch(/^\s*\.sidebar-menu\s*\{/m)
    // sidebar-footer 仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.sidebar-footer/)
    expect(layoutSource).not.toMatch(/^\s*\.sidebar-footer\s*\{/m)
    // version-info 仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.version-info/)
    expect(layoutSource).not.toMatch(/^\s*\.version-info\s*\{/m)
    // logo-area 仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.logo-area/)
    expect(layoutSource).not.toMatch(/^\s*\.logo-area\s*\{/m)
    // logo-icon 仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.logo-icon/)
    expect(layoutSource).not.toMatch(/^\s*\.logo-icon\s*\{/m)
  })

  // ── 通知跳转路由 (方案 v3.3 §六 D-2: 按 Notification.source 结构化路由) ──

  const mountWithNotifications = async (items: any[]) => {
    const { getNotifications } = await import('@/api/notification')
    vi.mocked(getNotifications).mockResolvedValue(items as any)
    const wrapper = mount(MainLayout, { global: { stubs } })
    await flushPromises()
    return wrapper
  }

  it('merge_failed 通知跳转逻辑设备管理页', async () => {
    const wrapper = await mountWithNotifications([
      { id: 1, type: 'error', title: '数据合并失败（已放弃重试）', description: '', source: 'merge_failed', source_id: '42', read: false, created_at: '' },
    ])
    const item = wrapper.find('.notification-item')
    expect(item.exists()).toBe(true)
    await item.trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/logical-device')
  })

  it('retention_expiring 通知带 retention 深链跳转', async () => {
    const wrapper = await mountWithNotifications([
      { id: 2, type: 'warning', title: '数据即将到期', description: '', source: 'retention_expiring', source_id: '7', read: false, created_at: '' },
    ])
    await wrapper.find('.notification-item').trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/logical-device?retention=7')
  })

  it('离线通知保留原 title 字符串匹配行为', async () => {
    const wrapper = await mountWithNotifications([
      { id: 3, type: 'warning', title: '节点离线', description: '', source: 'node_offline', source_id: '', read: false, created_at: '' },
    ])
    await wrapper.find('.notification-item').trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/edge-device')
  })

  it('source 优先于 title 匹配 (source=merge_failed 但 title 含"离线")', async () => {
    const wrapper = await mountWithNotifications([
      { id: 4, type: 'error', title: '离线设备合并搬迁失败', description: '', source: 'merge_failed', source_id: '9', read: false, created_at: '' },
    ])
    await wrapper.find('.notification-item').trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/logical-device')
    expect(mockPush).not.toHaveBeenCalledWith('/edge-device')
  })

  // ── 分组菜单在深色侧栏上的样式契约（防回退围栏）────────────────────────
  //
  // 背景：Phase 2.4 把 14 项平铺菜单改成 5 组 `el-sub-menu`，但样式只写了
  // `.el-menu-item`。EP 会在每个分组内**再插一层 `<ul class="el-menu el-menu--inline">`**，
  // 那一层没人覆盖 ⇒ 沿用 EP 默认**白底**。
  // 实测（Chromium 计算值）：5 个分组的 `.el-menu--inline` 全是 `rgb(255,255,255)`，
  // 分组标题 `.el-sub-menu__title` 是继承来的 `rgb(48,49,51)`（近黑）——
  // 深色侧栏上出现 5 块白底 + 5 个几乎看不见的标题（用户截图复现）。
  //
  // 这里用源码契约测试把它钉住：**任何人删掉透明化规则，本用例当场红**。
  // 不用挂载断言的原因：happy-dom 不加载 EP 真实 CSS，计算值拿不到白底，
  // 那样写出来的断言测不到真问题（假绿）。
  describe('分组菜单样式契约（防白底/防标题不可见回退）', () => {
    it('桌面与移动都必须把 el-sub-menu 内层容器置为透明', () => {
      // 桌面：.sidebar 作用域
      expect(
        /\.sidebar\s+:deep\(\.el-sub-menu\),[\s\S]{0,200}?background:\s*transparent/.test(layoutSource),
        '桌面侧栏缺少「el-sub-menu / __title / --inline 透明」规则 —— 分组会重新出现白底',
      ).toBe(true)
      // 移动：.mobile-sidebar-drawer 作用域（浅色抽屉，同样需要透明）
      expect(
        /\.mobile-sidebar-drawer\s+\.el-sub-menu\),[\s\S]{0,200}?background:\s*transparent/.test(layoutSource),
        '移动抽屉缺少同款透明规则',
      ).toBe(true)
    })

    it('分组标题必须显式配色（不得依赖继承的近黑默认值）', () => {
      // 桌面：深色底上用白色系变量
      expect(layoutSource).toMatch(/\.sidebar\s+:deep\(\.el-sub-menu__title\)\s*\{[\s\S]{0,300}?color:\s*var\(--sidebar-group-title/)
      // 移动：浅色底上用次级文字色
      expect(layoutSource).toMatch(/\.mobile-sidebar-drawer\s+\.el-sub-menu__title\)\s*\{[\s\S]{0,300}?color:\s*var\(--el-text-color-secondary\)/)
    })

    it('分组内子项必须缩进（与分组标题分层）', () => {
      // 注意选择器被 :deep(...) 包裹，故不能要求 `.el-menu-item` 后紧跟 `{`
      expect(layoutSource).toMatch(/\.sidebar\s+:deep\(\.el-sub-menu\s+\.el-menu-item\)[\s\S]{0,120}?padding-left:\s*36px/)
      expect(layoutSource).toMatch(/\.mobile-sidebar-drawer\s+\.el-sub-menu\s+\.el-menu-item\)[\s\S]{0,120}?padding-left:\s*36px/)
    })

    it('反证：不得只剩 .el-menu-item 规则（那就是本次缺陷的原始形态）', () => {
      // 若 el-sub-menu 三件套规则被整体删除，上面几条会红；
      // 这条额外确认「两个作用域都覆盖了」，防止只修桌面漏移动（或反之）。
      const subMenuRules = layoutSource.match(/el-sub-menu__title\)\s*\{/g) ?? []
      expect(subMenuRules.length, 'el-sub-menu__title 规则应至少覆盖桌面+移动两处').toBeGreaterThanOrEqual(2)
    })
  })
})
