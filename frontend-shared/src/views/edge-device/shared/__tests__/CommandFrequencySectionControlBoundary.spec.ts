import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

const mocks = vi.hoisted(() => ({
  getCommandIntervals: vi.fn(),
  updateCommandIntervals: vi.fn(),
  getNodeDetail: vi.fn(),
}))

vi.mock('@/api/edgeDevice', () => ({ edgeDeviceApi: mocks }))
vi.mock('@/api/node', () => ({ nodeApi: { getDetail: mocks.getNodeDetail } }))
vi.mock('element-plus', () => ({ ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))

import CommandFrequencySection from '../CommandFrequencySection.vue'

/**
 * 控制边界契约（原 CommandListControlBoundary.spec 迁移到共享组件）：
 * 非 schedulable 的指令是「一次性读取」，只能在下方「受控操作」里触发；
 * 不得在轮询配置里再出现第二个触发路径（否则同一动作两处入口、语义混乱）。
 */
const stubs = {
  'el-empty': defineComponent({
    props: ['description', 'title', 'kind', 'size'],
    template: '<div class="empty-state">{{ title }}{{ description }}</div>',
  }),
  'el-tag': defineComponent({ template: '<span class="el-tag"><slot /></span>' }),
  'el-input-number': defineComponent({ template: '<input />' }),
  'el-switch': defineComponent({ template: '<button />' }),
  'el-button': defineComponent({ template: '<button><slot /></button>' }),
  'el-alert': defineComponent({ template: '<div class="el-alert"><slot name="title" /><slot /></div>' }),
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

describe('指令频率共享组件 control boundary', () => {
  it('does not present non-schedulable templates as a second trigger path', async () => {
    mocks.getCommandIntervals.mockResolvedValue([command(false)])
    const wrapper = mount(CommandFrequencySection, {
      props: { deviceId: 1 },
      global: { stubs },
    })
    await flushPromises()

    expect(wrapper.text()).not.toContain('触发指令')
    expect(wrapper.text()).not.toContain('手动触发')
    expect(wrapper.text()).not.toContain('读取累计雨量')
    expect(wrapper.text()).toContain('一次性读取请使用下方受控操作')
    wrapper.unmount()
  })

  it('continues to render schedulable commands as polling configuration', async () => {
    mocks.getCommandIntervals.mockResolvedValue([command(true), command(false)])
    const wrapper = mount(CommandFrequencySection, {
      props: { deviceId: 1 },
      global: { stubs },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('轮询指令')
    expect(wrapper.text()).toContain('定时读取')
    expect(wrapper.text()).not.toContain('读取累计雨量')
    wrapper.unmount()
  })
})
