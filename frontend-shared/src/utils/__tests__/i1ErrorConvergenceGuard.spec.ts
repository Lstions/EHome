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
const FUNNEL_CALL = /feedback\.(handleError|error)\(/g

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

  it('反向守卫：收敛不得靠"删掉调用"实现，出口必须真实且广泛使用', () => {
    const hits = scan(new RegExp(FUNNEL_CALL.source, 'g'))
    // 本轮收口 109 处 + 既有 4 处 = 113
    expect(hits.length).toBeGreaterThanOrEqual(110)
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
