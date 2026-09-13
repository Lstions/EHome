import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import source from '../NodeOverview.vue?raw'

// 注意：vite/vitest 对 .css 的 ?raw 导入返回空串（已实测），故 theme.css 用 fs 直读源码。
// happy-dom 环境下 import.meta.url 非 file: 协议，故以 vitest 的工作目录（frontend-shared）定位。
const themeSource = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

/**
 * NodeOverview 页面级色板（--no-*）护栏。
 *
 * 判据口径（重要）：**浏览器实测是权威判据，本测试只是防回退护栏**。
 * 真实判据见 .tmp-probe/palette-contrast.mjs：在 1440px 下用
 * getComputedStyle(el).getPropertyValue('--no-x') 读亮/暗两主题的全部 --no-* 计算值，
 * 以卡片底 rgb(41,41,43) 计 WCAG 对比度。本测试只在源码层面复算同一套不变量，
 * 用来在源码改动时立刻变红，避免再退化。
 *
 * 解析口径：
 *  - 亮色 token 取自 NodeOverview.vue 的 .node-overview-page 块；
 *  - 暗色 token 取自同文件 html.dark .node-overview-page 块；
 *  - 暗色块里的 var(--x, fallback) 按「主题变量优先、无则 fallback」解析：
 *    主题变量取自 styles/theme.css 的 html.dark 块（与浏览器实际取值的优先级一致）。
 */

type RGB = [number, number, number]

/** 取出 selector 对应的第一个 { ... } 块（按大括号配对，避免嵌套误截） */
function extractBlock(css: string, selector: string): string {
  const at = css.indexOf(selector)
  if (at < 0) throw new Error('selector not found: ' + selector)
  const open = css.indexOf('{', at)
  if (open < 0) throw new Error('no block after selector: ' + selector)
  let depth = 0
  for (let i = open; i < css.length; i++) {
    if (css[i] === '{') depth++
    else if (css[i] === '}') {
      depth--
      if (depth === 0) return css.slice(open + 1, i)
    }
  }
  throw new Error('unterminated block: ' + selector)
}

/** 取出所有 selector 块并合并声明（后出现者覆盖先出现者） */
function extractAllBlocks(css: string, selector: string): string {
  const parts: string[] = []
  let from = 0
  for (;;) {
    const at = css.indexOf(selector, from)
    if (at < 0) break
    const rel = extractBlock(css.slice(at), selector)
    parts.push(rel)
    from = at + selector.length
  }
  return parts.join('\n')
}

/** 解析 --x: value; 形式的自定义属性声明 */
function declarations(block: string): Map<string, string> {
  const out = new Map<string, string>()
  for (const raw of block.split('\n')) {
    const m = /^\s*(--[a-z0-9-]+)\s*:\s*([^;]+);/i.exec(raw)
    if (m) out.set(m[1], m[2].trim())
  }
  return out
}

function parseColor(value: string): RGB | null {
  const v = value.trim().toLowerCase()
  let m = /^#([0-9a-f]{6})$/.exec(v)
  if (m) { const n = parseInt(m[1], 16); return [(n >> 16) & 255, (n >> 8) & 255, n & 255] }
  m = /^#([0-9a-f]{3})$/.exec(v)
  if (m) return [0, 1, 2].map(i => parseInt(m![1][i] + m![1][i], 16)) as RGB
  m = /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/.exec(v)
  if (m) return [Number(m[1]), Number(m[2]), Number(m[3])]
  return null
}

/** var(--x, fallback) 解析：主题变量优先，无则回退 fallback（与浏览器一致） */
function resolveColor(value: string, themeVars: Map<string, string>): RGB | null {
  const m = /^var\(\s*(--[a-z0-9-]+)\s*(?:,\s*([\s\S]+?)\s*)?\)$/i.exec(value.trim())
  if (m) {
    const name = m[1]
    if (themeVars.has(name)) return parseColor(themeVars.get(name)!)
    return m[2] ? resolveColor(m[2], themeVars) : null
  }
  return parseColor(value)
}

function relativeLuminance([r, g, b]: RGB): number {
  const f = (v: number) => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4) }
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
}

function contrast(fg: RGB, bg: RGB): number {
  const a = relativeLuminance(fg), b = relativeLuminance(bg)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

const light = declarations(extractBlock(source, '.node-overview-page'))
const dark = declarations(extractBlock(source, 'html.dark .node-overview-page'))
// 主题变量：theme.css 里所有 html.dark 块（含 [data-theme="dark"]/.dark-theme 选择器行）
const themeVars = declarations(extractAllBlocks(themeSource, 'html.dark'))

const TEXT_TOKENS = ['--no-text', '--no-text-secondary', '--no-text-muted', '--no-text-faint'] as const

describe('NodeOverview 页面色板 --no-* 暗色完整性护栏', () => {
  it('解析本身有效：亮/暗两块都能解析出完整 token（防止解析失败导致的假绿）', () => {
    expect(light.size).toBeGreaterThanOrEqual(19)
    expect(dark.size).toBeGreaterThanOrEqual(19)
    for (const t of TEXT_TOKENS) {
      expect(light.has(t), '亮色缺少 ' + t).toBe(true)
      expect(dark.has(t), '暗色缺少 ' + t).toBe(true)
    }
    expect(themeVars.has('--card-bg'), 'theme.css 暗色缺少 --card-bg').toBe(true)
    expect(themeVars.has('--color-danger')).toBe(true)
  })

  it('覆盖完整：亮色定义的每个 --no-* token，暗色块都必须覆盖', () => {
    const missing = [...light.keys()].filter(t => !dark.has(t))
    expect(missing, '暗色未覆盖的 token: ' + missing.join(', ')).toEqual([])
  })

  it('亮暗取值不同：每个 token 在暗色块都给出与亮色不同的值', () => {
    const same: string[] = []
    for (const [token, lightValue] of light) {
      if (dark.get(token) === lightValue) same.push(token + ' = ' + lightValue)
    }
    expect(same, '亮暗同值（层级/配色未随主题切换）: ' + same.join(', ')).toEqual([])
  })

  it('引用主题变量的 token：解析后仍必须与亮色不同（防止 var 指向同值变量）', () => {
    for (const [token, darkValue] of dark) {
      const lightValue = light.get(token)
      if (lightValue === undefined) continue
      if (!darkValue.startsWith('var(')) continue
      const resolved = resolveColor(darkValue, themeVars)
      expect(resolved, token + ' 的 ' + darkValue + ' 无法解析（主题变量与 fallback 都取不到色值）').not.toBeNull()
      const lightRgb = resolveColor(lightValue, themeVars)
      expect(lightRgb, token + ' 亮色值无法解析: ' + lightValue).not.toBeNull()
      expect(resolved, token + ' 解析值与亮色相同').not.toEqual(lightRgb)
    }
  })

  it('文字层级单调：对比度 text > secondary > muted > faint（卡片底）', () => {
    const cardBg = parseColor(themeVars.get('--card-bg')!)
    expect(cardBg, 'theme.css 暗色 --card-bg 无法解析').not.toBeNull()
    const c: Record<string, number> = {}
    for (const t of TEXT_TOKENS) {
      const rgb = resolveColor(dark.get(t)!, themeVars)
      expect(rgb, t + ' 暗色值无法解析: ' + dark.get(t)).not.toBeNull()
      c[t] = contrast(rgb!, cardBg!)
    }
    // 与浏览器实测同口径：12.03 / 7.23 / 5.96 / 3.95（卡片底 rgb(41,41,43)）
    expect(c['--no-text'], 'text > secondary').toBeGreaterThan(c['--no-text-secondary'])
    expect(c['--no-text-secondary'], 'secondary > muted').toBeGreaterThan(c['--no-text-muted'])
    expect(c['--no-text-muted'], 'muted > faint（此前的层级反转在此变红）').toBeGreaterThan(c['--no-text-faint'])
    expect(c['--no-text-faint'], 'faint 为禁用态，仍需 >= 3.0').toBeGreaterThanOrEqual(3.0)
  })

  it('亮色块保持设计稿原值（不得被暗色改动波及）', () => {
    expect(light.get('--no-text-faint')).toBe('#A7B1BF')
    expect(light.get('--no-success')).toBe('#22C55E')
    expect(light.get('--no-warning')).toBe('#F59E0B')
    expect(light.get('--no-danger')).toBe('#EF4444')
    expect(light.get('--no-bg-page')).toBe('#F5F7FA')
  })
})
