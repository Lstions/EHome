import { describe, expect, it, vi, beforeEach } from 'vitest'
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

interface DeviceDataMockState {
  device: Ref<Record<string, any> | null>
  loading: Ref<boolean>
  refreshing: Ref<boolean>
  syncingHA: Ref<boolean>
  wsConnected: Ref<boolean>
  latestData: Ref<Record<string, any> | null>
  realtimeDataItems: Ref<unknown[]>
}

const hoisted = vi.hoisted(() => ({
  routerBack: vi.fn(),
  fetchDeviceDetail: vi.fn(),
  fetchLatestData: vi.fn(),
  clearRealtimeData: vi.fn(),
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
    realtimeDataItems: ref<unknown[]>([]),
  }
  return {
    useDeviceData: () => ({
      ...hoisted.state,
      fetchDeviceDetail: hoisted.fetchDeviceDetail,
      fetchLatestData: hoisted.fetchLatestData,
      clearRealtimeData: hoisted.clearRealtimeData,
      handleRefresh: hoisted.handleRefresh,
      handleSyncToHA: hoisted.handleSyncToHA,
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
    vi.clearAllMocks()
    hoisted.state.device.value = null
    hoisted.state.loading.value = false
    hoisted.state.refreshing.value = false
    hoisted.state.syncingHA.value = false
    hoisted.state.wsConnected.value = true
    hoisted.state.latestData.value = null
    hoisted.state.realtimeDataItems.value = []
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

  it('renders device sections with metric values derived from latestData', () => {
    hoisted.state.device.value = { id: 42, name: '测试逆变器', device_type: 'inverter', status: 'online' }
    hoisted.state.latestData.value = {
      pv1_power: 1000, pv2_power: 500,
      battery_voltage: 51.2, battery_current: 10,
      grid_voltage: 220, grid_frequency: 50,
      load_power: 500,
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
      pvPower: 1500, loadPower: 500, batteryVoltage: 51.2, batteryCurrent: 10,
    })
    expect(wrapper.findComponent(InverterMpptCard).props('data')).toEqual(hoisted.state.latestData.value)
    expect(wrapper.findComponent(InverterStatusGrid).props('latestData')).toEqual(hoisted.state.latestData.value)
    expect(wrapper.findComponent(InverterTempCard).props('latestData')).toEqual(hoisted.state.latestData.value)
    expect(wrapper.findComponent(InverterEnergyCard).props('latestData')).toEqual(hoisted.state.latestData.value)

    expect(wrapper.findAll('.el-tag').some(t => t.text() === '实时')).toBe(true)
    expect(wrapper.text()).not.toContain('条告警')
  })

  it('shows the alarm count tag only when alarms or faults exist', () => {
    hoisted.state.device.value = { id: 42, name: 'x', device_type: 'inverter', status: 'offline' }
    hoisted.state.latestData.value = { alarm_overload: 1, alarm_battery_low: 3, fault_code: 2 }
    const wrapper = mount(InverterDetailPage, { global: globalOptions })
    expect(wrapper.text()).toContain('3条告警')
    expect(wrapper.findAll('.el-tag').some(t => t.text() === '实时')).toBe(false)

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

  it('maps battery current sign to charge/discharge/idle and legacy fallbacks', () => {
    hoisted.state.device.value = { id: 1, name: 'x', device_type: 'inverter', status: 'online' }
    const cases: Array<{ data: Record<string, any>; direction: string }> = [
      { data: { battery_current: -4 }, direction: 'charge' },
      { data: { battery_current: 0 }, direction: 'idle' },
      { data: { battery_current: 4 }, direction: 'discharge' },
      { data: { current: -2 }, direction: 'charge' },
      { data: { current: 2 }, direction: 'discharge' },
      { data: {}, direction: 'idle' },
    ]
    for (const c of cases) {
      hoisted.state.latestData.value = c.data
      const wrapper = mount(InverterDetailPage, { global: globalOptions })
      expect(wrapper.findAllComponents(MetricStatCard)[1].props('direction'), JSON.stringify(c.data)).toBe(c.direction)
    }
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
