import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Profile from '../Profile.vue'

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

// Stub child components
const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /></div>' },
  'el-row': { template: '<div class="el-row"><slot /></div>' },
  'el-col': { template: '<div class="el-col"><slot /></div>' },
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-avatar': { template: '<div class="el-avatar" />' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-form': { template: '<form class="el-form"><slot /></form>' },
  'el-form-item': { template: '<div class="el-form-item"><slot /></div>' },
  'el-input': { template: '<input class="el-input" />' },
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
}

describe('Profile.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders the profile-page container', async () => {
    const wrapper = mount(Profile, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.profile-page').exists()).toBe(true)
  })

  it('renders PageHeader with title "个人设置"', async () => {
    const wrapper = mount(Profile, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('[data-testid="page-header"]').exists()).toBe(true)
  })

  it('displays user info from store', async () => {
    const wrapper = mount(Profile, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.userInfo?.username).toBe('testadmin')
    expect(vm.userInfo?.email).toBe('admin@test.com')
  })

  it('renders the fixed system administrator identity', async () => {
    const wrapper = mount(Profile, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('系统管理员')
  })

  it('initializes form with empty passwords', async () => {
    const wrapper = mount(Profile, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.form.old_password).toBe('')
    expect(vm.form.new_password).toBe('')
    expect(vm.form.confirm_password).toBe('')
  })

  it('initializes submitting as false', async () => {
    const wrapper = mount(Profile, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.submitting).toBe(false)
  })

  it('has form validation rules defined', async () => {
    const wrapper = mount(Profile, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.rules.old_password).toBeDefined()
    expect(vm.rules.new_password).toBeDefined()
    expect(vm.rules.confirm_password).toBeDefined()
  })

  it('handleChangePassword returns early if no formRef', async () => {
    const wrapper = mount(Profile, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.formRef = null
    await vm.handleChangePassword()
    expect(mockChangePassword).not.toHaveBeenCalled()
  })

  it('renders user info card section', async () => {
    const wrapper = mount(Profile, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.info-card').exists()).toBe(true)
  })
})
