import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import InverterTempCard from '../InverterTempCard.vue'

/**
 * 本仓 el-progress 全局 stub 不保留 color 属性，这里用一个本地 stub
 * 把 percentage/color 暴露成 data-* 属性，以断言风扇转速的语义配色分支。
 */
const ProgressStub = defineComponent({
  name: 'ElProgress',
  props: {
    percentage: Number,
    color: [String, Array, Function],
    strokeWidth: Number,
    textInside: Boolean,
  },
  template: '<div class="el-progress" :data-percentage="percentage" :data-color="typeof color === \'string\' ? color : \'fn\'" />',
})

function mountCard(latestData: Record<string, any> | null) {
  return mount(InverterTempCard, {
    props: { latestData },
    global: { stubs: { 'el-progress': ProgressStub } },
  })
}

describe('InverterTempCard', () => {
  it('renders temperature values with magnitude classes', () => {
    const wrapper = mountCard({ pv_temp: 35.5, inverter_temp: 55.2, boost_temp: 70.1 })
    const items = wrapper.findAll('.temp-item')
    expect(items).toHaveLength(3)
    expect(items[0].find('.temp-label').text()).toBe('PV温度')
    expect(items[0].find('.temp-value').text()).toBe('35.5°C')
    expect(items[0].classes()).toContain('temp-normal')
    expect(items[1].find('.temp-value').text()).toBe('55.2°C')
    expect(items[1].classes()).toContain('temp-warning')
    expect(items[2].find('.temp-value').text()).toBe('70.1°C')
    expect(items[2].classes()).toContain('temp-danger')
  })

  it('classifies temperatures at the 40/60 boundaries', () => {
    const wrapper = mountCard({ pv_temp: 40, inverter_temp: 40.1, boost_temp: 60, max_temp: 60.1 })
    const items = wrapper.findAll('.temp-item')
    expect(items[0].classes()).toContain('temp-normal')
    expect(items[1].classes()).toContain('temp-warning')
    expect(items[2].classes()).toContain('temp-warning')
    expect(items[3].classes()).toContain('temp-danger')
  })

  it('renders -- and no severity class for non-numeric temperature, formats negatives', () => {
    const wrapper = mountCard({ pv_temp: 'abc', inverter_temp: -20.5 })
    const items = wrapper.findAll('.temp-item')
    expect(items[0].find('.temp-value').text()).toBe('--')
    expect(items[0].classes()).not.toContain('temp-normal')
    expect(items[0].classes()).not.toContain('temp-warning')
    expect(items[0].classes()).not.toContain('temp-danger')
    expect(items[1].find('.temp-value').text()).toBe('-20.5°C')
    expect(items[1].classes()).toContain('temp-normal')
  })

  it('renders fan status and speed progress', () => {
    const wrapper = mountCard({ fan1_speed: 60, fan1_status: 1, fan2_speed: 30, fan2_status: 1 })
    expect(wrapper.find('.fan-section').exists()).toBe(true)
    const fans = wrapper.findAll('.fan-item')
    expect(fans).toHaveLength(2)
    expect(fans[0].find('.fan-label').text()).toBe('风扇1')
    expect(fans[0].find('.el-tag').text()).toBe('运行')
    expect(fans[0].find('.el-progress').attributes('data-percentage')).toBe('60')
    expect(fans[1].find('.el-progress').attributes('data-percentage')).toBe('30')
  })

  it('maps fan speed to success/warning/danger colors', () => {
    expect(mountCard({ fan1_speed: 30 }).find('.el-progress').attributes('data-color')).toBe('#67c23a')
    expect(mountCard({ fan1_speed: 60 }).find('.el-progress').attributes('data-color')).toBe('#e6a23c')
    expect(mountCard({ fan1_speed: 90 }).find('.el-progress').attributes('data-color')).toBe('#f56c6c')
  })

  it('derives running state from status or speed and omits progress when speed unknown', () => {
    const wrapper = mountCard({ fan1_status: 0, fan2_speed: 0 })
    const fans = wrapper.findAll('.fan-item')
    expect(fans).toHaveLength(2)
    expect(fans[0].find('.el-tag').text()).toBe('停止')
    expect(fans[0].find('.fan-speed-bar').exists()).toBe(false)
    expect(fans[1].find('.el-tag').text()).toBe('停止')
    expect(fans[1].find('.el-progress').attributes('data-percentage')).toBe('0')
  })

  it('treats status > 0 and speed alone as running', () => {
    const wrapper = mountCard({ fan1_status: 2, fan2_speed: 25 })
    const fans = wrapper.findAll('.fan-item')
    expect(fans[0].find('.el-tag').text()).toBe('运行')
    expect(fans[0].find('.fan-speed-bar').exists()).toBe(false)
    expect(fans[1].find('.el-tag').text()).toBe('运行')
    expect(fans[1].find('.el-progress').attributes('data-percentage')).toBe('25')
  })

  it('keeps the temperature and fan sections independent', () => {
    const noFan = mountCard({ pv_temp: 30 })
    expect(noFan.find('.fan-section').exists()).toBe(false)
    expect(noFan.find('.el-empty').exists()).toBe(false)

    const noTemp = mountCard({ fan1_speed: 10 })
    expect(noTemp.find('.fan-section').exists()).toBe(true)
    expect(noTemp.findAll('.temp-item')).toHaveLength(0)
    expect(noTemp.find('.el-empty').exists()).toBe(false)
  })

  it('renders all seven temperature fields when present', () => {
    const wrapper = mountCard({
      pv_temp: 1, inverter_temp: 2, boost_temp: 3, transformer_temp: 4,
      max_temp: 5, pv2_temp: 6, dc_rectifier_temp: 7,
    })
    expect(wrapper.findAll('.temp-item')).toHaveLength(7)
  })

  it('shows el-empty placeholder for null or field-less data', () => {
    const fromNull = mountCard(null)
    expect(fromNull.find('.el-empty').exists()).toBe(true)
    expect(fromNull.text()).toContain('无温度/风扇数据')
    expect(fromNull.findAll('.temp-item')).toHaveLength(0)
    expect(fromNull.find('.fan-section').exists()).toBe(false)

    const fromOther = mountCard({ voltage: 12 })
    expect(fromOther.find('.el-empty').exists()).toBe(true)
  })
})
