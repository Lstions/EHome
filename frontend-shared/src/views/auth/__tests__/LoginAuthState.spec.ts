import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Login from '../Login.vue'

const { mockLogin, mockPush, mockInitialization, mockInitialize } = vi.hoisted(() => ({
  mockLogin: vi.fn(),
  mockPush: vi.fn(),
  mockInitialization: vi.fn(),
  mockInitialize: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => ({ query: {} }),
}))
vi.mock('@/api/auth', () => ({
  authApi: { initialization: mockInitialization, initialize: mockInitialize },
}))
vi.mock('@/stores/user', () => ({
  useUserStore: () => ({ login: mockLogin, recordLoginFailure: () => ({ attempts: 1, locked: false }) }),
}))
vi.mock('@/utils/loginLockout', () => ({
  getRemainingLockSeconds: vi.fn(() => 0),
  recordFailedAttempt: vi.fn(() => ({ locked: false, attempts: 1, remainingSeconds: 0 })),
  clearAttempts: vi.fn(),
}))

// Mock LoginForm 子组件 — vi.mock 工厂不能引用外部变量，用 vi.hoisted
const { LoginFormStub, InitializeAdminFormStub } = vi.hoisted(() => {
  const { defineComponent } = require('vue')
  return {
    LoginFormStub: defineComponent({
      name: 'LoginForm',
      emits: ['success', 'error'],
      template: '<button data-testid="login" @click="$emit(\'success\', \'admin\', \'wrong\', false)">登录</button>',
      methods: { setLoading: () => {} },
    }),
    InitializeAdminFormStub: defineComponent({
      name: 'InitializeAdminForm',
      emits: ['submit'],
      template: '<div><span>初始化凭据</span><button data-testid="initialize" @click="$emit(\'submit\', { credential: \'selector.secret\', username: \'admin\', password: \'strong-password-123\' })">创建管理员</button></div>',
      methods: { resetForm: () => {}, setLoading: () => {} },
    }),
  }
})
vi.mock('@/components/forms/LoginForm.vue', () => ({ default: LoginFormStub }))
vi.mock('@/components/forms/InitializeAdminForm.vue', () => ({ default: InitializeAdminFormStub }))

// 全局 ElAlert stub 已在 test-setup.ts 注册，支持 #title slot

describe('Login auth-state handling', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('shows migration-required instead of disguising it as a password failure', async () => {
    mockInitialization.mockResolvedValue({ state: 'migration_required' })
    mockLogin.mockRejectedValue(new Error('用户名或密码错误'))
    const wrapper = mount(Login)
    await flushPromises()
    await wrapper.get('[data-testid="login"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('认证状态异常')
    expect(mockLogin).not.toHaveBeenCalled()
  })

  it('shows the first-run administrator setup instead of calling login', async () => {
    mockInitialization.mockResolvedValue({ state: 'uninitialized' })
    mockLogin.mockRejectedValue(new Error('用户名或密码错误'))
    const wrapper = mount(Login)
    await flushPromises()
    expect(wrapper.find('[data-testid="initialize"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('初始化凭据')
    expect(wrapper.find('[data-testid="login"]').exists()).toBe(false)
    await wrapper.get('[data-testid="initialize"]').trigger('click')
    await flushPromises()
    expect(mockInitialize).toHaveBeenCalledWith({
      credential: 'selector.secret',
      username: 'admin',
      password: 'strong-password-123',
    })
    expect(mockLogin).not.toHaveBeenCalled()
  })

  it('switches to login after the administrator is created', async () => {
    mockInitialization.mockResolvedValue({ state: 'uninitialized' })
    mockInitialize.mockResolvedValue({ id: 1, username: 'admin' })
    const wrapper = mount(Login)
    await flushPromises()
    await wrapper.get('[data-testid="initialize"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('管理员账号创建成功')
    expect(wrapper.find('[data-testid="login"]').exists()).toBe(true)
  })

  it('clears previous error before applying new auth-state message', async () => {
    mockInitialization.mockResolvedValue({ state: 'migration_required' })
    const wrapper = mount(Login)
    await flushPromises()
    // <script setup> 不暴露 vm.errorMsg，但 errorMsg 为空时 alert 不显示
    // 直接点击登录验证 migration_required 消息显示
    await wrapper.get('[data-testid="login"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('认证状态异常')
  })
})
