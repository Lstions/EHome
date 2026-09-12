import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import InverterStatusGrid from '../InverterStatusGrid.vue'

function mountGrid(latestData: Record<string, any> | null) {
  return mount(InverterStatusGrid, { props: { latestData } })
}

describe('InverterStatusGrid', () => {
  it('renders the work-mode and fault tags for a complete snapshot', () => {
    const wrapper = mountGrid({ work_mode: 2, fault_code: 0 })
    const tags = wrapper.findAll('.status-item .el-tag')
    expect(tags).toHaveLength(2)
    expect(tags[0].text()).toBe('市电')
    expect(tags[0].attributes('data-type')).toBe('success')
    expect(tags[1].text()).toBe('无故障')
    expect(tags[1].attributes('data-type')).toBe('success')
    expect(wrapper.find('.el-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('无告警状态数据')
  })

  it('degrades to unknown/no-fault and el-empty when latestData is null', () => {
    const wrapper = mountGrid(null)
    const tags = wrapper.findAll('.status-item .el-tag')
    expect(tags[0].text()).toBe('未知')
    expect(tags[0].attributes('data-type')).toBe('success')
    expect(tags[1].text()).toBe('无故障')
    expect(wrapper.find('.el-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('无告警状态数据')
  })

  it('treats an empty object like null for work mode and alarms', () => {
    const wrapper = mountGrid({})
    expect(wrapper.findAll('.status-item .el-tag')[0].text()).toBe('未知')
    expect(wrapper.find('.el-empty').exists()).toBe(true)
    expect(wrapper.findAll('.grid-item')).toHaveLength(0)
  })

  it('decodes every documented work mode with its tag type', () => {
    const cases: Array<{ mode: number; label: string; type: string }> = [
      { mode: 0, label: '初始上电', type: 'success' },
      { mode: 1, label: '待机', type: 'success' },
      { mode: 2, label: '市电', type: 'success' },
      { mode: 3, label: '电池', type: 'warning' },
      { mode: 4, label: '故障', type: 'danger' },
      { mode: 5, label: '关机', type: 'info' },
      { mode: 6, label: '测试', type: 'success' },
    ]
    for (const c of cases) {
      const wrapper = mountGrid({ work_mode: c.mode })
      const tag = wrapper.findAll('.status-item .el-tag')[0]
      expect(tag.text(), 'mode ' + c.mode).toBe(c.label)
      expect(tag.attributes('data-type'), 'mode ' + c.mode).toBe(c.type)
    }
  })

  it('shows unknown(n) for unmapped / non-numeric modes and plain unknown for empty', () => {
    expect(mountGrid({ work_mode: 9 }).findAll('.status-item .el-tag')[0].text()).toBe('未知(9)')
    expect(mountGrid({ work_mode: 'abc' }).findAll('.status-item .el-tag')[0].text()).toBe('未知(abc)')
    expect(mountGrid({ work_mode: '' }).findAll('.status-item .el-tag')[0].text()).toBe('未知')
  })

  it('shows a danger fault code and falls back to error_code', () => {
    const byFault = mountGrid({ fault_code: 7 })
    const faultTag = byFault.findAll('.status-item .el-tag')[1]
    expect(faultTag.text()).toBe('#7')
    expect(faultTag.attributes('data-type')).toBe('danger')

    const byError = mountGrid({ error_code: 5 })
    const errorTag = byError.findAll('.status-item .el-tag')[1]
    expect(errorTag.text()).toBe('#5')
    expect(errorTag.attributes('data-type')).toBe('danger')
  })

  it('renders only defined alarm fields with active/inactive semantics', () => {
    const wrapper = mountGrid({
      work_mode: 1,
      alarm_overload: 1,
      alarm_battery_low: 0,
      alarm_overtemp: 3,
    })
    const items = wrapper.findAll('.grid-item')
    expect(items).toHaveLength(3)
    expect(items.map(i => i.find('.grid-label').text())).toEqual([
      '电池低电报警', '输出过载', '机器过温',
    ])
    expect(items[0].classes()).not.toContain('active')
    expect(items[1].classes()).toContain('active')
    expect(items[2].classes()).toContain('active')

    const tags = items.map(i => i.find('.el-tag'))
    expect(tags[0].text()).toBe('正常')
    expect(tags[0].attributes('data-type')).toBe('success')
    expect(tags[1].text()).toBe('异常')
    expect(tags[1].attributes('data-type')).toBe('danger')
    expect(tags[2].text()).toBe('异常')
  })

  it('omits alarm fields that are absent from the payload', () => {
    const wrapper = mountGrid({ alarm_pv_low: 0, alarm_fan_error: 1 })
    const items = wrapper.findAll('.grid-item')
    expect(items).toHaveLength(2)
    expect(items[0].find('.grid-label').text()).toBe('PV功率过低')
    expect(items[1].find('.grid-label').text()).toBe('风扇异常')
    expect(wrapper.text()).not.toContain('输出过载')
  })

  it('renders all twelve alarm fields when fully present', () => {
    const payload: Record<string, number> = {}
    const keys = [
      'alarm_pv_to_load', 'alarm_output', 'alarm_battery_low', 'alarm_battery_missing',
      'alarm_overload', 'alarm_overtemp', 'alarm_eeprom_data', 'alarm_eeprom_rw',
      'alarm_pv_low', 'alarm_input_overvoltage', 'alarm_battery_overvoltage', 'alarm_fan_error',
    ]
    for (const k of keys) payload[k] = 0
    const wrapper = mountGrid(payload)
    expect(wrapper.findAll('.grid-item')).toHaveLength(12)
  })
})
