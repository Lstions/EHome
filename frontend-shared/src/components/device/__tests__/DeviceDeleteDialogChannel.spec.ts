import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import DeviceDeleteDialog from '@/components/device/DeviceDeleteDialog.vue'
import type { EdgeDevice } from '@/api/edgeDevice'

// 缺陷回归（2026-09-17 生产实测）：
//   被删设备「测试雨量计」实际挂在 **UART0** 总线，删除确认对话框的「通道」栏却显示 "UART 1"。
// 根因：channelLabel 拼接的是 **设备级** hardware_type + hardware_id —— 而边缘设备上
//   · hardware_type 为空/无意义；
//   · hardware_id 是 **Modbus 从站地址**（雨量计 = "1"），不是总线名。
//   真正的总线名在**关联通道**上（device.channel.hardware_id === "UART0"/"UART1"）。
// 本文件守住的三个不变量：
//   ① 有 channel 时显示真实总线名 UART0 / UART1；
//   ② 无 channel（老数据/紧凑列表）时回退为 UNKNOWN '—'，不崩；
//   ③ 回归点：设备 hardware_id="1" 且 channel.hardware_id="UART0" 时，绝不出现 "UART 1"。

const mockGetLogicalDeviceInfo = vi.fn()

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getLogicalDeviceInfo: (...args: unknown[]) => mockGetLogicalDeviceInfo(...args),
  },
}))

function makeDevice(overrides: Partial<EdgeDevice> = {}): EdgeDevice {
  return {
    id: 7,
    node_id: 'F0F5BDFFFE02',
    channel_id: 3,
    name: '测试雨量计',
    device_type: 'sn3001_rain',
    protocol: 'modbus',
    // 设备级 hardware_type 按生产实际为空/无意义
    hardware_type: '',
    // 设备级 hardware_id = Modbus 从站地址，正是误导值的来源
    hardware_id: '1',
    config: {},
    status: 'active',
    last_data: null,
    last_data_time: null,
    created_at: '2026-09-17T00:00:00Z',
    node: { id: 1, name: 'Collector-A' },
    ...overrides,
  }
}

function mountDialog(device: EdgeDevice) {
  return mount(DeviceDeleteDialog, {
    props: { visible: true, device, submitting: false },
  })
}

/** 取「通道」这一行的值（按标签定位，避免误读页面上其它 "UART" 文本）。 */
function channelCellText(wrapper: ReturnType<typeof mountDialog>): string {
  const row = wrapper.findAll('.fact-row').find(r => r.find('.fact-label').text() === '通道')
  expect(row, '未找到「通道」行 —— 选择器失效，断言将形同虚设').toBeTruthy()
  return row!.find('.fact-value').text()
}

describe('DeviceDeleteDialog 通道栏 = 真实总线标识（回归 2026-09-17 "UART 1" 显示缺陷）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockGetLogicalDeviceInfo.mockReturnValue(new Promise(() => {}))
  })

  it('显示后端通道的总线名，而不是设备 Modbus 地址（雨量计: hardware_id="1" / channel="UART0"）', () => {
    const wrapper = mountDialog(makeDevice({
      channel_hardware_id: 'UART0',
      channel: { id: 3, node_id: 'F0F5BDFFFE02', hardware_type: 'UART', hardware_id: 'UART0', bus_type: 'UART' },
    }))

    expect(channelCellText(wrapper)).toBe('UART0')
  })

  it('回归点：hardware_id="1" + channel.hardware_id="UART0" 时，绝不拼出 "UART 1" 这类误导值', () => {
    const wrapper = mountDialog(makeDevice({
      hardware_type: 'uart',
      hardware_id: '1',
      channel_hardware_id: 'UART0',
      channel: { hardware_type: 'UART', hardware_id: 'UART0' },
    }))

    const cell = channelCellText(wrapper)
    expect(cell).toBe('UART0')
    // 全弹窗文本都不应出现 "UART 1"（防的是绕过 .fact-value 选择器的写法）
    expect(wrapper.text()).not.toContain('UART 1')
    expect(cell).not.toContain('UART 1')
    // 更一般地：不得把设备地址当总线名拼进通道栏
    expect(cell).not.toContain('1')
  })

  it('I2C 设备同样取通道总线名（UART1 场景）', () => {
    const wrapper = mountDialog(makeDevice({
      name: 'BMS-1',
      device_type: 'jiabaida_bms',
      hardware_id: '0x76',
      channel_hardware_id: 'UART1',
      channel: { hardware_type: 'UART', hardware_id: 'UART1' },
    }))

    expect(channelCellText(wrapper)).toBe('UART1')
  })

  it('仅有 channel 对象（未显式给 channel_hardware_id）时也读 channel.hardware_id', () => {
    const wrapper = mountDialog(makeDevice({
      channel: { hardware_type: 'I2C', hardware_id: 'I2C0' },
    }))

    expect(channelCellText(wrapper)).toBe('I2C0')
  })

  it('兼容性：channel 缺失（老数据/紧凑列表）回退为 —，不崩且不显示误导值', () => {
    const wrapper = mountDialog(makeDevice({ hardware_type: 'uart', hardware_id: '1' }))

    expect(channelCellText(wrapper)).toBe('—')
    expect(wrapper.text()).not.toContain('UART 1')
    // 弹窗其余信息仍然正常渲染（没有因缺 channel 而整块崩掉）
    expect(wrapper.text()).toContain('测试雨量计')
  })

  it('兼容性：后端给了 channel 对象但 hardware_id 为空 —— 同样回退为 —', () => {
    const wrapper = mountDialog(makeDevice({
      hardware_type: 'uart',
      hardware_id: '1',
      channel: { hardware_type: 'UART' },
    }))

    expect(channelCellText(wrapper)).toBe('—')
    expect(wrapper.text()).not.toContain('UART 1')
  })

  it('清理占位：channel_hardware_id 为空串（而非 undefined）也回退为 —', () => {
    const wrapper = mountDialog(makeDevice({
      hardware_type: 'uart',
      hardware_id: '1',
      channel_hardware_id: '',
      channel: { hardware_type: 'UART', hardware_id: '' },
    }))

    expect(channelCellText(wrapper)).toBe('—')
  })

  it('device 为 null 时不崩，通道栏为 —', () => {
    const wrapper = mount(DeviceDeleteDialog, {
      props: { visible: true, device: null, submitting: false },
    })

    // device 为 null 时整个 facts 区不渲染，弹窗本身仍可显示
    expect(wrapper.find('.delete-device-facts').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('UART 1')
  })
})
