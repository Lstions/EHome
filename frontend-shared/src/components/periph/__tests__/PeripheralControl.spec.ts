import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import type { GPIOConfig, PWMConfig } from '@/api/periph'
import type { Capabilities } from '@/api/node'

const mocks = vi.hoisted(() => ({
  getCapabilities: vi.fn(),
  gpioList: vi.fn(),
  gpioDelete: vi.fn(),
  pwmList: vi.fn(),
  pwmDelete: vi.fn(),
  channelList: vi.fn(),
  subscribe: vi.fn(),
  unsubscribe: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))
const wsState = vi.hoisted(() => ({ connected: false }))

vi.mock('@/api/node', () => ({ nodeApi: { getCapabilities: mocks.getCapabilities } }))
vi.mock('@/api/periph', () => ({
  gpioApi: { list: mocks.gpioList, delete: mocks.gpioDelete },
  pwmApi: { list: mocks.pwmList, delete: mocks.pwmDelete },
}))
vi.mock('@/api/channel', () => ({ channelApi: { getList: mocks.channelList } }))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    get connected() { return wsState.connected },
    subscribe: mocks.subscribe,
  }),
}))
vi.mock('element-plus', () => ({ ElMessage: { success: mocks.success, error: mocks.error } }))

import PeripheralControl from '@/components/periph/PeripheralControl.vue'

const GPIOListStub = defineComponent({
  name: 'GPIOResourceList',
  props: ['resources', 'configs', 'nodeId', 'offline', 'loading', 'occupiedPins'],
  emits: ['configure', 'edit', 'remove'],
  template: `<div class="gpio-list" :data-count="resources.length" :data-offline="String(offline)" :data-occupied="[...occupiedPins.keys()].sort((a,b)=>a-b).join(',')">
    <span v-for="item in resources" :key="item.id" class="gpio-resource">{{ item.id }}</span>
    <button class="configure-gpio" @click="$emit('configure', 2)">configure gpio</button>
    <button class="remove-gpio" @click="$emit('remove', 2)">remove gpio</button>
  </div>`,
})
const PWMListStub = defineComponent({
  name: 'PWMResourceList',
  props: ['resources', 'configs', 'nodeId', 'offline', 'loading', 'availablePins'],
  emits: ['configure', 'edit', 'remove'],
  template: `<div class="pwm-list" :data-count="resources.length" :data-pins="availablePins.join(',')">
    <span v-for="item in resources" :key="item.id" class="pwm-resource">{{ item.id }}</span>
    <button class="configure-pwm" @click="$emit('configure', 'PWM1')">configure pwm</button>
    <button class="remove-pwm" @click="$emit('remove', 'PWM0')">remove pwm</button>
  </div>`,
})
const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: ['loading', 'disabled'], emits: ['click'],
  template: '<button v-bind="$attrs" :disabled="disabled" @click="$emit(\'click\')"><slot /></button>',
})
const stubs = {
  GPIOResourceList: GPIOListStub,
  PWMResourceList: PWMListStub,
  'el-button': ButtonStub,
  'el-icon': defineComponent({ template: '<i><slot /></i>' }),
  'el-alert': defineComponent({ props: ['title'], template: '<div class="alert">{{ title }}<slot /></div>' }),
  Refresh: defineComponent({ template: '<i />' }),
}

const capabilities: Capabilities = { buses: {
  gpio: [{ id: 'GPIO2', pin: 2, enabled: true }, { id: 'GPIO6', pin: 6, enabled: true }],
  pwm: [{ id: 'PWM0', channel: 0, timer_count: 4, max_resolution_bits: 14 }, { id: 'PWM1', channel: 1, timer_count: 4, max_resolution_bits: 14 }],
} }
const gpioConfig = (pin: number): GPIOConfig => ({ node_id: 'node-1', pin, direction: 1, initial_level: 0, label: '', enabled: true })
const pwmConfig = (hardwareId: string, channel: number, pin: number): PWMConfig => ({
  node_id: 'node-1', hardware_id: hardwareId, channel, pin, frequency: 1000, duty: 5000,
  resolution: 14, auto_start: false, label: '', enabled: true,
})

const wrappers: VueWrapper[] = []
const track = (wrapper: VueWrapper) => { wrappers.push(wrapper); return wrapper }
afterEach(() => wrappers.splice(0).forEach(wrapper => wrapper.unmount()))

beforeEach(() => {
  vi.clearAllMocks()
  wsState.connected = false
  mocks.getCapabilities.mockResolvedValue(capabilities)
  mocks.gpioList.mockResolvedValue([gpioConfig(2)])
  mocks.pwmList.mockResolvedValue([pwmConfig('PWM0', 0, 6)])
  mocks.channelList.mockResolvedValue([])
  mocks.subscribe.mockReturnValue(mocks.unsubscribe)
})

function mountControl(offline = false) {
  return track(mount(PeripheralControl, { props: { nodeId: 'node-1', offline }, global: { stubs } }))
}

describe('PeripheralControl', () => {
  it('loads independent ESP32-reported GPIO and PWM resources', async () => {
    const wrapper = mountControl()
    await flushPromises()

    expect(mocks.getCapabilities).toHaveBeenCalledWith('node-1')
    expect(wrapper.findAll('.gpio-resource').map(item => item.text())).toEqual(['GPIO2', 'GPIO6'])
    expect(wrapper.findAll('.pwm-resource').map(item => item.text())).toEqual(['PWM0', 'PWM1'])
    expect(wrapper.get('.pwm-list').attributes('data-pins')).toBe('')
  })

  it('does not synthesize resources from persisted configs when no report exists', async () => {
    mocks.getCapabilities.mockResolvedValue({ buses: {} })
    const wrapper = mountControl()
    await flushPromises()

    expect(wrapper.get('.gpio-list').attributes('data-count')).toBe('0')
    expect(wrapper.get('.pwm-list').attributes('data-count')).toBe('0')
  })

  it('occupies only pins decoded from actual enabled channel bus_config', async () => {
    mocks.channelList.mockResolvedValue([
      { id: 1, enabled: true, bus_type: 'I2C', bus_config: '0708' },
      { id: 2, enabled: false, bus_type: 'UART', bus_config: '0206' },
    ])
    const wrapper = mountControl()
    await flushPromises()
    expect(mocks.channelList).toHaveBeenCalledWith('node-1')
    expect(wrapper.get('.gpio-list').attributes('data-occupied')).toBe('6,7,8')
    expect(wrapper.get('.pwm-list').attributes('data-pins')).toBe('')
  })

  it('does not reserve report defaults or malformed enabled channel configs', async () => {
    mocks.channelList.mockResolvedValue([{ id: 1, enabled: true, bus_type: 'I2C', bus_config: '07' }])
    const wrapper = mountControl()
    await flushPromises()
    expect(wrapper.get('.gpio-list').attributes('data-occupied')).toBe('6')
    expect(wrapper.get('.pwm-list').attributes('data-pins')).toBe('')
  })

  it('passes offline state and emits independent configuration identities', async () => {
    const wrapper = mountControl(true)
    await flushPromises()

    expect(wrapper.get('.gpio-list').attributes('data-offline')).toBe('true')
    await wrapper.get('.configure-gpio').trigger('click')
    await wrapper.get('.configure-pwm').trigger('click')
    expect(wrapper.emitted('configure-gpio')).toEqual([[2]])
    expect(wrapper.emitted('configure-pwm')).toEqual([['PWM1']])
  })

  it('deletes PWM by hardware_id and reloads capabilities and configs', async () => {
    mocks.pwmDelete.mockResolvedValue(undefined)
    const wrapper = mountControl()
    await flushPromises()
    mocks.getCapabilities.mockClear()

    await wrapper.get('.remove-pwm').trigger('click')
    await flushPromises()

    expect(mocks.pwmDelete).toHaveBeenCalledWith('node-1', 'PWM0')
    expect(mocks.getCapabilities).toHaveBeenCalledWith('node-1')
  })

  it('shows a retryable error when any required resource request fails', async () => {
    mocks.getCapabilities.mockRejectedValue(new Error('network'))
    const wrapper = mountControl()
    await flushPromises()

    expect(wrapper.text()).toContain('资源数据加载失败')
    expect(mocks.error).toHaveBeenCalledWith('加载外设资源失败: network')
  })

  it('does not subscribe to unowned peripheral results', async () => {
    wsState.connected = true
    const wrapper = mountControl()
    await flushPromises()

    expect(mocks.subscribe).not.toHaveBeenCalled()
    wrapper.unmount()
    wrappers.splice(wrappers.indexOf(wrapper), 1)
    expect(mocks.unsubscribe).not.toHaveBeenCalled()
  })
})
