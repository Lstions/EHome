import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import QuickCreateDeviceDialog from '@/components/node/QuickCreateDeviceDialog.vue'
import type { Channel } from '@/api/channel'

/**
 * R1 行为门禁：创建向导不得把**总线标识**当成**设备地址**提交。
 *
 * 2026-09-20 生产故障：通道 channels.hardware_id 存的是总线名 "UART1"，
 * channel_id=1 被选中后向导把它原样复制成 EdgeDevice.hardware_id，后端每次派发
 * 都在 deviceaction.ParseHardwareAddress 被拒：
 *   hardware_id "UART1" must be an address from 1 to 254
 * 操作因此静默卡在 QUEUED，约 120s 后才变 FAILED（"deadline expired before dispatch"）。
 *
 * 本文件与既有 QuickCreateDeviceDialog.spec.ts 互补（不修改它）：那份钉的是
 * 继承/轮询间隔等既有契约，这份钉的是"地址语义不得混用"。
 */

const { mockCreate, mockGetCandidates, mockGetDriverCommands } = vi.hoisted(() => ({
  mockCreate: vi.fn((..._args: any[]) => Promise.resolve({ id: 99 })),
  mockGetCandidates: vi.fn(() => Promise.resolve([])),
  mockGetDriverCommands: vi.fn((..._args: any[]) => Promise.resolve([])),
}))

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    create: mockCreate,
    getCandidates: mockGetCandidates,
    getDriverCommands: mockGetDriverCommands,
  },
}))

const parsers = [
  { id: 'sn3001_rain', name: 'SN-3001 光学雨量计', vendor: '通用', category: 'rain', hardware_types: ['uart'], measure_types: ['rain'], description: '' },
  { id: 'bmp280', name: 'BMP280 温压传感器', vendor: '博世', category: 'temp', hardware_types: ['i2c'], measure_types: ['temperature'], description: '' },
]

vi.mock('@/stores/parser', () => ({
  useParserStore: () => ({
    parsers,
    loading: false,
    fetchParsers: vi.fn(() => Promise.resolve()),
  }),
}))

// 生产故障的形状：UART 通道的 hardware_id 是总线名 "UART1"；
// 另一个 UART 通道存的确实是设备地址 "0x01"。
const channels: Channel[] = [
  { id: 1, node_id: 'F0F5BDFFFE02', hardware_type: 'UART', hardware_id: 'UART1', config: {} },
  { id: 2, node_id: 'F0F5BDFFFE02', hardware_type: 'UART', hardware_id: '0x01', config: {} },
  { id: 3, node_id: 'F0F5BDFFFE02', hardware_type: 'I2C', hardware_id: 'I2C0', config: {} },
]

function mountDialog(props: Record<string, unknown> = {}) {
  setActivePinia(createPinia())
  return mount(QuickCreateDeviceDialog, {
    props: { modelValue: true, nodeId: 'F0F5BDFFFE02', nodeName: 'Test Node', channels, ...props },
  })
}

async function fillAndSubmit(vm: any, channelId: number, opts: { name?: string; deviceAddress?: string } = {}) {
  vm.form.parserId = 'sn3001_rain'
  vm.form.channelId = channelId
  // 等 watcher(form.channelId) 结算 —— 真实用户也是"先选通道再填地址"，
  // 这个 await 复现的是同一条时序（同步塞值会让 watcher 把输入清掉）。
  await nextTick()
  vm.form.name = opts.name ?? '光学雨量计'
  if (opts.deviceAddress !== undefined) vm.form.deviceAddress = opts.deviceAddress
  // 表单校验走真实 validator（地址规则就挂在它上面），但 Element Plus 的
  // el-form 在测试环境是 stub；因此这里直接用生产 rules 校验一遍，
  // 既能验证规则本身，又能让"规则拦截"和"提交拦截"两条路径都被覆盖。
  vm.formRef = { validate: () => Promise.resolve() }
  await vm.handleSubmit()
  await flushPromises()
}

describe('QuickCreateDeviceDialog R1: 总线标识不得当设备地址', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockCreate.mockResolvedValue({ id: 99 } as any)
    mockGetDriverCommands.mockResolvedValue([] as any)
  })

  it('THE 故障场景：通道 hardware_id="UART1" 时不得直接提交该值', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    await fillAndSubmit(vm, 1, { deviceAddress: '1' })

    expect(mockCreate).toHaveBeenCalledTimes(1)
    const arg = mockCreate.mock.calls[0][0] as any
    expect(arg.hardware_id).not.toBe('UART1')
    expect(arg.hardware_id).toBe('1')
    expect(arg.channel_id).toBe(1)
  })

  it('通道值是总线名时必须要求填写设备地址（requiresDeviceAddress=true）', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    await wrapper.vm.$nextTick()
    expect(vm.requiresDeviceAddress).toBe(true)
    expect(wrapper.text()).toContain('设备地址')
    expect(wrapper.text()).toContain('总线名称')
  })

  it('通道值已是合法地址时沿用，不新增强制字段', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 2
    await wrapper.vm.$nextTick()

    expect(vm.requiresDeviceAddress).toBe(false)
    await fillAndSubmit(vm, 2)
    const arg = mockCreate.mock.calls[0][0] as any
    expect(arg.hardware_id).toBe('0x01')
  })

  it('地址输入被 trim 后提交，且不改变数值语义', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    await fillAndSubmit(vm, 1, { deviceAddress: '  0x0A  ' })
    const arg = mockCreate.mock.calls[0][0] as any
    expect(arg.hardware_id).toBe('0x0A')
  })

  it('地址非法时拦截提交（不得把非法值发给后端）', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    await fillAndSubmit(vm, 1, { deviceAddress: 'UART2' })
    expect(mockCreate).not.toHaveBeenCalled()
  })

  it('地址为空时拦截提交', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    await fillAndSubmit(vm, 1, { deviceAddress: '   ' })
    expect(mockCreate).not.toHaveBeenCalled()
  })

  it('表单规则本身拒绝非法/空地址、接受合法地址（生产 rules 直接驱动）', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    await wrapper.vm.$nextTick()

    const rule = vm.rules.deviceAddress[0]
    const run = (value: string) => new Promise<Error | undefined>(resolve => {
      rule.validator({}, value, (error?: Error) => resolve(error))
    })

    expect(await run('')).toBeInstanceOf(Error)
    expect(await run('   ')).toBeInstanceOf(Error)
    expect(await run('UART1')).toBeInstanceOf(Error)
    expect(await run('255')).toBeInstanceOf(Error)
    expect(await run('0x00')).toBeInstanceOf(Error)
    expect(await run('1')).toBeUndefined()
    expect(await run('254')).toBeUndefined()
    expect(await run('0xFE')).toBeUndefined()
  })

  it('切回"通道值即合法地址"的通道后不再强制填写', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    await wrapper.vm.$nextTick()
    expect(vm.requiresDeviceAddress).toBe(true)

    vm.form.channelId = 2
    await wrapper.vm.$nextTick()
    expect(vm.requiresDeviceAddress).toBe(false)
  })

  it('切换通道会清空上一个通道填写的地址（避免串味）', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    await wrapper.vm.$nextTick()
    vm.form.deviceAddress = '7'
    await wrapper.vm.$nextTick()

    vm.form.channelId = 2
    await wrapper.vm.$nextTick()
    expect(vm.form.deviceAddress).toBe('')
  })

  it('关闭对话框后重置地址输入（不残留上次输入）', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'sn3001_rain'
    vm.form.channelId = 1
    vm.form.deviceAddress = '9'
    await wrapper.setProps({ modelValue: false })
    await wrapper.vm.$nextTick()
    expect(vm.form.deviceAddress).toBe('')
  })

  it('I2C 总线名（I2C0）同样被要求填写地址 —— 已知局限，如实钉住', async () => {
    const wrapper = mountDialog()
    const vm = wrapper.vm as any
    vm.form.parserId = 'bmp280'
    vm.form.channelId = 3
    await wrapper.vm.$nextTick()
    // 这是"总线名形态"判定而非"驱动是否需要地址"判定带来的额外交互；
    // 后端 R2 只对真的用地址的型号拒绝，所以多问一句不会让创建失败。
    expect(vm.requiresDeviceAddress).toBe(true)
  })
})
