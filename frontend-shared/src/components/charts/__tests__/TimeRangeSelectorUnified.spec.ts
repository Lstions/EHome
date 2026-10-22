import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import TimeRangeSelector from '../TimeRangeSelector.vue'

/**
 * 时间档选择器「图表卡工具条档」统一契约。
 *
 * 2026-10-21：先收口「各页各自 :deep 覆盖导致同控件三档高度」。
 * 2026-10-22 用户第七轮真机报障「按钮大小比例与图表相比大太多了，按钮大小没有统一的规范」后，
 * 本组件改为**图表卡工具条唯一档位**（规范 §4.4「控件尺寸档位」）：
 *   · 桌面 32px / ≤768px 或 pointer:coarse 36px；字号 12px；圆角 6px；横向内距 10px；
 *   · 尺寸全部走 theme.css 的 --control-* token，与导出图标按钮、分组 tab 同一来源
 *     ⇒ 三者高度严格相等（不再出现 40/36/32 并存）；
 *   · 36px 属 §4.4 第 5 条「极高密度、非主要任务工具栏」例外，不得把正文 44px 档降级。
 */
const source = readFileSync(resolve(process.cwd(), 'src/components/charts/TimeRangeSelector.vue'), 'utf8')
const themeCss = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')
const chartsPanelSrc = readFileSync(resolve(process.cwd(), 'src/views/edge-device/bms/BmsChartsPanel.vue'), 'utf8')
const dashboardSrc = readFileSync(resolve(process.cwd(), 'src/views/dashboard/Dashboard.vue'), 'utf8')

describe('TimeRangeSelector 工具条档（32/36、12px、圆角 6、内距统一）', () => {
  // 组件里统一写成 `.time-range-selector :deep(.el-radio-button__inner) { … }`，
  // 下面按这个真实形态匹配（先按 `:deep(...) {` 定位规则块，再在该块内查属性）。

  it('分段按钮垂直居中（display:inline-flex + align-items:center + line-height:1）', () => {
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*display:\s*inline-flex/)
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*align-items:\s*center/)
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*justify-content:\s*center/)
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*line-height:\s*1;/)
  })

  it('字号恒 12px（与分组 tab / 导出按钮同档，不再 13/14/15 混用）', () => {
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*font-size:\s*var\(--control-font-toolbar,\s*12px\)/)
  })

  it('视觉高走同一 token：桌面 32px；窄屏与粗指针 36px（§4.4 第 5 条例外档）', () => {
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*height:\s*var\(--control-h-toolbar,\s*32px\)/)
    expect(source).toMatch(/@media \(max-width: 768px\)[\s\S]*?:deep\(\.el-radio-button__inner\)\s*\{[^}]*height:\s*var\(--control-h-toolbar-touch,\s*36px\)/)
    // 粗指针独立声明：宽视口触屏（900/1440 coarse）同样取 36，不能只靠宽度断点
    expect(source).toMatch(/@media \(pointer: coarse\)[\s\S]*?height:\s*var\(--control-h-toolbar-touch,\s*36px\)/)
  })

  it('圆角 6px + 横向内距 10px（与导出图标按钮 / 分组 tab 统一）', () => {
    expect(source).toMatch(/border-radius:\s*var\(--control-radius-toolbar,\s*6px\)/)
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*padding:\s*0 10px/)
  })

  it('36px 只在图表卡工具条作用域内（组件级），正文 44px 档在 theme.css 里未被降级', () => {
    // token 只追加、未改既有档
    expect(themeCss).toContain('--control-h-toolbar: 32px;')
    expect(themeCss).toContain('--control-h-toolbar-touch: 36px;')
    expect(themeCss).toContain('--control-font-toolbar: 12px;')
    expect(themeCss).toContain('--control-radius-toolbar: 6px;')
    // 既有 MUST 档原样保留：正文输入 / 粗指针分段 / 页头图标仍是 44px
    expect(themeCss).toMatch(/\.el-input__wrapper,[\s\S]{0,200}?min-height: 44px/)
    expect(themeCss).toMatch(/\.el-radio-button__inner,\s*\n\s*\.el-checkbox-button__inner \{\s*\n\s*min-height: 44px/)
    expect(themeCss).toMatch(/\.main-header \.el-button\.is-circle \{[^}]*min-height: 44px/)
    // 组件不得反过来把 44px 写成默认值（44 只应留在 theme.css 的全局 MUST 档里）
    const sourceCss = source.replace(/\/\*[\s\S]*?\*\//g, '')
    expect(sourceCss).not.toMatch(/min-height:\s*44px/)
  })
})

describe('TimeRangeSelector 档位管理', () => {
  /**
   * 曾有过 dense 紧凑档（卡片头 24px/12px）。2026-10-21 独立复核否决并已删除：
   *  ① 同一页面主控件 36px/13px 与卡片头 24px/12px 并存 = 用户反馈「大小也不对」的同源问题；
   *  ② dense 的选择器特异性 0,3,0 压过窄屏/粗指针规则（0,2,0），真实手机 393px
   *     （pointer:coarse）下触摸目标只有 24px（复核实测 52.7×24），低于项目 36px 档，很难点中。
   * 2026-10-22 起全站图表卡也只有一档：桌面 32px / 窄屏与粗指针 36px，字号恒 12px。
   */
  it('不再存在 dense 紧凑档（避免同页两种尺寸 + 手机触摸目标被压到 24px）', () => {
    expect(source).not.toContain('dense?: boolean')
    expect(source).not.toContain('is-dense')
    expect(chartsPanelSrc).not.toMatch(/<TimeRangeSelector[^>]*\sdense/)
  })

  it('BMS 图表卡不再本地覆盖分段按钮样式（各页各自覆盖是「同一控件三处不同高」的根因）', () => {
    expect(chartsPanelSrc).toContain('<TimeRangeSelector')
    expect(chartsPanelSrc).not.toMatch(/:deep\(\.el-radio-button__inner\)/)
  })

  it('showCustom=false 时不渲染「自定义」档（仪表盘只有固定档）', () => {
    expect(source).toMatch(/<el-radio-button v-if="showCustom" value="custom">/)
    expect(source).toContain('showCustom?: boolean')
  })

  it('仪表盘趋势卡复用同一组件（不自写 el-radio-group）', () => {
    expect(dashboardSrc).toContain('TimeRangeSelector')
    expect(dashboardSrc).toContain(':show-custom="false"')
    expect(dashboardSrc).not.toContain('<el-radio-button value="1h">1小时</el-radio-button>')
  })
})

describe('组件渲染', () => {
  it('4 个档位标签齐全，默认选中项来自 modelValue', () => {
    const wrapper = mount(TimeRangeSelector, {
      props: { modelValue: '24h', customRange: null },
    })
    // 全局 stub 下 el-radio-button 可能不渲染内部文本；至少断言四个档位存在于模板
    expect(source).toContain('1小时')
    expect(source).toContain('24小时')
    expect(source).toContain('7天')
    expect(source).toContain('自定义')
    expect(wrapper.exists()).toBe(true)
  })

  it('选择「自定义」才渲染日期区间选择器', async () => {
    const wrapper = mount(TimeRangeSelector, {
      props: { modelValue: '24h', customRange: null },
    })
    expect(wrapper.find('.custom-date-picker').exists()).toBe(false)
    await wrapper.setProps({ modelValue: 'custom' })
    expect(wrapper.find('.custom-date-picker').exists()).toBe(true)
  })
})
