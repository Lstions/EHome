import { describe, it, expect } from 'vitest'
import { parse } from '@vue/compiler-sfc'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { execSync } from 'node:child_process'

/**
 * F8 移动端宽表横滚合同 —— 口径基线与豁免白名单（规范 §4.3.2.2 MUST / §4.4.1 MUST）。
 *
 * 审计结论：含 el-table 的页面多数没有包 .mobile-table-wrapper。窄屏下
 * fixed="right" 操作列实测可占表格宽 117.9%，且 el-table 自身 overflow:hidden，
 * 既不可达也没有横滑提示。
 *
 * 本文件负责**审计口径的可持续性**：
 *   ① 口径是什么（含 <el-table 元素的 .vue，排除 __tests__）；
 *   ② 每个未包裹的文件必须在豁免白名单里登记具体理由；
 *   ③ 白名单不得腐烂（登记的文件必须真实存在，且理由必须成立）。
 *
 * 这里用**结构扫描**判定"是否被包裹"，不是字符串包含：
 * 对 <template> 做标签栈解析，只有 .mobile-table-wrapper 真的在 el-table 的
 * **祖先链**上才算包裹。`expect(src).toContain('class="mobile-table-wrapper"')`
 * 无法区分「包住了这张表」还是「包住了另一张表 / 只写在注释里」——
 * 本仓已有 6 例「伪装成正常」的教训，故下面配了 4 个反证用例证明它不是恒真。
 *
 * 每个页面的**真实 DOM 祖先链**断言放在各页自己的 spec 里（用该页既有的
 * mount 夹具）：
 *   AutomationRules.spec.ts / DataSourceList.spec.ts / Dashboard.spec.ts /
 *   FirmwareManage.spec.ts / LogicalDeviceList.spec.ts / LogHistoryPanel.spec.ts
 * 像素级可达性（elementFromPoint / fixed 列越界）由真浏览器探针验收：
 *   frontend-shared/.tmp-probe/f8-f10-probe.mjs
 */

const ROOT = process.cwd()

/** HTML/Vue 模板中的自闭合（void）元素：不需要闭合标签。 */
const VOID_TAGS = new Set(['br', 'hr', 'img', 'input', 'area', 'base', 'col', 'embed', 'link', 'meta', 'source', 'track', 'wbr'])

const TAG_RE = /<(\/?)([A-Za-z][A-Za-z0-9-]*)((?:"[^"]*"|'[^']*'|[^>"'])*?)(\/?)\s*>/g

/**
 * 标签栈扫描：返回 <template> 内每张 el-table 的祖先链上是否出现
 * .mobile-table-wrapper，以及该 wrapper 的直接子节点里有没有 .mobile-table-hint。
 *
 * 只做 div/template 级别的结构配对 —— Vue 模板是类 XML 的，凡是作为容器的
 * <div> 都必须闭合，因此对"wrapper 是否包住 el-table"这个问题，栈解析是精确的。
 */
export function scanTables(templateSource: string): Array<{ wrapped: boolean; hintInside: boolean }> {
  // 先剥注释：本仓大量注释里写着 .mobile-table-wrapper 的使用说明，
  // 不剥会把"注释里提到"误判成"结构上包裹"。
  const src = templateSource.replace(/<!--[\s\S]*?-->/g, '')
  // 每一帧记录：是否为 wrapper、以及**曾经**作为其直接子节点出现过 hint。
  // 不能查"当前栈上是否还有 hint 开着" —— hint 是自闭合语义的兄弟节点，
  // 到 el-table 出现时它早就闭合了（这正是第一版写错的地方）。
  const stack: Array<{ tag: string; wrapper: boolean; sawHintChild: boolean }> = []
  const out: Array<{ wrapped: boolean; hintInside: boolean }> = []
  let m: RegExpExecArray | null
  TAG_RE.lastIndex = 0
  while ((m = TAG_RE.exec(src)) !== null) {
    const [, closing, tag, rawAttrs, selfClose] = m
    const attrs = rawAttrs || ''
    const lower = tag.toLowerCase()
    if (closing === '/') {
      // 弹出到最近的同名开标签（容错：忽略不配对的闭合）
      for (let i = stack.length - 1; i >= 0; i -= 1) {
        if (stack[i].tag === lower) { stack.length = i; break }
      }
      continue
    }
    const isWrapper = lower === 'div' && /class="[^"]*\bmobile-table-wrapper\b[^"]*"/.test(attrs)
    // hint 用哪个标签都行（DataPanel 用的是 <p class="mobile-table-hint">），
    // theme.css 的 .mobile-table-hint 规则与标签无关。
    const isHint = /class="[^"]*\bmobile-table-hint\b[^"]*"/.test(attrs)
    if (lower === 'el-table') {
      const wrapperIdx = stack.map(s => s.wrapper).lastIndexOf(true)
      out.push({
        wrapped: wrapperIdx !== -1,
        // hint 必须是该 wrapper 的**直接子节点**，不能挂在别的卡片里
        hintInside: wrapperIdx !== -1 && stack[wrapperIdx].sawHintChild,
      })
      // el-table 一定闭合，不入栈（避免列组件影响配对）
      continue
    }
    // hint 开在谁下面，就记在谁的账上（只有直接父节点是 wrapper 才算数）
    if (isHint && stack.length > 0) stack[stack.length - 1].sawHintChild = true
    if (selfClose === '/' || VOID_TAGS.has(lower)) continue
    stack.push({ tag: lower, wrapper: isWrapper, sawHintChild: false })
  }
  return out
}

/**
 * 口径：src 下**真的出现 <el-table 元素**的 .vue（排除 __tests__）。
 *
 * 模式必须是 '<el-table'，**不能**写成 '<el-table[ >]'：本仓有 2 个文件
 * （ChannelList.vue / EdgeDeviceList.vue）把 el-table 的写法是
 * `<el-table\n  :data=...`，属性从下一行开始，紧跟标签名的是换行符而不是
 * 空格/'>'，用 [ >] 会把这两个**已正确包裹**的文件静默漏掉 ——
 * 漏掉不是"少报一个"，而是让它们永远不会被本 spec 检查（隐蔽的假绿）。
 */
function auditFiles(): string[] {
  const out = execSync("grep -rl '<el-table' src --include=*.vue || true", { cwd: ROOT, encoding: 'utf8' })
  return out.split('\n').map(s => s.trim()).filter(Boolean).filter(f => !f.includes('__tests__'))
}

function templateOf(rel: string): string {
  const raw = readFileSync(resolve(ROOT, rel), 'utf8')
  const { descriptor, errors } = parse(raw, { filename: rel })
  expect(errors, rel + ' SFC 解析失败: ' + JSON.stringify(errors)).toEqual([])
  expect(descriptor.template, rel + ' 没有 <template>').not.toBeNull()
  return descriptor.template!.content
}

/**
 * 含 el-table 但**经逐文件裁决无需包裹**的文件，连同具体理由。
 * 新增未包裹的 el-table 必须在此登记，否则本 spec 变红。
 * 裁决依据见 .logs/f8-f10-report.md 的完整裁决表。
 */
const EXEMPT: Record<string, string> = {
  'src/App.vue':
    '仅 CSS，无 el-table 元素：@media(max-width:480px){ .el-table{font-size:15px} } —— 移动端字号，不产生布局盒',
  'src/views/layout/MainLayout.vue':
    '仅 CSS，无 el-table 元素：@media(max-width:768px){ .el-table .el-button--small / .el-switch } 触控尺寸规则，属全局兜底',
  'src/styles/theme.css':
    '范式定义处：.mobile-table-wrapper / .mobile-table-hint / .el-table 全局规则本体，非页面',
  'src/test-setup.ts':
    "ElTable 测试替身（h('table', { class: 'el-table' })），非业务表格",
}

/**
 * **真实缺口，但因并发写者禁改而推迟**的文件。与 EXEMPT 的区别是：
 * 这些文件确实需要包裹，只是本任务一度拿不到写权限（改了会与另一个子代理互相覆盖）。
 * 登记在这里 = 缺口被跟踪，而不是被藏起来；谁接手谁把条目移到已包裹一侧。
 *
 * 当前为空：AlertRules.vue 曾是唯一一条，写者释放后已由本任务包裹完成
 * （见下方「关键业务页」用例）。保留这个常量是为了让后来者仍有登记缺口的手段。
 */
const DEFERRED: Record<string, string> = {}

describe('F8 移动端宽表横滚合同 — 口径基线与豁免白名单', () => {
  it('口径可复现：含 <el-table 元素的 .vue 数量稳定，且判定器对每个文件都给出结论', () => {
    const files = auditFiles()
    // 口径跑空守卫：如果 grep 模式失效，这个数字会掉到 0，后续断言会全部假绿
    expect(files.length, '口径跑空（0 个文件）').toBeGreaterThan(5)
    for (const f of files) {
      const tables = scanTables(templateOf(f))
      // 文件里必须有可识别的 el-table 元素；为 0 说明扫描器坏了
      expect(tables.length, f + ' 扫出 0 张 el-table，扫描器或口径失效').toBeGreaterThan(0)
    }
  })

  it('每张 el-table 要么在 .mobile-table-wrapper 祖先链上，要么文件在豁免白名单里', () => {
    const failures: string[] = []
    for (const f of auditFiles()) {
      const tables = scanTables(templateOf(f))
      const unwrapped = tables.filter(t => !t.wrapped).length
      if (unwrapped === 0) {
        // 反向守卫：已包裹的文件不得残留在 DEFERRED 里（否则缺口清单会腐烂）
        if (DEFERRED[f]) failures.push(`${f}: 已包裹，请从 DEFERRED 白名单删除该条`)
        continue
      }
      const reason = EXEMPT[f] ?? DEFERRED[f]
      if (!reason) failures.push(`${f}: ${unwrapped}/${tables.length} 张表未包裹，且无豁免/推迟理由`)
      else if (reason.includes('已包裹')) failures.push(`${f}: 豁免理由不成立（实测有未包裹的表）`)
    }
    expect(failures, '未包裹且未登记豁免的文件：\n' + failures.join('\n')).toEqual([])
  })

  it('被包裹的表同时有 .mobile-table-hint 直接子节点（横滑提示不能只在注释里）', () => {
    const failures: string[] = []
    for (const f of auditFiles()) {
      for (const [i, t] of scanTables(templateOf(f)).entries()) {
        if (t.wrapped && !t.hintInside) failures.push(`${f} 第 ${i + 1} 张表：有 wrapper 但 wrapper 内没有 .mobile-table-hint 直接子节点`)
      }
    }
    expect(failures, failures.join('\n')).toEqual([])
  })

  it('关键业务页确实被判为「已包裹」（防止上面几条恒真的空集通过）', () => {
    // 这 5 个页面是本任务新包裹的；如果扫描器恒返回 wrapped=true，它们会通过，
    // 但下面的反证用例会红。这里同时断言它们的**每一张**表都被包裹。
    for (const f of [
      'src/views/automation/AutomationRules.vue',
      'src/views/data-source/DataSourceList.vue',
      'src/views/dashboard/Dashboard.vue',
      'src/views/firmware/FirmwareManage.vue',
      'src/views/logical-device/LogicalDeviceList.vue',
      'src/components/node/LogHistoryPanel.vue',
      'src/views/alert/AlertRules.vue',
    ]) {
      const tables = scanTables(templateOf(f))
      expect(tables.length, f + ' 应至少有一张表').toBeGreaterThan(0)
      expect(tables.every(t => t.wrapped), f + ' 仍有未包裹的表').toBe(true)
      expect(tables.every(t => t.hintInside), f + ' 仍有缺 hint 的表').toBe(true)
    }
  })

  // ── 反证组：证明判定器能变红（不是恒真） ──────────────────────────────

  it('反证①：class 改名后必须判为未包裹', () => {
    const tpl = '<div class="mobile-table-wrapper"><div class="mobile-table-hint">x</div><el-table /></div>'
    expect(scanTables(tpl)).toEqual([{ wrapped: true, hintInside: true }])
    const mutated = tpl.replace('mobile-table-wrapper', 'not-a-wrapper')
    expect(scanTables(mutated)).toEqual([{ wrapped: false, hintInside: false }])
  })

  it('反证②：wrapper 只包住别的元素、没包住 el-table 时必须判为未包裹', () => {
    const tpl = '<div class="mobile-table-wrapper"><div class="mobile-table-hint">x</div></div><el-table />'
    expect(scanTables(tpl)).toEqual([{ wrapped: false, hintInside: false }])
  })

  it('反证③：hint 在 wrapper 之外（兄弟节点）时必须判为 hintInside=false', () => {
    const tpl = '<div><div class="mobile-table-hint">x</div><div class="mobile-table-wrapper"><el-table /></div></div>'
    expect(scanTables(tpl)).toEqual([{ wrapped: true, hintInside: false }])
  })

  it('反证④：wrapper 只出现在注释里时必须判为未包裹', () => {
    // 本仓注释里大量引用 .mobile-table-wrapper 的用法说明，这是最容易被骗过的一种
    const tpl = '<!-- 移动端宽表：见 theme.css .mobile-table-wrapper --><div><el-table /></div>'
    expect(scanTables(tpl)).toEqual([{ wrapped: false, hintInside: false }])
  })

  it('反证⑤：多张表时逐张判定，不能一张包裹就整页通过', () => {
    const tpl = [
      '<div class="mobile-table-wrapper"><div class="mobile-table-hint">x</div><el-table /></div>',
      '<el-table />',
    ].join('')
    expect(scanTables(tpl)).toEqual([{ wrapped: true, hintInside: true }, { wrapped: false, hintInside: false }])
  })

  // ── 白名单防腐 ─────────────────────────────────────────────────────

  it('豁免白名单不得腐烂：文件必须存在，且"仅 CSS/替身"的理由必须成立', () => {
    const files = auditFiles()
    for (const [f, reason] of Object.entries(EXEMPT)) {
      const src = readFileSync(resolve(ROOT, f), 'utf8')
      expect(src.length, f + ' 已不存在，请从豁免白名单移除').toBeGreaterThan(0)
      if (/仅 CSS|测试替身|范式定义处/.test(reason)) {
        expect(files, f + ' 登记为「' + reason + '」，但文件里其实有 <el-table 元素').not.toContain(f)
      }
    }
    // DEFERRED 的每一条都必须真的是"有表且未包裹"，且理由里写明为什么推迟
    for (const [f, reason] of Object.entries(DEFERRED)) {
      const tables = scanTables(templateOf(f))
      expect(tables.length, f + ' 登记为推迟，但文件里没有 el-table').toBeGreaterThan(0)
      expect(tables.some(t => !t.wrapped), f + ' 登记为推迟，但实测已全部包裹').toBe(true)
      expect(reason, f + ' 的推迟理由必须写明原因（含「禁止」「另一任务」「推迟」之一）').toMatch(/禁止|另一任务|推迟/)
    }
  })
})
