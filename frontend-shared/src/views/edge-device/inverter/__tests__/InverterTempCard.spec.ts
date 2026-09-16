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

  it('renders — and no severity class for non-numeric temperature, formats negatives', () => {
    const wrapper = mountCard({ pv_temp: 'abc', inverter_temp: -20.5 })
    const items = wrapper.findAll('.temp-item')
    expect(items[0].find('.temp-value').text()).toBe('—')
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

  it('maps fan speed to the success/warning/danger semantic tokens (F17：不得再用静态亮色常量)', () => {
    // 契约（规范 §3.6.2）：:color 传语义 token 引用，由浏览器按当前主题解析。
    // 此前传 THEME_COLORS.* 的十六进制字面量，暗色下不跟随主题（本仓"伪装成正常"家族）。
    const colorFor = (speed: number) => mountCard({ fan1_speed: speed }).find('.el-progress').attributes('data-color')
    // 三档全覆盖（>80 / 50~80 / <=50），漏测任一档都无法证明分支正确
    expect(colorFor(30)).toBe('var(--color-success)')
    expect(colorFor(60)).toBe('var(--color-warning)')
    expect(colorFor(90)).toBe('var(--color-danger)')
    // 守卫：将来改回硬编码十六进制 / 静态常量，这里必须变红
    for (const v of [colorFor(30), colorFor(60), colorFor(90)]) {
      expect(v, '风扇配色必须是 var(--color-*) 语义 token，不能是静态色值').toMatch(/^var\(--color-[a-z]+\)$/)
      expect(v).not.toMatch(/^#|^rgb/)
    }
    // 边界：恰好 50 / 80 走「不高于」一侧（> 判定，非 >=）
    expect(colorFor(50)).toBe('var(--color-success)')
    expect(colorFor(80)).toBe('var(--color-warning)')
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
