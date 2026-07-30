import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import ActionConfirmationDialog from '../ActionConfirmationDialog.vue'
import type { ActionDefinition } from '@/api/deviceOperation'

function makeDefinition(overrides: Partial<ActionDefinition> = {}): ActionDefinition {
  return {
    id: 'test_action', version: 1, name: '测试操作', description: 'desc',
    device_type: 'prs3001', semantics: 'set', risk: 'high',
    transport: 'channel_cmd_v2', ...overrides,
  }
}

function mountDialog(props: Record<string, unknown> = {}) {
  return mount(ActionConfirmationDialog, {
    props: { visible: true, definition: makeDefinition(), ...props } as any,
  })
}

describe('ActionConfirmationDialog', () => {
  it('renders risk level and action name from definition', () => {
    const wrapper = mountDialog()
    expect(wrapper.text()).toContain('测试操作 将向设备发送参数变更')
    expect(wrapper.text()).toContain('风险等级：high')
  })

  it('disables confirm button when reason is empty or whitespace only', async () => {
    const wrapper = mountDialog()
    const confirmBtn = wrapper.findAll('button').find(button => button.text().includes('确认并排队'))!
    expect(confirmBtn.attributes('disabled')).toBeDefined()

    await wrapper.find('textarea').setValue('   ')
    await nextTick()
    expect(confirmBtn.attributes('disabled')).toBeDefined()
  })

  it('notifies parent with trimmed reason', async () => {
    const onConfirm = vi.fn()
    const wrapper = mountDialog({ onConfirm })
    await wrapper.find('textarea').setValue('  测试理由  ')
    await nextTick()
    const confirmBtn = wrapper.findAll('button').find(button => button.text().includes('确认并排队'))!
    expect(confirmBtn.attributes('disabled')).toBeUndefined()
    await confirmBtn.trigger('click')
    expect(onConfirm).toHaveBeenCalledWith('测试理由')
  })

  it('notifies parent when cancel closes the dialog', async () => {
    const onVisibleUpdate = vi.fn()
    const wrapper = mountDialog({ 'onUpdate:visible': onVisibleUpdate })
    const cancelBtn = wrapper.findAll('button').find(button => button.text().includes('取消'))!
    await cancelBtn.trigger('click')
    expect(onVisibleUpdate).toHaveBeenCalledWith(false)
  })

  it('resets reason when definition changes or dialog reopens', async () => {
    const wrapper = mountDialog()
    await wrapper.find('textarea').setValue('旧理由')
    await wrapper.setProps({ definition: makeDefinition({ id: 'other_action', name: '其他操作' }) })
    await nextTick()
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('')

    await wrapper.setProps({ visible: false })
    await wrapper.setProps({ visible: true })
    await nextTick()
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('')
  })

  it('shows fallback text when definition is null', () => {
    const wrapper = mountDialog({ definition: null })
    expect(wrapper.text()).toContain('该操作 将向设备发送参数变更')
    expect(wrapper.text()).toContain('风险等级：unknown')
  })
})
