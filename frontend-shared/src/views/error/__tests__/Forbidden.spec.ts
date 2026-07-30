import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Forbidden from '../Forbidden.vue'

// Mock vue-router
const mockPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
}))

// Mock user store
vi.mock('@/stores/user', () => ({
  useUserStore: () => ({
    userInfo: { id: 2, username: 'viewer1', role: 'viewer' },
    logout: vi.fn(() => Promise.resolve()),
  }),
}))

// Mock icons
vi.mock('@element-plus/icons-vue', () => ({
  HomeFilled: { name: 'HomeFilled', template: '<i />' },
  SwitchButton: { name: 'SwitchButton', template: '<i />' },
}))

// 全局 Element Plus stub 已在 src/test-setup.ts 注册。
// ErrorPageLayout 是项目内组件，需要 stub 以隔离测试。
const stubs = {
  ErrorPageLayout: {
    props: ['code', 'title', 'gradient', 'maxWidth'],
    template: `<div class="error-page">
      <div class="error-code">{{ code }}</div>
      <div class="error-title">{{ title }}</div>
      <div class="error-description"><slot name="description" /></div>
      <div class="error-actions"><slot name="actions" /></div>
    </div>`,
  },
}

describe('Forbidden.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders the forbidden error page', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-page').exists()).toBe(true)
  })

  it('displays 403 error code', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-code').text()).toBe('403')
  })

  it('displays "无权访问" title', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-title').text()).toBe('无权访问')
  })

  it('computes username from store', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    // <script setup> 不暴露 vm 属性，通过渲染文本验证 username
    expect(wrapper.text()).toContain('viewer1')
  })

  it('displays the fixed system administrator identity', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('系统管理员')
  })

  it('goHome navigates to dashboard', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    // 通过 DOM 交互触发 goHome，而非 wrapper.vm.goHome()
    // "返回首页" 按钮绑定了 @click="goHome"
    const buttons = wrapper.findAll('.el-button')
    const homeBtn = buttons.find(b => b.text().includes('返回首页'))
    expect(homeBtn).toBeTruthy()
    await homeBtn!.trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })
})
