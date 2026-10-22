import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * 图表卡工具条「控件尺寸档位」契约（2026-10-22 第七轮真机报障①）。
 *
 * 用户原话：「按钮大小比例与图表相比大太多了，按钮大小没有统一的规范」。
 * 基线实测（真实逆变器页 /edge-device/15，.logs/ui-fix/round7/before-summary.json）：
 *   393 fine：时间档 48.5×40(fs13) / 导出 108.8×36(fs15) / 分组 tab 32(fs13)，
 *   同卡三档高度 40/36/32、三档字号 13/14/15；controlsH/chartH = 0.90（320 下 1.10~1.20）。
 *
 * 本文件锁「一档收口」的源码事实（真实浏览器量测在 .tmp-probe/r7-after.mjs / after-summary.json）：
 *   ① 四个 token 在 theme.css 只追加、既有 44px 档一字未改；
 *   ② 时间档 / 导出图标按钮 / 分组 tab 三处都消费同一组 token ⇒ 高度来源唯一、必然相等；
 *   ③ 不再有旧的 30/32/36/40/44 硬编码档混入工具条；字号 12、圆角 6；
 *   ④ 规范文档已落章（含 §4.4 第 5 条例外的判据与两种例外条件）。
 */
const read = (p: string) => readFileSync(resolve(process.cwd(), p), 'utf8')
const themeCss = read('src/styles/theme.css')
const historySource = read('src/views/edge-device/shared/HistoryChartSection.vue')
const timeRangeSource = read('src/components/charts/TimeRangeSelector.vue')
const doc = readFileSync(resolve(process.cwd(), '../docs/规范/前端开发与UIUX设计规范.md'), 'utf8')

/** 去掉注释，避免注释里的旧数字被当成规则（本仓有「注释骗过 indexOf」的教训）。 */
const stripCssComments = (css: string) => css.replace(/\/\*[\s\S]*?\*\//g, '')

describe('控件尺寸档位 token：只追加，不改既有 44px 档', () => {
  it('theme.css 定义了工具条四个 token（桌面 32 / 触摸 36 / 字号 12 / 圆角 6）', () => {
    expect(themeCss).toContain('--control-h-toolbar: 32px;')
    expect(themeCss).toContain('--control-h-toolbar-touch: 36px;')
    expect(themeCss).toContain('--control-font-toolbar: 12px;')
    expect(themeCss).toContain('--control-radius-toolbar: 6px;')
  })

  it('既有 44px MUST 档原样保留（正文输入 / 粗指针分段 / 页头图标不得被工具条例外降级）', () => {
    expect(themeCss).toMatch(/\.el-input__wrapper,[\s\S]{0,200}?min-height: 44px/)
    expect(themeCss).toMatch(/\.el-radio-button__inner,\s*\n\s*\.el-checkbox-button__inner \{\s*\n\s*min-height: 44px/)
    expect(themeCss).toMatch(/\.main-header \.el-button\.is-circle \{[^}]*min-height: 44px/)
  })

  it('没有新增 max-width:768px 块（F27/F30 的抽取锚点与块数门禁必须保持）', () => {
    const narrowBlocks = themeCss.split('@media (max-width: 768px)').length - 1
    expect(narrowBlocks, '出现了额外的 max-width:768 块：' + narrowBlocks).toBeLessThanOrEqual(3)
  })
})

describe('三件控件同档：时间档 / 导出图标按钮 / 分组 tab 共用 --control-* token', () => {
  const css = stripCssComments(historySource)

  it('导出图标按钮：32/36 方形、圆角 6、内距 0（图标按钮宽度=高度）', () => {
    expect(css).toMatch(/\.hist-export \{[^}]*width: var\(--control-h-toolbar, 32px\)/)
    expect(css).toMatch(/\.hist-export \{[^}]*height: var\(--control-h-toolbar, 32px\)/)
    expect(css).toMatch(/\.hist-export \{[^}]*border-radius: var\(--control-radius-toolbar, 6px\)/)
    expect(css).toMatch(/\.hist-export \{[^}]*padding: 0;/)
    expect(css).toMatch(/\.hist-export \{[^}]*min-height: var\(--control-h-toolbar, 32px\)/)
  })

  it('导出按钮字号也钉在 token 上（防 App.vue ≤480px 的 .el-button 15px 全局规则顶回来）', () => {
    // 第七轮独立验证 D6：App.vue 有 @media (max-width:480px){.el-button{font-size:15px}}，
    // 若不显式声明，320~480px 下工具条会重新出现「同一行两种字号」。
    expect(css).toMatch(/\.hist-export \{[^}]*font-size: var\(--control-font-toolbar, 12px\)/)
    // 反向：不得靠全局规则顺序取胜（必须在本组件的 scoped 规则里显式写）
    const appVue = read('src/App.vue')
    expect(appVue).toContain('@media (max-width: 480px)')
    expect(appVue).toMatch(/\.el-button \{\s*font-size: 15px;/)
  })

  it('分组 tab：与导出同 token（高度/字号/圆角），无 30px 旧档', () => {
    expect(css).toMatch(/\.hist-tab \{[^}]*height: var\(--control-h-toolbar, 32px\)/)
    expect(css).toMatch(/\.hist-tab \{[^}]*font-size: var\(--control-font-toolbar, 12px\)/)
    expect(css).toMatch(/\.hist-tab \{[^}]*border-radius: var\(--control-radius-toolbar, 6px\)/)
    expect(css).toMatch(/\.hist-tab \{[^}]*padding: 0 10px;/)
    // 旧档（30px / 13px）不得残留：它们正是「同卡三档」的来源
    expect(css).not.toContain('height: 30px')
    expect(css).not.toMatch(/\.hist-tab \{[^}]*font-size: 13px/)
  })

  it('时间档分段：同 token（32/36、12px、10px 内距）', () => {
    const tr = stripCssComments(timeRangeSource)
    expect(tr).toMatch(/:deep\(\.el-radio-button__inner\) \{[^}]*height: var\(--control-h-toolbar, 32px\)/)
    expect(tr).toMatch(/:deep\(\.el-radio-button__inner\) \{[^}]*font-size: var\(--control-font-toolbar, 12px\)/)
    expect(tr).toMatch(/:deep\(\.el-radio-button__inner\) \{[^}]*padding: 0 10px;/)
    expect(tr).toMatch(/--control-radius-toolbar/)
  })

  it('窄屏与粗指针都切 36px 触摸档（§4.4 第 5 条例外），且不只依赖宽度断点', () => {
    for (const [name, src] of [['HistoryChartSection', css], ['TimeRangeSelector', stripCssComments(timeRangeSource)]] as const) {
      expect(src, name + ' 缺 --control-h-toolbar-touch').toContain('--control-h-toolbar-touch')
      expect(src, name + ' 缺 pointer:coarse 分支').toContain('@media (pointer: coarse)')
    }
    // 导出与 tab 在窄屏/粗指针块里都显式声明 36
    expect(css).toMatch(/@media \(max-width: 768px\)[\s\S]*?\.hist-tab \{[^}]*height: var\(--control-h-toolbar-touch, 36px\)/)
    expect(css).toMatch(/@media \(pointer: coarse\)[\s\S]*?\.hist-export \{[^}]*min-height: var\(--control-h-toolbar-touch, 36px\)/)
    // 窄屏导出必须**同时**切宽高（只切高度会变成 32×36 长方形；实测基线 320 抓住过）
    expect(css).toMatch(/@media \(max-width: 768px\)[\s\S]*?\.hist-export \{[^}]*width: var\(--control-h-toolbar-touch, 36px\)/)
  })
})

describe('规范文档已落章（§4.4 控件尺寸档位 + §4.4.5 例外判据）', () => {
  it('新增「控件尺寸档位」小节并给出四档表（图表卡工具条 / 正文输入 / 页头图标 / 表格行内）', () => {
    expect(doc).toContain('#### 控件尺寸档位')
    expect(doc).toMatch(/图表卡工具条[^\n]*32px[^\n]*36px/)
    expect(doc).toMatch(/正文输入控件[^\n]*32px[^\n]*44px/)
    expect(doc).toMatch(/页头图标按钮[^\n]*44px[^\n]*44px/)
    expect(doc).toMatch(/表格行内操作按钮[^\n]*36/)
  })

  it('写清 36px 依据 §4.4.5（§4.4 第 5 条）例外的两个条件（可访问名称 + 相邻间距）', () => {
    expect(doc).toContain('§4.4.5')
    expect(doc).toContain('§4.4 第 5 条')
    expect(doc).toMatch(/极高密度、非主要任务的工具栏/)
    expect(doc).toMatch(/可访问名称/)
    expect(doc).toMatch(/间距 ≥6px/)
    // 44px 档不得被例外降级，文档必须有这句判据
    expect(doc).toMatch(/不得.*降级|不得被工具条例外降级/)
  })
})
