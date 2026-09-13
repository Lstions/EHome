import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import DeviceConfigForm from '@/components/forms/DeviceConfigForm.vue'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'
import driverApi from '@/api/driver'

// 只替换网络取数，保留 transformToCascaderOptions / flattenDrivers 的真实实现（纯函数）。
vi.mock('@/api/driver', async importOriginal => {
  const actual = await importOriginal<typeof import('@/api/driver')>()
  return { ...actual, default: { ...actual.default, getDriverTree: vi.fn() } }
})

vi.mock('@/api/deviceConfig', () => ({
  deviceConfigApi: {
    getList: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    setDefault: vi.fn(),
  },
}))

vi.mock('element-plus', async importOriginal => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() },
  }
})

const mockedDriverTree = vi.mocked(driverApi.getDriverTree)
const mockedUpdate = vi.mocked(deviceConfigApi.update)

/**
 * 覆盖 src/test-setup.ts 的通用 ElCascader：通用 stub 渲染默认插槽时不传作用域参数，
 * 会命中模板里的 `#default="{ data }"` 解构而抛错；这里按 Element Plus 的级联选择器契约
 * 补上 { data } 作用域，并保留 v-model/options 等入参，使「有没有渲染级联选择器」这一
 * DOM 事实可以被真实断言。
 */
const ElCascaderStub = defineComponent({
  name: 'ElCascader',
  props: {
    modelValue: { type: [String, Number, Array], default: undefined },
    options: { type: Array, default: () => [] },
    props: { type: Object, default: () => ({}) },
    placeholder: String,
    clearable: Boolean,
    filterable: Boolean,
  },
  emits: ['update:modelValue', 'change'],
  setup(props, { slots }) {
    return () => h('div', { class: 'el-cascader' }, [
      h('input', { class: 'el-cascader__input', value: String(props.modelValue ?? '') }),
      ...(slots.default ? [slots.default({ data: { label: 'stub', value: 'stub', hardware_types: [] } })] : []),
    ])
  },
})

/** 驱动树：OEM → 种类 → 驱动（与 GET /device-configs/tree 的 DriverTreeNode 形状一致）。 */
const driverTreeFixture = [
  {
    id: '威盟士',
    name: '威盟士',
    children: [
      {
        id: '雨量传感器',
        name: '雨量传感器',
        drivers: [
          {
            type: 'sn3001_rain',
            model: 'SN3001',
            display_name: '雨量传感器',
            hardware_types: ['uart'],
            description: '',
          },
        ],
      },
    ],
  },
]

/** 编辑态模板：device_type 是创建时选定的驱动型号。 */
const editConfig: DeviceConfig = {
  id: 7,
  name: '雨量模板',
  description: '',
  device_type: 'sn3001_rain',
  protocol: 'modbus',
  hardware_type: 'uart',
  config: { baudrate: 9600 },
  is_default: false,
  status: 'active',
  created_at: '',
  updated_at: '',
}

/** 表单底部的提交按钮（唯一 primary 按钮）。 */
const submitButton = 'button.el-button--primary'

/** 关闭态挂载后打开：与生产一致（DeviceConfigList 以 visible=false 常驻，点击才置 true）。 */
async function mountForm(config: DeviceConfig | null): Promise<VueWrapper<InstanceType<typeof DeviceConfigForm>>> {
  const wrapper = mount(DeviceConfigForm, {
    props: { visible: false, config },
    global: { components: { ElCascader: ElCascaderStub, 'el-cascader': ElCascaderStub } },
  })
  await wrapper.setProps({ visible: true })
  await flushPromises()
  return wrapper
}

/** el-form 在轻量 stub 下不执行 rules，按仓内既有约定注入 validate 通过桩。 */
function stubFormValidate(wrapper: VueWrapper<InstanceType<typeof DeviceConfigForm>>) {
  ;(wrapper.vm as unknown as { formRef: { validate: () => Promise<boolean> } }).formRef = {
    validate: () => Promise.resolve(true),
  }
}

describe('DeviceConfigForm 驱动字段（创建专属 vs 编辑只读）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockedDriverTree.mockResolvedValue(driverTreeFixture)
    mockedUpdate.mockResolvedValue(editConfig)
  })

  it('创建态渲染可编辑的「传感器驱动」级联选择器', async () => {
    const wrapper = await mountForm(null)

    const cascader = wrapper.find('.el-cascader')
    expect(cascader.exists()).toBe(true)
    expect(cascader.element.closest('.el-form-item')?.textContent).toContain('传感器驱动')
    // 只读回显只属于编辑态
    expect(wrapper.find('[data-test="driver-readonly"]').exists()).toBe(false)
  })

  it('编辑态不渲染驱动级联选择器，改为只读回显并注明创建后不可修改', async () => {
    const wrapper = await mountForm(editConfig)

    expect(wrapper.find('.el-cascader').exists()).toBe(false)
    const readonly = wrapper.find('[data-test="driver-readonly"]')
    expect(readonly.exists()).toBe(true)
    expect(readonly.text()).toContain('sn3001_rain')
    expect(readonly.text()).toContain('创建后不可修改')
  })

  it('编辑态保存不提交 driverPath，且保留配置自身的 device_type', async () => {
    const wrapper = await mountForm(editConfig)
    stubFormValidate(wrapper)

    await wrapper.find(submitButton).trigger('click')
    await flushPromises()

    expect(mockedUpdate).toHaveBeenCalledTimes(1)
    const [id, payload] = mockedUpdate.mock.calls[0]
    expect(id).toBe(7)
    // 后端 models.DeviceConfig 没有 driverPath 字段：提交它只会被静默忽略。
    expect(Object.prototype.hasOwnProperty.call(payload, 'driverPath')).toBe(false)
    expect(payload.device_type).toBe('sn3001_rain')
    expect(payload.name).toBe('雨量模板')
  })

  it('创建态保存仍携带用户选定的驱动型号 device_type', async () => {
    const mockedCreate = vi.mocked(deviceConfigApi.create)
    mockedCreate.mockResolvedValue(editConfig)
    const wrapper = await mountForm(null)
    stubFormValidate(wrapper)

    const form = (wrapper.vm as unknown as { form: { device_type: string; name: string } }).form
    form.name = '新模板'
    form.device_type = 'sn3001_rain'

    await wrapper.find(submitButton).trigger('click')
    await flushPromises()

    expect(mockedCreate).toHaveBeenCalledTimes(1)
    const payload = mockedCreate.mock.calls[0][0]
    expect(payload.device_type).toBe('sn3001_rain')
    expect(Object.prototype.hasOwnProperty.call(payload, 'driverPath')).toBe(false)
  })
})
