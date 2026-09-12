import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import InverterMpptCard from '../InverterMpptCard.vue'

function mountCard(data: Record<string, number> | null) {
  return mount(InverterMpptCard, { props: { data } })
}

describe('InverterMpptCard', () => {
  it('renders one card per detected channel with voltage/current/power', () => {
    const wrapper = mountCard({
      pv1_voltage: 320.4,
      pv1_current: 5.25,
      pv1_power: 1682.1,
      pv2_voltage: 300,
      pv2_current: 4,
      pv2_power: 1200,
    })

    const cards = wrapper.findAll('.mppt-card')
    expect(cards).toHaveLength(2)
    expect(wrapper.findAll('.mppt-title').map(el => el.text())).toEqual(['MPPT1', 'MPPT2'])
    expect(cards[0].text()).toContain('320.4V')
    expect(cards[0].text()).toContain('5.25A')
    expect(cards[0].text()).toContain('1.68kW')
    expect(cards[1].text()).toContain('300.0V')
    expect(cards[1].text()).toContain('4.00A')
    expect(cards[1].text()).toContain('1.20kW')
    expect(cards[0].find('.el-tag').text()).toBe('运行中')
    expect(cards[0].find('.el-tag').attributes('data-type')).toBe('success')
  })

  it('renders all four channels when present and respects non-contiguous indices', () => {
    const all = mountCard({ pv1_power: 1, pv2_power: 2, pv3_power: 3, pv4_power: 4 })
    expect(all.findAll('.mppt-card')).toHaveLength(4)
    expect(all.findAll('.mppt-title').map(el => el.text())).toEqual(['MPPT1', 'MPPT2', 'MPPT3', 'MPPT4'])

    const sparse = mountCard({ pv3_power: 100 })
    expect(sparse.findAll('.mppt-card')).toHaveLength(1)
    expect(sparse.find('.mppt-title').text()).toBe('MPPT3')
  })

  it('marks a defined-but-idle channel offline and tags it 断开', () => {
    const wrapper = mountCard({ pv1_voltage: 0, pv1_current: 0, pv1_power: 0, pv2_power: 500 })
    const cards = wrapper.findAll('.mppt-card')
    expect(cards).toHaveLength(2)
    expect(cards[0].classes()).toContain('offline')
    expect(cards[0].find('.el-tag').text()).toBe('断开')
    expect(cards[0].find('.el-tag').attributes('data-type')).toBe('info')
    expect(cards[1].classes()).not.toContain('offline')
    expect(cards[1].find('.el-tag').text()).toBe('运行中')
  })

  it('marks a channel online from voltage alone and shows zero for missing current/power', () => {
    const wrapper = mountCard({ pv1_voltage: 250 })
    const card = wrapper.find('.mppt-card')
    expect(card.classes()).not.toContain('offline')
    expect(card.text()).toContain('250.0V')
    expect(card.text()).toContain('0.00A')
    expect(card.text()).toContain('0W')
  })

  it('falls back to mppt{n}_ keys and prefers pv{n}_ when both exist', () => {
    const legacy = mountCard({ mppt1_voltage: 200.5, mppt1_power: 400 })
    expect(legacy.findAll('.mppt-card')).toHaveLength(1)
    expect(legacy.find('.mppt-card').text()).toContain('200.5V')
    expect(legacy.find('.mppt-card').text()).toContain('400W')

    const preferred = mountCard({ pv1_voltage: 100, mppt1_voltage: 200 })
    expect(preferred.find('.mppt-card').text()).toContain('100.0V')
    expect(preferred.find('.mppt-card').text()).not.toContain('200.0V')
  })

  it('formats power at the W/kW boundary including negatives', () => {
    const wrapper = mountCard({ pv1_voltage: 1, pv1_power: 999, pv2_voltage: 1, pv2_power: 1000, pv3_voltage: 1, pv3_power: -1500 })
    const cards = wrapper.findAll('.mppt-card')
    expect(cards[0].text()).toContain('999W')
    expect(cards[1].text()).toContain('1.00kW')
    expect(cards[2].text()).toContain('-1.50kW')
  })

  it('shows the empty placeholder for null or field-less data', () => {
    const fromNull = mountCard(null)
    expect(fromNull.findAll('.mppt-card')).toHaveLength(0)
    expect(fromNull.find('.el-empty').exists()).toBe(true)
    expect(fromNull.text()).toContain('无MPPT通道数据')

    const fromEmpty = mountCard({})
    expect(fromEmpty.find('.el-empty').exists()).toBe(true)
    expect(fromEmpty.findAll('.mppt-card')).toHaveLength(0)
  })
})
