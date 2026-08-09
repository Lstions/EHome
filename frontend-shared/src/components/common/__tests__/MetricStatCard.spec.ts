import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
// 契约测试：直接读 SFC 源码字符串（项目惯例，happy-dom 不注入 scoped style 到 DOM）
import src from '../MetricStatCard.vue?raw'

/**
 * MetricStatCard.vue 测试
 *
 * 行为断言：挂载真实组件，验证 label/value/unit/subText/progress/direction 渲染。
 * 契约断言：读源码字符串，防「渐变底白图标」「辅助槽不占位」回归。
 */

describe('MetricStatCard.vue 行为', () => {
  it('渲染 label、value、unit、subText', () => {
    const wrapper = mount(MetricStatCard, {
      props: {
        label: '剩余容量',
        value: 76.2,
        unit: 'Ah',
        subText: '/ 105Ah',
      },
      slots: {
        icon: '<span class="test-icon">图</span>',
      },
    })

    expect(wrapper.get('.metric-label').text()).toBe('剩余容量')
    expect(wrapper.get('.metric-value').text()).toContain('76.2')
    expect(wrapper.get('.metric-unit').text()).toBe('Ah')
    expect(wrapper.get('.metric-sub').text()).toBe('/ 105Ah')
    // icon slot 透传
    expect(wrapper.find('.test-icon').exists()).toBe(true)
  })

  it('progress 提供时渲染 el-progress 并透传百分比', () => {
    const wrapper = mount(MetricStatCard, {
      props: {
        label: 'SOC',
        value: 62,
        unit: '%',
        progress: 62,
      },
    })

    expect(wrapper.find('.el-progress').exists()).toBe(true)
    expect(wrapper.find('.el-progress-bar__inner').attributes('style') ?? '').toContain('width: 62%')
  })

  it('direction="discharge" 时辅助槽显示「放电中」且用 danger 红（少爷拍板：放电红）', () => {
    const wrapper = mount(MetricStatCard, {
      props: {
        label: '电流',
        value: -12.3,
        unit: 'A',
        direction: 'discharge',
      },
      slots: {
        icon: '<span>⚡</span>',
      },
    })

    const directionEl = wrapper.get('.metric-direction')
    expect(directionEl.text()).toBe('放电中')
    // 少爷拍板：放电= danger 红（语义方向色，非告警）
    expect(wrapper.html()).toContain('el-color-danger')
    // 不带残留的 negative 类（旧 .negative 标红机制已废弃，由 direction 配色接管）
    expect(directionEl.classes()).not.toContain('negative')
  })

  it('direction="charge"/"idle" 分别显示「充电中」(success 绿)/「静止」', () => {
    const charge = mount(MetricStatCard, {
      props: { label: '电流', value: 5, unit: 'A', direction: 'charge' },
    })
    expect(charge.get('.metric-direction').text()).toBe('充电中')
    // 少爷拍板：充电= success 绿
    expect(charge.html()).toContain('el-color-success')

    const idle = mount(MetricStatCard, {
      props: { label: '电流', value: 0, unit: 'A', direction: 'idle' },
    })
    expect(idle.get('.metric-direction').text()).toBe('静止')
  })

  it('subText/progress/direction 均缺省时渲染占位行（辅助槽永远占位）', () => {
    const wrapper = mount(MetricStatCard, {
      props: { label: '总电压', value: 51.2, unit: 'V' },
    })

    const placeholder = wrapper.get('.metric-aux-placeholder')
    expect(placeholder.attributes('aria-hidden')).toBe('true')
    // happy-dom 不注入 scoped style，visibility 由下方契约测试断言源码规则
    // 占位行必须是 .metric-aux 的直接子元素，维持同排卡片等高结构
    expect(placeholder.element.parentElement?.classList.contains('metric-aux')).toBe(true)
  })

  it('tone 映射到图标容器 color 语义变量（透明底彩色图标）', () => {
    const wrapper = mount(MetricStatCard, {
      props: { label: 'SOC', value: 88.5, unit: '%', tone: 'success' },
      slots: { icon: '<span>图</span>' },
    })

    const iconStyle = wrapper.get('.metric-icon').attributes('style') ?? ''
    expect(iconStyle).toContain('var(--el-color-success)')
    // 图标容器不得带 background（透明底契约）
    expect(iconStyle).not.toMatch(/background/i)
  })

  it('tone 缺省为 primary', () => {
    const wrapper = mount(MetricStatCard, {
      props: { label: 'SOC', value: 88.5, unit: '%' },
    })

    expect(wrapper.get('.metric-icon').attributes('style')).toContain('var(--el-color-primary)')
  })
})

describe('MetricStatCard.vue 契约（源码字符串断言）', () => {
  // 提取 <style> 块（happy-dom 不注入 scoped style，直接读源码）
  const styleMatch = src.match(/<style[^>]*>([\s\S]*?)<\/style>/)
  const css = styleMatch?.[1] ?? ''

  it('源码不含 linear-gradient（防渐变底回退）', () => {
    expect(src).not.toContain('linear-gradient')
    expect(css).not.toMatch(/gradient/i)
  })

  it('图标容器所有规则块无 background 声明（透明底契约）', () => {
    // 匹配所有 .metric-icon 开头的规则块（含 :deep(.el-icon) 与移动端重载）
    const iconRules = css.match(/\.metric-icon[^{]*\{[^}]*\}/g) ?? []
    expect(iconRules.length).toBeGreaterThan(0)
    for (const rule of iconRules) {
      expect(rule).not.toMatch(/background/i)
    }
  })

  it('组件内不存在 .negative 放电标红类（语义色混淆回归防护）', () => {
    expect(css).not.toContain('.negative')
    expect(src).not.toContain('.negative')
  })

  it('辅助槽有 min-height 且存在 invisible 占位行规则（永远占位契约）', () => {
    expect(css).toMatch(/\.metric-aux\s*\{[^}]*min-height/)
    expect(css).toMatch(/\.metric-aux-placeholder\s*\{[^}]*visibility:\s*hidden/)
  })

  it('移动端断点完整覆盖图标/数值/单位紧凑规格', () => {
    // 媒体块是 style 末尾最后一个块，贪婪匹配到最后一个 } 即完整媒体块
    const mobileBlock = css.match(/@media \(max-width: 768px\)\s*\{([\s\S]*)\}/)?.[1] ?? ''
    expect(mobileBlock).toContain('width: 34px')
    expect(mobileBlock).toContain('height: 34px')
    expect(mobileBlock).toContain('border-radius: 8px')
    expect(mobileBlock).toContain('font-size: 22px')
    expect(mobileBlock).toContain('.metric-value')
    expect(mobileBlock).toContain('.metric-unit')
  })
})
