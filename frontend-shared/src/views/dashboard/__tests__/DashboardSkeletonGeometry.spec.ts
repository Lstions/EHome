import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * 仪表盘 KPI 骨架/内容几何合同 —— 源码层防回退护栏（审计条目 D4-01）。
 *
 * 分工（重要）：
 *   真实几何的权威判据是**浏览器实测**（骨架首屏卡与加载后内容卡的首屏高度差、
 *   加载完成后是否整体下跳）——由主控在真浏览器里另测，本文件不做像素测量。
 *   happy-dom 没有布局引擎（getBoundingClientRect 恒 0），在这里断言"高度相等"
 *   只会得到恒真的假绿。
 *
 *   本测试守的是**构造层不变量**：骨架条必须与它所替代的那一行同高，
 *   即 .stat-value/.stat-label 的 line-height 与 .stat-value.skeleton-bar/
 *   .stat-label.skeleton-bar 的 height 成对同值（桌面与移动端各一对）。
 *   这正是 D4-01 的根因：空元素没有文本就没有行盒，骨架条若用魔法数字定高，
 *   就会在加载完成时让整个区段下跳；把两侧绑成一对后，高度由构造决定地相等。
 *
 * 为什么必须 fs 直读源码：vitest 默认 css:false，'*.vue?raw' / '*.css?raw'
 * 可能拿到空串导致断言永远通过；且 CSS 编译产物无法反查声明的成对关系。
 */
const dashboardSource = readFileSync(
  resolve(process.cwd(), 'src/views/dashboard/Dashboard.vue'),
  'utf8'
)

/** 去掉 /\* ... *\/ 注释：注释里出现的 "line-height: 34px" 不得算作声明 */
export function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '')
}

/** 取 selector 的所有规则块体（按大括号配对）。selector 必须精确匹配规则头，
 *  因此 '.stat-value' 不会误命中 '.stat-value.skeleton-bar {'。 */
export function ruleBodies(css: string, selector: string): string[] {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const re = new RegExp(escaped + '\\s*\\{', 'g')
  const out: string[] = []
  let m: RegExpExecArray | null
  while ((m = re.exec(css)) !== null) {
    const open = m.index + m[0].length - 1
    let depth = 0
    for (let i = open; i < css.length; i += 1) {
      if (css[i] === '{') depth += 1
      else if (css[i] === '}') {
        depth -= 1
        if (depth === 0) {
          out.push(css.slice(open + 1, i))
          re.lastIndex = i + 1
          break
        }
      }
    }
  }
  return out
}

/** 取 selector 的第一个规则块体；找不到返回空串 */
export function firstRuleBody(css: string, selector: string): string {
  const bodies = ruleBodies(css, selector)
  return bodies.length > 0 ? bodies[0] : ''
}

/** 从块体里读某个属性的 px 数值；找不到或不是 px 返回 null */
export function pxOf(blockBody: string, prop: string): number | null {
  const re = new RegExp('(?:^|[;{\\s])' + prop + '\\s*:\\s*(-?[\\d.]+)px\\s*;')
  const m = blockBody.match(re)
  return m ? Number(m[1]) : null
}

export interface Pair {
  /** 人类可读的配对名，失败信息里直接用 */
  label: string
  /** 内容态行盒高度（line-height: Npx） */
  content: number | null
  /** 骨架条高度（height: Npx） */
  skeleton: number | null
}

/**
 * 分类器（纯函数，可被正/反例直接喂）：
 * 每对必须两侧都存在且数值相等，否则 ok=false 并给出具体失败原因。
 */
export function classify(pairs: Pair[]): { ok: boolean; failures: string[] } {
  const failures: string[] = []
  for (const p of pairs) {
    if (p.content === null) failures.push(p.label + ': 未找到 line-height: Npx 声明')
    if (p.skeleton === null) failures.push(p.label + ': 未找到骨架条 height: Npx 声明')
    if (p.content !== null && p.skeleton !== null && p.content !== p.skeleton) {
      failures.push(
        p.label + ': line-height ' + p.content + 'px ≠ 骨架条 height ' + p.skeleton + 'px'
      )
    }
  }
  return { ok: failures.length === 0, failures }
}

// ── 从 Dashboard.vue 抽出 <style scoped>，并切出桌面段 / 移动端 @media 段 ──
const styleSection = (() => {
  const at = dashboardSource.indexOf('<style')
  if (at < 0) throw new Error('Dashboard.vue 没有 <style> 段')
  const open = dashboardSource.indexOf('>', at) + 1
  const end = dashboardSource.indexOf('</style>', open)
  return dashboardSource.slice(open, end)
})()

const css = stripComments(styleSection)

const mediaBody = (() => {
  const marker = '@media (max-width: 768px)'
  const at = css.indexOf(marker)
  if (at < 0) throw new Error('Dashboard.vue 缺少移动端 @media (max-width: 768px) 块')
  return firstRuleBody(css.slice(at), marker)
})()

const mediaStart = css.indexOf('@media (max-width: 768px)')
const desktopCss = css.slice(0, mediaStart)

function pair(
  label: string,
  scope: string,
  rowSelector: string,
  skeletonSelector: string
): Pair {
  return {
    label,
    content: pxOf(firstRuleBody(scope, rowSelector), 'line-height'),
    skeleton: pxOf(firstRuleBody(scope, skeletonSelector), 'height'),
  }
}

/** 同一行盒：内容态字号 × 1.2 的 natural 行高（±1px），用来防止"两侧一起改成 90px"式绕过 */
function naturalLineBoxHeight(scope: string, rowSelector: string): number | null {
  const fs = pxOf(firstRuleBody(scope, rowSelector), 'font-size')
  return fs === null ? null : Math.round(fs * 1.2)
}

const pairs: Pair[] = [
  pair('桌面 .stat-value', desktopCss, '.stat-value', '.stat-value.skeleton-bar'),
  pair('桌面 .stat-label', desktopCss, '.stat-label', '.stat-label.skeleton-bar'),
  pair('移动端 .stat-value', mediaBody, '.stat-value', '.stat-value.skeleton-bar'),
  pair('移动端 .stat-label', mediaBody, '.stat-label', '.stat-label.skeleton-bar'),
]

describe('Dashboard.vue KPI 骨架/内容几何（D4-01 源码层门禁）', () => {
  it('分类器自检：正例通过、反例（不等 / 缺失）必须被判失败', () => {
    // 正例
    const good = classify([
      { label: 'a', content: 34, skeleton: 34 },
      { label: 'b', content: 17, skeleton: 17 },
    ])
    expect(good.ok).toBe(true)
    expect(good.failures).toEqual([])

    // 反例 1：骨架条被改成魔法数字（正是本次变异自证要抓的形态）
    const unequal = classify([{ label: 'a', content: 34, skeleton: 90 }])
    expect(unequal.ok).toBe(false)
    expect(unequal.failures.join(' ')).toContain('34px ≠ 骨架条 height 90px')

    // 反例 2：声明缺失（空元素没有文本，没有 height 就没有高度）
    const missing = classify([{ label: 'c', content: 19, skeleton: null }])
    expect(missing.ok).toBe(false)
    expect(missing.failures.join(' ')).toContain('未找到骨架条 height')

    // 反例 3：连内容态 line-height 都没有（回到 normal 行高 = 不可构造相等）
    const noContent = classify([{ label: 'd', content: null, skeleton: 34 }])
    expect(noContent.ok).toBe(false)
    expect(noContent.failures.join(' ')).toContain('未找到 line-height')
  })

  it('四对（桌面/移动端 × value/label）的 line-height 与骨架条 height 成对同值', () => {
    const result = classify(pairs)
    expect(result.failures).toEqual([])
    expect(result.ok).toBe(true)
    // 防止"两侧一起改"：每一对都必须与字号×1.2 的 natural 行盒同量级（±1px）
    for (const [scope, row] of [
      [desktopCss, '.stat-value'],
      [desktopCss, '.stat-label'],
      [mediaBody, '.stat-value'],
      [mediaBody, '.stat-label'],
    ] as const) {
      const skeleton = pxOf(firstRuleBody(scope, row + '.skeleton-bar'), 'height')
      const natural = naturalLineBoxHeight(scope, row)
      expect(natural, row + ' 缺少 font-size 声明，断言会假绿').not.toBeNull()
      expect(
        Math.abs((skeleton as number) - (natural as number)),
        row + ' 骨架条高度 ' + skeleton + 'px 与 natural 行盒 ' + natural + 'px 相差超过 1px'
      ).toBeLessThanOrEqual(1)
    }
  })

  it('骨架分支复用内容态同一外层容器与结构类，且 aria-hidden="true"', () => {
    const template = dashboardSource.slice(0, dashboardSource.indexOf('<script'))
    const skeletonIdx = template.indexOf('data-test="dashboard-stats-skeleton"')
    const loadedIdx = template.indexOf('data-test="dashboard-stats-loaded"')
    expect(skeletonIdx, '缺少骨架态容器 data-test="dashboard-stats-skeleton"').toBeGreaterThan(0)
    expect(loadedIdx, '缺少内容态容器 data-test="dashboard-stats-loaded"').toBeGreaterThan(0)

    const skeletonBlock = template.slice(skeletonIdx, loadedIdx)
    // 骨架必须是同一 .dashboard-stats / .stat-card / .stat-content / .stat-icon / .stat-info 结构
    for (const cls of [
      'class="dashboard-stats"',
      'class="stat-card"',
      'class="stat-content"',
      'class="stat-icon skeleton-bar"',
      'class="stat-info"',
    ]) {
      expect(skeletonBlock, '骨架分支缺少 ' + cls).toContain(cls)
    }
    // 骨架是装饰：承载 v-for 的每张骨架卡都必须 aria-hidden="true"
    // （源码里 v-for 只写一次，展开成 4 张；断言 v-for="i in 4" 与 aria-hidden
    //   出现在同一个 <el-card> 开标签上，才对"4 张都隐藏"有约束力）
    const skeletonCardTag = skeletonBlock.match(/<el-card\b[^>]*>/) 
    expect(skeletonCardTag, '骨架分支缺少 <el-card> 开标签').not.toBeNull()
    expect(skeletonCardTag![0]).toContain('v-for="i in 4"')
    expect(skeletonCardTag![0]).toContain('aria-hidden="true"')
    expect(skeletonCardTag![0]).toContain('class="stat-card"')
    // 骨架不得再出现会改变卡片盒模型的分支（裸 SkeletonCard）
    expect(skeletonBlock).not.toContain('SkeletonCard')
  })

  it('SkeletonCard 的 stat 变体保留（NodeList/EdgeDeviceList 仍在用）', () => {
    const other = [
      resolve(process.cwd(), 'src/views/node/NodeList.vue'),
      resolve(process.cwd(), 'src/views/edge-device/EdgeDeviceList.vue'),
    ].map((p) => readFileSync(p, 'utf8'))
    for (const src of other) expect(src).toContain('variant="stat"')
    const skeletonCard = readFileSync(
      resolve(process.cwd(), 'src/components/common/SkeletonCard.vue'),
      'utf8'
    )
    expect(skeletonCard).toContain(".skeleton-card.stat {")
  })
})
