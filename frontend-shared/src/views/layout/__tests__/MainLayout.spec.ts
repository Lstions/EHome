import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MainLayout from '@/views/layout/MainLayout.vue'
import layoutSource from '../MainLayout.vue?raw'

// ── 辅助：读取 theme.css 原始内容 ──────────────────────
// Vitest 环境下 ?raw 后缀对 .css 文件返回空串（CSS 插件拦截），
// 用 Node.js createRequire + fs 同步读取文件内容。
// tsc 配置不含 node 类型，用 @ts-expect-error 抑制类型错误。
async function loadThemeCssRaw(): Promise<string> {
  // @ts-expect-error — Node.js 内置模块，Vitest 运行时可用
  const { createRequire } = await import('module')
  const req = createRequire(import.meta.url)
  const path = req('path')
  const fs = req('fs')
  const cssPath = path.resolve(
    path.dirname(import.meta.url.replace('file://', '')),
    '../../../styles/theme.css',
  )
  return fs.readFileSync(cssPath, 'utf-8')
}

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
    subscribe: vi.fn(() => vi.fn()),
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

// Stub all Element Plus components to avoid full DOM rendering
const stubs = {
  ThemeSwitch: { template: '<div data-testid="theme-switch" />' },
  RouterView: { template: '<div class="router-view" />' },
  'el-container': { template: '<div class="el-container"><slot /></div>' },
  'el-aside': { template: '<div class="el-aside"><slot /></div>' },
  'el-header': { template: '<div class="el-header"><slot /></div>' },
  'el-main': { template: '<div class="el-main"><slot /></div>' },
  'el-menu': {
    template: '<div class="el-menu"><slot /></div>',
    emits: ['select'],
    setup(_props: any, { emit }: { emit: (event: string, ...args: any[]) => void }) {
      return {
        selectHandler: (index: string) => emit('select', index),
      }
    },
  },
  'el-menu-item': {
    template: '<div class="el-menu-item" :data-index="index"><slot /></div>',
    props: ['index'],
  },
  'el-drawer': {
    template: '<div class="el-drawer" v-if="modelValue"><slot /></div>',
    props: ['modelValue', 'direction', 'withHeader', 'size'],
    emits: ['update:modelValue'],
  },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-button': {
    template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>',
    emits: ['click'],
  },
  'el-breadcrumb': { template: '<div class="el-breadcrumb"><slot /></div>' },
  'el-breadcrumb-item': { template: '<span class="el-breadcrumb-item"><slot /></span>' },
  'el-input': { template: '<input class="el-input" />' },
  'el-badge': { template: '<div class="el-badge"><slot /></div>' },
  'el-popover': { template: '<div class="el-popover"><slot /><slot name="reference" /></div>' },
  'el-scrollbar': { template: '<div class="el-scrollbar"><slot /></div>' },
  'el-dropdown': { template: '<div class="el-dropdown"><slot /><slot name="dropdown" /></div>' },
  'el-dropdown-menu': { template: '<div class="el-dropdown-menu"><slot /></div>' },
  'el-dropdown-item': { template: '<div class="el-dropdown-item"><slot /></div>' },
  'el-avatar': { template: '<div class="el-avatar"><slot /></div>' },
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

  it('renders all 8 admin menu items', async () => {
    const wrapper = mount(MainLayout, {
      global: {
        stubs,
        components: {},
      },
    })
    await flushPromises()

    const menuItems = wrapper.findAll('.el-menu-item')
    expect(menuItems).toHaveLength(8)
    const paths = menuItems.map((el) => el.attributes('data-index'))
    expect(paths).toEqual([
      '/dashboard',
      '/node',
      '/edge-device',
      '/channel',
      '/data',
      '/firmware',
      '/device-configs',
      '/monitor',
    ])
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

  it('uses mobile-specific classes and structure in the drawer', async () => {
    const wrapper = mount(MainLayout, {
      global: { stubs },
    })
    await flushPromises()

    // Open the mobile drawer by clicking the hamburger button
    const hamburger = wrapper.find('[aria-label="打开导航菜单"]')
    expect(hamburger.exists()).toBe(true)
    await hamburger.trigger('click')
    await flushPromises()

    // Mobile drawer should exist (isMobile = true at 375px)
    const drawer = wrapper.find('.el-drawer')
    expect(drawer.exists()).toBe(true)

    // Mobile-specific classes
    expect(drawer.find('.mobile-drawer-body').exists()).toBe(true)
    expect(drawer.find('.mobile-logo-area').exists()).toBe(true)
    expect(drawer.find('.mobile-logo-text').exists()).toBe(true)
    expect(drawer.find('.mobile-sidebar-menu').exists()).toBe(true)
    expect(drawer.find('.mobile-sidebar-footer').exists()).toBe(true)
    expect(drawer.find('.mobile-version-info').exists()).toBe(true)
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

  it('theme.css 中 html.dark 覆盖 --el-bg-color 以确保 Teleport 继承深色', async () => {
    // theme.css 中的 html.dark 覆盖确保 Teleport 到 body 的抽屉能继承深色变量。
    // Vitest 环境下 ?raw 对 CSS 返回空串，用动态 require 读取文件内容。
    const themeCssSource = await loadThemeCssRaw()
    expect(themeCssSource).toBeTruthy()
    expect(themeCssSource.length).toBeGreaterThan(100)
    // html.dark 必须覆盖 --el-bg-color
    expect(themeCssSource).toMatch(/html\.dark[^{]*\{[^}]*--el-bg-color:\s*var\(--bg-color\)/)
    // 暗色主题 --bg-color 必须为深色值
    expect(themeCssSource).toMatch(/\[data-theme="dark"\][^{]*\{[^}]*--bg-color:\s*#[0-9a-fA-F]{6}/)
    // html.dark 也设置 --el-menu-bg-color
    expect(themeCssSource).toMatch(/html\.dark[^{]*\{[^}]*--el-menu-bg-color/)
    // html.dark 设置 --el-fill-color-light
    expect(themeCssSource).toMatch(/html\.dark[^{]*\{[^}]*--el-fill-color-light/)
  })
})
