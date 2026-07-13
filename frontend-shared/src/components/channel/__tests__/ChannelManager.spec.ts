import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ChannelManager from '../ChannelManager.vue'

vi.mock('@/api/channel', () => ({
  channelApi: { create: vi.fn(), update: vi.fn() },
}))
vi.mock('@/api/node', () => ({
  nodeApi: { scanI2C: vi.fn(), syncConfig: vi.fn() },
}))

const stubs = {
  'el-dialog': { template: '<div class="el-dialog"><slot /><slot name="footer" /></div>' },
  'el-form': { template: '<form><slot /></form>' },
  'el-form-item': { template: '<section><slot /></section>' },
  'el-select': { template: '<div class="el-select"><slot /></div>' },
  'el-option': { template: '<option />' },
  'el-input': { template: '<input />' },
  'el-input-number': { template: '<input type="number" />' },
  'el-radio-group': { template: '<div><slot /></div>' },
  'el-radio-button': { template: '<button><slot /></button>' },
  'el-switch': { template: '<input type="checkbox" />' },
  'el-button': { template: '<button><slot /></button>' },
  'el-icon': { template: '<i><slot /></i>' },
}

describe('ChannelManager', () => {
  it('keeps UART parameters editable when capability data is temporarily unavailable', async () => {
    const wrapper = mount(ChannelManager, {
      props: {
        modelValue: false,
        collectorId: 'node-1',
        initialData: {
          id: 7,
          hardware_type: 'uart',
          hardware_id: 'UART1',
          name: '现场串口',
          enabled: true,
          config: { baud_rate: 9600, data_bits: 8, stop_bits: 1, parity: 'none' },
        },
      },
      global: { stubs },
    })

    await wrapper.setProps({ modelValue: true })

    expect(wrapper.findAll('input[type="number"]').length).toBeGreaterThan(0)
    expect(wrapper.findAll('button').length).toBeGreaterThanOrEqual(3)
  })
})
