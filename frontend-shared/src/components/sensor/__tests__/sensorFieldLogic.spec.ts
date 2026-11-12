import { describe, it, expect } from 'vitest'
import type { SensorField, SensorFieldErrorCode } from '@/api/sensorField'
import {
  AGGREGATE_BRIEF,
  aggregateBrief,
  aggregateGapNote,
  buildFamilyOptionTitle,
  buildFamilySelector,
  buildFieldOptionLabel,
  fallbackFieldFromCategory,
  formatRelativeTime,
  groupFieldFamilies,
  isNumericGate,
  parseFamilySelector,
  toSelectorErrorInfo,
  translateSelectorError,
} from '../sensorFieldLogic'

/** 造一条字段目录项；未给出的字段按"数值、无中文名、从未上报"填默认值。 */
function field(partial: Partial<SensorField> & { name: string }): SensorField {
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

/** 固定"当前时刻"，相对时间断言才可重复（不依赖真实时钟）。 */
const NOW = Date.parse('2026-11-12T12:00:00Z')
const ago = (ms: number) => new Date(NOW - ms).toISOString()

describe('isNumericGate 类型门禁（文档 §3.2：非数值字段会让规则永不触发）', () => {
  it('number 与「未知」（空串/null）放行', () => {
    expect(isNumericGate('number')).toBe(true)
    expect(isNumericGate('')).toBe(true)
    expect(isNumericGate(undefined)).toBe(true)
    expect(isNumericGate(null)).toBe(true)
  })

  it('boolean/enum/bitmask/string/raw 一律拦住', () => {
    for (const valueType of ['boolean', 'enum', 'bitmask', 'string', 'raw']) {
      expect(isNumericGate(valueType), valueType).toBe(false)
    }
  })
})

describe('groupFieldFamilies 字段族分组（BMS 16 节电芯）', () => {
  const cells = Array.from({ length: 16 }, (_, index) => field({
    name: 'cell_voltage_' + (index + 1),
    label_zh: '电芯电压 ' + (index + 1),
    unit: 'V',
  }))

  it('16 个 cell_voltage_N 聚成 1 条族，成员按数字后缀升序（_2 在 _10 之前）', () => {
    const families = groupFieldFamilies(cells)
    expect(families).toHaveLength(1)
    expect(families[0].prefix).toBe('cell_voltage')
    expect(families[0].members).toHaveLength(16)
    expect(families[0].members[0].name).toBe('cell_voltage_1')
    expect(families[0].members[1].name).toBe('cell_voltage_2')
    expect(families[0].members[15].name).toBe('cell_voltage_16')
  })

  it('成员 <2 不成族；非 _数字 结尾、基名非小写开头都不算族成员', () => {
    const families = groupFieldFamilies([
      field({ name: 'temperature_1' }),      // 单成员 → 不成族
      field({ name: 'cell_voltage_max' }),   // 不是 _{数字}
      field({ name: 'Cell_1' }),             // 基名必须小写（后端选择器区分大小写）
      field({ name: 'soc' }),                // 无序号
      field({ name: 'temperature_2' }),      // 与 temperature_1 成族
    ])
    expect(families.map(family => family.prefix)).toEqual(['temperature'])
    expect(families[0].members.map(member => member.name)).toEqual(['temperature_1', 'temperature_2'])
  })

  it('族标题 = 中文基名 + 族(成员数)：电芯电压族(16)', () => {
    expect(buildFamilyOptionTitle(groupFieldFamilies(cells)[0])).toBe('电芯电压族(16)')
  })

  it('label_zh 全缺时退回物理名基名，不编中文名', () => {
    const family = groupFieldFamilies([
      field({ name: 'cell_voltage_1' }),
      field({ name: 'cell_voltage_2' }),
    ])[0]
    expect(buildFamilyOptionTitle(family)).toBe('cell_voltage 族(2)')
  })
})

describe('选择器语法：族是 _*:聚合（不是 _*.聚合）', () => {
  it('buildFamilySelector 产出 cell_voltage_*:max', () => {
    expect(buildFamilySelector('cell_voltage', 'max')).toBe('cell_voltage_*:max')
    expect(buildFamilySelector('cell_voltage', 'max')).not.toContain('*.')
  })

  it('parseFamilySelector 只认 _*:聚合；缺聚合、点号形态、字面量、别名、派生都返回 null', () => {
    expect(parseFamilySelector('cell_voltage_*:max')).toEqual({ prefix: 'cell_voltage', aggregate: 'max' })
    expect(parseFamilySelector('cell_voltage_*')).toBeNull()
    expect(parseFamilySelector('cell_voltage_*.max')).toBeNull()
    expect(parseFamilySelector('temperature_1')).toBeNull()
    expect(parseFamilySelector('@soc')).toBeNull()
    expect(parseFamilySelector('virtual:cell_delta')).toBeNull()
    expect(parseFamilySelector('')).toBeNull()
  })
})

describe('formatRelativeTime 相对时间文案', () => {
  it('秒 / 分 / 时 / 天', () => {
    expect(formatRelativeTime(ago(12_000), NOW)).toBe('12s 前')
    expect(formatRelativeTime(ago(5 * 60_000), NOW)).toBe('5m 前')
    expect(formatRelativeTime(ago(3 * 3600_000), NOW)).toBe('3h 前')
    expect(formatRelativeTime(ago(2 * 86400_000), NOW)).toBe('2d 前')
  })

  it('从未上报 / 非法时间 / 时钟偏差导致的未来时间', () => {
    expect(formatRelativeTime(null, NOW)).toBe('从未上报')
    expect(formatRelativeTime(undefined, NOW)).toBe('从未上报')
    expect(formatRelativeTime('not-a-date', NOW)).toBe('从未上报')
    // 设备与服务端时钟偏差时不能出现 "-5s 前"
    expect(formatRelativeTime(new Date(NOW + 60_000).toISOString(), NOW)).toBe('0s 前')
  })
})

describe('buildFieldOptionLabel 选项文案', () => {
  it('label_zh + 物理名 + 单位 + 相对 last_seen', () => {
    const label = buildFieldOptionLabel(field({
      name: 'temperature_1',
      label_zh: '温度探头 1',
      unit: '°C',
      last_seen: ago(12_000),
    }), NOW)
    expect(label).toBe('温度探头 1 · temperature_1 · °C · 12s 前')
  })

  it('label_zh 缺失时回落 name，且不重复物理名', () => {
    expect(buildFieldOptionLabel(field({ name: 'protection_status' }), NOW))
      .toBe('protection_status · 从未上报')
  })

  it('非数值与已弃用都有文案标记（灰显/DOM 之外的可读判据）', () => {
    const label = buildFieldOptionLabel(field({
      name: 'protection_status',
      value_type: 'enum',
      deprecated: true,
    }), NOW)
    expect(label).toContain('已弃用')
    expect(label).toContain('非数值，阈值比较不生效')
  })

  it('中文名与物理名在同一个 label 里 ⇒ el-select 默认过滤能同时命中「温度」与「temperature」', () => {
    const label = buildFieldOptionLabel(field({ name: 'temperature_1', label_zh: '温度探头 1' }), NOW)
    expect(label).toContain('温度')
    expect(label).toContain('temperature')
  })
})

describe('聚合人话与缺员语义', () => {
  it('六个已实现聚合各有一句人话', () => {
    expect(aggregateBrief('any')).toBe('任意一个')
    expect(aggregateBrief('all')).toBe('全部')
    expect(aggregateBrief('max')).toBe('最高')
    expect(aggregateBrief('min')).toBe('最低')
    expect(aggregateBrief('avg')).toBe('平均')
    expect(aggregateBrief('sum')).toBe('合计')
  })

  it('缺员语义：all/avg/sum 缺员即不触发；min/max 按现有成员求值并标降级；any 不受影响', () => {
    for (const aggregate of ['all', 'avg', 'sum']) {
      expect(aggregateGapNote(aggregate), aggregate).toContain('不触发')
    }
    for (const aggregate of ['min', 'max']) {
      expect(aggregateGapNote(aggregate), aggregate).toContain('降级')
    }
    expect(aggregateGapNote('any')).toContain('不影响')
  })

  it('词表外的聚合不编造人话（候选由 /schema 决定，前端只负责已知聚合的展示文案）', () => {
    expect(AGGREGATE_BRIEF.median).toBeUndefined()
    expect(aggregateBrief('median')).toBe('')
    expect(aggregateGapNote('median')).toBe('—')
  })
})

describe('translateSelectorError 错误码人话（只翻译 /schema.error_codes 表内的码）', () => {
  const contract: SensorFieldErrorCode[] = [
    { code: 'invalid_selector_syntax', http: 400 },
    { code: 'unsupported_aggregate', http: 400 },
    { code: 'resolution_unbound', http: 200 },
  ]

  it('契约表内且有人话 ⇒ 显示人话，不露后端英文原串', () => {
    const text = translateSelectorError('invalid_selector_syntax', 'invalid selector syntax: foo', contract)
    expect(text).toContain('语法不合法')
    expect(text).not.toContain('invalid selector syntax')
    expect(translateSelectorError('unsupported_aggregate', 'bad aggregate', contract)).toContain('不支持的聚合方式')
  })

  it('契约表内但前端没有文案（后端新增码）⇒ 回落后端 message', () => {
    expect(translateSelectorError('some_brand_new_code', 'backend says no', [{ code: 'some_brand_new_code', http: 400 }]))
      .toBe('backend says no')
  })

  it('契约表外的码 / 空表 / 未取到 schema ⇒ 回落后端 message（不硬翻未知契约）', () => {
    expect(translateSelectorError('unknown_semantic', 'unknown semantic: foo', contract)).toBe('unknown semantic: foo')
    expect(translateSelectorError('invalid_selector_syntax', 'syntax err', [])).toBe('syntax err')
    expect(translateSelectorError('invalid_selector_syntax', 'syntax err', undefined)).toBe('syntax err')
  })

  it('没有 error_code ⇒ message', () => {
    expect(translateSelectorError(undefined, 'boom', contract)).toBe('boom')
    expect(translateSelectorError('', 'boom', contract)).toBe('boom')
  })
})

describe('toSelectorErrorInfo 抛出物归一（不依赖 instanceof）', () => {
  it('ApiError 形状：400 + error_code', () => {
    const error = Object.assign(new Error('bad selector'), { status: 400, errorCode: 'unsupported_aggregate' })
    expect(toSelectorErrorInfo(error)).toEqual({
      status: 400,
      errorCode: 'unsupported_aggregate',
      message: 'bad selector',
    })
  })

  it('普通对象 / 字符串 / null 都不抛', () => {
    expect(toSelectorErrorInfo({ status: 503, message: 'unavailable' }))
      .toEqual({ status: 503, errorCode: undefined, message: 'unavailable' })
    expect(toSelectorErrorInfo('boom')).toEqual({ status: undefined, errorCode: undefined, message: 'boom' })
    expect(toSelectorErrorInfo(null)).toEqual({ status: undefined, errorCode: undefined, message: '选择器校验失败' })
  })
})

describe('fallbackFieldFromCategory 降级候选', () => {
  it('只有 code+unit：类型未知（门禁放行）、无中文名、从未上报', () => {
    const fallback = fallbackFieldFromCategory('temperature', '°C')
    expect(fallback.name).toBe('temperature')
    expect(fallback.unit).toBe('°C')
    expect(fallback.label_zh).toBe('')
    expect(fallback.value_type).toBe('')
    // 未知类型必须放行，否则降级路径会变成空列表（类型真值仍由后端 resolve 兜底）
    expect(isNumericGate(fallback.value_type)).toBe(true)
    expect(buildFieldOptionLabel(fallback, NOW)).toBe('temperature · °C · 从未上报')
  })
})
