import { describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { createPinia } from 'pinia'
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

const LoginFormStub = defineComponent({
  emits: ['success', 'error'],
  template: '<button data-testid="login" @click="$emit(\'success\', \'admin\', \'wrong\', false)">登录</button>',
  methods: { setLoading: vi.fn() },
})
const AlertStub = defineComponent({
  props: ['title'],
  setup(props) { return () => h('div', { 'data-testid': 'alert' }, String(props.title || '')) },
})
const InitializeAdminFormStub = defineComponent({
  emits: ['submit'],
  template: '<div><span>初始化凭据</span><button data-testid="initialize" @click="$emit(\'submit\', { credential: \'selector.secret\', username: \'admin\', password: \'strong-password-123\' })">创建管理员</button></div>',
  methods: { resetForm: vi.fn(), setLoading: vi.fn() },
})

describe('Login auth-state handling', () => {
  it('shows migration-required instead of disguising it as a password failure', async () => {
    mockInitialization.mockResolvedValue({ state: 'migration_required' })
    mockLogin.mockRejectedValue(new Error('用户名或密码错误'))
    const wrapper = mount(Login, {
      global: { plugins: [createPinia()], stubs: { LoginForm: LoginFormStub, ElAlert: AlertStub } },
    })
    await flushPromises()
    await wrapper.get('[data-testid="login"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('认证迁移')
    expect(mockLogin).not.toHaveBeenCalled()
  })

  it('shows the first-run administrator setup instead of calling login', async () => {
    mockInitialization.mockResolvedValue({ state: 'uninitialized' })
    mockLogin.mockRejectedValue(new Error('用户名或密码错误'))
    const wrapper = mount(Login, {
      global: {
        plugins: [createPinia()],
        stubs: { LoginForm: LoginFormStub, InitializeAdminForm: InitializeAdminFormStub, ElAlert: AlertStub },
      },
    })
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
    const wrapper = mount(Login, {
      global: {
        plugins: [createPinia()],
        stubs: { LoginForm: LoginFormStub, InitializeAdminForm: InitializeAdminFormStub, ElAlert: AlertStub },
      },
    })
    await flushPromises()
    await wrapper.get('[data-testid="initialize"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('管理员账号创建成功')
    expect(wrapper.find('[data-testid="login"]').exists()).toBe(true)
  })

  it('clears previous error before applying new auth-state message', async () => {
    mockInitialization.mockResolvedValue({ state: 'migration_required' })
    const wrapper = mount(Login, {
      global: { plugins: [createPinia()], stubs: { LoginForm: LoginFormStub, ElAlert: AlertStub } },
    })
    await flushPromises()
    // Simulate the alert closes, then user clicks login again
    wrapper.vm.errorMsg = ''
    await wrapper.get('[data-testid="login"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('认证迁移')
  })
})
