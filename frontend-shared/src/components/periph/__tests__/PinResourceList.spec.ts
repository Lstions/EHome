/// <reference types="node" />
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, nextTick } from 'vue'
import type { GPIOConfig, PWMConfig } from '@/api/periph'

const mocks = vi.hoisted(() => ({
  gpioSet: vi.fn(),
  gpioRead: vi.fn(),
  gpioDelete: vi.fn(),
  pwmStart: vi.fn(),
  pwmStop: vi.fn(),
  pwmSetDuty: vi.fn(),
  pwmGetState: vi.fn(),
  pwmDelete: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  confirm: vi.fn(),
}))

vi.mock('@/api/periph', () => ({
  gpioApi: {
    set: mocks.gpioSet,
    read: mocks.gpioRead,
    delete: mocks.gpioDelete,
  },
  pwmApi: {
    start: mocks.pwmStart,
    stop: mocks.pwmStop,
    setDuty: mocks.pwmSetDuty,
    getState: mocks.pwmGetState,
    delete: mocks.pwmDelete,
  },
}))
vi.mock('element-plus', () => ({
  ElMessage: { success: mocks.success, error: mocks.error },
  ElMessageBox: { confirm: mocks.confirm },
}))

import PinResourceList from '@/components/periph/PinResourceList.vue'

// --- Stubs ---
const SwitchStub = defineComponent({
  name: 'ElSwitch',
  inheritAttrs: false,
  props: ['modelValue', 'loading', 'disabled', 'activeText', 'inactiveText', 'ariaLabel'],
  emits: ['update:modelValue', 'change'],
  template: `<div v-bind="$attrs" class="el-switch-stub" :data-disabled="String(Boolean(disabled))" :data-loading="String(Boolean(loading))" :aria-label="ariaLabel">
    <button @click="$emit('change', !modelValue)">switch</button>
  </div>`,
})

const SliderStub = defineComponent({
  name: 'ElSlider',
  inheritAttrs: false,
  props: ['modelValue', 'min', 'max', 'step', 'showTooltip', 'disabled', 'ariaLabel'],
  emits: ['input', 'change', 'update:modelValue'],
  template: `<div v-bind="$attrs" class="el-slider-stub" :data-disabled="String(Boolean(disabled))" :aria-label="ariaLabel">
    <input aria-label="slider" type="range" :value="modelValue" :disabled="disabled" />
  </div>`,
})

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

const DropdownStub = defineComponent({
  name: 'ElDropdown',
  inheritAttrs: false,
  props: ['trigger'],
  emits: ['command'],
  template: `<div v-bind="$attrs" class="el-dropdown-stub"><slot /><slot name="dropdown" /></div>`,
})

const DropdownMenuStub = defineComponent({
  template: `<div class="el-dropdown-menu-stub"><slot /></div>`,
})

const DropdownItemStub = defineComponent({
  props: ['command'],
  emits: ['click'],
  template: `<div class="el-dropdown-item-stub" @click="$root.$emit('command', command)"><slot /></div>`,
})

const RadioGroupStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue', 'size'],
  emits: ['update:modelValue'],
  template: `<div v-bind="$attrs" class="el-radio-group-stub"><slot /></div>`,
})

const RadioButtonStub = defineComponent({
  props: ['value'],
  template: `<span class="el-radio-button-stub" :data-value="value"><slot /></span>`,
})

const InputStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue', 'size', 'clearable', 'placeholder'],
  emits: ['update:modelValue'],
  template: `<input v-bind="$attrs" class="el-input-stub" :value="modelValue" />`,
})

const SkeletonStub = defineComponent({
  props: ['rows', 'animated'],
  template: '<div class="el-skeleton-stub" :data-rows="rows">skeleton</div>',
})

const EmptyStub = defineComponent({
  props: ['description', 'imageSize'],
  template: '<div class="el-empty-stub">{{ description }}</div>',
})

const AlertStub = defineComponent({
  props: ['type', 'closable', 'title', 'showIcon'],
  template: '<div class="el-alert-stub" :data-type="type">{{ title }}</div>',
})

const stubs = {
  'el-switch': SwitchStub,
  'el-slider': SliderStub,
  'el-button': ButtonStub,
  'el-tag': TagStub,
  'el-dropdown': DropdownStub,
  'el-dropdown-menu': DropdownMenuStub,
  'el-dropdown-item': DropdownItemStub,
  'el-radio-group': RadioGroupStub,
  'el-radio-button': RadioButtonStub,
  'el-input': InputStub,
  'el-skeleton': SkeletonStub,
  'el-empty': EmptyStub,
  'el-alert': AlertStub,
}

// --- Factories ---
const gpioConfig = (pin: number, overrides: Partial<GPIOConfig> = {}): GPIOConfig => ({
  node_id: 'node-1',
  pin,
  direction: 1,
  initial_level: 0,
  label: '',
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
  label: '',
  enabled: true,
  ...overrides,
})

const hwGpio = (pin: number, id?: string) => ({ id: id || `GPIO${pin}`, pin })

function mountList(propsOverrides: Record<string, any> = {}): VueWrapper {
  return mount(PinResourceList, {
    props: {
      hardwareGpio: [],
      gpioConfigs: [],
      pwmConfigs: [],
      nodeId: 'node-1',
      ...propsOverrides,
    },
    global: { stubs },
  })
}

describe('PinResourceList', () => {
  const wrappers: VueWrapper[] = []

  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
    mocks.confirm.mockResolvedValue('confirm')
  })

  afterEach(() => {
    wrappers.splice(0).forEach(w => w.unmount())
    vi.useRealTimers()
  })

  const track = (w: VueWrapper) => { wrappers.push(w); return w }

  // === 唯一 pin 合并 ===
  describe('unique pin merge', () => {
    it('each physical pin appears only once even if both gpio and pwm have it', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5), hwGpio(12)],
        gpioConfigs: [gpioConfig(5)],
        pwmConfigs: [pwmConfig(12)],
      }))

      const rows = wrapper.findAll('.pin-resource-row')
      expect(rows).toHaveLength(2)
      // GPIO 5 → gpio type
      expect(rows[0].attributes('data-state')).toBe('gpio')
      // GPIO 12 → pwm type
      expect(rows[1].attributes('data-state')).toBe('pwm')
    })

    it('gpio config takes priority over pwm config for same pin', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5)],
        gpioConfigs: [gpioConfig(5)],
        pwmConfigs: [pwmConfig(5)],  // 不应同时出现
      }))

      const rows = wrapper.findAll('.pin-resource-row')
      expect(rows).toHaveLength(1)
      expect(rows[0].attributes('data-state')).toBe('gpio')
    })

    it('available rows render for pins without any config', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(1), hwGpio(2), hwGpio(3)],
        gpioConfigs: [gpioConfig(2)],
      }))

      const rows = wrapper.findAll('.pin-resource-row')
      expect(rows).toHaveLength(3)
      expect(rows[0].attributes('data-state')).toBe('available')
      expect(rows[1].attributes('data-state')).toBe('gpio')
      expect(rows[2].attributes('data-state')).toBe('available')
    })

    it('occupied pins show warning tag', () => {
      const occupied = new Map([[3, 'UART TX']])
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(1), hwGpio(3)],
        gpioConfigs: [],
        pwmConfigs: [],
        occupiedPins: occupied,
      }))

      const rows = wrapper.findAll('.pin-resource-row')
      expect(rows[1].attributes('data-state')).toBe('occupied')
      expect(rows[1].text()).toContain('UART TX')
    })

    it('includes config-only pins not in hardware list (degraded)', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(1)],
        gpioConfigs: [gpioConfig(1), gpioConfig(99)],
      }))

      const rows = wrapper.findAll('.pin-resource-row')
      expect(rows).toHaveLength(2)
      expect(rows[1].attributes('data-state')).toBe('gpio')
    })
  })

  // === 排序 ===
  it('sorts rows by pin number ascending', () => {
    const wrapper = track(mountList({
      hardwareGpio: [hwGpio(12), hwGpio(3), hwGpio(7)],
    }))

    const rows = wrapper.findAll('.pin-resource-row')
    expect(rows[0].text()).toContain('GPIO 3')
    expect(rows[1].text()).toContain('GPIO 7')
    expect(rows[2].text()).toContain('GPIO 12')
  })

  // === GPIO 行 ===
  describe('GPIO rows', () => {
    it('OUTPUT shows el-switch for HIGH/LOW', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5)],
        gpioConfigs: [gpioConfig(5, { direction: 1, initial_level: 0 })],
      }))

      const row = wrapper.find('.pin-resource-row')
      expect(row.find('.el-switch-stub').exists()).toBe(true)
      expect(row.find('[aria-label="GPIO 5 输出电平"]').exists()).toBe(true)
    })

    it('INPUT shows read button, no switch', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(12)],
        gpioConfigs: [gpioConfig(12, { direction: 0 })],
      }))

      const row = wrapper.find('.pin-resource-row')
      expect(row.find('.el-switch-stub').exists()).toBe(false)
      const buttons = row.findAll('button')
      const readBtn = buttons.find(b => b.text().includes('读取'))
      expect(readBtn).toBeTruthy()
    })

    it('calls gpioApi.set on switch change and updates level', async () => {
      mocks.gpioSet.mockResolvedValue(undefined)
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5)],
        gpioConfigs: [gpioConfig(5, { direction: 1, initial_level: 0 })],
      }))

      const sw = wrapper.findComponent(SwitchStub)
      sw.vm.$emit('change', true)
      await flushPromises()

      expect(mocks.gpioSet).toHaveBeenCalledWith('node-1', 5, 1)
      expect(mocks.success).toHaveBeenCalledOnce()
    })

    it('rolls back level on set failure', async () => {
      mocks.gpioSet.mockRejectedValue(new Error('network'))
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5)],
        gpioConfigs: [gpioConfig(5, { direction: 1, initial_level: 1 })],
      }))

      const sw = wrapper.findComponent(SwitchStub)
      sw.vm.$emit('change', false)  // Try to set LOW
      await flushPromises()

      expect(mocks.error).toHaveBeenCalledOnce()
      // Level should remain HIGH (rolled back)
      expect(wrapper.find('.level-text').text()).toBe('HIGH')
    })

    it('INPUT read button calls gpioApi.read', async () => {
      mocks.gpioRead.mockResolvedValue({ level: 1 })
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(12)],
        gpioConfigs: [gpioConfig(12, { direction: 0 })],
      }))

      const buttons = wrapper.findAll('button')
      const readBtn = buttons.find(b => b.text().includes('读取'))!
      await readBtn.trigger('click')
      await flushPromises()

      expect(mocks.gpioRead).toHaveBeenCalledWith('node-1', 12)
      expect(wrapper.find('.level-text').text()).toBe('HIGH')
    })

    it('shows 未知 for level before read (INPUT)', () => {
      mocks.gpioRead.mockReturnValue(new Promise(() => {}))
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(12)],
        gpioConfigs: [gpioConfig(12, { direction: 0 })],
      }))

      expect(wrapper.find('.level-text').text()).toBe('未知')
    })

    it('OUTPUT shows initial_level from config', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5)],
        gpioConfigs: [gpioConfig(5, { direction: 1, initial_level: 1 })],
      }))

      expect(wrapper.find('.level-text').text()).toBe('HIGH')
    })
  })

  // === PWM 行 ===
  describe('PWM rows', () => {
    it('shows running tag, duty value and slider', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(6)],
        pwmConfigs: [pwmConfig(6, { duty: 3750 })],
      }))

      const row = wrapper.find('.pin-resource-row')
      expect(row.find('.el-slider-stub').exists()).toBe(true)
      expect(row.text()).toContain('37.50%')
      // 运行态未知 → 未知
      expect(row.text()).toContain('未知')
    })

    it('start button calls pwmApi.start', async () => {
      mocks.pwmStart.mockResolvedValue(undefined)
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(6)],
        pwmConfigs: [pwmConfig(6)],
      }))

      const buttons = wrapper.findAll('button')
      const startBtn = buttons.find(b => b.text().includes('启动'))!
      await startBtn.trigger('click')
      await flushPromises()

      expect(mocks.pwmStart).toHaveBeenCalledWith('node-1', 6)
      expect(mocks.success).toHaveBeenCalledOnce()
    })

    it('stop button (not danger) calls pwmApi.stop', async () => {
      mocks.pwmStart.mockResolvedValue(undefined)
      mocks.pwmStop.mockResolvedValue(undefined)
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(6)],
        pwmConfigs: [pwmConfig(6)],
      }))

      // Start first
      const buttons = wrapper.findAll('button')
      const startBtn = buttons.find(b => b.text().includes('启动'))!
      await startBtn.trigger('click')
      await flushPromises()

      // Now stop button should be visible
      const buttons2 = wrapper.findAll('button')
      const stopBtn = buttons2.find(b => b.text().includes('停止'))!
      await stopBtn.trigger('click')
      await flushPromises()

      expect(mocks.pwmStop).toHaveBeenCalledWith('node-1', 6)
    })

    it('duty slider change calls setDuty after 300ms debounce', async () => {
      mocks.pwmStart.mockResolvedValue(undefined)
      mocks.pwmSetDuty.mockResolvedValue(undefined)
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(6)],
        pwmConfigs: [pwmConfig(6)],
      }))

      // Start PWM first
      const buttons = wrapper.findAll('button')
      await buttons.find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()

      const slider = wrapper.findComponent(SliderStub)
      slider.vm.$emit('change', 6000)
      await flushPromises()

      vi.advanceTimersByTime(299)
      expect(mocks.pwmSetDuty).not.toHaveBeenCalled()

      vi.advanceTimersByTime(1)
      expect(mocks.pwmSetDuty).toHaveBeenCalledWith('node-1', 6, 6000)
    })

    it('duty failure rolls back to server value', async () => {
      mocks.pwmStart.mockResolvedValue(undefined)
      mocks.pwmSetDuty.mockRejectedValue(new Error('fail'))
      mocks.pwmGetState.mockResolvedValue({ running: true, duty: 5000, frequency: 1000 })
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(6)],
        pwmConfigs: [pwmConfig(6, { duty: 5000 })],
      }))

      // Start
      const buttons = wrapper.findAll('button')
      await buttons.find(b => b.text().includes('启动'))!.trigger('click')
      await flushPromises()

      const slider = wrapper.findComponent(SliderStub)
      slider.vm.$emit('change', 8000)
      await flushPromises()
      vi.advanceTimersByTime(300)
      await flushPromises()

      // Should have rolled back
      expect(mocks.pwmGetState).toHaveBeenCalled()
      expect(mocks.error).toHaveBeenCalledOnce()
    })
  })

  // === available 行 ===
  describe('available rows', () => {
    it('shows 配置 GPIO and 启用 PWM buttons', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(1)],
      }))

      const row = wrapper.find('.pin-resource-row')
      const buttons = row.findAll('button')
      expect(buttons.some(b => b.text().includes('配置 GPIO'))).toBe(true)
      expect(buttons.some(b => b.text().includes('启用 PWM'))).toBe(true)
    })

    it('emits configure-gpio event with pin', async () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(7)],
      }))

      const buttons = wrapper.findAll('button')
      const cfgBtn = buttons.find(b => b.text().includes('配置 GPIO'))!
      await cfgBtn.trigger('click')

      expect(wrapper.emitted('configure-gpio')).toBeTruthy()
      expect(wrapper.emitted('configure-gpio')![0]).toEqual([7])
    })

    it('emits configure-pwm event with pin', async () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(7)],
      }))

      const buttons = wrapper.findAll('button')
      const pwmBtn = buttons.find(b => b.text().includes('启用 PWM'))!
      await pwmBtn.trigger('click')

      expect(wrapper.emitted('configure-pwm')).toBeTruthy()
      expect(wrapper.emitted('configure-pwm')![0]).toEqual([7])
    })
  })

  // === 筛选与搜索 ===
  describe('filter and search', () => {
    it('filter all shows all rows', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(1), hwGpio(2)],
        gpioConfigs: [gpioConfig(2)],
      }))

      expect(wrapper.findAll('.pin-resource-row')).toHaveLength(2)
    })

    it('filter available shows only available', async () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(1), hwGpio(2), hwGpio(3)],
        gpioConfigs: [gpioConfig(2)],
      }))

      // Simulate clicking "可用" filter
      const radioGroup = wrapper.findComponent(RadioGroupStub)
      await radioGroup.vm.$emit('update:modelValue', 'available')
      await nextTick()

      const rows = wrapper.findAll('.pin-resource-row')
      expect(rows).toHaveLength(2)
      rows.forEach(r => expect(r.attributes('data-state')).toBe('available'))
    })

    it('filter configured shows only gpio and pwm', async () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(1), hwGpio(2), hwGpio(3)],
        gpioConfigs: [gpioConfig(2)],
        pwmConfigs: [pwmConfig(3)],
      }))

      const radioGroup = wrapper.findComponent(RadioGroupStub)
      await radioGroup.vm.$emit('update:modelValue', 'configured')
      await nextTick()

      const rows = wrapper.findAll('.pin-resource-row')
      expect(rows).toHaveLength(2)
    })

    it('search filters by pin number', async () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5), hwGpio(12), hwGpio(20)],
      }))

      const input = wrapper.findComponent(InputStub)
      await input.vm.$emit('update:modelValue', '12')
      await nextTick()

      const rows = wrapper.findAll('.pin-resource-row')
      expect(rows).toHaveLength(1)
      expect(rows[0].text()).toContain('GPIO 12')
    })
  })

  // === 离线 ===
  describe('offline state', () => {
    it('shows offline alert', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(1)],
        offline: true,
      }))

      const alert = wrapper.find('.el-alert-stub')
      expect(alert.exists()).toBe(true)
      expect(alert.attributes('data-type')).toBe('warning')
      expect(alert.text()).toContain('离线')
    })

    it('disables GPIO switch when offline', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5)],
        gpioConfigs: [gpioConfig(5, { direction: 1 })],
        offline: true,
      }))

      const sw = wrapper.findComponent(SwitchStub)
      expect(sw.props('disabled')).toBe(true)
    })

    it('disables PWM start button when offline', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(6)],
        pwmConfigs: [pwmConfig(6)],
        offline: true,
      }))

      const buttons = wrapper.findAll('button')
      const startBtn = buttons.find(b => b.text().includes('启动'))!
      expect((startBtn.element as HTMLButtonElement).disabled).toBe(true)
    })

    it('disables available row config buttons when offline', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(1)],
        offline: true,
      }))

      // Check only the config action buttons, not the toolbar refresh button
      const row = wrapper.find('.pin-resource-row')
      const buttons = row.findAll('button')
      buttons.forEach(btn => {
        expect((btn.element as HTMLButtonElement).disabled).toBe(true)
      })
    })

    it('keeps row content readable (no opacity/pointer-events)', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5)],
        gpioConfigs: [gpioConfig(5, { direction: 1, initial_level: 1 })],
        offline: true,
      }))

      const row = wrapper.find('.pin-resource-row')
      // Row should not have opacity or pointer-events styles
      const el = row.element as HTMLElement
      expect(el.style.opacity).toBe('')
      expect(el.style.pointerEvents).toBe('')
      // Content is visible
      expect(row.text()).toContain('HIGH')
    })
  })

  // === 未知状态 ===
  describe('unknown state', () => {
    it('PWM shows 未知 when running is null', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(6)],
        pwmConfigs: [pwmConfig(6)],
      }))

      const row = wrapper.find('.pin-resource-row')
      expect(row.text()).toContain('未知')
    })

    it('GPIO INPUT shows 未知 before read', () => {
      mocks.gpioRead.mockReturnValue(new Promise(() => {}))
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(12)],
        gpioConfigs: [gpioConfig(12, { direction: 0 })],
      }))

      expect(wrapper.find('.level-text').text()).toBe('未知')
    })

    it('level dot has level-unknown class when level is null', () => {
      mocks.gpioRead.mockReturnValue(new Promise(() => {}))
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(12)],
        gpioConfigs: [gpioConfig(12, { direction: 0 })],
      }))

      expect(wrapper.find('.level-dot.level-unknown').exists()).toBe(true)
    })
  })

  // === 加载/空状态 ===
  describe('loading and empty', () => {
    it('shows skeleton on initial loading', () => {
      const wrapper = track(mountList({ initialLoading: true }))
      expect(wrapper.find('.el-skeleton-stub').exists()).toBe(true)
    })

    it('shows empty state when no pins', () => {
      const wrapper = track(mountList({ hardwareGpio: [] }))
      expect(wrapper.find('.el-empty-stub').exists()).toBe(true)
      expect(wrapper.find('.el-empty-stub').text()).toContain('未报告 GPIO 资源')
    })

    it('shows error alert on loadError', () => {
      const wrapper = track(mountList({ loadError: true, hardwareGpio: [hwGpio(1)] }))
      expect(wrapper.find('.el-alert-stub[data-type="error"]').exists()).toBe(true)
    })
  })

  // === 移除配置（danger + 确认） ===
  describe('remove config', () => {
    it('shows confirm dialog before removing GPIO', async () => {
      mocks.gpioDelete.mockResolvedValue(undefined)
      mocks.confirm.mockResolvedValue('confirm')
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5)],
        gpioConfigs: [gpioConfig(5)],
      }))

      // Find the dropdown "更多" button
      const buttons = wrapper.findAll('button')
      const moreBtn = buttons.find(b => b.text().includes('更多'))
      if (moreBtn) {
        await moreBtn.trigger('click')
      }
      // Emit command from dropdown
      const dropdown = wrapper.findComponent(DropdownStub)
      if (dropdown) {
        dropdown.vm.$emit('command', 'remove')
      }
      await flushPromises()

      expect(mocks.confirm).toHaveBeenCalledOnce()
      expect(mocks.gpioDelete).toHaveBeenCalledWith('node-1', 5)
    })
  })

  // === 结构 ===
  describe('structure', () => {
    it('renders ul with aria-label', () => {
      const wrapper = track(mountList({ hardwareGpio: [hwGpio(1)] }))
      const ul = wrapper.find('ul.pin-resource-list')
      expect(ul.exists()).toBe(true)
      expect(ul.attributes('aria-label')).toBe('GPIO 与 PWM 引脚资源')
    })

    it('each row has data-state attribute', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(1), hwGpio(2)],
        gpioConfigs: [gpioConfig(2)],
      }))

      const rows = wrapper.findAll('.pin-resource-row')
      expect(rows[0].attributes('data-state')).toBe('available')
      expect(rows[1].attributes('data-state')).toBe('gpio')
    })

    it('has pin-identity, pin-configuration, pin-runtime, pin-actions regions', () => {
      const wrapper = track(mountList({
        hardwareGpio: [hwGpio(5)],
        gpioConfigs: [gpioConfig(5, { direction: 1 })],
      }))

      const row = wrapper.find('.pin-resource-row')
      expect(row.find('.pin-identity').exists()).toBe(true)
      expect(row.find('.pin-configuration').exists()).toBe(true)
      expect(row.find('.pin-runtime').exists()).toBe(true)
      expect(row.find('.pin-actions').exists()).toBe(true)
    })
  })

  // === CSS 容器查询响应式 ===
  describe('container query responsive layout', () => {
    const fs = require('fs')
    const path = require('path')
    const sfcSource = fs.readFileSync(path.resolve(__dirname, '../PinResourceList.vue'), 'utf-8')
    const pwmRowSource = fs.readFileSync(path.resolve(__dirname, '../PWMChannelRow.vue'), 'utf-8')
    const gpioRowSource = fs.readFileSync(path.resolve(__dirname, '../GPIOPinRow.vue'), 'utf-8')

    it('pin-resource-panel establishes container context', () => {
      expect(sfcSource).toContain('container-type: inline-size')
      expect(sfcSource).toContain('container-name: pin-panel')
    })

    it('uses @container pin-panel as primary responsive trigger (not solely @media)', () => {
      expect(sfcSource).toContain('@container pin-panel (max-width: 600px)')
    })

    it('row grid columns use minmax(0,...) to prevent min-content overflow', () => {
      expect(sfcSource).toContain('minmax(0, 1.1fr)')
      expect(sfcSource).toContain('minmax(0, 1.2fr)')
      expect(sfcSource).toContain('minmax(0, 2fr)')
      expect(sfcSource).not.toContain('minmax(150px,')
      expect(sfcSource).not.toContain('minmax(180px,')
      expect(sfcSource).not.toContain('minmax(280px,')
    })

    it('key elements have min-width:0 to prevent flex/grid overflow', () => {
      // panel, filter, list, row, identity, configuration, runtime, actions
      const minWidthCount = (sfcSource.match(/min-width:\s*0/g) || []).length
      expect(minWidthCount).toBeGreaterThanOrEqual(7)
    })

    it('duty slider uses width clamp (not fixed px width)', () => {
      expect(sfcSource).toContain('min-width: 80px')
      expect(sfcSource).toContain('max-width: 180px')
      expect(sfcSource).toContain('width: 100%')
    })

    it('PWMChannelRow uses container query (not viewport @media)', () => {
      expect(pwmRowSource).toContain('container-type: inline-size')
      expect(pwmRowSource).toContain('container-name: pwm-row')
      expect(pwmRowSource).toContain('@container pwm-row')
      expect(pwmRowSource).not.toContain('@media (max-width: 768px)')
    })

    it('GPIOPinRow has min-width:0 and max-width:100% to prevent overflow', () => {
      expect(gpioRowSource).toContain('min-width: 0')
      expect(gpioRowSource).toContain('max-width: 100%')
    })
  })
})
