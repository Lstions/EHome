import { describe, expect, it } from 'vitest'
import { compileStyleAsync, parse } from '@vue/compiler-sfc'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * 自滚动（用户真机反馈「页面有时自己往下滚一段」）回归门禁 — 2026-10-21。
 *
 * ## 根因（实测，非猜测）
 * 主滚动容器是 MainLayout 的 `.el-main.main-content`（overflow-y:auto），**不是 window**。
 * 页面进入后图表 / 表格 / 折叠块分批渲染 ⇒ **视口上方内容高度变化** ⇒ Chromium 的
 * scroll anchoring 为"保住视口内锚点节点"**静默改写 scrollTop**。它不经过 JS 的
 * scrollIntoView / scrollTo / focus，也不发 focus 事件（插桩实测 0 次），因此用户看到
 * "没人操作页面却滚了一段"。
 *
 * 实测证据（frontend-shared/.logs/ui-fix/round3/）：
 *   · scroll3-desk-data-after-scroll.json — dashboard 滚到 1500 后，异步内容沉降期间出现
 *     +60px 自增，而 jsSets 只有探针自己的 seed；
 *   · scroll-scroll3-m-logical-*.json / scroll2 探针 — +76 / +60 / +136px 自增，同样无 JS 事件；
 *   · scroll-anchor-ab.json — 同一场景 A/B：overflow-anchor:auto（改前）自增 N 次，
 *     none（改后）0 次；JS 程序化滚动两侧均为 0。
 *   历史同类：BmsDetailPage.vue:2058-2062（默认锚定 -169px vs 关闭后 0px，用户报障过）
 *   与 MainLayout.vue 的 toggleSidebar 注释（侧栏动画窗口内临时关闭锚定）。
 *
 * ## 本文件锁什么
 *   ① 滚动容器本体永久声明 overflow-anchor:none（唯一修复点，防止被顺手删掉）；
 *   ② 渲染路径里**不得**出现无条件的 scrollIntoView / scrollTo(0) / focus 滚动
 *      （排除「换个 API 再犯一次」的假修）；
 *   ③ 反向护栏：把 overflow-anchor 声明删掉后判定器必须变红（证明它不是恒真）。
 */

const ROOT = process.cwd()
const MAIN_LAYOUT = 'src/views/layout/MainLayout.vue'
const ROUTER_INDEX = 'src/router/index.ts'

function sfcTemplate(rel: string): string {
  const raw = readFileSync(resolve(ROOT, rel), 'utf8')
  const { descriptor, errors } = parse(raw, { filename: rel })
  expect(errors, rel + ' SFC 解析失败: ' + JSON.stringify(errors)).toEqual([])
  return descriptor.template?.content ?? ''
}

async function compiledScopedCss(rel: string): Promise<string> {
  const raw = readFileSync(resolve(ROOT, rel), 'utf8')
  const { descriptor } = parse(raw, { filename: rel })
  const chunks: string[] = []
  for (const block of descriptor.styles) {
    if (!block.scoped) continue
    const res = await compileStyleAsync({
      source: block.content,
      filename: rel,
      id: 'data-v-scrollscope',
      scoped: true,
    })
    expect(res.errors, JSON.stringify(res.errors)).toEqual([])
    chunks.push(res.code)
  }
  return chunks.join('\n')
}

/** 判据唯一入口：编译后的 .main-content 规则里必须有 overflow-anchor: none。 */
function mainContentHasAnchoringOff(css: string): boolean {
  const m = css.match(/\.main-content\[data-v-[a-z0-9]+\]\s*\{([^}]*)\}/)
  if (!m) return false
  return /overflow-anchor:\s*none/.test(m[1])
}

describe('主滚动容器不得被浏览器 scroll anchoring 改写 scrollTop', () => {
  it('MainLayout 的 .main-content 声明 overflow-anchor: none（唯一滚动容器上的根因修复）', async () => {
    const css = await compiledScopedCss(MAIN_LAYOUT)
    const rule = css.match(/\.main-content\[data-v-[a-z0-9]+\]\s*\{([^}]*)\}/)
    expect(rule, '找不到编译后的 .main-content 规则（分母为 0，断言会假绿）').not.toBeNull()
    expect(
      /overflow-anchor:\s*none/.test(rule![1]),
      '滚动容器缺少 overflow-anchor:none —— 异步渲染导致视口上方高度变化时，浏览器会静默改写 scrollTop（用户报障「页面自己往下滚」）',
    ).toBe(true)
    // 容器本身必须仍可滚动（修复不是"禁用滚动"）
    expect(rule![1], '不得为修自滚动而禁用滚动').toMatch(/overflow-y:\s*auto/)
  })

  it('反向护栏：删掉 overflow-anchor 声明后同一判据必须变红（证明判定器不是恒真）', async () => {
    const css = await compiledScopedCss(MAIN_LAYOUT)
    expect(mainContentHasAnchoringOff(css), '正常源码应判为 true').toBe(true)
    const mutated = css.replace(/overflow-anchor:\s*none;?/g, '')
    expect(
      mainContentHasAnchoringOff(mutated),
      '把声明删掉后判定器仍返回 true —— 它是恒真的，绿灯失去意义',
    ).toBe(false)
  })

  it('渲染路径不得出现无条件 scrollIntoView / window.scrollTo（防止换 API 再犯）', () => {
    for (const rel of [MAIN_LAYOUT, ROUTER_INDEX]) {
      const src = readFileSync(resolve(ROOT, rel), 'utf8')
      const code = src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/<!--[\s\S]*?-->/g, '').replace(/^\s*\/\/.*$/gm, '')
      expect(code, rel + ' 出现 scrollIntoView：渲染路径的滚动跳转会在异步内容到达后把用户卷走').not.toContain('scrollIntoView')
      // 只禁"无条件 scrollTo(0/顶)"这类会主动卷动页面的调用；window.scrollTo 在应用外壳模式
      // （html/body overflow:hidden）下本就无意义，出现即为可疑。
      expect(code, rel + ' 出现 window.scrollTo').not.toMatch(/window\.scrollTo\(/)
    }
  })

  it('MainLayout 模板仍把内容渲染进唯一的 .el-main.main-content（滚动容器不新增第二层）', () => {
    const tpl = sfcTemplate(MAIN_LAYOUT)
    expect(tpl).toMatch(/<el-main class="main-content"/)
    expect(tpl).toMatch(/<router-view/)
    // 页面内自建滚动容器会绕开本修复（滚动容器不唯一 ⇒ overflow-anchor 作用对象不确定），
    // 故内容槽里不得再套一层 overflow-y:auto 的容器（用 slider 包裹也属违约）。
    const mainBlock = tpl.slice(tpl.indexOf('<el-main'), tpl.indexOf('</el-main>'))
    expect(mainBlock, '内容槽里又套了一个滚动容器').not.toMatch(/overflow-y:\s*auto/)
  })
})
