import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * 设备类型图标「浅底 + 语义前景」对比度合同（2026-10-17）。
 *
 * 背景：图标全站统一时把「基色文字 + --el-color-*-light-9 底」这个范式推进了
 * 全局样式。亮色下五个语义色都达标，但**暗色下 primary 只有 2.70** ——
 * 基色 #1f5ad8 是为「白字落在其上 ≥4.5」选的（够深），同一颜色作文字落在
 * 深底 #18222b 上就不够了。这与 theme.css 里 --el-tag-primary-text 是同一个
 * 结论，故图标前景拆成独立 token --icon-*，亮暗各自取值。
 *
 * 本测试是**源码级 + 算术级**护栏（不含浏览器）：
 *   ① token 层：亮暗两块都必须定义全部 --icon-*，且取值不同（否则没做主题化）；
 *   ② 算术层：用 WCAG 相对亮度公式复算「--icon-X on --el-color-X-light-9」≥4.5；
 *   ③ 防回退：.device-type-icon 的 .tone-* 不得再写 color: var(--el-color-*) 基色
 *      （那正是暗色 2.70 的成因）。
 * 真实渲染仍以浏览器实测为准。
 */

const ROOT = process.cwd()
const THEME = readFileSync(resolve(ROOT, 'src/styles/theme.css'), 'utf8')

/**
 * 取暗色主题段的文本。
 * 不用「取到第一个 }」——暗色是**多个连续块**（基础色块、暗色安全网块、
 * el-tag 前景块），第一个 } 之后仍有 --el-color-*-light-9 / --el-tag-primary-text，
 * 截断会误报「缺少变量」。故取从暗色入口到 Element Plus 桥接段之前。
 */
function darkSection(): string {
  const i = THEME.indexOf('[data-theme="dark"]')
  expect(i, '找不到暗色主题段').toBeGreaterThan(-1)
  const marker = THEME.indexOf(':root {', i)
  return THEME.slice(i, marker === -1 ? THEME.length : marker)
}

/**
 * 取某个变量的**最后一处**声明值（!! 去重后取末次）。
 * 为什么取末次：本文件里 --el-color-*-light-9 在亮色侧有意声明两次
 * （原 :root 块 + 文件末尾带 !important 的高优先级重申），后者才是实际生效值。
 */
function lastVarOf(text: string, name: string): string {
  const re = new RegExp(name.replace(/[-]/g, '\\-') + ':\\s*(#[0-9a-fA-F]{6})', 'g')
  const all = [...text.matchAll(re)].map((m) => m[1].toLowerCase())
  expect(all.length, '缺少变量 ' + name).toBeGreaterThan(0)
  return all[all.length - 1]
}

/** 从文本里取一个 CSS 变量的值（去注释、去空格）。 */
function varOf(text: string, name: string): string {
  const m = text.match(new RegExp(name.replace(/[-]/g, '\\-') + ':\\s*(#[0-9a-fA-F]{6})'))
  expect(m, '缺少变量 ' + name).not.toBeNull()
  return (m as RegExpMatchArray)[1].toLowerCase()
}

/** WCAG 2.x 相对亮度。 */
function luminance(hex: string): number {
  const c = hex.replace('#', '')
  const toLin = (v: number) => (v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4))
  const r = toLin(parseInt(c.slice(0, 2), 16) / 255)
  const g = toLin(parseInt(c.slice(2, 4), 16) / 255)
  const b = toLin(parseInt(c.slice(4, 6), 16) / 255)
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(a: string, b: string): number {
  const l1 = luminance(a)
  const l2 = luminance(b)
  const hi = Math.max(l1, l2)
  const lo = Math.min(l1, l2)
  return (hi + 0.05) / (lo + 0.05)
}

const TONES = ['primary', 'success', 'warning', 'danger', 'info'] as const

const lightBlock = THEME.slice(0, THEME.indexOf('[data-theme="dark"]'))
const darkBlock = darkSection()

describe('设备类型图标：浅底 + 语义前景对比度合同', () => {
  it('token 层：亮暗都定义全部 --icon-*，且取值不同', () => {
    for (const tone of TONES) {
      const name = '--icon-' + tone
      const lv = varOf(lightBlock, name)
      const dv = varOf(darkBlock, name)
      expect(lv, name + ' 亮暗取值相同 = 没做主题化').not.toBe(dv)
    }
  })

  it('算术层：亮色下「--icon-X on --el-color-X-light-9」≥ 4.5', () => {
    const failures: string[] = []
    for (const tone of TONES) {
      const fg = varOf(lightBlock, '--icon-' + tone)
      // light-9 在亮色侧声明两次（原 :root + 末尾 !important 重申），取生效的末次
      const bg = lastVarOf(lightBlock, '--el-color-' + tone + '-light-9')
      const ratio = contrast(fg, bg)
      if (ratio < 4.5) failures.push(`亮色 ${tone}: ${fg} on ${bg} = ${ratio.toFixed(2)}`)
    }
    expect(failures, failures.join('\n')).toEqual([])
  })

  it('算术层：暗色下「--icon-X on --el-color-X-light-9」≥ 4.5', () => {
    const failures: string[] = []
    for (const tone of TONES) {
      const fg = varOf(darkBlock, '--icon-' + tone)
      const bg = lastVarOf(darkBlock, '--el-color-' + tone + '-light-9')
      const ratio = contrast(fg, bg)
      if (ratio < 4.5) failures.push(`暗色 ${tone}: ${fg} on ${bg} = ${ratio.toFixed(2)}`)
    }
    expect(failures, failures.join('\n')).toEqual([])
  })

  it('防回退：.tone-* 不得再用语义基色作前景（暗色 primary 只有 2.70）', () => {
    const i = THEME.indexOf('.device-type-icon.tone-primary')
    expect(i, '找不到 .device-type-icon.tone-primary 规则').toBeGreaterThan(-1)
    const end = THEME.indexOf('\n}', i)
    const rule = THEME.slice(i, end === -1 ? THEME.length : end)
    // 必须是 var(--icon-primary)，不得是 var(--el-color-primary)
    expect(rule).toMatch(/color:\s*var\(--icon-primary\)/)
    expect(rule, '用语义基色作前景会在暗色下掉到 2.70').not.toMatch(/color:\s*var\(--el-color-primary\)/)
  })

  it('亮色 light-9 必须用 !important 压过 EP 后注入的同特异性 :root', () => {
    // 实测背景：element-plus 默认 :root 与 theme.css 同为 (0,1,0)，
    // 它**后注入**，故 theme.css 里不带 !important 的 light-9 全部失效，
    // computed 退回 EP 默认（success #f0f9eb 而非项目调优的 #e8f2eb）。
    // 本用例把这个结论钉住：亮色重申块必须带 !important。
    const i = THEME.indexOf(':root:not(.dark):not([data-theme="dark"])')
    const alt = THEME.indexOf(':root:root:not(')
    const start = i === -1 ? alt : i
    expect(start, '找不到亮色 light-9 重申块').toBeGreaterThan(-1)
    const end = THEME.indexOf('\n}', start)
    const block = THEME.slice(start, end === -1 ? THEME.length : end)
    for (const tone of TONES) {
      expect(block, tone + ' 的 light-9 重申缺少 !important，会被 EP 默认档盖掉').toMatch(
        new RegExp('--el-color-' + tone + '-light-9:\\s*#[0-9a-fA-F]{6}\\s*!important'),
      )
    }
  })

  it('暗色 light-9 也必须带 !important，且选择器覆盖三个暗色入口', () => {
    // 与文件里 --el-fill-color 等 9 个变量的做法一致：显式列举三入口 + !important。
    // 只挂 html.dark 时，单靠 data-theme="dark" 进入的用户会落回 EP 默认。
    const i = THEME.indexOf('Element Plus 暗色主题覆盖')
    expect(i, '找不到暗色覆盖段').toBeGreaterThan(-1)
    const header = THEME.slice(i, THEME.indexOf('{', i))
    expect(header, '暗色覆盖段应同时列 [data-theme=dark] / .dark-theme / html.dark').toMatch(/\[data-theme="dark"\]/)
    expect(header).toMatch(/html\.dark/)
    for (const tone of TONES) {
      expect(THEME, '暗色 ' + tone + ' light-9 缺少 !important').toMatch(
        new RegExp('--el-color-' + tone + '-light-9:\\s*#[0-9a-fA-F]{6}\\s*!important'),
      )
    }
  })

  it('暗色 primary 前景与 --el-tag-primary-text 取同一值（避免第二套约定）', () => {
    // --el-tag-primary-text 定义在暗色块内，但位置在 --icon-* 之后；
    // 直接用 varOf 扫全文取值即可，不依赖块切片边界。
    const iconPrimary = varOf(darkBlock, '--icon-primary')
    const tagText = varOf(THEME, '--el-tag-primary-text')
    expect(iconPrimary, '暗色 primary 图标前景应与既有 --el-tag-primary-text 同值').toBe(tagText)
  })
})
