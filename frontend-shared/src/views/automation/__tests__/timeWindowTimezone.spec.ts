import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h, reactive } from 'vue'
import type { AutomationRule } from '@/api/automation'
import { emptyForm, useRuleForm, type RuleFormModel } from '../composables/useRuleForm'
import TriggerFields from '../components/TriggerFields.vue'

/**
 * time_window 时区选择（2026-11-18）。
 *
 * 背景：后端窗口求值用的是进程时区，生产容器是 UTC；东八区用户配 08:00–18:00
 * 实际 16:00–02:00 生效。后端改为按规则自带时区解释后，前端的责任是：默认值、
 * 必填、编辑回显、把 timezone 提交上去 —— 四条都在这里钉住。
 *
 * 断言口径：默认值/校验读**组合式函数的真实返回值**（用户点保存时页面读的就是它），
 * 控件只在 TriggerFields 单测里看真实渲染，不重复挂整页 —— 页面级接线已由
 * AutomationRules.spec.ts 的既有用例覆盖。
 */

// 浏览器时区随机器/CI 容器漂移（UTC 容器 vs 东八区开发机），直接断言
// Intl.resolvedOptions() 会让用例与运行环境同源、永远为真。这里只固定「默认值
// 的来源」；词表、合法性、UTC 偏移仍走真实实现（见 timezone.spec.ts）。
const { FIXED_BROWSER_TZ } = vi.hoisted(() => ({ FIXED_BROWSER_TZ: 'America/New_York' }))
vi.mock('../timezone', async importOriginal => {
  const actual = await importOriginal<typeof import('../timezone')>()
  return { ...actual, browserTimeZone: () => FIXED_BROWSER_TZ }
})

/** 一条后端形状的规则（只填用例关心的字段，其余给 emptyForm 同源的合法默认值）。 */
function makeRule(overrides: Partial<AutomationRule> = {}): AutomationRule {
  return {
    id: 7,
    name: '时间窗规则',
    enabled: true,
    trigger_type: 'time_window',
    cooldown_sec: 300,
    max_daily_exec: 0,
    require_confirmed: false,
    action_type: 'notification',
    action_level: 'info',
    created_at: '2026-11-18T00:00:00Z',
    updated_at: '2026-11-18T00:00:00Z',
    ...overrides,
  }
}

function makeForm() {
  return useRuleForm({ catalogDefinition: () => null, resetSaveDiagnostics: () => {} })
}

describe('time_window 时区：默认值与编辑回显', () => {
  it('新建时切到时间窗口 → 默认填浏览器时区（不是留空等用户猜）', () => {
    const api = makeForm()
    api.openCreate()
    api.triggerSelection.value = 'time_window'
    api.onTriggerTypeChange()
    expect(api.form.timezone).toBe(FIXED_BROWSER_TZ)
  })

  it('切到时间窗口不覆盖已有值（如从日出日落规则带过来的定位时区）', () => {
    const api = makeForm()
    api.openCreate()
    api.triggerSelection.value = 'time_window'
    api.form.timezone = 'Asia/Tokyo'
    api.onTriggerTypeChange()
    expect(api.form.timezone).toBe('Asia/Tokyo')
  })

  it('切到日出/日落不会凭空写 timezone（sun_event 仍由定位结果回填）', () => {
    const api = makeForm()
    api.openCreate()
    api.triggerSelection.value = 'sunrise'
    api.onTriggerTypeChange()
    expect(api.form.timezone, 'sun_event 的时区只能来自定位结果').toBe('')
  })

  it('编辑已有 time_window 规则回显已保存的 timezone', () => {
    const api = makeForm()
    api.openEdit(makeRule({ timezone: 'Europe/Berlin' }))
    expect(api.form.timezone).toBe('Europe/Berlin')
  })

  it('存量 time_window 没带时区 → 补浏览器时区（必填字段不能空着打开）', () => {
    const api = makeForm()
    api.openEdit(makeRule())
    expect(api.form.timezone).toBe(FIXED_BROWSER_TZ)
  })

  it('sun_event 的 timezone 不被默认值污染：没有定位结果时仍为空', () => {
    const api = makeForm()
    api.openEdit(makeRule({ trigger_type: 'sun_event', trigger_sun_kind: 'sunset' }))
    expect(api.form.timezone).toBe('')
  })
})

describe('time_window 时区：必填校验与提交体', () => {
  function validTimeWindowForm() {
    const api = makeForm()
    api.openCreate()
    api.triggerSelection.value = 'time_window'
    api.onTriggerTypeChange()
    api.form.name = '白天开窗'
    api.form.trigger_window_start = '08:00'
    api.form.trigger_window_end = '18:00'
    return api
  }

  it('时区为空时阻止提交，文案明确说「时间窗口必须选择时区」', () => {
    const api = validTimeWindowForm()
    api.form.timezone = ''
    expect(api.validate()).toBe('时间窗口必须选择时区')
    expect(api.validateStep(2), '向导第 2 步也必须拦住').toBe('时间窗口必须选择时区')
  })

  it('非法 IANA 名同样拦住（手改坏 / 别的工具写入的存量值）', () => {
    const api = validTimeWindowForm()
    api.form.timezone = 'Asia/Shangai'
    expect(api.validate()).toBe('时区无效：请选择 IANA 时区（如 Asia/Shanghai）')
  })

  it('时区合法后触发步骤通过，且提交体带 timezone', () => {
    const api = validTimeWindowForm()
    api.form.timezone = 'Asia/Shanghai'
    expect(api.validateStep(2)).toBeNull()
    const payload = api.buildPayload()
    expect(payload.timezone).toBe('Asia/Shanghai')
    expect(payload.trigger_window_start).toBe('08:00')
  })

  it('sensor_threshold 的提交体仍不带 timezone（不改变既有契约）', () => {
    const api = makeForm()
    api.openCreate()
    api.form.name = '高温'
    api.form.timezone = 'Asia/Shanghai'
    expect(api.buildPayload().timezone).toBeUndefined()
  })
})

// ── 控件渲染：只在 time_window 出现、可搜索、文案与回写正确 ──────────────────

/** 捕获 props 的 el-select 替身：全局 stub 不暴露 filterable，而「可搜索」是需求本身。 */
const CapturingElSelect = defineComponent({
  name: 'CapturingElSelect',
  props: {
    modelValue: { type: [String, Number, Array], default: '' },
    filterable: Boolean,
    placeholder: String,
    disabled: Boolean,
  },
  emits: ['update:modelValue', 'change'],
  setup(props, { slots, emit }) {
    const onChange = (event: Event) => {
      const value = (event.target as HTMLSelectElement).value
      emit('update:modelValue', value)
      emit('change', value)
    }
    return () => h('select', {
      class: 'el-select',
      value: String(props.modelValue ?? ''),
      disabled: props.disabled,
      onChange,
    }, slots.default?.())
  },
})

const CapturingElOption = defineComponent({
  name: 'CapturingElOption',
  props: { value: { type: [String, Number], required: true }, label: String },
  setup(props) {
    return () => h('option', { class: 'el-option', value: String(props.value) }, props.label ?? String(props.value))
  },
})

/** 时间选择器替身：本用例不关心时间控件，但缺了它会渲染成未知元素并刷警告。 */
const ElTimePickerStub = defineComponent({
  name: 'ElTimePickerStub',
  props: {
    modelValue: { type: String, default: '' },
    format: String,
    valueFormat: String,
    clearable: Boolean,
    placeholder: String,
  },
  emits: ['update:modelValue', 'change'],
  setup(props, { emit }) {
    const update = (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value)
    return () => h('input', { class: 'el-time-picker', value: props.modelValue, onInput: update })
  },
})

function timeWindowForm(): RuleFormModel {
  const form = reactive(emptyForm()) as RuleFormModel
  form.trigger_type = 'time_window'
  form.timezone = 'Asia/Shanghai'
  form.trigger_window_start = '08:00'
  form.trigger_window_end = '18:00'
  return form
}

async function mountFields(form: RuleFormModel) {
  const wrapper = mount(TriggerFields, {
    props: {
      form,
      devices: [],
      sourceGroupOptions: [],
      selectedSourceGroup: undefined,
      triggerSelection: form.trigger_type,
      thresholdUnit: '',
      thresholdPrecision: 3,
      windowStartFixNote: '',
      windowEndFixNote: '',
      isOvernightWindow: false,
      isSunTrigger: form.trigger_type === 'sun_event',
      conditionRows: [],
      conditionsRawMode: false,
      conditionsRawInvalid: false,
      previewLoading: false,
      previewDisabled: false,
      previewHint: '',
      previewResult: null,
    },
    global: {
      components: {
        ElSelect: CapturingElSelect,
        'el-select': CapturingElSelect,
        ElOption: CapturingElOption,
        'el-option': CapturingElOption,
        ElTimePicker: ElTimePickerStub,
        'el-time-picker': ElTimePickerStub,
      },
    },
  })
  await flushPromises()
  return wrapper
}

type FieldsWrapper = Awaited<ReturnType<typeof mountFields>>

function timezoneSelect(wrapper: FieldsWrapper) {
  const found = wrapper.findAllComponents(CapturingElSelect)
    .find(candidate => candidate.attributes('data-test') === 'field-window-timezone')
  expect(found, '时间窗没有渲染时区下拉').toBeTruthy()
  return found!
}

describe('TriggerFields：时间窗口时区控件', () => {
  it('只在 time_window 渲染；日出/日落与传感器阈值都没有这个控件', async () => {
    const windowWrapper = await mountFields(timeWindowForm())
    expect(windowWrapper.find('[data-test="field-window-timezone"]').exists()).toBe(true)

    const sunForm = reactive(emptyForm()) as RuleFormModel
    sunForm.trigger_type = 'sun_event'
    sunForm.trigger_sun_kind = 'sunset'
    const sunWrapper = await mountFields(sunForm)
    expect(sunWrapper.find('[data-test="field-window-timezone"]').exists()).toBe(false)
    // 2026-11-14 裁决的既有行为不变：日出日落没有手选时区的入口
    expect(sunWrapper.find('[data-test="field-timezone"]').exists()).toBe(false)

    const sensorWrapper = await mountFields(reactive(emptyForm()) as RuleFormModel)
    expect(sensorWrapper.find('[data-test="field-window-timezone"]').exists()).toBe(false)
  })

  it('可搜索（filterable），且选项来自 Intl.supportedValuesOf 而不是手写十几个', async () => {
    const wrapper = await mountFields(timeWindowForm())
    const select = timezoneSelect(wrapper)
    expect(select.props('filterable'), '下拉不能搜索：几百个 IANA 时区没法用').toBe(true)

    const values = wrapper.find('[data-test="field-window-timezone"]')
      .findAll('option').map(option => option.attributes('value'))
    expect(values.length).toBeGreaterThan(100)
    expect(values).toContain('Asia/Shanghai')
    expect(values).toContain('UTC')
  })

  it('选项文案带 UTC 偏移；用户选择写回 form.timezone', async () => {
    const form = timeWindowForm()
    const wrapper = await mountFields(form)
    const select = wrapper.find('[data-test="field-window-timezone"]')
    const shanghai = select.findAll('option').find(option => option.attributes('value') === 'Asia/Shanghai')
    expect(shanghai?.text()).toBe('Asia/Shanghai（UTC+08:00）')

    await select.setValue('Asia/Tokyo')
    expect(form.timezone).toBe('Asia/Tokyo')
  })

  it('一句人话说明窗口按所选时区解释，避免用户以为是浏览器/服务器时间', async () => {
    const wrapper = await mountFields(timeWindowForm())
    const hint = wrapper.find('[data-test="window-timezone-hint"]')
    expect(hint.exists(), '没有口径说明：用户会按浏览器/服务器时间理解这个窗口').toBe(true)
    expect(hint.text()).toContain('按所选时区解释')
    expect(hint.text()).toContain('与日出日落规则同一口径')
    expect(hint.text()).toContain('不是浏览器或服务器本地时间')
  })

  it('存量/别名时区不在词表里时仍渲染为选中项（用户必须看得到当前值）', async () => {
    const form = timeWindowForm()
    form.timezone = 'Legacy/Nowhere'
    const wrapper = await mountFields(form)
    const values = wrapper.find('[data-test="field-window-timezone"]')
      .findAll('option').map(option => option.attributes('value'))
    expect(values).toContain('Legacy/Nowhere')
  })
})
