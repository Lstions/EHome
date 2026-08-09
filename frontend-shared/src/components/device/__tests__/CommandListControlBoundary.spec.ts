import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

const mocks = vi.hoisted(() => ({
  getCommandIntervals: vi.fn(),
  updateCommandIntervals: vi.fn(),
}))

vi.mock('@/api/edgeDevice', () => ({ edgeDeviceApi: mocks }))
vi.mock('element-plus', () => ({ ElMessage: { success: vi.fn(), error: vi.fn() } }))

import CommandList from '../CommandList.vue'

const stubs = {
  'el-empty': defineComponent({
    props: ['description'],
    template: '<div class="empty-state">{{ description }}</div>',
  }),
  'el-tag': defineComponent({ template: '<span><slot /></span>' }),
  'el-input-number': defineComponent({ template: '<input />' }),
  'el-switch': defineComponent({ template: '<button />' }),
  'el-button': defineComponent({ template: '<button><slot /></button>' }),
}

const command = (schedulable: boolean) => ({
  id: schedulable ? 'scheduled-read' : 'one-shot-read',
  name: schedulable ? '定时读取' : '读取累计雨量',
  type: 'read',
  cmd_byte: 3,
  write_data: '',
  read_length: 7,
  delay_ms: 0,
  interval_ms: 5000,
  current_interval_ms: schedulable ? 5000 : 0,
  schedulable,
  description: 'read command',
})

beforeEach(() => {
  vi.clearAllMocks()
  mocks.updateCommandIntervals.mockResolvedValue(undefined)
})

describe('CommandList control boundary', () => {
  it('does not present non-schedulable templates as a second trigger path', async () => {
    mocks.getCommandIntervals.mockResolvedValue([command(false)])
    const wrapper = mount(CommandList, {
      props: { deviceId: 1 },
      global: { stubs },
    })
    await flushPromises()

    expect(wrapper.text()).not.toContain('触发指令')
    expect(wrapper.text()).not.toContain('手动触发')
    expect(wrapper.text()).not.toContain('读取累计雨量')
    expect(wrapper.text()).toContain('一次性读取请使用下方受控操作')
  })

  it('continues to render schedulable commands as polling configuration', async () => {
    mocks.getCommandIntervals.mockResolvedValue([command(true), command(false)])
    const wrapper = mount(CommandList, {
      props: { deviceId: 1 },
      global: { stubs },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('轮询指令')
    expect(wrapper.text()).toContain('定时读取')
    expect(wrapper.text()).not.toContain('读取累计雨量')
  })
})
