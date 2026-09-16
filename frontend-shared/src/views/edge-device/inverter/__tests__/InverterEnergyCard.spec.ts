import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import InverterEnergyCard from '../InverterEnergyCard.vue'

/**
 * InverterEnergyCard 纯 props 展示组件。
 * props.latestData: Record<string, any> | null；模板用可选链兜底。
 * 内部 formatEnergy 分档：null/NaN -> '—'；>=10000 -> 0 位小数；
 * >=100 -> 1 位小数；其余 -> 2 位小数。
 */

function mountCard(latestData: Record<string, any> | null) {
  return mount(InverterEnergyCard, { props: { latestData } })
}

function energyText(wrapper: ReturnType<typeof mountCard>): string[] {
  return wrapper.findAll('.energy-value').map(el => el.text())
}

describe('InverterEnergyCard', () => {
  it('renders four labelled energy cards with threshold-formatted values', () => {
    const wrapper = mountCard({
      daily_energy: 12.5,
      monthly_energy: 250.5,
      yearly_energy: 12345,
      total_energy: 2000000,
    })

    expect(wrapper.findAll('.energy-card')).toHaveLength(4)
    expect(wrapper.findAll('.energy-label').map(el => el.text())).toEqual([
      '日发电量', '月发电量', '年发电量', '总发电量',
    ])
    expect(wrapper.findAll('.energy-unit')).toHaveLength(4)
    expect(wrapper.findAll('.energy-unit')[0].text()).toBe('kWh')

    const values = energyText(wrapper)
    expect(values[0]).toContain('12.50')
    expect(values[1]).toContain('250.5')
    expect(values[2]).toContain('12345')
    expect(values[3]).toContain('2000000')
  })

  it('applies a distinct class per card', () => {
    const wrapper = mountCard({})
    const cards = wrapper.findAll('.energy-card')
    expect(cards[0].classes()).toContain('daily')
    expect(cards[1].classes()).toContain('monthly')
    expect(cards[2].classes()).toContain('yearly')
    expect(cards[3].classes()).toContain('total')
  })

  it('renders — placeholders without throwing when latestData is null or empty', () => {
    const fromNull = mountCard(null)
    expect(fromNull.findAll('.energy-card')).toHaveLength(4)
    expect(energyText(fromNull).filter(t => t.includes('—'))).toHaveLength(4)

    const fromEmpty = mountCard({})
    expect(energyText(fromEmpty).filter(t => t.includes('—'))).toHaveLength(4)
  })

  it('renders — for non-numeric values instead of NaN output', () => {
    const wrapper = mountCard({ daily_energy: 'abc', monthly_energy: NaN })
    const values = energyText(wrapper)
    expect(values[0]).toContain('—')
    expect(values[1]).toContain('—')
    expect(values[0]).not.toContain('NaN')
  })

  it('switches formatting at the 100 and 10000 boundaries', () => {
    const wrapper = mountCard({
      daily_energy: 99.99,
      monthly_energy: 100,
      yearly_energy: 9999.9,
      total_energy: 10000,
    })
    const values = energyText(wrapper)
    expect(values[0]).toContain('99.99')
    expect(values[1]).toContain('100.0')
    expect(values[1]).not.toContain('100.00')
    expect(values[2]).toContain('9999.9')
    expect(values[3]).toContain('10000')
    expect(values[3]).not.toContain('.')
  })

  it('handles zero, negative and enormous values', () => {
    const wrapper = mountCard({ daily_energy: 0, monthly_energy: -12.5, total_energy: 1e12 })
    const values = energyText(wrapper)
    expect(values[0]).toContain('0.00')
    expect(values[1]).toContain('-12.50')
    expect(values[2]).toContain('—')
    expect(values[3]).toContain('1000000000000')
  })
})
