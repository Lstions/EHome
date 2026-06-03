import { ref, onMounted, onUnmounted, readonly } from 'vue'

/** 响应式断点（与 src/styles/theme.css 保持一致） */
export const BREAKPOINTS = {
  xs: 480,
  sm: 640,
  md: 768,
  lg: 1024,
  xl: 1280,
  '2xl': 1536,
} as const

export type BreakpointKey = keyof typeof BREAKPOINTS

/** 当前窗口宽度 */
const windowWidth = ref(typeof window !== 'undefined' ? window.innerWidth : 1280)

function updateWidth() {
  windowWidth.value = window.innerWidth
}

/** 全局响应式 composable（在多个组件间共享 windowWidth） */
export function useResponsive() {
  onMounted(() => {
    window.addEventListener('resize', updateWidth)
  })
  onUnmounted(() => {
    window.removeEventListener('resize', updateWidth)
  })

  const isMobile = readonly(ref(windowWidth.value < BREAKPOINTS.md))
  const isTablet = readonly(ref(
    windowWidth.value >= BREAKPOINTS.md && windowWidth.value < BREAKPOINTS.lg,
  ))
  const isDesktop = readonly(ref(windowWidth.value >= BREAKPOINTS.lg))

  return {
    width: windowWidth,
    isMobile,
    isTablet,
    isDesktop,
  }
}

/** 便捷判断：是否 ≤ md (768) */
export function isMobileWidth(): boolean {
  return windowWidth.value < BREAKPOINTS.md
}
