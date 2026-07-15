/**
 * Shared color utilities for EHomeSystem frontend.
 *
 * All UI colors should use Element Plus CSS variables (var(--el-color-*)) for
 * theme compatibility. This module provides hex fallbacks ONLY for contexts
 * where CSS variables cannot be used (e.g. ECharts chart options, which run
 * in JS/Canvas, not DOM/CSS).
 *
 * Usage rules:
 * - In <style> blocks: always use var(--el-color-*) — never hardcode hex.
 * - In JS/computed properties for charts: import from here.
 * - The hex values below mirror Element Plus's default theme palette so they
 *   stay visually consistent with the CSS variables used in styles.
 */

// Element Plus default theme hex values (for JS-only contexts like ECharts)
export const THEME_COLORS = {
  primary: '#409eff',
  success: '#67c23a',
  warning: '#e6a23c',
  danger: '#f56c6c',
  info: '#909399',
} as const

export type ThemeColors = { [K in keyof typeof THEME_COLORS]: string }

/** Resolve current CSS theme tokens for Canvas/JS contexts. */
export function getThemeColors(): ThemeColors {
  if (typeof document === 'undefined') return THEME_COLORS
  const styles = getComputedStyle(document.documentElement)
  const resolve = (token: string, fallback: string) => styles.getPropertyValue(token).trim() || fallback
  return {
    primary: resolve('--color-primary', THEME_COLORS.primary),
    success: resolve('--color-success', THEME_COLORS.success),
    warning: resolve('--color-warning', THEME_COLORS.warning),
    danger: resolve('--color-danger', THEME_COLORS.danger),
    info: resolve('--color-info', THEME_COLORS.info),
  }
}

export function getThemeSurfaceColors() {
  if (typeof document === 'undefined') {
    return { text: '#303133', regular: '#606266', border: '#e8eaec', split: '#ebeef5', overlay: '#ffffff' }
  }
  const styles = getComputedStyle(document.documentElement)
  const resolve = (token: string, fallback: string) => styles.getPropertyValue(token).trim() || fallback
  return {
    text: resolve('--text-color-primary', '#303133'),
    regular: resolve('--text-color-regular', '#606266'),
    border: resolve('--border-color', '#e8eaec'),
    split: resolve('--border-color-light', '#ebeef5'),
    overlay: resolve('--bg-color-overlay', '#ffffff'),
  }
}

// Light variants for gradients (mirror var(--el-color-*-light-3))
export const THEME_COLORS_LIGHT = {
  primary: '#79bbff',
  success: '#85ce61',
  warning: '#ebb563',
  danger: '#f78989',
  info: '#a6a9ad',
} as const

/**
 * Get a quality-based color string for JS contexts (ECharts, inline styles).
 * Uses unified thresholds across all pages.
 * - quality >= 80: success (green)
 * - quality >= 60: warning (orange)
 * - quality < 60: danger (red)
 */
export function getQualityColor(quality: number): string {
  const colors = getThemeColors()
  if (quality >= 80) return colors.success
  if (quality >= 60) return colors.warning
  return colors.danger
}

/**
 * Get a latency-based color string for JS contexts.
 * - ms < 50: success (green)
 * - ms < 200: warning (orange)
 * - ms >= 200: danger (red)
 */
export function getLatencyColor(ms: number): string {
  const colors = getThemeColors()
  if (ms < 50) return colors.success
  if (ms < 200) return colors.warning
  return colors.danger
}
