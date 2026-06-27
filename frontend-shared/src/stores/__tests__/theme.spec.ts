import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { nextTick } from 'vue'
import { useThemeStore } from '@/stores/theme'

// Mock localStorage
const localStorageMock = (() => {
  let store: Record<string, string> = {}
  return {
    getItem: vi.fn((key: string) => store[key] || null),
    setItem: vi.fn((key: string, value: string) => {
      store[key] = value
    }),
    removeItem: vi.fn((key: string) => {
      delete store[key]
    }),
    clear: vi.fn(() => {
      store = {}
    })
  }
})()

Object.defineProperty(window, 'localStorage', {
  value: localStorageMock
})

// Mock matchMedia
const matchMediaMock = vi.fn((query: string) => ({
  matches: false,
  media: query,
  onchange: null,
  addListener: vi.fn(),
  removeListener: vi.fn(),
  addEventListener: vi.fn(),
  removeEventListener: vi.fn(),
  dispatchEvent: vi.fn()
}))

Object.defineProperty(window, 'matchMedia', {
  value: matchMediaMock
})

describe('Theme Store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorageMock.clear()
    vi.clearAllMocks()
  })

  it('should initialize with light theme by default', () => {
    const store = useThemeStore()
    expect(store.mode).toBe('light')
  })

  it('should initialize with saved theme from localStorage', () => {
    localStorageMock.getItem.mockReturnValue('dark')
    
    const store = useThemeStore()
    expect(store.mode).toBe('dark')
  })

  it('should toggle theme from light to dark', () => {
    const store = useThemeStore()
    store.setTheme('light')
    
    store.toggleTheme()
    
    expect(store.mode).toBe('dark')
  })

  it('should toggle theme from dark to light', () => {
    const store = useThemeStore()
    store.setTheme('dark')
    
    store.toggleTheme()
    
    expect(store.mode).toBe('light')
  })

  it('should set theme to dark', () => {
    const store = useThemeStore()
    
    store.setTheme('dark')
    
    expect(store.mode).toBe('dark')
    expect(localStorageMock.setItem).toHaveBeenCalledWith('theme', 'dark')
  })

  it('should set theme to light', async () => {
    const store = useThemeStore()
    store.setTheme('dark') // Start with dark
    await nextTick()
    
    store.setTheme('light')
    await nextTick()
    
    expect(store.mode).toBe('light')
    expect(localStorageMock.setItem).toHaveBeenCalledWith('theme', 'light')
  })

  it('should apply theme to document', () => {
    const store = useThemeStore()
    
    store.setTheme('dark')
    
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
  })

  it('should remove dark class when switching to light theme', async () => {
    const store = useThemeStore()
    store.setTheme('dark')
    await nextTick()
    
    store.setTheme('light')
    await nextTick()
    
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })
})
