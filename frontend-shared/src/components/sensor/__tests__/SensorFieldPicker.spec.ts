import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SensorFieldPicker from '../SensorFieldPicker.vue'
import pickerSource from '../SensorFieldPicker.vue?raw'
import { sensorFieldApi } from '@/api/sensorField'
import type {
  SensorField,
  SensorFieldCatalog,
  SensorFieldResolution,
  SensorFieldSchema,
} from '@/api/sensorField'

// Element Plus 组件由 src/test-setup.ts 全局 stub（ElSelect 渲染为原生 select、
// ElOption 渲染为原生 option），这里只 mock 数据层与日志层。
vi.mock('@/api/sensorField', () => ({
  sensorFieldApi: {
    listFields: vi.fn(),
    resolve: vi.fn(),
    getSchema: vi.fn(),
    listCategories: vi.fn(),
  },
}))

const { mockLoggerWarn } = vi.hoisted(() => ({ mockLoggerWarn: vi.fn() }))
vi.mock('@/utils/logger', () => ({
  logger: { warn: mockLoggerWarn, info: vi.fn(), error: vi.fn(), debug: vi.fn() },
}))

const mockedApi = vi.mocked(sensorFieldApi)

/** 造一条字段目录项；未给出的按"数值、无中文名、从未上报"填默认。 */
function makeField(partial: Partial<SensorField> & { name: string }): SensorField {
  return {
    unit: '',
    label_zh: '',
    value_type: 'number',
    precision: 0,
    sources: ['obs'],
    semantics: [],
    first_seen: null,
    last_seen: null,
    samples: 1,
    deprecated: false,
    ...partial,
  }
}

function catalog(fields: SensorField[], truncated = false): SensorFieldCatalog {
  return { edge_device_id: 10, device_type: 'bms', truncated, fields }
}

function schema(overrides: Partial<SensorFieldSchema> = {}): SensorFieldSchema {
  return {
    selector_version: 1,
    aggregates: ['any', 'all', 'max', 'min', 'avg', 'sum'],
    aggregate_default: 'any',
    virtual_fns: [],
    error_codes: [
      { code: 'invalid_selector_syntax', http: 400 },
      { code: 'unsupported_aggregate', http: 400 },
      { code: 'resolution_unbound', http: 200 },
    ],
    aliases: [],
    limits: { max_selector_chars: 64, max_family_size: 64, max_virtual_depth: 4 },
    ...overrides,
  }
}

function resolution(overrides: Partial<SensorFieldResolution> = {}): SensorFieldResolution {
  return {
    selector: '',
    status: 'resolved',
    kind: 'literal',
    aggregate: '',
    matched: ['temperature_1'],
    ambiguous: false,
    reason: '',
    ...overrides,
  }
}

const TEMPERATURE = makeField({
  name: 'temperature_1',
  label_zh: '温度探头 1',
  unit: '°C',
  last_seen: new Date(Date.now() - 12_000).toISOString(),
})
const SOC = makeField({ name: 'soc', label_zh: '电量', unit: '%' })
const PROTECTION = makeField({ name: 'protection_status', label_zh: '保护状态', value_type: 'enum' })
const CELLS = Array.from({ length: 16 }, (_, index) => makeField({
  name: 'cell_voltage_' + (index + 1),
  label_zh: '电芯电压 ' + (index + 1),
  unit: 'V',
}))

const wrappers: Array<{ unmount: () => void }> = []

interface MountOverrides {
  modelValue?: string
  deviceId?: number
  disabled?: boolean
  placeholder?: string
  allowCreate?: boolean
}

function mountPicker(overrides: MountOverrides = {}) {
  const wrapper = mount(SensorFieldPicker, {
    props: {
      modelValue: overrides.modelValue ?? '',
      deviceId: 'deviceId' in overrides ? overrides.deviceId : 10,
      disabled: overrides.disabled ?? false,
      placeholder: overrides.placeholder ?? '',
      allowCreate: overrides.allowCreate ?? true,
    },
  })
  wrappers.push(wrapper)
  return wrapper
}

async function mountReady(overrides: MountOverrides = {}) {
  const wrapper = mountPicker(overrides)
  await flushPromises()
  return wrapper
}

/** 主下拉里的真实候选项（排除 ElSelect stub 自带的那条 value='' 占位项）。 */
function fieldOptions(wrapper: ReturnType<typeof mountPicker>) {
  return wrapper.findAll('option.el-option')
}

function optionTexts(wrapper: ReturnType<typeof mountPicker>) {
  return fieldOptions(wrapper).map(option => option.text())
}

function findOption(wrapper: ReturnType<typeof mountPicker>, needle: string) {
  const found = fieldOptions(wrapper).find(option => option.text().includes(needle))
  if (!found) throw new Error('未找到选项 "' + needle + '"，实际有：' + optionTexts(wrapper).join(' | '))
  return found
}

function mainSelect(wrapper: ReturnType<typeof mountPicker>) {
  return wrapper.find('[data-test="sensor-field-select"] select')
}

beforeEach(() => {
  vi.clearAllMocks()
  mockedApi.listFields.mockResolvedValue(catalog([TEMPERATURE, SOC, PROTECTION]))
  mockedApi.getSchema.mockResolvedValue(schema())
  mockedApi.resolve.mockResolvedValue(resolution())
  mockedApi.listCategories.mockResolvedValue([{ code: 'temperature_1', unit: '°C' }])
})

afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  vi.useRealTimers()
})

describe('未选设备', () => {
  it('显示「请先选择边缘设备」且一个请求都不发', async () => {
    const wrapper = mountPicker({ deviceId: undefined })
    await flushPromises()
    expect(wrapper.find('[data-test="sensor-field-picker"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="sensor-field-need-device"]').text()).toContain('请先选择边缘设备')
    expect(fieldOptions(wrapper)).toHaveLength(0)
    // edge_device_id 是必填 query：没设备就发请求只会拿到 400/404
    expect(mockedApi.listFields).not.toHaveBeenCalled()
    expect(mockedApi.getSchema).not.toHaveBeenCalled()
    expect(mockedApi.listCategories).not.toHaveBeenCalled()
  })
})

describe('类型门禁：默认只列数值，非数值可见但不可选', () => {
  it('计数「数值 2 / 全部 3」，数值模式下非数值字段不出现', async () => {
    const wrapper = await mountReady()
    expect(wrapper.find('[data-test="sensor-mode-numeric"]').text()).toContain('数值 2')
    expect(wrapper.find('[data-test="sensor-mode-all"]').text()).toContain('全部 3')
    const texts = optionTexts(wrapper)
    expect(texts.some(text => text.includes('temperature_1'))).toBe(true)
    expect(texts.some(text => text.includes('soc'))).toBe(true)
    expect(texts.some(text => text.includes('protection_status'))).toBe(false)
  })

  it('切「全部」后非数值项可见、disabled、并标注「非数值，阈值比较不生效」', async () => {
    const wrapper = await mountReady()
    await wrapper.find('[data-test="sensor-mode-all"]').trigger('click')

    expect(fieldOptions(wrapper)).toHaveLength(3)
    const protection = findOption(wrapper, 'protection_status')
    expect(protection.attributes('disabled')).toBeDefined()
    expect(protection.text()).toContain('非数值，阈值比较不生效')
    // 数值项在「全部」模式下依然可选
    expect(findOption(wrapper, 'temperature_1').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-test="sensor-nonnumeric-note"]').exists()).toBe(true)
  })

  it('选项文案 = 中文名 + 物理名 + 单位（中文名与物理名同串，中英文搜索都能命中）', async () => {
    const wrapper = await mountReady()
    const temperature = findOption(wrapper, 'temperature_1')
    expect(temperature.text()).toContain('温度探头 1')
    expect(temperature.text()).toContain('temperature_1')
    expect(temperature.text()).toContain('°C')
  })

  it('deprecated 字段标注「已弃用」（灰显类名挂在下拉项上）', async () => {
    mockedApi.listFields.mockResolvedValue(catalog([
      makeField({ name: 'old_temperature', label_zh: '旧温度', unit: '°C', deprecated: true }),
    ]))
    const wrapper = await mountReady()
    expect(findOption(wrapper, 'old_temperature').text()).toContain('已弃用')
    expect(pickerSource).toContain('sensor-field-option--deprecated')
  })
})

describe('字段族 + 聚合（词表只来自 /schema）', () => {
  it('16 节电芯聚成一条「电芯电压族(16)」', async () => {
    mockedApi.listFields.mockResolvedValue(catalog(CELLS))
    const wrapper = await mountReady()
    const family = fieldOptions(wrapper).find(option => option.text().includes('电芯电压族(16)'))
    expect(family, optionTexts(wrapper).join(' | ')).toBeTruthy()
    // 下拉内部伪值，不会作为选择器提交
    expect(family?.attributes('value')).toBe('family:cell_voltage')
    // 16 个成员各自仍然可选（族只是"多字段聚合"的入口，不隐藏单节）
    const memberValues = fieldOptions(wrapper)
      .map(option => String(option.attributes('value') ?? ''))
      .filter(value => /^cell_voltage_\d+$/.test(value))
    expect(memberValues).toHaveLength(16)
  })

  it('选中族 ⇒ 产出带 schema 默认聚合的选择器；聚合候选逐项等于 schema.aggregates', async () => {
    mockedApi.listFields.mockResolvedValue(catalog(CELLS))
    // 刻意让默认值 = max（不是家喻户晓的 any）：证明确实读 aggregate_default
    mockedApi.getSchema.mockResolvedValue(schema({
      aggregates: ['any', 'max', 'sum'],
      aggregate_default: 'max',
    }))
    const wrapper = await mountReady()

    await mainSelect(wrapper).setValue('family:cell_voltage')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['cell_voltage_*:max'])
    // 族选择器必须是 _*:聚合（点号形态是语法错误，规则会静默死掉）
    expect(String(wrapper.emitted('update:modelValue')?.at(-1)?.[0])).not.toContain('*.')

    // 父组件回写后才出现聚合下拉（v-model 是唯一真相，组件不另存一份状态）
    await wrapper.setProps({ modelValue: 'cell_voltage_*:max' })
    const aggregate = wrapper.find('[data-test="sensor-aggregate"]')
    expect(aggregate.exists()).toBe(true)
    const aggregateSelect = aggregate.find('select')
    const aggregateValues = aggregateSelect.findAll('option')
      .map(option => option.attributes('value'))
      .filter(value => value !== '' && value !== undefined)
    expect(aggregateValues).toEqual(['any', 'max', 'sum'])
    expect((aggregateSelect.element as HTMLSelectElement).value).toBe('max')
    expect(aggregate.text()).toContain('cell_voltage_*:max')

    // 每个聚合一句人话 + 下方缺员语义
    expect(aggregateSelect.findAll('option').map(option => option.text()))
      .toEqual(expect.arrayContaining(['any · 任意一个', 'max · 最高', 'sum · 合计']))
    const gap = wrapper.find('[data-test="sensor-aggregate-gap"]')
    expect(gap.text()).toContain('all / avg / sum 缺员即不触发')
    expect(gap.text()).toContain('min / max 仍按现有成员求值并标降级')

    // 改聚合只改聚合段
    await aggregateSelect.setValue('sum')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['cell_voltage_*:sum'])
  })

  it('/schema 拿不到聚合词表时族条目不可选（不在前端硬编码 any/max）', async () => {
    mockedApi.listFields.mockResolvedValue(catalog(CELLS))
    mockedApi.getSchema.mockRejectedValue(Object.assign(new Error('schema down'), { status: 503 }))
    const wrapper = await mountReady()
    const family = fieldOptions(wrapper).find(option => option.text().includes('电芯电压族(16)'))
    expect(family?.attributes('disabled')).toBeDefined()
    expect(family?.text()).toContain('聚合词表不可用')
    expect(mockLoggerWarn).toHaveBeenCalledWith(expect.stringContaining('schema'), expect.anything())
  })
})

describe('change-field：具体字段给对象，族/手输给 null', () => {
  it('选中字面量字段', async () => {
    const wrapper = await mountReady()
    await mainSelect(wrapper).setValue('temperature_1')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['temperature_1'])
    expect(wrapper.emitted('change-field')?.at(-1)?.[0]).toMatchObject({ name: 'temperature_1', unit: '°C' })
  })

  it('选中字段族', async () => {
    mockedApi.listFields.mockResolvedValue(catalog(CELLS))
    const wrapper = await mountReady()
    await mainSelect(wrapper).setValue('family:cell_voltage')
    expect(wrapper.emitted('change-field')?.at(-1)).toEqual([null])
  })
})

describe('语义别名分组（仅 /schema.aliases 非空时渲染）', () => {
  it('空别名表 ⇒ 整块不渲染，也没有 @ 开头的选项', async () => {
    const wrapper = await mountReady()
    expect(wrapper.find('[data-test="sensor-alias-group"]').exists()).toBe(false)
    expect(fieldOptions(wrapper).some(option => String(option.attributes('value') ?? '').startsWith('@'))).toBe(false)
  })

  it('有别名 ⇒ 渲染 @@别名 分组，值为 @soc', async () => {
    mockedApi.getSchema.mockResolvedValue(schema({
      aliases: [{ semantic: 'soc', candidates: ['rsoc', 'soc'], expected_unit: '%' }],
    }))
    const wrapper = await mountReady()
    const group = wrapper.find('[data-test="sensor-alias-group"]')
    expect(group.exists()).toBe(true)
    // ElOptionGroup stub 把 label 透传成属性（真实 EP 里它是分组标题「@@别名」）
    expect(group.attributes('label')).toBe('@@别名')
    const alias = fieldOptions(wrapper).find(option => option.attributes('value') === '@soc')
    expect(alias?.text()).toContain('rsoc')

    await mainSelect(wrapper).setValue('@soc')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['@soc'])
    expect(wrapper.emitted('change-field')?.at(-1)).toEqual([null])
  })
})

describe('保存前校验（400ms 防抖 + 三态文案）', () => {
  it('resolved ⇒ 灰字「解析到 N 个字段」，多候选时说明已归一', async () => {
    mockedApi.resolve.mockResolvedValue(resolution({
      status: 'resolved',
      ambiguous: true,
      matched: ['cell_voltage_1', 'cell_voltage_2', 'cell_voltage_3'],
    }))
    const wrapper = await mountReady()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    await wrapper.setProps({ modelValue: 'cell_voltage_*:max' })
    await vi.advanceTimersByTimeAsync(400)
    await wrapper.vm.$nextTick()

    const status = wrapper.find('[data-test="sensor-resolve-status"]')
    expect(status.attributes('data-status')).toBe('resolved')
    expect(status.text()).toContain('解析到 3 个字段')
    expect(status.text()).toContain('已归一为 cell_voltage_1')
    expect(status.classes()).toContain('sf-status--resolved')
    expect(mockedApi.resolve).toHaveBeenCalledWith(10, 'cell_voltage_*:max')
  })

  it('unbound ⇒ 黄字「当前解析不到，保存后可能永不触发」，且不阻止输入', async () => {
    mockedApi.resolve.mockResolvedValue(resolution({
      status: 'unbound',
      matched: [],
      reason: 'resolution_unbound',
    }))
    const wrapper = await mountReady()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    await wrapper.setProps({ modelValue: 'temperatuer' })
    await vi.advanceTimersByTimeAsync(400)
    await wrapper.vm.$nextTick()

    const status = wrapper.find('[data-test="sensor-resolve-status"]')
    expect(status.attributes('data-status')).toBe('unbound')
    expect(status.text()).toContain('当前解析不到，保存后可能永不触发')
    expect(status.classes()).toContain('sf-status--unbound')
    // 软失败：输入控件仍可用（与后端 warnings 口径一致，硬拦会让用户无法提前配规则）
    expect(mainSelect(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('400 + 契约表内的 error_code ⇒ 红字人话（不露后端英文原串）', async () => {
    mockedApi.resolve.mockRejectedValue(Object.assign(new Error('unsupported aggregate: median'), {
      status: 400,
      errorCode: 'unsupported_aggregate',
    }))
    const wrapper = await mountReady()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    await wrapper.setProps({ modelValue: 'cell_voltage_*:median' })
    await vi.advanceTimersByTimeAsync(400)
    await wrapper.vm.$nextTick()

    const status = wrapper.find('[data-test="sensor-resolve-status"]')
    expect(status.attributes('data-status')).toBe('error')
    expect(status.text()).toContain('不支持的聚合方式')
    expect(status.text()).not.toContain('unsupported aggregate: median')
    expect(status.classes()).toContain('sf-status--error')
  })

  it('400 + 契约表外的 error_code ⇒ 回落后端 message（未知码不硬翻）', async () => {
    mockedApi.resolve.mockRejectedValue(Object.assign(new Error('backend says no'), {
      status: 400,
      errorCode: 'brand_new_code',
    }))
    const wrapper = await mountReady()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    await wrapper.setProps({ modelValue: 'whatever' })
    await vi.advanceTimersByTimeAsync(400)
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-test="sensor-resolve-status"]').text()).toContain('backend says no')
  })

  it('400ms 防抖：连续改值只打一次 resolve，最终值为准', async () => {
    const wrapper = await mountReady()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    await wrapper.setProps({ modelValue: 'cell' })
    await vi.advanceTimersByTimeAsync(200)
    await wrapper.setProps({ modelValue: 'cell_voltage_1' })
    await vi.advanceTimersByTimeAsync(200)
    expect(mockedApi.resolve).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(250)
    expect(mockedApi.resolve).toHaveBeenCalledTimes(1)
    expect(mockedApi.resolve).toHaveBeenCalledWith(10, 'cell_voltage_1')
  })
})

describe('三级降级链', () => {
  it('字段目录 503 ⇒ 降级到已上报类别（仍可选中），并留 warn', async () => {
    mockedApi.listFields.mockRejectedValue(Object.assign(new Error('service unavailable'), { status: 503 }))
    mockedApi.listCategories.mockResolvedValue([
      { code: 'cell_voltage_1', unit: 'V' },
      { code: 'soc', unit: '%' },
    ])
    const wrapper = await mountReady()

    expect(mockedApi.listCategories).toHaveBeenCalledWith(10)
    expect(wrapper.find('[data-test="sensor-fallback-note"]').exists()).toBe(true)
    const texts = optionTexts(wrapper)
    expect(texts.some(text => text.includes('cell_voltage_1') && text.includes('V'))).toBe(true)
    expect(texts.some(text => text.includes('soc'))).toBe(true)
    expect(mockLoggerWarn).toHaveBeenCalledWith(expect.stringContaining('降级到设备已上报类别'), expect.anything())
  })

  it('目录与类别都失败 ⇒ 退化为纯手输 + 指定提示文案，且不抛未处理异常', async () => {
    mockedApi.listFields.mockRejectedValue(new Error('network down'))
    mockedApi.listCategories.mockRejectedValue(new Error('network down'))
    const wrapper = await mountReady()

    expect(wrapper.find('[data-test="sensor-manual-note"]').text())
      .toContain('字段目录不可用，可手工输入物理名')
    expect(wrapper.find('[data-test="sensor-field-select"]').exists()).toBe(false)
    const manual = wrapper.find('[data-test="sensor-field-manual"]')
    expect(manual.exists()).toBe(true)
    // 手输仍然可用（不阻塞建规则），且给出 null 字段对象
    await manual.setValue('temperature_1')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['temperature_1'])
    expect(wrapper.emitted('change-field')?.at(-1)).toEqual([null])
    expect(mockLoggerWarn).toHaveBeenCalledTimes(2)
  })
})

describe('设备切换与手动刷新', () => {
  it('deviceId 变化自动重载，旧设备字段不残留；schema 成功过不再重复拉', async () => {
    const wrapper = await mountReady()
    expect(mockedApi.listFields).toHaveBeenCalledTimes(1)
    expect(mockedApi.getSchema).toHaveBeenCalledTimes(1)

    mockedApi.listFields.mockResolvedValue(catalog([makeField({ name: 'only_new_device', unit: 'V' })]))
    await wrapper.setProps({ deviceId: 11 })
    await flushPromises()

    expect(mockedApi.listFields).toHaveBeenLastCalledWith(11)
    expect(mockedApi.getSchema).toHaveBeenCalledTimes(1)
    const texts = optionTexts(wrapper)
    expect(texts.some(text => text.includes('only_new_device'))).toBe(true)
    expect(texts.some(text => text.includes('temperature_1'))).toBe(false)
  })

  it('defineExpose({ reload }) 支持手动刷新', async () => {
    const wrapper = await mountReady()
    expect(mockedApi.listFields).toHaveBeenCalledTimes(1)
    const vm = wrapper.vm as unknown as { reload: () => Promise<void> }
    await vm.reload()
    await flushPromises()
    expect(mockedApi.listFields).toHaveBeenCalledTimes(2)
  })
})

describe('a11y / data-test / 样式约定', () => {
  it('el-select 带 aria-label；四个 data-test 钩子齐备', async () => {
    const wrapper = await mountReady()
    expect(wrapper.find('[data-test="sensor-field-picker"]').exists()).toBe(true)
    expect(mainSelect(wrapper).attributes('aria-label')).toBe('传感器字段选择器')
    // ElOption stub 不 spread attrs（test-setup.ts:130-139），选项级 data-test 只能对源码断言；
    // 其余两个钩子（sensor-aggregate / sensor-resolve-status）在上面的用例里已按 DOM 断言过。
    expect(pickerSource).toContain('data-test="sensor-field-option"')
    expect(pickerSource).toContain('data-test="sensor-aggregate"')
    expect(pickerSource).toContain('data-test="sensor-resolve-status"')
    expect(pickerSource).toContain('aria-label="传感器字段选择器"')
  })

  it('宽度 100%、触控高度 ≥36px、不自带页面级断点', () => {
    expect(pickerSource).toContain('width: 100%')
    expect(pickerSource).toContain('min-height: 36px')
    // 注释里可以提到 @media（解释为什么不写断点），但不得出现真正的断点规则
    expect(pickerSource).not.toMatch(/@media\s*\(/)
  })
})
