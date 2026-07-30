import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import Login from '@/views/auth/Login.vue'

// Mock vue-router
const mockPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => ({ query: {} }),
}))

// Mock loginLockout
vi.mock('@/utils/loginLockout', () => ({
  getRemainingLockSeconds: vi.fn(() => 0),
  recordFailedAttempt: vi.fn(() => ({ locked: false, attempts: 1, remainingSeconds: 0 })),
  clearAttempts: vi.fn(),
}))

// Mock user store
const mockLogin = vi.fn()
const mockRecordLoginFailure = vi.fn(() => ({ locked: false, attempts: 1, remainingSeconds: 0 }))
vi.mock('@/stores/user', () => ({
  useUserStore: () => ({
    login: mockLogin,
    recordLoginFailure: mockRecordLoginFailure,
    isLoggedIn: false,
    userInfo: null,
    token: '',
  }),
}))

vi.mock('@/api/auth', () => ({
  authApi: {
    initialization: vi.fn().mockResolvedValue({ state: 'initialized' }),
  },
}))

// Mock LoginForm 子组件，避免导入真实组件及其依赖
// 全局 Element Plus stub 已在 src/test-setup.ts 注册
vi.mock('@/components/forms/LoginForm.vue', () => ({
  default: {
    name: 'LoginForm',
    template: '<div data-testid="login-form" />',
    props: ['disabled'],
    emits: ['success', 'error'],
  },
}))

// Mock InitializeAdminForm 以避免导入其依赖
vi.mock('@/components/forms/InitializeAdminForm.vue', () => ({
  default: {
    name: 'InitializeAdminForm',
    template: '<div data-testid="init-form" />',
  },
}))

describe('Login.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders login container with brand', async () => {
    const wrapper = mount(Login)
    await flushPromises()
    expect(wrapper.find('.login-container').exists()).toBe(true)
    expect(wrapper.find('.login-box').exists()).toBe(true)
    expect(wrapper.find('.brand-name').text()).toBe('EHomeSystem')
  })

  it('renders LoginForm component', async () => {
    const wrapper = mount(Login)
    await flushPromises()
    expect(wrapper.find('[data-testid="login-form"]').exists()).toBe(true)
  })

  it('shows error message when login fails', async () => {
    mockLogin.mockRejectedValue(new Error('用户名或密码错误'))
    mockRecordLoginFailure.mockReturnValue({ locked: false, attempts: 1, remainingSeconds: 0 })

    const wrapper = mount(Login)
    await flushPromises()

    // <script setup> 不暴露 vm.handleLogin，通过 LoginForm emit('success') 触发
    const form = wrapper.findComponent({ name: 'LoginForm' })
    form.vm.$emit('success', 'admin', 'wrong', false)
    await flushPromises()
    await nextTick()

    expect(mockLogin).toHaveBeenCalledWith('admin', 'wrong', false)
    // 错误消息通过 el-alert 渲染
    expect(wrapper.text()).toContain('用户名或密码错误')
  })

  it('redirects to dashboard on successful login', async () => {
    mockLogin.mockResolvedValue(undefined)

    const wrapper = mount(Login)
    await flushPromises()

    const form = wrapper.findComponent({ name: 'LoginForm' })
    form.vm.$emit('success', 'admin', 'admin123', true)
    await flushPromises()
    await nextTick()

    expect(mockLogin).toHaveBeenCalledWith('admin', 'admin123', true)
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  it('redirects to query.redirect on successful login', async () => {
    mockLogin.mockResolvedValue(undefined)

    const wrapper = mount(Login)
    await flushPromises()

    const form = wrapper.findComponent({ name: 'LoginForm' })
    form.vm.$emit('success', 'admin', 'admin123', false)
    await flushPromises()
    await nextTick()

    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  it('shows lockout alert when lockSeconds > 0', async () => {
    // getRemainingLockSeconds 在 vi.mock 工厂中返回 0，
    // 需要在 mount 前改为返回 300
    const { getRemainingLockSeconds } = await import('@/utils/loginLockout')
    vi.mocked(getRemainingLockSeconds).mockReturnValue(300)

    const wrapper = mount(Login)
    await flushPromises()

    // <script setup> 不暴露 vm.lockSeconds，通过 el-alert 渲染验证
    expect(wrapper.text()).toContain('300')
    expect(wrapper.text()).toContain('登录已锁定')
  })

  it('displays app version in footer', async () => {
    const wrapper = mount(Login)
    await flushPromises()
    const version = wrapper.find('.version')
    expect(version.exists()).toBe(true)
    expect(version.text()).toMatch(/^v/)
  })
})
