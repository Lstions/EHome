import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

// 未知值占位符门禁（回归锁）。
//
// ===== 规范要求 =====
// 全站「未知 / 无数据」占位符必须统一为全角破折号 '—'（U+2014）。
// 历史上有三种写法并存：'—'（正确）、'--'（半角双连字符）、'-'（半角单连字符）。
//
// ===== 为什么值得门禁 =====
// 这类残留**不会报错、不会被功能测试发现** —— 界面上只是少了一点点视觉一致性，
// 但它是「规范写了、代码没执行」的直接证据。2026-09-15 复核时计划文档把它列为
// 「未做」（术语残留 / 未用 '—' / 骨架卡高度不一致 三项之一）。
// 2026-09-16 实测仍有 4 处 `return '--'`（BMS / 逆变器三个页面），已修并加本门禁。
//
// ===== 判据范围（刻意收窄，避免误报）=====
// 只查**字面量返回/赋值**的占位符：`'--'` 或 `"- -"` 形式的纯占位字符串。
// 不查注释（本仓注释里会引用 '--' 作为反例说明，如 DataPanel.vue）。
// 不查 CSS/样式文件（那里的 -- 是自定义属性前缀，与占位符无关）。
//
// ===== 本门禁查不了什么（诚实声明）=====
//   · 查不了「该用占位符的地方没写占位符」（例如返回空字符串）；
//   · 查不了 '-' 单连字符（可能与表格里的负号、分隔线混淆，误报风险高于收益）；
//   · 只覆盖 .vue / .ts 源码，不覆盖构建产物。

const SRC = join(__dirname, '..', '..')

/** 递归收集 .vue / .ts 源文件（排除测试与 node_modules）。 */
function collectSourceFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === '__tests__' || name === 'dist') continue
    const p = join(dir, name)
    const st = statSync(p)
    if (st.isDirectory()) {
      collectSourceFiles(p, out)
    } else if (name.endsWith('.vue') || name.endsWith('.ts')) {
      if (!name.endsWith('.spec.ts')) out.push(p)
    }
  }
  return out
}

/**
 * 分类器：从一份源码里抽出「半角连字符占位符」的违规点。
 *
 * 纯函数，便于用正例/反例自检 —— 这是本仓门禁的既有范式
 * （见 glossaryTermsGate.spec.ts / errorContextDistinctnessGate.spec.ts）。
 */
export function findHalfWidthPlaceholders(src: string): string[] {
  const out: string[] = []
  for (const raw of src.split('\n')) {
    const line = raw
    // 遮蔽注释：本仓注释会引用 '--' 作为反例
    const noComment = line.replace(/\/\/.*$/, '').replace(/<!--[\s\S]*?-->/g, '')
    // 匹配 '--' 或 "--" 作为独立字符串字面量
    const m = noComment.match(/['"]--['"]/)
    if (m) out.push(line.trim())
  }
  return out
}

describe('未知值占位符门禁', () => {
  it('分类器自检：正例必报、反例必不报', () => {
    const positives = [
      "if (v === null) return '--'",
      'const s = "--"',
      "default: return '--'",
    ]
    for (const p of positives) {
      expect(findHalfWidthPlaceholders(p).length, `正例必报: ${p}`).toBeGreaterThan(0)
    }

    const negatives = [
      "if (v === null) return '—'", // 正确写法
      "// 注释里的 '--' 不算（规范 §3.4.5 要求用 '—'）",
      "const css = '--color-primary'", // 这是 '--color-primary'，不是独立 '--'
      "grid-template-columns: repeat(2, 1fr)",
    ]
    for (const n of negatives) {
      expect(findHalfWidthPlaceholders(n), `反例必不报: ${n}`).toEqual([])
    }
  })

  it('全站源码不得使用半角 \'--\' 作为未知值占位符', () => {
    const files = collectSourceFiles(SRC)

    // 分母守卫：必须真的扫到足够多的源文件，否则门禁是空转
    // （2026-09-16 实测约 200+ 个 .vue/.ts；取 100 作为下限）
    expect(files.length).toBeGreaterThan(100)

    const offences: string[] = []
    for (const f of files) {
      const hits = findHalfWidthPlaceholders(readFileSync(f, 'utf-8'))
      for (const h of hits) offences.push(`${relative(SRC, f)}: ${h}`)
    }

    expect(
      offences,
      '未知值占位符必须统一为全角破折号 —（规范 §3.4.5）。\n' +
        '半角 -- 是历史遗留写法，不会报错但破坏全站一致性。\n' +
        offences.join('\n'),
    ).toEqual([])
  })

  it('全站确实存在用 — 的占位符（防止「全部改成空字符串」式规避）', () => {
    const files = collectSourceFiles(SRC)
    let emDashCount = 0
    for (const f of files) {
      const src = readFileSync(f, 'utf-8')
      const m = src.match(/return '—'/g)
      if (m) emDashCount += m.length
    }
    // 2026-09-16 实测 26 处；取 20 作为下限，允许正常增删
    expect(emDashCount).toBeGreaterThan(20)
  })
})
