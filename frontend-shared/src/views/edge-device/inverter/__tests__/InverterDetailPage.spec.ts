import { describe, expect, it, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent } from 'vue'
import type { Ref } from 'vue'
import InverterDetailPage from '../InverterDetailPage.vue'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
import InverterPowerFlow from '../InverterPowerFlow.vue'
import InverterStatusGrid from '../InverterStatusGrid.vue'
import InverterMpptCard from '../InverterMpptCard.vue'
import InverterTempCard from '../InverterTempCard.vue'
import InverterEnergyCard from '../InverterEnergyCard.vue'
import inverterDetailSource from '../InverterDetailPage.vue?raw'

/** 去掉注释后的页面源码：**否定断言必须基于它**（注释里写「已移除实时数据流」不应判红）。 */
const inverterDetailCode = inverterDetailSource
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/(^|[^:])\/\/[^\n]*/g, '$1')

interface DeviceDataMockState {
  device: Ref<Record<string, any> | null>
  loading: Ref<boolean>
  refreshing: Ref<boolean>
  syncingHA: Ref<boolean>
  wsConnected: Ref<boolean>
  latestData: Ref<Record<string, any> | null>
}

const hoisted = vi.hoisted(() => ({
  routerBack: vi.fn(),
  fetchDeviceDetail: vi.fn(),
  fetchLatestData: vi.fn(),
  handleRefresh: vi.fn(),
  handleSyncToHA: vi.fn(),
  state: null as unknown as DeviceDataMockState,
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ back: hoisted.routerBack }),
  useRoute: () => ({ params: { id: '42' } }),
}))

vi.mock('@/composables/useDeviceData', async () => {
  const { ref } = await import('vue')
  hoisted.state = {
    device: ref<Record<string, any> | null>(null),
    loading: ref<boolean>(false),
    refreshing: ref<boolean>(false),
    syncingHA: ref<boolean>(false),
    wsConnected: ref<boolean>(true),
    latestData: ref<Record<string, any> | null>(null),
  }
  return {
    useDeviceData: () => ({
      ...hoisted.state,
      fetchDeviceDetail: hoisted.fetchDeviceDetail,
      fetchLatestData: hoisted.fetchLatestData,
      handleRefresh: hoisted.handleRefresh,
      handleSyncToHA: hoisted.handleSyncToHA,
    }),
  }
})

/**
 * 2026-11-19 报障链路（「BMS 在放电、INV 流向图静止」）的桩：
 * 只替换**取数**（同节点兄弟设备列表 / 最新值快照）与实时层，
 * 判定仍走真实的 useSiblingBattery + powerFlow，页面行为才被真正测到。
 */
const siblingMock = vi.hoisted(() => ({
  getList: vi.fn(),
  getLatestValues: vi.fn(),
  latestData: null as any,
  fieldAt: null as any,
}))

vi.mock('@/api/edgeDevice', async (importOriginal) => {
  const actual = await importOriginal<Record<string, any>>()
  return {
    ...actual,
    edgeDeviceApi: {
      ...actual.edgeDeviceApi,
      getList: siblingMock.getList,
      getLatestValues: siblingMock.getLatestValues,
    },
  }
})

vi.mock('@/composables/useRealtimeData', async () => {
  const { ref } = await import('vue')
  siblingMock.latestData = ref(null)
  siblingMock.fieldAt = ref({})
  return {
    useRealtimeData: () => ({
      latestData: siblingMock.latestData,
      fieldAt: siblingMock.fieldAt,
    }),
  }
})

const DeviceHeaderStub = defineComponent({
  name: 'DeviceHeader',
  props: {
    device: Object,
    wsConnected: Boolean,
    syncingHA: Boolean,
    refreshing: Boolean,
    title: String,
  },
  emits: ['back', 'syncToHA', 'refresh', 'updated'],
  template: '<div class="device-header-stub">{{ title }}' +
    '<button class="emit-back" @click="$emit(\'back\')" />' +
    '<button class="emit-sync" @click="$emit(\'syncToHA\')" />' +
    '<button class="emit-updated" @click="$emit(\'updated\')" />' +
    '<button class="emit-refresh" @click="$emit(\'refresh\')" /></div>',
})
const InfoCardStub = defineComponent({ name: 'DeviceInfoCard', template: '<div class="device-info-stub" />' })
const HistoryStub = defineComponent({ name: 'HistoryChartSection', template: '<div class="history-stub" />' })
const CommandStub = defineComponent({ name: 'CommandFrequencySection', template: '<div class="command-stub" />' })
const ControlStub = defineComponent({ name: 'DeviceControlPanel', template: '<div class="control-stub" />' })
const RealtimeStub = defineComponent({ name: 'RealtimeDataList', template: '<div class="realtime-stub" />' })
const SkeletonStub = defineComponent({
  name: 'ElSkeleton',
  props: { rows: Number, animated: Boolean },
  template: '<div class="el-skeleton" />',
})

const globalOptions = {
  stubs: {
    DeviceHeader: DeviceHeaderStub,
    DeviceInfoCard: InfoCardStub,
    HistoryChartSection: HistoryStub,
    CommandFrequencySection: CommandStub,
    DeviceControlPanel: ControlStub,
    RealtimeDataList: RealtimeStub,
    ElSkeleton: SkeletonStub,
    'el-skeleton': SkeletonStub,
  },
}

describe('InverterDetailPage', () => {
  beforeEach(() => {
    // 2026-10-21：页面 setup 里要登记顶部面包屑末段（useBreadcrumbStore），
    // 组件因此依赖 active pinia。这里给每个用例一份干净实例。
    setActivePinia(createPinia())
    vi.clearAllMocks()
    hoisted.state.device.value = null
    hoisted.state.loading.value = false
    hoisted.state.refreshing.value = false
    hoisted.state.syncingHA.value = false
    hoisted.state.wsConnected.value = true
    hoisted.state.latestData.value = null
    // 默认：同节点没有 BMS（其它用例的语境），只有专门用例才提供 BMS 设备
    siblingMock.getList.mockReset()
    siblingMock.getLatestValues.mockReset()
    siblingMock.getList.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 })
    siblingMock.getLatestValues.mockResolvedValue([])
  })

  it('renders a skeleton while loading without a device', () => {
    hoisted.state.loading.value = true
    const wrapper = mount(InverterDetailPage, { global: globalOptions })
    expect(wrapper.find('.inverter-detail').exists()).toBe(true)
    expect(wrapper.find('.el-skeleton').exists()).toBe(true)
    expect(wrapper.findAllComponents(MetricStatCard)).toHaveLength(0)
  })

  it('renders no data sections when device is null and not loading', () => {
    const wrapper = mount(InverterDetailPage, { global: globalOptions })
    expect(wrapper.find('.device-header-stub').exists()).toBe(true)
    expect(wrapper.find('.el-skeleton').exists()).toBe(false)
    expect(wrapper.findAllComponents(MetricStatCard)).toHaveLength(0)
  })

  /**
   * ⚠ 本用例用的键必须是**后端真实字段名**。
   *
   * 2026-10-19 修正：原用例用的是 battery_current / load_power / current —— 
   * 这三个字段在泰琪丰驱动里**都不存在**（真实字段是
   * battery_charge_current / battery_discharge_current / output_active_power）。
   * 于是用例一直绿着，而真实页面永远拿不到值：**测试与生产在两个世界里**，
   * 这正是"页面空着但没人发现"的原因之一。
   *
   * 现在用例与 InverterDetailPage 的传参口径一致。
   */
  it('renders device sections with metric values derived from latestData', () => {
    hoisted.state.device.value = { id: 42, name: '测试逆变器', device_type: 'techfine_inverter', status: 'online' }
    hoisted.state.latestData.value = {
      pv1_power: 1000, pv2_power: 500,
      battery_voltage: 51.2, battery_discharge_current: 10,
      battery_capacity: 69,
      grid_voltage: 220, grid_frequency: 50,
      output_active_power: 500,
    }
    const wrapper = mount(InverterDetailPage, { global: globalOptions })

    expect(wrapper.find('.device-header-stub').text()).toBe('测试逆变器')
    const cards = wrapper.findAllComponents(MetricStatCard)
    expect(cards).toHaveLength(4)
    expect(cards[0].props('label')).toBe('PV输入')
    expect(cards[0].props('value')).toBe('1.50kW')
    expect(cards[1].props('label')).toBe('电池')
    expect(cards[1].props('value')).toBe('51.2')
    expect(cards[1].props('direction')).toBe('discharge')
    expect(cards[2].props('label')).toBe('电网')
    expect(cards[2].props('value')).toBe('220')
    expect(cards[2].props('subText')).toBe('50.0Hz')
    expect(cards[3].props('label')).toBe('负载')
    expect(cards[3].props('value')).toBe('500W')

    expect(wrapper.findComponent(InverterPowerFlow).props()).toMatchObject({
      pvPower: 1500, loadPower: 500, batteryVoltage: 51.2, batteryDischargeCurrent: 10,
    })
    expect(wrapper.findComponent(InverterMpptCard).props('data')).toEqual(hoisted.state.latestData.value)
    expect(wrapper.findComponent(InverterStatusGrid).props('latestData')).toEqual(hoisted.state.latestData.value)
    expect(wrapper.findComponent(InverterTempCard).props('latestData')).toEqual(hoisted.state.latestData.value)
    expect(wrapper.findComponent(InverterEnergyCard).props('latestData')).toEqual(hoisted.state.latestData.value)

    // 2026-10-21 用户要求删除「实时数据流」：页面不再渲染 RealtimeDataList。
    // 保留 RealtimeDataList stub 作为**探测器** —— 若后续重构重新挂载该组件，
    // .realtime-stub 会立刻出现，这条反向断言变红。
    expect(wrapper.find('.realtime-stub').exists(), '实时数据流组件已按用户要求删除，不得复活').toBe(false)
    expect(wrapper.text()).not.toContain('条告警')
  })

  it('shows the alarm count tag only when alarms or faults exist', () => {
    hoisted.state.device.value = { id: 42, name: 'x', device_type: 'inverter', status: 'offline' }
    hoisted.state.latestData.value = { alarm_overload: 1, alarm_battery_low: 3, fault_code: 2 }
    const wrapper = mount(InverterDetailPage, { global: globalOptions })
    expect(wrapper.text()).toContain('3条告警')
    expect(wrapper.find('.realtime-stub').exists()).toBe(false)

    hoisted.state.latestData.value = { alarm_overload: 0, fault_code: 0 }
    const clean = mount(InverterDetailPage, { global: globalOptions })
    expect(clean.text()).not.toContain('条告警')
  })

  it('falls back from per-channel PV power to pv_power/solar_power', () => {
    hoisted.state.device.value = { id: 42, name: 'x', device_type: 'inverter', status: 'online' }

    hoisted.state.latestData.value = { pv1_power: 0, pv_power: 750 }
    expect(mount(InverterDetailPage, { global: globalOptions }).findAllComponents(MetricStatCard)[0].props('value')).toBe('750W')

    hoisted.state.latestData.value = { solar_power: 250 }
    expect(mount(InverterDetailPage, { global: globalOptions }).findAllComponents(MetricStatCard)[0].props('value')).toBe('250W')

    hoisted.state.latestData.value = { pv1_power: 100, pv2_power: 200, pv3_power: 300 }
    expect(mount(InverterDetailPage, { global: globalOptions }).findAllComponents(MetricStatCard)[0].props('value')).toBe('600W')
  })

  /**
   * 电池方向判定：**充电与放电是两个独立寄存器**，不是同一个量的正负号。
   *
   * 泰琪丰 HBAT 应答里 battery_charge_current 与 battery_discharge_current
   * 各占一个字段，浮充/待机时都可能为 0。旧用例假设存在 battery_current
   * （带符号的单值）—— 那个字段后端从来不返回，于是断言测的是一个不存在
   * 的契约；真实页面恒显示 idle（灰色）。本用例改用真实字段。
   */
  it('maps charge/discharge to direction from the two independent current fields', () => {
    hoisted.state.device.value = { id: 1, name: 'x', device_type: 'techfine_inverter', status: 'online' }
    const cases: Array<{ data: Record<string, any>; direction: string; note: string }> = [
      { data: { battery_charge_current: 4 }, direction: 'charge', note: '充电电流 > 0' },
      { data: { battery_discharge_current: 6 }, direction: 'discharge', note: '放电电流 > 0' },
      { data: { battery_charge_current: 0, battery_discharge_current: 0 }, direction: 'idle', note: '两者皆 0（浮充/待机）' },
      { data: {}, direction: 'idle', note: '字段缺失' },
    ]
    for (const c of cases) {
      hoisted.state.latestData.value = c.data
      const wrapper = mount(InverterDetailPage, { global: globalOptions })
      expect(
        wrapper.findAllComponents(MetricStatCard)[1].props('direction'),
        c.note + ' -> ' + JSON.stringify(c.data),
      ).toBe(c.direction)
    }
  })

  /**
   * 2026-11-19 用户报障：「BMS 都在放电了，INV 流向图还显示电池静止，不对！」
   *
   * 现场数据（同一分钟级窗口，生产库）：逆变器 53.1V/89%、充电 0A、放电 0A；
   * 同节点 BMS current=-1.96A。改前卡片与流向图都写「静止」。
   * 现在逆变器两路都为 0 时回退到同节点 BMS 判向，并标注「· BMS」。
   */
  it('falls back to the same-node BMS current when the inverter battery current reads 0', async () => {
    hoisted.state.device.value = {
      id: 42, name: '逆变器', device_type: 'techfine_inverter', status: 'online', node_id: 'NODE-1',
    }
    hoisted.state.latestData.value = {
      pv1_power: 193, output_active_power: 187,
      battery_voltage: 53.1, battery_capacity: 89,
      battery_charge_current: 0, battery_discharge_current: 0,
      grid_voltage: 226.5, work_mode: 3,
    }
    siblingMock.getList.mockResolvedValue({
      items: [{ id: 7, device_type: 'jiabaida_bms', node_id: 'NODE-1' }],
      total: 1, page: 1, page_size: 20,
    })
    siblingMock.getLatestValues.mockResolvedValue([
      { sensor_name: 'current', value: -1.96, unit: 'A', timestamp: new Date().toISOString() },
    ])

    const wrapper = mount(InverterDetailPage, { global: globalOptions })
    await flushPromises()

    expect(siblingMock.getList).toHaveBeenCalledWith(expect.objectContaining({ node_id: 'NODE-1' }))
    expect(siblingMock.getLatestValues).toHaveBeenCalledWith(7)

    const flow = wrapper.findComponent(InverterPowerFlow)
    expect(flow.props('batteryBmsCurrent')).toBeCloseTo(-1.96, 2)
    expect(flow.find('.pf-node.battery .pf-label').text()).toBe('放电 2.0A · BMS')

    const cards = wrapper.findAllComponents(MetricStatCard)
    expect(cards[1].props('direction'), '卡片与流向图必须同源').toBe('discharge')
    expect(cards[1].props('subText')).toBe('89% · 放 2.0A·BMS')
    wrapper.unmount()
  })

  /**
   * UI 层的物理闸门：越界读数显示 "—"，不把垃圾值当真实读数展示。
   *
   * 2026-10-19 实测：后端曾把 HBMS1 的 16 位二进制串当电池电压，写库 2087 条
   * 1e12 伏特。解析器根因已修，但前端仍是最后一道防线。
   */
  it('shows — instead of an implausible battery voltage', () => {
    hoisted.state.device.value = { id: 1, name: 'x', device_type: 'techfine_inverter', status: 'online' }
    hoisted.state.latestData.value = { battery_voltage: 1000000000000 }
    const wrapper = mount(InverterDetailPage, { global: globalOptions })
    expect(wrapper.findAllComponents(MetricStatCard)[1].props('value')).toBe('—')
  })

  it('renders — placeholders and zero power when latestData is null', () => {
    hoisted.state.device.value = { id: 1, name: 'x', device_type: 'inverter', status: 'offline' }
    const wrapper = mount(InverterDetailPage, { global: globalOptions })
    const cards = wrapper.findAllComponents(MetricStatCard)
    expect(cards).toHaveLength(4)
    expect(cards[0].props('value')).toBe('0W')
    expect(cards[1].props('value')).toBe('—')
    expect(cards[2].props('value')).toBe('—')
    expect(cards[2].props('subText')).toBe('—Hz')
    expect(cards[3].props('value')).toBe('0W')
    expect(wrapper.findComponent(InverterPowerFlow).props('pvPower')).toBe(0)
    expect(wrapper.findComponent(InverterMpptCard).props('data')).toBeNull()
    expect(wrapper.text()).not.toContain('条告警')
  })

  /**
   * 2026-10-21 用户要求：逆变器详情页不再显示「实时数据流」。
   * 反向契约用结构化标识（组件名 / composable 字段 / 状态数组），不用中文文案。
   * 保留的区块做分母守卫 —— 否则「没有实时流」可能只是文件被误删空。
   */
  it('2026-10-21：实时数据流已删除，源码不得再出现实时流组件/状态/文案', () => {
    for (const marker of ['RealtimeDataList', 'realtimeDataItems', 'clearRealtimeData', '实时数据流']) {
      expect(inverterDetailCode, '已删除标识复活：' + marker).not.toContain(marker)
    }
    for (const title of ['功率流向', '状态与告警', 'MPPT通道', '温度与风扇', '发电量统计']) {
      expect(inverterDetailCode, '缺少保留区块：' + title).toContain(title)
    }
  })

  it('wires header events to router and composable actions, and loads data on mount', async () => {
    const wrapper = mount(InverterDetailPage, { global: globalOptions })
    await flushPromises()
    expect(hoisted.fetchDeviceDetail).toHaveBeenCalledTimes(1)
    expect(hoisted.fetchLatestData).toHaveBeenCalledTimes(1)

    await wrapper.find('.emit-back').trigger('click')
    expect(hoisted.routerBack).toHaveBeenCalledTimes(1)
    await wrapper.find('.emit-sync').trigger('click')
    expect(hoisted.handleSyncToHA).toHaveBeenCalledTimes(1)
    await wrapper.find('.emit-updated').trigger('click')
    expect(hoisted.fetchDeviceDetail).toHaveBeenCalledTimes(2)
    await wrapper.find('.emit-refresh').trigger('click')
    expect(hoisted.handleRefresh).toHaveBeenCalledTimes(1)
    expect(typeof hoisted.handleRefresh.mock.calls[0][0]).toBe('function')
  })
})
