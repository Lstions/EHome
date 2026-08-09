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

// —— P0-A 预取调度验证 ——
// 预取函数抽在 utils/prefetch.ts（字面量动态 import 路径与 router 懒加载一致）。
// mock 该模块导出函数：vi.fn 每次调用都记录，不受「mock 工厂只在模块首次导入
// 时执行一次」的缓存限制，也不依赖用例执行顺序。
import {
  prefetchMainLayout as mockPrefetchMainLayout,
  prefetchDashboard as mockPrefetchDashboard,
} from '@/utils/prefetch'
vi.mock('@/utils/prefetch', () => ({
  prefetchMainLayout: vi.fn().mockResolvedValue({}),
  prefetchDashboard: vi.fn().mockResolvedValue({}),
}))

describe('Login.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  // 必须先于本文件中任何其他 mount(Login) 执行：mock 工厂在本文件内每个模块
  // 只运行一次（模块注册表缓存，之后的 import() 复用已求值模块不会再跑工厂），
  // 因此工厂计数只能证明「本文件内第一次 import 该模块」——即此测试自身触发的那次。
  it('prefetches main layout and dashboard chunks on mount (P0-A)', async () => {
    mount(Login)
    await flushPromises()
    // happy-dom 无 requestIdleCallback，Login.vue 回退 setTimeout(prefetch, 0)；
    // 等待宏任务执行完毕后，两条预取应各被调度一次。
    await new Promise((resolve) => setTimeout(resolve, 30))
    expect(mockPrefetchMainLayout).toHaveBeenCalledTimes(1)
    expect(mockPrefetchDashboard).toHaveBeenCalledTimes(1)
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

  it('does not report network failures as wrong credentials', async () => {
    mockLogin.mockRejectedValue(new Error('Network Error'))

    const wrapper = mount(Login)
    await flushPromises()

    const form = wrapper.findComponent({ name: 'LoginForm' })
    form.vm.$emit('success', 'admin', 'password', false)
    await flushPromises()
    await nextTick()

    expect(wrapper.text()).toContain('无法连接服务器，请检查网络连接后重试。')
    expect(mockRecordLoginFailure).not.toHaveBeenCalled()
  })

  it('shows server rate-limit feedback with retry time', async () => {
    mockLogin.mockRejectedValue(Object.assign(new Error('too many login attempts'), {
      status: 429,
      retryAfterSeconds: 60,
    }))

    const wrapper = mount(Login)
    await flushPromises()

    const form = wrapper.findComponent({ name: 'LoginForm' })
    form.vm.$emit('success', 'admin', 'password', false)
    await flushPromises()
    await nextTick()

    expect(wrapper.text()).toContain('登录尝试过于频繁，请 1 分钟后重试。')
    expect(mockRecordLoginFailure).not.toHaveBeenCalled()
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

  it('shows fullscreen brand overlay while navigating (P0-B)', async () => {
    mockLogin.mockResolvedValue(undefined)
    // 手动控制的 pending promise：保证断言时 push 尚未 resolve
    let resolvePush: () => void = () => {}
    let pushStarted = false
    mockPush.mockImplementation(() => {
      pushStarted = true
      return new Promise<void>((resolve) => {
        resolvePush = () => resolve()
      })
    })

    const wrapper = mount(Login)
    await flushPromises()

    const form = wrapper.findComponent({ name: 'LoginForm' })
    form.vm.$emit('success', 'admin', 'admin123', true)

    // push 已调用且尚未 resolve：过渡层应显示
    await flushPromises()
    expect(pushStarted).toBe(true)
    expect(wrapper.find('.login-transition').exists()).toBe(true)
    expect(wrapper.find('.login-transition__logo').exists()).toBe(true)
    expect(wrapper.find('.login-transition__text').text()).toContain('正在进入系统')

    // 手动 resolve，验证过渡层随后关闭（等一个宏任务让 await 恢复）
    resolvePush()
    await new Promise((resolve) => setTimeout(resolve, 5))
    await flushPromises()
    await nextTick()
    expect(wrapper.find('.login-transition').exists()).toBe(false)
  })

  it('hides overlay and surfaces error when navigation fails (P0-B)', async () => {
    mockLogin.mockResolvedValue(undefined)
    // 导航失败（例如懒加载 chunk 下载失败 → router.push 拒绝）
    mockPush.mockRejectedValue(new Error('Failed to fetch dynamically imported module'))

    const wrapper = mount(Login)
    await flushPromises()

    const form = wrapper.findComponent({ name: 'LoginForm' })
    form.vm.$emit('success', 'admin', 'admin123', true)
    await flushPromises()
    await nextTick()

    expect(mockPush).toHaveBeenCalledWith('/dashboard')
    expect(wrapper.find('.login-transition').exists()).toBe(false)
    expect(wrapper.text()).toContain('页面加载失败，请刷新重试')
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
