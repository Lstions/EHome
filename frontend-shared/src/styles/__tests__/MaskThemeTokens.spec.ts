import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * 遮罩 / 毛玻璃 token 合同（2026-09-29）。
 *
 * 背景（这个文件为什么存在）：
 * 用户报告「页面切换有黑色遮罩」。根因是 theme.css 在 `:root` 里硬编码
 * `--el-mask-color: rgba(0,0,0,0.7)` —— 亮色主题下同样是 70% 黑，压在白色表格上
 * 就是那个灰块/黑块。生产构建实测遮罩为 rgba(0,0,0,0.7)。
 *
 * 修复过程中又踩到**第二个更隐蔽的机制**，本测试的一半篇幅就是钉住它：
 * element-plus 把 `--el-mask-color` 定义在 `html.dark{}` 里，特异性 (0,1,1)
 * 高于项目桥接所在的 `:root` (0,1,0) —— **与样式表加载顺序无关**。
 * 也就是说：把项目值改成 var() 引用**并不够**，暗色下仍会被 EP 的 `#000c`(α0.8 纯黑) 接管。
 * 必须把桥接写进暗色块（同特异性）+ `!important`（顺序无关）。
 *
 * 判据口径：本项目既有教训（SidebarThemeTokens.spec.ts:19-20）——
 * vitest 默认 `css:false`，`import '...css?raw'` 返回空串、断言会**永远通过**。
 * 故这里一律 `readFileSync` 直读真实文件；读不到就抛错（不许静默通过）。
 */

const themeCss = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

/** 剥掉 CSS 注释：注释里会出现 `:root`、`--el-mask-color:` 等字样，
 *  不剥会让下面的"按选择器取块"和"按变量取值"全部串味（实测踩到过）。 */
const cssNoComments = themeCss.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))

/** 扫描所有顶层「selector { ... }」规则块（大括号配对）。 */
function ruleBlocks(css: string): Array<{ sel: string; body: string }> {
  const out: Array<{ sel: string; body: string }> = []
  let i = 0
  while (i < css.length) {
    const open = css.indexOf('{', i)
    if (open < 0) break
    const selStart = css.lastIndexOf('}', open) + 1
    const sel = css.slice(selStart, open).trim().replace(/\s+/g, ' ')
    let depth = 0
    let end = -1
    for (let j = open; j < css.length; j += 1) {
      if (css[j] === '{') depth += 1
      else if (css[j] === '}') {
        depth -= 1
        if (depth === 0) { end = j; break }
      }
    }
    if (end < 0) break
    out.push({ sel, body: css.slice(open + 1, end) })
    i = end + 1
  }
  return out
}

function decls(body: string): Map<string, string> {
  const m = new Map<string, string>()
  for (const x of body.matchAll(/(--[a-zA-Z0-9-]+)\s*:\s*([^;]+);/g)) m.set(x[1], x[2].trim())
  return m
}

const blocks = ruleBlocks(cssNoComments)

/** 选择器**精确等于** `:root` 的块（本文件有 3 个，必须合并；只取第一个会漏）。 */
const rootBlocks = blocks.filter((b) => b.sel === ':root')
/** 暗色块：选择器组里含 dark 入口的块（含 `html.dark` EP 覆盖块与安全网块）。 */
const darkBlocks = blocks.filter((b) => /(^|,\s*)(html\.dark|\[data-theme="dark"\]|\.dark-theme)(\s*$|,|\s)/.test(b.sel))

function merged(list: Array<{ body: string }>): Map<string, string> {
  const m = new Map<string, string>()
  for (const b of list) for (const [k, v] of decls(b.body)) m.set(k, v)
  return m
}

const light = merged(rootBlocks)
const dark = merged(darkBlocks)

/** 从 `rgba(r,g,b,a)` / `#rrggbb` 取通道与 alpha（本测试只处理这两种写法）。 */
function parseColor(v: string): { rgb: [number, number, number]; a: number } {
  const s = v.trim()
  const rgba = s.match(/rgba?\(([^)]+)\)/)
  if (rgba) {
    const p = rgba[1].split(',').map((x) => parseFloat(x.trim()))
    return { rgb: [p[0], p[1], p[2]], a: p.length > 3 ? p[3] : 1 }
  }
  const hex = s.match(/^#([0-9a-fA-F]{3,8})$/)
  if (hex) {
    let h = hex[1]
    if (h.length === 3) h = h.split('').map((c) => c + c).join('')
    return { rgb: [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)], a: 1 }
  }
  throw new Error('unsupported color: ' + v)
}

describe('遮罩 / 毛玻璃 token 合同', () => {
  it('解析本身有效（防止解析失败导致的假绿）', () => {
    expect(themeCss.length).toBeGreaterThan(1000)
    expect(rootBlocks.length, 'theme.css 应有多于 1 个 :root 块（本测试必须合并它们）').toBeGreaterThan(1)
    expect(darkBlocks.length, '应能定位到暗色块').toBeGreaterThan(0)
    expect(light.size).toBeGreaterThan(60)
    expect(dark.size).toBeGreaterThan(60)
  })

  it('亮/暗两块都定义了遮罩与毛玻璃 token', () => {
    for (const t of ['--mask-bg', '--mask-bg-extra-light', '--overlay-bg', '--mask-blur']) {
      expect(light.has(t), '亮色缺少 ' + t).toBe(true)
      expect(dark.has(t), '暗色缺少 ' + t).toBe(true)
    }
  })

  it('遮罩底色亮/暗不同，且方向正确（亮色推白、暗色推黑）', () => {
    const l = parseColor(light.get('--mask-bg')!)
    const d = parseColor(dark.get('--mask-bg')!)
    expect(light.get('--mask-bg'), '亮暗同值 = 没做主题化（这正是「亮色下也是黑遮罩」的形态）')
      .not.toBe(dark.get('--mask-bg'))
    // 亮色必须是浅色（三通道都 > 200），暗色必须是深色（都 < 60）
    expect(Math.min(...l.rgb), '亮色遮罩应为浅色').toBeGreaterThan(200)
    expect(Math.max(...d.rgb), '暗色遮罩应为深色').toBeLessThan(60)
    // α 必须显著小于 1，否则模糊无收益（糊成纯色）；也要大于 0，否则遮罩不成立
    for (const [name, c] of [['亮', l], ['暗', d]] as const) {
      expect(c.a, name + '色遮罩 α 过低，遮不住内容').toBeGreaterThan(0.2)
      expect(c.a, name + '色遮罩 α 过高，模糊会被压成纯色（α0.9 的 EP 默认就是反例）').toBeLessThan(0.8)
    }
  })

  it('模态遮罩（--overlay-bg）亮/暗不同且是深色系', () => {
    const l = parseColor(light.get('--overlay-bg')!)
    const d = parseColor(dark.get('--overlay-bg')!)
    expect(light.get('--overlay-bg')).not.toBe(dark.get('--overlay-bg'))
    expect(Math.max(...l.rgb), '模态遮罩亮色下也应是深色底（推远内容），不是白雾').toBeLessThan(60)
    expect(Math.max(...d.rgb)).toBeLessThan(60)
  })

  /* ── 以下三条是这个文件的核心：钉住「EP 特异性抢管」这个隐蔽机制 ── */

  it('EP 桥接的 --el-mask-color 系列在暗色块内有重声明（否则暗色下被 EP html.dark 接管）', () => {
    // 先确认前提仍然成立：EP 确实把这两个变量定义在 html.dark 下
    const epDark = readFileSync(
      resolve(process.cwd(), 'node_modules/element-plus/theme-chalk/dark/css-vars.css'),
      'utf8',
    )
    const epDarkBlock = epDark.match(/html\.dark\{([\s\S]*?)\}/)
    expect(epDarkBlock, 'EP dark css-vars 的 html.dark 块应可定位（EP 升级后结构若变，本测试需同步）').toBeTruthy()
    expect(epDarkBlock![1]).toContain('--el-mask-color')

    for (const v of ['--el-mask-color', '--el-mask-color-extra-light']) {
      const inDark = dark.get(v)
      expect(inDark, `EP 在 html.dark(0,1,1) 里定义了 ${v}，项目若只在 :root(0,1,0) 桥接会被静默架空`).toBeTruthy()
      expect(inDark, v + ' 的暗色桥接必须带 !important（dev 下 EP 后加载，顺序相反）').toContain('!important')
      expect(inDark, v + ' 的暗色桥接必须指向项目 token，而不是复述字面量').toMatch(/var\(--mask-bg/)
    }
  })

  it('遮罩底色不再以字面量硬编码在 :root（回归红线：改回 rgba(0,0,0,0.7) 必须红）', () => {
    expect(light.get('--el-mask-color'), '--el-mask-color 必须走 token 引用').toMatch(/var\(--mask-bg/)
    expect(themeCss, '不得再出现「亮色块里写死黑色遮罩」这一原始缺陷').not.toMatch(
      /--el-mask-color:\s*rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*0?\.7\s*\)/,
    )
  })

  it('亮色 :root 的遮罩桥接也带 !important（否则 dev 下被 EP 的 :root 抢走）', () => {
    // 背景：EP **亮色**档同样把 --el-mask-color / --el-overlay-color-lighter 定义在 :root，
    // 与本项目桥接同特异性 ⇒ 谁后加载谁赢。prod 下 theme.css 在后（项目赢，α0.6），
    // dev 下 EP 由 JS 按需后注入（EP 赢，实测 α0.9 近全白）⇒ 两侧观感不一致。
    // 这条护栏钉住「必须 !important」，让 dev 与 prod 解析结果一致。
    for (const v of ['--el-mask-color', '--el-mask-color-extra-light', '--el-overlay-color-lighter']) {
      const l = light.get(v)
      expect(l, '亮色 :root 缺少桥接：' + v).toBeTruthy()
      expect(l, v + ' 在亮色 :root 的桥接必须带 !important（EP 亮色档也在 :root 定义同名变量）').toContain('!important')
    }
  })

  it('--el-overlay-color-lighter 已接线（改前项目从未定义 ⇒ 恒为 EP 的 50% 纯黑）', () => {
    const bridged = [...rootBlocks, ...darkBlocks]
      .map((b) => decls(b.body).get('--el-overlay-color-lighter'))
      .filter(Boolean)
    expect(bridged.length, '项目此前从未定义 --el-overlay-color-lighter，这是「对话框遮罩恒黑」的根因').toBeGreaterThan(0)
    expect(bridged.some((v) => /var\(--overlay-bg/.test(v!)), '必须指向项目 token').toBe(true)
  })

  it('毛玻璃规则存在且覆盖 loading 与 overlay 两类遮罩', () => {
    const maskBlock = blocks.find((b) => b.sel === '.el-loading-mask')
    const overlayBlock = blocks.find((b) => b.sel === '.el-overlay')
    expect(maskBlock, '.el-loading-mask 规则缺失 ⇒ v-loading 无模糊').toBeTruthy()
    expect(overlayBlock, '.el-overlay 规则缺失 ⇒ 对话框遮罩无模糊').toBeTruthy()
    expect(maskBlock!.body).toMatch(/backdrop-filter:\s*blur\(var\(--mask-blur/)
    expect(overlayBlock!.body).toMatch(/backdrop-filter:\s*blur\(var\(--mask-blur/)
    // -webkit- 前缀必须同时写（Safari / 部分 iOS WebView）
    expect(maskBlock!.body).toMatch(/-webkit-backdrop-filter/)
  })

  it('不支持 backdrop-filter 的环境有降级（不得只剩半透明底）', () => {
    const supports = blocks.find((b) => b.sel.startsWith('@supports not'))
    expect(supports, '缺少 @supports not 降级块').toBeTruthy()
    expect(supports!.body).toMatch(/\.el-loading-mask/)
    expect(supports!.body).toMatch(/\.el-overlay/)
  })

  it('模糊半径取自 token 而非写死（组件层不得各写各的 blur）', () => {
    // 只取**规则的声明体**里的 blur，且跳过 `@supports` 的**前置条件**——
    // `@supports not ((backdrop-filter: blur(1px)) ...)` 里的 1px 是特性探测语法，
    // 不是半径（先按裸文本扫、再按选择器前缀过滤都会踩到它，实测踩过两次）。
    const declBlurs: string[] = []
    for (const b of blocks) {
      if (b.sel.startsWith('@supports')) continue
      for (const m of b.body.matchAll(/(?:-webkit-)?backdrop-filter\s*:\s*blur\(([^)]*)\)/g)) {
        declBlurs.push(m[1].trim())
      }
    }
    expect(declBlurs.length, '应能取到 backdrop-filter 的 blur 声明').toBeGreaterThan(0)
    for (const d of declBlurs) {
      expect(d, '模糊半径应引用 --mask-blur，而不是各写各的字面量：' + d).toMatch(/^var\(--mask-blur/)
    }
  })
})
