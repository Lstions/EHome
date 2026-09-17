import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'

// 端到端回归（raw API payload → normalize() → 删除对话框「通道」栏）。
//
// 为什么必须单独有这个文件：DeviceDeleteDialogChannel.spec.ts 直接构造 EdgeDevice
// fixture（显式写 channel_hardware_id），**测不到 normalize()**。若 normalize() 退回
// 旧写法（丢弃 channel / 不产出 channel_hardware_id），那个文件的用例仍会全绿 —— 假绿。
// 这里喂**后端真实形状**的 payload（device.hardware_id="1" + channel.hardware_id="UART0"），
// 走真实 normalize()，再断言对话框显示 UART0。

const mockClient = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  delete: vi.fn(),
  defaults: { baseURL: '' },
}))

vi.mock('@/api/client', () => ({ default: mockClient }))

import { edgeDeviceApi } from '@/api/edgeDevice'
import DeviceDeleteDialog from '@/components/device/DeviceDeleteDialog.vue'

// 生产实测（2026-09-17）：测试雨量计挂在 UART0，设备级 hardware_id 是 Modbus 从站地址 "1"。
const rawRainGauge = {
  id: 7,
  node_id: 'F0F5BDFFFE02',
  channel_id: 3,
  name: '测试雨量计',
  type: 'sn3001_rain',
  protocol: 'modbus',
  hardware_id: '1',
  channel: { id: 3, node_id: 'F0F5BDFFFE02', hardware_type: 'UART', hardware_id: 'UART0', bus_type: 'UART' },
  status: 'active',
  created_at: '2026-09-17T00:00:00Z',
}

function channelCellText(wrapper: ReturnType<typeof mount>): string {
  const row = wrapper.findAll('.fact-row').find(r => r.find('.fact-label').text() === '通道')
  expect(row, '未找到「通道」行 —— 选择器失效，断言将形同虚设').toBeTruthy()
  return row!.find('.fact-value').text()
}

describe('normalize() → 删除对话框「通道」栏 端到端回归', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockClient.get.mockImplementation((url: string) => {
      if (String(url).includes('/logical-device-info')) {
        return Promise.resolve({
          data: { edge_device_id: 7, name: null, logical_device_id: null, retention_days: null, instance_count: 1 },
        })
      }
      return Promise.resolve({ data: rawRainGauge })
    })
  })

  it('normalize() 保留通道总线名，且不改变 hardware_id（设备地址）的语义', async () => {
    const device = await edgeDeviceApi.getDetail(7)

    // ① hardware_id 仍是 Modbus 从站地址 —— 语义不得被"修复"篡改（其它调用方依赖它）
    expect(device.hardware_id).toBe('1')
    // ② 总线名被推送式保留下来（旧实现里 channel 被整体丢弃 → 这里会是 undefined）
    expect(device.channel_hardware_id).toBe('UART0')
    expect(device.channel?.hardware_id).toBe('UART0')
    expect(device.channel?.hardware_type).toBe('UART')
  })

  it('对话框据此显示 UART0，而不是误导性的 "UART 1"', async () => {
    const device = await edgeDeviceApi.getDetail(7)
    const wrapper = mount(DeviceDeleteDialog, { props: { visible: true, device, submitting: false } })

    expect(channelCellText(wrapper)).toBe('UART0')
    expect(wrapper.text()).not.toContain('UART 1')
  })

  it('列表接口同样保留通道总线名（紧凑列表路径）', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [rawRainGauge], total: 1 } })
    const res = await edgeDeviceApi.getList()

    expect(res.items[0].channel_hardware_id).toBe('UART0')
    expect(res.items[0].hardware_id).toBe('1')
  })

  it('兼容性：老数据没有 channel 时 normalize() 不产出总线名，对话框回退为 —', async () => {
    const { channel, ...legacy } = rawRainGauge
    mockClient.get.mockImplementation((url: string) => {
      if (String(url).includes('/logical-device-info')) {
        return Promise.resolve({
          data: { edge_device_id: 7, name: null, logical_device_id: null, retention_days: null, instance_count: 1 },
        })
      }
      return Promise.resolve({ data: legacy })
    })

    const device = await edgeDeviceApi.getDetail(7)
    expect(device.channel_hardware_id).toBeUndefined()
    expect(device.channel).toBeUndefined()

    const wrapper = mount(DeviceDeleteDialog, { props: { visible: true, device, submitting: false } })
    expect(channelCellText(wrapper)).toBe('—')
    expect(wrapper.text()).not.toContain('UART 1')
  })
})
