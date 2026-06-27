import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import StatusBadge from '../StatusBadge.vue'

const elStubs = {
  'el-tag': {
    props: ['type', 'size', 'effect'],
    template: '<span data-testid="tag" :data-type="type" :data-size="size" :data-effect="effect"><slot /></span>',
  },
}

describe('StatusBadge.vue', () => {
  it('maps online status to success tag and Chinese text', () => {
    const wrapper = mount(StatusBadge, {
      props: { status: 'online' as any },
      global: { stubs: elStubs },
    })
    expect(wrapper.find('[data-testid="tag"]').attributes('data-type')).toBe('success')
    expect(wrapper.text()).toContain('在线')
  })

  it('maps error status to danger tag and fault text', () => {
    const wrapper = mount(StatusBadge, {
      props: { status: 'error' as any, effect: 'dark' },
      global: { stubs: elStubs },
    })
    const tag = wrapper.find('[data-testid="tag"]')
    expect(tag.attributes('data-type')).toBe('danger')
    expect(tag.attributes('data-effect')).toBe('dark')
    expect(wrapper.text()).toContain('故障')
  })

  it('falls back to raw unknown custom status', () => {
    const wrapper = mount(StatusBadge, {
      props: { status: 'custom-state' as any },
      global: { stubs: elStubs },
    })
    expect(wrapper.text()).toContain('custom-state')
  })
})
