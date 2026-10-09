/**
 * 缺陷回归（2026-10-03）：新建 UART 通道后「改波特率」报「bus_config 为空」。
 *
 * 旧行为：handleSubmit 组装 bus_config 时 `if (form.hardware_type === 'uart' && hw)`，
 * hw 找不到（资源能力尚未上报 / hardware_id 与资源 id 不完全相等）就**静默**留下空串并提交，
 * 落库一条"以后永远改不了波特率"的通道。用户当下看不到任何异常，等点「改波特率」才报错。
 *
 * 本文件锁两条：
 *   1. 资源能力缺失 ⇒ **拒绝提交**（不落空 bus_config），并给出可操作提示；
 *   2. 资源能力齐备 ⇒ 提交的 bus_config 是 10 字节合法 hex，波特率默认 9600（不是 115200）。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import source from '../ChannelManager.vue?raw'

const mocks = vi.hoisted(() => ({
  createChannel: vi.fn(),
  updateChannel: vi.fn(),
  syncConfig: vi.fn(),
  error: vi.fn(),
}))

vi.mock('@/api/channel', () => ({ channelApi: { create: vi.fn(), update: vi.fn() } }))
vi.mock('@/api/node', () => ({ nodeApi: { scanI2C: vi.fn(), syncConfig: mocks.syncConfig } }))
vi.mock('@/stores/channel', () => ({
  useChannelStore: () => ({ createChannel: mocks.createChannel, updateChannel: mocks.updateChannel }),
}))
vi.mock('@/utils/logger', () => ({ logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() } }))
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  // ElMessage 在真实 element-plus 里既是可调用函数（ElMessage(opts)）又带 .error/.success
  // 等快捷方法。utils/feedback 走的是**可调用**形态（feedback.error → ElMessage({...})，
  // 见 utils/feedback.ts:99），所以 mock 必须实现"可调用 + 带方法"两种形态；
  // 旧 mock 只挂了方法、本体不可调用，一旦调用方改走 feedback 就会
  // "ElMessage is not a function" 并以 unhandled rejection 形式漏出去。
  const message = Object.assign(
    (options: unknown) => {
      // feedback.error 传入 { message, type, ... }；把 message 交给断言用的 spy。
      const opts = options as { message?: unknown }
      mocks.error(String(opts?.message ?? ''))
      return { close: () => {} }
    },
    {
      ...actual.ElMessage,
      error: mocks.error,
      success: vi.fn(),
      warning: vi.fn(),
      info: vi.fn(),
    },
  )
  return {
    ...actual,
    ElMessage: message,
    ElMessageBox: { confirm: vi.fn(() => Promise.resolve()) },
  }
})

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
const FormItemStub = defineComponent({ props: { label: String, prop: String }, template: '<section><slot /></section>' })
const SelectStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: [String, Number], disabled: Boolean, placeholder: String },
  emits: ['update:modelValue', 'change'],
  template: '<div class="el-select" v-bind="$attrs"><slot /></div>',
})
const OptionStub = defineComponent({ props: { label: [String, Number], value: [String, Number, Boolean] }, template: '<option :value="value" />' })
const InputStub = defineComponent({ inheritAttrs: false, props: { modelValue: String }, emits: ['update:modelValue'], template: '<input v-bind="$attrs" />' })
const InputNumberStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: Number, min: Number, max: Number, step: Number },
  emits: ['update:modelValue'],
  template: '<input type="number" v-bind="$attrs" />',
})
const RadioGroupStub = defineComponent({ inheritAttrs: false, props: { modelValue: [String, Number, Boolean] }, emits: ['update:modelValue'], template: '<div v-bind="$attrs"><slot /></div>' })
const RadioButtonStub = defineComponent({ props: { value: [String, Number, Boolean] }, template: '<button><slot /></button>' })
const SwitchStub = defineComponent({ props: { modelValue: Boolean }, emits: ['update:modelValue'], template: '<input type="checkbox" :checked="modelValue" />' })
const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: { type: String, loading: Boolean, disabled: Boolean },
  emits: ['click'],
  template: '<button v-bind="$attrs" @click="$emit(\'click\')"><slot /></button>',
})
const IconStub = defineComponent({ template: '<i><slot /></i>' })

const components = {
  ElDialog: DialogStub, ElFormItem: FormItemStub, ElSelect: SelectStub, ElOption: OptionStub,
  ElInput: InputStub, ElInputNumber: InputNumberStub, ElRadioGroup: RadioGroupStub,
  ElRadioButton: RadioButtonStub, ElSwitch: SwitchStub, ElButton: ButtonStub, ElIcon: IconStub,
}
const stubs = { ElForm: FormStub }

beforeEach(() => vi.clearAllMocks())

async function mountAndSubmit(props: Record<string, unknown>) {
  const wrapper = mount(ChannelManager, {
    props: { modelValue: false, collectorId: "node-1", ...props },
    global: { components: components as Record<string, any>, stubs: stubs as Record<string, any> },
  })
  await wrapper.setProps({ modelValue: true })
  const submit = wrapper.findAll("button").find(b => b.text() === "创建" || b.text() === "保存")
  if (!submit) throw new Error("未找到创建/保存按钮")
  await submit.trigger("click")
  await flushPromises()
  return wrapper
}

describe("ChannelManager 不得静默产出空 bus_config（2026-10-03）", () => {
  it("资源能力未上报时拒绝提交，并给出可操作提示", async () => {
    await mountAndSubmit({ presetHardwareType: "uart", presetHardwareId: "UART1", capabilities: { buses: { uart: [] } } })
    expect(mocks.createChannel, "能力缺失时不得提交（旧行为是静默落空 bus_config）").not.toHaveBeenCalled()
    expect(mocks.error).toHaveBeenCalledTimes(1)
    const message = String(mocks.error.mock.calls[0][0])
    expect(message).toContain("尚未上报")
    expect(message).toContain("UART")
  })

  it("i2c/spi 同样受保护（它们的 bus_config 也是后续配置的唯一数据源）", async () => {
    await mountAndSubmit({ presetHardwareType: "i2c", presetHardwareId: "I2C0", capabilities: { buses: { i2c: [] } } })
    expect(mocks.createChannel).not.toHaveBeenCalled()
    expect(mocks.error).toHaveBeenCalled()
  })

  // ===================================================================
  // 编辑态回归（2026-10-03 前端审查发现的 P1）
  //
  // 原判据 `needsBusConfig && !hw` 对**编辑已有通道**同样生效。
  // 编辑入口（NodeOverview 通道行「配置」）不检查能力是否加载，
  // 能力拉取失败时 capabilities 被置成 {buses:{}} ⇒ hw 找不到 ⇒
  // 连"改个通道名/改轮询周期"都保存不了 —— 把「改不了波特率」
  // 换成了「连名字都改不了」，比原缺陷更糟。
  //
  // 本用例是**用户可见效果**断言：编辑已有 UART 通道时，
  // 即使 capabilities 完全缺失，也必须能提交更新，且**不得**报错。
  //
  // 它凭什么会失败：把守卫的 `&& !isEditingExistingChannel` 去掉，
  // updateChannel 会变成 0 次调用且 error 被调用，本用例红。
  // ===================================================================
  it("编辑已有 UART 通道时，能力缺失也必须允许保存（P1 回归）", async () => {
    const existingChannel = {
      id: 42,
      node_id: "node-1",
      name: "现场 UART1",
      hardware_type: "UART",
      bus_type: "UART",
      hardware_id: "UART1",
      bus_config: "1415000012C008010000",
      enabled: true,
      interval_ms: 5000,
      config: {},
    }
    // 能力完全缺失：模拟"能力拉取失败/节点离线"时的真实状态。
    await mountAndSubmit({ initialData: existingChannel, capabilities: { buses: {} } })
    expect(
      mocks.error,
      "编辑已有通道时不得因能力缺失而报错：那是把「改不了波特率」换成「连名字都改不了」",
    ).not.toHaveBeenCalled()
    expect(
      mocks.updateChannel,
      "编辑已有通道必须能提交更新（不得被资源能力守卫拦下）",
    ).toHaveBeenCalledTimes(1)
  })

  it("编辑已有通道时不得把已存在的 bus_config 清空", async () => {
    const existingChannel = {
      id: 7,
      node_id: "node-1",
      name: "现场 UART1",
      hardware_type: "UART",
      bus_type: "UART",
      hardware_id: "UART1",
      bus_config: "1415000012C008010000",
      enabled: true,
      interval_ms: 5000,
      config: {},
    }
    await mountAndSubmit({ initialData: existingChannel, capabilities: { buses: {} } })
    const payload = mocks.updateChannel.mock.calls[0]?.[1] as Record<string, any> | undefined
    // 能力缺失时组装不出新的 bus_config，必须**不带该字段**（保持 undefined），
    // 让后端 PUT 保留原值；绝不能显式传空串（那会造出改不了波特率的通道）。
    if (payload && "bus_config" in payload) {
      expect(
        payload.bus_config,
        "编辑态不得把 bus_config 写成空串（后端会因此判定无法重配波特率）",
      ).not.toBe("")
    }
  })

  it("能力齐备时提交 10 字节合法 bus_config，默认波特率 9600（不是 115200）", async () => {
    await mountAndSubmit({
      presetHardwareType: "uart",
      presetHardwareId: "UART1",
      capabilities: { buses: { uart: [{ id: "UART1", default_tx_pin: 20, default_rx_pin: 21, max_baud: 5000000 }] } },
    })
    expect(mocks.createChannel).toHaveBeenCalledTimes(1)
    const data = mocks.createChannel.mock.calls[0][0] as Record<string, any>
    const hex = String(data.bus_config || "")
    expect(hex, "bus_config 不得为空").not.toBe("")
    // 7 字节：tx + rx + baud(BE32) + DMA flags。
    // 固件把 byte6 读作 DMA flags（bus_dma.h），不是 data_bits —— 见
    // ChannelManager.vue 里的布局说明。旧写法写 data_bits=8 ⇒ 0x08&0x01==0
    // ⇒ 静默关闭 DMA。
    expect(hex).toMatch(/^[0-9A-F]{14}$/)
    expect(hex.slice(0, 4)).toBe("1415")
    expect(Number.parseInt(hex.slice(4, 12), 16), "默认波特率必须是 9600").toBe(9600)
    // DMA 2026-10-09 (user requirement): DMA defaults OFF, user enables it manually.
    //   User: "C6 S3 all UARTs: only one can use DMA at a time"
    //         "Principle: DMA defaults off, configured manually by the user"
    // So a newly created channel must write byte6 = 0x00.
    // The old assertion (& 0x01).toBe(1) locked the OLD behavior.
    expect(Number.parseInt(hex.slice(12, 14), 16), "byte6 must NOT enable DMA by default").toBe(0)
  })

  it("源码级防回退：不得再出现 115200 默认值，且守卫覆盖需要 bus_config 的总线", () => {
    const codeOnly = source
      .replace(/<!--[\s\S]*?-->/g, "")
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/^\s*\/\/.*$/gm, "")
    // 只针对**默认值**的两个原站点做精确防回退（115200 仍可作为步长出现在 computeBaudStep，
    // 那是既有语义，不属于本次缺陷）。
    expect(codeOnly, "表单默认值不得再硬编码 115200").not.toContain("baud_rate = 115200")
    expect(codeOnly, "提交兜底不得再硬编码 115200").not.toContain("baud_rate || 115200")
    expect(codeOnly).toContain("needsBusConfig")
    expect(codeOnly).toContain("DEFAULT_UART_BAUD_RATE")
  })
})
