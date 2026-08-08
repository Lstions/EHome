import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import CreateWizardCommandIntervals from '@/components/device/CreateWizardCommandIntervals.vue'

const { mockGetDriverCommands } = vi.hoisted(() => ({
  mockGetDriverCommands: vi.fn((..._args: any[]) => Promise.resolve([])),
}))

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: { getDriverCommands: mockGetDriverCommands },
}))

const jiabaidaCommands = () => [
  { id: 'read_basic_info', name: '读取基本信息', type: 'read', cmd_byte: 0x03, write_data: '', read_length: 60, delay_ms: 100, interval_ms: 5000, schedulable: true, description: '总电压、电流、剩余容量' },
  { id: 'read_cell_voltage', name: '读取单体电压', type: 'read', cmd_byte: 0x04, write_data: '', read_length: 50, delay_ms: 100, interval_ms: 0, schedulable: true, description: '每串电芯电压' },
  { id: 'read_hardware_version', name: '读取硬件版本', type: 'read', cmd_byte: 0x05, write_data: '', read_length: 40, delay_ms: 100, interval_ms: 0, schedulable: true, description: '硬件版本字符串' },
  { id: 'read_comprehensive', name: '读取综合信息', type: 'read', cmd_byte: 0x0F, write_data: '', read_length: 100, delay_ms: 100, interval_ms: 0, schedulable: true, description: '0x03超集' },
  { id: 'read_protection_count', name: '读取保护历史次数', type: 'read', cmd_byte: 0xAA, write_data: '', read_length: 40, delay_ms: 100, interval_ms: 0, schedulable: true, description: '保护触发次数统计' },
]

const nonSchedulable = () => [
  { id: 'close_discharge_mos', name: '关放电MOS', type: 'write', cmd_byte: 0xE1, write_data: '', read_length: 0, delay_ms: 0, interval_ms: 0, schedulable: false, description: '一次性触发' },
]

function mountComp(props: Record<string, unknown> = {}) {
  return mount(CreateWizardCommandIntervals, {
    props: { deviceType: 'jiabaida_bms', ...props },
    global: {
      stubs: {
        'el-alert': { template: '<div class="el-alert"><slot name="title" /><slot /></div>' },
      },
    },
  })
}

describe('CreateWizardCommandIntervals.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('loads driver commands for the given device type and keeps only schedulable ones', async () => {
    mockGetDriverCommands.mockResolvedValue([...jiabaidaCommands(), ...nonSchedulable()] as any)
    const wrapper = mountComp()
    await flushPromises()

    expect(mockGetDriverCommands).toHaveBeenCalledWith('jiabaida_bms')
    const vm = wrapper.vm as any
    expect(vm.schedulableCommands.map((c: any) => c.id)).toEqual([
      'read_basic_info', 'read_cell_voltage', 'read_hardware_version',
      'read_comprehensive', 'read_protection_count',
    ])
    // 非轮询指令不进入配置
    expect(vm.schedulableCommands.some((c: any) => c.id === 'close_discharge_mos')).toBe(false)
    // 默认间隔取自模板 (interval_ms, 0 保留)
    const intervals = vm.getIntervals()
    expect(intervals).toEqual({
      read_basic_info: 5000,
      read_cell_voltage: 0,
      read_hardware_version: 0,
      read_comprehensive: 0,
      read_protection_count: 0,
    })
  })

  it('formats cmd_byte as 0xXX hex (0x03/0x04/0x05/0x0F/0xAA)', async () => {
    mockGetDriverCommands.mockResolvedValue(jiabaidaCommands() as any)
    const wrapper = mountComp()
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('0x03')
    expect(text).toContain('0x04')
    expect(text).toContain('0x05')
    expect(text).toContain('0x0F')
    expect(text).toContain('0xAA')
    // 名称/读/描述都渲染
    expect(text).toContain('读取基本信息')
    expect(text).toContain('读')
  })

  it('0 = 禁用: setInterval(0) 后 getIntervals 返回 0, 且重新启用恢复默认间隔', async () => {
    mockGetDriverCommands.mockResolvedValue(jiabaidaCommands() as any)
    const wrapper = mountComp()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.setInterval('read_cell_voltage', 8000)
    vm.setInterval('read_hardware_version', 0)   // 禁用
    vm.onToggle('read_cell_voltage', false)      // 开关关闭 → 0
    let intervals = vm.getIntervals()
    expect(intervals.read_cell_voltage).toBe(0)
    expect(intervals.read_hardware_version).toBe(0)

    // 重新启用 → 恢复默认 (模板 interval_ms, 0 时兜底 5000)
    vm.onToggle('read_cell_voltage', true)
    intervals = vm.getIntervals()
    expect(intervals.read_cell_voltage).toBe(5000)
  })

  it('switching deviceType resets loaded commands and intervals (no carry-over)', async () => {
    mockGetDriverCommands.mockResolvedValueOnce(jiabaidaCommands() as any)
    mockGetDriverCommands.mockResolvedValueOnce([] as any)
    const wrapper = mountComp()
    await flushPromises()
    const vm = wrapper.vm as any
    vm.setInterval('read_basic_info', 7000)

    await wrapper.setProps({ deviceType: 'sn3001_rain' })
    await flushPromises()

    expect(mockGetDriverCommands).toHaveBeenLastCalledWith('sn3001_rain')
    expect(vm.schedulableCommands).toHaveLength(0)
    // getIntervals 对无 schedulable 指令的驱动返回 null (不提交任何间隔)
    expect(vm.getIntervals()).toBeNull()
  })

  it('load failure: emits load-error, getIntervals returns null, retry recovers', async () => {
    mockGetDriverCommands.mockRejectedValueOnce(new Error('drv down'))
    const wrapper = mountComp()
    await flushPromises()

    const vm = wrapper.vm as any
    expect(vm.loadFailed).toBe(true)
    expect(vm.getIntervals()).toBeNull()
    // 错误事件已向父级发出
    expect(wrapper.emitted('load-error')).toBeTruthy()
    expect(wrapper.emitted('load-error')![0][0]).toContain('drv down')

    // 重试成功
    mockGetDriverCommands.mockResolvedValueOnce(jiabaidaCommands() as any)
    await vm.loadCommands()
    await flushPromises()
    expect(vm.loadFailed).toBe(false)
    expect(vm.getIntervals()).not.toBeNull()
  })

  it('renders an el-alert with retry when load fails', async () => {
    mockGetDriverCommands.mockRejectedValueOnce(new Error('drv down'))
    const wrapper = mountComp()
    await flushPromises()
    const alert = wrapper.find('.el-alert')
    expect(alert.exists()).toBe(true)
    expect(wrapper.text()).toContain('驱动轮询指令加载失败')
    expect(wrapper.text()).toContain('重试')
  })
})
