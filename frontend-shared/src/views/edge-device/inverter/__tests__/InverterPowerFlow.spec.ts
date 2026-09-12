import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import InverterPowerFlow from '../InverterPowerFlow.vue'

interface FlowProps {
  pvPower: number
  loadPower: number
  batteryVoltage: number
  batteryCurrent: number
}

function mountFlow(props: FlowProps) {
  return mount(InverterPowerFlow, { props })
}

describe('InverterPowerFlow', () => {
  it('renders node values and active flow arrows for a producing system', () => {
    const wrapper = mountFlow({ pvPower: 3000, loadPower: 1500, batteryVoltage: 51.2, batteryCurrent: 0 })
    expect(wrapper.find('.node.pv .node-value').text()).toBe('3.00kW')
    expect(wrapper.find('.node.load .node-value').text()).toBe('1.50kW')
    expect(wrapper.find('.node.battery .node-label').text()).toBe('51.2V')
    expect(wrapper.find('.node.pv circle').classes()).toContain('active')
    expect(wrapper.find('.node.load circle').classes()).toContain('active')
    expect(wrapper.find('.node.inverter circle').classes()).toContain('active')
    expect(wrapper.findAll('.arrow')).toHaveLength(2)
  })

  it('shows charging label/class/arrow for negative battery current', () => {
    const wrapper = mountFlow({ pvPower: 1000, loadPower: 0, batteryVoltage: 51.2, batteryCurrent: -5.5 })
    expect(wrapper.find('.node.battery .node-label').text()).toBe('充电 5.5A')
    expect(wrapper.find('.node.battery circle').classes()).toContain('charging')
    expect(wrapper.find('.node.battery circle').classes()).not.toContain('discharging')
    expect(wrapper.find('line[y2="132"]').classes()).toContain('charging')
    expect(wrapper.find('.arrow.charging').exists()).toBe(true)
    expect(wrapper.find('.arrow.discharging').exists()).toBe(false)
  })

  it('shows discharging label/class/arrow for positive battery current', () => {
    const wrapper = mountFlow({ pvPower: 1000, loadPower: 0, batteryVoltage: 51.2, batteryCurrent: 10.2 })
    expect(wrapper.find('.node.battery .node-label').text()).toBe('放电 10.2A')
    expect(wrapper.find('.node.battery circle').classes()).toContain('discharging')
    expect(wrapper.find('.node.battery circle').classes()).not.toContain('charging')
    expect(wrapper.find('line[y2="132"]').classes()).toContain('discharging')
    expect(wrapper.find('.arrow.discharging').exists()).toBe(true)
    expect(wrapper.find('.arrow.charging').exists()).toBe(false)
  })

  it('renders an idle all-zero system without active state or arrows', () => {
    const wrapper = mountFlow({ pvPower: 0, loadPower: 0, batteryVoltage: 0, batteryCurrent: 0 })
    expect(wrapper.find('.node.pv .node-value').text()).toBe('0W')
    expect(wrapper.find('.node.load .node-value').text()).toBe('0W')
    expect(wrapper.find('.node.battery .node-label').text()).toBe('0.0V')
    expect(wrapper.find('.node.pv circle').classes()).not.toContain('active')
    expect(wrapper.find('.node.load circle').classes()).not.toContain('active')
    expect(wrapper.findAll('.arrow')).toHaveLength(0)
  })

  it('formats power at the W/kW boundary and for negatives', () => {
    const small = mountFlow({ pvPower: 999, loadPower: 0.4, batteryVoltage: 0, batteryCurrent: 0 })
    expect(small.find('.node.pv .node-value').text()).toBe('999W')
    expect(small.find('.node.load .node-value').text()).toBe('0W')

    const large = mountFlow({ pvPower: 1000, loadPower: 12500, batteryVoltage: 0, batteryCurrent: 0 })
    expect(large.find('.node.pv .node-value').text()).toBe('1.00kW')
    expect(large.find('.node.load .node-value').text()).toBe('12.50kW')

    const negative = mountFlow({ pvPower: -1500, loadPower: -999, batteryVoltage: 0, batteryCurrent: 0 })
    expect(negative.find('.node.pv .node-value').text()).toBe('-1.50kW')
    expect(negative.find('.node.load .node-value').text()).toBe('-999W')
    expect(negative.find('.node.pv circle').classes()).not.toContain('active')
    expect(negative.find('.node.load circle').classes()).not.toContain('active')
    expect(negative.findAll('.arrow')).toHaveLength(0)
  })
})
