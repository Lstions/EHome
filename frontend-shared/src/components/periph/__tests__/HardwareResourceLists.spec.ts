import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import type { GPIOBusResource, PWMBusResource } from '@/api/node'
import type { GPIOConfig, PWMConfig } from '@/api/periph'

const mocks = vi.hoisted(() => ({
  gpioSet: vi.fn(),
  gpioRead: vi.fn(),
  pwmStart: vi.fn(),
  pwmStop: vi.fn(),
  pwmSetDuty: vi.fn(),
  pwmGetState: vi.fn(),
  messageSuccess: vi.fn(),
  messageError: vi.fn(),
}))

vi.mock('@/api/periph', () => ({
  gpioApi: { set: mocks.gpioSet, read: mocks.gpioRead },
  pwmApi: {
    start: mocks.pwmStart,
    stop: mocks.pwmStop,
    setDuty: mocks.pwmSetDuty,
    getState: mocks.pwmGetState,
  },
}))
vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: mocks.messageSuccess, error: mocks.messageError }),
}))

import GPIOResourceList from '@/components/periph/GPIOResourceList.vue'
import PWMResourceList from '@/components/periph/PWMResourceList.vue'

const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: ['disabled', 'loading'],
  emits: ['click'],
  template: '<button v-bind="$attrs" :disabled="disabled" @click="$emit(\'click\')"><slot /></button>',
})
const TagStub = defineComponent({ template: '<span class="tag"><slot /></span>' })
const SwitchStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue', 'disabled', 'ariaLabel'],
  emits: ['change'],
  template: '<button class="switch" :aria-label="ariaLabel" :disabled="disabled" @click="$emit(\'change\', !modelValue)">{{ modelValue ? \'HIGH\' : \'LOW\' }}</button>',
})
const SliderStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue', 'disabled', 'ariaLabel'],
  emits: ['input', 'change'],
  template: '<button class="slider" :aria-label="ariaLabel" :disabled="disabled" @click="$emit(\'input\', 6500); $emit(\'change\', 6500)">{{ modelValue }}</button>',
})
const EmptyStub = defineComponent({ props: ['description'], template: '<div class="empty">{{ description }}</div>' })
const AlertStub = defineComponent({ props: ['title'], template: '<div class="alert">{{ title }}<slot /></div>' })

const stubs = {
  ElButton: ButtonStub,
  ElTag: TagStub,
  ElSwitch: SwitchStub,
  ElSlider: SliderStub,
  ElEmpty: EmptyStub,
  ElAlert: AlertStub,
  ElSkeleton: defineComponent({ template: '<div class="skeleton" />' }),
}

const gpioHardware: GPIOBusResource[] = [
  { id: 'GPIO2', pin: 2, enabled: true },
  { id: 'GPIO6', pin: 6, enabled: true },
]
const pwmHardware: PWMBusResource[] = [
  { id: 'PWM0', channel: 0, timer_count: 4, max_resolution_bits: 14 },
  { id: 'PWM1', channel: 1, timer_count: 4, max_resolution_bits: 14 },
]
const gpioConfig = (pin: number): GPIOConfig => ({
  node_id: 'node-1', pin, direction: 1, initial_level: 0, label: '', enabled: true,
})
const pwmConfig = (hardwareId: string, pin: number): PWMConfig => ({
  node_id: 'node-1', hardware_id: hardwareId, channel: Number(hardwareId.replace('PWM', '')), pin,
  frequency: 1000, duty: 5000, resolution: 14, auto_start: false, label: '', enabled: true,
})

const wrappers: VueWrapper[] = []
const track = (wrapper: VueWrapper) => { wrappers.push(wrapper); return wrapper }
afterEach(() => wrappers.splice(0).forEach(wrapper => wrapper.unmount()))
beforeEach(() => {
  vi.clearAllMocks()
  vi.useRealTimers()
})

describe('GPIOResourceList', () => {
  it('shows waiting state and does not invent rows when ESP32 reported no GPIO resources', () => {
    const wrapper = track(mount(GPIOResourceList, {
      props: { resources: [], configs: [gpioConfig(9)], nodeId: 'node-1' },
      global: { stubs },
    }))

    expect(wrapper.text()).toContain('等待节点硬件资源上报')
    expect(wrapper.findAll('[data-testid="gpio-resource-row"]')).toHaveLength(0)
    expect(wrapper.text()).toContain('GPIO9')
    expect(wrapper.text()).toContain('无效配置')
  })

  it('renders only reported GPIO resources and configures a free row through a click', async () => {
    const onConfigure = vi.fn()
    const wrapper = track(mount(GPIOResourceList, {
      props: {
        resources: gpioHardware,
        configs: [gpioConfig(9)],
        nodeId: 'node-1',
        occupiedPins: new Map([[6, 'UART TX']]),
        onConfigure,
      },
      global: { stubs },
    }))

    expect(wrapper.findAll('[data-testid="gpio-resource-row"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('GPIO 2')
    expect(wrapper.text()).toContain('UART TX')
    expect(wrapper.text()).toContain('GPIO9')

    await wrapper.get('[data-testid="configure-gpio-2"]').trigger('click')
    // 异步 <script setup> emit 在全局 stub 环境可能不进入 wrapper.emitted，
    // 监听器直接验证父组件可观察到的 configure 回调。
    expect(onConfigure).toHaveBeenCalledWith(2)
    expect(wrapper.find('[data-testid="configure-gpio-6"]').exists()).toBe(false)
  })
})

describe('PWMResourceList', () => {
  it('uses reported PWM hardware as row identity and displays its GPIO route', () => {
    const wrapper = track(mount(PWMResourceList, {
      props: {
        resources: pwmHardware,
        configs: [pwmConfig('PWM0', 6)],
        nodeId: 'node-1',
        availablePins: [2],
      },
      global: { stubs },
    }))

    const rows = wrapper.findAll('[data-testid="pwm-resource-row"]')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('PWM0 → GPIO6')
    expect(rows[1].text()).toContain('PWM1')
  })

  it('configures an unconfigured reported PWM resource by hardware id', async () => {
    const onConfigure = vi.fn()
    const wrapper = track(mount(PWMResourceList, {
      props: { resources: pwmHardware, configs: [], nodeId: 'node-1', availablePins: [2, 6], onConfigure },
      global: { stubs },
    }))

    await wrapper.get('[data-testid="configure-pwm-PWM1"]').trigger('click')
    expect(onConfigure).toHaveBeenCalledWith('PWM1')
  })

  it('never promotes a config-only PWM resource into the reported resource list', () => {
    const wrapper = track(mount(PWMResourceList, {
      props: { resources: pwmHardware, configs: [pwmConfig('PWM9', 6)], nodeId: 'node-1', availablePins: [2] },
      global: { stubs },
    }))

    expect(wrapper.findAll('[data-testid="pwm-resource-row"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('PWM9')
    expect(wrapper.text()).toContain('无效配置')
  })

  it('keeps Start unavailable while runtime state is unknown', async () => {
    mocks.pwmGetState.mockResolvedValue({ hardware_id: 'PWM0', channel: 0, pin: 6, frequency: 1000, duty: 5000, resolution: 14, auto_start: false, enabled: true })
    const wrapper = track(mount(PWMResourceList, {
      props: {
        resources: pwmHardware,
        configs: [pwmConfig('PWM0', 6)],
        nodeId: 'node-1',
        availablePins: [2],
      },
      global: { stubs },
    }))

    await flushPromises()
    expect(wrapper.text()).toContain('等待状态')
    // A REST config snapshot cannot prove runtime stopped, so Start remains
    // unavailable until an authoritative PeriphRsp updates running state.
    expect(wrapper.find('[data-testid="start-pwm-PWM0"]').exists()).toBe(false)
  })

  it('waits for authoritative runtime acknowledgement after Start', async () => {
    mocks.pwmGetState.mockResolvedValue({ duty: 5000 })
    mocks.pwmStart.mockResolvedValue({ message: 'sent' })
    const wrapper = track(mount(PWMResourceList, {
      props: { resources: pwmHardware, configs: [pwmConfig('PWM0', 6)], nodeId: 'node-1', availablePins: [2] },
      global: { stubs },
    }))
    await flushPromises()
    ;(wrapper.vm as any).applyRuntimeState('PWM0', false, 5000)
    await wrapper.vm.$nextTick()
    await wrapper.get('[data-testid="start-pwm-PWM0"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('已停止')
    expect(wrapper.text()).toContain('等待设备响应')
    ;(wrapper.vm as any).applyRuntimeState('PWM0', true, 5000)
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('运行中')
  })

  // ── 占空比滑块的「拖动只改显示、松手 300ms 后才写设备」契约 ──
  //
  // 该契约原先只由**已删除**的 PWMChannelRow spec 覆盖（C5 清理时随死文件一并删除）。
  // 而 PWMResourceList 是存活实现，本轮又把它的 input/change 处理器改写为收敛
  // `Arrayable<number>`（el-slider 支持 range 模式故载荷可能是数组），
  // 因此必须把这条契约重新钉在**存活组件**上，否则"拖动时每像素写一次设备"
  // 这类回归将无人拦截。
  describe('占空比滑块写入节流（存活实现）', () => {
    it('拖动（input）不写设备；松手（change）过 300ms 后才写', async () => {
      vi.useFakeTimers()
      try {
        mocks.pwmSetDuty.mockClear()
        // 本用例必须**分别**驱动 input 与 change：默认 stub 一次点击连发两者，
        // 那样无法区分"input 就写了"与"只有 change 才写"——
        // 实测这种写法下"input 也调 scheduleDuty"的变异**能存活**（防抖掩盖了差异）。
        // 故这里用一个只发 input、另一个只发 change 的两个探针元素。
        const ProbeStub = defineComponent({
          inheritAttrs: false,
          props: ['modelValue', 'disabled', 'ariaLabel'],
          emits: ['input', 'change'],
          template: `<div>
            <button class="slider-input-only" @click="$emit('input', 6500)">in</button>
            <button class="slider-change-only" @click="$emit('change', 6500)">ch</button>
          </div>`,
        })
        const wrapper = track(mount(PWMResourceList, {
          props: { resources: pwmHardware, configs: [pwmConfig('PWM0', 6)], nodeId: 'node-1', availablePins: [2] },
          global: { stubs: { ...stubs, ElSlider: ProbeStub } },
        }))
        await flushPromises()
        ;(wrapper.vm as any).applyRuntimeState('PWM0', true, 5000)
        await wrapper.vm.$nextTick()

        // ① 只发 input：本地显示可变，但**不得**排入写设备
        await wrapper.find('.slider-input-only').trigger('click')
        await vi.advanceTimersByTimeAsync(1000)
        expect(
          mocks.pwmSetDuty,
          'input（拖动中）不得写设备 —— 否则拖动会按像素打爆设备',
        ).not.toHaveBeenCalled()

        // ② 只发 change：309ms 后才写
        await wrapper.find('.slider-change-only').trigger('click')
        await vi.advanceTimersByTimeAsync(299)
        expect(mocks.pwmSetDuty, '299ms 时仍在防抖窗口内，不该写').not.toHaveBeenCalled()
        await vi.advanceTimersByTimeAsync(20)
        expect(mocks.pwmSetDuty, '过防抖窗口后必须写').toHaveBeenCalled()
      } finally {
        vi.useRealTimers()
      }
    })
  })
})
