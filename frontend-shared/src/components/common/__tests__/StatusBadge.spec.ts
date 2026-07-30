import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import StatusBadge from '../StatusBadge.vue'

// 全局 Element Plus stub 已在 src/test-setup.ts 注册，
// 不再需要每个测试文件自行 stub el-tag。
// 全局 stub 的 ElTag 渲染为 <span class="el-tag" :data-type="type" :data-effect="effect">

describe('StatusBadge.vue', () => {
  it('maps online status to success tag and Chinese text', () => {
    const wrapper = mount(StatusBadge, {
      props: { status: 'online' as any },
    })
    const tag = wrapper.find('.el-tag')
    expect(tag.attributes('data-type')).toBe('success')
    expect(wrapper.text()).toContain('在线')
  })

  it('maps error status to danger tag and fault text', () => {
    const wrapper = mount(StatusBadge, {
      props: { status: 'error' as any, effect: 'dark' },
    })
    const tag = wrapper.find('.el-tag')
    expect(tag.attributes('data-type')).toBe('danger')
    expect(tag.attributes('data-effect')).toBe('dark')
    expect(wrapper.text()).toContain('故障')
  })

  it('falls back to raw unknown custom status', () => {
    const wrapper = mount(StatusBadge, {
      props: { status: 'custom-state' as any },
    })
    expect(wrapper.text()).toContain('custom-state')
  })
})
