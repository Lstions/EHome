import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * 应用顶栏（MainLayout .main-header）窄宽度不塌陷契约（2026-10-21）。
 *
 * 用户原话：「宽度达到一定条件下顶部组件显示异常」。
 * 实测（改前，本地探针 .tmp-probe/r6-appbar.mjs）：769~900 之间顶栏三段互相挤压，
 * 面包屑「边缘设备管理 / inter」被压成 2 行(900) → 3 行(860) → 4 行(820) → 6 行/114px(769)，
 * 文字竖排并溢出 60px 高的顶栏；769 还有 9px 横向溢出，「在线」徽标被压成两行小圆点。
 *
 * 根因：left / center / right 三段都没有可收缩项 ——
 *   · .header-center 的输入框有 min-content 下限；
 *   · .header-right 的图标组是固定内容；
 *   · 于是唯一能被压的就是 .header-left（面包屑），而面包屑默认允许换行 ⇒ 竖排。
 *
 * 本文件锁四条修法，防止回归：
 *   ① .header-left 允许收缩（min-width:0 + flex-shrink）；
 *   ② 面包屑永不换行、末级用省略号（规则必须写在 .breadcrumb 本体上 —— 模板里
 *      .breadcrumb 就是 el-breadcrumb 根元素，写成后代选择器是空操作，这一条曾真的写错过，
 *      故加反向断言）；
 *   ③ .header-right 不参与收缩，「在线」徽标 nowrap；
 *   ④ 769~1024 隐藏搜索框（顶栏放不下三段），≤768 原本就隐藏。
 *
 * 像素级效果由探针截图保证（.logs/ui-fix/round6/appbar-769.png）；本 spec 只锁机制。
 */
const source = readFileSync(resolve(process.cwd(), 'src/views/layout/MainLayout.vue'), 'utf8')

/** 取某个选择器的规则体（第一个 { 到第一个 }，本文件没有嵌套 CSS，够用） */
function block(selector: string): string {
  const idx = source.indexOf(selector)
  expect(idx, '缺少选择器: ' + selector).toBeGreaterThan(-1)
  const start = source.indexOf('{', idx)
  const end = source.indexOf('}', start)
  return source.slice(start + 1, end)
}

describe('应用顶栏窄宽度不塌陷', () => {
  it('① .header-left 允许收缩（min-width:0 + 可 shrink）', () => {
    const b = block('.header-left {')
    expect(b).toMatch(/min-width:\s*0/)
    expect(b).toMatch(/flex:\s*0 1 auto/)
  })

  it('② 面包屑永不换行 + 末级省略号', () => {
    const b = block('.breadcrumb {')
    expect(b).toMatch(/flex-wrap:\s*nowrap/)
    expect(b).toMatch(/white-space:\s*nowrap/)
    expect(b).toMatch(/overflow:\s*hidden/)
    expect(source).toMatch(/:deep\(\.el-breadcrumb__item:last-child\)\s*\{[^}]*flex:\s*0 1 auto/)
    expect(source).toMatch(/:deep\(\.el-breadcrumb__inner\)\s*\{[^}]*text-overflow:\s*ellipsis/)
  })

  it('② 反向：不得把「不换行」写在 .breadcrumb :deep(.el-breadcrumb) 上（同一元素，空操作）', () => {
    expect(source).not.toMatch(/\.breadcrumb\s*:deep\(\.el-breadcrumb\)\s*\{/)
  })

  it('③ .header-right 不收缩 + 「在线」徽标 nowrap', () => {
    expect(block('.header-right {')).toMatch(/flex:\s*0 0 auto/)
    expect(block('.ws-status {')).toMatch(/white-space:\s*nowrap/)
  })

  it('④ 769~1024 隐藏搜索框，≤768 也隐藏（顶栏放不下三段）', () => {
    const tablet = source.slice(source.indexOf('@media (min-width: 769px) and (max-width: 1024px)'))
    expect(tablet).toMatch(/\.header-center\s*\{[^}]*display:\s*none/)
    const mobile = source.slice(source.indexOf('@media (max-width: 768px)'))
    expect(mobile).toMatch(/\.header-center\s*\{[^}]*display:\s*none/)
  })
})
