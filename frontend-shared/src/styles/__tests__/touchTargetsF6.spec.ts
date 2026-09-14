import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * F6 触控热区 —— 尺寸规则合同测试（规范 §4.4.5 MUST / §4.4.7）。
 *
 * 这层只守护「源码里到底写了什么」，**不能**代替浏览器实测：
 * 本仓已有血泪教训 —— theme.css 里曾新增 `.el-pagination .el-button { min-width:36px }`，
 * 被 Vite 的 CSS 压缩器（lightningcss，Vite 8 的 cssMinify 默认值）
 * **误判为 keyframes 名并整条丢弃**：源码有、构建成功无报错、产物里没有。
 * 因此本文件必须与两件事配套（见 .tmp-probe/ 与 .logs/f6-report.md）：
 *   1) tools/css-survival-check.mjs —— 读构建产物确认规则存活；
 *   2) .tmp-probe/f6-verify.mjs —— 真实 Chromium 里用 elementFromPoint 验收热区。
 *
 * 注：不能 `import '@/styles/theme.css?raw'`——vitest 默认不处理 CSS（css:false），
 * 该导入会被替换为空字符串，断言会永远"通过"。故直接从磁盘读取源文件。
 */
const themeCss = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

/** 抽取一个媒体查询块的完整文本（按花括号配平）。 */
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

/** 抽取一条顶层规则的声明体；selector 需与源码逐字一致。
 *  必须传"尺寸区"文本，否则会先命中暗色主题里同名的 .el-pagination 变量块。 */
function ruleBody(region: string, selector: string): string {
  // 尺寸区的第一条规则没有前导换行，故两种写法都要试
  let i = region.startsWith(selector + ' {') ? 0 : region.indexOf('\n' + selector + ' {')
  if (i === -1) return ''
  const j = region.indexOf('}', i)
  return j === -1 ? '' : region.slice(i + selector.length + 2, j)
}

/** 抽取尺寸区里**最后**一个匹配的媒体块（窄屏块在尺寸区出现过两次）。 */
function lastMediaBlock(region: string, header: string): string {
  const i = region.lastIndexOf(header)
  return i === -1 ? '' : mediaBlock(region.slice(i), header)
}

/** 本任务新增的尺寸区：从第 1 条规则到「隐藏滚动条」注释之间。 */
const SIZE_START = themeCss.indexOf('.el-table .el-button--small {')
const SIZE_END = themeCss.indexOf('/* 隐藏滚动条但保持滚动功能 */')
const sizeRegion = SIZE_START !== -1 && SIZE_END > SIZE_START ? themeCss.slice(SIZE_START, SIZE_END) : ''

const coarseBlock = mediaBlock(themeCss, '@media (pointer: coarse)')
const wideBlock = mediaBlock(themeCss, '@media (min-width: 769px)')
const narrowBlock = lastMediaBlock(sizeRegion, '@media (max-width: 768px)')

describe('F6 触控热区：尺寸规则合同', () => {
  it('读到了非空的 theme.css（防桩文件：分母自证）', () => {
    expect(themeCss.length).toBeGreaterThan(10000)
    expect(themeCss).toContain('--color-primary')
    // 尺寸区确实存在且非空，否则下面所有断言都会在空字符串上"假通过"
    expect(sizeRegion.length).toBeGreaterThan(500)
  })

  it('@media (pointer: coarse) 分支存在且覆盖主要交互元素（§4.4.7）', () => {
    expect(coarseBlock, '@media (pointer: coarse) 块缺失').not.toBe('')
    // 覆盖面：不是只加一条规则，主要交互元素都要在粗指针下补热区。
    for (const sel of [
      '.main-header .el-button.is-circle',
      '.main-header .user-menu',
      '.el-input__wrapper',
      '.el-select__wrapper',
      '.el-textarea__inner',
      '.el-table .el-button--small',
      '.el-table .el-checkbox',
      '.el-table .el-switch',
      '.el-pagination',
      '.el-input-number__increase',
      '.el-radio-button__inner',
      '.touch-target',
      '.el-button-group .el-button',
      '.el-dropdown-menu__item',
    ]) {
      expect(coarseBlock, `粗指针分支缺少 ${sel}`).toContain(sel)
    }
  })

  it('粗指针下正文控件升到 44px、高密度表格行内操作 >=36px', () => {
    expect(coarseBlock).toMatch(/\.el-input__wrapper,[\s\S]{0,120}min-height: 44px/)
    expect(coarseBlock).toMatch(/\.el-table \.el-button--small \{[^}]*min-height: 44px/)
    expect(coarseBlock).toMatch(/\.el-table \.el-button--small \{[^}]*min-width: 36px/)
    // 高密度工具栏例外仍是 36px 档
    expect(coarseBlock).toMatch(/\.el-button-group \.el-button \{[^}]*min-width: 36px/)
    expect(coarseBlock).toMatch(/\.el-button-group \.el-button \{[^}]*min-height: 36px/)
  })

  it('页头图标按钮 >=44px（审计实测 32x32；页头不是高密度工具栏）', () => {
    const body = ruleBody(sizeRegion, '.main-header .el-button.is-circle')
    expect(body, '.main-header .el-button.is-circle 规则缺失').not.toBe('')
    expect(body).toMatch(/min-width: 44px/)
    expect(body).toMatch(/min-height: 44px/)
    // 用户菜单是自研 div 控件，单独补
    const um = ruleBody(sizeRegion, '.main-header .user-menu')
    expect(um).toMatch(/min-height: 44px/)
  })

  it('表格行内 18px 小按钮（257/264 的单一根因）补到 >=36px', () => {
    const body = ruleBody(sizeRegion, '.el-table .el-button--small')
    expect(body, '.el-table .el-button--small 规则缺失').not.toBe('')
    expect(body).toMatch(/min-height: 36px/)
    expect(body).toMatch(/min-width: 36px/)
  })

  it('行高守恒：宽视口用 :has() 收紧「含小按钮/复选框」单元格的 padding（不改行高）', () => {
    expect(wideBlock, '@media (min-width: 769px) 块缺失').not.toBe('')
    expect(wideBlock).toContain('.el-table .el-table__cell:has(> .cell > .el-button--small)')
    expect(wideBlock).toContain('.el-table .el-table__cell:has(> .cell > .el-checkbox)')
    expect(wideBlock).toMatch(/padding-top: 1px/)
    expect(wideBlock).toMatch(/padding-bottom: 1px/)
  })

  it('超长文本链接（命令 ID）被夹在单元格内，中心点才命得中', () => {
    const body = ruleBody(sizeRegion, '.el-table .cell > .el-button')
    expect(body, '.el-table .cell > .el-button 规则缺失').not.toBe('')
    expect(body).toMatch(/max-width: 100%/)
  })

  it('分页、小号选择器、input-number、分段控件、行内可点文本都补足热区', () => {
    expect(ruleBody(sizeRegion, '.el-pagination')).toMatch(/--el-pagination-button-height: 36px/)
    expect(ruleBody(sizeRegion, '.el-pagination')).toMatch(/--el-pagination-button-height-small: 36px/)
    expect(ruleBody(sizeRegion, '.el-select--small .el-select__wrapper')).toMatch(/min-height: 36px/)
    expect(themeCss).toMatch(/\.el-input-number__increase,\s*\n\s*\.el-input-number__decrease \{[^}]*min-width: 36px/)
    expect(themeCss).toMatch(/\.el-radio-button__inner,\s*\n\s*\.el-checkbox-button__inner \{[^}]*min-height: 36px/)
    expect(themeCss).toMatch(/\.fact-value\.copyable,\s*\n\s*a\.device-link \{[^}]*padding-top: 10px/)
  })

  it('窄屏紧凑工具条按钮补到 36px', () => {
    expect(narrowBlock).toMatch(/\.el-button--small:not\(\.is-circle\) \{[^}]*min-height: 36px/)
  })

  it('反例守卫：本任务只加尺寸，不得削弱到 36px 以下，也不得靠伪元素撑热区', () => {
    // 尺寸区里不得出现小于 36px 的 min-width/min-height（32/24/22/18 都是被修掉的原值）
    const tooSmall = sizeRegion.match(/min-(?:width|height):\s*(?:1?[0-9]|2[0-9]|3[0-5])px/g)
    expect(tooSmall, '尺寸区出现了 <36px 的 min-width/min-height：' + String(tooSmall)).toBeNull()
    // 也不得整体放大行高/字号（裁决：不改视觉密度）
    expect(sizeRegion).not.toMatch(/line-height:/)
    expect(sizeRegion).not.toMatch(/font-size:/)
    // 伪元素方案已被实测否决（::after 在 el-table 单元格内被裁剪）
    expect(sizeRegion).not.toContain('::after')
    expect(sizeRegion).not.toContain('::before')
  })

  it('反例守卫：本任务新增块不得触碰颜色声明（颜色由别的任务负责）', () => {
    for (const prop of ['color:', 'background:', 'background-color:', 'border-color:', '--el-color-', '--color-']) {
      expect(sizeRegion.includes(prop), `尺寸区不应出现颜色声明 ${prop}`).toBe(false)
    }
  })
})