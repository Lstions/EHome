import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

/**
 * PageHeader 的 props 门禁（回归锁）。
 *
 * 背景（实测缺陷，2026-09-15）: `AlertRules.vue` 与 `AutomationRules.vue` 曾写成
 * `<PageHeader title="…" description="…" />`，而 PageHeader 只声明了
 * `title` / `subtitle` / `showBack` 三个 prop —— `description` 不在其中，
 * 于是它**静默**变成 fallthrough attribute 落到根 div 上，**副标题根本不渲染**。
 *
 * 为什么必须用"源码扫描"而不是渲染测试来钉住它:
 *   渲染测试只能覆盖**有人写了测试的那些页面**；而这类错误的本质是
 *   "**任何**页面都可能把 prop 名写错，且写错后**没有任何报错**"（不是 TS 错误、
 *   不是运行时异常、Element/Vue 都不会警告）。真实浏览器实测：
 *     /alerts      -> 页头文本只有 "告警规则"（0 个副标题节点）
 *     /data-sources-> 页头文本含 "数据源 为逻辑设备的数据类别声明主备来源…"（有副标题）
 *   ⇒ 只有"扫描全部调用点 + 与真实 props 白名单比对"才能在**新增页面时**立刻发现。
 *
 * 变红条件:
 *   - 有人在新页面用了 description=（或任何拼错的 prop 名）;
 *   - 有人给 PageHeader 加了新 prop 却没同步白名单（此时是"白名单过期"，也应更新本文件）;
 *   - 有人删掉了某页面的 subtitle=（那属于功能回退，由各页自己的渲染测试覆盖，不在本门禁范围）。
 */

const VIEWS_DIR = join(process.cwd(), 'src/views')
// PageHeader 的**真实** props（与 src/components/common/PageHeader.vue 的 defineProps 对齐）。
// 注意: Vue 模板里 prop 可写成 camelCase 或 kebab-case（`showBack` ↔ `show-back`），
// 两者都合法 —— 门禁必须同时接受，否则会误报（本文件初版就误报过 show-back）。
const DECLARED_PROPS = ['title', 'subtitle', 'showBack']
const ALLOWED_PROPS = new Set<string>()
for (const p of DECLARED_PROPS) {
  ALLOWED_PROPS.add(p)
  ALLOWED_PROPS.add(p.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase()))
}
// 这些不是组件 prop，而是**任何元素都合法**的 attribute/指令（fallthrough 是预期行为）。
const ALWAYS_ALLOWED = new Set([
  'class', 'style', 'id', 'key', 'ref', 'is',
  // v-* 指令与修饰符（扫描器剥掉 v- 前缀后可能拿到这些）
  'if', 'else', 'else-if', 'for', 'show', 'model', 'bind', 'on', 'html', 'text', 'once', 'memo', 'cloak', 'pre', 'slot', 'deep', 'global',
])

function walk(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === '__tests__') continue
      out.push(...walk(p))
    } else if (name.endsWith('.vue')) {
      out.push(p)
    }
  }
  return out
}

/** 抽出所有 <PageHeader ...> 开标签里的属性名（含多行形式）。 */
export function pageHeaderPropsIn(source: string): { file: string; props: string[] }[] {
  const found: { file: string; props: string[] }[] = []
  // 匹配 <PageHeader ... > 或 <PageHeader ... />
  const re = /<PageHeader\b([^>]*?)\/?>/gs
  let m: RegExpExecArray | null
  while ((m = re.exec(source)) !== null) {
    const attrs = m[1]
    // 收集 attr="..." / :attr="..." / attr='...' 里的名字
    const names: string[] = []
    const attrRe = /(?:^|\s)(:?)([A-Za-z][\w-]*)\s*=/g
    let a: RegExpExecArray | null
    while ((a = attrRe.exec(attrs)) !== null) {
      names.push(a[2])
    }
    found.push({ file: '', props: names })
  }
  return found
}

describe('PageHeader props 门禁', () => {
  it('全站 PageHeader 调用点只使用真实存在的 props', () => {
    const offenders: string[] = []
    let scanned = 0
    let callSites = 0

    for (const file of walk(VIEWS_DIR)) {
      const src = readFileSync(file, 'utf8')
      scanned++
      for (const { props } of pageHeaderPropsIn(src)) {
        callSites++
        for (const p of props) {
          if (ALLOWED_PROPS.has(p) || ALWAYS_ALLOWED.has(p)) continue
          {
            offenders.push(`${file.replace(process.cwd() + '/', '')}: 用了不存在的 prop "${p}"（PageHeader 只接受 ${[...ALLOWED_PROPS].join(' / ')}）`)
          }
        }
      }
    }

    // 防止"扫描器什么也没扫到"造成假绿
    expect(scanned, '扫描到的 .vue 文件数为 0 —— 扫描器路径错了').toBeGreaterThan(10)
    expect(callSites, '扫到的 PageHeader 调用点为 0 —— 正则失效，门禁形同虚设').toBeGreaterThan(5)
    expect(offenders, `PageHeader 用了不存在的 prop（会静默变成 fallthrough attr，副标题不渲染）:\n${offenders.join('\n')}`).toEqual([])
  })

  it('分类器自检: 能认出正确与错误的 prop（防止扫描器坏掉后永远绿）', () => {
    // 正确写法
    expect(pageHeaderPropsIn('<PageHeader title="A" subtitle="B" />')[0].props).toEqual(['title', 'subtitle'])
    // 曾经的真实缺陷写法
    const bad = pageHeaderPropsIn('<PageHeader title="告警规则" description="越限自动告警" />')
    expect(bad[0].props).toEqual(['title', 'description'])
    expect(bad[0].props.some((p) => !ALLOWED_PROPS.has(p))).toBe(true)
    // 多行 + #extra 插槽形式（Monitor.vue 的写法）
    const multi = pageHeaderPropsIn('<PageHeader title="系统监控">\n  <template #extra><button/></template>\n</PageHeader>')
    expect(multi[0].props).toEqual(['title'])
    // 绑定式写法
    expect(pageHeaderPropsIn('<PageHeader :title="t" :show-back="true" />')[0].props).toEqual(['title', 'show-back'])
  })
})
