import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import QuickCreateDeviceDialog from '@/components/node/QuickCreateDeviceDialog.vue'
import source from '@/components/node/QuickCreateDeviceDialog.vue?raw'

const { mockCreate, mockGetCandidates, mockGetDriverCommands } = vi.hoisted(() => ({
  mockCreate: vi.fn((..._args: any[]) => Promise.resolve({ id: 99 })),
  mockGetCandidates: vi.fn(() => Promise.resolve([])),
  // EDGE-WIZ-004/005: 默认返回空指令集 (普通驱动); 具体测试按需覆盖为
  // jiabaida_bms 的 5 条 schedulable 指令。
  mockGetDriverCommands: vi.fn((..._args: any[]) => Promise.resolve([])),
}))

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    create: mockCreate,
    getCandidates: mockGetCandidates,
    getDriverCommands: mockGetDriverCommands,
  },
}))

const parsers = [
  { id: 'sn3001_rain', name: 'SN-3001 光学雨量计', vendor: '通用', category: 'rain', hardware_types: ['uart'], measure_types: ['rain'], description: '' },
  { id: 'bmp280', name: 'BMP280 温压传感器', vendor: '博世', category: 'temp', hardware_types: ['i2c'], measure_types: ['temperature'], description: '' },
  // EDGE-WIZ-004/005: 声明 schedulable 轮询指令的驱动 (如嘉佰达 BMS)
  { id: 'jiabaida_bms', name: '嘉佰达 BMS', vendor: '嘉佰达', category: 'bms', hardware_types: ['uart'], measure_types: ['battery'], description: '' },
]

vi.mock('@/stores/parser', () => ({
  useParserStore: () => ({
    parsers,
    loading: false,
    fetchParsers: vi.fn(() => Promise.resolve()),
  }),
}))

const channels = [
  { id: 1, node_id: 'F0F5BDFFFE02', hardware_type: 'UART', hardware_id: '0x01', config: {} },
  { id: 2, node_id: 'F0F5BDFFFE02', hardware_type: 'I2C', hardware_id: 'I2C0', config: {} },
]

// EDGE-WIZ-004/005: 嘉佰达 BMS 驱动的 schedulable 轮询指令模板 (含一条非轮询写指令)
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

function mountDialog(props: Record<string, unknown> = {}) {
  setActivePinia(createPinia())
  return mount(QuickCreateDeviceDialog, {
    props: {
      modelValue: true,
      nodeId: 'F0F5BDFFFE02',
      nodeName: 'Test Node',
      channels,
      ...props,
    },
    global: {
      stubs: {
        // Element Plus components are globally stubbed by test setup
      },
    },
  })
}

describe('QuickCreateDeviceDialog.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockGetDriverCommands.mockResolvedValue([] as any)
  })
  it('filters node channels by the selected parser bus type (case-insensitive)', () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    // 选 uart 解析器后,只应看到 UART 通道(channel id=1),尽管后端存的是大写 'UART'
    vm.form.parserId = 'sn3001_rain'
    expect(vm.filteredChannels.map((c: any) => c.id)).toEqual([1])
    // 换 i2c 解析器,应只看到 I2C 通道(channel id=2)
    vm.form.parserId = 'bmp280'
    expect(vm.filteredChannels.map((c: any) => c.id)).toEqual([2])
  })

  it('clears the chosen channel when the parser changes', () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    vm.onParserChange()
    expect(vm.form.channelId).toBeUndefined()
  })

  it('creates a driver-backed device (no device_config_id) with type=parser.id', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    vm.form.name = '雨量计'
    vm.form.interval_ms = 1000
    // bypass el-form validate
    vm.formRef = { validate: () => Promise.resolve() }
    await vm.handleSubmit()
    await flushPromises()
    expect(mockCreate).toHaveBeenCalledWith(expect.objectContaining({
      name: '雨量计',
      node_id: 'F0F5BDFFFE02',
      channel_id: 1,
      hardware_id: '0x01',
      type: 'sn3001_rain',
      interval_ms: 1000,
    }))
    // 不得携带 device_config_id(无模板路径)
    const arg = mockCreate.mock.calls[0][0] as any
    expect(arg.device_config_id).toBeUndefined()
  })

  // R2: channels 加载中与"无匹配通道"是两种状态——加载中显示 loading 提示,
  // 不误报"暂无匹配通道";加载完且无匹配才显示引导文案。
  it('distinguishes channels-loading from no-matching-channel states', async () => {
    // 加载中: 显示"正在加载通道…",不显示"暂无匹配"
    const loadingWrapper = mountDialog({ channels: [], channelsLoading: true })
    const loadingVm = loadingWrapper.vm as any
    loadingVm.form.parserId = 'sn3001_rain'
    await loadingWrapper.vm.$nextTick()
    expect(loadingVm.channelsLoading).toBe(true)
    expect(loadingWrapper.text()).toContain('正在加载通道')
    expect(loadingWrapper.text()).not.toContain('暂无匹配')

    // 加载完但无匹配: 显示"暂无匹配 UART 的通道"引导
    const emptyWrapper = mountDialog({ channels: [], channelsLoading: false })
    const emptyVm = emptyWrapper.vm as any
    emptyVm.form.parserId = 'sn3001_rain'
    await emptyWrapper.vm.$nextTick()
    expect(emptyVm.channelsLoading).toBe(false)
    expect(emptyWrapper.text()).toContain('暂无匹配 UART 的通道')
  })

  // P3: 取消/关闭对话框(modelValue 变 false)时触发 watch 里的 reset(),
  // 重开后表单应已清空,不残留上次输入。
  it('resets the form when the dialog is closed (modelValue -> false)', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    // 先填值
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    vm.form.name = '雨量计'
    vm.form.interval_ms = 5000
    // 关闭对话框,触发 watch(modelValue) -> reset()
    await wrapper.setProps({ modelValue: false })
    await wrapper.vm.$nextTick()
    // 重开
    await wrapper.setProps({ modelValue: true })
    await wrapper.vm.$nextTick()
    // 表单应已重置为初始值
    expect(vm.form.parserId).toBe('')
    expect(vm.form.channelId).toBeUndefined()
    expect(vm.form.name).toBe('')
    expect(vm.form.interval_ms).toBe(1000)
  })

  // ---- 数据生命周期 T5: 继承历史数据折叠区 (方案 v3.3 §3.1 入口2) ----

  it('折叠区默认折叠: 渲染标题但不请求 candidates (延迟加载)', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    await wrapper.vm.$nextTick()
    await flushPromises()

    // 折叠区标题文案 (§3.1 入口2; ElCollapseItem 为通用 stub 不渲染
    // #title 具名插槽, 标题用源码契约断言, 折叠区正文提示用渲染断言)
    expect(source).toContain('继承历史数据（可选）')
    expect(wrapper.text()).toContain('若为更换/重建的同一台物理设备')
    // 默认折叠状态
    expect(vm.inheritCollapsed).toEqual([])
    expect(vm.inheritExpanded).toBe(false)
    // active=false → 候选组件不请求 (延迟加载, 折叠时零开销)
    expect(mockGetCandidates).not.toHaveBeenCalled()
  })

  it('展开折叠区: 以当前型号+节点+通道上下文请求 candidates', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    await wrapper.vm.$nextTick()

    // 展开折叠区 (el-collapse v-model 含 name='inherit')
    vm.inheritCollapsed = ['inherit']
    await wrapper.vm.$nextTick()
    await flushPromises()

    expect(vm.inheritExpanded).toBe(true)
    expect(mockGetCandidates).toHaveBeenCalledTimes(1)
    expect(mockGetCandidates).toHaveBeenCalledWith({
      type: 'sn3001_rain',
      node_id: 'F0F5BDFFFE02',
      hardware_id: '0x01',
      channel_id: 1,
    })
  })

  it('已选候选时提交携带 logical_device_id', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    vm.form.name = '雨量计-重建'
    vm.form.interval_ms = 1000
    vm.inheritLogicalDeviceId = 42
    vm.formRef = { validate: () => Promise.resolve() }
    await vm.handleSubmit()
    await flushPromises()
    expect(mockCreate).toHaveBeenCalledWith(expect.objectContaining({
      name: '雨量计-重建',
      node_id: 'F0F5BDFFFE02',
      channel_id: 1,
      type: 'sn3001_rain',
      logical_device_id: 42,
    }))
  })

  it('未选候选(默认折叠)时提交不携带 logical_device_id', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    vm.form.name = '雨量计-新'
    vm.formRef = { validate: () => Promise.resolve() }
    await vm.handleSubmit()
    await flushPromises()
    const arg = mockCreate.mock.calls[0][0] as any
    expect(arg.logical_device_id).toBeUndefined()
  })

  // ---- EDGE-WIZ-004/005: 逐指令轮询间隔 (复用 CreateWizardCommandIntervals) ----

  it('schedulable 指令加载并渲染: 隐藏全局采集间隔, 提示逐条设置', async () => {
    mockGetDriverCommands.mockResolvedValue([...jiabaidaCommands(), ...nonSchedulable()] as any)
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'jiabaida_bms'
    await wrapper.vm.$nextTick()
    await flushPromises()

    // 请求按型号发出
    expect(mockGetDriverCommands).toHaveBeenCalledWith('jiabaida_bms')
    // 子组件拉取的是 schedulable 指令, 非轮询写指令被过滤
    const el = vm.commandIntervalsRef as any
    expect(el.schedulableCommands.map((c: any) => c.id)).toEqual([
      'read_basic_info', 'read_cell_voltage', 'read_hardware_version',
      'read_comprehensive', 'read_protection_count',
    ])
    // 渲染: 指令名 + 十六进制指令码
    expect(wrapper.text()).toContain('读取基本信息')
    expect(wrapper.text()).toContain('0x03')
    // 有 schedulable 指令 → 隐藏全局采集间隔, 展示逐指令提示
    expect(vm.hasSchedulableCommands).toBe(true)
    expect(wrapper.text()).not.toContain('采集间隔 (ms)')
    expect(wrapper.text()).toContain('按下方“轮询指令”逐条设置间隔')
  })

  it('无 schedulable 指令的驱动保留全局采集间隔', async () => {
    mockGetDriverCommands.mockResolvedValue([] as any)
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    await wrapper.vm.$nextTick()
    await flushPromises()

    expect(mockGetDriverCommands).toHaveBeenCalledWith('sn3001_rain')
    expect(vm.hasSchedulableCommands).toBe(false)
    expect(wrapper.text()).toContain('采集间隔 (ms)')
  })

  it('修改/禁用逐指令间隔后提交携带 command_intervals', async () => {
    mockGetDriverCommands.mockResolvedValue(jiabaidaCommands() as any)
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'jiabaida_bms'
    vm.form.channelId = 1
    vm.form.name = '嘉佰达BMS'
    await wrapper.vm.$nextTick()
    await flushPromises()

    const el = vm.commandIntervalsRef as any
    el.setInterval('read_cell_voltage', 8000) // 修改默认间隔
    el.setInterval('read_basic_info', 0)      // 禁用 (0)

    vm.formRef = { validate: () => Promise.resolve() }
    await vm.handleSubmit()
    await flushPromises()

    expect(mockCreate).toHaveBeenCalledWith(expect.objectContaining({
      type: 'jiabaida_bms',
      command_intervals: {
        read_basic_info: 0,
        read_cell_voltage: 8000,
        read_hardware_version: 0,
        read_comprehensive: 0,
        read_protection_count: 0,
      },
    }))
  })

  it('切换型号后提交不带旧指令间隔', async () => {
    mockGetDriverCommands.mockResolvedValueOnce(jiabaidaCommands() as any)
    mockGetDriverCommands.mockResolvedValueOnce([] as any)
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'jiabaida_bms'
    vm.form.channelId = 1
    vm.form.name = '嘉佰达BMS'
    await wrapper.vm.$nextTick()
    await flushPromises()

    const el = vm.commandIntervalsRef as any
    el.setInterval('read_basic_info', 7000)

    // 切换到无 schedulable 指令的驱动 (onParserChange 清通道, 再重选)
    vm.form.parserId = 'sn3001_rain'
    vm.onParserChange()
    vm.form.channelId = 1
    await wrapper.vm.$nextTick()
    await flushPromises()

    expect(mockGetDriverCommands).toHaveBeenLastCalledWith('sn3001_rain')
    expect(vm.hasSchedulableCommands).toBe(false)

    vm.formRef = { validate: () => Promise.resolve() }
    await vm.handleSubmit()
    await flushPromises()

    const arg = mockCreate.mock.calls[0][0] as any
    expect(arg.command_intervals).toBeUndefined()
    expect(arg.interval_ms).toBe(1000)
  })

  it('驱动指令加载失败: 拦截提交, 不静默创建', async () => {
    mockGetDriverCommands.mockRejectedValue(new Error('drv down'))
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'jiabaida_bms'
    vm.form.channelId = 1
    vm.form.name = '嘉佰达BMS'
    await wrapper.vm.$nextTick()
    await flushPromises()

    // 加载失败状态已记录 (拦截提交的开关)
    expect(vm.commandIntervalsError).toContain('drv down')
    expect(vm.commandIntervalsReady).toBe(false)

    vm.formRef = { validate: () => Promise.resolve() }
    await vm.handleSubmit()
    await flushPromises()

    // 不得携带旧驱动数据静默创建
    expect(mockCreate).not.toHaveBeenCalled()
  })

  it('关闭重置清空折叠区状态与继承选择', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.inheritCollapsed = ['inherit']
    vm.inheritLogicalDeviceId = 7
    await wrapper.setProps({ modelValue: false })
    await wrapper.vm.$nextTick()
    expect(vm.inheritCollapsed).toEqual([])
    expect(vm.inheritLogicalDeviceId).toBeNull()
  })
})
