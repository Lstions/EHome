import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import InverterPowerFlow from '../InverterPowerFlow.vue'

/**
 * 逆变器功率流组件测试。
 *
 * 本文件在 2026-10-18 重写过一次，原因是组件本身被重写：
 *   · 旧版读的是 `battery_current`（**后端不存在的字段**），于是方向判定恒走
 *     idle 分支 —— 旧的三个用例之所以能过，是因为它们直接喂 props，
 *     而真实页面喂进来的是 undefined。这类"测试绿、线上灰"的形态是最坏的：
 *     组件内部逻辑没问题，问题在两个世界的字段名对不上。
 *   · 新版按**充电电流/放电电流两个独立字段**判定（泰琪丰协议就是两个寄存器），
 *     并补上了电网节点。
 *
 * 因此新用例的 props 一律使用**后端真实字段名**，与 InverterDetailPage 的传参一致。
 */

interface FlowProps {
  pvPower: number
  loadPower: number
  batteryVoltage: number
  batteryChargeCurrent: number
  batteryDischargeCurrent: number
  batteryCapacity: number
  gridVoltage: number
  workMode: number | null
  apparentPower?: number
}

function mountFlow(props: Partial<FlowProps> = {}) {
  return mount(InverterPowerFlow, {
    props: {
      pvPower: 0,
      loadPower: 0,
      batteryVoltage: 0,
      batteryChargeCurrent: 0,
      batteryDischargeCurrent: 0,
      batteryCapacity: 0,
      gridVoltage: 0,
      workMode: null,
      ...props,
    },
  })
}

describe('InverterPowerFlow', () => {
  it('renders PV/grid/load/battery values from real backend field names', () => {
    const wrapper = mountFlow({
      pvPower: 3000,
      loadPower: 1500,
      batteryVoltage: 51.2,
      batteryCapacity: 83,
      gridVoltage: 226.3,
      workMode: 3,
    })
    expect(wrapper.find('.pf-node.pv .pf-value').text()).toBe('3.00kW')
    expect(wrapper.find('.pf-node.load .pf-value').text()).toBe('1.50kW')
    expect(wrapper.find('.pf-node.grid .pf-value').text()).toBe('226.3V')
    expect(wrapper.find('.pf-node.battery .pf-value').text()).toBe('51.2V · 83%')
    // work_mode=3 对应驱动 workModeMap 的 'B' = Battery
    expect(wrapper.find('.pf-node.inverter .pf-value').text()).toBe('电池 Battery')
  })

  it('marks PV and load as active when they carry power', () => {
    const wrapper = mountFlow({ pvPower: 3000, loadPower: 1500 })
    expect(wrapper.find('.pf-node.pv circle').classes()).toContain('on')
    expect(wrapper.find('.pf-node.load circle').classes()).toContain('on')
  })

  it('reads charging from batteryChargeCurrent, not from a signed current', () => {
    const wrapper = mountFlow({ batteryVoltage: 51.2, batteryChargeCurrent: 5.5 })
    expect(wrapper.find('.pf-node.battery .pf-label').text()).toBe('充电 5.5A')
    expect(wrapper.find('.pf-node.battery circle').classes()).toContain('charging')
    expect(wrapper.find('.pf-node.battery circle').classes()).not.toContain('discharging')
  })

  it('reads discharging from batteryDischargeCurrent', () => {
    const wrapper = mountFlow({ batteryVoltage: 52.3, batteryDischargeCurrent: 6 })
    expect(wrapper.find('.pf-node.battery .pf-label').text()).toBe('放电 6.0A')
    expect(wrapper.find('.pf-node.battery circle').classes()).toContain('discharging')
    expect(wrapper.find('.pf-node.battery circle').classes()).not.toContain('charging')
  })

  it('shows idle when both charge and discharge currents are zero', () => {
    const wrapper = mountFlow({ batteryVoltage: 52, batteryChargeCurrent: 0, batteryDischargeCurrent: 0 })
    expect(wrapper.find('.pf-node.battery .pf-label').text()).toBe('静止')
    expect(wrapper.find('.pf-node.battery circle').classes()).not.toContain('charging')
    expect(wrapper.find('.pf-node.battery circle').classes()).not.toContain('discharging')
  })

  it('renders idle system without any active nodes', () => {
    const wrapper = mountFlow()
    expect(wrapper.find('.pf-node.pv .pf-value').text()).toBe('0W')
    expect(wrapper.find('.pf-node.load .pf-value').text()).toBe('0W')
    expect(wrapper.find('.pf-node.pv circle').classes()).not.toContain('on')
    expect(wrapper.find('.pf-node.grid circle').classes()).not.toContain('on')
  })

  it('shows placeholder for missing work mode instead of guessing', () => {
    const wrapper = mountFlow({ workMode: null })
    expect(wrapper.find('.pf-node.inverter .pf-value').text()).toBe('—')
  })

  it('formats power at the W/kW boundary and for negatives', () => {
    const small = mountFlow({ pvPower: 999, loadPower: 0.4 })
    expect(small.find('.pf-node.pv .pf-value').text()).toBe('999W')

    const large = mountFlow({ pvPower: 1000, loadPower: 12500 })
    expect(large.find('.pf-node.pv .pf-value').text()).toBe('1.00kW')
    expect(large.find('.pf-node.load .pf-value').text()).toBe('12.50kW')

    const negative = mountFlow({ pvPower: -1500, loadPower: -999 })
    expect(negative.find('.pf-node.pv .pf-value').text()).toBe('-1.50kW')
    expect(negative.find('.pf-node.load .pf-value').text()).toBe('-999W')
    expect(negative.find('.pf-node.pv circle').classes()).not.toContain('on')
  })
})
