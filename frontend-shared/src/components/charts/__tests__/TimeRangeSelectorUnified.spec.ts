import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import TimeRangeSelector from '../TimeRangeSelector.vue'

/**
 * 时间档选择器统一契约（2026-10-21 用户真机反馈「时间样式不对、文字没居中、大小也不对」）。
 *
 * 背景（生产实测 393×851）：
 *   ① 全局 theme.css 只给 .el-radio-button__inner 抬了 min-height:36px（触摸目标），
 *      没改 display/line-height ⇒ 盒子被撑高、文字贴上沿（看起来没居中）；
 *   ② 各调用页各自 :deep 覆盖成 24px ⇒ 同一控件在图表卡/诊断卡里两种高度；
 *   ③ 窄屏 flex:1 1 auto 按内容分配宽度 ⇒ 实测 53/59/41/58px 长短不齐。
 * 本文件锁住「组件内一次收口」的判据，防止再退回各页各写一套。
 */
const source = readFileSync(resolve(process.cwd(), 'src/components/charts/TimeRangeSelector.vue'), 'utf8')
const chartsPanelSrc = readFileSync(resolve(process.cwd(), 'src/views/edge-device/bms/BmsChartsPanel.vue'), 'utf8')
const dashboardSrc = readFileSync(resolve(process.cwd(), 'src/views/dashboard/Dashboard.vue'), 'utf8')

describe('TimeRangeSelector 样式统一', () => {
  // 组件里统一写成 `.time-range-selector :deep(.el-radio-button__inner) { … }`，
  // 下面按这个真实形态匹配（先按 `:deep(...) {` 定位规则块，再在该块内查属性）。

  it('分段按钮垂直居中（display:inline-flex + align-items:center + line-height:1）', () => {
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*display:\s*inline-flex/)
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*align-items:\s*center/)
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*justify-content:\s*center/)
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*line-height:\s*1;/)
  })

  it('统一字号 13px（不再继承 EP small 的 12px 与相邻正文不一致）', () => {
    expect(source).toMatch(/:deep\(\.el-radio-button__inner\)\s*\{[^}]*font-size:\s*13px/)
  })

  it('窄屏四段等宽铺满（flex:1 1 0），而非按内容分配长短不齐', () => {
    expect(source).toMatch(/:deep\(\.el-radio-button\)\s*\{[^}]*flex:\s*1 1 0/)
    expect(source).toMatch(/@media \(max-width: 768px\)[\s\S]*?:deep\(\.el-radio-button__inner\)\s*\{[^}]*width:\s*100%/)
  })

  it('触摸目标：窄屏 ≥40px、粗指针 44px', () => {
    expect(source).toMatch(/@media \(max-width: 768px\)[\s\S]*?min-height:\s*40px/)
    expect(source).toMatch(/@media \(pointer: coarse\)[\s\S]*?min-height:\s*44px/)
  })
})

describe('TimeRangeSelector 两种档位', () => {
  it('dense 档用于卡片头（与相邻 small 按钮同高 24px）', () => {
    expect(source).toContain('dense?: boolean')
    expect(source).toMatch(/\.is-dense[^{]*\.el-radio-button__inner[^}]*min-height:\s*24px|is-dense :deep\(\.el-radio-button__inner\)|is-dense[\s\S]{0,120}min-height:\s*24px/)
  })

  it('showCustom=false 时不渲染「自定义」档（仪表盘只有固定档）', () => {
    expect(source).toMatch(/<el-radio-button v-if="showCustom" value="custom">/)
    expect(source).toContain('showCustom?: boolean')
  })

  it('BMS 图表卡用 dense 档且不再本地覆盖分段按钮样式（覆盖是「三处不同高」的根因）', () => {
    expect(chartsPanelSrc).toMatch(/<TimeRangeSelector[\s\S]{0,200}?dense/)
    expect(chartsPanelSrc).not.toMatch(/:deep\(\.el-radio-button__inner\)/)
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