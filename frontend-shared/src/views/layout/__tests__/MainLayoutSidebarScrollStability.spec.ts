import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import MainLayout from '@/views/layout/MainLayout.vue'
import layoutSource from '../MainLayout.vue?raw'

/**
 * 侧栏折叠/展开时内容跳动（2026-10-21 用户真机反馈）的合同测试。
 *
 * 缺陷形态（Playwright 逐帧实测，.tmp-probe/round3-sidebar-ab.mjs）：
 *   侧栏 width 走 0.3s 过渡，内容区 flex 自适应 ⇒ 每帧重排；部分页面 scrollHeight 随宽度
 *   变化（inverter@1024: 6637→6451）。浏览器的 scroll anchoring 为保住视口锚点会**改写
 *   scrollTop** ⇒ 折叠后 scrollTop 2075→1928（-147）、3557→3308（-249），
 *   视口内内容整体位移 147~249px —— 用户看到的就是"收缩时内容跳一下"。
 *
 * 修复：在滚动容器本体上**永久**关闭滚动锚定（.main-content{overflow-anchor:none}）。
 *   —— 初版曾在侧栏动画的 420ms 窗口内临时关闭；后由「页面进入后分批渲染」的同类缺陷
 *   复现证明：任何内容高度变化都会触发锚定改写 scrollTop，不只是侧栏动画，故改为永久关闭。
 *   两套实现只保留永久版（单一实现、可被 MainScrollAnchoring.spec 锁住）。
 * 运行时 A/B 对照（同一会话、同一滚动位置，注入 .sidebar-animating{overflow-anchor:auto}
 * 模拟改前行为）：改前 -147/-249px，改后 scrollTop 序列恒为单值、可见锚点位移 0。
 *
 * 注意：本测试是**源码/行为层**护栏；「浏览器里确实不跳」由上述 Playwright 脚本覆盖
 * （happy-dom 不实现 scroll anchoring，无法在这里复现 scrollTop 被改写）。
 */
const themeCss = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

// ── mocks（与 MainLayout.spec 同套，唯一区别：桌面视口，让 .sidebar 与折叠按钮真实渲染）──
const { mockToggleSidebar } = vi.hoisted(() => ({ mockToggleSidebar: vi.fn() }))
const { mockPush, mockRoute } = vi.hoisted(() => ({ mockPush: vi.fn(), mockRoute: { path: '/data' } }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: mockPush }), useRoute: () => mockRoute }))
vi.mock('@/stores/user', () => ({
  useUserStore: () => ({ userInfo: { id: 1, username: 'admin', email: 'admin@test.com' }, logout: vi.fn(() => Promise.resolve()) }),
}))
vi.mock('@/stores/ui', () => ({
  useUIStore: () => ({ sidebarCollapsed: false, toggleSidebar: mockToggleSidebar }),
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    connected: false, connect: vi.fn(), disconnect: vi.fn(), isAuthenticated: true,
    sessionInvalidated: false, isCurrentTokenInvalidated: () => false, authInvalidationNonce: 0,
    reconnectWithFreshToken: vi.fn(), subscribe: vi.fn(() => vi.fn()), onConnected: vi.fn(() => vi.fn()),
  }),
}))
vi.mock('@/stores/node', () => ({ useNodeStore: () => ({ fetchNodes: vi.fn(() => Promise.resolve()) }) }))
vi.mock('@/stores/edgeDevice', () => ({ useEdgeDeviceStore: () => ({ fetchList: vi.fn(() => Promise.resolve()) }) }))
vi.mock('@/api/notification', () => ({
  getNotifications: vi.fn(() => Promise.resolve([])),
  getUnreadCount: vi.fn(() => Promise.resolve(0)),
  markAsRead: vi.fn(() => Promise.resolve()),
  markAllAsRead: vi.fn(() => Promise.resolve()),
}))
vi.mock('@/router/routeLoaders', () => ({ preloadPrimaryRoutes: vi.fn(() => Promise.resolve([])) }))
vi.mock('@/utils/feedback', () => ({
  default: { success: vi.fn(), info: vi.fn(), warning: vi.fn(), handleError: vi.fn(), confirmDanger: vi.fn(() => Promise.resolve(true)) },
}))
vi.mock('@/utils/logger', () => ({ logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() } }))
vi.mock('element-plus', () => ({ ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }) }))
// 注意：模板里是 `v-if="!isMobile"`，而 useResponsive 真实实现返回 **ref/computed**。
// mock 必须返回真实 ref（不能是 {value:false} 这种普通对象）—— 普通对象在模板里恒为真值，
// 会走进移动端分支（桌面 .sidebar / 折叠按钮根本不渲染），断言会指向错误的按钮。
vi.mock('@/composables/useResponsive', async () => {
  const { ref } = await import('vue')
  return {
    useResponsive: () => ({
      width: ref(1440),
      isMobile: ref(false),
      isTablet: ref(false),
      isDesktop: ref(true),
    }),
  }
})

const stubs = {
  ThemeSwitch: { template: '<div data-testid="theme-switch" />' },
  RouterView: { template: '<div class="router-view" />' },
}

function mountDesktop() {
  return mount(MainLayout, { global: { stubs } })
}

describe('侧栏折叠：滚动容器临时关闭滚动锚定（防内容跳动）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('源码契约：滚动容器本体永久关闭 overflow-anchor，且不再有按需抑制态', () => {
    // 1) 模板没有任何「临时抑制态」类绑定（单一实现：只有永久这一条）
    expect(layoutSource).toMatch(/<el-main class="main-content">/)
    expect(layoutSource).not.toContain('sidebar-animating')
    // 2) 规则写在 .main-content 本体上（不误伤表格等内部滚动容器）
    expect(layoutSource).toMatch(/\.main-content \{[^}]*overflow-anchor:\s*none/)
    // 3) 保留滚动能力：不能顺手把 overflow-y 关掉
    expect(layoutSource).toMatch(/\.main-content \{[^}]*overflow-y:\s*auto/)
  })

  it('页面进入后的分批渲染同样受保护（永久关闭覆盖「非侧栏」触发源）', () => {
    // 这一条是「为什么不用临时窗口」的回归守卫：/edge-device/15、/logical-device 等页面
    // 进入后图表/表格分批渲染，视口上方高度变化也会触发锚定改写 scrollTop（实测 +60/+76/+136px）。
    // 若把规则改回「仅在侧栏动画窗口内生效」，本断言立即变红。
    expect(layoutSource).not.toMatch(/SIDEBAR_ANIM_MS/)
    expect(layoutSource).not.toMatch(/\}\n\s*\/\* 侧栏 width 过渡期间/)
  })

  it('挂载级：折叠按钮仍走 store.toggleSidebar，且滚动容器不带任何「临时抑制态」类', async () => {
    const wrapper = mountDesktop()
    await flushPromises()
    const mainEl = wrapper.find('.main-content')
    expect(mainEl.exists(), '.main-content 未渲染').toBe(true)

    await wrapper.find('.collapse-btn').trigger('click')
    expect(mockToggleSidebar, '折叠按钮未调用 store.toggleSidebar').toHaveBeenCalledTimes(1)

    // 锚定是**永久**关闭的（写在 .main-content 本体），不再依赖点击时加类：
    // 这样「页面进入后分批渲染」与「侧栏动画重排」两种触发源都被同一条规则覆盖。
    const css = wrapper.find('.main-content').element
    expect(css.className).not.toContain('sidebar-animating')
    expect(layoutSource).toMatch(/\.main-content \{[^}]*overflow-anchor:\s*none/)
    expect(layoutSource).not.toContain('sidebar-animating')

    wrapper.unmount()
  })
})

describe('分段控件（el-radio-button / el-checkbox-button）文字垂直居中', () => {
  const centeringBlock = (() => {
    const at = themeCss.indexOf('.el-radio-group .el-radio-button__inner')
    if (at < 0) return ''
    const open = themeCss.indexOf('{', at)
    const close = themeCss.indexOf('}', open)
    return themeCss.slice(open + 1, close)
  })()

  it('theme.css 统一声明 inline-flex 居中（display + 两个 center）', () => {
    expect(centeringBlock, 'theme.css 缺少分段控件居中块').not.toBe('')
    expect(centeringBlock).toMatch(/display:\s*inline-flex/)
    expect(centeringBlock).toMatch(/align-items:\s*center/)
    expect(centeringBlock).toMatch(/justify-content:\s*center/)
    // 不得改行高/字号：这段落在 touchTargetsF6.spec 的「尺寸区」，
    // 该门禁禁止借改行高凑高度（裁决：不改视觉密度）。实测 flex 居中已足够（偏移 0px）。
    expect(centeringBlock).not.toMatch(/line-height:/)
    expect(centeringBlock).not.toMatch(/font-size:/)
  })

  it('居中规则必须比 EP 基础规则更具体（0,1,0）—— EP 的 theme-chalk 按组件懒注入且排在本文件之后', () => {
    // 反例守卫：改回同特异性选择器会按顺序被 EP 的 display:inline-block 覆盖（实测静默失效）。
    // 定位规则本体（注释里也提到了选择器文本，故按「选择器后紧跟 {」的模式找）。
    const rulePattern = /\.el-radio-group \.el-radio-button__inner,\s*\n\.el-checkbox-group \.el-checkbox-button__inner \{/
    expect(themeCss, '缺少 (0,2,0) 的居中规则').toMatch(rulePattern)
    // 基础块仍保留 min-height（touchTargetsF6.spec 按字面量锁定该选择器，不得删）
    expect(themeCss).toMatch(/\.el-radio-button__inner,\s*\n\s*\.el-checkbox-button__inner \{[^}]*min-height: 36px/)
  })

  it('不得只抬高度不居中（本次缺陷的成因）：基础块内不得再出现脏 display 覆盖', () => {
    const at = themeCss.indexOf('.el-radio-button__inner,\n.el-checkbox-button__inner')
    expect(at).toBeGreaterThan(0)
    const block = themeCss.slice(at, themeCss.indexOf('}', at))
    expect(block).not.toMatch(/display:\s*inline-block/)
    expect(block).not.toMatch(/line-height:\s*\d+px/)
  })

  it('TimeRangeSelector 自带收口不得与本收口冲突（两处取值一致）', async () => {
    const src = (await import('@/components/charts/TimeRangeSelector.vue?raw')).default as string
    expect(src).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*display:\s*inline-flex/)
    expect(src).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*line-height:\s*1;/)
  })
})