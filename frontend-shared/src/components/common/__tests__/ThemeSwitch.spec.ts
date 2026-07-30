import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ThemeSwitch from '../ThemeSwitch.vue'

// 全局 Element Plus stub 已在 src/test-setup.ts 注册，
// ElDropdown 渲染为 <div class="el-dropdown">, ElDropdownMenu 为 <div class="el-dropdown-menu">, 等。

// Mock the icons so we don't need the full @element-plus/icons-vue
vi.mock('@element-plus/icons-vue', () => ({
  Sunny: { name: 'Sunny', template: '<i class="sun" />' },
  Moon: { name: 'Moon', template: '<i class="moon" />' },
  Monitor: { name: 'Monitor', template: '<i class="monitor" />' },
}))

// Mock matchMedia (happy-dom may not provide it)
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })),
})

describe('ThemeSwitch.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
  })

  it('renders dropdown with theme options', () => {
    const wrapper = mount(ThemeSwitch)
    expect(wrapper.find('.el-dropdown').exists()).toBe(true)
    expect(wrapper.find('.el-dropdown-menu').exists()).toBe(true)
    const items = wrapper.findAll('.el-dropdown-menu__item')
    expect(items).toHaveLength(3) // light, dark, system
  })

  it('renders option labels in Chinese', () => {
    const wrapper = mount(ThemeSwitch)
    const text = wrapper.text()
    expect(text).toContain('亮色模式')
    expect(text).toContain('暗色模式')
    expect(text).toContain('跟随系统')
  })
})
