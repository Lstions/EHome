import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import NotFound from '../NotFound.vue'

// Mock vue-router
const mockPush = vi.fn()
const mockBack = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush, back: mockBack }),
}))

// Mock icons
vi.mock('@element-plus/icons-vue', () => ({
  HomeFilled: { name: 'HomeFilled', template: '<i />' },
  Back: { name: 'Back', template: '<i />' },
}))

// 全局 Element Plus stub 已在 src/test-setup.ts 注册。
// ErrorPageLayout 是项目内组件，stub 以隔离。
const stubs = {
  ErrorPageLayout: {
    props: ['code', 'title', 'gradient', 'maxWidth', 'description'],
    template: `<div class="error-page">
      <div class="error-code">{{ code }}</div>
      <div class="error-title">{{ title }}</div>
      <div class="error-description">{{ description }}</div>
      <div class="error-actions"><slot name="actions" /></div>
    </div>`,
  },
}

/** 写入 vue-router 在 push/replace 时维护的 history.state（dist/buildState 同构）。 */
function setRouterHistoryState(state: unknown) {
  window.history.replaceState(state as never, '')
}

describe('NotFound.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setRouterHistoryState(null)
  })

  afterEach(() => {
    setRouterHistoryState(null)
  })

  async function mountPage() {
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    return wrapper
  }

  async function clickBack(wrapper: Awaited<ReturnType<typeof mountPage>>) {
    const buttons = wrapper.findAll('.el-button')
    const backBtn = buttons.find(b => b.text().includes('返回上页'))
    expect(backBtn).toBeTruthy()
    await backBtn!.trigger('click')
  }

  it('renders the not-found error page', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('.error-page').exists()).toBe(true)
  })

  it('displays 404 error code', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('.error-code').text()).toBe('404')
  })

  it('displays "页面不存在" title', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('.error-title').text()).toBe('页面不存在')
  })

  it('goHome navigates to dashboard', async () => {
    const wrapper = await mountPage()
    // <script setup> 不暴露 vm.goHome，通过 DOM 交互触发
    const buttons = wrapper.findAll('.el-button')
    const homeBtn = buttons.find(b => b.text().includes('返回首页'))
    expect(homeBtn).toBeTruthy()
    await homeBtn!.trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  it('goBack calls router.back when vue-router recorded an in-app previous page', async () => {
    // 应用内跳转到 404：history.state 由 vue-router 写入，back 指向上一条应用内 URL
    setRouterHistoryState({ back: '/dashboard', current: '/not-exist', forward: null, position: 2, replaced: false, scroll: null })
    const wrapper = await mountPage()
    await clickBack(wrapper)
    expect(mockBack).toHaveBeenCalledTimes(1)
    expect(mockPush).not.toHaveBeenCalled()
  })

  /**
   * 回归：新标签页直链一个不存在的路由后浏览器 history.length === 2，
   * 旧的 `window.history.length > 1` 判据为真，back() 会落到 about:blank。
   */
  it('goBack navigates to dashboard (never about:blank) when the entry is the first app page', async () => {
    // 首次进入应用时 vue-router 写入的 state：back 为 null
    setRouterHistoryState({ back: null, current: '/not-exist', forward: null, position: 1, replaced: true, scroll: null })
    const wrapper = await mountPage()
    await clickBack(wrapper)
    expect(mockBack).not.toHaveBeenCalled()
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  it('goBack navigates to dashboard when history.state carries no in-app marker', async () => {
    // 无 state（例如被浏览器降级为 location.assign 的情况）同样不能 back()
    setRouterHistoryState(null)
    const wrapper = await mountPage()
    await clickBack(wrapper)
    expect(mockBack).not.toHaveBeenCalled()
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  it('goBack navigates to dashboard when the previous entry belongs to another origin (back: null + history.length > 1)', async () => {
    // about:blank / 外部来源进入的等价 state：back 明确为 null
    setRouterHistoryState({ back: null, current: '/whatever', forward: null, position: 1, replaced: true, scroll: null })
    window.history.pushState({ external: true }, '')
    const wrapper = await mountPage()
    await clickBack(wrapper)
    expect(mockBack).not.toHaveBeenCalled()
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })
})
