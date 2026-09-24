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
  gpioCreate: vi.fn(),
  gpioUpdate: vi.fn(),
  pwmCreate: vi.fn(),
  pwmUpdate: vi.fn(),
  channelList: vi.fn(),
  subscribe: vi.fn(),
  unsubscribe: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  // feedback.error()/handleError() 走 ElMessage({...}) 函数式调用
  message: vi.fn(),
  // G10：confirmDanger 的底层；默认 resolve（用户确认）
  confirm: vi.fn(() => Promise.resolve()),
}))
const wsState = vi.hoisted(() => ({ connected: false }))

vi.mock('@/api/node', () => ({ nodeApi: { getCapabilities: mocks.getCapabilities } }))
vi.mock('@/api/periph', () => ({
  gpioApi: { list: mocks.gpioList, delete: mocks.gpioDelete, create: mocks.gpioCreate, update: mocks.gpioUpdate },
  pwmApi: {
    list: mocks.pwmList, delete: mocks.pwmDelete,
    create: mocks.pwmCreate, update: mocks.pwmUpdate,
  },
}))
vi.mock('@/api/channel', () => ({ channelApi: { getList: mocks.channelList } }))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    get connected() { return wsState.connected },
    subscribe: mocks.subscribe,
  }),
}))
vi.mock('element-plus', () => ({
  ElMessage: Object.assign(mocks.message, { success: mocks.success, error: mocks.error, warning: vi.fn() }),
  // G10：移除 GPIO/PWM 配置改走 feedback.confirmDanger（底层 ElMessageBox.confirm）。
  // 默认 resolve = 用户确认；取消分支用 mockRejectedValueOnce 驱动。
  ElMessageBox: { confirm: mocks.confirm },
}))

// PeripheralControl 显式 import 子组件；使用模块 mock（而非 global.stubs）确保替换生效。
vi.mock('@/components/periph/GPIOResourceList.vue', () => ({
  default: {
    name: 'GPIOResourceList',
    props: ['resources', 'configs', 'nodeId', 'offline', 'loading', 'occupiedPins'],
    emits: ['configure', 'edit', 'remove'],
    template: `<div class="gpio-list" :data-count="resources.length" :data-offline="String(offline)" :data-occupied="[...occupiedPins.keys()].sort((a,b)=>a-b).join(',')">
      <span v-for="item in resources" :key="item.id" class="gpio-resource">{{ item.id }}</span>
      <button class="configure-gpio" @click="$emit('configure', 2)">configure gpio</button>
      <button class="remove-gpio" @click="$emit('remove', 2)">remove gpio</button>
    </div>`,
  },
}))
vi.mock('@/components/periph/PWMResourceList.vue', () => ({
  default: {
    name: 'PWMResourceList',
    props: ['resources', 'configs', 'nodeId', 'offline', 'loading', 'availablePins'],
    emits: ['configure', 'edit', 'remove'],
    template: `<div class="pwm-list" :data-count="resources.length" :data-pins="availablePins.join(',')">
      <span v-for="item in resources" :key="item.id" class="pwm-resource">{{ item.id }}</span>
      <button class="configure-pwm" @click="$emit('configure', 'PWM1')">configure pwm</button>
      <button class="remove-pwm" @click="$emit('remove', 'PWM0')">remove pwm</button>
    </div>`,
  },
}))

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
  ElButton: ButtonStub,
  ElIcon: defineComponent({ template: '<i><slot /></i>' }),
  ElAlert: defineComponent({ props: ['title'], template: '<div class="alert">{{ title }}<slot /></div>' }),
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

function mountControl(
  offline = false,
  onConfigureGpio?: (pin: number) => void,
  onConfigurePwm?: (hardwareId: string) => void,
) {
  return track(mount(PeripheralControl, {
    props: { nodeId: 'node-1', offline, onConfigureGpio, onConfigurePwm } as any,
    global: { stubs },
  }))
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

  it('passes offline state and notifies parent with independent configuration identities', async () => {
    const onConfigureGpio = vi.fn()
    const onConfigurePwm = vi.fn()
    const wrapper = mountControl(true, onConfigureGpio, onConfigurePwm)
    await flushPromises()

    expect(wrapper.get('.gpio-list').attributes('data-offline')).toBe('true')
    await wrapper.get('.configure-gpio').trigger('click')
    await wrapper.get('.configure-pwm').trigger('click')
    expect(onConfigureGpio).toHaveBeenCalledWith(2)
    expect(onConfigurePwm).toHaveBeenCalledWith('PWM1')
  })

  it('deletes PWM by hardware_id and reloads capabilities and configs', async () => {
    mocks.pwmDelete.mockResolvedValue(undefined)
    mocks.confirm.mockResolvedValue(undefined)
    const wrapper = mountControl()
    await flushPromises()
    mocks.getCapabilities.mockClear()

    await wrapper.get('.remove-pwm').trigger('click')
    await flushPromises()

    // G10：删除前必须先经危险确认（后端是硬删 + 设备侧 DECONFIG，不可逆）
    expect(mocks.confirm, '移除 PWM 配置前必须先确认').toHaveBeenCalled()
    expect(mocks.pwmDelete).toHaveBeenCalledWith('node-1', 'PWM0')
    expect(mocks.getCapabilities).toHaveBeenCalledWith('node-1')
  })

  it('G10：移除 PWM 配置时用户取消 → 不得发删除请求', async () => {
    const wrapper = mountControl()
    await flushPromises()
    mocks.pwmDelete.mockClear()
    mocks.confirm.mockRejectedValueOnce(new Error('cancel'))

    await wrapper.get('.remove-pwm').trigger('click')
    await flushPromises()

    expect(mocks.pwmDelete).not.toHaveBeenCalled()
  })

  it('G10：移除 GPIO 配置同样要先确认；确认后才删除并重载', async () => {
    mocks.gpioDelete.mockResolvedValue(undefined)
    mocks.confirm.mockResolvedValue(undefined)
    const wrapper = mountControl()
    await flushPromises()
    mocks.getCapabilities.mockClear()

    await wrapper.get('.remove-gpio').trigger('click')
    await flushPromises()

    expect(mocks.confirm, '移除 GPIO 配置前必须先确认').toHaveBeenCalled()
    expect(mocks.gpioDelete).toHaveBeenCalledWith('node-1', 2)
    expect(mocks.getCapabilities).toHaveBeenCalledWith('node-1')
  })

  it('G10：移除 GPIO 配置时用户取消 → 不得发删除请求', async () => {
    const wrapper = mountControl()
    await flushPromises()
    mocks.gpioDelete.mockClear()
    mocks.confirm.mockRejectedValueOnce(new Error('cancel'))

    await wrapper.get('.remove-gpio').trigger('click')
    await flushPromises()

    expect(mocks.gpioDelete).not.toHaveBeenCalled()
  })

  it('shows a retryable error when any required resource request fails', async () => {
    mocks.getCapabilities.mockRejectedValue(new Error('network'))
    const wrapper = mountControl()
    await flushPromises()

    expect(wrapper.text()).toContain('资源数据加载失败')
    // I-1: 失败走统一出口。注意 extractErrorMessage 的语义：第二个参数是「兜底」而非「前缀」——
    // error 自带 message 时以它为准，因此这里展示 'network'（旧实现是 '加载外设资源失败: network'）。
    expect(mocks.message).toHaveBeenCalledWith(expect.objectContaining({
      message: 'network',
      type: 'error',
      duration: 5000,
    }))
  })

  it('I-1: 加载失败时优先展示服务端 message', async () => {
    mocks.getCapabilities.mockRejectedValue(
      Object.assign(new Error('Request failed with status code 500'), {
        response: { data: { message: '节点 7 的 GPIO 驱动未就绪' } },
      }),
    )
    mountControl()
    await flushPromises()

    // 关键判据：后端具体原因必须可见（旧实现只显示本地 error.message）
    expect(mocks.message).toHaveBeenCalledWith(expect.objectContaining({
      message: '节点 7 的 GPIO 驱动未就绪',
      type: 'error',
      duration: 5000,
    }))
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

  // ── 外设「配置」能力必须真的可达（C5 能力迁移的核心断言）──────────────
  //
  // 背景：GPIO/PWM 配置的**创建/编辑**在整仓只由死文件 ChannelPanel 实现，
  // 生产侧因此完全不可达（设备上报的引脚能列出，但点「配置」无处可去）。
  // 本组件现自带 PeripheralConfigDialog（与 ChannelPanel 同形的共享实现），
  // 下面这些用例锁住"点了真的能配"，防止将来退回"只 emit 不落地"。
  describe('外设配置可达性（C5 能力迁移）', () => {
    // 对话框由真实 el-dialog 渲染并 **teleport 到 document.body** ——
    // 若 mount 不 attachTo，teleport 目标之外的内容不会被渲染，
    // 断言会看到 null（这不是"表单没渲染"，而是"我没挂到文档上"）。
    // 故本组用例统一 attachTo document.body，并在 afterEach 清理。
    function mountAttached(offline = false) {
      const wrapper = track(mount(PeripheralControl, {
        props: { nodeId: 'node-1', offline } as any,
        global: { stubs },
        attachTo: document.body,
      }))
      return wrapper
    }
    const findSubmit = (testid: string) =>
      Array.from(document.querySelectorAll('button'))
        .find(b => b.getAttribute('data-testid') === testid) as HTMLButtonElement | undefined

    afterEach(() => { document.body.innerHTML = '' })

    it('点「配置 GPIO」打开表单；提交后调用 gpioApi.create 并带 pin', async () => {
      mocks.getCapabilities.mockResolvedValue({ buses: { gpio: [{ id: 'GPIO2', pin: 2, enabled: true }], pwm: [] } })
      mocks.gpioList.mockResolvedValue([])   // 未配置 ⇒ 走 create 分支
      mocks.pwmList.mockResolvedValue([])
      mocks.gpioCreate.mockResolvedValue(undefined)

      const wrapper = mountAttached()
      await flushPromises()
      await wrapper.get('.configure-gpio').trigger('click')
      await flushPromises()

      // 表单必须真的出现（而不是只 emit 一个事件给父组件）
      expect(document.body.querySelector('[data-testid="gpio-direction"]'), 'GPIO 配置表单未渲染').not.toBeNull()

      const submit = findSubmit('submit-gpio')
      expect(submit, '缺少提交按钮').toBeTruthy()
      submit!.click()
      await flushPromises()

      expect(mocks.gpioCreate, '应走 create（该引脚尚无配置）').toHaveBeenCalledWith(
        'node-1',
        expect.objectContaining({ pin: 2 }),
      )
    })

    it('已配置的引脚点「编辑」走 gpioApi.update 而非 create', async () => {
      mocks.getCapabilities.mockResolvedValue({ buses: { gpio: [{ id: 'GPIO2', pin: 2, enabled: true }], pwm: [] } })
      mocks.gpioList.mockResolvedValue([gpioConfig(2)])   // 已配置 ⇒ 走 update
      mocks.pwmList.mockResolvedValue([])
      mocks.gpioUpdate.mockResolvedValue(undefined)

      const wrapper = mountAttached()
      await flushPromises()
      await wrapper.get('.configure-gpio').trigger('click')
      await flushPromises()

      findSubmit('submit-gpio')!.click()
      await flushPromises()

      expect(mocks.gpioUpdate).toHaveBeenCalledWith('node-1', 2, expect.anything())
      expect(mocks.gpioCreate, '编辑不得走 create（否则会 409 或重复配置）').not.toHaveBeenCalled()
    })

    it('PWM「配置」同样开表单并走 pwmApi.create（带 hardware_id）', async () => {
      mocks.getCapabilities.mockResolvedValue({ buses: { gpio: [], pwm: [{ id: 'PWM1', channel: 1, timer_count: 4, max_resolution_bits: 14 }] } })
      mocks.gpioList.mockResolvedValue([])
      mocks.pwmList.mockResolvedValue([])
      mocks.pwmCreate.mockResolvedValue(undefined)

      const wrapper = mountAttached()
      await flushPromises()
      await wrapper.get('.configure-pwm').trigger('click')
      await flushPromises()

      expect(document.body.querySelector('[data-testid="pwm-pin"]'), 'PWM 配置表单未渲染').not.toBeNull()
      findSubmit('submit-pwm')!.click()
      await flushPromises()

      expect(mocks.pwmCreate).toHaveBeenCalledWith(
        'node-1',
        expect.objectContaining({ hardware_id: 'PWM1' }),
      )
    })

    it('离线时不得写配置（提交被拦下并提示）', async () => {
      mocks.getCapabilities.mockResolvedValue({ buses: { gpio: [{ id: 'GPIO2', pin: 2, enabled: true }], pwm: [] } })
      mocks.gpioList.mockResolvedValue([])
      mocks.pwmList.mockResolvedValue([])

      const wrapper = mountAttached(true)   // offline
      await flushPromises()
      await wrapper.get('.configure-gpio').trigger('click')
      await flushPromises()

      findSubmit('submit-gpio')!.click()
      await flushPromises()

      expect(mocks.gpioCreate, '离线态不得发写请求').not.toHaveBeenCalled()
    })
  })
})
