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

// Stub child components
const stubs = {
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
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
    expect(wrapper.find('.error-page.forbidden').exists()).toBe(true)
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
    const vm = wrapper.vm as any
    expect(vm.username).toBe('viewer1')
  })

  it('computes roleLabel for viewer', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.roleLabel).toBe('观察者')
  })

  it('computes roleTagType for viewer as info', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.roleTagType).toBe('info')
  })

  it('goHome navigates to dashboard', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.goHome()
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })
})
