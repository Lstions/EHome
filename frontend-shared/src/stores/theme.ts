import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

export type ThemeMode = 'light' | 'dark'

export const useThemeStore = defineStore('theme', () => {
  const savedTheme = localStorage.getItem('theme') as ThemeMode | null
  const systemQuery = window.matchMedia('(prefers-color-scheme: dark)')
  const followsSystem = ref(!savedTheme)
  const mode = ref<ThemeMode>(savedTheme || (systemQuery.matches ? 'dark' : 'light'))

  // 应用主题
  const applyTheme = (theme: ThemeMode) => {
    document.documentElement.setAttribute('data-theme', theme)
    document.body.className = theme === 'dark' ? 'dark-theme' : 'light-theme'
    
    // 更新 Element Plus 主题
    const html = document.documentElement
    if (theme === 'dark') {
      html.classList.add('dark')
    } else {
      html.classList.remove('dark')
    }
  }

  // 切换主题
  const toggleTheme = () => {
    followsSystem.value = false
    mode.value = mode.value === 'light' ? 'dark' : 'light'
  }

  // 设置主题
  const setTheme = (theme: ThemeMode) => {
    followsSystem.value = false
    mode.value = theme
    localStorage.setItem('theme', theme)
  }

  const followSystem = () => {
    followsSystem.value = true
    localStorage.removeItem('theme')
    mode.value = systemQuery.matches ? 'dark' : 'light'
  }

  // 监听主题变化。flush: 'sync' 让 mode/localStorage/DOM 三者保持同步不变量，
  // 避免调用 setTheme 后 DOM 仍停留在旧主题直到 nextTick。
  watch(mode, (newMode) => {
    if (!followsSystem.value) localStorage.setItem('theme', newMode)
    applyTheme(newMode)
  }, { immediate: true, flush: 'sync' })

  // 监听系统主题变化
  systemQuery.addEventListener('change', (event) => {
    if (followsSystem.value) mode.value = event.matches ? 'dark' : 'light'
  })

  return {
    mode,
    followsSystem,
    toggleTheme,
    setTheme,
    followSystem
  }
})
