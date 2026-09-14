import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * F27 窄屏基础控件高度 —— 尺寸规则合同测试（规范 §4.4.5）。
 *
 * ## 本任务修正的口径（这个文件存在的理由）
 *
 * 审计原记录给的是「全站 62 个非表格内控件 <36px」，样本里出现
 * el-input__inner 234x30 / el-select__input 207x24 / el-radio-button__original 13x13。
 * 复测（.tmp-probe/f27-pairing.mjs、f27-inventory.mjs）证明这三类**全是误报**：
 *   - .el-input__inner(30px) 的真实热区是祖先 .el-input__wrapper —— 实测 44px；
 *   - .el-select__input(24px) 同上，wrapper 44px（其中心还被 wrapper 内的
 *     placeholder 兄弟节点覆盖，只量 inner 会同时产生尺寸与命中两种误报）；
 *   - .el-radio-button__original-radio(13px) 是 EP 的不可见代理元素
 *     （产物 CSS 原文：.el-radio-button__original-radio{opacity:0;z-index:-1;position:absolute}），
 *     真实热区是 label.el-radio-button —— 实测 36px。
 * 把候选集从「内部展示元素」换成「控件盒」后，13 条路由的真实不达标 24 个，
 * **全部是 32px 的普通 el-button**。
 *
 * ## 本文件守护的不变量
 *   1. 窄屏（<=768px）普通/小号 el-button 的 min-height >= 36px；
 *   2. 该规则待在媒体查询里 —— 桌面 32px 是有意的紧凑密度（§4.2.3）；
 *   3. 只加尺寸，不碰颜色/字号/行高，也不得削弱既有 44px 档。
 *
 * 注意：本层只守护「源码里写了什么」，**不能**代替真实浏览器取证（本仓有
 * 「源码有规则、产物里没有」的教训）。配套证据见 .tmp-probe/f27-css-survival.mjs
 * （解析产物 CSSOM + 计算样式）与 .tmp-probe/f27-inventory.mjs（真实 Chromium）。
 */
const themeCss = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

const BASE = '.el-button:not(.is-circle):not(.is-link):not(.is-text):not(.el-button--small)'
const SMALL = '.el-button--small:not(.is-circle):not(.is-link):not(.is-text)'

/** 位置是否处于注释内（F6 记录过的坑：注释里的同名文本会骗过 indexOf）。 */
function inComment(css: string, pos: number): boolean {
  const open = css.lastIndexOf('/*', pos)
  if (open === -1) return false
  return css.lastIndexOf('*/', pos) < open
}

/** 找 needle 第一次**不在注释里**出现的位置，找不到返回 -1。 */
function indexOutsideComment(css: string, needle: string): number {
  for (let from = 0; ; ) {
    const k = css.indexOf(needle, from)
    if (k === -1) return -1
    if (!inComment(css, k)) return k
    from = k + 1
  }
}

/** 从 start 处抽出配平的花括号块。 */
function balancedBlock(css: string, start: number): string {
  let depth = 0
  for (let i = start; i < css.length; i += 1) {
    if (css[i] === '{') depth += 1
    else if (css[i] === '}') { depth -= 1; if (depth === 0) return css.slice(start, i + 1) }
  }
  return ''
}

/**
 * 返回**包住** pos 的最外层 @media 头的起始索引；没有则 -1。
 * 说明：F27 的规则被合并进 F6 已有的 @media (max-width:768px) 块内，
 * 所以定位方式是「从规则反找它所属的 @media」，而不是「找第 N 个 @media 块」。
 * 后者会被同条件的兄弟块顶开 —— F6 的抽取器正是这样假红过（见下）。
 */
function enclosingMediaStart(css: string, pos: number): number {
  let depth = 0
  for (let i = pos; i >= 0; i -= 1) {
    if (css[i] === '}') depth += 1
    else if (css[i] === '{') {
      if (depth === 0) {
        const lineStart = css.lastIndexOf('\n', i) + 1
        const head = css.slice(lineStart, i).trim()
        if (!head.startsWith('@media')) return -1
        const indent = css.slice(lineStart, i).length - css.slice(lineStart, i).trimStart().length
        return lineStart + indent
      }
      depth -= 1
    }
  }
  return -1
}

/** 取一条规则的声明体；selector 必须是该规则选择器列表中的**一项**（支持逗号列表）。 */
function ruleBody(region: string, selector: string): string {
  let from = 0
  for (;;) {
    const k = region.indexOf(selector, from)
    if (k === -1) return ''
    from = k + 1
    const lineStart = region.lastIndexOf('\n', k) + 1
    if (region.slice(lineStart, k).trim() !== '') continue
    const after = region.slice(k + selector.length)
    if (!/^\s*(,|\{)/.test(after)) continue
    const open = region.indexOf('{', k)
    if (open === -1) continue
    const between = region.slice(k, open)
    if (between.includes('}') || between.includes(';')) continue
    const close = region.indexOf('}', open)
    return close === -1 ? '' : region.slice(open + 1, close)
  }
}

// F27 的窄屏块 = **包住 F27 规则**的那个 @media（不依赖块的数量与顺序）
const f27RulePos = indexOutsideComment(themeCss, '.' + BASE.slice(1))
const f27MediaStart = f27RulePos === -1 ? -1 : enclosingMediaStart(themeCss, f27RulePos)
const NARROW = f27MediaStart === -1 ? '' : balancedBlock(themeCss, f27MediaStart)

describe('F27 窄屏基础控件高度：尺寸规则合同', () => {
  it('读到了非空 theme.css（防桩文件：分母自证）', () => {
    expect(themeCss.length).toBeGreaterThan(10000)
    expect(themeCss).toContain('--color-primary')
    expect(NARROW, '定位不到包住 F27 规则的 @media 块').not.toBe('')
  })

  it('定位器自校准：注释里的 @media 文本不得污染定位（F6 同一个坑）', () => {
    // F6 的执行者曾在注释里写 "@media (pointer: coarse)" 字面量而假红；
    // F27 的 f27-extract.mjs 也翻过一次车（注释里引用 "@media (min-width: 769px)"）。
    // 这里用一个「注释里有 @media、且块内还有同条件兄弟块」的构造自证定位器。
    const fake = [
      '/* 注释里写 @media (max-width: 768px) 不得被当成真规则 */',
      '@media (max-width: 768px) {',
      '  .probe-sentinel-f27 { min-height: 36px; }',
      '}',
      '@media (max-width: 768px) {',
      '  .probe-sibling-f27 { min-height: 44px; }',
      '}',
    ].join('\n')
    const pos = indexOutsideComment(fake, '.probe-sentinel-f27')
    expect(pos, '定位器没找到哨兵规则').toBeGreaterThan(-1)
    const start = enclosingMediaStart(fake, pos)
    expect(start, '定位器没找到包住哨兵的 @media').toBeGreaterThan(-1)
    const blk = balancedBlock(fake, start)
    expect(blk, '定位到错误的块（被注释或兄弟块骗到）').toContain('.probe-sentinel-f27')
    expect(blk).not.toContain('.probe-sibling-f27')
    expect(blk).not.toContain('注释里写')
  })

  it('窄屏块内把普通/小号 el-button 补到 >=36px（F27 的真实缺陷）', () => {
    const body = ruleBody(NARROW, BASE)
    expect(body, BASE + ' 规则缺失').not.toBe('')
    expect(body).toMatch(/min-height:\s*36px/)
    // 小号变体：EP 自带 --el-button-size:24px，必须在同一条规则里一起钉住
    const body2 = ruleBody(NARROW, SMALL)
    expect(body2, SMALL + ' 规则缺失').not.toBe('')
    expect(body2).toMatch(/min-height:\s*36px/)
  })

  it('F27 规则与 F6 规则合并在同一个窄屏块内（防止别人抽取锚点被顶开）', () => {
    // 这条守的是「合并」这个决定本身：F6 的 touchTargetsF6.spec.ts 用
    // lastMediaBlock() 按注释标题定位窄屏块；若 F27 另起一个同条件的
    // @media (max-width: 768px)，它的锚点会被顶开，F6 的两条断言随即假红
    // （实测 2 failed）。所以本块必须同时含 F6 的表格 Switch 规则与 F27 的按钮规则。
    expect(NARROW, 'F27 规则没有被合进 F6 的窄屏块（可能又新开了一个同条件 @media）').toContain('.el-table .el-switch')
    expect(NARROW).toContain(BASE)
    // 同条件的窄屏块数量不因 F27 增加：源码里 max-width:768px 的块应保持既有数量（3 个）。
    const narrowBlocks = themeCss.split('@media (max-width: 768px)').length - 1
    expect(
      narrowBlocks,
      '出现了额外的 @media (max-width: 768px) 块（F27 应合并进既有块，而不是新开）：' + narrowBlocks
    ).toBeLessThanOrEqual(3)
  })

  it('桌面密度隔离：不得出现「无作用域」的无条件 .el-button 尺寸规则', () => {
    // 1440px 下这些按钮就是 32px，是**有意的**紧凑运维密度（§4.2.3），不是缺陷。
    // 会破坏它的是「裸 .el-button 的无条件尺寸规则」，所以逐条扫：
    // 裸选择器 + 带 min-* + 不在任何 @media 内 ⇒ 违规。
    // 作用域选择器不在此列，它们各有既有的、桌面也成立的尺寸理由：
    //   .el-table .el-button--small          —— 行内操作 18px，配 769px 的 :has() 收 padding 保行高
    //   .main-header .el-button.is-circle    —— 页头图标，桌面也走 44px（F6 裁决）
    const offenders: string[] = []
    const ruleRe = /(^|\n)([^\n{}]*)\{/g
    let m: RegExpExecArray | null
    while ((m = ruleRe.exec(themeCss)) !== null) {
      const selStart = m.index + m[1].length
      if (inComment(themeCss, selStart)) continue
      const open = themeCss.indexOf('{', selStart)
      const close = themeCss.indexOf('}', open)
      if (open === -1 || close === -1) continue
      const body = themeCss.slice(open + 1, close)
      if (!/min-(height|width)\s*:/.test(body)) continue
      if (enclosingMediaStart(themeCss, selStart) !== -1) continue
      const bare = m[2].split(',').map((s) => s.trim()).filter((p) => p.startsWith('.el-button'))
      if (bare.length) offenders.push(bare.join(', ') + ' { ' + body.trim().replace(/\s+/g, ' ').slice(0, 60) + ' }')
    }
    expect(
      offenders,
      '出现了**无条件**的裸 .el-button 尺寸规则，会改掉桌面 32px 密度：' + JSON.stringify(offenders)
    ).toEqual([])
  })

  it('反例守卫：F27 新增块只加尺寸，不得碰颜色/字号/行高', () => {
    const block = NARROW.slice(NARROW.indexOf('/* ---- F27'), NARROW.indexOf(BASE))
    expect(block, 'F27 注释段缺失').not.toBe('')
    for (const prop of ['color:', 'background:', 'border-color:', '--el-color-', '--color-']) {
      expect(block.includes(prop), 'F27 尺寸块不应出现颜色声明 ' + prop).toBe(false)
    }
    expect(block).not.toMatch(/font-size:/)
    expect(block).not.toMatch(/line-height:/)
    const tooSmall = block.match(/min-(?:width|height):\s*(?:[0-9]|[12][0-9]|3[0-5])px/g)
    expect(tooSmall, 'F27 块出现了 <36px 的 min-*：' + String(tooSmall)).toBeNull()
  })

  it('本任务不得削弱既有 44px 档（窄屏输入控件仍是 44px）', () => {
    // F27 只碰按钮。若有人为了「让按钮过」而顺手把输入控件从 44 拉到 36，
    // 就削弱了 §4.4.5 的 MUST 档 —— 这条挡住它。
    expect(NARROW).toMatch(/\.el-input__wrapper,[\s\S]{0,160}min-height:\s*44px/)
  })
})
