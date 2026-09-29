import { describe, it, expect } from 'vitest'
import { readFileSync, existsSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * 「前景落在语义实心填充上」对比度合同（2026-09-29）。
 *
 * 背景：暗色主题把语义基色**提亮**（success #147a3a→#85ce61、warning #9a5b06→#ebb563、
 * danger #b91c1c→#f78989），于是写死 `color: #fff` 的图标/文字在暗色下塌到 1.85–2.36:1。
 * 最严重的是 NetworkBanner（离线提示）——恰恰是用户最需要看清的元素。
 *
 * 本测试是**源码级防回退护栏**：它不含浏览器，只断言「不得再出现未加说明的
 * `color: #fff` 落在语义实心色上」。真实渲染仍以 CDP 实测为准（见交付说明）。
 *
 * 为什么不用 `?raw` 导入：vitest 默认 `css:false`，`import '*.vue?raw'` 在部分配置下
 * 返回空串会让断言永久通过（仓库既有教训，见 SidebarThemeTokens.spec.ts:19-20）。
 * 故一律 readFileSync。
 */

const ROOT = process.cwd()
const THEME = readFileSync(resolve(ROOT, 'src/styles/theme.css'), 'utf8')

/**
 * 允许保留 `color: #fff` 的文件与理由。
 * 加白名单时必须逐个写清「底色是什么、为什么白字成立」——不接受"以后再看"。
 */
const ALLOWED_WHITE_ON_FILL: Record<string, string> = {
  'src/views/layout/MainLayout.vue':
    '底为 --brand-gradient（两端深色，暗色白字最差 4.83:1）/ 侧栏恒深色品牌底。',
  'src/views/node/NodeList.vue': '⚠️ 死代码（.stat-content 模板未使用），已加注释标注。',
  'src/views/dashboard/Dashboard.vue': '⚠️ 死声明（模板内联 style 覆盖），已加注释标注。',
  'src/views/config/DeviceConfigList.vue':
    '基类前景；底由 --brand-gradient 给，语义色变体已在各自规则里改用 --text-on-fill。',
  'src/views/edge-device/EdgeDeviceList.vue':
    '基类前景；.total 走 --brand-gradient，语义色变体已改用 --text-on-fill。',
  'src/views/edge-device/inverter/InverterEnergyCard.vue':
    '仅 monthly（primary 家族，两端深色）保留白字；其余变体用 --text-on-fill。',
}

function vueFiles(): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = resolve(dir, e.name)
      if (e.isDirectory()) walk(p)
      else if (e.name.endsWith('.vue')) out.push(p)
    }
  }
  walk(resolve(ROOT, 'src'))
  return out
}
import { readdirSync } from 'node:fs'

/** 该 `color: #fff` 行**上方 4 行内**是否有解释性注释（中文注释即视为人工裁决过）。 */
function hasJustification(lines: string[], idx: number): boolean {
  for (let i = Math.max(0, idx - 4); i < idx; i += 1) {
    const l = lines[i].trim()
    if ((l.startsWith('/*') || l.startsWith('//') || l.startsWith('*')) && /[\u4e00-\u9fa5]/.test(l)) return true
  }
  return false
}

describe('「白字落在语义实心填充上」对比度合同', () => {
  it('token 层：--text-on-fill 亮暗不同，且方向正确（亮白 / 暗深）', () => {
    expect(THEME).toMatch(/--text-on-fill:\s*#ffffff/)
    expect(THEME).toMatch(/--text-on-fill:\s*#1a1a1a/)
  })

  it('token 层：--brand-gradient 亮暗不同（暗色不得沿用 primary→success）', () => {
    const decls = [...THEME.matchAll(/--brand-gradient:\s*([^;]+);/g)].map((m) => m[1].trim())
    expect(decls.length, '--brand-gradient 应在亮暗两块各定义一次').toBeGreaterThanOrEqual(2)
    expect(new Set(decls).size, '--brand-gradient 亮暗取值相同 = 没做主题化').toBeGreaterThan(1)
    // 暗色块里不得再出现「primary → success」这一暗色下白字只有 1.91 的组合
    const darkIdx = THEME.indexOf('[data-theme="dark"]')
    const darkPart = THEME.slice(darkIdx, THEME.indexOf('/* ============================================\n   Element Plus 桥接变量的**暗色安全网**'))
    expect(darkPart, '暗色 --brand-gradient 不应再跨到 success（暗色下白字 1.91）').not.toMatch(
      /--brand-gradient:[^;]*--el-color-success|--brand-gradient:[^;]*#85ce61/,
    )
  })

  it('不得出现「语义实心色 + 未加说明的 color:#fff」组合（白名单外零容忍）', () => {
    const offenders: string[] = []
    for (const f of vueFiles()) {
      const rel = f.slice(ROOT.length + 1)
      if (rel.includes('/dev/')) continue // DEV 专用页不进生产（router 里 import.meta.env.DEV 静态消除）
      const src = readFileSync(f, 'utf8')
      const lines = src.split('\n')
      lines.forEach((line, i) => {
        if (!/color:\s*#fff\b/.test(line)) return
        // 同一行内联背景也算（如 `.x { background: var(--el-color-warning); color: #fff; }`）
        const sameLine = /background[^;]*--el-color-(success|warning|danger|error|info)|background[^;]*--color-(success|warning|danger|info|adc)/.test(line)
        if (!sameLine && !ALLOWED_WHITE_ON_FILL[rel]) {
          offenders.push(`${rel}:${i + 1}`)
        } else if (!hasJustification(lines, i) && rel !== 'src/views/layout/MainLayout.vue') {
          // 白名单文件也要求就近注释说明（MainLayout 已有文件级说明）
          offenders.push(`${rel}:${i + 1} (缺少就近说明注释)`)
        }
      })
    }
    expect(offenders, '以下位置的白字未加说明，需改用 --text-on-fill 或补注释：\n' + offenders.join('\n')).toEqual([])
  })

  it('NetworkBanner（离线提示）不得再用「语义实心底 + 白字」', () => {
    const p = resolve(ROOT, 'src/components/common/NetworkBanner.vue')
    expect(existsSync(p)).toBe(true)
    const src = readFileSync(p, 'utf8')
    // 这是本类缺陷里最严重的一处（离线横幅是用户最需要看清的通知）
    expect(src).not.toMatch(/\.network-banner[^{]*\{[^}]*color:\s*#fff/)
    // 必须走「-light-9 浅底 + 基色文字」范式
    expect(src).toMatch(/\.network-banner\.warning\s*\{[^}]*background:\s*var\(--el-color-warning-light-9\)/)
    expect(src).toMatch(/\.network-banner\.error\s*\{[^}]*background:\s*var\(--el-color-danger-light-9\)/)
  })
})
