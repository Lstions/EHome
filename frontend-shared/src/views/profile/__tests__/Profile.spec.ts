import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Profile from '../Profile.vue'
import profileSource from '../Profile.vue?raw'

// Mock vue-router
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

// Use vi.hoisted for mock functions referenced inside vi.mock factories
const { mockLogout, mockChangePassword } = vi.hoisted(() => ({
  mockLogout: vi.fn(() => Promise.resolve()),
  mockChangePassword: vi.fn(() => Promise.resolve()),
}))

vi.mock('@/stores/user', () => ({
  useUserStore: () => ({
    userInfo: { id: 1, username: 'testadmin', email: 'admin@test.com', role: 'admin' },
    logout: mockLogout,
  }),
}))

vi.mock('@/api/auth', () => ({
  authApi: {
    changePassword: mockChangePassword,
  },
}))

vi.mock('@/utils/feedback', () => ({
  default: {
    success: vi.fn(),
    handleError: vi.fn(),
  },
}))

// Mock useResponsive composable（Profile 组件依赖它）
vi.mock('@/composables/useResponsive', () => ({
  useResponsive: () => ({
    width: { value: 1440 },
    isMobile: { value: false },
    isTablet: { value: false },
    isDesktop: { value: true },
  }),
}))

// Mock icons — 必须包含 Profile 和 PageHeader 两个组件用到的所有 icon
vi.mock('@element-plus/icons-vue', () => ({
  UserFilled: { name: 'UserFilled', template: '<i />' },
  ArrowLeft: { name: 'ArrowLeft', template: '<i />' },
}))

// 全局 Element Plus stub 已在 src/test-setup.ts 注册。
// PageHeader 是项目内组件，让真实组件渲染（依赖已在 mock 中补充）。

describe('Profile.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders the profile-page container', async () => {
    const wrapper = mount(Profile)
    await flushPromises()
    expect(wrapper.find('.profile-page').exists()).toBe(true)
  })

  it('renders PageHeader with title "个人设置"', async () => {
    const wrapper = mount(Profile)
    await flushPromises()
    // PageHeader 真实渲染，通过 .page-header h2 验证标题
    expect(wrapper.find('.page-header').exists()).toBe(true)
    expect(wrapper.find('.page-header h2').text()).toContain('个人设置')
  })

  it('displays user info from store', async () => {
    const wrapper = mount(Profile)
    await flushPromises()
    // <script setup> 不暴露 vm.userInfo，通过渲染文本验证
    expect(wrapper.text()).toContain('testadmin')
    expect(wrapper.text()).toContain('admin@test.com')
  })

  it('renders the fixed system administrator identity', async () => {
    const wrapper = mount(Profile)
    await flushPromises()
    expect(wrapper.text()).toContain('系统管理员')
  })

  it('renders password form fields with empty values', async () => {
    const wrapper = mount(Profile)
    await flushPromises()
    // <script setup> 不暴露 vm.form，通过 DOM 验证 input 初始值
    const inputs = wrapper.findAll('input')
    expect(inputs.length).toBeGreaterThanOrEqual(3)
    inputs.forEach(input => {
      expect((input.element as HTMLInputElement).value).toBe('')
    })
  })

  it('renders submit and reset buttons', async () => {
    const wrapper = mount(Profile)
    await flushPromises()
    const buttons = wrapper.findAll('.el-button')
    const texts = buttons.map(b => b.text())
    expect(texts.some(t => t.includes('提交'))).toBe(true)
    expect(texts.some(t => t.includes('重置'))).toBe(true)
  })

  it('renders user info card section', async () => {
    const wrapper = mount(Profile)
    await flushPromises()
    expect(wrapper.find('.info-card').exists()).toBe(true)
  })

  it('does not call changePassword on mount', async () => {
    mount(Profile)
    await flushPromises()
    expect(mockChangePassword).not.toHaveBeenCalled()
  })

  // ── C-ops 审计回归钉（2026-10-21）──────────────────────────────────────
  it('C-ops：新密码 placeholder 不再与 form-tip 规则说明重复', async () => {
    const wrapper = mount(Profile)
    await flushPromises()
    const placeholders = wrapper.findAll('input').map(i => (i.element as HTMLInputElement).placeholder)
    expect(placeholders).toContain('请输入新密码')
    expect(placeholders, 'placeholder 与 form-tip 文案重复').not.toContain('至少 8 位字符')
    // 规则说明仍完整保留在 form-tip 里（不能把提示也删了）
    expect(wrapper.text()).toContain('至少 8 位字符，且不能与当前密码相同')
  })

  it('C-ops：窄屏横向内距交还 main（与其它页对齐），桌面两卡等高', () => {
    // 窄屏去掉 12px 横向 padding 后，卡片 x 才与其它页的 main 内距一致
    expect(profileSource).toMatch(/@media \(max-width: 768px\)[\s\S]*?\.profile-page \{\s*padding: 0 0 16px;/)
    expect(profileSource, '仍保留叠加在 main 之上的 12px 横向内距').not.toContain('padding: 0 12px 16px')
    // 桌面等高：el-col 变 flex 容器、卡片撑满列高
    expect(profileSource).toMatch(/\.profile-page :deep\(\.el-col\) \{\s*display: flex;/)
    expect(profileSource).toMatch(/\.profile-page :deep\(\.el-col > \.el-card\) \{[^}]*flex: 1 1 auto/)
  })
})
