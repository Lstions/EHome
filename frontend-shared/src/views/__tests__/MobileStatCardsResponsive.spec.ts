import { describe, expect, it } from 'vitest'
import dashboardSource from '@/views/dashboard/Dashboard.vue?raw'
import configSource from '@/views/config/DeviceConfigList.vue?raw'
import edgeDeviceSource from '@/views/edge-device/EdgeDeviceList.vue?raw'
import nodeSource from '@/views/node/NodeList.vue?raw'

/**
 * 移动端统计卡「紧凑且可读」契约（规范 §4.3 统计卡 MUST 第 3 条）。
 *
 * 2026-09-15 变更说明（为什么不再是 toContain('line-height: 1.3')）：
 * Dashboard.vue 为修 D4-01（骨架/内容几何不一致）需要把 .stat-value/.stat-label
 * 的行盒写成**显式 px**，才能与骨架条的 height 成对同值（无单位行高没有 px 可配对）。
 * 10px 字号 × 1.3 = 13px，**视觉与布局不变**，但字面量从 `line-height: 1.3`
 * 变成了 `line-height: 13px`。
 *
 * 因此本用例改为断言**比值本身**（line-height ÷ font-size ≈ 1.3），
 * 这比原来的字符串匹配**更强**而不是更弱：
 *   · 旧写法：只要文本里有 `line-height: 1.3` 就通过 —— 即使 font-size 被改成 20px
 *     或该行高声明根本不属于 .stat-label 也会绿；
 *   · 新写法：必须从**移动端 .stat-label 规则块内**取出 font-size 与 line-height，
 *     按 px/无单位两种记法各自归一化成数值，再校验比值达标。
 * 想改坏它必须真的改到行距（如 1.3→2.0，或 13px→26px），改注释不算。
 */

/**
 * 从源码的 `@media (max-width: 768px)` 段里取出 `.stat-label { ... }` 规则块。
 * 取不到返回 null（fail-closed：认不出就让用户裁，而不是静默放过）。
 */
export function mobileLabelBlock(source: string): string | null {
  const mediaAt = source.indexOf('@media (max-width: 768px)')
  if (mediaAt < 0) return null
  const scope = source.slice(mediaAt)
  const at = scope.indexOf('.stat-label')
  if (at < 0) return null
  const open = scope.indexOf('{', at)
  const close = scope.indexOf('}', open)
  if (open < 0 || close < 0) return null
  return scope.slice(open + 1, close)
}

/** 从规则块里取某属性的**数值**：支持 px（13px → 13）与无单位（1.3 → 1.3），取不到返回 null */
export function numericProp(block: string, prop: string): number | null {
  const m = block.match(new RegExp('(?:^|[;{\\s])' + prop + '\\s*:\\s*(-?[\\d.]+)(px)?\\s*;'))
  return m ? Number(m[1]) : null
}

/**
 * 分类器（纯函数，可被正/反例直接喂）：
 * 返回 line-height ÷ font-size 的比值；任一侧缺失返回 null。
 */
export function mobileLabelLineHeightRatio(source: string): number | null {
  const block = mobileLabelBlock(source)
  if (block === null) return null
  const lh = numericProp(block, 'line-height')
  const fs = numericProp(block, 'font-size')
  if (lh === null || fs === null || fs === 0) return null
  // 无单位行高本身就是倍数（1.3）；px 行高要除以字号（13 / 10 = 1.3）
  const unitless = /line-height\s*:\s*-?[\d.]+\s*;/.test(block)
  return unitless ? lh : lh / fs
}

const EXPECTED_RATIO = 1.3
const TOLERANCE = 0.02

const mobileStatCardPages = [
  ['Dashboard', dashboardSource, '.dashboard-stats'],
  ['DeviceConfigList', configSource, '.stats-row'],
  ['EdgeDeviceList', edgeDeviceSource, '.stats-row'],
  ['NodeList', nodeSource, '.stats-row'],
] as const

describe('mobile statistic card layout contract', () => {
  it('分类器自检：px 记法与无单位记法必须归一化成同一比值，缺失/异常必须判 null', () => {
    const pxForm = '.stat-label { font-size: 10px; line-height: 13px; }'.replace('.stat-label', '@media (max-width: 768px) { .stat-label') + '}'
    const unitForm = '@media (max-width: 768px) { .stat-label { font-size: 10px; line-height: 1.3; } }'
    const pxRatio = mobileLabelLineHeightRatio(pxForm)
    const unitRatio = mobileLabelLineHeightRatio(unitForm)
    expect(pxRatio, 'px 记法应归一化为 1.3').toBeCloseTo(EXPECTED_RATIO, 5)
    expect(unitRatio, '无单位记法应归一化为 1.3').toBeCloseTo(EXPECTED_RATIO, 5)

    // 反例 1：行距真的被改大（1.3 -> 2.0）
    const tooLoose = '@media (max-width: 768px) { .stat-label { font-size: 10px; line-height: 2; } }'
    expect(mobileLabelLineHeightRatio(tooLoose)).not.toBeCloseTo(EXPECTED_RATIO, 2)
    // 反例 2：px 记法但数值不对（13px -> 26px）
    const pxWrong = '@media (max-width: 768px) { .stat-label { font-size: 10px; line-height: 26px; } }'
    expect(mobileLabelLineHeightRatio(pxWrong)).not.toBeCloseTo(EXPECTED_RATIO, 2)
    // 反例 3：没有移动端段 / 没有 .stat-label => null（fail-closed，不得当成通过）
    expect(mobileLabelLineHeightRatio('.stat-label { line-height: 1.3; }')).toBeNull()
    expect(mobileLabelLineHeightRatio('@media (max-width: 768px) { .other { line-height: 1.3; } }')).toBeNull()
  })

  it.each(mobileStatCardPages)('%s keeps four compact, readable cards at <=768px', (_name, source, gridSelector) => {
    expect(source).toContain('@media (max-width: 768px)')
    const escapedGridSelector = gridSelector.replace('.', '\\.')
    expect(source).toMatch(new RegExp(`${escapedGridSelector}\\s*\\{[\\s\\S]*?grid-template-columns:\\s*repeat\\(4, minmax\\(0, 1fr\\)\\);\\s*[\\s\\S]*?gap:\\s*8px;`))
    expect(source).toContain('font-size: 10px')
    // 行距：必须落在 .stat-label 内，且比值 ≈1.3（px 与无单位两种记法都接受）
    const ratio = mobileLabelLineHeightRatio(source)
    expect(ratio, '移动端 .stat-label 的 font-size/line-height 取不到，断言会假绿').not.toBeNull()
    expect(
      Math.abs((ratio as number) - EXPECTED_RATIO),
      '移动端 .stat-label 行距比值应为 ' + EXPECTED_RATIO + '，实测 ' + ratio
    ).toBeLessThan(TOLERANCE)
    expect(source).toContain('max-height: 2.6em')
    expect(source).toContain('overflow: hidden')
    expect(source).toContain('word-break: keep-all')
    expect(source).toContain('overflow-wrap: break-word')
  })

  it('移动端 label 必须同时保留「缩写」与「范围词」（F14 在移动端同样 MUST）', () => {
    // ── 这条断言的前身是 expect(source).toContain('mobile-label="边缘设备"') ──
    // 它钉住的其实是**缺陷**：≤768px 时 StatCard 把桌面 label 设为 display:none，
    // 只显示 mobile-label；而 mobile-label 当时只有缩写、**没有范围词**，
    // 于是移动端用户看不到「本页」—— F14（统计卡范围标注 MUST）在移动端静默失效。
    // 前身写法还只做字符串匹配：任何位置出现该串即绿，改不动语义。
    // 现改为**真契约**：逐个 mobile-label 断言「有缩写」且「有范围词」。
    const mobileLabels = [...edgeDeviceSource.matchAll(/:?mobile-label="([^"]*)"/g)].map((m) => m[1])
    expect(mobileLabels.length, 'EdgeDeviceList 应有 3 个 mobile-label').toBe(3)
    for (const raw of mobileLabels) {
      // 动态写法是模板串（含反引号）：先去反引号，再把 SCOPE_* 常量解析成字面量
      const resolved = raw
        .replace(/^`|`$/g, '')
        .replace(/\$\{SCOPE_PAGE\}/g, '本页')
        .replace(/\$\{SCOPE_GLOBAL\}/g, '全局')
      expect(resolved, '移动端 label「' + raw + '」缺范围词').toMatch(/（(本页|全局|当前筛选)）/)
      // 缩写：去掉范围词后必须明显短于桌面 label（这是「concise」的原意）
      const concise = resolved.replace(/（[^）]*）/, '')
      expect(concise.length, '移动端 label「' + concise + '」应是缩写').toBeLessThanOrEqual(5)
    }
    // 具体保留原缩写词，防有人「加范围词时顺手把缩写也改长」
    expect(edgeDeviceSource).toContain('边缘设备（')
    expect(edgeDeviceSource).toContain('离线/异常（')
  })

  it.each([
    ['DeviceConfigList', configSource],
    ['EdgeDeviceList', edgeDeviceSource],
    ['NodeList', nodeSource],
  ] as const)('%s retains its two-column intermediate breakpoint', (_name, source) => {
    expect(source).toContain('@media (max-width: 1200px)')
    expect(source).toContain('grid-template-columns: repeat(2, 1fr)')
  })
})
