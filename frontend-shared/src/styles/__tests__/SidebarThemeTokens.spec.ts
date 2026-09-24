import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import layoutSource from '@/views/layout/MainLayout.vue?raw'

/**
 * F16 侧栏主题 token 合同（规范 §3.6.1 主题变量化 / §3.6.2 色值取自 token 层 / §3.6.5 亮暗双主题）。
 *
 * 判据口径（重要）：**真实浏览器实测是权威判据**，见 .tmp-probe/f16-sidebar-theme.mjs
 * （在 8082 实例上 loginViaApi → 切亮/暗 → 读 getComputedStyle 逐字符值）。
 * 本测试是**源码层防回退护栏**：它复算浏览器会做的那次 var() 解析，
 * 从而在"有人把 token 换回硬编码"时立刻变红，不需要起浏览器。
 *
 * 为什么必须自己解析 var()：vitest 的 happy-dom 环境**不做**元素样式上的 var() 替换
 * （已实测：getComputedStyle(el).backgroundImage 恒为空串），
 * 但**能**正确解析自定义属性的定义值（getComputedStyle(documentElement).getPropertyValue('--x')）。
 * 故这里取"定义值"一侧，按 CSS 的 var() 语义手动代入声明 —— 与浏览器结果一致。
 *
 * 也不能 import '@/styles/theme.css?raw'：vitest 默认 css:false，该导入是空串，
 * 断言会永远通过（"伪装成正常"）。必须 fs 直读。
 */
const themeCss = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

/** 取出 selector 对应的第一个 { ... } 块（按大括号配对） */
function block(css: string, selector: string): string {
  const at = css.indexOf(selector)
  if (at < 0) throw new Error('selector not found: ' + selector)
  const open = css.indexOf('{', at)
  if (open < 0) throw new Error('no block after selector: ' + selector)
  let depth = 0
  for (let i = open; i < css.length; i += 1) {
    if (css[i] === '{') depth += 1
    else if (css[i] === '}') {
      depth -= 1
      if (depth === 0) return css.slice(open + 1, i)
    }
  }
  throw new Error('unterminated block: ' + selector)
}

/** 取出所有匹配 selector 的块并合并声明（后出现者覆盖先出现者） */
function allBlocks(css: string, selector: string): Map<string, string> {
  const out = new Map<string, string>()
  let from = 0
  for (;;) {
    const at = css.indexOf(selector, from)
    if (at < 0) return out
    const open = css.indexOf('{', at)
    if (open < 0) return out
    let depth = 0
    let end = -1
    for (let i = open; i < css.length; i += 1) {
      if (css[i] === '{') depth += 1
      else if (css[i] === '}') {
        depth -= 1
        if (depth === 0) { end = i; break }
      }
    }
    if (end < 0) return out
    for (const m of css.slice(open + 1, end).matchAll(/(--[a-zA-Z0-9-]+)\s*:\s*([^;]+);/g)) {
      out.set(m[1], m[2].trim())
    }
    from = end + 1
  }
}

// 亮色 token 定义在第一个 :root 块里（theme.css 另有 EP 桥接的 :root 块，此处只取项目语义 token 块）
const light = new Map<string, string>()
for (const m of block(themeCss, ':root').matchAll(/(--[a-zA-Z0-9-]+)\s*:\s*([^;]+);/g)) light.set(m[1], m[2].trim())
// 暗色 token 定义在 html.dark 块里（theme.css 有多个 html.dark 块，合并取并集）
const dark = allBlocks(themeCss, 'html.dark')

/** 侧栏 token：必须在亮/暗两套都定义，且（除显式豁免外）取值不同 */
const SIDEBAR_TOKENS = [
  '--sidebar-bg',
  '--sidebar-bg-gradient',
  '--sidebar-text',
  '--sidebar-hover-bg',
  '--sidebar-active-bg',
  '--sidebar-active-color',
  '--sidebar-border',
] as const

/** 豁免：hover 文字固定纯白，深色侧栏上两主题同值是有意的（见 theme.css 注释） */
const SAME_VALUE_BY_DESIGN = ['--sidebar-text-hover'] as const

/**
 * 取 .sidebar 规则块里的某个声明，并按 CSS var() 语义代入主题值。
 * 若声明是硬编码字面量，则亮/暗返回同一个字符串 —— 这正是 F16 要被测出来的缺陷形态。
 */
function sidebarDecl(prop: string, theme: 'light' | 'dark'): string {
  const sidebarBlock = block(layoutSource, '.sidebar {')
  const m = sidebarBlock.match(new RegExp('(?:^|[;{\\s])' + prop + '\\s*:\\s*([^;]+);'))
  if (!m) throw new Error('declaration not found in .sidebar: ' + prop)
  const decl = m[1].trim()
  const vars = theme === 'light' ? light : dark
  // var(--a, fallback) / var(--a, var(--b, ...)) —— 逐个代入，fallback 仅在 token 缺失时生效
  let out = decl
  for (let i = 0; i < 8 && out.includes('var('); i += 1) {
    out = out.replace(/var\((--[a-zA-Z0-9-]+)\s*(?:,\s*([^()]*(?:\([^()]*\)[^()]*)*))?\)/g, (_all, name: string, fb?: string) => {
      const v = vars.get(name)
      if (v !== undefined) return v
      return fb !== undefined ? fb.trim() : ''
    })
  }
  return out.replace(/\s+/g, ' ').trim()
}

// ── WCAG 2.x 相对亮度对比度（与 tools/theme-contrast-measure.mjs 同口径） ──
type RGB = [number, number, number]
const chan = (c: number) => { const s = c / 255; return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4) }
const lum = ([r, g, b]: RGB) => 0.2126 * chan(r) + 0.7152 * chan(g) + 0.0722 * chan(b)
function contrast(fg: RGB, bg: RGB): number {
  const a = lum(fg), b = lum(bg)
  const hi = Math.max(a, b), lo = Math.min(a, b)
  return (hi + 0.05) / (lo + 0.05)
}
function parseColor(v: string): RGB {
  const s = v.trim()
  if (s.startsWith('#')) {
    const h = s.length === 4 ? s.slice(1).split('').map(c => c + c).join('') : s.slice(1)
    return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)]
  }
  const m = s.match(/rgba?\(([^)]+)\)/)
  if (!m) throw new Error('unsupported color: ' + v)
  const p = m[1].split(',').map(x => parseFloat(x.trim()))
  return [p[0], p[1], p[2]]
}
function alphaOf(v: string): number {
  const m = v.trim().match(/rgba\(([^)]+)\)/)
  if (!m) return 1
  const p = m[1].split(',')
  return p.length > 3 ? parseFloat(p[3]) : 1
}
const over = (fg: RGB, a: number, bg: RGB): RGB => fg.map((c, i) => Math.round(c * a + bg[i] * (1 - a))) as RGB
/** 在渐变上按位置取色（线性插值） */
const atStop = (a: RGB, b: RGB, t: number): RGB => a.map((c, i) => Math.round(c + (b[i] - c) * t)) as RGB
/** 从 linear-gradient(...) 声明里取出两个端点色 */
function gradientStops(decl: string): [RGB, RGB] {
  const cols = [...decl.matchAll(/(#[0-9a-fA-F]{3,8}|rgba?\([^)]*\))/g)].map(m => m[1])
  if (cols.length < 2) throw new Error('gradient needs >=2 stops: ' + decl)
  return [parseColor(cols[0]), parseColor(cols[1])]
}

describe('F16 侧栏主题 token 合同', () => {
  it('解析本身有效（防止解析失败导致的假绿）', () => {
    expect(themeCss.length).toBeGreaterThan(1000)
    expect(light.size).toBeGreaterThan(30)
    expect(dark.size).toBeGreaterThan(30)
    expect(layoutSource).toContain('.sidebar {')
  })

  it('亮/暗两块都定义了全部侧栏 token', () => {
    for (const t of [...SIDEBAR_TOKENS, ...SAME_VALUE_BY_DESIGN]) {
      expect(light.has(t), '亮色缺少 ' + t).toBe(true)
      expect(dark.has(t), '暗色缺少 ' + t).toBe(true)
    }
  })

  it('侧栏 token 亮/暗取值不同（除按规范豁免的纯白 hover 文字）', () => {
    for (const t of SIDEBAR_TOKENS) {
      expect(light.get(t), t + ' 亮暗同值，等于没做主题化').not.toBe(dark.get(t))
    }
    for (const t of SAME_VALUE_BY_DESIGN) {
      expect(light.get(t)).toBe('#ffffff')
      expect(dark.get(t)).toBe('#ffffff')
    }
  })

  it('侧栏 background 消费 --sidebar-bg-gradient 而非硬编码（F16 核心）', () => {
    const sidebarBlock = block(layoutSource, '.sidebar {')
    expect(sidebarBlock).toMatch(/background:\s*var\(--sidebar-bg-gradient/)
    // 硬编码渐变色不得再出现为 .sidebar 的背景值
    expect(sidebarBlock).not.toMatch(/background:\s*linear-gradient\(180deg, #1a1f2e/)
  })

  it('亮/暗两主题下 .sidebar 的解析后 background 不同（浏览器判据的源码级复算）', () => {
    const l = sidebarDecl('background', 'light')
    const d = sidebarDecl('background', 'dark')
    expect(l).toContain('linear-gradient')
    expect(d).toContain('linear-gradient')
    // 这是 F16 的红色判据：改回硬编码时 l === d
    expect(l, '侧栏背景亮暗相同 = F16 回归').not.toBe(d)
    expect(l).toBe(light.get('--sidebar-bg-gradient'))
    expect(d).toBe(dark.get('--sidebar-bg-gradient'))
  })

  it('logo-area / sidebar-footer 的分隔线亮暗不同（--sidebar-border 已接线）', () => {
    const logoBorder = block(layoutSource, '.sidebar .logo-area {')
    const footBorder = block(layoutSource, '.sidebar .sidebar-footer {')
    expect(logoBorder).toMatch(/border-bottom:\s*1px solid var\(--sidebar-border/)
    expect(footBorder).toMatch(/border-top:\s*1px solid var\(--sidebar-border/)
    expect(light.get('--sidebar-border')).not.toBe(dark.get('--sidebar-border'))
  })

  it('菜单项默认文字亮暗不同（--sidebar-text 已接线）', () => {
    const menu = block(layoutSource, '.sidebar :deep(.el-menu-item) {')
    expect(menu).toMatch(/color:\s*var\(--sidebar-text/)
    expect(light.get('--sidebar-text')).not.toBe(dark.get('--sidebar-text'))
  })

  it('菜单项 hover 背景亮暗不同（--sidebar-hover-bg 已接线，不得回落到不存在的 token）', () => {
    const hover = block(layoutSource, '.sidebar :deep(.el-menu-item:hover) {')
    expect(hover).toMatch(/background:\s*var\(--sidebar-hover-bg/)
    expect(light.has('--sidebar-hover-bg'), '--sidebar-hover-bg 必须在 theme.css 亮色块定义').toBe(true)
    expect(dark.has('--sidebar-hover-bg'), '--sidebar-hover-bg 必须在 theme.css 暗色块定义').toBe(true)
    expect(light.get('--sidebar-hover-bg')).not.toBe(dark.get('--sidebar-hover-bg'))
  })

  it('活动菜单项 color/background 都接线到侧栏 token 且亮暗不同', () => {
    const active = block(layoutSource, '.sidebar :deep(.el-menu-item.is-active) {')
    expect(active).toMatch(/color:\s*var\(--sidebar-active-color/)
    expect(active).toMatch(/background:\s*var\(--sidebar-active-bg/)
    expect(light.get('--sidebar-active-color')).not.toBe(dark.get('--sidebar-active-color'))
    expect(light.get('--sidebar-active-bg')).not.toBe(dark.get('--sidebar-active-bg'))
  })

  it('活动菜单项文字在亮/暗两主题下均达 WCAG AA 4.5:1（真实缺陷回归护栏）', () => {
    // 主控真实像素采样：修复前 2.34:1（text #1f5ad8 vs 侧栏实测底 rgb(30,44,69)）。
    // 这里按浏览器口径复算：文字色 vs（--sidebar-active-bg 叠加在侧栏渐变上的合成色），
    // 并遍历整个渐变区间取**最差**位置（保守口径），避免只测端点造成假绿。
    for (const theme of ['light', 'dark'] as const) {
      const vars = theme === 'light' ? light : dark
      const text = parseColor(vars.get('--sidebar-active-color')!)
      const activeBg = vars.get('--sidebar-active-bg')!
      const bgColor = parseColor(activeBg)
      const bgAlpha = alphaOf(activeBg)
      const [s0, s1] = gradientStops(vars.get('--sidebar-bg-gradient')!)
      let worst = Infinity
      let worstBg: RGB = s0
      for (let t = 0; t <= 1.0001; t += 0.02) {
        const composed = over(bgColor, bgAlpha, atStop(s0, s1, t))
        const r = contrast(text, composed)
        if (r < worst) { worst = r; worstBg = composed }
      }
      expect(
        worst,
        theme + ' 主题活动菜单项对比度 ' + worst.toFixed(2) + ':1（fg ' + vars.get('--sidebar-active-color') +
          ' vs bg rgb(' + worstBg.join(',') + ')）低于 AA 4.5'
      ).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('分组标题在亮/暗两主题下均达 WCAG AA 4.5:1（Phase 2.4 分组后新增护栏）', () => {
    // 背景：Phase 2.4 把 14 项平铺菜单改成 5 组 el-sub-menu，但分组标题
    // （`.el-sub-menu__title`）**没有配色** ⇒ 继承 EP 默认 rgb(48,49,51)（近黑），
    // 在深色侧栏上几乎不可见（用户截图复现）。修复时给 token 赋值，此处按同一口径复算。
    //
    // α 取值是用本函数算出来的，不是目测：0.45 → 4.25:1（**不合格**，初版就是它）、
    // 0.50 → 4.90:1（刚过线）、0.55 → 5.62:1（采用）。若将来有人调低 α，本用例会红。
    for (const theme of ['light', 'dark'] as const) {
      const vars = theme === 'light' ? light : dark
      const raw = vars.get('--sidebar-group-title')
      expect(raw, '--sidebar-group-title 未定义').toBeTruthy()
      const text = parseColor(raw!)
      const a = alphaOf(raw!)
      const [s0, s1] = gradientStops(vars.get('--sidebar-bg-gradient')!)
      let worst = Infinity
      let worstBg: RGB = s0
      // 分组标题是半透明白**直接压在侧栏渐变上**（不像活动项还有 active-bg 叠一层），
      // 故合成基准就是渐变本身，遍历全程取最差位置（保守口径）。
      for (let t = 0; t <= 1.0001; t += 0.02) {
        const bgStop = atStop(s0, s1, t)
        const composed = over(text, a, bgStop)
        const r = contrast(composed, bgStop)
        if (r < worst) { worst = r; worstBg = composed }
      }
      expect(
        worst,
        theme + ' 主题分组标题对比度 ' + worst.toFixed(2) + ':1（' + raw +
          ' vs rgb(' + worstBg.join(',') + ')）低于 AA 4.5'
      ).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('侧栏样式里每个 var(--x, fallback) 的 --x 都真的在 theme.css 定义（"引用了不存在的 token"比硬编码更隐蔽）', () => {
    const sidebarRegion = layoutSource.slice(
      layoutSource.indexOf('/* ========== 侧边栏'),
      layoutSource.indexOf('/* ========== 右侧容器'),
    )
    const referenced = new Set<string>()
    for (const m of sidebarRegion.matchAll(/var\((--[a-zA-Z0-9-]+)/g)) referenced.add(m[1])
    expect(referenced.size, '侧栏未引用任何 token，说明解析区域取错了').toBeGreaterThan(3)
    const missing: string[] = []
    for (const t of referenced) {
      // --el-* 由 Element Plus 自身提供，不属于 theme.css 的职责
      if (t.startsWith('--el-')) continue
      if (!light.has(t) && !dark.has(t)) missing.push(t)
    }
    expect(missing, '侧栏引用了 theme.css 未定义的 token，var() 将永远走 fallback：' + missing.join(', ')).toEqual([])
  })
})

/**
 * 审计报告 #17（D4，低）：登录页品牌背景渐变硬编码。
 *
 * 与 F16 同一形态、同一判据口径：**直接把 linear-gradient 写在声明里**是缺陷，
 * 而 `var(--token, linear-gradient(...))` 是"token 优先 + 缺失时降级"的纵深防御 ——
 * F16 自己就是这么落地的（MainLayout.vue:785），其护栏也只禁前者（见上方 not.toMatch）。
 * 所以这里沿用同一口径，而不是要求字面量为零：**要求字面量从"唯一来源"降级为"fallback"**。
 */
describe('审计 #17 登录页品牌渐变 token 合同', () => {
  const loginSource = readFileSync(resolve(process.cwd(), 'src/views/auth/Login.vue'), 'utf8')

  it('解析有效（防止读错文件导致的假绿）', () => {
    expect(loginSource).toContain('.login-container')
    expect(loginSource).toContain('.login-transition')
  })

  it('--login-bg-gradient 在亮/暗两套都有定义', () => {
    expect(light.has('--login-bg-gradient'), '亮色缺少 --login-bg-gradient').toBe(true)
    expect(dark.has('--login-bg-gradient'), '暗色缺少 --login-bg-gradient').toBe(true)
  })

  it('登录页两处背景都消费 --login-bg-gradient（#17 核心）', () => {
    for (const sel of ['.login-container {', '.login-transition {']) {
      const b = block(loginSource, sel)
      expect(b, sel + ' 未接线到 --login-bg-gradient').toMatch(/background:[ \t]*var\(--login-bg-gradient/)
      // 裸硬编码仍属违规（与 F16 护栏同口径）
      expect(b, sel + ' 仍把 linear-gradient 当作唯一来源').not.toMatch(
        /background:[ \t]*linear-gradient\(135deg, #1a1f2e/,
      )
    }
  })

  it('亮/暗两套 --login-bg-gradient 同值是有意设计（须与注释一致，不是漏做暗色）', () => {
    // 设计上登录页在所有主题下都保持深色品牌底：**同值是期望**，
    // 但必须有人写明理由（theme.css 的两处注释），否则下一个人会以为漏了暗色适配。
    expect(light.get('--login-bg-gradient')).toBe(dark.get('--login-bg-gradient'))
    expect(themeCss).toContain('--login-bg-gradient')
    expect(loginSource).toContain('#17')
  })
})
