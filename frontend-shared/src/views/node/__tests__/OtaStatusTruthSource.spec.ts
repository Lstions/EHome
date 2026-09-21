import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import nodeDetailSource from '../NodeDetail.vue?raw'
import nodeOverviewSource from '../NodeOverview.vue?raw'

/**
 * OTA 状态机一致性门禁（真源 = backend/internal/ota/ota.go 的 Status* 常量）。
 *
 * ── 为什么需要这个文件 ──
 * 同类的 OTAFormStatusCoverage.spec.ts 把"后端全集"**手抄**成 8 个字面量常量。
 * 抄本会漂移：后端新增一个状态（如 timeout）时，前端映射漏了也**不会**变红，
 * 而那正是本仓已发生过的真实缺陷（NodeOverview 只覆盖 5 态）。
 * 本门禁改为**直接解析后端源码**取真源：
 *   - 读不到 / 解析不出状态 ⇒ 抛错（防止路径写错变成空集合 ⇒ 全绿）；
 *   - 解析出的集合做下界断言（>= 5 且有 needs_retry）⇒ 防止正则失效；
 *   - NodeDetail / NodeOverview 的每个状态映射与真源**双向相等**（不得多、不得少）。
 *
 * 覆盖面：NodeDetail.vue 的 OTA_STATUS_TYPES / OTA_STATUS_TEXTS，
 *        NodeOverview.vue 的 otaStatusText.texts / otaTagClass.classes。
 *        （NodeDetail 的 OTA_STATUS_TYPES 是 el-tag 的 type 集，
 *          'info'/'warning'/'success'/'danger' 是 Element Plus 的类型词表，
 *          不是状态名，故按"键"而不是"值"校验。）
 */

// ── 真源 ──────────────────────────────────────────────────────────────
// vitest 的 cwd = frontend-shared（vitest.config.ts 所在目录），后端在上一级。
const BACKEND_OTA_PATH = resolve(process.cwd(), '..', 'backend', 'internal', 'ota', 'ota.go')
const backendOtaSource = readFileSync(BACKEND_OTA_PATH, 'utf8')

/**
 * 截取 `const (` … `)` 分组块。
 *
 * 必须配对截取而不是"从 const 到文件尾"：后者会把后面的 Wire* 常量、
 * 函数体一并吞进来，删掉真规则也能靠后面的代码蒙混过关（本仓已实际发生过）。
 */
function groupedConstBlockOf(source: string, keyword = 'const ('): string {
  const start = source.indexOf(keyword)
  if (start < 0) throw new Error('找不到常量分组: ' + keyword)
  const open = source.indexOf('(', start)
  let depth = 0
  for (let i = open; i < source.length; i++) {
    if (source[i] === '(') depth++
    else if (source[i] === ')') {
      depth--
      if (depth === 0) return source.slice(open, i + 1)
    }
  }
  throw new Error('常量分组未闭合: ' + keyword)
}

/** 后端 Status* 常量的字面量集合（真源）。 */
function backendStatusesOf(source: string): string[] {
  const block = groupedConstBlockOf(source)
  const matches = [...block.matchAll(/^\s*(Status[A-Za-z]\w*)\s*=\s*"([^"]*)"/gm)]
  return matches.map(m => m[2])
}

const BACKEND_OTA_STATUSES = backendStatusesOf(backendOtaSource)

/**
 * 从源码里截取指定常量的字面量块（配对截取，支持 `{}` 与 `[]`）。
 * 思路与 OTAFormStatusCoverage.spec.ts 的 ruleBlockOf 相同；此处复制一份，
 * 不去改那个文件（它是既有门禁，改它等于动别人验过的资产）。
 */
function ruleBlockOf(source: string, constName: string): string {
  const anchor = 'const ' + constName
  const start = source.indexOf(anchor)
  if (start < 0) throw new Error('找不到常量定义: ' + anchor)
  const candidates = [source.indexOf('{', start), source.indexOf('[', start)].filter(i => i >= 0)
  if (candidates.length === 0) throw new Error('找不到字面量块: ' + constName)
  const open = Math.min(...candidates)
  const opener = source[open]
  const closer = opener === '{' ? '}' : ']'
  let depth = 0
  for (let i = open; i < source.length; i++) {
    if (source[i] === opener) depth++
    else if (source[i] === closer) {
      depth--
      if (depth === 0) return source.slice(open, i + 1)
    }
  }
  throw new Error('常量块未闭合: ' + constName)
}

/** `const NAME = {...}` 形态的"状态 → 规则"映射。 */
interface StatusRuleMap {
  constName: string
  statuses: string[]
  rules: Map<string, string>
}

function statusRuleMapOf(source: string, constName: string): StatusRuleMap {
  const block = ruleBlockOf(source, constName)
  return statusRuleMapOfBlock(constName, block)
}

/** 去掉行注释与块注释（遵守引号状态，避免误删字符串里的 "//"）。 */
function stripComments(src: string): string {
  let out = ''
  let quote: string | null = null
  for (let i = 0; i < src.length; i++) {
    const ch = src[i]
    if (quote) {
      out += ch
      if (ch === '\\') { out += src[++i] ?? ''; continue }
      if (ch === quote) quote = null
      continue
    }
    if (ch === "'" || ch === '"' || ch === '`') { quote = ch; out += ch; continue }
    if (ch === '/' && src[i + 1] === '/') {
      while (i < src.length && src[i] !== '\n') i++
      out += '\n'
      continue
    }
    if (ch === '/' && src[i + 1] === '*') {
      i += 2
      while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++
      i++
      continue
    }
    out += ch
  }
  return out
}

/** 按顶层分隔符切分（跳过字符串内与括号内的分隔符）。 */
function splitTopLevel(inner: string, sep = ','): string[] {
  const parts: string[] = []
  let depth = 0
  let quote: string | null = null
  let cur = ''
  for (let i = 0; i < inner.length; i++) {
    const ch = inner[i]
    if (quote) {
      cur += ch
      if (ch === '\\') { cur += inner[++i] ?? ''; continue }
      if (ch === quote) quote = null
      continue
    }
    if (ch === "'" || ch === '"' || ch === '`') { quote = ch; cur += ch; continue }
    if (ch === '{' || ch === '[' || ch === '(') depth++
    else if (ch === '}' || ch === ']' || ch === ')') depth--
    if (ch === sep && depth === 0) { parts.push(cur); cur = ''; continue }
    cur += ch
  }
  parts.push(cur)
  return parts
}

/** 字符串字面量去引号（保留裸值/表达式原样）。 */
function unquoteLiteral(value: string): string {
  const m = value.match(/^'([^'\\]*)'$/) || value.match(/^"([^"\\]*)"$/)
  return m ? m[1] : value
}

function statusRuleMapOfBlock(constName: string, block: string): StatusRuleMap {
  // 先剥注释：映射项后面常带 `// 问题态：…` 说明，直接按逗号切会把注释混进键名。
  const inner = stripComments(block.slice(1, -1))
  const statuses: string[] = []
  const rules = new Map<string, string>()
  for (const raw of splitTopLevel(inner)) {
    const entry = raw.trim()
    if (!entry) continue
    // 键允许裸标识符或引号字符串；只按**首个**冒号切分（值里可能含 ':'）。
    const m = entry.match(/^['"]?([A-Za-z_$][\w$]*)['"]?\s*:\s*([\s\S]+)$/)
    if (!m) throw new Error(constName + ': 无法解析映射项 ' + JSON.stringify(entry.slice(0, 60)))
    statuses.push(m[1])
    rules.set(m[1], unquoteLiteral(m[2].trim()))
  }
  if (rules.size === 0) throw new Error(constName + ': 映射块解析出 0 项')
  return { constName, statuses, rules }
}

/** NodeOverview.vue 里"函数内定义局部映射"的形态：`const <name>: Record<string,string> = {...}`。 */
function localRuleMapOf(source: string, mapConstName: string, fnName: string): StatusRuleMap {
  const fnStart = source.indexOf('function ' + fnName)
  if (fnStart < 0) throw new Error('找不到函数: ' + fnName)
  const block = ruleBlockOf(source.slice(fnStart), mapConstName)
  return statusRuleMapOfBlock(mapConstName + '@' + fnName, block)
}

// ── 分类器：门禁用例与"分类器自检"用例共用同一实现 ──────────────────
const PROBLEM_STATUSES = ['failed', 'timeout', 'needs_retry'] as const
const GRAY_CLASS = 'bus-tag-gray'

/**
 * 审计一个"状态 → 规则"映射相对后端全集的完整性，返回违规描述列表（空 = 通过）。
 * 同时被下面的断言用例与"分类器自检"用例调用 —— 所以自检真的在证明它会报红。
 */
function auditStatusRuleMap(
  label: string,
  map: StatusRuleMap,
  backend: readonly string[],
  options: { problemStatuses?: readonly string[]; grayClass?: string } = {},
): string[] {
  const violations: string[] = []
  const keys = [...map.rules.keys()]
  const missing = backend.filter(s => !map.rules.has(s))
  const extra = keys.filter(s => !backend.includes(s))
  if (missing.length) violations.push(label + ' 缺少后端状态映射: ' + missing.join(', '))
  if (extra.length) violations.push(label + ' 含后端不存在的状态（死条目）: ' + extra.join(', '))
  if (map.statuses.length !== keys.length) {
    violations.push(label + ' 映射项数与解析出的键数不一致（同键重复）')
  }
  for (const s of options.problemStatuses ?? []) {
    if (map.rules.get(s) === options.grayClass) {
      violations.push(label + ' 问题态 ' + s + ' 落到了中性灰 ' + options.grayClass)
    }
  }
  return violations
}

/** 把映射块的原始文本还原成可比较的形态（用于 cancelled 残留检查）。 */
function ruleMapRawOf(source: string, constName: string): string {
  return ruleBlockOf(source, constName)
}
function localRuleMapRawOf(source: string, mapConstName: string, fnName: string): string {
  const fnStart = source.indexOf('function ' + fnName)
  if (fnStart < 0) throw new Error('找不到函数: ' + fnName)
  return ruleBlockOf(source.slice(fnStart), mapConstName)
}

// ── CSS 解析（类名是否真的有样式、亮暗两套是否都可达） ────────────────
function stripCssComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '')
}

function styleChunksOf(src: string): string {
  const chunks: string[] = []
  const re = /<style[^>]*>([\s\S]*?)<\/style>/g
  let m: RegExpExecArray | null
  while ((m = re.exec(src))) chunks.push(m[1])
  if (chunks.length === 0) throw new Error('找不到 <style> 块')
  return chunks.join('\n')
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** 取某选择器的规则体（含注释已剥离的样式源）。 */
function cssRuleBodyOf(styleSource: string, selector: string): string {
  const re = new RegExp('(?:^|[},])\\s*' + escapeRegExp(selector) + '\\s*\\{([^}]*)\\}', 'm')
  const m = styleSource.match(re)
  if (!m) throw new Error('找不到 CSS 规则: ' + selector)
  return m[1]
}

/** 取某个作用域块（`.node-overview-page` / `html.dark .node-overview-page`）内的自定义属性声明。 */
function declarationsInScope(styleSource: string, selector: string): Map<string, string> {
  const body = cssRuleBodyOf(styleSource, selector)
  const out = new Map<string, string>()
  const re = /(--[\w-]+)\s*:\s*([^;]+);/g
  let m: RegExpExecArray | null
  while ((m = re.exec(body))) out.set(m[1], m[2].trim())
  return out
}

/**
 * 解析值里所有 var(--x) 引用在该作用域内是否可达。
 * 可达 = 该作用域自身定义了 --x（继续递归），或 var() 自带兜底值，或引用了主题全局 token。
 * 返回不可达的 token 列表。
 */
function unreachableTokens(
  value: string,
  scope: Map<string, string>,
  globals: ReadonlySet<string>,
  seen = new Set<string>(),
): string[] {
  const missing: string[] = []
  const re = /var\(\s*(--[\w-]+)\s*(,)?/g
  let m: RegExpExecArray | null
  while ((m = re.exec(value))) {
    const token = m[1]
    const hasFallback = m[2] === ','
    if (scope.has(token)) {
      if (seen.has(token)) continue
      seen.add(token)
      missing.push(...unreachableTokens(scope.get(token)!, scope, globals, seen))
      continue
    }
    if (globals.has(token)) continue
    if (hasFallback) continue
    missing.push(token)
  }
  return missing
}

// 主题全局 token：由 styles/theme.css 等全局样式提供，不在页面块内定义也合法。
const GLOBAL_THEME_TOKENS = new Set([
  '--color-danger', '--color-success', '--color-warning', '--color-primary',
  '--text-color-primary', '--text-color-regular', '--text-color-secondary',
  '--border-color', '--border-color-lighter', '--bg-color-page', '--card-bg',
])

describe('门禁自身有效（防止"扫描器坏掉后永远绿"）', () => {
  it('后端真源解析出预期的状态集合（路径错 / 正则失效会在这里先炸）', () => {
    // 下界断言：解析出空集合会让后面所有"双向相等"断言变成空对空 ⇒ 假绿
    expect(BACKEND_OTA_STATUSES.length, '后端状态解析出 0 个 —— 路径或正则失效').toBeGreaterThanOrEqual(5)
    expect(BACKEND_OTA_STATUSES).toContain('needs_retry')
    expect(new Set(BACKEND_OTA_STATUSES).size, '后端状态有重复').toBe(BACKEND_OTA_STATUSES.length)
  })

  it('规则块是配对截取，没有吞到后续代码', () => {
    const types = ruleMapRawOf(nodeDetailSource, 'OTA_STATUS_TYPES')
    expect(types.startsWith('{')).toBe(true)
    // 若退化成"截到文件尾"，块内会混进紧随其后的 OTA_STATUS_TEXTS
    expect(types, '截取越界：把 OTA_STATUS_TEXTS 吞进来了').not.toContain('OTA_STATUS_TEXTS')

    const texts = ruleMapRawOf(nodeDetailSource, 'OTA_STATUS_TEXTS')
    expect(texts.startsWith('{')).toBe(true)
    expect(texts, '截取越界：把 getOTAStatusType 吞进来了').not.toContain('getOTAStatusType')

    const overview = localRuleMapRawOf(nodeOverviewSource, 'texts', 'otaStatusText')
    expect(overview.startsWith('{')).toBe(true)
    expect(overview, '截取越界：把 otaTagClass 吞进来了').not.toContain('otaTagClass')
  })

  it('扫描面非空：每个被检查的映射都真的解析出了条目', () => {
    const scans = [
      statusRuleMapOf(nodeDetailSource, 'OTA_STATUS_TYPES'),
      statusRuleMapOf(nodeDetailSource, 'OTA_STATUS_TEXTS'),
      localRuleMapOf(nodeOverviewSource, 'texts', 'otaStatusText'),
      localRuleMapOf(nodeOverviewSource, 'classes', 'otaTagClass'),
    ]
    for (const s of scans) {
      expect(s.rules.size, s.constName + ' 解析出 0 项 ⇒ 门禁形同虚设').toBeGreaterThan(0)
    }
  })

  it('分类器自检：喂已知违规样本必须报红，喂合规样本必须报绿', () => {
    const backend = ['pending', 'downloading', 'verifying', 'installing', 'success', 'failed', 'timeout', 'needs_retry']

    // 合规样本
    const good = statusRuleMapOfBlock('good', '{ ' + backend.map(s => s + ": 'x'").join(', ') + ' }')
    expect(auditStatusRuleMap('good', good, backend)).toEqual([])

    // 违规样本 1：漏掉 verifying（正是本仓真实发生过的缺陷形态）
    const missing = statusRuleMapOfBlock('missing', "{ pending: 'x', downloading: 'x' }")
    expect(auditStatusRuleMap('missing', missing, backend).length).toBeGreaterThan(0)

    // 违规样本 2：多出死条目 cancelled（后端从未定义）
    const dead = statusRuleMapOfBlock('dead', '{ ' + backend.map(s => s + ": 'x'").join(', ') + ", cancelled: '已取消' }")
    expect(auditStatusRuleMap('dead', dead, backend).length).toBeGreaterThan(0)

    // 违规样本 3：问题态落到中性灰
    const grayProblem = statusRuleMapOfBlock('grayProblem', "{ timeout: 'bus-tag-gray' }")
    const v = auditStatusRuleMap('grayProblem', grayProblem, backend, { problemStatuses: PROBLEM_STATUSES, grayClass: GRAY_CLASS })
    expect(v.some(x => x.includes('中性灰'))).toBe(true)
  })
})

describe('NodeDetail.vue：OTA 状态映射 ≡ 后端真源（双向相等）', () => {
  const types = statusRuleMapOf(nodeDetailSource, 'OTA_STATUS_TYPES')
  const texts = statusRuleMapOf(nodeDetailSource, 'OTA_STATUS_TEXTS')

  it('OTA_STATUS_TYPES 覆盖且仅覆盖后端全集', () => {
    expect(auditStatusRuleMap('OTA_STATUS_TYPES', types, BACKEND_OTA_STATUSES, {
      problemStatuses: PROBLEM_STATUSES,
      grayClass: 'info',
    })).toEqual([])
  })

  it('OTA_STATUS_TEXTS 覆盖且仅覆盖后端全集', () => {
    expect(auditStatusRuleMap('OTA_STATUS_TEXTS', texts, BACKEND_OTA_STATUSES)).toEqual([])
  })

  it('两表键序一致（渲染时不会出现"有文案没颜色"的错配）', () => {
    expect(types.statuses).toEqual(texts.statuses)
  })

  it('8 态文案都是中文，且不等于状态名本身', () => {
    for (const status of BACKEND_OTA_STATUSES) {
      const text = texts.rules.get(status)!.replace(/^['"]|['"]$/g, '')
      expect(/[\u4e00-\u9fa5]/.test(text), status + ' 文案应为中文: ' + text).toBe(true)
      expect(text).not.toBe(status)
    }
  })
})

describe('NodeOverview.vue：OTA 状态映射 ≡ 后端真源（双向相等）', () => {
  const texts = localRuleMapOf(nodeOverviewSource, 'texts', 'otaStatusText')
  const classes = localRuleMapOf(nodeOverviewSource, 'classes', 'otaTagClass')

  it('otaStatusText 覆盖且仅覆盖后端全集', () => {
    expect(auditStatusRuleMap('otaStatusText.texts', texts, BACKEND_OTA_STATUSES)).toEqual([])
  })

  it('otaTagClass 覆盖且仅覆盖后端全集', () => {
    expect(auditStatusRuleMap('otaTagClass.classes', classes, BACKEND_OTA_STATUSES, {
      problemStatuses: PROBLEM_STATUSES,
      grayClass: GRAY_CLASS,
    })).toEqual([])
  })

  it('两表键序一致，且与 NodeDetail 的中文文案逐态相同', () => {
    const detailTexts = statusRuleMapOf(nodeDetailSource, 'OTA_STATUS_TEXTS')
    expect(texts.statuses).toEqual(classes.statuses)
    expect(texts.statuses).toEqual(detailTexts.statuses)
    for (const status of BACKEND_OTA_STATUSES) {
      expect(texts.rules.get(status), status + ' 文案与 NodeDetail 不一致').toBe(detailTexts.rules.get(status))
    }
  })

  it('问题态颜色语义正确：failed/timeout 红、needs_retry 橙、成功绿、进行中蓝', () => {
    expect(classes.rules.get('failed')).toBe('bus-tag-red')
    expect(classes.rules.get('timeout')).toBe('bus-tag-red')
    expect(classes.rules.get('needs_retry')).toBe('bus-tag-orange')
    expect(classes.rules.get('success')).toBe('bus-tag-green')
    for (const s of ['pending', 'downloading', 'verifying', 'installing']) {
      expect(classes.rules.get(s), s + ' 应为进行中蓝色').toBe('bus-tag-blue')
    }
    // 未映射状态仍回退中性灰 —— 回退本身合法，但上面的双向相等保证不会有未映射的真实状态
    expect(nodeOverviewSource).toContain("return classes[status] || 'bus-tag-gray'")
  })
})

describe("'cancelled' 死条目清零（后端从未定义该状态）", () => {
  const targets: Array<[string, string]> = [
    ['NodeDetail.OTA_STATUS_TYPES', ruleMapRawOf(nodeDetailSource, 'OTA_STATUS_TYPES')],
    ['NodeDetail.OTA_STATUS_TEXTS', ruleMapRawOf(nodeDetailSource, 'OTA_STATUS_TEXTS')],
    ['NodeOverview.otaStatusText', localRuleMapRawOf(nodeOverviewSource, 'texts', 'otaStatusText')],
    ['NodeOverview.otaTagClass', localRuleMapRawOf(nodeOverviewSource, 'classes', 'otaTagClass')],
  ]

  it('后端 ota.go 里确实没有 cancelled 状态字面量（真源核对）', () => {
    expect(/^\s*Status[A-Za-z]\w*\s*=\s*"cancelled"/m.test(backendOtaSource)).toBe(false)
    expect(BACKEND_OTA_STATUSES).not.toContain('cancelled')
  })

  it.each(targets)('%s 里不得再出现 cancelled 状态键', (_label, block) => {
    // 键形态（含引号键），且允许冒号周围有空白；不匹配说明性文字里的单词
    expect(/['"]?cancelled['"]?\s*:/.test(block)).toBe(false)
  })
})

describe('NodeOverview.vue：问题态标签样式真实存在且亮暗两套都可达', () => {
  const styleSource = stripCssComments(styleChunksOf(nodeOverviewSource))

  it('.bus-tag-red / .bus-tag-orange 都有样式定义（写了类名没样式 = 视觉回退成无色）', () => {
    expect(cssRuleBodyOf(styleSource, '.bus-tag-red').trim().length).toBeGreaterThan(0)
    expect(cssRuleBodyOf(styleSource, '.bus-tag-orange').trim().length).toBeGreaterThan(0)
  })

  it('红/橙 rule 引用的 token 在亮暗两套作用域内都可达', () => {
    const light = declarationsInScope(styleSource, '.node-overview-page')
    const dark = declarationsInScope(styleSource, 'html.dark .node-overview-page')
    // 扫描面下界：页面级 token 必须真的解析出来了
    expect(light.size, '亮色 token 解析为 0 —— 作用域块没截对').toBeGreaterThan(10)
    expect(dark.size, '暗色 token 解析为 0 —— 作用域块没截对').toBeGreaterThan(10)

    for (const selector of ['.bus-tag-red', '.bus-tag-orange']) {
      const value = cssRuleBodyOf(styleSource, selector)
      expect(unreachableTokens(value, light, GLOBAL_THEME_TOKENS), selector + ' 亮色下引用不可达 token').toEqual([])
      expect(unreachableTokens(value, dark, GLOBAL_THEME_TOKENS), selector + ' 暗色下引用不可达 token').toEqual([])
    }
  })

  it('分类器自检：不可达 token 与未定义类名都会被这个解析器抓住', () => {
    const light = declarationsInScope(styleSource, '.node-overview-page')
    // 合规样本
    expect(unreachableTokens('.bus-tag-red { color: var(--no-danger); }', light, GLOBAL_THEME_TOKENS)).toEqual([])
    // 违规样本：token 未定义且无兜底
    expect(unreachableTokens('color: var(--no-such-token);', light, GLOBAL_THEME_TOKENS)).toEqual(['--no-such-token'])
    // 违规样本：类名根本没定义
    expect(() => cssRuleBodyOf(styleSource, '.bus-tag-not-defined')).toThrow()
  })
})
