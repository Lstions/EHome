import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import mainLayout from '@/views/layout/MainLayout.vue?raw'
import themeSwitch from '@/components/common/ThemeSwitch.vue?raw'

/**
 * 触控目标合同（规范 §4.4.5 MUST / §4.4.7 SHOULD）。
 *
 * 这是 CSS/模板源码合同测试：断言“放大策略确实写在样式里”，
 * 与 Playwright 实测（390/360 下 getBoundingClientRect + 真实 mouse.click /
 * touchscreen.tap 落在原视觉盒之外仍生效）互补。
 * 之所以不用伪元素方案：实测 ::after 在 el-table 单元格内会被裁剪，
 * elementFromPoint 与真实指针都点不到视觉盒之外，故必须用真实布局盒。
 *
 * 注：不能 `import '@/styles/theme.css?raw'`——vitest 默认不处理 CSS（css:false），
 * 该导入会被替换为空字符串，断言会永远“通过”。故直接从磁盘读取源文件。
 */
const themeCss = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

function mediaBlock(css: string, header: string): string {
  const start = css.indexOf(header)
  if (start === -1) return ''
  let depth = 0
  for (let i = start; i < css.length; i += 1) {
    if (css[i] === '{') depth += 1
    else if (css[i] === '}') {
      depth -= 1
      if (depth === 0) return css.slice(start, i + 1)
    }
  }
  return ''
}

const coarseBlock = mediaBlock(themeCss, '@media (pointer: coarse)')
const mobileBlocks = themeCss.split(/\n(?=@media)/).filter(b => b.includes('max-width: 768px')).join('\n')

describe('touch target contract', () => {
  it('reads a non-empty theme.css (guards against a stubbed/empty fixture)', () => {
    expect(themeCss.length).toBeGreaterThan(1000)
    expect(themeCss).toContain('.mobile-table-wrapper')
  })

  it('defines .touch-target with a real >=36px layout box (not a pseudo-element)', () => {
    expect(themeCss).toContain('.touch-target {')
    expect(themeCss).toMatch(/\.touch-target \{[^}]*min-width: 36px/)
    expect(themeCss).toMatch(/\.touch-target \{[^}]*min-height: 36px/)
    // 伪元素方案已被实测否决：不得再靠 ::after 撑热区
    expect(themeCss).not.toContain('.touch-target::after')
  })

  it('raises .touch-target to 44px on narrow screens and for coarse pointers', () => {
    expect(mobileBlocks).toMatch(/\.touch-target \{[^}]*min-height: 44px/)
    expect(coarseBlock).toMatch(/\.touch-target \{[^}]*min-height: 44px/)
  })

  it('implements the §4.4.7 pointer:coarse branch at all', () => {
    expect(coarseBlock).not.toBe('')
    expect(coarseBlock).toContain('.touch-target')
    expect(coarseBlock).toContain('min-height: 44px')
  })

  it('raises the dense table row buttons (the 257-target root cause) to >=36px on narrow screens', () => {
    // 审计量化：automation 页 257 个 <36px 目标全部来自表格行内 30×18 的小按钮。
    // 这些页面属其他任务，本任务只在全局补尺寸规则。
    expect(mobileBlocks).toMatch(/\.el-table \.el-button--small \{[^}]*min-height: 36px/)
  })

  it('raises mobile form inputs to >=44px (primary mobile input targets)', () => {
    expect(mobileBlocks).toMatch(/\.el-input__wrapper,\s*\n?\s*\.el-select__wrapper,/)
    expect(mobileBlocks).toMatch(/\.el-textarea__inner \{[^}]*min-height: 44px/)
  })

  it('never shrinks the dense table row buttons below the §4.4.5 36px floor', () => {
    // 反例守卫：不得出现 <36px 的表格行按钮规则
    const tableSmall = mobileBlocks.match(/\.el-table \.el-button--small \{[^}]*\}/)
    expect(tableSmall).not.toBeNull()
    expect(tableSmall![0]).not.toMatch(/min-height: (1?[0-9]|2[0-9]|3[0-5])px/)
  })

  it('sizes the mobile header icon buttons to 44x44 as real boxes', () => {
    // 页头三个图标按钮：汉堡(MainLayout) + 通知铃铛(MainLayout) + 主题(ThemeSwitch)
    const mainHeaderBlock = mediaBlock(mainLayout, '@media (max-width: 768px)')
    expect(mainHeaderBlock).toMatch(/\.main-header \.el-button\.is-circle \{[^}]*min-width: 44px/)
    expect(mainHeaderBlock).toMatch(/\.main-header \.el-button\.is-circle \{[^}]*min-height: 44px/)

    const switchBlock = mediaBlock(themeSwitch, '@media (max-width: 768px)')
    expect(switchBlock).toMatch(/\.el-button\.is-circle \{[^}]*min-width: 44px/)
    expect(switchBlock).toMatch(/\.el-button\.is-circle \{[^}]*min-height: 44px/)
  })

  it('wires the shared .touch-target class onto the row action buttons of NodeList', async () => {
    const nodeList = (await import('@/views/node/NodeList.vue?raw')).default
    expect(nodeList).toContain('class="touch-target"')
    // 删除按钮同时要有可访问名称（对象身份）
    expect(nodeList).toContain(':aria-label=')
    expect(nodeList).toContain('删除 ${row.name}')
  })
})
