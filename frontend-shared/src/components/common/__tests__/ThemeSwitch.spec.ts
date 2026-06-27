import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ThemeSwitch from '../ThemeSwitch.vue'

const stubs = {
  'el-dropdown': {
    props: ['trigger'],
    template: '<div data-testid="dropdown"><slot /><slot name="dropdown" /></div>',
  },
  'el-dropdown-menu': { template: '<div data-testid="menu"><slot /></div>' },
  'el-dropdown-item': {
    props: ['command', 'disabled', 'divided'],
    template: '<div data-testid="item" :data-command="command" :data_disabled="disabled"><slot /></div>',
  },
  'el-button': {
    props: ['icon'],
    template: '<button data-testid="btn"><slot /></button>',
  },
  'el-icon': { template: '<i><slot /></i>' },
}

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
    const wrapper = mount(ThemeSwitch, { global: { stubs } })
    expect(wrapper.find('[data-testid="dropdown"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="menu"]').exists()).toBe(true)
    const items = wrapper.findAll('[data-testid="item"]')
    expect(items).toHaveLength(3) // light, dark, system
  })

  it('renders option labels in Chinese', () => {
    const wrapper = mount(ThemeSwitch, { global: { stubs } })
    const text = wrapper.text()
    expect(text).toContain('亮色模式')
    expect(text).toContain('暗色模式')
    expect(text).toContain('跟随系统')
  })
})
