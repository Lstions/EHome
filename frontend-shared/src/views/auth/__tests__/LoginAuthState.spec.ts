import { describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { createPinia } from 'pinia'
import Login from '../Login.vue'

const { mockLogin, mockPush, mockInitialization } = vi.hoisted(() => ({
  mockLogin: vi.fn(),
  mockPush: vi.fn(),
  mockInitialization: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => ({ query: {} }),
}))
vi.mock('@/api/auth', () => ({ authApi: { initialization: mockInitialization } }))
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
})
