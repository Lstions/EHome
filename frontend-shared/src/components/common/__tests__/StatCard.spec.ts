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

  it('hides the mobile label on desktop by default (no duplicate text)', async () => {
    const wrapper = mount(StatCard, {
      props: { label: '本页边缘设备', mobileLabel: '边缘设备' },
    })

    // Both label spans exist in the DOM for responsive switching
    const desktopSpan = wrapper.find('.stat-label-desktop')
    const mobileSpan = wrapper.find('.stat-label-mobile')
    expect(desktopSpan.exists()).toBe(true)
    expect(mobileSpan.exists()).toBe(true)
    expect(mobileSpan.attributes('aria-hidden')).toBe('true')

    // jsdom does not inject scoped <style> into the DOM, so read the
    // SFC source directly to confirm the desktop-default hiding rule
    // exists outside of any @media block.
    const fs = await import('node:fs')
    const path = await import('node:path')
    const sfcPath = path.resolve(__dirname, '../StatCard.vue')
    const sfcSource = fs.readFileSync(sfcPath, 'utf-8')
    // Extract the <style> block content (before closing tag)
    const styleMatch = sfcSource.match(/<style[^>]*>([\s\S]*?)<\/style>/)
    expect(styleMatch).not.toBeNull()
    const css = styleMatch![1]
    // Desktop default: .stat-label-mobile { display: none } must appear
    // before the @media block (i.e. at top level, not inside @media)
    const mediaIdx = css.indexOf('@media')
    const ruleIdx = css.indexOf('.stat-label-mobile')
    expect(ruleIdx).toBeGreaterThan(-1)
    // The rule must appear before the @media block to be a desktop default
    expect(ruleIdx).toBeLessThan(mediaIdx)
    // And must contain display: none
    const ruleEnd = css.indexOf('}', ruleIdx)
    const ruleBlock = css.slice(ruleIdx, ruleEnd)
    expect(ruleBlock).toContain('display: none')
  })
})
