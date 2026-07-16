import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type DOMWrapper, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import type { GPIOConfig, PWMConfig } from '@/api/periph'

const mocks = vi.hoisted(() => ({
  queryResources: vi.fn(),
  getCapabilities: vi.fn(),
  getHardwareConfig: vi.fn(),
  updateHardwareConfig: vi.fn(),
  getChannels: vi.fn(),
  getTemplates: vi.fn(),
  gpioList: vi.fn(),
  gpioCreate: vi.fn(),
  gpioUpdate: vi.fn(),
  gpioDelete: vi.fn(),
  gpioSet: vi.fn(),
  gpioRead: vi.fn(),
  pwmList: vi.fn(),
  pwmCreate: vi.fn(),
  pwmUpdate: vi.fn(),
  pwmDelete: vi.fn(),
  pwmStart: vi.fn(),
  pwmStop: vi.fn(),
  pwmSetDuty: vi.fn(),
  pwmSetFreq: vi.fn(),
  pwmGetState: vi.fn(),
  messageSuccess: vi.fn(),
  messageWarning: vi.fn(),
  messageError: vi.fn(),
}))

vi.mock('@/api/node', () => ({
  nodeApi: {
    queryResources: mocks.queryResources,
    getCapabilities: mocks.getCapabilities,
    getHardwareConfig: mocks.getHardwareConfig,
    updateHardwareConfig: mocks.updateHardwareConfig,
  },
}))
vi.mock('@/api/deviceConfig', () => ({ deviceConfigApi: { getList: mocks.getTemplates } }))
vi.mock('@/api/channel', () => ({
  channelApi: { getList: mocks.getChannels, scan: vi.fn(), reconfigure: vi.fn() },
}))
vi.mock('@/api/periph', () => ({
  gpioApi: {
    list: mocks.gpioList,
    create: mocks.gpioCreate,
    update: mocks.gpioUpdate,
    delete: mocks.gpioDelete,
    set: mocks.gpioSet,
    read: mocks.gpioRead,
  },
  pwmApi: {
    list: mocks.pwmList,
    create: mocks.pwmCreate,
    update: mocks.pwmUpdate,
    delete: mocks.pwmDelete,
    start: mocks.pwmStart,
    stop: mocks.pwmStop,
    setDuty: mocks.pwmSetDuty,
    setFreq: mocks.pwmSetFreq,
    getState: mocks.pwmGetState,
  },
}))
vi.mock('@/stores/dma', () => ({
  useDmaStore: () => ({
    mergedChannels: [],
    toggling: {},
    isSwitchOn: vi.fn(() => false),
    fetch: vi.fn(),
    toggle: vi.fn(),
  }),
}))
vi.mock('@/stores/channel', () => ({
  useChannelStore: () => ({ deleteChannel: vi.fn() }),
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ subscribe: vi.fn(() => vi.fn()) }),
}))
vi.mock('@/utils/sessionCache', () => ({
  assertSessionGeneration: vi.fn(),
  getSessionGeneration: vi.fn(() => 1),
}))
vi.mock('@/utils/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))
vi.mock('element-plus', () => ({
  ElMessage: {
    success: mocks.messageSuccess,
    warning: mocks.messageWarning,
    error: mocks.messageError,
  },
}))
vi.mock('@/components/channel/ChannelManager.vue', () => ({ default: defineComponent({ template: '<div />' }) }))
vi.mock('@/components/channel/ChannelTerminal.vue', () => ({ default: defineComponent({ template: '<div />' }) }))

import ChannelPanel from '@/components/node/ChannelPanel.vue'

const SlotStub = defineComponent({ template: '<div><slot /><slot name="title" /><slot name="footer" /></div>' })
const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: ['disabled', 'loading'],
  emits: ['click'],
  template: '<button v-bind="$attrs" :disabled="disabled" @click="$emit(\'click\')"><slot /></button>',
})
const DialogStub = defineComponent({
  props: ['modelValue', 'title'],
  emits: ['update:modelValue', 'closed'],
  template: '<section v-if="modelValue" role="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></section>',
})
const InputStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue', 'disabled'],
  emits: ['update:modelValue'],
  template: '<input v-bind="$attrs" :value="modelValue" :disabled="disabled" @input="$emit(\'update:modelValue\', $event.target.value)" />',
})
const SelectStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue', 'disabled'],
  emits: ['update:modelValue'],
  setup(props, { attrs, emit, slots }) {
    return () => h('div', attrs, [
      slots.default?.(),
      h('button', {
        type: 'button',
        disabled: props.disabled,
        'data-testid': 'select-value-2',
        onClick: () => emit('update:modelValue', 2),
      }, 'GPIO2'),
    ])
  },
})
const OptionStub = defineComponent({
  props: ['value', 'label'],
  template: '<span>{{ label }}</span>',
})
const SwitchStub = defineComponent({
  props: ['modelValue', 'disabled'],
  emits: ['update:modelValue', 'change'],
  template: '<button class="switch" :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue); $emit(\'change\', !modelValue)">{{ modelValue }}</button>',
})
const InputNumberStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue', 'min', 'max'],
  template: '<input class="input-number" v-bind="$attrs" :data-value="modelValue" :data-min="min" :data-max="max" />',
})

const stubs = {
  'el-tabs': SlotStub,
  'el-tab-pane': SlotStub,
  'el-collapse': SlotStub,
  'el-collapse-item': SlotStub,
  'el-form': SlotStub,
  'el-form-item': SlotStub,
  'el-radio-group': SlotStub,
  'el-dialog': DialogStub,
  'el-button': ButtonStub,
  'el-input': InputStub,
  'el-select': SelectStub,
  'el-option': OptionStub,
  'el-slider': InputStub,
  'el-input-number': InputNumberStub,
  'el-switch': SwitchStub,
  'el-radio': defineComponent({ template: '<label><slot /></label>' }),
  'el-tag': defineComponent({ template: '<span><slot /></span>' }),
  'el-icon': SlotStub,
  'el-empty': defineComponent({ props: ['description'], template: '<div>{{ description }}</div>' }),
  'el-skeleton': defineComponent({ template: '<div class="skeleton" />' }),
  'el-alert': defineComponent({ props: ['title'], template: '<div>{{ title }}</div>' }),
  'el-checkbox': defineComponent({ template: '<input type="checkbox" disabled />' }),
  Refresh: true,
  Plus: true,
}

const gpioConfig = (overrides: Partial<GPIOConfig> = {}): GPIOConfig => ({
  node_id: 'node-1',
  pin: 2,
  direction: 1,
  initial_level: 0,
  label: 'Relay old',
  enabled: true,
  ...overrides,
})
const pwmConfig = (overrides: Partial<PWMConfig> = {}): PWMConfig => ({
  node_id: 'node-1',
  hardware_id: 'PWM0',
  channel: 0,
  pin: 6,
  frequency: 1000,
  duty: 5000,
  resolution: 14,
  auto_start: false,
  label: 'Fan',
  enabled: true,
  ...overrides,
})

let gpioConfigs: GPIOConfig[]
let pwmConfigs: PWMConfig[]
const wrappers: VueWrapper[] = []

afterEach(() => wrappers.splice(0).forEach(wrapper => wrapper.unmount()))
beforeEach(() => {
  vi.clearAllMocks()
  gpioConfigs = [gpioConfig()]
  pwmConfigs = [pwmConfig()]
  mocks.queryResources.mockRejectedValue(new Error('offline in test'))
  mocks.getCapabilities.mockResolvedValue({
    buses: {
      gpio: [
        { id: 'GPIO2', pin: 2, enabled: true },
        { id: 'GPIO6', pin: 6, enabled: true },
      ],
      pwm: [{ id: 'PWM0', channel: 0, timer_count: 4, max_resolution_bits: 14 }],
    },
  })
  mocks.getHardwareConfig.mockResolvedValue({ hardware: { buses: {} } })
  mocks.getChannels.mockResolvedValue([])
  mocks.getTemplates.mockResolvedValue({ items: [] })
  mocks.gpioList.mockImplementation(async () => gpioConfigs.map(config => ({ ...config })))
  mocks.pwmList.mockImplementation(async () => pwmConfigs.map(config => ({ ...config })))
  mocks.gpioCreate.mockResolvedValue(undefined)
  mocks.gpioUpdate.mockImplementation(async (_nodeId: string, pin: number, payload: Partial<GPIOConfig>) => {
    gpioConfigs = gpioConfigs.map(config => config.pin === pin ? { ...config, ...payload, pin } : config)
  })
  mocks.pwmCreate.mockResolvedValue(undefined)
  mocks.pwmUpdate.mockImplementation(async (_nodeId: string, hardwareId: string, payload: Partial<PWMConfig>) => {
    pwmConfigs = pwmConfigs.map(config => config.hardware_id === hardwareId
      ? { ...config, ...payload, hardware_id: hardwareId, channel: config.channel }
      : config)
  })
})

async function mountReady(): Promise<VueWrapper> {
  const wrapper = mount(ChannelPanel, {
    props: { collectorId: 7, nodeDeviceId: 'node-1', collectorStatus: 'online' },
    global: { stubs },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

function rowWithText(wrapper: VueWrapper, testId: string, text: string): DOMWrapper<Element> {
  const row = wrapper.findAll(`[data-testid="${testId}"]`).find(candidate => candidate.text().includes(text))
  if (!row) throw new Error(`Missing ${testId} row containing ${text}`)
  return row
}

async function clickButtonWithText(wrapper: { findAll(selector: string): DOMWrapper<Element>[] }, text: string): Promise<void> {
  const button = wrapper.findAll('button').find(candidate => candidate.text().includes(text))
  if (!button) throw new Error(`Missing button containing ${text}`)
  await button.trigger('click')
}

describe('ChannelPanel GPIO/PWM edit flow', () => {
  it('uses actual enabled remapped channel pins and ignores inactive or malformed routes', async () => {
    mocks.getChannels.mockResolvedValue([
      { id: 1, enabled: true, bus_type: 'I2C', bus_config: '0207' },
      { id: 2, enabled: false, bus_type: 'UART', bus_config: '0608' },
      { id: 3, enabled: true, bus_type: 'I2C', bus_config: '06' },
    ])
    const wrapper = await mountReady()
    const gpioList = wrapper.getComponent({ name: 'GPIOResourceList' })
    expect([...gpioList.props('occupiedPins').keys()].sort()).toEqual([2, 6, 7])
    const pwmList = wrapper.getComponent({ name: 'PWMResourceList' })
    expect(pwmList.props('availablePins')).toEqual([])
  })

  it('bounds PWM resolution by the selected reported resource', async () => {
    mocks.getCapabilities.mockResolvedValue({ buses: {
      gpio: [{ id: 'GPIO2', pin: 2, enabled: true }, { id: 'GPIO6', pin: 6, enabled: true }],
      pwm: [{ id: 'PWM0', channel: 0, timer_count: 4, max_resolution_bits: 12 }],
    } })
    pwmConfigs = [pwmConfig({ resolution: 12 })]
    const wrapper = await mountReady()
    await clickButtonWithText(rowWithText(wrapper, 'pwm-resource-row', 'PWM0'), '编辑')
    const resolution = wrapper.findAll('.input-number').find(input => input.attributes('data-value') === '12')
    expect(resolution?.attributes('data-max')).toBe('12')
  })

  it('updates an edited GPIO by its original pin, never creates, and refreshes the row', async () => {
    const wrapper = await mountReady()
    const row = rowWithText(wrapper, 'gpio-resource-row', 'GPIO 2')

    await clickButtonWithText(row, '编辑')
    const dialog = wrapper.get('[role="dialog"]')
    const labelInput = dialog.findAll('input').find(input => input.attributes('placeholder') === '可选，如：继电器')
    if (!labelInput) throw new Error('Missing editable GPIO label input')
    await labelInput.setValue('Relay updated')
    await clickButtonWithText(dialog, '确认保存')
    await flushPromises()

    expect(mocks.gpioUpdate).toHaveBeenCalledWith('node-1', 2, {
      direction: 1,
      initial_level: 0,
      label: 'Relay updated',
    })
    expect(mocks.gpioCreate).not.toHaveBeenCalled()
    expect(rowWithText(wrapper, 'gpio-resource-row', 'GPIO 2').text()).toContain('Relay updated')
  })

  it('updates an edited PWM by its original hardware id, never creates, and refreshes the route label', async () => {
    const wrapper = await mountReady()
    const row = rowWithText(wrapper, 'pwm-resource-row', 'PWM0 → GPIO6')

    await clickButtonWithText(row, '编辑')
    const dialog = wrapper.get('[role="dialog"]')
    const routeOption = dialog.findAll('button').find(button => button.text() === 'GPIO2')
    if (!routeOption) throw new Error('Missing GPIO2 PWM route option')
    await routeOption.trigger('click')
    await clickButtonWithText(dialog, '确认保存')
    await flushPromises()

    expect(mocks.pwmUpdate).toHaveBeenCalledWith('node-1', 'PWM0', {
      pin: 2,
      frequency: 1000,
      duty: 5000,
      resolution: 14,
      auto_start: false,
      label: 'Fan',
    })
    expect(mocks.pwmCreate).not.toHaveBeenCalled()
    expect(rowWithText(wrapper, 'pwm-resource-row', 'PWM0').text()).toContain('PWM0 → GPIO2')
  })

  it('omits the reported PWM channel from create payload because the backend resolves it', async () => {
    pwmConfigs = []
    const wrapper = await mountReady()
    await clickButtonWithText(rowWithText(wrapper, 'pwm-resource-row', 'PWM0'), '配置')
    const dialog = wrapper.get('[role="dialog"]')
    await clickButtonWithText(dialog, '确认添加')
    await flushPromises()

    expect(mocks.pwmCreate).toHaveBeenCalledWith('node-1', {
      hardware_id: 'PWM0',
      pin: 6,
      frequency: 1000,
      duty: 0,
      resolution: 14,
      auto_start: false,
      label: '',
    })
    expect(mocks.pwmCreate.mock.calls[0][1]).not.toHaveProperty('channel')
  })
})
