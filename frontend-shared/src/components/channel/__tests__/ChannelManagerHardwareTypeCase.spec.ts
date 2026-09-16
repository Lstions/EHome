/**
 * 缺陷回归（写侧）：生产库 channels.hardware_type 为大写（唯一约定 'UART'/'I2C'/…），
 * 但 ChannelManager 提交时只把 bus_type 转大写：
 *   hardware_type: form.hardware_type        // 'uart'
 *   bus_type: form.hardware_type.toUpperCase() // 'UART'
 * 后端 handler_device.go 的校验用 strings.EqualFold（大小写不敏感）⇒ 通过；
 * 写入 tx.Create(&ch) 原样落库、不做 ToUpper ⇒ 同一列混入小写行。
 *
 * 本用例是**行为断言**：真实点「创建」把 payload 抓出来，断言两个字段同一大小写约定。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import type { Channel } from '@/api/channel'

const mocks = vi.hoisted(() => ({
  createChannel: vi.fn(),
  updateChannel: vi.fn(),
  syncConfig: vi.fn(),
}))

vi.mock('@/api/channel', () => ({ channelApi: { create: vi.fn(), update: vi.fn() } }))
vi.mock('@/api/node', () => ({ nodeApi: { scanI2C: vi.fn(), syncConfig: mocks.syncConfig } }))
vi.mock('@/stores/channel', () => ({
  useChannelStore: () => ({ createChannel: mocks.createChannel, updateChannel: mocks.updateChannel }),
}))
vi.mock('@/utils/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import ChannelManager from '@/components/channel/ChannelManager.vue'

const DialogStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: Boolean, title: String, width: String },
  emits: ['update:modelValue', 'closed'],
  template: '<div class="el-dialog" v-if="modelValue"><slot /><slot name="footer" /></div>',
})

const FormStub = defineComponent({
  props: { model: Object, rules: Object, labelPosition: String, disabled: Boolean },
  setup(_, { expose }) {
    expose({ validate: () => Promise.resolve(true) })
  },
  template: '<form><slot /></form>',
})

const FormItemStub = defineComponent({
  props: { label: String, prop: String },
  template: '<section><slot /></section>',
})

const SelectStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: [String, Number], disabled: Boolean, placeholder: String },
  emits: ['update:modelValue', 'change'],
  template: '<div class="el-select" v-bind="$attrs"><slot /></div>',
})

const OptionStub = defineComponent({
  props: { label: [String, Number], value: [String, Number, Boolean] },
  template: '<option :value="value" />',
})

const InputStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: String },
  emits: ['update:modelValue'],
  template: '<input v-bind="$attrs" />',
})

const InputNumberStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: Number, min: Number, max: Number, step: Number },
  emits: ['update:modelValue'],
  template: '<input type="number" v-bind="$attrs" />',
})

const RadioGroupStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: [String, Number, Boolean] },
  emits: ['update:modelValue'],
  template: '<div v-bind="$attrs"><slot /></div>',
})

const RadioButtonStub = defineComponent({
  props: { value: [String, Number, Boolean] },
  template: '<button><slot /></button>',
})

const SwitchStub = defineComponent({
  props: { modelValue: Boolean },
  emits: ['update:modelValue'],
  template: '<input type="checkbox" :checked="modelValue" />',
})

const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: { type: String, loading: Boolean, disabled: Boolean },
  emits: ['click'],
  template: '<button v-bind="$attrs" @click="$emit(\'click\')"><slot /></button>',
})

const IconStub = defineComponent({ template: '<i><slot /></i>' })

// 注意：SFC 里的 <el-form> 被 unplugin-vue-components 编译成显式 import，
// VTU 的 global.components 覆盖不了显式导入的组件名（实测仍渲染 test-setup 的 ElForm stub，
// 它不 expose validate ⇒ handleSubmit 第一行就 return）。
// 因此这里用 stubs 选项把显式导入的 ElForm 换成可 expose validate 的 stub。
const components = {
  ElDialog: DialogStub,
  ElFormItem: FormItemStub,
  ElSelect: SelectStub,
  ElOption: OptionStub,
  ElInput: InputStub,
  ElInputNumber: InputNumberStub,
  ElRadioGroup: RadioGroupStub,
  ElRadioButton: RadioButtonStub,
  ElSwitch: SwitchStub,
  ElButton: ButtonStub,
  ElIcon: IconStub,
}

const stubs = { ElForm: FormStub }

interface ManagerProps {
  modelValue: boolean
  collectorId: string | number
  capabilities?: unknown
  initialData?: Partial<Channel> | null
  presetHardwareType?: string
  presetHardwareId?: string
  readonly?: boolean
}

const baseProps = (overrides: Partial<ManagerProps> = {}): ManagerProps => ({
  modelValue: false,
  collectorId: 'node-1',
  ...overrides,
})

// 每个用例都必须清 mock：否则第二个用例的 calls[0] 仍是**上一个用例**的调用，
// 断言会拿 UART 的数据去比 I2C 的期望（实测报 expected 'UART' to be 'I2C'）。
beforeEach(() => {
  vi.clearAllMocks()
})

const mountManager = async (props: ManagerProps) => {
  const wrapper = mount(ChannelManager, {
    props,
    global: { components: components as Record<string, any>, stubs: stubs as Record<string, any> },
  })
  await wrapper.setProps({ modelValue: true })
  return wrapper
}

async function clickPrimarySubmit(wrapper: { findAll(selector: string): any[] }): Promise<void> {
  const buttons = wrapper.findAll('button')
  const submit = buttons.find(button => button.text() === '创建' || button.text() === '保存')
  if (!submit) throw new Error('未找到创建/保存按钮')
  await submit.trigger('click')
  await flushPromises()
}

describe('ChannelManager 写侧枚举大小写约定（后端存储约定为大写）', () => {
  it('创建 UART 通道时 hardware_type 与 bus_type 同为后端约定的大写', async () => {
    const wrapper = await mountManager(baseProps({ presetHardwareType: 'uart', presetHardwareId: 'UART1' }))
    await clickPrimarySubmit(wrapper)

    expect(mocks.createChannel).toHaveBeenCalledTimes(1)
    const data = mocks.createChannel.mock.calls[0][0] as Record<string, unknown>
    expect(data.hardware_type).toBe('UART')
    expect(data.bus_type).toBe('UART')
    expect(String(data.hardware_type)).toBe(String(data.bus_type))
  })

  it('创建 I2C 通道时同样是统一大写（不是只转 bus_type）', async () => {
    const wrapper = await mountManager(baseProps({ presetHardwareType: 'i2c', presetHardwareId: 'I2C0' }))
    await clickPrimarySubmit(wrapper)

    const data = mocks.createChannel.mock.calls[0][0] as Record<string, unknown>
    expect(data.hardware_type).toBe('I2C')
    expect(String(data.hardware_type)).toBe(String(data.bus_type))
  })
})
