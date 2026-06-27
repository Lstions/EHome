import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import UserList from '../UserList.vue'

// Mock vue-router
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

// Use vi.hoisted for mock functions referenced inside vi.mock factories
const {
  mockList,
  mockCreate,
  mockUpdate,
  mockDelete,
  mockResetPassword,
} = vi.hoisted(() => ({
  mockList: vi.fn(() =>
    Promise.resolve({
      data: {
        list: [
          { id: 1, username: 'admin', email: 'admin@test.com', role: 'admin', enabled: true, created_at: '2025-01-01T00:00:00Z', last_login_at: '2025-06-01T12:00:00Z' },
          { id: 2, username: 'operator1', email: 'op@test.com', role: 'operator', enabled: true, created_at: '2025-02-01T00:00:00Z', last_login_at: null },
          { id: 3, username: 'viewer1', email: 'viewer@test.com', role: 'viewer', enabled: false, created_at: '2025-03-01T00:00:00Z' },
        ],
        total: 3,
      },
    })
  ),
  mockCreate: vi.fn(() => Promise.resolve({ id: 4 })),
  mockUpdate: vi.fn(() => Promise.resolve()),
  mockDelete: vi.fn(() => Promise.resolve()),
  mockResetPassword: vi.fn(() => Promise.resolve()),
}))

vi.mock('@/stores/user', () => ({
  useUserStore: () => ({
    userInfo: { id: 1, username: 'admin', role: 'admin' },
  }),
}))

vi.mock('@/api/user', () => ({
  userApi: {
    list: mockList,
    create: mockCreate,
    update: mockUpdate,
    delete: mockDelete,
    resetPassword: mockResetPassword,
  },
}))

vi.mock('@/utils/feedback', () => ({
  default: {
    success: vi.fn(),
    handleError: vi.fn(),
    confirmDanger: vi.fn(() => Promise.resolve(true)),
  },
}))

vi.mock('@/utils/format', () => ({
  formatTime: (time: string) => time || '-',
}))

// Stub child components
const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /><slot name="extra" /></div>' },
  'el-card': { template: '<div class="el-card"><slot /></div>' },
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
  'el-input': { template: '<input class="el-input" />' },
  'el-select': { template: '<div class="el-select"><slot /></div>' },
  'el-option': { template: '<div />' },
  'el-form': { template: '<form class="el-form"><slot /></form>' },
  'el-form-item': { template: '<div class="el-form-item"><slot /></div>' },
  'el-table': { template: '<div class="el-table"><slot /></div>' },
  'el-table-column': { template: '<div />' },
  'el-pagination': { template: '<div class="el-pagination" />' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-dialog': { template: '<div class="el-dialog"><slot /><slot name="footer" /></div>' },
  'el-switch': { template: '<div class="el-switch" />' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
}

describe('UserList.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders the user-list-page container', async () => {
    const wrapper = mount(UserList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.user-list-page').exists()).toBe(true)
  })

  it('calls fetchList on mount', async () => {
    mount(UserList, { global: { stubs } })
    await flushPromises()
    expect(mockList).toHaveBeenCalled()
  })

  it('displays user list after loading', async () => {
    const wrapper = mount(UserList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.list.length).toBe(3)
  })

  it('computes total correctly', async () => {
    const wrapper = mount(UserList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.total).toBe(3)
  })

  it('handles fetch error gracefully', async () => {
    mockList.mockRejectedValueOnce(new Error('Network error'))
    const wrapper = mount(UserList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.loading).toBe(false)
  })

  it('roleLabel maps roles correctly', async () => {
    const wrapper = mount(UserList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.roleLabel('admin')).toBe('管理员')
    expect(vm.roleLabel('operator')).toBe('操作员')
    expect(vm.roleLabel('viewer')).toBe('观察者')
  })

  it('roleTagType maps roles correctly', async () => {
    const wrapper = mount(UserList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.roleTagType('admin')).toBe('danger')
    expect(vm.roleTagType('operator')).toBe('warning')
    expect(vm.roleTagType('viewer')).toBe('info')
  })

  it('openCreateDialog resets form and shows dialog', async () => {
    const wrapper = mount(UserList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.openCreateDialog()
    expect(vm.editingUser).toBeNull()
    expect(vm.form.username).toBe('')
    expect(vm.dialogVisible).toBe(true)
  })

  it('openEditDialog populates form with user data', async () => {
    const wrapper = mount(UserList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    const user = { id: 2, username: 'operator1', email: 'op@test.com', role: 'operator', enabled: true }
    vm.openEditDialog(user)
    expect(vm.editingUser).toStrictEqual(user)
    expect(vm.form.email).toBe('op@test.com')
    expect(vm.form.role).toBe('operator')
    expect(vm.dialogVisible).toBe(true)
  })

  it('resetFilters resets filter state', async () => {
    const wrapper = mount(UserList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.filters.keyword = 'test'
    vm.filters.role = 'admin'
    vm.filters.page = 3
    vm.resetFilters()
    expect(vm.filters.keyword).toBe('')
    expect(vm.filters.role).toBeUndefined()
    expect(vm.filters.page).toBe(1)
  })

  it('computes currentUserId from store', async () => {
    const wrapper = mount(UserList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.currentUserId).toBe(1)
  })
})
