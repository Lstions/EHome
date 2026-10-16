import { describe, expect, it } from 'vitest'
import routerSource from '@/router/index.ts?raw'
import demoSource from '@/dev/BmsDemoPage.vue?raw'

/**
 * BMS 设计稿 demo 页门禁契约（2026-08-17）：
 * - /dev/bms-demo 仅在 import.meta.env.DEV 下注册（生产构建静态消除）
 * - 页面带 DEV 水印，防止被误当生产页
 */
describe('BmsDemoPage DEV gating', () => {
  it('registers /dev/bms-demo inside the import.meta.env.DEV guard', () => {
    expect(routerSource).toContain("path: '/dev/bms-demo'")
    expect(routerSource).toContain("import('@/dev/BmsDemoPage.vue')")
    // 与 MockBmsPanel 同一 DEV 条件展开块。
    // 2026-10-16：router 里出现了第二个 DEV 展开块（MainLayout 下的 /dev/edge-device-cards），
    // 原非贪婪匹配只取第一个块 => 断言失靶。改为全局匹配后挑出含本路由的那一块。
    const guards = routerSource.match(/import\.meta\.env\.DEV \? \[[\s\S]*?\] : \[\]/g) ?? []
    expect(guards.some((g) => g.includes('/dev/bms-demo'))).toBe(true)
  })

  it('shows a visible DEV watermark banner', () => {
    expect(demoSource).toContain('dev-watermark')
    expect(demoSource).toContain('DEV DEMO')
    expect(demoSource).toContain('mock')
  })
})
