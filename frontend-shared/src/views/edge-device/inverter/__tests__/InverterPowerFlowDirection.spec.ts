import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import InverterPowerFlow from '../InverterPowerFlow.vue'

/**
 * 功率流向**箭头方向**的组件级契约（2026-10-23 用户报障）。
 *
 * 用户原话：「逆变器功率流向的方向不对，明明电池放电，方向却指向电池。」
 *
 * 改前的形态（正是本文件要挡住回退的东西）：
 *   电池连线是一条静态 <path d="M358,140 … 530,200">，起点固定在逆变器、终点固定在电池，
 *   marker-end 恒落在电池端 ⇒ 放电时箭头依然指进电池，与同一节点「放电 6.0A」的文字矛盾。
 *   direction 纯函数测试（powerFlowDirection.spec.ts）**测不到这个**：函数可以判对方向，
 *   而模板没消费它。所以这里断言的是**渲染结果**：path 的起点/终点与 marker-end。
 *
 * 判据（不依赖像素）：电池连线的 d 属性
 *   · 放电（电池 → 逆变器）：起点在电池侧（M530…），终点在逆变器侧（…358,…）
 *   · 充电（逆变器 → 电池）：起点在逆变器侧（M358…），终点在电池侧（…530,…）
 *   · 静止：不画箭头（无 marker-end）
 */

const BASE = {
  pvPower: 0,
  loadPower: 219,
  batteryVoltage: 52.3,
  batteryChargeCurrent: 0,
  batteryDischargeCurrent: 0,
  batteryCapacity: 81,
  gridVoltage: 231.5,
  workMode: 3 as number | null,
}

function mountFlow(over: Partial<typeof BASE> = {}) {
  return mount(InverterPowerFlow, { props: { ...BASE, ...over } })
}

/** 电池那条连线：模板里它是最后一个 <path>（带 title 的功率连线）。 */
function batteryPath(wrapper: ReturnType<typeof mountFlow>) {
  const paths = wrapper.findAll('path.pf-link')
  expect(paths.length, '应有 4 条支路连线').toBe(4)
  return paths[3]
}

describe('功率流向箭头：电池支路方向必须由读数决定（用户报障点）', () => {
  it('放电 6.0A ⇒ 连线起点在电池、终点在逆变器，且带箭头（指向逆变器）', () => {
    const wrapper = mountFlow({ batteryDischargeCurrent: 6 })
    const p = batteryPath(wrapper)
    const d = p.attributes('d') ?? ''
    // 起点必须是电池端（x≈530），终点必须是逆变器端（x≈358）
    expect(d.trim().startsWith('M530'), '放电时连线必须从电池出发：' + d).toBe(true)
    expect(d.trim().endsWith('358,140'), '放电时连线必须落在逆变器端：' + d).toBe(true)
    expect(p.attributes('marker-end'), '有流动必须有箭头').toBe('url(#pf-arrow-discharge)')
    expect(wrapper.find('.pf-node.battery circle').classes()).toContain('discharging')
    expect(wrapper.find('.pf-node.battery .pf-label').text()).toBe('放电 6.0A')
    wrapper.unmount()
  })

  it('充电 5.5A ⇒ 连线起点在逆变器、终点在电池（箭头指向电池）', () => {
    const wrapper = mountFlow({ batteryChargeCurrent: 5.5 })
    const d = batteryPath(wrapper).attributes('d') ?? ''
    expect(d.trim().startsWith('M358'), '充电时连线必须从逆变器出发：' + d).toBe(true)
    expect(d.trim().endsWith('530,200'), '充电时连线必须落在电池端：' + d).toBe(true)
    expect(batteryPath(wrapper).attributes('marker-end')).toBe('url(#pf-arrow-charge)')
    wrapper.unmount()
  })

  it('充放电都为 0 ⇒ 不画箭头（静止不猜方向）', () => {
    const wrapper = mountFlow({ batteryChargeCurrent: 0, batteryDischargeCurrent: 0 })
    expect(batteryPath(wrapper).attributes('marker-end'), '无流动不得画箭头').toBeUndefined()
    expect(wrapper.find('.pf-node.battery .pf-label').text()).toBe('静止')
    wrapper.unmount()
  })

  it('两个电流同时非零（矛盾读数）⇒ 不画箭头，标签把两个数都列出来', () => {
    const wrapper = mountFlow({ batteryChargeCurrent: 3, batteryDischargeCurrent: 2 })
    expect(batteryPath(wrapper).attributes('marker-end'), '矛盾读数不得猜方向').toBeUndefined()
    const label = wrapper.find('.pf-node.battery .pf-label').text()
    expect(label).toContain('充电 3.0A')
    expect(label).toContain('放电 2.0A')
    wrapper.unmount()
  })
})

describe('功率流向箭头：其余三条支路方向固定，但同样由读数决定"画不画"', () => {
  it('PV 有功率 ⇒ PV → 逆变器且有箭头；PV=0 ⇒ 无箭头', () => {
    const on = mountFlow({ pvPower: 1800 })
    expect(on.findAll('path.pf-link')[0].attributes('marker-end')).toBe('url(#pf-arrow-flow)')
    on.unmount()

    const off = mountFlow({ pvPower: 0 })
    expect(off.findAll('path.pf-link')[0].attributes('marker-end')).toBeUndefined()
    off.unmount()
  })

  it('负载有功率 ⇒ 逆变器 → 负载且有箭头；负载=0 ⇒ 无箭头', () => {
    const on = mountFlow({ loadPower: 219 })
    expect(on.findAll('path.pf-link')[2].attributes('marker-end')).toBe('url(#pf-arrow-flow)')
    on.unmount()

    const off = mountFlow({ loadPower: 0 })
    expect(off.findAll('path.pf-link')[2].attributes('marker-end')).toBeUndefined()
    off.unmount()
  })

  it('电网没有功率传感器 ⇒ 有电压时画箭头但同时在图例声明"方向为推定"', () => {
    const wrapper = mountFlow({ gridVoltage: 231.5 })
    expect(wrapper.findAll('path.pf-link')[1].attributes('marker-end')).toBe('url(#pf-arrow-flow)')
    // 关键：必须显式声明这不是实测方向，否则用户会以为市电功率也测了
    expect(wrapper.find('.pf-legend-note.is-inferred').exists()).toBe(true)
    expect(wrapper.find('.pf-legend-note.is-inferred').text()).toContain('推定')
    wrapper.unmount()

    const noGrid = mountFlow({ gridVoltage: 0 })
    expect(noGrid.findAll('path.pf-link')[1].attributes('marker-end')).toBeUndefined()
    expect(noGrid.find('.pf-legend-note.is-inferred').exists()).toBe(false)
    noGrid.unmount()
  })
})

describe('箭头头部与连线同色（方向信息不能输给配色）', () => {
  it('三条语义 marker 都定义了填充色，且不再全部用中性灰', () => {
    const wrapper = mountFlow({ batteryDischargeCurrent: 6, pvPower: 1000 })
    const html = wrapper.html()
    for (const id of ['pf-arrow-flow', 'pf-arrow-charge', 'pf-arrow-discharge']) {
      expect(html, '缺少 marker ' + id).toContain('id="' + id + '"')
    }
    // 反例守卫：改前四条连线共用同一个灰色 marker —— 那正是"箭头最弱"的形态
    expect(html).toContain('marker-fill-flow')
    expect(html).toContain('marker-fill-discharging')
    wrapper.unmount()
  })
})

describe('窄屏（≤768px）DOM 版：每个连接位独立箭头', () => {
  const originalMatchMedia = window.matchMedia
  beforeEach(() => {
    // happy-dom 默认 matches=false（走桌面 SVG）；这里强制窄屏分支
    window.matchMedia = ((query: string) => ({
      matches: true, media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {},
      addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    })) as unknown as typeof window.matchMedia
  })
  afterEach(() => { window.matchMedia = originalMatchMedia })

  it('电池放电 ⇒ 下排右侧箭头朝上（电池 → 逆变器），不是写死的向下', () => {
    const wrapper = mount(InverterPowerFlow, { props: { ...BASE, batteryDischargeCurrent: 6 } })
    const rows = wrapper.findAll('.pfm-arrow-row')
    expect(rows.length, '上下两排连接位').toBe(2)
    const bottom = rows[1].findAll('.pfm-arrow').map(a => a.text())
    // 右列是电池：放电 ⇒ ↑
    expect(bottom[1]).toBe('↑')
    // 左列是负载：始终由逆变器流出 ⇒ ↓
    expect(bottom[0]).toBe('↓')
    wrapper.unmount()
  })

  it('电池充电 ⇒ 下排右侧箭头朝下（逆变器 → 电池）', () => {
    const wrapper = mount(InverterPowerFlow, { props: { ...BASE, batteryChargeCurrent: 5.5 } })
    const bottom = wrapper.findAll('.pfm-arrow-row')[1].findAll('.pfm-arrow').map(a => a.text())
    expect(bottom[1]).toBe('↓')
    wrapper.unmount()
  })

  it('静止 ⇒ 下排右侧是中性点而非箭头（不猜方向）', () => {
    const wrapper = mount(InverterPowerFlow, { props: { ...BASE } })
    const bottom = wrapper.findAll('.pfm-arrow-row')[1].findAll('.pfm-arrow').map(a => a.text())
    expect(bottom[1]).toBe('·')
    wrapper.unmount()
  })

  it('上排：PV 有功率 ⇒ ↓（流入逆变器）；无功率 ⇒ 中性点', () => {
    const on = mount(InverterPowerFlow, { props: { ...BASE, pvPower: 1800 } })
    expect(on.findAll('.pfm-arrow-row')[0].findAll('.pfm-arrow')[0].text()).toBe('↓')
    on.unmount()

    const off = mount(InverterPowerFlow, { props: { ...BASE, pvPower: 0 } })
    expect(off.findAll('.pfm-arrow-row')[0].findAll('.pfm-arrow')[0].text()).toBe('·')
    off.unmount()
  })

  it('两排连接位都有读屏可读的方向说明（不能只念"↓ ↓"）', () => {
    const wrapper = mount(InverterPowerFlow, { props: { ...BASE, batteryDischargeCurrent: 6 } })
    const rows = wrapper.findAll('.pfm-arrow-row')
    expect(rows[0].attributes('aria-label')).toContain('流入逆变器')
    expect(rows[1].attributes('aria-label')).toContain('电池')
    expect(rows[1].attributes('aria-label')).toContain('流入逆变器')
    wrapper.unmount()
  })
})
