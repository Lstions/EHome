import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { stripComments, walkSourceFiles } from '@/utils/__tests__/sourceScan'

/**
 * R2 / F-7 防漂移门禁：**前端不得再持有 4 级 risk 字面量**。
 *
 * 背景：风险等级的唯一事实源在后端（deviceaction.RiskLevel，2 级）。驱动出厂声明的
 * low/medium/high/critical 由后端在 Register 期归一化，前端只消费 risk_level /
 * requires_confirmation。前端一旦自己比较 4 级字面量，就出现了第二份判定 —— 后端
 * 改映射时前端会静默漂移（这正是改造前 DeviceControlPanel.vue:40-41/148 的形态）。
 *
 * 反向验证（方案 §8 F-7 指定的注入）：把 DeviceControlPanel.vue 的判定改回
 * risk === 'critical' → 本门禁必须失败。
 *
 * 扫描面：src/ 下的业务源码（.vue/.ts），**跳过 __tests__**（测试夹具刻意保留旧
 * 4 级 risk 字段以证明后端的兼容期字段仍在）。
 */

const SRC = resolve(process.cwd(), 'src')

const RISK_LEVELS = ['low', 'medium', 'high', 'critical']
const COMPARE_OPS = ['===', '!==', '==', '!=']

/**
 * 抓「把 risk 属性与 4 级字面量直接比较」的写法。
 *
 * 判据锁定「risk 标识符 + 比较运算符 + 4 级字面量」这一形态，因此下列**不判违规**：
 *   · 后端兼容期字段作为**数据**出现（fixture / 类型声明）；
 *   · 注释或字符串里只是文档说明（stripComments 已遮蔽注释）。
 */
export function riskLiteralComparisonsIn(source: string): string[] {
  const hits: string[] = []
  const code = stripComments(source)
  const ops = COMPARE_OPS.join('|')
  code.split('\n').forEach((line, index) => {
    for (const level of RISK_LEVELS) {
      const re = new RegExp('\\brisk\\s*(' + ops + ')\\s*["\']' + level + '["\']')
      if (re.test(line)) hits.push('第 ' + (index + 1) + ' 行: ' + line.trim())
    }
  })
  return hits
}

describe('前端风险字面量防漂移 (R2 / F-7)', () => {
  it('业务源码里不得出现 risk 与 4 级字面量的比较', () => {
    const files = walkSourceFiles(SRC)
    // 分母守卫：必须真的扫到源码，否则门禁是空转 (vacuous pass)。
    expect(files.length, '扫描面为空 —— 路径写错了，门禁形同虚设').toBeGreaterThan(50)
    const offenses: string[] = []
    for (const file of files) {
      for (const hit of riskLiteralComparisonsIn(readFileSync(file, 'utf8'))) {
        offenses.push(file + ': ' + hit)
      }
    }
    expect(offenses, '前端出现 ' + offenses.length + ' 处 4 级 risk 字面量比较（应改读后端下发的 risk_level / requires_confirmation）').toEqual([])
  })

  it('判定器自检：违规写法必须被抓到，合法写法不得误报', () => {
    // 反向自证 —— 一个坏掉的判定器会让上面的门禁永远绿。
    expect(riskLiteralComparisonsIn("if (action.risk === 'critical') {}")).toHaveLength(1)
    expect(riskLiteralComparisonsIn('const hot = def.risk !== "high"')).toHaveLength(1)
    expect(riskLiteralComparisonsIn('if (action.risk == "medium") {}')).toHaveLength(1)
    // 合法写法：读后端下发的 2 级契约、样式映射表、注释里的说明。
    expect(riskLiteralComparisonsIn("if (action.risk_level === 'elevated') {}")).toEqual([])
    expect(riskLiteralComparisonsIn("const risk = 'high' // 驱动出厂方言说明")).toEqual([])
    expect(riskLiteralComparisonsIn("// 改造前是 action.risk === 'critical'")).toEqual([])
  })
})
