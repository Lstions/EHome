import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import StatCard from '@/components/common/StatCard.vue'

describe('StatCard.vue', () => {
  it('exposes an accessible keyboard-operable control when clickable', async () => {
    const onClick = vi.fn()
    const wrapper = mount(StatCard, {
      props: { label: '本页边缘设备', iconColor: 'var(--el-color-primary)' },
      attrs: { onClick },
      slots: {
        icon: '<span>图标</span>',
        value: '<span class="stat-value">12</span>',
      },
    })

    const card = wrapper.get('.stat-card')
    expect(card.attributes('role')).toBe('button')
    expect(card.attributes('tabindex')).toBe('0')
    expect(card.attributes('aria-label')).toBe('查看本页边缘设备')

    await card.trigger('keydown.enter')
    await card.trigger('keydown.space')

    expect(onClick).toHaveBeenCalledTimes(2)
  })

  it('keeps a non-clickable statistic out of the keyboard tab order', () => {
    const wrapper = mount(StatCard, {
      props: { label: '模板总数', value: 3 },
    })

    const card = wrapper.get('.stat-card')
    expect(card.attributes('role')).toBeUndefined()
    expect(card.attributes('tabindex')).toBeUndefined()
    expect(card.attributes('aria-label')).toBeUndefined()
  })

  it('uses a concise visual label on mobile while preserving the full label for assistive technology', () => {
    const wrapper = mount(StatCard, {
      props: { label: '本页边缘设备', mobileLabel: '边缘设备' },
    })

    expect(wrapper.get('.stat-label-desktop').text()).toBe('本页边缘设备')
    expect(wrapper.get('.stat-label-mobile').text()).toBe('边缘设备')
    expect(wrapper.get('.stat-label-mobile').attributes('aria-hidden')).toBe('true')
  })
})
