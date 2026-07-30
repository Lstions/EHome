import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import EmptyState from '../EmptyState.vue'

// 全局 Element Plus stub 已在 src/test-setup.ts 注册，
// ElButton 渲染为 <button class="el-button ...">, ElIcon 渲染为 <span class="el-icon">

describe('EmptyState.vue', () => {
  it('renders default title when no props provided', () => {
    const wrapper = mount(EmptyState)
    expect(wrapper.find('.empty-title').text()).toBe('暂无数据')
  })

  it('renders custom title and description', () => {
    const wrapper = mount(EmptyState, {
      props: { title: 'No devices', description: 'Add one to get started' },
    })
    expect(wrapper.find('.empty-title').text()).toBe('No devices')
    expect(wrapper.find('.empty-description').text()).toBe('Add one to get started')
  })

  it('applies size modifier class', () => {
    const small = mount(EmptyState, { props: { size: 'small' } })
    expect(small.find('.empty-state.small').exists()).toBe(true)

    const large = mount(EmptyState, { props: { size: 'large' } })
    expect(large.find('.empty-state.large').exists()).toBe(true)
  })

  it('applies the scenario kind class for contextual empty states', () => {
    const wrapper = mount(EmptyState, { props: { kind: 'filtered' } })
    expect(wrapper.find('.empty-state.filtered').exists()).toBe(true)
  })

  it('renders quick action buttons when provided', () => {
    const actions = [
      { label: '刷新', handler: () => {} },
      { label: '新建', type: 'primary' as const, handler: () => {} },
    ]
    const wrapper = mount(EmptyState, {
      props: { quickActions: actions },
    })
    const buttons = wrapper.findAll('.el-button')
    expect(buttons).toHaveLength(2)
    expect(buttons[0].text()).toBe('刷新')
    expect(buttons[1].text()).toBe('新建')
  })

  it('does not render action slot area when no slot or quickActions', () => {
    const wrapper = mount(EmptyState)
    expect(wrapper.find('.empty-action').exists()).toBe(false)
    expect(wrapper.find('.empty-quick-actions').exists()).toBe(false)
  })
})
