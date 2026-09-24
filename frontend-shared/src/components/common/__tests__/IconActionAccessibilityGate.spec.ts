/**
 * 纯图标 <button> 可访问性门禁（回归锁）—— I-9 A 类修复的配套门禁。
 *
 * 背景（主控实测，2026-09-15）：把占位用的 `<el-icon @click>` 改成真 `<button>` 时，
 * 浏览器会给裸 button 套上**默认边框/背景/内边距**（灰色描边盒子）。实测缺陷：
 * `NodeOverview.vue` 的 `.ph-edit` 已改成 `<button>`，但当时**漏了 UA 复位**，
 * 铅笔图标被默认样式盒子包住 —— 与「视觉与改前一致」的承诺不符。
 * 本仓没有全局 button 复位（theme.css 无裸 button 选择器，Element Plus 只覆盖 .el-button），
 * 因此每个自绘 button 都必须自己复位。
 *
 * 为什么必须用「源码扫描」而不是渲染测试：这类错误**不报错、不 TS 报错**；
 * 渲染测试只覆盖有人写了用例的组件，而「新增一个图标按钮 → 忘记复位/忘记名字」
 * 在任何页面都可能发生。只有全站扫描能在新增页面时立刻发现。
 *
 * 判据（fail-closed）：对全站每个**纯图标原生 `<button>`**（内部除 `<el-icon>` 外无内容）
 * 断言四件事，缺一即报：
 *   ① `type="button"`（表单内不误触发 submit）；
 *   ② 有可访问名（`aria-label`/`:aria-label`）—— 图标按钮没有可见文本，
 *      没有名字对读屏就是「按钮」；且同类图标一页重复 N 次，必须带行内标识；
 *   ③ 其 class 的样式规则含 UA 复位（`border: 0` + `background: transparent`
 *      + `padding: 0` + `font: inherit`）；
 *   ④ 其 class 有 `:focus-visible` 焦点环（键盘用户必须看得见焦点）。
 *
 * 能力边界（与裁决文档 §5 同源）：本门禁是**源码层判据**。
 *   - 查不了运行时行为：不启动浏览器、不算 computed style、不跑 axe/Playwright；
 *   - 「class 的规则里写了复位」**不等于**该规则真的命中该元素（优先级、作用域、
 *     被覆盖都查不出来）——只保证「写了」，不保证「生效」；
 *   - 不认 `:class` 动态类名绑定（认不出即 fail-closed 报出，而非静默放过）；
 *   - 只覆盖「纯图标原生 button」；带可见文字的 button 由文字提供可访问名，不在本门禁内。
 *
 * 变红条件：新增/修改图标按钮时漏了 type/aria-label/UA 复位/焦点环，或有人删掉已有的任一项。
 */

import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { stripComments, walkSourceFiles } from '@/utils/__tests__/sourceScan'

// 与 placeholderDashGate / SwitchAccessibleNameGate 同口径：以 vitest 的 cwd 为根（frontend-shared/）
const SRC_DIR = join(process.cwd(), 'src')

/**
 * 全站业务源码（walkSourceFiles 已跳过 __tests__）。
 * `src/dev/**`（开发演示页 BmsDemoPage.vue）按裁决文档 §5.2 的口径**显式排除**：
 * 它不在 I-9 的 32 处分母内，本轮也未被裁决 —— 门禁不能替它做未做的判断。
 */
const SCAN_FILES = walkSourceFiles(SRC_DIR).filter(
  (file) => !relative(SRC_DIR, file).replace(/\\/g, '/').startsWith('dev/'),
)

/** 纯图标 button 的判据：开/闭标签之间除一个 <el-icon> 外没有别的内容 */
const PURE_ICON_INNER = /^\s*<el-icon\b[\s\S]*<\/el-icon>\s*$/

/** UA 复位四件套：浏览器默认样式必须被显式清掉 */
const UA_RESET = [
  { key: 'border', re: /border\s*:\s*(0|none)\b/ },
  { key: 'background', re: /background\s*:\s*(transparent|none)\b/ },
  { key: 'padding', re: /padding\s*:\s*0\b/ },
  { key: 'font', re: /font\s*:\s*inherit\b/ },
] as const

export interface IconButtonSite {
  /** 1-based 行号 */
  line: number
  /** 开标签原文 */
  tag: string
  /** 静态 class 列表（`:class` 动态绑定认不出，为空） */
  classes: string[]
  /** 缺失项：type | name | reset | focus */
  missing: string[]
}

/**
 * 找开标签结束 `>` 的下标，**跳过引号内的 `>`**。
 * 模板属性值本身就常含 `>`（箭头函数、比较）——朴素 indexOf 会把标签截断在属性中间，
 * 于是后面的 aria-label 看不见，门禁报出**假缺失**。
 */
function findTagEnd(source: string, start: number): number {
  let quote: string | null = null
  for (let i = start; i < source.length; i += 1) {
    const ch = source[i]
    if (quote) {
      if (ch === quote) quote = null
      continue
    }
    if (ch === '"' || ch === "'" || ch === '`') {
      quote = ch
      continue
    }
    if (ch === '>') return i
  }
  return -1
}

/** 抽出全部 <style> 块内容，用于查某个 class 的样式规则。 */
function styleText(source: string): string {
  let out = ''
  const re = /<style[^>]*>([\s\S]*?)<\/style>/g
  let m: RegExpExecArray | null
  while ((m = re.exec(source)) !== null) out += m[1] + '\n'
  return out
}

/** 取该 class 的规则体（不含 `:hover` 等伪类的基准规则 `{...}`）。 */
function ruleBody(styles: string, cls: string): string | null {
  const re = new RegExp('\\.' + cls.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\s*\\{([^}]*)\\}')
  const m = styles.match(re)
  return m ? m[1] : null
}

/** 该 class 是否有 :focus-visible 规则。 */
function hasFocusRing(styles: string, cls: string): boolean {
  const esc = cls.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return new RegExp('\\.' + esc + '\\s*:focus-visible').test(styles)
}

/**
 * 抽取一份源码里全部「纯图标 <button>」并逐项判定。
 * 导出以便**分类器自检**（同一纯函数喂正例/反例），这是本仓既有范式。
 */
export function iconButtonSitesIn(source: string): IconButtonSite[] {
  const stripped = stripComments(source)
  const styles = styleText(stripped)
  const sites: IconButtonSite[] = []
  const re = /<button\b/g
  let m: RegExpExecArray | null
  while ((m = re.exec(stripped)) !== null) {
    const start = m.index
    const gt = findTagEnd(stripped, start)
    if (gt < 0) break
    const close = stripped.indexOf('</button', gt)
    if (close < 0) continue
    const inner = stripped.slice(gt + 1, close)
    if (!PURE_ICON_INNER.test(inner)) { re.lastIndex = gt + 1; continue }
    const tag = stripped.slice(start, gt + 1)
    const classMatch = tag.match(/\bclass\s*=\s*"([^"]*)"/)
    const classes = classMatch ? classMatch[1].split(/\s+/).filter(Boolean) : []
    const missing: string[] = []
    if (!/\btype\s*=\s*"button"/.test(tag)) missing.push('type')
    if (!/:?aria-label\s*=/.test(tag)) missing.push('name')
    // fail-closed：认不出静态 class（例如只有 :class 动态绑定）就判为缺复位与焦点环
    const resetOk = classes.some((cls) => {
      const body = ruleBody(styles, cls)
      return body !== null && UA_RESET.every((d) => d.re.test(body))
    })
    if (!resetOk) missing.push('reset')
    if (!classes.some((cls) => hasFocusRing(styles, cls))) missing.push('focus')
    sites.push({ line: stripped.slice(0, start).split('\n').length, tag, classes, missing })
    re.lastIndex = gt + 1
  }
  return sites
}

describe('纯图标 <button> 可访问性门禁', () => {
  const files = SCAN_FILES

  it('全站每个纯图标 <button> 都有 type=button / 可访问名 / UA 复位 / :focus-visible', () => {
    const offenders: string[] = []
    let scannedButtons = 0
    for (const file of files) {
      for (const site of iconButtonSitesIn(readFileSync(file, 'utf-8'))) {
        scannedButtons++
        if (site.missing.length === 0) continue
        offenders.push(
          relative(process.cwd(), file) + ':' + site.line + ' 缺少 ' + site.missing.join(', ') +
            ' :: ' + site.tag.replace(/\s+/g, ' '),
        )
      }
    }
    expect(scannedButtons, '扫到的纯图标 button 为 0 —— 扫描器/判据失效，门禁形同虚设').toBeGreaterThan(3)
    expect(offenders, '纯图标 button 缺失项：\n' + offenders.join('\n')).toEqual([])
  })

  it('分母守卫：扫描器真的走到了全站源码', () => {
    // 若 walkSourceFiles 被改坏成「什么都扫不到」，下面两条会变红，
    // 从而避免「0 违规」其实来自「扫描器空转」的假绿。
    expect(files.length, '扫描到的源码文件数为 0 —— 扫描路径错了').toBeGreaterThan(100)
    const withIcon = files.filter((f) => iconButtonSitesIn(readFileSync(f, 'utf-8')).length > 0)
    // 阈值口径修正（2026-09-23）：原为 `>= 2`。当时第 2 个命中文件是**死文件**
    // `NodeDetail.vue`（本轮按 C5 删除；其 edit-icon 按钮在删除前已由 NodeOverview 的
    // mini-edit 等价覆盖）。该守卫的**意图**是"判据没被改坏 ⇒ 仍能找到纯图标 button 样本"，
    // 而非"必须 ≥2 个文件"—— 分母随合法清理下降会让 `>= 2` 变成假红。
    // 改为「文件数 ≥1」+「总命中数 ≥3」：判据真被改坏时命中会掉到 0，仍会红，故未放水。
    expect(withIcon.length, '没有任何文件命中纯图标 button —— 判据被改坏').toBeGreaterThanOrEqual(1)
    const sitesTotal = withIcon.reduce((n, f) => n + iconButtonSitesIn(readFileSync(f, 'utf-8')).length, 0)
    expect(sitesTotal, '纯图标 button 总命中数过低 —— 判据可能被改坏').toBeGreaterThanOrEqual(3)
  })

  it('分类器自检：正例必不报、反例必报（防止扫描器坏掉后永远绿）', () => {
    const GOOD_CLASS = '.act { border: 0; background: transparent; padding: 0; font: inherit; }'
    const GOOD_FOCUS = '.act:focus-visible { outline: 2px solid red; }'
    const good =
      '<template><button type="button" class="act" aria-label="重命名节点 X"><el-icon><Edit /></el-icon></button></template>' +
      '<style scoped>' + GOOD_CLASS + GOOD_FOCUS + '</style>'
    expect(iconButtonSitesIn(good)).toHaveLength(1)
    expect(iconButtonSitesIn(good)[0].missing).toEqual([])

    // ① 少 type
    const noType = '<button class="act" aria-label="重命名"><el-icon/><Edit /></el-icon></button><style>' + GOOD_CLASS + GOOD_FOCUS + '</style>'
    expect(iconButtonSitesIn(noType)[0].missing).toContain('type')
    // ② 少可访问名
    const noName = '<button type="button" class="act"><el-icon><Edit /></el-icon></button><style>' + GOOD_CLASS + GOOD_FOCUS + '</style>'
    expect(iconButtonSitesIn(noName)[0].missing).toContain('name')
    // ③ 少 UA 复位（这正是主控实测到的 .ph-edit 缺陷形态）
    const noReset = '<button type="button" class="act" aria-label="重命名"><el-icon><Edit /></el-icon></button><style>.act { color: red; }' + GOOD_FOCUS + '</style>'
    expect(iconButtonSitesIn(noReset)[0].missing).toContain('reset')
    // 复位只写一半（漏 font: inherit）同样必须报出
    const partialReset = '<button type="button" class="act" aria-label="重命名"><el-icon><Edit /></el-icon></button><style>.act { border: 0; background: transparent; padding: 0; }' + GOOD_FOCUS + '</style>'
    expect(iconButtonSitesIn(partialReset)[0].missing).toContain('reset')
    // ④ 少 :focus-visible 焦点环
    const noFocus = '<button type="button" class="act" aria-label="重命名"><el-icon><Edit /></el-icon></button><style>' + GOOD_CLASS + '</style>'
    expect(iconButtonSitesIn(noFocus)[0].missing).toContain('focus')

    // 反例：非纯图标（含可见文字）⇒ 由文字提供可访问名，不属于本门禁
    const withText = '<button type="button">查看</button>'
    expect(iconButtonSitesIn(withText)).toHaveLength(0)
    // 反例：<el-button> 不是原生 button，不在本门禁内（另有 el-* 门禁）
    const epButton = '<el-button type="button" aria-label="x"><el-icon><Edit /></el-icon></el-button>'
    expect(iconButtonSitesIn(epButton)).toHaveLength(0)
    // 反例：动态 :class 认不出 ⇒ fail-closed 报 reset + focus，而不是静默放过
    const dynamicClass = '<button type="button" :class="k" aria-label="重命名"><el-icon><Edit /></el-icon></button><style>' + GOOD_CLASS + GOOD_FOCUS + '</style>'
    const dyn = iconButtonSitesIn(dynamicClass)
    expect(dyn).toHaveLength(1)
    expect(dyn[0].missing).toEqual(expect.arrayContaining(['reset', 'focus']))

    // 属性值里含 '>' 时不得把标签截断（否则 aria-label 看不见 ⇒ 假缺失）
    const gtInAttr = '<button type="button" class="act" aria-label="重命名" :title="(a ?? 2) > 1 ? \'x\' : \'y\'"><el-icon><Edit /></el-icon></button><style>' + GOOD_CLASS + GOOD_FOCUS + '</style>'
    expect(iconButtonSitesIn(gtInAttr)[0].missing).toEqual([])
    // 行号必须准确（失败信息要能直接定位）
    const lines = '<div>\n  <span/>\n  <button type="button" class="act" aria-label="x"><el-icon><Edit /></el-icon></button>\n</div><style>' + GOOD_CLASS + GOOD_FOCUS + '</style>'
    expect(iconButtonSitesIn(lines)[0].line).toBe(3)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 第二节：非原生可聚焦元素上的 @click（I-9 裁决文档的分母口径）
//
// 判据：全站扫描「挂了 @click、但既无 tabindex、也无 role、也无 @keydown 的元素」，
// 断言它们要么有完整的 role + tabindex + @keydown.enter + @keydown.space 四件套，
// 要么是**已登记例外**（下方 REGISTERED_EXCEPTIONS，逐条对应裁决文档 ID）。
//
// 组件白名单依据（这是本节最容易做成「持续误报」的地方，必须写清）：
//   `StatCard.vue` 自身在 `attrs.onClick` 存在时注入完整可访问性 —— 第 2-11 行原文：
//     <div class="stat-card" v-bind="attrs"
//       :role="isClickable ? 'button' : undefined"
//       :tabindex="isClickable ? 0 : undefined"
//       :aria-label="isClickable ? `查看${label}` : undefined"
//       @keydown.enter.prevent="handleKeyboardActivate"
//       @keydown.space.prevent="handleKeyboardActivate">
//   第 43-53 行：`isClickable = computed(() => Boolean(attrs.onClick))`，
//   `handleKeyboardActivate` 把键盘事件转发给 `attrs.onClick`。
//   ⇒ 扫描器看到的是**组件使用点** `<StatCard @click=...>`，真正的宿主 `<div>`
//     在运行时已带全套属性。裁决文档 B 类 6 处全部是它（NodeList ×3 / EdgeDeviceList ×3），
//     故列入白名单；这不是「放过缺陷」，而是「扫描器不认识组件语义」的已知假阳性。
//   同理，原生即可聚焦的标签/组件（button/a/input/…/el-button/el-link/router-link）
//   在**判据层**直接排除，无需逐处登记。
//
// 能力边界（同源裁决文档 §5）：纯源码层判据，查不了运行时行为 —— 不启动浏览器、
// 不验证 role/tabindex 在真实 DOM 里是否生效、也无法证明「已登记例外」的等价入口
// 真的键盘可达（D 类的判据全部来自人工代码阅读）。
// ────────────────────────────────────────────────────────────────────────────
/** 原生即可聚焦/可激活的 HTML 标签（判据层直接排除） */
const NATIVE_FOCUSABLE_HTML = new Set([
  'button', 'a', 'input', 'select', 'textarea', 'summary', 'details',
  'label', 'audio', 'video', 'iframe', 'area', 'object', 'embed',
])

/** Element Plus / vue-router 里自带可聚焦语义的组件 */
const NATIVE_FOCUSABLE_COMPONENTS = new Set(['el-button', 'el-link', 'router-link'])

/** 自带可访问性的组件（依据见上方文件头；裁决文档 B 类 6 处） */
const COMPONENT_WHITELIST = new Set(['StatCard'])

/** 只有交互角色的 role 才算「已声明角色」；role="row" 之类不构成键盘入口 */
const INTERACTIVE_ROLES = new Set([
  'button', 'link', 'tab', 'menuitem', 'option', 'radio', 'checkbox', 'switch', 'treeitem',
])

/**
 * 已登记例外：裁决文档已逐处判为非 A 类（B/C/D）的位置。
 * key = 相对路径 + '::' + 首个 @click 表达式（不含行号，重构换行不会造成假红）。
 * value = 裁决文档 ID + 分类，便于审计时回去查原文。
 */
const REGISTERED_EXCEPTIONS = new Map<string, string>([
  // C 类：@click.stop 只为阻止冒泡，元素自身不产生动作
  ['src/components/channel/ChannelTerminal.vue::toggleExpand(\'tx\', index)', '#28 D：同一终端「导出」按钮输出未截断全量 hex，等价入口'],
  ['src/components/channel/ChannelTerminal.vue::toggleExpand(\'rx\', index)', '#29 D：同上，同一「导出」按钮覆盖两个面板'],
  ['src/views/dashboard/Dashboard.vue::router.push(\'/node?status=offline\')', '#20 D：统计卡 el-card 可达 /node + NodeList 状态筛选'],
  ['src/views/dashboard/Dashboard.vue::router.push(\'/edge-device?status=offline\')', '#21 D：统计卡可达 /edge-device + 列表状态筛选'],
  ['src/views/dashboard/Dashboard.vue::router.push(\'/data\')', '#22 D：侧栏 el-menu「数据面板」roving tabindex 可达'],
  ['src/views/layout/MainLayout.vue::router.push(\'/dashboard\')', '#1 D：侧栏 el-menu「仪表盘」项 roving tabindex 可达'],
  ['src/views/layout/MainLayout.vue::handleMobileLogoClick', '#2 D：抽屉内同一 el-menu 仪表盘项 :tabindex=0'],
  ['src/views/node/NodeOverview.vue::refreshAll', '#14 D：页头原生 <button>刷新</button> 完全等价'],
  // 2026-09-20 D1 修复后移除：'src/views/node/NodeOverview.vue::goToDetail'。
  // 原例外理由写的是「目标即当前路由」—— 那句话本身就是缺陷描述（点通道行推当前页 = 原地打转），
  // 登记成例外等于把缺陷合法化，还让修复者因为条目变 stale 而报红。
  // 现该入口为带四件套的 goToNodeChannels（跳 /channel?node=<序列号>），**不得再登记为例外**。
  ['src/views/node/NodeOverview.vue::selectedResourceId = resource.id', '#18 D：行内「资源名」「查看」两个原生 button'],
  ['src/views/node/NodeOverview.vue::viewDevice(row)', '#19 D：视图切「列表」后每行原生「查看」button'],
])

export interface ClickSite {
  line: number
  tagName: string
  /** 首个 @click 的表达式（用于登记例外与失败信息） */
  handler: string
  /** 开标签原文 */
  tag: string
  hasCompleteCombo: boolean
}

/** 抽出全部「非原生可聚焦元素 + @click」并判定四件套是否完整。 */
export function clickOnNonFocusableSitesIn(source: string): ClickSite[] {
  const stripped = stripComments(source)
  const sites: ClickSite[] = []
  const re = /<([A-Za-z][-A-Za-z0-9_.]*)\b/g
  let m: RegExpExecArray | null
  while ((m = re.exec(stripped)) !== null) {
    const tagName = m[1]
    const start = m.index
    const gt = findTagEnd(stripped, start)
    if (gt < 0) break
    const tag = stripped.slice(start, gt + 1)
    re.lastIndex = gt + 1
    if (!/@click|v-on:click/.test(tag)) continue
    const low = tagName.toLowerCase()
    if (NATIVE_FOCUSABLE_HTML.has(low) || NATIVE_FOCUSABLE_COMPONENTS.has(low)) continue
    if (COMPONENT_WHITELIST.has(tagName)) continue
    const clicks = tag.match(/(?:@click|v-on:click)(?:\.[\w.-]+)*\s*=\s*("[^"]*"|'[^']*')/g) || []
    const handler = (clicks[0] || '')
      .replace(/^(?:@click|v-on:click)(?:\.[\w.-]+)*\s*=\s*/, '')
      .replace(/^["']|[\"']$/g, '')
      .trim()
    // @click.stop 无 handler：只为阻止冒泡，C 类，无动作可触发
    if (handler === '') continue
    const roleMatch = tag.match(/\brole\s*=\s*["']([^"']*)["']/)
    const hasRole = roleMatch !== null && INTERACTIVE_ROLES.has(roleMatch[1].trim().toLowerCase())
    const hasTabindex = /\btabindex\s*=/.test(tag)
    const kd = [...tag.matchAll(/@keydown((?:\.[\w-]+)*)/g)].map((x) => x[1]).join('.').split('.').filter(Boolean)
    const hasEnter = kd.includes('enter')
    const hasSpace = kd.includes('space')
    sites.push({
      line: stripped.slice(0, start).split('\n').length,
      tagName,
      handler,
      tag,
      hasCompleteCombo: hasRole && hasTabindex && hasEnter && hasSpace,
    })
  }
  return sites
}

export function clickSiteKey(relFile: string, site: ClickSite): string {
  return relFile + '::' + site.handler
}

describe('非原生可聚焦元素 @click 门禁（I-9 分母口径）', () => {
  const files = SCAN_FILES

  it('每个 @click 位置要么有完整 role+tabindex+keydown 四件套，要么是已登记例外', () => {
    const offenders: string[] = []
    let scanned = 0
    const seen = new Set<string>()
    for (const file of files) {
      const rel = relative(process.cwd(), file)
      for (const site of clickOnNonFocusableSitesIn(readFileSync(file, 'utf-8'))) {
        scanned++
        const key = clickSiteKey(rel, site)
        seen.add(key)
        if (site.hasCompleteCombo) continue
        if (REGISTERED_EXCEPTIONS.has(key)) continue
        offenders.push(
          rel + ':' + site.line + ' <' + site.tagName + '> 缺 role/tabindex/@keydown.enter/@keydown.space' +
            ' :: ' + site.tag.replace(/\s+/g, ' '),
        )
      }
    }
    expect(scanned, '扫到的 @click 位置 < 5 —— 扫描器空转，门禁形同虚设').toBeGreaterThan(5)
    expect(offenders, '键盘不可达的 @click 位置：\n' + offenders.join('\n')).toEqual([])
    // 反向守卫：登记表里若有条目已经对不上任何真实位置（被改名/删除），必须报出来，
    // 否则这张表会越长越大，逐渐掩盖新缺陷。
    const stale = [...REGISTERED_EXCEPTIONS.keys()].filter((k) => !seen.has(k))
    expect(stale, 'REGISTERED_EXCEPTIONS 中已失效的条目（源码已变，请复核）：\n' + stale.join('\n')).toEqual([])
  })

  it('分母守卫：扫描器必须真的走到全站源码且判据不是恒真', () => {
    expect(files.length, '扫描到的源码文件数为 0 —— 扫描路径错了').toBeGreaterThan(100)
    // 判据必须能区分：同一段源码里，补上四件套后不应再被当作候选
    const bare = '<template><div class="x" @click="go()">x</div></template>'
    const fixed = '<template><div class="x" role="button" tabindex="0" @click="go()" @keydown.enter.prevent="go()" @keydown.space.prevent="go()">x</div></template>'
    expect(clickOnNonFocusableSitesIn(bare)).toHaveLength(1)
    expect(clickOnNonFocusableSitesIn(fixed)[0].hasCompleteCombo).toBe(true)
  })

  it('分类器自检：正例必报、反例必不报（防止扫描器坏掉后永远绿）', () => {
    // 违规：裸 div 带 @click
    expect(clickOnNonFocusableSitesIn('<div @click="go()">x</div>')[0].hasCompleteCombo).toBe(false)
    // 缺三件套中的任意一件都算不完整（只有 tabindex 没有 keydown = 能聚焦但按不动）
    const partial = [
      '<div tabindex="0" @click="go()">x</div>',
      '<div role="button" @click="go()">x</div>',
      '<div role="button" tabindex="0" @click="go()" @keydown.enter.prevent="go()">x</div>',
      '<div role="button" tabindex="0" @click="go()" @keydown.space.prevent="go()">x</div>',
    ]
    for (const p of partial) expect(clickOnNonFocusableSitesIn(p)[0].hasCompleteCombo).toBe(false)
    // 反例：原生可聚焦标签不在判据内
    expect(clickOnNonFocusableSitesIn('<button @click="go()">x</button>')).toHaveLength(0)
    expect(clickOnNonFocusableSitesIn('<a href="#" @click="go()">x</a>')).toHaveLength(0)
    expect(clickOnNonFocusableSitesIn('<el-button @click="go()">x</el-button>')).toHaveLength(0)
    // 反例：白名单组件（StatCard 自带可访问性，见文件头依据）
    expect(clickOnNonFocusableSitesIn('<StatCard label="x" @click="go()" />')).toHaveLength(0)
    // 反例：@click.stop 无 handler（C 类，只阻止冒泡）
    expect(clickOnNonFocusableSitesIn('<div class="x" @click.stop>y</div>')).toHaveLength(0)
    // 反例：注释里的 @click 不算（stripComments 必须生效）
    expect(clickOnNonFocusableSitesIn('<!-- 这里解释 <div @click="go()"> 为什么危险 -->')).toHaveLength(0)
    expect(clickOnNonFocusableSitesIn('// <div @click="go()">')).toHaveLength(0)
    // role="row" 不是交互角色，不能当作键盘入口
    expect(clickOnNonFocusableSitesIn('<tr role="row" tabindex="0" @click="go()" @keydown.enter.prevent="go()" @keydown.space.prevent="go()">x</tr>')[0].hasCompleteCombo).toBe(false)
    // 行号与 handler 必须准确（失败信息要能直接定位、登记表要能对上）
    const s = clickOnNonFocusableSitesIn('<div>\n  <span/>\n  <div class="chan-row" @click="goToDetail">x</div>\n</div>')[0]
    expect(s.line).toBe(3)
    expect(s.handler).toBe('goToDetail')
  })
})
