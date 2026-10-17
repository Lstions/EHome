import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { stripComments, walkSourceFiles } from './sourceScan'

/**
 * 术语表禁称门禁（回归锁）—— docs/设计/术语表.md §4「禁止的命名（反模式）」的前端执法。
 *
 * 背景（实测，2026-09-15）：审计 F18 只记了一条「术语残留『设备模板』」。实测复核后
 * 发现**分母比清单大得多**，本文件把这个分母变成可复跑的判据：
 *   ① 用户可见文案里的禁称 —— 术语表 §2.3 / §4 明确禁止，实测仍在生产构建产物里；
 *   ② 注释/内部标识里的同一词 —— **不算违规**（术语表禁的是「新增中文 UI」的旧称，
 *      不是禁止讨论历史命名）。因此本门禁必须**遮蔽注释**，否则会误报一片。
 *
 * 为什么必须全站扫描而不是补渲染测试：新页面抄一句旧文案不会有任何报错，
 * 而术语漂移是**累积性**的（旧的改完，新的又抄回来）—— 只有扫描能止住。
 *
 * 判据（口径固定，fail-closed）：在**去注释**后的源码里出现禁称关键词即违规。
 * 关键词表必须与 docs/设计/术语表.md §4 表格逐字一致。
 *
 * 与文档的分工：本门禁只覆盖 frontend-shared/src 的源码与用户可见文案；
 * 后端与文档中的旧称由 tools/docs/claims-audit.sh 与人工评审覆盖。
 */

const SRC_DIR = join(process.cwd(), 'src')

/** 术语表 §4 的禁称 → 正确替代（键必须与 docs/设计/术语表.md §4 表格逐字一致） */
export const BANNED_TERMS: Record<string, string> = {
  '设备模板': '设备配置',
  '采集器': '节点',
  '设备类型': '设备型号',
  'GPIO 通道': 'GPIO 外设直控',
  'PWM 通道': 'PWM 外设直控',
  'GPIO通道': 'GPIO 外设直控',
  'PWM通道': 'PWM 外设直控',
}

export const REGISTERED_EXCEPTIONS: { term: string; count: number; reason: string }[] = [
  {
    term: '设备类型',
    count: 13,
    reason: '需产品措辞裁定；改动会触及 2 条逐字断言文案的测试，见 docs/分析/后续工作计划与方案-2026-09-15.md',
  },
]

// 已登记的例外必须与实测精确匹配，多了要裁决、少了要销账。
// 为什么用精确数量而不是白名单文件：数量是最难被悄悄稀释的口径。
// 设备类型 的理由：术语表 §4 裁定为设备型号，但实测仍有 13 处用户可见文案，
// 且已有 2 条测试逐字断言了其中的字符串，全量替换属产品措辞决策。


export interface TermHit {
  /** 1-based 行号 */
  line: number
  term: string
  /** 去注释后的整行（trim） */
  text: string
}

/**
 * 抽取一处源码（已去注释）里的全部禁称命中。
 * 导出以便**分类器自检**（同一函数喂正例/反例），这是本仓既有范式。
 */
export function bannedTermHitsIn(source: string): TermHit[] {
  const lines = stripComments(source).split('\n')
  const hits: TermHit[] = []
  lines.forEach((lineText, idx) => {
    for (const term of Object.keys(BANNED_TERMS)) {
      if (lineText.includes(term)) hits.push({ line: idx + 1, term, text: lineText.trim() })
    }
  })
  return hits
}

describe('术语表禁称门禁', () => {
  const files = walkSourceFiles(SRC_DIR)

  function scanAll() {
    const byTerm: Record<string, string[]> = {}
    for (const f of files) {
      const rel = relative(process.cwd(), f)
      for (const hit of bannedTermHitsIn(readFileSync(f, 'utf-8'))) {
        const line = rel + ':' + hit.line + '  「' + hit.term + '」应为「' + BANNED_TERMS[hit.term] + '」  ' + hit.text
        const bucket = byTerm[hit.term] || (byTerm[hit.term] = [])
        bucket.push(line)
      }
    }
    return byTerm
  }

  it('用户可见源码里不得出现术语表 §4 的禁称（已登记例外除外）', () => {
    const byTerm = scanAll()
    const registered = new Set(REGISTERED_EXCEPTIONS.map((e) => e.term))
    const violations = Object.entries(byTerm)
      .filter(([term]) => !registered.has(term))
      .flatMap(([, lines]) => lines)
    expect(violations).toEqual([])
  })

  it('登记簿 fail-closed：例外数量必须精确匹配（多了要裁决，少了要销账）', () => {
    const byTerm = scanAll()
    const mismatches: string[] = []
    for (const entry of REGISTERED_EXCEPTIONS) {
      const actual = (byTerm[entry.term] || []).length
      if (actual !== entry.count) {
        mismatches.push('「' + entry.term + '」登记 ' + entry.count + ' 处，实测 ' + actual + ' 处'
          + (actual < entry.count ? '（已修复 => 请从 REGISTERED_EXCEPTIONS 销账）' : '（新增 => 请显式裁决后再登记）'))
      }
    }
    expect(mismatches).toEqual([])
  })

  it('分母守卫：扫描器必须真的扫到源码（防止「0 违规」来自扫描器空转）', () => {
    expect(files.length).toBeGreaterThan(100)
    // 反向守卫：拿一个必然命中的合成样本喂给同一函数，证明判据真的在工作
    expect(bannedTermHitsIn('<span>无需预先创建设备模板</span>').length).toBe(1)
  })

  it('分类器自检：正例必报（文案/模板/模板串）；反例必不报（注释与规范用词）', () => {
    const bt = String.fromCharCode(96)
    const positive = [
      '<span>无需预先创建设备模板</span>',
      "const tip = '请先创建设备模板'",
      'const msg = ' + bt + '选择设备模板' + bt,
      '<el-option label="设备类型" />',
    ].join('\n')
    expect(bannedTermHitsIn(positive).map((h) => h.line)).toEqual([1, 2, 3, 4])

    const negative = [
      '// 旧名「设备模板」见术语表 §4',
      '<!-- 此处曾用设备模板，已更名 -->',
      '/* 采集器是 v2.1 的旧称 */',
      "const label = '设备配置'",
      "const t = '设备型号'",
    ].join('\n')
    expect(bannedTermHitsIn(negative)).toEqual([])
  })
})
