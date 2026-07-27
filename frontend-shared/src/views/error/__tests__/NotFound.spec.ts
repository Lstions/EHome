import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import NotFound from '../NotFound.vue'

// Mock vue-router
const mockPush = vi.fn()
const mockBack = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush, back: mockBack }),
}))

// Stub child components
const stubs = {
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
}

describe('NotFound.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders the not-found error page', async () => {
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-page').exists()).toBe(true)
  })

  it('displays 404 error code', async () => {
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-code').text()).toBe('404')
  })

  it('displays "页面不存在" title', async () => {
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-title').text()).toBe('页面不存在')
  })

  it('goHome navigates to dashboard', async () => {
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.goHome()
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  it('goBack calls router.back when history exists', async () => {
    const origLength = window.history.length
    Object.defineProperty(window, 'history', { value: { length: 2 }, writable: true, configurable: true })
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.goBack()
    expect(mockBack).toHaveBeenCalled()
    Object.defineProperty(window, 'history', { value: { length: origLength }, writable: true, configurable: true })
  })

  it('goBack navigates to dashboard when no history', async () => {
    Object.defineProperty(window, 'history', { value: { length: 1 }, writable: true, configurable: true })
    const wrapper = mount(NotFound, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.goBack()
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })
})
