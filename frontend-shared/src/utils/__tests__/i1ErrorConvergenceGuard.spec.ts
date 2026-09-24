import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

/**
 * I-1「ElMessage.error 收敛」的静态回归守卫。
 *
 * 为什么需要它（而不只靠逐组件的行为断言）：
 * 本轮改动的 109 处分布在 33 个文件里，逐个补行为测试成本高且极易漏。
 * 这条守卫以**源码事实**整体锁住收敛结果 —— 任何人再写回裸 ElMessage.error，
 * 或把错误提示写回 3 秒默认时长，都会在这里立刻变红。
 *
 * 实现说明（踩过的坑，勿回退）：
 * 最初用 execSync('grep ...') 实现，但 vitest 里 import.meta.url 不是 file: 协议，
 * 由它推导的 cwd 无效 → grep 抛错 → 被 catch 成空数组 → 测试**假绿**。
 * 因此改为直接用 node:fs 递归扫描源码，路径来自 process.cwd()（vitest 在
 * frontend-shared 下运行），无 shell、无转义、无静默失败。
 *
 * 注意：ElMessage.success / .warning / .info 按主控的分母纪律**不在本轮范围**，
 * 因此守卫只针对 .error，不误伤其余 146 处。
 */

const SRC = join(process.cwd(), 'src')

/** 递归收集 src 下参与收敛的源码文件（排除 __tests__ / 测试文件）。 */
function sourceFiles(dir = SRC): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      if (entry === '__tests__' || entry === 'node_modules') continue
      out.push(...sourceFiles(full))
    } else if (entry.endsWith('.ts') || entry.endsWith('.vue')) {
      out.push(full)
    }
  }
  return out
}

const BARE_ERROR_CALL = /ElMessage\.error\(/g
/**
 * 收敛出口的全部形态。
 *
 * **2026-09-15 修正一处假阴性**：原写法是 `/feedback\.(handleError|error)\(/g`，
 * 它**匹配不到 `feedback.handleErrorWithContext(`** —— 因为 `handleError` 之后紧跟的是 `W`
 * 而不是 `(`，正则要求字面量 `(`。后果：把一处 `handleError` 改进为 `handleErrorWithContext`
 * 会让下面的分母守卫**减少 1**，于是「做了正确的改进」反而被判成「收敛在退化」。
 * 该假阴性由 LogPanel.vue 的三处歧义文案修复暴露（110 → 108）。
 * `handleErrorWithContext` 是 feedback 的正规出口（feedback.ts:132），必须计入分母。
 */
const FUNNEL_CALL = /feedback\.(handleErrorWithContext|handleError|error)\(/g

function scan(re: RegExp): { file: string; line: number; text: string }[] {
  const hits: { file: string; line: number; text: string }[] = []
  for (const file of sourceFiles()) {
    const lines = readFileSync(file, 'utf8').split('\n')
    lines.forEach((text, i) => {
      re.lastIndex = 0
      if (re.test(text)) hits.push({ file: file.slice(SRC.length + 1), line: i + 1, text: text.trim() })
    })
  }
  return hits
}

describe('I-1 静态守卫：错误提示必须走 utils/feedback', () => {
  it('守卫自身有效：能扫到源码文件，且正则确实会命中已知形态', () => {
    // 先证明扫描器不是空转 —— 否则下面所有 toEqual([]) 都是假绿
    expect(sourceFiles().length).toBeGreaterThan(100)
    const re = new RegExp(BARE_ERROR_CALL.source)
    expect(re.test("ElMessage.error('刷新失败')")).toBe(true)
    expect(re.test('ElMessage.error(\`发送失败: \${errMsg}\`)')).toBe(true)
    expect(re.test("feedback.error('刷新失败')")).toBe(false)
  })

  it('src 下不允许再出现裸 ElMessage.error（__tests__ 除外）', () => {
    const hits = scan(new RegExp(BARE_ERROR_CALL.source, 'g'))
    const detail = hits.map(h => `  ${h.file}:${h.line}  ${h.text}`).join('\n')
    expect(hits, `发现裸 ElMessage.error，请改走 feedback.handleError / feedback.error：\n${detail}`).toEqual([])
  })

  it('分类器自检：出口正则必须覆盖全部三种形态（防假阴性）', () => {
    // 这条是 2026-09-15 新增：原正则漏掉 handleErrorWithContext，
    // 导致「把 handleError 改进为 WithContext」会被误判为分母下降。
    for (const form of [
      "feedback.handleError(e, 'x')",
      "feedback.handleErrorWithContext(e, 'x')",
      "feedback.error('x')",
    ]) {
      const re = new RegExp(FUNNEL_CALL.source, 'g')
      expect(re.test(form), '出口正则必须匹配: ' + form).toBe(true)
    }
    // 反例：不该被当成出口的写法
    for (const notForm of [
      "ElMessage.error('x')",
      "console.error('x')",
      "logger.error('x')",
    ]) {
      const re = new RegExp(FUNNEL_CALL.source, 'g')
      expect(re.test(notForm), '不该匹配: ' + notForm).toBe(false)
    }
  })

  it('反向守卫：收敛不得靠"删掉调用"实现，出口必须真实且广泛使用', () => {
    const hits = scan(new RegExp(FUNNEL_CALL.source, 'g'))
    // 本轮收口 109 处 + 既有 4 处 = 113；2026-09-15 起含 handleErrorWithContext，
    // 实测 117（三种形态合计），阈值保持不变（110）以免掩盖真实退化。
    //
    // 阈值下调（2026-09-23，110 → 100）：删除了死文件 `NodeDetail.vue`（C5 清理），
    // 它贡献 7 处出口调用（`git show HEAD:frontend-shared/src/views/node/NodeDetail.vue
    // | grep -cE "feedback\.(handleError|handleErrorWithContext|error)"` → 7），实测降到 109。
    // 必须区分两类下降：
    //   - 「删掉调用以通过门禁」= 本守卫要防的作弊 ⇒ 降阈值会掩盖它；
    //   - 「删除承载调用的死文件」= 合法清理 ⇒ 不降阈值会变成假红。
    // 本次属后者（该文件已由报告附录 A/B 证明不可达且能力已迁移）。
    // 下界取 100：远高于"删几处调用"的量级，故仍能捕获后者式作弊。
    expect(hits.length).toBeGreaterThanOrEqual(100)
  })

  it('每个收敛后的文件都真实 import 了 utils/feedback', () => {
    const users = scan(new RegExp(FUNNEL_CALL.source, 'g'))
    const files = [...new Set(users.map(h => h.file))]
    const missing = files.filter(f => !readFileSync(join(SRC, f), 'utf8').includes("utils/feedback"))
    expect(missing).toEqual([])
    expect(files.length).toBeGreaterThanOrEqual(30)
  })

  it('feedback 模块仍把错误时长钉在 5 秒（不得回落到 EP 默认 3 秒）', () => {
    const src = readFileSync(join(SRC, 'utils', 'feedback.ts'), 'utf8')
    expect(src).toMatch(/DEFAULT_ERROR_OPTS[^\n]*duration:\s*5000/)
  })
})
