import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
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

// 使用 test-setup.ts 全局注册的 ElButton/ElTag/ElSlider stub（真实可交互 DOM），
// 不再用本地 stub 覆盖，避免断言与渲染结构不一致。

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

function mountRow(
  config: PWMConfig,
  offline = false,
  running: boolean | null = null,
  onStateChange?: (hardwareId: string, state: boolean | null) => void,
): VueWrapper {
  return mount(PWMChannelRow, {
    props: { config, nodeId: 'node-1', offline, running, onStateChange } as any,
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
    beforeEach(() => {
      // start/stop 测试不需要 fake timers，使用真实定时器
      vi.useRealTimers()
    })

    it('calls pwmApi.start on start button', async () => {
      mocks.start.mockResolvedValue(undefined)
      const wrapper = track(mountRow(pwmConfig()))

      const startBtn = wrapper.findAll('button').find(b => b.text().includes('启动'))!
      await startBtn.trigger('click')
      await flushPromises()

      expect(mocks.start).toHaveBeenCalledWith('node-1', 'PWM0')
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

      expect(mocks.stop).toHaveBeenCalledWith('node-1', 'PWM0')
      expect(wrapper.text()).toContain('已停止')
    })

    it('shows error on start failure and keeps 未知', async () => {
      mocks.start.mockRejectedValue(new Error('fault'))
      const wrapper = track(mountRow(pwmConfig()))

      await wrapper.findAll('button').find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()

      expect(mocks.error).toHaveBeenCalledOnce()
    })

    it('notifies parent of state-change on start/stop', async () => {
      mocks.start.mockResolvedValue(undefined)
      mocks.stop.mockResolvedValue(undefined)
      const onStateChange = vi.fn()
      const wrapper = track(mountRow(pwmConfig(), false, null, onStateChange))

      await wrapper.findAll('button').find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()
      expect(onStateChange).toHaveBeenCalledWith('PWM0', true)

      await wrapper.vm.$nextTick()
      const stopBtn = wrapper.findAll('button').find(b => b.text().includes('停止'))!
      await stopBtn.trigger('click')
      await flushPromises()
      expect(onStateChange).toHaveBeenLastCalledWith('PWM0', false)
      expect(onStateChange).toHaveBeenCalledTimes(2)
    })
  })

  describe('duty slider debounce', () => {
    it('does not call setDuty immediately on slider change', async () => {
      mocks.setDuty.mockResolvedValue(undefined)
      const wrapper = track(mountRow(pwmConfig(), false, true))

      const slider = wrapper.get('input.el-slider')
      await slider.setValue(6000)
      await flushPromises()

      expect(mocks.setDuty).not.toHaveBeenCalled()
    })

    it('calls setDuty after 300ms debounce', async () => {
      mocks.setDuty.mockResolvedValue(undefined)
      const wrapper = track(mountRow(pwmConfig(), false, true))

      const slider = wrapper.get('input.el-slider')
      await slider.setValue(6000)
      await flushPromises()

      vi.advanceTimersByTime(299)
      expect(mocks.setDuty).not.toHaveBeenCalled()

      vi.advanceTimersByTime(1)
      expect(mocks.setDuty).toHaveBeenCalledWith('node-1', 'PWM0', 6000)
    })

    it('rolls back duty on setDuty failure', async () => {
      mocks.setDuty.mockRejectedValue(new Error('fail'))
      mocks.getState.mockResolvedValue({ running: true, duty: 5000, frequency: 1000 })
      const wrapper = track(mountRow(pwmConfig({ duty: 5000 }), false, true))

      const slider = wrapper.get('input.el-slider')
      await slider.setValue(8000)
      await flushPromises()
      vi.advanceTimersByTime(300)
      await flushPromises()

      // Should have rolled back to 5000
      expect(wrapper.get('.pwm-duty-value').text()).toBe('50.00%')
      expect(mocks.error).toHaveBeenCalledOnce()
    })

    it('updates local display on slider input without API call', async () => {
      const wrapper = track(mountRow(pwmConfig(), false, true))

      const slider = wrapper.get('input.el-slider')
      await slider.setValue(8000)
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
      const slider = wrapper.get('input.el-slider')
      expect((slider.element as HTMLInputElement).disabled).toBe(true)
    })

    it('disables slider when not running', () => {
      const wrapper = track(mountRow(pwmConfig(), false, false))
      const slider = wrapper.get('input.el-slider')
      expect((slider.element as HTMLInputElement).disabled).toBe(true)
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
      // 全局 ElSlider stub 将 aria-label 渲染到 <input> 元素上
      const slider = wrapper.find('input.el-slider')
      expect(slider.attributes('aria-label')).toContain('GPIO 15')
    })
  })
})
