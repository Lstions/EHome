import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import BmsMosStatus from '../BmsMosStatus.vue'

/**
 * F17：BMS MOS 状态图标配色必须走语义 token（规范 §3.6.2 —— 色值取自 token 层）。
 *
 * 此前用 THEME_COLORS.success/info 的十六进制亮色常量，
 * 暗色主题下图标颜色不变（不响应主题）。
 *
 * 分工说明：happy-dom（vitest 环境）**不做** var() 替换，
 * 因此这一层能给出的最强断言是「传给 el-icon 的 :color 确实是 var(--color-*) 引用」；
 * 「该 token 在亮/暗下确实取不同值」由 src/styles/__tests__/SidebarThemeTokens.spec.ts
 * 的 token 解析断言与 .tmp-probe 下的真实浏览器探针覆盖。
 * 全局 el-icon stub（src/test-setup.ts）把 color 透传到 style 上。
 */
describe('BmsMosStatus 语义 token 配色', () => {
  const mountWith = (data: Record<string, number> | null) => mount(BmsMosStatus, { props: { data } })

  it('充电/放电 MOS 导通时图标用 var(--color-success)，断开时用 var(--color-info)', () => {
    const both = mountWith({ fet_status: 3 })
    const icons = both.findAll('.el-icon')
    expect(icons.length).toBeGreaterThanOrEqual(2)
    expect(icons[0].attributes('style')).toContain('var(--color-success)')
    expect(icons[1].attributes('style')).toContain('var(--color-success)')

    const off = mountWith({ fet_status: 0 })
    const offIcons = off.findAll('.el-icon')
    expect(offIcons[0].attributes('style')).toContain('var(--color-info)')
    expect(offIcons[1].attributes('style')).toContain('var(--color-info)')
  })

  it('无 FET 状态数据时提示图标用 var(--color-info)', () => {
    const wrapper = mountWith(null)
    const icons = wrapper.findAll('.el-icon')
    expect(icons.length).toBeGreaterThan(0)
    expect(icons[icons.length - 1].attributes('style')).toContain('var(--color-info)')
  })

  it('所有图标配色都是 var(--color-*) 引用，不含任何硬编码色值（防回退守卫）', () => {
    for (const data of [{ fet_status: 3 }, { fet_status: 0 }, null]) {
      const wrapper = mountWith(data)
      const icons = wrapper.findAll('.el-icon')
      expect(icons.length, 'stub 未渲染出图标，断言会假绿').toBeGreaterThan(0)
      for (const icon of icons) {
        const style = icon.attributes('style') || ''
        expect(style, '图标缺少 color').toMatch(/color:\s*var\(--color-[a-z]+\)/)
        expect(style, '图标配色含硬编码色值：' + style).not.toMatch(/#[0-9a-fA-F]{3,8}|rgb\(/)
      }
    }
  })
})
