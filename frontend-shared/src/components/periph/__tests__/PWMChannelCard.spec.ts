import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import type { PWMConfig } from '@/api/periph'

const mocks = vi.hoisted(() => ({
  start: vi.fn(),
  stop: vi.fn(),
  setDuty: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))

vi.mock('@/api/periph', () => ({
  pwmApi: {
    start: mocks.start,
    stop: mocks.stop,
    setDuty: mocks.setDuty,
  },
}))
vi.mock('element-plus', () => ({
  ElMessage: { success: mocks.success, error: mocks.error },
}))

import PWMChannelCard from '@/components/periph/PWMChannelCard.vue'

const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: ['loading', 'disabled', 'type', 'size'],
  emits: ['click'],
  computed: {
    isDisabled(): boolean {
      return Boolean(this.disabled)
    },
  },
  template: `<button v-bind="$attrs" :disabled="isDisabled" :data-loading="String(Boolean(loading))" @click="$emit('click')"><slot /></button>`,
})

const TagStub = defineComponent({
  inheritAttrs: false,
  props: ['type', 'size'],
  template: `<span v-bind="$attrs"><slot /></span>`,
})

const SliderStub = defineComponent({
  name: 'ElSlider',
  inheritAttrs: false,
  props: ['modelValue', 'min', 'max', 'step', 'showTooltip', 'disabled'],
  emits: ['input', 'change', 'update:modelValue'],
  template: `<div v-bind="$attrs" class="el-slider-stub" :data-disabled="String(Boolean(disabled))">
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

function mountCard(config: PWMConfig, offline = false): VueWrapper {
  return mount(PWMChannelCard, {
    props: { config, nodeId: 'node-1', offline },
    global: { stubs },
  })
}

describe('PWMChannelCard', () => {
  const wrappers: VueWrapper[] = []

  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
  })

  afterEach(() => {
    wrappers.splice(0).forEach(w => w.unmount())
    vi.useRealTimers()
  })

  const track = (wrapper: VueWrapper) => {
    wrappers.push(wrapper)
    return wrapper
  }

  describe('display', () => {
    it('renders frequency from config', () => {
      const wrapper = track(mountCard(pwmConfig({ frequency: 2000 })))
      expect(wrapper.get('.freq-value').text()).toContain('2000 Hz')
    })

    it('renders duty percentage from config', () => {
      const wrapper = track(mountCard(pwmConfig({ duty: 5000 })))
      expect(wrapper.get('.duty-value').text()).toBe('50.00%')
    })

    it('updates local duty when config.duty prop changes', async () => {
      const wrapper = track(mountCard(pwmConfig({ duty: 2500 })))
      expect(wrapper.get('.duty-value').text()).toBe('25.00%')

      await wrapper.setProps({ config: pwmConfig({ duty: 7500 }) })
      expect(wrapper.get('.duty-value').text()).toBe('75.00%')
    })

    it('shows stopped status tag initially', () => {
      const wrapper = track(mountCard(pwmConfig()))
      expect(wrapper.get('.card-header-right .el-tag, .card-header-right span').text()).toContain('已停止')
    })
  })

  describe('start/stop', () => {
    it('calls pwmApi.start and sets running state on start', async () => {
      mocks.start.mockResolvedValue(undefined)
      const wrapper = track(mountCard(pwmConfig()))

      const buttons = wrapper.findAll('button')
      const startBtn = buttons.find(b => b.text().includes('启动'))!
      await startBtn.trigger('click')
      await flushPromises()

      expect(mocks.start).toHaveBeenCalledWith('node-1', 6)
      expect(wrapper.text()).toContain('运行中')
      expect(mocks.success).toHaveBeenCalledOnce()
    })

    it('disables start button when running', async () => {
      mocks.start.mockResolvedValue(undefined)
      const wrapper = track(mountCard(pwmConfig()))

      const buttons = wrapper.findAll('button')
      const startBtn = buttons.find(b => b.text().includes('启动'))!
      await startBtn.trigger('click')
      await flushPromises()

      expect((startBtn.element as HTMLButtonElement).disabled).toBe(true)
    })

    it('calls pwmApi.stop and clears running state on stop', async () => {
      mocks.start.mockResolvedValue(undefined)
      mocks.stop.mockResolvedValue(undefined)
      const wrapper = track(mountCard(pwmConfig()))

      // start first
      const buttons = wrapper.findAll('button')
      const startBtn = buttons.find(b => b.text().includes('启动'))!
      await startBtn.trigger('click')
      await flushPromises()
      expect(wrapper.text()).toContain('运行中')

      // then stop
      const stopBtn = buttons.find(b => b.text().includes('停止'))!
      await stopBtn.trigger('click')
      await flushPromises()

      expect(mocks.stop).toHaveBeenCalledWith('node-1', 6)
      expect(wrapper.text()).toContain('已停止')
      expect(mocks.success).toHaveBeenCalledTimes(2)
    })

    it('disables stop button when not running', () => {
      const wrapper = track(mountCard(pwmConfig()))

      const buttons = wrapper.findAll('button')
      const stopBtn = buttons.find(b => b.text().includes('停止'))!
      expect((stopBtn.element as HTMLButtonElement).disabled).toBe(true)
    })

    it('shows error on start failure', async () => {
      mocks.start.mockRejectedValue(new Error('hw fault'))
      const wrapper = track(mountCard(pwmConfig()))

      const buttons = wrapper.findAll('button')
      const startBtn = buttons.find(b => b.text().includes('启动'))!
      await startBtn.trigger('click')
      await flushPromises()

      expect(mocks.error).toHaveBeenCalledOnce()
      expect(wrapper.text()).toContain('已停止')
    })

    it('shows error on stop failure and keeps running state', async () => {
      mocks.start.mockResolvedValue(undefined)
      mocks.stop.mockRejectedValue(new Error('busy'))
      const wrapper = track(mountCard(pwmConfig()))

      const buttons = wrapper.findAll('button')
      const startBtn = buttons.find(b => b.text().includes('启动'))!
      await startBtn.trigger('click')
      await flushPromises()

      const stopBtn = buttons.find(b => b.text().includes('停止'))!
      await stopBtn.trigger('click')
      await flushPromises()

      expect(mocks.error).toHaveBeenCalledOnce()
      expect(wrapper.text()).toContain('运行中')
    })
  })

  describe('duty change debounce', () => {
    it('does not call setDuty immediately on slider change', async () => {
      mocks.setDuty.mockResolvedValue(undefined)
      mocks.start.mockResolvedValue(undefined)
      const wrapper = track(mountCard(pwmConfig()))

      const buttons = wrapper.findAll('button')
      await buttons.find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()

      const slider = wrapper.findComponent(SliderStub)
      slider.vm.$emit('change', 6000)
      await flushPromises()

      expect(mocks.setDuty).not.toHaveBeenCalled()
    })

    it('calls setDuty after 300ms debounce on slider change', async () => {
      mocks.setDuty.mockResolvedValue(undefined)
      mocks.start.mockResolvedValue(undefined)
      const wrapper = track(mountCard(pwmConfig()))

      const buttons = wrapper.findAll('button')
      await buttons.find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()

      const slider = wrapper.findComponent(SliderStub)
      slider.vm.$emit('change', 6000)
      await flushPromises()

      vi.advanceTimersByTime(299)
      expect(mocks.setDuty).not.toHaveBeenCalled()

      vi.advanceTimersByTime(1)
      expect(mocks.setDuty).toHaveBeenCalledWith('node-1', 6, 6000)
    })

    it('resets debounce timer on rapid successive changes', async () => {
      mocks.setDuty.mockResolvedValue(undefined)
      mocks.start.mockResolvedValue(undefined)
      const wrapper = track(mountCard(pwmConfig()))

      const buttons = wrapper.findAll('button')
      await buttons.find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()

      const slider = wrapper.findComponent(SliderStub)
      slider.vm.$emit('change', 6000)
      vi.advanceTimersByTime(200)

      slider.vm.$emit('change', 7000)
      vi.advanceTimersByTime(200)
      expect(mocks.setDuty).not.toHaveBeenCalled()

      vi.advanceTimersByTime(100)
      expect(mocks.setDuty).toHaveBeenCalledTimes(1)
      expect(mocks.setDuty).toHaveBeenCalledWith('node-1', 6, 7000)
    })

    it('updates local display on slider input without API call', async () => {
      mocks.start.mockResolvedValue(undefined)
      const wrapper = track(mountCard(pwmConfig()))

      const buttons = wrapper.findAll('button')
      await buttons.find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()

      const slider = wrapper.findComponent(SliderStub)
      slider.vm.$emit('input', 8000)
      await flushPromises()

      expect(wrapper.get('.duty-value').text()).toBe('80.00%')
      expect(mocks.setDuty).not.toHaveBeenCalled()
    })
  })

  describe('offline state', () => {
    it('disables start and stop buttons when offline', () => {
      const wrapper = track(mountCard(pwmConfig(), true))

      const buttons = wrapper.findAll('button')
      const startBtn = buttons.find(b => b.text().includes('启动'))!
      const stopBtn = buttons.find(b => b.text().includes('停止'))!
      expect((startBtn.element as HTMLButtonElement).disabled).toBe(true)
      expect((stopBtn.element as HTMLButtonElement).disabled).toBe(true)
    })

    it('applies pwm-offline class', () => {
      const wrapper = track(mountCard(pwmConfig(), true))
      expect(wrapper.find('.pwm-offline').exists()).toBe(true)
    })
  })

  describe('remove emit', () => {
    it('emits remove with pin number on remove button click', async () => {
      const wrapper = track(mountCard(pwmConfig({ pin: 9 })))

      const buttons = wrapper.findAll('button')
      const removeBtn = buttons.find(b => b.text().includes('✕'))!
      await removeBtn.trigger('click')

      expect(wrapper.emitted('remove')).toBeTruthy()
      expect(wrapper.emitted('remove')![0]).toEqual([9])
    })
  })
})
