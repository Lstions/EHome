import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import ActionConfirmationDialog from '../ActionConfirmationDialog.vue'
import type { ActionDefinition } from '@/api/deviceOperation'

const stubs = {
  'el-dialog': { template: '<div class="el-dialog-stub" v-if="modelValue"><slot /><slot name="footer" /></div>', props: ['modelValue', 'title', 'width', 'closeOnClickModal'], emits: ['update:model-value'] },
  'el-alert': { template: '<div class="el-alert-stub" :title="title" />', props: ['type', 'closable', 'title'] },
  'el-form': { template: '<form><slot /></form>', props: ['labelPosition'] },
  'el-form-item': { template: '<div class="form-item"><slot /></div>', props: ['label', 'required'] },
  'el-input': { template: '<textarea :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" :maxlength="maxlength" :placeholder="placeholder" />', props: ['modelValue', 'type', 'rows', 'maxlength', 'showWordLimit', 'placeholder'], emits: ['update:modelValue'] },
  'el-button': { template: '<button :disabled="disabled" :class="type" @click="$emit(\'click\')"><slot /></button>', props: ['type', 'disabled'], emits: ['click'] },
}

function makeDefinition(overrides: Partial<ActionDefinition> = {}): ActionDefinition {
  return {
    id: 'test_action', version: 1, name: '测试操作', description: 'desc',
    device_type: 'prs3001', semantics: 'set', risk: 'high',
    transport: 'channel_cmd_v2', ...overrides,
  }
}

function mountDialog(props: Record<string, unknown> = {}) {
  return mount(ActionConfirmationDialog, {
    props: { visible: true, definition: makeDefinition(), ...props },
    global: { stubs },
  })
}

describe('ActionConfirmationDialog', () => {
  it('renders risk level and action name from definition', () => {
    const wrapper = mountDialog()
    // Action name is in the el-alert title prop
    const alert = wrapper.find('.el-alert-stub')
    expect(alert.attributes('title')).toContain('测试操作')
    // Risk level is in the paragraph text
    expect(wrapper.text()).toContain('high')
  })

  it('disables confirm button when reason is empty', () => {
    const wrapper = mountDialog()
    const buttons = wrapper.findAll('button')
    const confirmBtn = buttons.find(b => b.text().includes('确认并排队'))
    expect(confirmBtn).toBeTruthy()
    expect(confirmBtn!.attributes('disabled')).toBeDefined()
  })

  it('disables confirm button when reason is whitespace only', async () => {
    const wrapper = mountDialog()
    const textarea = wrapper.find('textarea')
    await textarea.setValue('   ')
    await nextTick()
    const confirmBtn = wrapper.findAll('button').find(b => b.text().includes('确认并排队'))
    expect(confirmBtn!.attributes('disabled')).toBeDefined()
  })

  it('enables confirm button when reason has content', async () => {
    const wrapper = mountDialog()
    const textarea = wrapper.find('textarea')
    await textarea.setValue('现场测试需要')
    await nextTick()
    const confirmBtn = wrapper.findAll('button').find(b => b.text().includes('确认并排队'))
    expect(confirmBtn!.attributes('disabled')).toBeUndefined()
  })

  it('emits confirm with trimmed reason', async () => {
    const wrapper = mountDialog()
    await wrapper.find('textarea').setValue('  测试理由  ')
    await nextTick()
    const confirmBtn = wrapper.findAll('button').find(b => b.text().includes('确认并排队'))
    await confirmBtn!.trigger('click')
    expect(wrapper.emitted('confirm')).toBeTruthy()
    expect(wrapper.emitted('confirm')![0]).toEqual(['测试理由'])
  })

  it('emits update:visible false on cancel', async () => {
    const wrapper = mountDialog()
    const cancelBtn = wrapper.findAll('button').find(b => b.text().includes('取消'))
    await cancelBtn!.trigger('click')
    expect(wrapper.emitted('update:visible')).toBeTruthy()
    expect(wrapper.emitted('update:visible')![0]).toEqual([false])
  })

  it('resets reason when definition changes', async () => {
    const wrapper = mountDialog()
    await wrapper.find('textarea').setValue('旧理由')
    await nextTick()
    await wrapper.setProps({ definition: makeDefinition({ id: 'other_action', name: '其他操作' }) })
    await nextTick()
    const textarea = wrapper.find('textarea')
    expect((textarea.element as HTMLTextAreaElement).value).toBe('')
  })

  it('resets reason when dialog reopens', async () => {
    const wrapper = mountDialog({ visible: false })
    await wrapper.setProps({ visible: true })
    await nextTick()
    const textarea = wrapper.find('textarea')
    expect((textarea.element as HTMLTextAreaElement).value).toBe('')
  })

  it('shows fallback text when definition is null', () => {
    const wrapper = mountDialog({ definition: null })
    // el-alert title is a prop; check the alert stub's title attribute
    const alert = wrapper.find('.el-alert-stub')
    expect(alert.attributes('title')).toContain('该操作')
    expect(wrapper.text()).toContain('unknown')
  })
})
