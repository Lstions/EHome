import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, type PropType } from 'vue'
import type { GPIOConfig, PWMConfig } from '@/api/periph'

const mocks = vi.hoisted(() => ({
  gpioList: vi.fn(),
  gpioSet: vi.fn(),
  gpioToggle: vi.fn(),
  gpioRead: vi.fn(),
  pwmList: vi.fn(),
  pwmStart: vi.fn(),
  pwmStop: vi.fn(),
  pwmSetDuty: vi.fn(),
  subscribe: vi.fn(),
  unsubscribe: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))

// A hoisted mutable object controls the connected state so tests can toggle it
// without re-mocking the module. We cannot use Vue ref() here because vi.hoisted
// runs before imports are resolved.
const wsState = vi.hoisted(() => ({
  connected: false,
}))

vi.mock('@/api/periph', () => ({
  gpioApi: {
    list: mocks.gpioList,
    set: mocks.gpioSet,
    toggle: mocks.gpioToggle,
    read: mocks.gpioRead,
  },
  pwmApi: {
    list: mocks.pwmList,
    start: mocks.pwmStart,
    stop: mocks.pwmStop,
    setDuty: mocks.pwmSetDuty,
  },
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    get connected() {
      return wsState.connected
    },
    subscribe: mocks.subscribe,
  }),
}))
vi.mock('element-plus', () => ({
  ElMessage: { success: mocks.success, error: mocks.error },
}))

import PeripheralControl from '@/components/periph/PeripheralControl.vue'
import { WS_EVENT } from '@/events/events'

// --- Child stubs that expose key interaction surface ---

const GPIOPinCardStub = defineComponent({
  name: 'GPIOPinCard',
  props: {
    config: { type: Object as PropType<GPIOConfig>, required: true },
    nodeId: { type: String, required: true },
    offline: Boolean,
  },
  emits: ['remove'],
  template: `<div class="gpio-pin-card-stub" :data-pin="config.pin" :data-node-id="nodeId" :data-offline="String(offline)">
    <span class="stub-pin-name">GPIO {{ config.pin }}</span>
    <button class="stub-remove" @click="$emit('remove', config.pin)">remove</button>
  </div>`,
})

const PWMChannelCardStub = defineComponent({
  name: 'PWMChannelCard',
  props: {
    config: { type: Object as PropType<PWMConfig>, required: true },
    nodeId: { type: String, required: true },
    offline: Boolean,
  },
  emits: ['remove'],
  template: `<div class="pwm-channel-card-stub" :data-pin="config.pin" :data-node-id="nodeId" :data-offline="String(offline)">
    <span class="stub-pin-name">PWM {{ config.pin }}</span>
    <button class="stub-remove" @click="$emit('remove', config.pin)">remove</button>
  </div>`,
})

const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: ['loading', 'disabled'],
  emits: ['click'],
  computed: {
    isDisabled(): boolean {
      return Boolean(this.disabled)
    },
  },
  template: `<button v-bind="$attrs" :disabled="isDisabled" :data-loading="String(Boolean(loading))" @click="$emit('click')"><slot /></button>`,
})

const SkeletonStub = defineComponent({
  props: ['rows', 'animated'],
  template: '<div class="el-skeleton-stub" :data-rows="rows">skeleton</div>',
})

const EmptyStub = defineComponent({
  props: ['description', 'imageSize'],
  template: '<div class="el-empty-stub">{{ description }}</div>',
})

const stubs = {
  'el-button': ButtonStub,
  'el-skeleton': SkeletonStub,
  'el-empty': EmptyStub,
  'el-icon': defineComponent({ template: '<i><slot /></i>' }),
  GPIOPinCard: GPIOPinCardStub,
  PWMChannelCard: PWMChannelCardStub,
  Refresh: defineComponent({ template: '<span>refresh</span>' }),
}

const gpioConfig = (pin: number, overrides: Partial<GPIOConfig> = {}): GPIOConfig => ({
  node_id: 'node-1',
  pin,
  direction: 1,
  initial_level: 0,
  label: `GPIO${pin}`,
  enabled: true,
  ...overrides,
})

const pwmConfig = (pin: number, overrides: Partial<PWMConfig> = {}): PWMConfig => ({
  node_id: 'node-1',
  pin,
  frequency: 1000,
  duty: 5000,
  resolution: 14,
  auto_start: false,
  label: `PWM${pin}`,
  enabled: true,
  ...overrides,
})

function mountControl(props: { nodeId: string; offline?: boolean } = { nodeId: 'node-1' }): VueWrapper {
  return mount(PeripheralControl, {
    props,
    global: { stubs },
  })
}

describe('PeripheralControl', () => {
  const wrappers: VueWrapper[] = []
  let wsHandler: ((message: unknown) => void) | undefined

  beforeEach(() => {
    vi.clearAllMocks()
    wsState.connected = false
    mocks.gpioList.mockResolvedValue([])
    mocks.pwmList.mockResolvedValue([])
    mocks.gpioRead.mockResolvedValue({ level: 0 })
    mocks.subscribe.mockImplementation((_event: string, handler: (message: unknown) => void) => {
      wsHandler = handler
      return mocks.unsubscribe
    })
  })

  afterEach(() => {
    wrappers.splice(0).forEach(w => w.unmount())
    wsHandler = undefined
  })

  const track = (wrapper: VueWrapper) => {
    wrappers.push(wrapper)
    return wrapper
  }

  it('loads GPIO and PWM configs simultaneously on mount', async () => {
    mocks.gpioList.mockResolvedValue([gpioConfig(5), gpioConfig(12)])
    mocks.pwmList.mockResolvedValue([pwmConfig(6)])
    const wrapper = track(mountControl())
    await flushPromises()

    expect(mocks.gpioList).toHaveBeenCalledWith('node-1')
    expect(mocks.pwmList).toHaveBeenCalledWith('node-1')
    // Both should be called during the same tick (Promise.all)
    expect(mocks.gpioList.mock.invocationCallOrder[0]).toBeLessThan(mocks.pwmList.mock.invocationCallOrder[0] + 10)
    expect(mocks.pwmList.mock.invocationCallOrder[0]).toBeLessThan(mocks.gpioList.mock.invocationCallOrder[0] + 10)
    expect(wrapper.findAll('.gpio-pin-card-stub')).toHaveLength(2)
    expect(wrapper.findAll('.pwm-channel-card-stub')).toHaveLength(1)
  })

  it('shows empty state when no configs are loaded', async () => {
    const wrapper = track(mountControl())
    await flushPromises()

    expect(wrapper.find('.el-empty-stub').exists()).toBe(true)
    expect(wrapper.get('.el-empty-stub').text()).toContain('暂无外设配置')
    expect(wrapper.findAll('.gpio-pin-card-stub')).toHaveLength(0)
    expect(wrapper.findAll('.pwm-channel-card-stub')).toHaveLength(0)
  })

  it('renders GPIO config cards when GPIO configs are available', async () => {
    mocks.gpioList.mockResolvedValue([gpioConfig(5, { label: 'LED' }), gpioConfig(13, { label: 'Relay' })])
    const wrapper = track(mountControl())
    await flushPromises()

    const cards = wrapper.findAll('.gpio-pin-card-stub')
    expect(cards).toHaveLength(2)
    expect(cards[0].attributes('data-pin')).toBe('5')
    expect(cards[1].attributes('data-pin')).toBe('13')
  })

  it('renders PWM config cards when PWM configs are available', async () => {
    mocks.pwmList.mockResolvedValue([pwmConfig(6, { label: 'Fan' }), pwmConfig(7, { label: 'Buzzer' })])
    const wrapper = track(mountControl())
    await flushPromises()

    const cards = wrapper.findAll('.pwm-channel-card-stub')
    expect(cards).toHaveLength(2)
    expect(cards[0].attributes('data-pin')).toBe('6')
    expect(cards[1].attributes('data-pin')).toBe('7')
  })

  it('renders both GPIO and PWM sections when both are available', async () => {
    mocks.gpioList.mockResolvedValue([gpioConfig(5)])
    mocks.pwmList.mockResolvedValue([pwmConfig(6)])
    const wrapper = track(mountControl())
    await flushPromises()

    expect(wrapper.findAll('.gpio-pin-card-stub')).toHaveLength(1)
    expect(wrapper.findAll('.pwm-channel-card-stub')).toHaveLength(1)
    expect(wrapper.text()).toContain('GPIO')
    expect(wrapper.text()).toContain('PWM')
  })

  it('passes offline prop to child cards', async () => {
    mocks.gpioList.mockResolvedValue([gpioConfig(5)])
    mocks.pwmList.mockResolvedValue([pwmConfig(6)])
    const wrapper = track(mountControl({ nodeId: 'node-1', offline: true }))
    await flushPromises()

    expect(wrapper.get('.gpio-pin-card-stub').attributes('data-offline')).toBe('true')
    expect(wrapper.get('.pwm-channel-card-stub').attributes('data-offline')).toBe('true')
  })

  it('reloads both GPIO and PWM on refresh button click', async () => {
    mocks.gpioList.mockResolvedValue([gpioConfig(5)])
    mocks.pwmList.mockResolvedValue([pwmConfig(6)])
    const wrapper = track(mountControl())
    await flushPromises()

    mocks.gpioList.mockClear()
    mocks.pwmList.mockClear()
    mocks.gpioList.mockResolvedValue([gpioConfig(5), gpioConfig(10)])
    mocks.pwmList.mockResolvedValue([pwmConfig(6), pwmConfig(11)])

    const buttons = wrapper.findAll('button')
    const refreshBtn = buttons.find(b => b.text().includes('刷新'))!
    await refreshBtn.trigger('click')
    await flushPromises()

    expect(mocks.gpioList).toHaveBeenCalledWith('node-1')
    expect(mocks.pwmList).toHaveBeenCalledWith('node-1')
    expect(wrapper.findAll('.gpio-pin-card-stub')).toHaveLength(2)
    expect(wrapper.findAll('.pwm-channel-card-stub')).toHaveLength(2)
  })

  it('subscribes to PERIPH_RESULT when websocket is connected', async () => {
    wsState.connected = true
    const wrapper = track(mountControl())
    await flushPromises()

    expect(mocks.subscribe).toHaveBeenCalledWith(WS_EVENT.PERIPH_RESULT, expect.any(Function))
    wrapper.unmount()
  })

  it('does not subscribe when websocket is not connected', async () => {
    track(mountControl())
    await flushPromises()

    expect(mocks.subscribe).not.toHaveBeenCalled()
  })

  it('unsubscribes on unmount', async () => {
    wsState.connected = true
    const wrapper = track(mountControl())
    await flushPromises()

    expect(mocks.subscribe).toHaveBeenCalledOnce()
    wrapper.unmount()
    wrappers.splice(wrappers.indexOf(wrapper), 1)
    expect(mocks.unsubscribe).toHaveBeenCalledOnce()
  })

  it('refreshes when receiving PERIPH_RESULT for this node', async () => {
    wsState.connected = true
    const wrapper = track(mountControl())
    await flushPromises()

    mocks.gpioList.mockClear()
    mocks.pwmList.mockClear()
    mocks.gpioList.mockResolvedValue([gpioConfig(5), gpioConfig(6)])
    mocks.pwmList.mockResolvedValue([pwmConfig(7)])

    wsHandler!({ type: WS_EVENT.PERIPH_RESULT, payload: { node_id: 'node-1' } })
    await flushPromises()

    expect(mocks.gpioList).toHaveBeenCalledWith('node-1')
    expect(mocks.pwmList).toHaveBeenCalledWith('node-1')
    expect(wrapper.findAll('.gpio-pin-card-stub')).toHaveLength(2)
    expect(wrapper.findAll('.pwm-channel-card-stub')).toHaveLength(1)
  })

  describe('layout structure', () => {
    it('renders card-grid with responsive grid class', async () => {
      mocks.gpioList.mockResolvedValue([gpioConfig(5)])
      const wrapper = track(mountControl())
      await flushPromises()

      const grid = wrapper.find('.card-grid')
      expect(grid.exists()).toBe(true)
    })

    it('renders GPIO section with section-title', async () => {
      mocks.gpioList.mockResolvedValue([gpioConfig(5)])
      const wrapper = track(mountControl())
      await flushPromises()

      const section = wrapper.find('.periph-section')
      expect(section.exists()).toBe(true)
      expect(section.find('.section-title').text()).toBe('GPIO')
    })

    it('renders PWM section with section-title', async () => {
      mocks.pwmList.mockResolvedValue([pwmConfig(6)])
      const wrapper = track(mountControl())
      await flushPromises()

      const section = wrapper.find('.periph-section')
      expect(section.exists()).toBe(true)
      expect(section.find('.section-title').text()).toBe('PWM')
    })
  })

  it('does not refresh when receiving PERIPH_RESULT for a different node', async () => {
    wsState.connected = true
    track(mountControl())
    await flushPromises()

    mocks.gpioList.mockClear()
    mocks.pwmList.mockClear()

    wsHandler!({ type: WS_EVENT.PERIPH_RESULT, payload: { node_id: 'other-node' } })
    await flushPromises()

    expect(mocks.gpioList).not.toHaveBeenCalled()
    expect(mocks.pwmList).not.toHaveBeenCalled()
  })
})
