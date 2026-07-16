import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'

const mocks = vi.hoisted(() => ({
  gpioList: vi.fn(),
  gpioCreate: vi.fn(),
  gpioUpdate: vi.fn(),
  pwmList: vi.fn(),
  pwmCreate: vi.fn(),
  pwmUpdate: vi.fn(),
}))

vi.mock('@/api/periph', () => ({
  gpioApi: {
    list: mocks.gpioList,
    create: mocks.gpioCreate,
    update: mocks.gpioUpdate,
    delete: vi.fn(),
  },
  pwmApi: {
    list: mocks.pwmList,
    create: mocks.pwmCreate,
    update: mocks.pwmUpdate,
    delete: vi.fn(),
  },
}))
vi.mock('@/api/node', () => ({
  nodeApi: {
    queryResources: vi.fn().mockRejectedValue(new Error('offline')),
    getCapabilities: vi.fn().mockResolvedValue({
      buses: {
        adc: [], i2c: [], spi: [], uart: [],
        gpio: [{ id: 'GPIO2', pin: 2, enabled: true }],
        pwm: [{ id: 'PWM0', channel: 0, timer_count: 4, max_resolution_bits: 14 }],
      },
    }),
    getHardwareConfig: vi.fn().mockResolvedValue({ hardware: { buses: {} } }),
    updateHardwareConfig: vi.fn(),
  },
}))
vi.mock('@/api/deviceConfig', () => ({
  deviceConfigApi: { getList: vi.fn().mockResolvedValue({ items: [] }) },
}))
vi.mock('@/api/channel', () => ({
  channelApi: { getList: vi.fn().mockResolvedValue([]) },
}))
vi.mock('@/stores/dma', () => ({
  useDmaStore: () => ({
    mergedChannels: [], toggling: {}, fetch: vi.fn(), toggle: vi.fn(), isSwitchOn: vi.fn(),
  }),
}))
vi.mock('@/stores/channel', () => ({
  useChannelStore: () => ({ deleteChannel: vi.fn() }),
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ subscribe: vi.fn(() => vi.fn()) }),
}))
vi.mock('@/utils/sessionCache', () => ({
  assertSessionGeneration: vi.fn(), getSessionGeneration: vi.fn(() => 1),
}))
vi.mock('@/utils/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import ChannelPanel from '../ChannelPanel.vue'

const PassthroughStub = defineComponent({ template: '<div><slot /><slot name="title" /><slot name="footer" /></div>' })
const DialogStub = defineComponent({
  props: { modelValue: Boolean, title: String },
  emits: ['update:modelValue', 'closed'],
  template: '<section v-if="modelValue" :data-testid="`dialog-${title}`"><slot /><slot name="footer" /></section>',
})
const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: { disabled: Boolean, loading: Boolean },
  emits: ['click'],
  template: '<button v-bind="$attrs" :disabled="disabled" @click="$emit(\'click\')"><slot /></button>',
})
const GPIOResourceListStub = defineComponent({
  props: { configs: { type: Array, default: () => [] } },
  emits: ['edit'],
  template: '<button data-testid="edit-gpio" @click="$emit(\'edit\', configs[0].pin)">编辑 GPIO</button>',
})
const PWMResourceListStub = defineComponent({
  props: { configs: { type: Array, default: () => [] } },
  emits: ['edit'],
  template: '<button data-testid="edit-pwm" @click="$emit(\'edit\', configs[0].hardware_id)">编辑 PWM</button>',
})

const stubs = {
  'el-tabs': PassthroughStub,
  'el-tab-pane': PassthroughStub,
  'el-collapse': PassthroughStub,
  'el-collapse-item': PassthroughStub,
  'el-skeleton': true,
  'el-empty': true,
  'el-tag': PassthroughStub,
  'el-checkbox': true,
  'el-switch': true,
  'el-dialog': DialogStub,
  'el-form': PassthroughStub,
  'el-form-item': PassthroughStub,
  'el-input': defineComponent({
    props: { modelValue: [String, Number] },
    template: '<span>{{ modelValue }}</span>',
  }),
  'el-select': PassthroughStub,
  'el-option': true,
  'el-radio-group': PassthroughStub,
  'el-radio': PassthroughStub,
  'el-input-number': true,
  'el-slider': true,
  'el-button': ButtonStub,
  'el-icon': PassthroughStub,
  ChannelManager: true,
  ChannelTerminal: true,
  GPIOResourceList: GPIOResourceListStub,
  PWMResourceList: PWMResourceListStub,
}

const gpioConfig = {
  node_id: 'node-1', pin: 2, direction: 1, initial_level: 1,
  label: 'Relay', enabled: true,
}
const pwmConfig = {
  node_id: 'node-1', hardware_id: 'PWM0', channel: 0, pin: 6,
  frequency: 2000, duty: 3750, resolution: 12, auto_start: true,
  label: 'Fan', enabled: true,
}

const wrappers: VueWrapper[] = []
afterEach(() => wrappers.splice(0).forEach(wrapper => wrapper.unmount()))
beforeEach(() => {
  vi.clearAllMocks()
  mocks.gpioList.mockResolvedValue([gpioConfig])
  mocks.pwmList.mockResolvedValue([pwmConfig])
  mocks.gpioUpdate.mockResolvedValue(undefined)
  mocks.pwmUpdate.mockResolvedValue(undefined)
})

async function mountPanel() {
  const wrapper = mount(ChannelPanel, {
    props: { collectorId: '1', nodeDeviceId: 'node-1', collectorStatus: 'online' },
    global: { stubs },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('ChannelPanel GPIO/PWM editing', () => {
  it('updates the immutable GPIO pin selected from an edit row instead of creating', async () => {
    const wrapper = await mountPanel()

    await wrapper.get('[data-testid="edit-gpio"]').trigger('click')
    const dialog = wrapper.get('[data-testid="dialog-配置 GPIO 引脚"]')
    expect(dialog.text()).toContain('GPIO2')
    await dialog.get('[data-testid="submit-gpio"]').trigger('click')
    await flushPromises()

    expect(mocks.gpioUpdate).toHaveBeenCalledWith('node-1', 2, {
      direction: 1,
      initial_level: 1,
      label: 'Relay',
    })
    expect(mocks.gpioCreate).not.toHaveBeenCalled()
  })

  it('updates the immutable PWM hardware id selected from an edit row instead of creating', async () => {
    const wrapper = await mountPanel()

    await wrapper.get('[data-testid="edit-pwm"]').trigger('click')
    const dialog = wrapper.get('[data-testid="dialog-配置 PWM 硬件资源"]')
    expect(dialog.text()).toContain('PWM0 (channel 0)')
    await dialog.get('[data-testid="submit-pwm"]').trigger('click')
    await flushPromises()

    expect(mocks.pwmUpdate).toHaveBeenCalledWith('node-1', 'PWM0', {
      pin: 6,
      frequency: 2000,
      duty: 3750,
      resolution: 12,
      auto_start: true,
      label: 'Fan',
    })
    expect(mocks.pwmCreate).not.toHaveBeenCalled()
  })
})
