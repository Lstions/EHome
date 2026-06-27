import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

export type ThemeMode = 'light' | 'dark'

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(
    (localStorage.getItem('theme') as ThemeMode) || 
    (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
  )

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
    mode.value = mode.value === 'light' ? 'dark' : 'light'
  }

  // 设置主题
  const setTheme = (theme: ThemeMode) => {
    mode.value = theme
  }

  // 监听主题变化。flush: 'sync' 让 mode/localStorage/DOM 三者保持同步不变量，
  // 避免调用 setTheme 后 DOM 仍停留在旧主题直到 nextTick。
  watch(mode, (newMode) => {
    localStorage.setItem('theme', newMode)
    applyTheme(newMode)
  }, { immediate: true, flush: 'sync' })

  // 监听系统主题变化
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', (e) => {
    if (!localStorage.getItem('theme')) {
      mode.value = e.matches ? 'dark' : 'light'
    }
  })

  return {
    mode,
    toggleTheme,
    setTheme
  }
})
