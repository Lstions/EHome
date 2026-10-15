import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ActionConfirmationDialog from '../ActionConfirmationDialog.vue'
import type { ActionDefinition } from '@/api/deviceOperation'

function makeDefinition(overrides: Partial<ActionDefinition> = {}): ActionDefinition {
  return {
    id: 'test_action', version: 1, name: '测试操作', description: 'desc',
    device_type: 'prs3001', semantics: 'set', risk: 'high',
    risk_level: 'elevated', requires_confirmation: true,
    transport: 'channel_cmd_v2', ...overrides,
  }
}

function mountDialog(props: Record<string, unknown> = {}) {
  return mount(ActionConfirmationDialog, {
    props: { visible: true, definition: makeDefinition(), ...props } as any,
  })
}

describe('ActionConfirmationDialog', () => {
  // 2026-10-15 用户裁定：弹窗只保留「确认 / 取消」，不再有必填的操作理由。
  it('只渲染确认与取消两个按钮', () => {
    const wrapper = mountDialog()
    const labels = wrapper.findAll('button').map(button => button.text())
    expect(labels).toEqual(['取消', '确认'])
  })

  it('不渲染操作理由输入框（已移除该过度设计）', () => {
    const wrapper = mountDialog()
    expect(wrapper.find('textarea').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('操作理由')
  })

  it('展示操作名，并在风险操作上给出风险提示', () => {
    const wrapper = mountDialog()
    expect(wrapper.text()).toContain('测试操作 将向设备发送指令')
    expect(wrapper.text()).toContain('该操作为风险操作')
  })

  it('普通操作不显示风险提示，按钮用主色', () => {
    const wrapper = mountDialog({ definition: makeDefinition({ risk_level: 'normal' }) })
    expect(wrapper.text()).not.toContain('风险操作')
    const confirmBtn = wrapper.findAll('button').find(button => button.text() === '确认')!
    expect(confirmBtn.classes().join(' ')).toContain('el-button--primary')
  })

  it('确认按钮无需任何输入即可点击并通知父组件', async () => {
    const onConfirm = vi.fn()
    const wrapper = mountDialog({ onConfirm })
    const confirmBtn = wrapper.findAll('button').find(button => button.text() === '确认')!
    expect(confirmBtn.attributes('disabled')).toBeUndefined()
    await confirmBtn.trigger('click')
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('取消关闭弹窗', async () => {
    const onVisibleUpdate = vi.fn()
    const wrapper = mountDialog({ 'onUpdate:visible': onVisibleUpdate })
    const cancelBtn = wrapper.findAll('button').find(button => button.text() === '取消')!
    await cancelBtn.trigger('click')
    expect(onVisibleUpdate).toHaveBeenCalledWith(false)
  })

  it('definition 为 null 时不崩且给出兜底文案', () => {
    const wrapper = mountDialog({ definition: null })
    expect(wrapper.text()).toContain('该操作 将向设备发送指令')
  })
})
