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
  /** 同节点 BMS 的有符号电流（A，正=充电 / 负=放电）；BASE 里默认没有 BMS */
  batteryBmsCurrent: null as number | null,
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

/**
 * 2026-11-19 用户报障：「BMS 都在放电了，INV 流向图还显示电池静止，不对！」
 *
 * 现场：逆变器 HBAT 充/放电流连续数分钟都是 0（整安培分辨率 + 没接 BMS 通讯），
 * 同节点 BMS current=-1.96A。改前 batteryFlow(0,0) ⇒ 静止且不画箭头。
 * 现在逆变器两路都为 0 时用 BMS 判向，并在标签/图例里注明来源。
 */
describe('电池支路：逆变器读 0 时由同节点 BMS 判向（2026-11-19 报障）', () => {
  it('两路 0 + BMS -1.96A ⇒ 箭头指向逆变器，标签「放电 2.0A · BMS」+ 图例注明来源', () => {
    const wrapper = mountFlow({ batteryBmsCurrent: -1.96 })
    const p = batteryPath(wrapper)
    const d = p.attributes('d') ?? ''
    expect(d.trim().startsWith('M530'), 'BMS 报放电：连线必须从电池出发').toBe(true)
    expect(d.trim().endsWith('358,140')).toBe(true)
    expect(p.attributes('marker-end')).toBe('url(#pf-arrow-discharge)')
    expect(wrapper.find('.pf-node.battery circle').classes()).toContain('discharging')
    expect(wrapper.find('.pf-node.battery .pf-label').text()).toBe('放电 2.0A · BMS')
    expect(wrapper.text()).toContain('电池电流取自 BMS')
    wrapper.unmount()
  })

  it('两路 0 + BMS +1.5A ⇒ 充电方向，同样注明来源', () => {
    const wrapper = mountFlow({ batteryBmsCurrent: 1.5 })
    expect(batteryPath(wrapper).attributes('d')?.trim().startsWith('M358')).toBe(true)
    expect(batteryPath(wrapper).attributes('marker-end')).toBe('url(#pf-arrow-charge)')
    expect(wrapper.find('.pf-node.battery .pf-label').text()).toBe('充电 1.5A · BMS')
    wrapper.unmount()
  })

  it('BMS 读数落在死区（-0.05A）⇒ 仍是静止，不画箭头也不加 BMS 标注', () => {
    const wrapper = mountFlow({ batteryBmsCurrent: -0.05 })
    expect(batteryPath(wrapper).attributes('marker-end')).toBeUndefined()
    expect(wrapper.find('.pf-node.battery .pf-label').text()).toBe('静止')
    expect(wrapper.text()).not.toContain('电池电流取自 BMS')
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

  it('电池模式 + 231.5V：无箭头、灰虚线、节点标「未使用」，图例说明未参与供能', () => {
    const wrapper = mountFlow({ gridVoltage: 231.5, workMode: 3 })
    const gridPath = wrapper.findAll('path.pf-link')[1]
    expect(gridPath.attributes('marker-end'), '电池模式不得画箭头').toBeUndefined()
    expect(gridPath.classes()).toContain('is-idle')
    expect(gridPath.classes()).not.toContain('on')
    expect(wrapper.find('.pf-node.grid .pf-label-status').attributes('data-status')).toBe('未使用')
    expect(wrapper.find('.pf-node.grid circle').classes()).not.toContain('on')
    expect(wrapper.find('.pf-legend-note.is-idle').text()).toContain('未使用')
    wrapper.unmount()
  })

  it('市电模式 + 231.5V：电网按供电正常画绿色实线/箭头，节点标「供电」', () => {
    const wrapper = mountFlow({ gridVoltage: 231.5, workMode: 2 })
    const gridPath = wrapper.findAll('path.pf-link')[1]
    expect(gridPath.attributes('marker-end')).toBe('url(#pf-arrow-flow)')
    expect(gridPath.classes()).toContain('on')
    expect(wrapper.find('.pf-node.grid .pf-label-status').attributes('data-status')).toBe('供电')
    expect(wrapper.find('.pf-node.grid circle').classes()).toContain('on')
    expect(wrapper.find('.pf-legend-note.is-idle').exists()).toBe(false)
    wrapper.unmount()
  })

  it('待机 / 未知模式：不画箭头，节点标「待机」/「不可判定」', () => {
    const standby = mountFlow({ gridVoltage: 231.5, workMode: 1 })
    expect(standby.findAll('path.pf-link')[1].attributes('marker-end')).toBeUndefined()
    expect(standby.find('.pf-node.grid .pf-label-status').attributes('data-status')).toBe('待机')
    standby.unmount()

    const unknown = mountFlow({ gridVoltage: 231.5, workMode: null })
    expect(unknown.findAll('path.pf-link')[1].attributes('marker-end')).toBeUndefined()
    expect(unknown.find('.pf-node.grid .pf-label-status').attributes('data-status')).toBe('不可判定')
    unknown.unmount()
  })

  it('无电压：标「未接入」，不画箭头', () => {
    const wrapper = mountFlow({ gridVoltage: 0, workMode: 2 })
    expect(wrapper.findAll('path.pf-link')[1].attributes('marker-end')).toBeUndefined()
    expect(wrapper.find('.pf-node.grid .pf-label-status').attributes('data-status')).toBe('未接入')
    wrapper.unmount()
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

  it('逆变器两路 0 + BMS 放电 ⇒ 下排右侧箭头朝上，节点文案带「· BMS」', () => {
    const wrapper = mount(InverterPowerFlow, { props: { ...BASE, batteryBmsCurrent: -1.96 } })
    const bottom = wrapper.findAll('.pfm-arrow-row')[1].findAll('.pfm-arrow')
    expect(bottom[1].text()).toBe('↑')
    expect(bottom[1].classes()).toContain('is-discharging')
    expect(wrapper.find('[data-branch="battery"] .pfm-label').text()).toBe('放电 2.0A · BMS')
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

  it('上排电网：电池模式接入但未使用 ⇒ 中性点「·」+ 徽标「未使用」', () => {
    const wrapper = mount(InverterPowerFlow, { props: { ...BASE, pvPower: 1000, gridVoltage: 231.5, workMode: 3 } })
    const top = wrapper.findAll('.pfm-arrow-row')[0].findAll('.pfm-arrow')
    expect(top[1].text()).toBe('·')
    expect(top[1].classes()).toContain('is-idle')
    expect(wrapper.find('[data-branch="grid"] .pfm-status').text()).toBe('未使用')
    wrapper.unmount()
  })

  it('上排电网：市电模式 ⇒ ↓ + 绿色 flow 色调 + 徽标「供电」', () => {
    const wrapper = mount(InverterPowerFlow, { props: { ...BASE, gridVoltage: 231.5, workMode: 2 } })
    const top = wrapper.findAll('.pfm-arrow-row')[0].findAll('.pfm-arrow')
    expect(top[1].text()).toBe('↓')
    expect(top[1].classes()).toContain('is-flow')
    expect(wrapper.find('[data-branch="grid"] .pfm-status').text()).toBe('供电')
    wrapper.unmount()
  })

  it('两排连接位都有读屏可读的方向说明（不能只念"↓ ↓"）', () => {
    const wrapper = mount(InverterPowerFlow, { props: { ...BASE, pvPower: 1000, batteryDischargeCurrent: 6 } })
    const rows = wrapper.findAll('.pfm-arrow-row')
    expect(rows[0].attributes('aria-label')).toContain('光伏流入逆变器')
    expect(rows[0].attributes('aria-label')).toContain('电网已接入，未使用')
    expect(rows[1].attributes('aria-label')).toContain('电池')
    expect(rows[1].attributes('aria-label')).toContain('流入逆变器')
    wrapper.unmount()
  })
})
