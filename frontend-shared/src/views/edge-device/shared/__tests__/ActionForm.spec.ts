import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import ActionForm from '../ActionForm.vue'
import type { ActionDefinition } from '@/api/deviceOperation'

function makeDefinition(overrides: Partial<ActionDefinition> = {}): ActionDefinition {
  return {
    id: 'form_action', version: 1, name: '表单操作', description: 'desc',
    device_type: 'prs3001', semantics: 'set', risk: 'medium',
    transport: 'channel_cmd_v2',
    input_schema: {
      properties: {
        mode: { type: 'string', enum: ['auto', 'manual'] },
        threshold: { type: 'integer', minimum: 0, maximum: 100 },
        enabled: { type: 'boolean' },
      },
      required: ['mode', 'threshold'],
    },
    ...overrides,
  }
}

function mountForm(props: Record<string, unknown> = {}) {
  return mount(ActionForm, {
    props: { visible: true, definition: makeDefinition(), ...props } as any,
  })
}

function submitButton(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAll('button').find(button => button.text().includes('继续'))!
}

describe('ActionForm', () => {
  it('renders fields sorted alphabetically from input_schema', () => {
    const wrapper = mountForm()
    const labels = wrapper.findAll('.el-form-item__label').map(label => label.text())
    expect(labels).toEqual(['enabled', 'mode', 'threshold'])
  })

  it('renders enum, boolean, and integer editors with constraints', () => {
    const wrapper = mountForm()
    const selects = wrapper.findAll('select.el-select')
    expect(selects).toHaveLength(1)
    expect(selects[0].findAll('option')).toHaveLength(3) // placeholder + 2 enum choices
    // 当前全局 ElSwitch 是 button，实现合同由 class 和 model binding 确认。
    expect(wrapper.find('.el-switch').exists()).toBe(true)
    const numberInput = wrapper.find('input[type="number"]')
    expect(numberInput.attributes('min')).toBe('0')
    expect(numberInput.attributes('max')).toBe('100')
  })

  it('disables submit until required fields are complete', async () => {
    const wrapper = mountForm()
    expect(submitButton(wrapper).attributes('disabled')).toBeDefined()

    await wrapper.find('select.el-select').setValue('auto')
    await wrapper.find('input[type="number"]').setValue(50)
    await nextTick()
    expect(submitButton(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('notifies parent with collected parameters', async () => {
    const onSubmit = vi.fn()
    const wrapper = mountForm({ onSubmit })
    await wrapper.find('select.el-select').setValue('manual')
    await wrapper.find('input[type="number"]').setValue(75)
    await nextTick()
    await submitButton(wrapper).trigger('click')
    expect(onSubmit).toHaveBeenCalledWith({ mode: 'manual', threshold: 75 })
  })

  it('shows unsupported alert and disables submit for unsupported types', () => {
    const wrapper = mountForm({
      definition: makeDefinition({
        input_schema: {
          properties: { level: { type: 'integer', enum: ['1', '2', '3'] } as any },
          required: ['level'],
        },
      }),
    })
    expect(wrapper.text()).toContain('此动作的参数类型尚未获得客户端支持')
    expect(submitButton(wrapper).attributes('disabled')).toBeDefined()
  })

  it('notifies parent when cancel closes the dialog', async () => {
    const onVisibleUpdate = vi.fn()
    const wrapper = mountForm({ 'onUpdate:visible': onVisibleUpdate })
    const cancelButton = wrapper.findAll('button').find(button => button.text().includes('取消'))!
    await cancelButton.trigger('click')
    expect(onVisibleUpdate).toHaveBeenCalledWith(false)
  })

  it('resets values when definition changes and initializes required booleans', async () => {
    const wrapper = mountForm()
    await wrapper.find('select.el-select').setValue('auto')
    await wrapper.find('input[type="number"]').setValue(50)
    await wrapper.setProps({ definition: makeDefinition({ id: 'other_action' }) })
    await nextTick()
    expect(submitButton(wrapper).attributes('disabled')).toBeDefined()

    await wrapper.setProps({
      definition: makeDefinition({
        id: 'boolean_action',
        input_schema: { properties: { active: { type: 'boolean' } }, required: ['active'] },
      }),
    })
    await nextTick()
    expect(submitButton(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('handles null definition gracefully', () => {
    const wrapper = mountForm({ definition: null })
    expect(wrapper.findAll('.el-form-item')).toHaveLength(0)
    expect(wrapper.text()).not.toContain('此动作的参数类型尚未获得客户端支持')
  })
})
