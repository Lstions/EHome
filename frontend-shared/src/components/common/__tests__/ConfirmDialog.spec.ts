import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ConfirmDialog from '../ConfirmDialog.vue'

const stubs = {
  'el-dialog': {
    props: ['modelValue', 'title', 'width'],
    template: `<div v-if="modelValue" data-testid="dialog"><div data-testid="title">{{ title }}</div><slot /><slot name="footer" /></div>`,
  },
  'el-button': {
    props: ['type'],
    template: '<button data-testid="btn" :data-type="type"><slot /></button>',
  },
}

describe('ConfirmDialog.vue', () => {
  it('shows dialog when visible=true', () => {
    const wrapper = mount(ConfirmDialog, {
      props: { visible: true, title: '确认删除?', message: '此操作不可恢复' },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="dialog"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="title"]').text()).toBe('确认删除?')
    expect(wrapper.text()).toContain('此操作不可恢复')
  })

  it('hides dialog when visible=false', () => {
    const wrapper = mount(ConfirmDialog, {
      props: { visible: false, title: 'test', message: 'msg' },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="dialog"]').exists()).toBe(false)
  })

  it('emits confirm event when confirm button clicked', async () => {
    const wrapper = mount(ConfirmDialog, {
      props: { visible: true, title: 't', message: 'm' },
      global: { stubs },
    })
    const buttons = wrapper.findAll('[data-testid="btn"]')
    // Second button is the primary/confirm
    await buttons[1].trigger('click')
    expect(wrapper.emitted('confirm')).toBeTruthy()
  })

  it('emits cancel event when cancel button clicked', async () => {
    const wrapper = mount(ConfirmDialog, {
      props: { visible: true, title: 't', message: 'm' },
      global: { stubs },
    })
    const buttons = wrapper.findAll('[data-testid="btn"]')
    // First button is cancel
    await buttons[0].trigger('click')
    expect(wrapper.emitted('cancel')).toBeTruthy()
  })
})
