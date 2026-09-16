import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { stripComments, walkSourceFiles } from '@/utils/__tests__/sourceScan'

/**
 * F14 统计卡范围词门禁（规范 §4.3 MUST）—— 本仓第 6 条全站源码扫描门禁。
 *
 * 缺陷（2026-09-15 实测）：同一行统计卡里**混排两种口径且不标范围**。
 * 最典型的是 DataSourceList：`总来源 = store.total` 来自后端 `q.Count(&total)`（筛选后**全量**），
 * 而「权威/待命/熔断」来自 `items.value`（**当前页** ≤20 条）。
 * 不标范围时用户看到「总来源 25」而三个状态加起来最多 20，会以为数据丢了。
 * DeviceConfigList 同形（total 全量 / 其余本页）；EdgeDeviceList 的「今日数据」
 * 读服务端全表计数，也与当前页无关。
 *
 * ── 判据（fail-closed）────────────────────────────────────────────────────
 * 每个 `<StatCard>` 的 label 必须是下列之一：
 *   ① 静态字面量里含范围词（「本页」/「当前筛选」/「全局」/「进程启动以来」，见 SCOPE_WORDS）；
 *   ② 动态 `:label=` 且模板串里引用了已声明的 SCOPE_* 常量（常量值本身含范围词）。
 * 认不出的写法一律报出来让人裁决 —— 而不是静默放过。
 *
 * ── 本门禁**查不了什么**（能力边界，必须与判据一起读）──────────────────────
 * 1. **查不了语义**：门禁只证明「label 上写了范围词」，**不能证明这个词是对的**。
 *    把本页数字标成「全局」同样能过。正确性靠逐卡人工核对分母出处（本文件头逐页记录了
 *    total 与其余计数的来源），以及各页的 mount 测试。
 * 2. **查不了非 StatCard 的手写统计卡**：本门禁分母 = 使用 `StatCard.vue` 的位置。
 *    手写 `.stat-card` 的页面（Dashboard / Monitor / DataPanel）不在分母内 ——
 *    它们的范围词由各自文件的渲染测试覆盖（DataPanel 对 `.stat-scope` 有真实 DOM 断言）。
 * 3. **查不了范围词是否渲染出来**：`:label` 若在运行时算成空串，源码层看不出来。
 *
 * ── 变红条件 ──────────────────────────────────────────────────────────────
 *   · 新增 StatCard 时漏写范围词；
 *   · 把已有的范围词删掉；
 *   · SCOPE_* 常量被改名（判据认不出 ⇒ 报「未声明/未引用」）或被改成非范围词。
 */

const SRC_DIR = join(process.cwd(), 'src')

/** 已登记的范围词。新增口径必须先在这里登记，否则「新词」会被判成漏标。 */
export const SCOPE_WORDS = ['本页', '当前筛选', '全局', '进程启动以来']

export interface StatCardSite {
  /** 1-based 行号 */
  line: number
  /** 开标签原文 */
  tag: string
  /** label 的取值形态 */
  kind: 'static' | 'dynamic' | 'missing'
  /** 静态 label 的字面量（dynamic 时为空） */
  literal: string
  /** dynamic 时引用到的 SCOPE_* 常量名 */
  refs: string[]
  /**
   * mobile-label 的取值形态与内容。≤768px 时 StatCard 把桌面 label 设为 display:none，
   * **只显示 mobile-label** —— 所以它也必须带范围词，否则 F14 在移动端静默失效
   * （这正是本门禁第一版漏掉的形态：只查 label= 会让移动端缺陷全绿通过）。
   */
  mobile: { kind: 'none' | 'static' | 'dynamic'; literal: string; refs: string[] }
}

/**
 * 抽取一份源码里全部 `<StatCard>` 开始标签并分类。
 * 导出以便**分类器自检**（同一函数喂正例/反例），这是本仓既有范式。
 */
export function statCardSitesIn(source: string): StatCardSite[] {
  const stripped = stripComments(source)
  const sites: StatCardSite[] = []
  const re = /<StatCard\b/g
  let m: RegExpExecArray | null
  while ((m = re.exec(stripped)) !== null) {
    // 开始标签可能跨多行；按引号配对推进，避免把属性值里的 > 当标签结束
    let i = m.index + m[0].length
    let quote: string | null = null
    let end = -1
    for (; i < stripped.length; i++) {
      const ch = stripped[i]
      if (quote) { if (ch === quote) quote = null; continue }
      if (ch === '"' || ch === "'") { quote = ch; continue }
      if (ch === '>') { end = i; break }
    }
    if (end < 0) break
    const tag = stripped.slice(m.index, end + 1)
    const line = stripped.slice(0, m.index).split('\n').length
    const dynamic = tag.match(/:label\s*=\s*"`([^`]*)`"/)
    const staticM = tag.match(/(?<!:)\blabel\s*=\s*"([^"]*)"/)
    const dynMobile = tag.match(/:mobile-label\s*=\s*"`([^`]*)`"/)
    const statMobile = tag.match(/(?<!:)\bmobile-label\s*=\s*"([^"]*)"/)
    const mobile: StatCardSite['mobile'] = dynMobile
      ? {
          kind: 'dynamic',
          literal: '',
          refs: [...dynMobile[1].matchAll(/\b(SCOPE_[A-Z_]+)\b/g)].map((x) => x[1]),
        }
      : statMobile
        ? { kind: 'static', literal: statMobile[1], refs: [] }
        : { kind: 'none', literal: '', refs: [] }
    if (dynamic) {
      const refs = [...dynamic[1].matchAll(/\b(SCOPE_[A-Z_]+)\b/g)].map((x) => x[1])
      sites.push({ line, tag, kind: 'dynamic', literal: '', refs, mobile })
    } else if (staticM) {
      sites.push({ line, tag, kind: 'static', literal: staticM[1], refs: [], mobile })
    } else {
      sites.push({ line, tag, kind: 'missing', literal: '', refs: [], mobile })
    }
    re.lastIndex = end + 1
  }
  return sites
}

/**
 * 判定一处 StatCard 是否带了范围词。
 * `scopeConsts` 是「常量名 → 值」表，由 scopeConstsIn 从同一份源码里解析。
 */
export function classifyStatCard(
  site: StatCardSite,
  scopeConsts: Map<string, string>,
): { ok: boolean; reason?: string } {
  if (site.kind === 'missing') return { ok: false, reason: '没有 label 属性' }
  if (site.kind === 'static') {
    const hit = SCOPE_WORDS.some((w) => site.literal.includes(w))
    return hit ? { ok: true } : { ok: false, reason: '静态 label「' + site.literal + '」不含任何已登记范围词' }
  }
  if (site.refs.length === 0) {
    return { ok: false, reason: '动态 label 未引用任何 SCOPE_* 常量' }
  }
  for (const ref of site.refs) {
    const value = scopeConsts.get(ref)
    if (value === undefined) return { ok: false, reason: '引用了未声明的常量 ' + ref }
    if (!SCOPE_WORDS.some((w) => value.includes(w))) {
      return { ok: false, reason: '常量 ' + ref + ' = 「' + value + '」不含范围词' }
    }
  }
  return { ok: true }
}

/**
 * 判定 mobile-label 是否也带了范围词。
 *
 * 为什么必须单列一条：StatCard 在 ≤768px 时把桌面 label 设为 `display:none`，
 * **只显示 mobile-label**。本门禁第一版只查 `label=`，于是「桌面有范围词、移动端没有」
 * 这种形态全绿通过 —— 主控用真实浏览器探针在 390/768 两档实测才发现
 * （移动端显示「边缘设备/在线/离线-异常」，范围词完全消失）。
 * 未提供 mobile-label 的卡片不算违规：那种情况下移动端显示的就是桌面 label（自带范围词）。
 */
export function classifyMobileLabel(
  site: StatCardSite,
  scopeConsts: Map<string, string>,
): { ok: boolean; reason?: string } {
  const m = site.mobile
  if (m.kind === 'none') return { ok: true }
  if (m.kind === 'static') {
    const hit = SCOPE_WORDS.some((w) => m.literal.includes(w))
    return hit
      ? { ok: true }
      : { ok: false, reason: 'mobile-label「' + m.literal + '」不含范围词（≤768px 时桌面 label 被隐藏，移动端将看不到范围）' }
  }
  if (m.refs.length === 0) {
    return { ok: false, reason: '动态 mobile-label 未引用任何 SCOPE_* 常量' }
  }
  for (const ref of m.refs) {
    const value = scopeConsts.get(ref)
    if (value === undefined) return { ok: false, reason: 'mobile-label 引用了未声明的常量 ' + ref }
    if (!SCOPE_WORDS.some((w) => value.includes(w))) {
      return { ok: false, reason: 'mobile-label 的常量 ' + ref + ' = 「' + value + '」不含范围词' }
    }
  }
  return { ok: true }
}

/** 从源码里解析 `const SCOPE_X = '...'` 常量表（只认单/双引号字面量）。 */
export function scopeConstsIn(source: string): Map<string, string> {
  const out = new Map<string, string>()
  const stripped = stripComments(source)
  const re = /const\s+(SCOPE_[A-Z_]+)\s*=\s*(['"])([^'"]*)\2/g
  let m: RegExpExecArray | null
  while ((m = re.exec(stripped)) !== null) out.set(m[1], m[3])
  return out
}

const SCAN_FILES = walkSourceFiles(SRC_DIR)

describe('F14 统计卡范围词门禁', () => {
  it('全站每个 StatCard 的 label 都带范围词（静态字面量或 SCOPE_* 常量）', () => {
    const offenders: string[] = []
    let scanned = 0
    for (const file of SCAN_FILES) {
      const src = readFileSync(file, 'utf-8')
      const consts = scopeConstsIn(src)
      for (const site of statCardSitesIn(src)) {
        scanned++
        const verdict = classifyStatCard(site, consts)
        if (!verdict.ok) {
          offenders.push(
            relative(process.cwd(), file) + ':' + site.line + ' ' + verdict.reason +
              ' :: ' + site.tag.replace(/\s+/g, ' '),
          )
        }
        const mv = classifyMobileLabel(site, consts)
        if (!mv.ok) {
          offenders.push(
            relative(process.cwd(), file) + ':' + site.line + ' ' + mv.reason +
              ' :: ' + site.tag.replace(/\s+/g, ' '),
          )
        }
      }
    }
    expect(scanned, '扫到的 StatCard 为 0 —— 扫描器/判据失效，门禁形同虚设').toBeGreaterThan(10)
    expect(offenders, '统计卡缺范围词（规范 §4.3 MUST）：\n' + offenders.join('\n')).toEqual([])
  })

  it('分母守卫：扫描器真的走到了全站源码，且判据不是恒真', () => {
    expect(SCAN_FILES.length, '扫描到的源码文件数为 0 —— 扫描路径错了').toBeGreaterThan(100)
    const withCards = SCAN_FILES.filter((f) => statCardSitesIn(readFileSync(f, 'utf-8')).length > 0)
    expect(withCards.length, '没有任何文件命中 StatCard —— 判据被改坏').toBeGreaterThanOrEqual(3)
  })

  it('分类器自检：正例必过、反例必报', () => {
    const consts = new Map([
      ['SCOPE_PAGE', '本页'],
      ['SCOPE_FILTERED', '当前筛选'],
      ['SCOPE_GLOBAL', '全局'],
      ['SCOPE_BOGUS', '随便'],
    ])
    const positive = [
      '<StatCard label="本页节点" />',
      '<StatCard label="当前筛选总数" />',
      '<StatCard :label="`总来源（${SCOPE_FILTERED}）`" />',
      '<StatCard :label="`${SCOPE_PAGE}启用`" />',
    ].join('\n')
    const posSites = statCardSitesIn(positive)
    expect(posSites).toHaveLength(4)
    for (const site of posSites) expect(classifyStatCard(site, consts).ok, site.tag).toBe(true)

    const negative = [
      '<StatCard label="模板总数" />',
      '<StatCard label="总线类型" />',
      '<StatCard :label="`总线类型（${SCOPE_BOGUS}）`" />',
      '<StatCard :label="`总数 ${someOtherConst}`" />',
      '<StatCard icon-color="red" />',
    ].join('\n')
    const negSites = statCardSitesIn(negative)
    expect(negSites).toHaveLength(5)
    for (const site of negSites) expect(classifyStatCard(site, consts).ok, site.tag).toBe(false)
  })

  it('mobile-label 分类器自检：无 mobile-label 放过；有则必须带范围词', () => {
    const consts = new Map([
      ['SCOPE_PAGE', '本页'],
      ['SCOPE_GLOBAL', '全局'],
      ['SCOPE_BOGUS', '随便'],
    ])
    // 正例：不提供 mobile-label（移动端显示桌面 label，自带范围词）
    const noMobile = statCardSitesIn('<StatCard label="本页节点" />')
    expect(noMobile).toHaveLength(1)
    expect(noMobile[0].mobile.kind).toBe('none')
    expect(classifyMobileLabel(noMobile[0], consts).ok).toBe(true)

    // 正例：静态与动态 mobile-label 都带范围词
    const ok = statCardSitesIn([
      '<StatCard label="本页边缘设备" mobile-label="边缘设备（本页）" />',
      '<StatCard label="本页在线" :mobile-label="`在线（${SCOPE_PAGE}）`" />',
    ].join('\n'))
    expect(ok).toHaveLength(2)
    for (const s of ok) expect(classifyMobileLabel(s, consts).ok, s.tag).toBe(true)

    // 反例：这正是真实发生过的缺陷 —— 桌面有范围词、移动端丢掉
    const bad = statCardSitesIn([
      '<StatCard label="本页边缘设备" mobile-label="边缘设备" />',
      '<StatCard label="本页在线" :mobile-label="`在线（${SCOPE_BOGUS}）`" />',
      '<StatCard label="本页离线" :mobile-label="`离线 ${someOther}`" />',
    ].join('\n'))
    expect(bad).toHaveLength(3)
    for (const s of bad) {
      expect(classifyMobileLabel(s, consts).ok, '应报违规: ' + s.tag).toBe(false)
      // 桌面 label 本身是合规的 ⇒ 必须是 mobile 分类器抓到它（证明确实是新增的判据在起作用）
      expect(classifyStatCard(s, consts).ok, '桌面 label 应合规: ' + s.tag).toBe(true)
    }
  })

  it('常量解析器自检：能读出 SCOPE_* 值，且不把注释里的当常量', () => {
    const src = [
      "// const SCOPE_FAKE = '假的'",
      "const SCOPE_PAGE = '本页'",
      'const SCOPE_FILTERED = "当前筛选"',
    ].join('\n')
    const consts = scopeConstsIn(src)
    expect(consts.get('SCOPE_PAGE')).toBe('本页')
    expect(consts.get('SCOPE_FILTERED')).toBe('当前筛选')
    expect(consts.has('SCOPE_FAKE'), '注释里的常量声明不得被解析').toBe(false)
  })
})
