import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import StatusItemGrid from '@/components/common/StatusItemGrid.vue'

// 全局 Element Plus stub 已在 src/test-setup.ts 注册。
// ElTag 渲染为 <span class="el-tag" :data-type="type">,
// ElEmpty 渲染为 <div class="el-empty">{{ description }}</div>,
// ElIcon 渲染为 <span class="el-icon">。
// WarningFilled/CircleCheck 从 @element-plus/icons-vue 导入，需要 mock。

vi.mock('@element-plus/icons-vue', () => ({
  WarningFilled: { name: 'WarningFilled', template: '<i class="warning-icon" />' },
  CircleCheck: { name: 'CircleCheck', template: '<i class="check-icon" />' },
}))

describe('StatusItemGrid', () => {
  const items = [
    { key: 'a', label: '过压保护', active: true },
    { key: 'b', label: '欠压保护', active: false },
  ]

  it('renders a grid-item for each item when items are non-empty', () => {
    const wrapper = mount(StatusItemGrid, { props: { items } })
    expect(wrapper.findAll('.grid-item')).toHaveLength(2)
    expect(wrapper.text()).toContain('过压保护')
    expect(wrapper.text()).toContain('欠压保护')
  })

  it('shows the active/inactive tag text from props', () => {
    const wrapper = mount(StatusItemGrid, {
      props: { items, activeText: '触发', inactiveText: '正常' },
    })
    const tags = wrapper.findAll('.el-tag')
    expect(tags[0].text()).toBe('触发')
    expect(tags[1].text()).toBe('正常')
  })

  it('图标颜色走语义 token（F17：不得再用静态 THEME_COLORS 十六进制常量）', () => {
    // 契约（规范 §3.6.2）：el-icon 的 :color 传 var(--color-*) 语义 token 字符串，
    // 由浏览器按当前主题解析；此前传 THEME_COLORS.danger/success 的亮色十六进制，
    // 暗色下不跟随主题（本仓"伪装成正常"家族）。
    const wrapper = mount(StatusItemGrid, { props: { items } })
    const icons = wrapper.findAll('.el-icon')
    expect(icons).toHaveLength(2)
    // active → danger，inactive → success
    expect(icons[0].attributes('style')).toContain('var(--color-danger)')
    expect(icons[1].attributes('style')).toContain('var(--color-success)')
    // 守卫：不得回退到硬编码色值
    for (const icon of icons) {
      expect(icon.attributes('style')).not.toMatch(/#[0-9a-fA-F]{3,8}|rgb\(/)
    }
  })

  it('applies the active class only to items with active=true', () => {
    const wrapper = mount(StatusItemGrid, { props: { items } })
    const gridItems = wrapper.findAll('.grid-item')
    expect(gridItems[0].classes()).toContain('active')
    expect(gridItems[1].classes()).not.toContain('active')
  })

  it('renders the built-in el-empty when items is empty and no summary slot', () => {
    const wrapper = mount(StatusItemGrid, {
      props: { items: [], emptyText: '无状态数据' },
    })
    expect(wrapper.findAll('.grid-item')).toHaveLength(0)
    expect(wrapper.find('.el-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('无状态数据')
  })

  it('renders the summary slot content when items are empty', () => {
    const SummaryChild = defineComponent({
      components: { StatusItemGrid },
      template: `
        <StatusItemGrid :items="[]">
          <template #summary>
            <div class="custom-summary">保护状态正常</div>
          </template>
        </StatusItemGrid>
      `,
    })
    const wrapper = mount(SummaryChild)
    expect(wrapper.find('.custom-summary').exists()).toBe(true)
    // Built-in el-empty should NOT appear when summary slot is provided
    expect(wrapper.find('.grid-empty').exists()).toBe(false)
  })

  it('renders the summary slot content even when it contains an el-empty fallback', () => {
    // This simulates BmsProtectionGrid's pattern: summary slot with
    // a conditional el-empty for the no-data case
    const SummaryWithFallback = defineComponent({
      components: { StatusItemGrid },
      props: ['hasData'],
      template: `
        <StatusItemGrid :items="[]">
          <template #summary>
            <div v-if="hasData" class="protection-summary">保护状态正常</div>
            <div v-else class="el-empty">无保护状态数据</div>
          </template>
        </StatusItemGrid>
      `,
    })
    const wrapper = mount(SummaryWithFallback, {
      props: { hasData: false },
    })
    // The el-empty fallback inside the summary slot should be visible
    expect(wrapper.find('.el-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('无保护状态数据')
  })

  it('does not render summary slot when items are non-empty', () => {
    const SummaryChild = defineComponent({
      components: { StatusItemGrid },
      template: `
        <StatusItemGrid :items="items">
          <template #summary>
            <div class="custom-summary">should not appear</div>
          </template>
        </StatusItemGrid>
      `,
      data: () => ({ items }),
    })
    const wrapper = mount(SummaryChild)
    expect(wrapper.find('.custom-summary').exists()).toBe(false)
    expect(wrapper.findAll('.grid-item')).toHaveLength(2)
  })
})
