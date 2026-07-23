import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import Login from '@/views/auth/Login.vue'

// Mock vue-router
const mockPush = vi.fn()
const mockRoute = { query: {} as Record<string, string> }
vi.mock('vue-router', () => {
  return {
    useRouter: () => ({ push: mockPush }),
    useRoute: () => mockRoute,
  }
})

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

// Mock LoginForm component (child) — exposes setLoading so Login.vue's
// finally block can call it without TypeError.
vi.mock('@/components/forms/LoginForm.vue', () => ({
  default: {
    name: 'LoginForm',
    template: '<div data-testid="login-form"><slot /></div>',
    props: ['disabled'],
    emits: ['success', 'error'],
    methods: {
      setLoading(_v: boolean) {},
    },
  },
}))

describe('Login.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders login container with brand', () => {
    const wrapper = mount(Login, {
      global: {
        stubs: {
          LoginForm: true,
          'el-alert': true,
        },
      },
    })
    expect(wrapper.find('.login-container').exists()).toBe(true)
    expect(wrapper.find('.login-box').exists()).toBe(true)
    expect(wrapper.find('.brand-name').text()).toBe('EHomeSystem')
  })

  it('renders LoginForm component', () => {
    const wrapper = mount(Login, {
      global: {
        stubs: {
          LoginForm: { template: '<div data-testid="login-form" />' },
          'el-alert': true,
        },
      },
    })
    expect(wrapper.find('[data-testid="login-form"]').exists()).toBe(true)
  })

  it('shows error message when login fails', async () => {
    mockLogin.mockRejectedValue(Object.assign(new Error('用户名或密码错误'), { status: 401 }))
    mockRecordLoginFailure.mockReturnValue({ locked: false, attempts: 1, remainingSeconds: 0 })

    const wrapper = mount(Login, {
      global: {
        stubs: {
          LoginForm: {
            template: '<div data-testid="login-form" />',
            props: ['disabled'],
            emits: ['success', 'error'],
          },
          'el-alert': {
            template: '<div data-testid="el-alert">{{ $slots.title ? $slots.title() : title }}</div>',
            props: ['title', 'type', 'closable', 'showIcon'],
          },
        },
      },
    })

    // Simulate login failure via handleLogin
    const vm = wrapper.vm as any
    await vm.handleLogin('admin', 'wrong', false)
    await nextTick()

    expect(mockLogin).toHaveBeenCalledWith('admin', 'wrong', false)
    expect(wrapper.vm.errorMsg).toBe('用户名或密码错误，还剩 4 次机会')
  })

  it('does not report network failures as wrong credentials', async () => {
    mockLogin.mockRejectedValue(new Error('Network Error'))
    const wrapper = mount(Login, {
      global: {
        stubs: {
          LoginForm: { template: '<div data-testid="login-form" />' },
          'el-alert': true,
        },
      },
    })

    await (wrapper.vm as any).handleLogin('admin', 'password', false)
    expect(wrapper.vm.errorMsg).toBe('无法连接服务器，请检查网络连接后重试。')
    expect(mockRecordLoginFailure).not.toHaveBeenCalled()
  })

  it('shows server rate-limit feedback with retry time', async () => {
    mockLogin.mockRejectedValue(Object.assign(new Error('too many login attempts'), {
      status: 429,
      retryAfterSeconds: 60,
    }))
    const wrapper = mount(Login, {
      global: {
        stubs: {
          LoginForm: { template: '<div data-testid="login-form" />' },
          'el-alert': true,
        },
      },
    })

    await (wrapper.vm as any).handleLogin('admin', 'password', false)
    expect(wrapper.vm.errorMsg).toBe('登录尝试过于频繁，请 1 分钟后重试。')
    expect(mockRecordLoginFailure).not.toHaveBeenCalled()
  })

  it('redirects to dashboard on successful login', async () => {
    mockLogin.mockResolvedValue(undefined)

    const wrapper = mount(Login, {
      global: {
        stubs: {
          LoginForm: true,
          'el-alert': true,
        },
      },
    })

    const vm = wrapper.vm as any
    await vm.handleLogin('admin', 'admin123', true)
    await nextTick()

    expect(mockLogin).toHaveBeenCalledWith('admin', 'admin123', true)
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  it('redirects to query.redirect on successful login', async () => {
    mockLogin.mockResolvedValue(undefined)
    mockPush.mockClear()
    mockRoute.query = { redirect: '/node' }

    const wrapper = mount(Login, {
      global: {
        stubs: {
          LoginForm: true,
          'el-alert': true,
        },
      },
    })

    const vm = wrapper.vm as any
    await vm.handleLogin('admin', 'admin123', false)
    await nextTick()

    expect(mockPush).toHaveBeenCalledWith('/node')
  })

  it('shows lockout alert when lockSeconds > 0', async () => {
    const { getRemainingLockSeconds } = await import('@/utils/loginLockout')
    vi.mocked(getRemainingLockSeconds).mockReturnValue(300)

    const wrapper = mount(Login, {
      global: {
        stubs: {
          LoginForm: { template: '<div data-testid="login-form" />', props: ['disabled'] },
          'el-alert': {
            template: '<div data-testid="el-alert" />',
            props: ['title', 'type', 'closable', 'showIcon'],
          },
        },
      },
    })

    // lockSeconds should be set from getRemainingLockSeconds
    expect(wrapper.vm.lockSeconds).toBe(300)
  })

  it('displays app version in footer', () => {
    const wrapper = mount(Login, {
      global: {
        stubs: {
          LoginForm: true,
          'el-alert': true,
        },
      },
    })
    const version = wrapper.find('.version')
    expect(version.exists()).toBe(true)
    expect(version.text()).toMatch(/^v/)
  })
})
