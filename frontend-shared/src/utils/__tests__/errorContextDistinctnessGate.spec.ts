import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { stripComments, walkSourceFiles } from '@/utils/__tests__/sourceScan'

/**
 * 错误提示可区分性门禁（回归锁）—— 「同一文件里两个不同操作不得报同一句话」。
 *
 * 背景（2026-09-15 实测）：`LogPanel.vue` 有**三处不同操作** —— 日志流开关、日志级别、
 * 持久化开关 —— 但三处失败都调 `handleError(e, '操作失败')`。而 `handleError` 的第二参数
 * 是**兜底而非前缀**（`extractErrorMessage` 的优先级是 `response.data.message || ... || fallback`），
 * 后端又**总是**带 message（`api/envelope.go` 的 `Error()` 必写 Message）⇒
 * 用户实际看到的只有服务端那句话（例如 `invalid level`），
 * **既不知道是哪个控件失败，也不知道失败的是哪件事**。
 * 对比同一文件的三条成功路径 `日志流已开启 / 日志级别已更新 / 持久化已开启` ——
 * 作者显然知道这是三件不同的事，只有失败路径被抹平了。
 *
 * ── 判据（机械可验，fail-closed）────────────────────────────────────────
 * 对每个文件收集所有 `handleError(e, 'X')` / `handleErrorWithContext(e, 'X')` 的第二参数字面量，
 * 断言**同一字面量不得在同一文件出现 >=2 次**。
 *
 * 为什么是「同一文件内」而不是全局：
 *   · 同一个词在不同页面出现是**正常**的（两页各有一个「删除失败」，用户不会混淆）；
 *   · 真会误导用户的是**同一屏/同一组件内**两个操作报同一句话 —— 那是同一个上下文里的歧义。
 *
 * 为什么用「完全相同的字面量」而不是 NLP 判断「文案是否有信息量」：
 *   · 前者机械可验、零误报；后者会变成主观判断，且必然腐烂。
 *   · 因此本门禁**只**拦「一模一样」这一种最明确的形态；
 *     「保存失败 vs 更新失败」这类弱区分**不在本门禁能力内**（边界见下）。
 *
 * ── 本门禁查不了什么（能力边界，必须与判据一起读）────────────────────────
 * 1. **查不了文案是否真的有信息量**：把三处改成「失败 A」「失败 B」「失败 C」即可通过，
 *    但那仍然是坏文案。本门禁只保证「可区分」，不保证「有意义」。
 * 2. **查不了动态拼接**：`handleError(e, cond ? 'a' : 'b')` 与模板串不在分母内。
 * 3. **查不了非 handleError 渠道**：直接 `ElMessage.error('操作失败')` 的写法不被覆盖。
 * 4. **查不了跨文件歧义**：同一对话框由多个子组件拼成时，跨文件重复看不见。
 *
 * ── 变红条件 ──────────────────────────────────────────────────────────────
 *   · 同一文件里新增/保留两处相同文案的错误提示。
 */

const SRC_DIR = join(process.cwd(), 'src')

/** 匹配 handleError(expr, '字面量') 与 handleErrorWithContext(expr, '字面量') 的文案参数。 */
const CTX_CALL = /handleError(?:WithContext)?\(\s*[A-Za-z_$][\w$.]*\s*,\s*(['"])([^'"]+)\1/g

export interface ErrorContextSite {
  /** 1-based 行号 */
  line: number
  /** 第二参数的字面量 */
  text: string
}

/**
 * 抽取一份源码里所有错误提示文案（含行号）。
 * 导出以便**分类器自检**（同一函数喂正例/反例），这是本仓既有范式。
 */
export function errorContextsIn(source: string): ErrorContextSite[] {
  const stripped = stripComments(source)
  const out: ErrorContextSite[] = []
  let m: RegExpExecArray | null
  while ((m = CTX_CALL.exec(stripped)) !== null) {
    out.push({ line: stripped.slice(0, m.index).split('\n').length, text: m[2] })
  }
  return out
}

/** 找出一份源码里「同一文案出现 >=2 次」的组（纯函数，便于自检）。 */
export function duplicateContextsIn(source: string): { text: string; lines: number[] }[] {
  const byText = new Map<string, number[]>()
  for (const site of errorContextsIn(source)) {
    const lines = byText.get(site.text) ?? []
    lines.push(site.line)
    byText.set(site.text, lines)
  }
  const out: { text: string; lines: number[] }[] = []
  for (const [text, lines] of byText) {
    if (lines.length >= 2) out.push({ text, lines })
  }
  return out
}

const SCAN_FILES = walkSourceFiles(SRC_DIR)

describe('错误提示可区分性门禁', () => {
  it('同一文件内不得有两处相同的错误提示文案', () => {
    const offenders: string[] = []
    let scanned = 0
    for (const file of SCAN_FILES) {
      const src = readFileSync(file, 'utf-8')
      scanned += errorContextsIn(src).length
      for (const dup of duplicateContextsIn(src)) {
        offenders.push(
          relative(process.cwd(), file) + ' 文案「' + dup.text + '」出现在行 ' + dup.lines.join(', ') +
            ' —— 同一组件内不同操作报同一句话，用户无法判断是哪个操作失败',
        )
      }
    }
    expect(scanned, '扫到的错误提示文案为 0 —— 扫描器/判据失效，门禁形同虚设').toBeGreaterThan(30)
    expect(offenders, '同一文件内出现重复的错误提示文案：\n' + offenders.join('\n')).toEqual([])
  })

  it('分母守卫：扫描器真的走到了全站源码，且判据不是恒真', () => {
    expect(SCAN_FILES.length, '扫描到的源码文件数为 0 —— 扫描路径错了').toBeGreaterThan(100)
    const withCtx = SCAN_FILES.filter((f) => errorContextsIn(readFileSync(f, 'utf-8')).length > 0)
    expect(withCtx.length, '没有任何文件命中错误提示 —— 判据被改坏').toBeGreaterThanOrEqual(10)
  })

  it('分类器自检：同文件重复必报、跨文件/单次必不报', () => {
    // 正例：这正是 LogPanel.vue 修复前的形态
    const positive = [
      "feedback.handleError(e, '操作失败')",
      "feedback.handleError(x, '操作失败')",
    ].join('\n')
    expect(duplicateContextsIn(positive)).toHaveLength(1)
    expect(duplicateContextsIn(positive)[0].lines).toEqual([1, 2])

    // 正例：handleError 与 handleErrorWithContext 混用，同一文案同样算重
    const mixed = [
      "feedback.handleError(e, '删除失败')",
      "feedback.handleErrorWithContext(e, '删除失败')",
    ].join('\n')
    expect(duplicateContextsIn(mixed)).toHaveLength(1)

    // 反例：各不相同 ⇒ 不报
    const negative = [
      "feedback.handleError(e, '开启日志流失败')",
      "feedback.handleError(e, '更新日志级别失败')",
      "feedback.handleError(e, '开启日志持久化失败')",
    ].join('\n')
    expect(duplicateContextsIn(negative)).toEqual([])

    // 反例：只在注释里重复 ⇒ 不算
    const commented = [
      "// feedback.handleError(e, '操作失败')",
      "feedback.handleErrorWithContext(e, '保存 GPIO 失败')",
    ].join('\n')
    expect(duplicateContextsIn(commented)).toEqual([])

    // 反例：单次出现不报
    expect(duplicateContextsIn("feedback.handleError(e, '唯一文案')")).toEqual([])
  })
})
