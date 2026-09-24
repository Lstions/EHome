import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import type { GPIOBusResource, PWMBusResource } from '@/api/node'
import type { GPIOConfig, PWMConfig } from '@/api/periph'
// D6：触控目标契约用源码断言（happy-dom 不应用 media query，挂载级断言测不到移动端尺寸）。
import gpioListSource from '@/components/periph/GPIOResourceList.vue?raw'
import pwmListSource from '@/components/periph/PWMResourceList.vue?raw'

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

  // ── D6：GPIO 列表的「去重复文案 + 过滤条」契约 ────────────────────────
  describe('D6 GPIO 列表可读性（去重 + 过滤）', () => {
    // 4 个引脚、其中 1 个已配置
    const manyGpio: GPIOBusResource[] = [
      { id: 'GPIO0', pin: 0, enabled: true }, { id: 'GPIO1', pin: 1, enabled: true },
      { id: 'GPIO2', pin: 2, enabled: true }, { id: 'GPIO3', pin: 3, enabled: true },
    ]

    it('未配置行**不得**再出现「ESP32 已上报」这类逐行相同的占位文案', () => {
      // 改前每行都渲染这句（实测 8 行逐字相同），占着 170px+ 的列却零信息量。
      const wrapper = track(mount(GPIOResourceList, {
        props: { resources: manyGpio, configs: [], nodeId: 'node-1' },
        global: { stubs },
      }))
      expect(wrapper.findAll('[data-testid="gpio-resource-row"]')).toHaveLength(4)
      expect(wrapper.text()).not.toContain('ESP32 已上报')

      // 反证：把每行 text 剥掉**结构性的、本来就该相同的**部分
      // （引脚号 + 「可用」状态标签）后，**不应再剩任何正文**。
      // 这样若有人塞回一句"XX 已上报"式占位描述，剩余文本会非空 ⇒ 红。
      // 注：不能直接比较"整行文本是否互不相同" —— 4 行合法地共享「可用」标签，
      // 那样写会假红（我第一版就是这么写的，被这条用例自己拦下了）。
      for (const row of wrapper.findAll('[data-testid="gpio-resource-row"]')) {
        const residual = row.text()
          .replace(/GPIO\s*\d+/g, '')
          .replace(/可用/g, '')
          .replace(/配置 GPIO/g, '')
          .trim()
        expect(residual, '未配置行残留了描述性文案（应为空）："' + residual + '"').toBe('')
      }
    })

    it('过滤条按「已配置/未配置」真过滤', async () => {
      const wrapper = track(mount(GPIOResourceList, {
        props: { resources: manyGpio, configs: [gpioConfig(2)], nodeId: 'node-1' },
        global: { stubs },
      }))
      expect(wrapper.findAll('[data-testid="gpio-resource-row"]')).toHaveLength(4)

      const vm = wrapper.vm as unknown as { filter: string }
      vm.filter = 'configured'
      await wrapper.vm.$nextTick()
      const configured = wrapper.findAll('[data-testid="gpio-resource-row"]')
      expect(configured, '已配置筛选应只剩 1 行').toHaveLength(1)
      expect(configured[0].text()).toContain('GPIO 2')

      vm.filter = 'unconfigured'
      await wrapper.vm.$nextTick()
      expect(wrapper.findAll('[data-testid="gpio-resource-row"]'), '未配置筛选应剩 3 行').toHaveLength(3)

      vm.filter = 'all'
      await wrapper.vm.$nextTick()
      expect(wrapper.findAll('[data-testid="gpio-resource-row"]')).toHaveLength(4)
    })

    it('筛选后为空时必须给说明（不能留白，否则用户以为页面坏了）', async () => {
      const wrapper = track(mount(GPIOResourceList, {
        props: { resources: manyGpio, configs: [], nodeId: 'node-1' },   // 全未配置
        global: { stubs },
      }))
      ;(wrapper.vm as unknown as { filter: string }).filter = 'configured'
      await wrapper.vm.$nextTick()
      expect(wrapper.findAll('[data-testid="gpio-resource-row"]')).toHaveLength(0)
      expect(wrapper.text()).toContain('没有已配置的 GPIO')
    })

    it('「无效配置」在设备**一个 GPIO 都没上报**时仍须显示（最需要它的场景）', () => {
      // 这是我重构时真踩过的回归：把 staleConfigs 嵌进 `resources.length > 0` 后，
      // 设备零上报时反而看不到孤儿配置 —— 而那正是它最该出现的时刻。
      const wrapper = track(mount(GPIOResourceList, {
        props: { resources: [], configs: [gpioConfig(9)], nodeId: 'node-1' },
        global: { stubs },
      }))
      expect(wrapper.text()).toContain('无效配置')
      expect(wrapper.text()).toContain('GPIO9')
    })
  })

  // ── D6 / 规范 §4.4.5：移动端触控目标 ≥44px ──────────────────────────
  //
  // 为什么用源码断言：happy-dom **不应用 @media 查询**，挂载级测不出移动端尺寸
  // （实测：删掉 44px 规则后全部挂载用例仍然通过 ⇒ 那条规则此前零覆盖）。
  // 这里钉住"移动端断点内确实抬到 44px"，桌面密度不受影响。
  describe('D6 移动端触控目标契约', () => {
    it.each([
      ['GPIOResourceList', gpioListSource],
      ['PWMResourceList', pwmListSource],
    ])('%s 在移动端断点内把操作按钮抬到 ≥44px', (_name, src) => {
      // 取最后一个 @media (max-width: 768px) 块（两份文件都只有一个移动端断点）
      const idx = src.indexOf('@media (max-width: 768px)')
      expect(idx, '未找到移动端断点').toBeGreaterThan(-1)
      const block = src.slice(idx, src.indexOf('</style>', idx))
      expect(block, '移动端断点内缺少 min-height: 44px（规范 §4.4.5 MUST）').toMatch(/min-height:\s*44px/)

      // 不仅要求"声明存在"，还要求**选择器足够强**：
      // theme.css:722 的 `.el-button--small:not(.is-circle):not(.is-link):not(.is-text)`
      // 特异性 (0,4,0)，会压过简单的 `.actions :deep(.el-button)` (0,3,0) ——
      // 我 2026-09-24 就是这样写了一个"存在但无效"的规则，源码断言当时没拦住。
      // 故这里要求选择器**包含 :not() 链**（复刻全局形制）。
      // 真实级联结果由 PeriphTouchTargetCascade.spec.ts 在计算值层面验收。
      const mq44 = block.match(/([^{}]*)\{[^{}]*min-height:\s*44px/)
      expect(mq44, '未找到 44px 规则的声明块').not.toBeNull()
      expect(
        mq44![1],
        '44px 规则的选择器缺少 :not() 链 —— 会被 theme.css 的 (0,4,0) 全局窄屏规则压过，规则形同虚设',
      ).toContain(':not(')
    })

    it('反证：桌面端**不得**被抬到 44px（那会让 8 行列表高度虚增）', () => {
      // 桌面基础规则里不应出现 44px 的按钮高度 —— 桌面是鼠标场景，规范只要求移动端。
      const desktopPart = gpioListSource.slice(0, gpioListSource.indexOf('@media (max-width: 768px)'))
      expect(desktopPart).not.toMatch(/\.actions\s+:deep\(\.el-button\)/)
    })
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
