import { describe, it, expect, beforeEach, vi } from 'vitest'
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

describe('NotFound.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders the not-found error page', async () => {
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-page').exists()).toBe(true)
  })

  it('displays 404 error code', async () => {
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-code').text()).toBe('404')
  })

  it('displays "页面不存在" title', async () => {
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-title').text()).toBe('页面不存在')
  })

  it('goHome navigates to dashboard', async () => {
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    // <script setup> 不暴露 vm.goHome，通过 DOM 交互触发
    const buttons = wrapper.findAll('.el-button')
    const homeBtn = buttons.find(b => b.text().includes('返回首页'))
    expect(homeBtn).toBeTruthy()
    await homeBtn!.trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  it('goBack calls router.back when history exists', async () => {
    Object.defineProperty(window, 'history', { value: { length: 2 }, writable: true, configurable: true })
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    const buttons = wrapper.findAll('.el-button')
    const backBtn = buttons.find(b => b.text().includes('返回上页'))
    expect(backBtn).toBeTruthy()
    await backBtn!.trigger('click')
    expect(mockBack).toHaveBeenCalled()
  })

  it('goBack navigates to dashboard when no history', async () => {
    Object.defineProperty(window, 'history', { value: { length: 1 }, writable: true, configurable: true })
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    const buttons = wrapper.findAll('.el-button')
    const backBtn = buttons.find(b => b.text().includes('返回上页'))
    expect(backBtn).toBeTruthy()
    await backBtn!.trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })
})
