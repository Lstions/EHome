import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import type { PWMConfig } from '@/api/periph'

const mocks = vi.hoisted(() => ({
  start: vi.fn(),
  stop: vi.fn(),
  setDuty: vi.fn(),
  getState: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))

vi.mock('@/api/periph', () => ({
  pwmApi: {
    start: mocks.start,
    stop: mocks.stop,
    setDuty: mocks.setDuty,
    getState: mocks.getState,
  },
}))
vi.mock('element-plus', () => ({
  ElMessage: { success: mocks.success, error: mocks.error },
}))

import PWMChannelRow from '@/components/periph/PWMChannelRow.vue'

const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: ['loading', 'disabled', 'type', 'size'],
  emits: ['click'],
  computed: {
    isDisabled(): boolean { return Boolean(this.disabled) },
  },
  template: `<button v-bind="$attrs" :disabled="isDisabled" :data-loading="String(Boolean(loading))" @click="$emit('click')"><slot /></button>`,
})

const TagStub = defineComponent({
  inheritAttrs: false,
  props: ['type', 'size', 'effect'],
  template: `<span v-bind="$attrs" class="el-tag-stub"><slot /></span>`,
})

const SliderStub = defineComponent({
  name: 'ElSlider',
  inheritAttrs: false,
  props: ['modelValue', 'min', 'max', 'step', 'showTooltip', 'disabled', 'ariaLabel'],
  emits: ['input', 'change', 'update:modelValue'],
  template: `<div v-bind="$attrs" class="el-slider-stub" :data-disabled="String(Boolean(disabled))" :aria-label="ariaLabel">
    <input aria-label="duty-slider" type="range" :value="modelValue" :disabled="disabled" />
  </div>`,
})

const stubs = {
  'el-button': ButtonStub,
  'el-tag': TagStub,
  'el-slider': SliderStub,
}

const pwmConfig = (overrides: Partial<PWMConfig> = {}): PWMConfig => ({
  node_id: 'node-1',
  pin: 6,
  frequency: 1000,
  duty: 5000,
  resolution: 14,
  auto_start: false,
  label: 'Fan',
  enabled: true,
  ...overrides,
})

function mountRow(config: PWMConfig, offline = false, running: boolean | null = null): VueWrapper {
  return mount(PWMChannelRow, {
    props: { config, nodeId: 'node-1', offline, running },
    global: { stubs },
  })
}

describe('PWMChannelRow', () => {
  const wrappers: VueWrapper[] = []

  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
  })

  afterEach(() => {
    wrappers.splice(0).forEach(w => w.unmount())
    vi.useRealTimers()
  })

  const track = (w: VueWrapper) => { wrappers.push(w); return w }

  describe('display', () => {
    it('renders duty percentage from config', () => {
      const wrapper = track(mountRow(pwmConfig({ duty: 5000 })))
      expect(wrapper.get('.pwm-duty-value').text()).toBe('50.00%')
    })

    it('shows 未知 when running is null', () => {
      const wrapper = track(mountRow(pwmConfig()))
      expect(wrapper.text()).toContain('未知')
    })

    it('shows 运行中 when running is true', () => {
      const wrapper = track(mountRow(pwmConfig(), false, true))
      expect(wrapper.text()).toContain('运行中')
    })

    it('shows 已停止 when running is false', () => {
      const wrapper = track(mountRow(pwmConfig(), false, false))
      expect(wrapper.text()).toContain('已停止')
    })

    it('updates local duty when config.duty prop changes', async () => {
      const wrapper = track(mountRow(pwmConfig({ duty: 2500 })))
      expect(wrapper.get('.pwm-duty-value').text()).toBe('25.00%')

      await wrapper.setProps({ config: pwmConfig({ duty: 7500 }) })
      expect(wrapper.get('.pwm-duty-value').text()).toBe('75.00%')
    })
  })

  describe('start/stop', () => {
    it('calls pwmApi.start on start button', async () => {
      mocks.start.mockResolvedValue(undefined)
      const wrapper = track(mountRow(pwmConfig()))

      const startBtn = wrapper.findAll('button').find(b => b.text().includes('启动'))!
      await startBtn.trigger('click')
      await flushPromises()

      expect(mocks.start).toHaveBeenCalledWith('node-1', 6)
      expect(wrapper.text()).toContain('运行中')
      expect(mocks.success).toHaveBeenCalledOnce()
    })

    it('shows stop button (not danger) when running', async () => {
      mocks.start.mockResolvedValue(undefined)
      const wrapper = track(mountRow(pwmConfig()))

      await wrapper.findAll('button').find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()

      const stopBtn = wrapper.findAll('button').find(b => b.text().includes('停止'))
      expect(stopBtn).toBeTruthy()
      // Stop button should NOT have type="danger"
      expect(stopBtn!.attributes('type')).not.toBe('danger')
    })

    it('calls pwmApi.stop on stop button', async () => {
      mocks.start.mockResolvedValue(undefined)
      mocks.stop.mockResolvedValue(undefined)
      const wrapper = track(mountRow(pwmConfig()))

      await wrapper.findAll('button').find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()

      const stopBtn = wrapper.findAll('button').find(b => b.text().includes('停止'))!
      await stopBtn.trigger('click')
      await flushPromises()

      expect(mocks.stop).toHaveBeenCalledWith('node-1', 6)
      expect(wrapper.text()).toContain('已停止')
    })

    it('shows error on start failure and keeps 未知', async () => {
      mocks.start.mockRejectedValue(new Error('fault'))
      const wrapper = track(mountRow(pwmConfig()))

      await wrapper.findAll('button').find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()

      expect(mocks.error).toHaveBeenCalledOnce()
    })

    it('emits state-change on start/stop', async () => {
      mocks.start.mockResolvedValue(undefined)
      mocks.stop.mockResolvedValue(undefined)
      const wrapper = track(mountRow(pwmConfig()))

      await wrapper.findAll('button').find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()
      expect(wrapper.emitted('state-change')![0]).toEqual([6, true])

      const stopBtn = wrapper.findAll('button').find(b => b.text().includes('停止'))!
      await stopBtn.trigger('click')
      await flushPromises()
      expect(wrapper.emitted('state-change')![1]).toEqual([6, false])
    })
  })

  describe('duty slider debounce', () => {
    it('does not call setDuty immediately on slider change', async () => {
      mocks.setDuty.mockResolvedValue(undefined)
      const wrapper = track(mountRow(pwmConfig(), false, true))

      const slider = wrapper.findComponent(SliderStub)
      slider.vm.$emit('change', 6000)
      await flushPromises()

      expect(mocks.setDuty).not.toHaveBeenCalled()
    })

    it('calls setDuty after 300ms debounce', async () => {
      mocks.setDuty.mockResolvedValue(undefined)
      const wrapper = track(mountRow(pwmConfig(), false, true))

      const slider = wrapper.findComponent(SliderStub)
      slider.vm.$emit('change', 6000)
      await flushPromises()

      vi.advanceTimersByTime(299)
      expect(mocks.setDuty).not.toHaveBeenCalled()

      vi.advanceTimersByTime(1)
      expect(mocks.setDuty).toHaveBeenCalledWith('node-1', 6, 6000)
    })

    it('rolls back duty on setDuty failure', async () => {
      mocks.setDuty.mockRejectedValue(new Error('fail'))
      mocks.getState.mockResolvedValue({ running: true, duty: 5000, frequency: 1000 })
      const wrapper = track(mountRow(pwmConfig({ duty: 5000 }), false, true))

      const slider = wrapper.findComponent(SliderStub)
      slider.vm.$emit('change', 8000)
      await flushPromises()
      vi.advanceTimersByTime(300)
      await flushPromises()

      // Should have rolled back to 5000
      expect(wrapper.get('.pwm-duty-value').text()).toBe('50.00%')
      expect(mocks.error).toHaveBeenCalledOnce()
    })

    it('updates local display on slider input without API call', async () => {
      const wrapper = track(mountRow(pwmConfig(), false, true))

      const slider = wrapper.findComponent(SliderStub)
      slider.vm.$emit('input', 8000)
      await flushPromises()

      expect(wrapper.get('.pwm-duty-value').text()).toBe('80.00%')
      expect(mocks.setDuty).not.toHaveBeenCalled()
    })
  })

  describe('offline state', () => {
    it('disables start button when offline', () => {
      const wrapper = track(mountRow(pwmConfig(), true))
      const startBtn = wrapper.findAll('button').find(b => b.text().includes('启动'))!
      expect((startBtn.element as HTMLButtonElement).disabled).toBe(true)
    })

    it('disables slider when offline', () => {
      const wrapper = track(mountRow(pwmConfig(), true, true))
      const slider = wrapper.findComponent(SliderStub)
      expect(slider.props('disabled')).toBe(true)
    })

    it('disables slider when not running', () => {
      const wrapper = track(mountRow(pwmConfig(), false, false))
      const slider = wrapper.findComponent(SliderStub)
      expect(slider.props('disabled')).toBe(true)
    })

    it('keeps content readable when offline', () => {
      const wrapper = track(mountRow(pwmConfig(), true))
      const el = wrapper.find('.pwm-channel-row').element as HTMLElement
      expect(el.style.opacity).toBe('')
      expect(el.style.pointerEvents).toBe('')
    })
  })

  describe('structure', () => {
    it('renders with pwm-channel-row class', () => {
      const wrapper = track(mountRow(pwmConfig()))
      expect(wrapper.find('.pwm-channel-row').exists()).toBe(true)
    })

    it('has aria-label on slider containing pin number', () => {
      const wrapper = track(mountRow(pwmConfig({ pin: 15 })))
      const slider = wrapper.findComponent(SliderStub)
      expect(slider.props('ariaLabel')).toContain('GPIO 15')
    })
  })
})
