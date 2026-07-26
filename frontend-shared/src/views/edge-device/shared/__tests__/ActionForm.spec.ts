import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import ActionForm from '../ActionForm.vue'
import type { ActionDefinition } from '@/api/deviceOperation'

const stubs = {
  'el-dialog': { template: '<div class="el-dialog-stub" v-if="modelValue"><slot /><slot name="footer" /></div>', props: ['modelValue', 'title', 'width', 'closeOnClickModal'], emits: ['update:model-value'] },
  'el-alert': { template: '<div class="el-alert-stub" :title="title" />', props: ['type', 'closable', 'title'] },
  'el-form': { template: '<form><slot /></form>', props: ['labelPosition'] },
  'el-form-item': { template: '<div class="form-item" :data-label="label"><slot /></div>', props: ['label', 'required'] },
  'el-switch': { template: '<input type="checkbox" :checked="modelValue" @change="$emit(\'update:modelValue\', $event.target.checked)" />', props: ['modelValue'], emits: ['update:modelValue'] },
  'el-select': { template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><slot /></select>', props: ['modelValue'], emits: ['update:modelValue'] },
  'el-option': { template: '<option :value="value">{{ label }}</option>', props: ['label', 'value'] },
  'el-input-number': { template: '<input type="number" :value="modelValue" @input="$emit(\'update:modelValue\', Number($event.target.value))" :min="min" :max="max" />', props: ['modelValue', 'min', 'max', 'precision'], emits: ['update:modelValue'] },
  'el-input': { template: '<input type="text" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" :maxlength="maxlength" />', props: ['modelValue', 'maxlength'], emits: ['update:modelValue'] },
  'el-button': { template: '<button :disabled="disabled" :class="type" @click="$emit(\'click\')"><slot /></button>', props: ['type', 'disabled'], emits: ['click'] },
}

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
    props: { visible: true, definition: makeDefinition(), ...props },
    global: { stubs },
  })
}

describe('ActionForm', () => {
  it('renders fields sorted alphabetically from input_schema', () => {
    const wrapper = mountForm()
    const items = wrapper.findAll('.form-item')
    expect(items.length).toBe(3)
    // Sorted: enabled, mode, threshold
    expect(items[0].attributes('data-label')).toBe('enabled')
    expect(items[1].attributes('data-label')).toBe('mode')
    expect(items[2].attributes('data-label')).toBe('threshold')
  })

  it('renders enum as select with options', () => {
    const wrapper = mountForm()
    const select = wrapper.find('select')
    expect(select.exists()).toBe(true)
    const options = select.findAll('option')
    expect(options.length).toBe(2)
    expect(options[0].text()).toBe('auto')
    expect(options[1].text()).toBe('manual')
  })

  it('renders boolean as switch (checkbox)', () => {
    const wrapper = mountForm()
    const checkbox = wrapper.find('input[type="checkbox"]')
    expect(checkbox.exists()).toBe(true)
  })

  it('renders integer as number input with min/max', () => {
    const wrapper = mountForm()
    const numberInput = wrapper.find('input[type="number"]')
    expect(numberInput.exists()).toBe(true)
    expect(numberInput.attributes('min')).toBe('0')
    expect(numberInput.attributes('max')).toBe('100')
  })

  it('disables submit when required fields are incomplete', () => {
    const wrapper = mountForm()
    const submitBtn = wrapper.findAll('button').find(b => b.text().includes('继续'))
    expect(submitBtn).toBeTruthy()
    expect(submitBtn!.attributes('disabled')).toBeDefined()
  })

  it('enables submit when all required fields are filled', async () => {
    const wrapper = mountForm()
    // Fill mode (select)
    const select = wrapper.find('select')
    await select.setValue('auto')
    // Fill threshold (number)
    const numberInput = wrapper.find('input[type="number"]')
    await numberInput.setValue(50)
    await nextTick()
    const submitBtn = wrapper.findAll('button').find(b => b.text().includes('继续'))
    expect(submitBtn!.attributes('disabled')).toBeUndefined()
  })

  it('emits submit with collected params', async () => {
    const wrapper = mountForm()
    await wrapper.find('select').setValue('manual')
    await wrapper.find('input[type="number"]').setValue(75)
    await nextTick()
    const submitBtn = wrapper.findAll('button').find(b => b.text().includes('继续'))
    await submitBtn!.trigger('click')
    expect(wrapper.emitted('submit')).toBeTruthy()
    const params = wrapper.emitted('submit')![0][0] as Record<string, unknown>
    expect(params.mode).toBe('manual')
    expect(params.threshold).toBe(75)
  })

  it('shows unsupported alert and disables submit for unsupported types', () => {
    // The component considers enum on non-string as unsupported
    const wrapper = mountForm({
      definition: makeDefinition({
        input_schema: {
          properties: { level: { type: 'integer', enum: ['1', '2', '3'] } as any },
          required: ['level'],
        },
      }),
    })
    expect(wrapper.find('.el-alert-stub').exists()).toBe(true)
    const submitBtn = wrapper.findAll('button').find(b => b.text().includes('继续'))
    expect(submitBtn!.attributes('disabled')).toBeDefined()
  })

  it('emits update:visible false on cancel', async () => {
    const wrapper = mountForm()
    const cancelBtn = wrapper.findAll('button').find(b => b.text().includes('取消'))
    await cancelBtn!.trigger('click')
    expect(wrapper.emitted('update:visible')).toBeTruthy()
    expect(wrapper.emitted('update:visible')![0]).toEqual([false])
  })

  it('resets values when definition changes', async () => {
    const wrapper = mountForm()
    await wrapper.find('select').setValue('auto')
    await wrapper.find('input[type="number"]').setValue(50)
    await nextTick()
    await wrapper.setProps({ definition: makeDefinition({ id: 'other_action' }) })
    await nextTick()
    // After reset, submit should be disabled again (required fields empty)
    const submitBtn = wrapper.findAll('button').find(b => b.text().includes('继续'))
    expect(submitBtn!.attributes('disabled')).toBeDefined()
  })

  it('initializes required boolean fields to false on reset', async () => {
    const wrapper = mountForm({
      definition: makeDefinition({
        input_schema: {
          properties: { active: { type: 'boolean' } },
          required: ['active'],
        },
      }),
    })
    await nextTick()
    // Required boolean should be initialized to false → complete is true
    const submitBtn = wrapper.findAll('button').find(b => b.text().includes('继续'))
    expect(submitBtn!.attributes('disabled')).toBeUndefined()
  })

  it('handles null definition gracefully', () => {
    const wrapper = mountForm({ definition: null })
    const dialog = wrapper.find('.el-dialog-stub')
    expect(dialog.exists()).toBe(true)
    // No fields render with null definition
    expect(wrapper.findAll('.form-item').length).toBe(0)
    // No unsupported alert
    expect(wrapper.find('.el-alert-stub').exists()).toBe(false)
  })
})
