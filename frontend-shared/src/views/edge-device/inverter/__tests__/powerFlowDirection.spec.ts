import { describe, expect, it } from 'vitest'
import {
  batteryFlow,
  gridFlow,
  loadFlow,
  pvFlow,
  MAX_CURRENT_A,
  MAX_POWER_W,
} from '../powerFlow'

/**
 * 功率流向「方向」契约（2026-10-23 用户报障）：
 *   「逆变器功率流向的方向不对，明明电池放电，方向却指向电池。」
 *
 * 改前箭头是写死的（电池连线起点在逆变器、终点在电池，marker-end 恒落电池端），
 * 所以放电时画面仍然是「逆变器 → 电池」，与同一节点上「放电 6.0A」的文字矛盾。
 * 本文件把「每条支路的能量方向」锁成纯函数的返回值 —— 组件只消费它，不允许自己再判一次。
 *
 * 约定：dir 是相对**逆变器**的
 *   'in'  = 能量从该节点流入逆变器
 *   'out' = 能量从逆变器流向该节点
 *   'none'= 无可判定流动（不画箭头）
 */
describe('功率流向：电池方向（用户报障的正是这一条）', () => {
  it('放电 > 0 ⇒ 电池 → 逆变器（dir=in），标签「放电 N.N A」', () => {
    const f = batteryFlow(0, 6)
    expect(f.dir).toBe('in')
    expect(f.active).toBe(true)
    expect(f.cls).toBe('discharging')
    expect(f.label).toBe('放电 6.0A')
    expect(f.text).toContain('电池流向逆变器')
  })

  it('充电 > 0 ⇒ 逆变器 → 电池（dir=out），标签「充电 N.N A」', () => {
    const f = batteryFlow(5.5, 0)
    expect(f.dir).toBe('out')
    expect(f.active).toBe(true)
    expect(f.cls).toBe('charging')
    expect(f.label).toBe('充电 5.5A')
    expect(f.text).toContain('逆变器流向电池')
  })

  it('两者皆 0 ⇒ 无流动（dir=none），标签「静止」，不猜方向', () => {
    const f = batteryFlow(0, 0)
    expect(f.dir).toBe('none')
    expect(f.active).toBe(false)
    expect(f.cls).toBe('')
    expect(f.label).toBe('静止')
  })

  it('两者同时非零（读数矛盾）⇒ 不判定方向，但把两个数都摆出来', () => {
    const f = batteryFlow(3, 2)
    expect(f.dir, '矛盾读数不得猜方向').toBe('none')
    expect(f.label).toContain('充电 3.0A')
    expect(f.label).toContain('放电 2.0A')
    expect(f.text).toContain('不判定方向')
  })

  it('脏数据（负值 / NaN / 越界）不得产生方向', () => {
    for (const bad of [-5, NaN, undefined, null, MAX_CURRENT_A + 1]) {
      expect(batteryFlow(bad as number, 0).dir, 'bad charge ' + String(bad)).toBe('none')
      expect(batteryFlow(0, bad as number).dir, 'bad discharge ' + String(bad)).toBe('none')
    }
  })
})

describe('功率流向：光伏 / 负载', () => {
  it('光伏有功率 ⇒ 光伏 → 逆变器（dir=in）', () => {
    expect(pvFlow(1200).dir).toBe('in')
    expect(pvFlow(1200).active).toBe(true)
    expect(pvFlow(1200).inferred).toBe(false)
    expect(pvFlow(1200).text).toContain('光伏流向逆变器')
  })

  it('光伏无功率 ⇒ 无流动，不画箭头', () => {
    expect(pvFlow(0).dir).toBe('none')
    expect(pvFlow(-100).dir, '负功率无物理含义，不得当成"倒送"画反箭头').toBe('none')
    expect(pvFlow(MAX_POWER_W + 1).dir, '越界读数视为坏数据').toBe('none')
  })

  it('负载有功率 ⇒ 逆变器 → 负载（dir=out）', () => {
    expect(loadFlow(219).dir).toBe('out')
    expect(loadFlow(219).text).toContain('逆变器流向负载')
    expect(loadFlow(0).dir).toBe('none')
  })
})

describe('功率流向：电网（无功率传感器 ⇒ 方向必须标为推定）', () => {
  it('有合法电压 ⇒ 电网 → 逆变器，但 inferred=true（协议没有市电功率字段）', () => {
    const f = gridFlow(231.5)
    expect(f.dir).toBe('in')
    expect(f.active).toBe(true)
    expect(f.inferred, '电网方向无法实测，必须标推定，不得与实测支路混为一谈').toBe(true)
    expect(f.text).toContain('推定')
  })

  it('无电压 / 坏电压 ⇒ 无流动（不画箭头）', () => {
    expect(gridFlow(0).dir).toBe('none')
    expect(gridFlow(undefined).dir).toBe('none')
    expect(gridFlow(1e12).dir).toBe('none')
  })
})

describe('功率流向：与用户截图一致的真实工况（电池放电 6.0A / 负载 219W / 市电 231.5V）', () => {
  it('该工况下三条支路方向自洽：PV 无输出、电网流入、负载流出、电池流入', () => {
    const battery = batteryFlow(0, 6)
    const grid = gridFlow(231.5)
    const load = loadFlow(219)
    // 截图里 PV 读到 1W（等于没发电）：方向仍是「流入」，但只在有真实功率时才高亮箭头，
    // 本用例只锁方向语义，不把 1W 当成"光伏在供电"。
    expect(pvFlow(0).dir).toBe('none')
    expect(pvFlow(1).dir, '1W 也是真实读数（极小的流入），方向不得反').toBe('in')
    expect(grid.dir).toBe('in')
    expect(load.dir).toBe('out')
    expect(battery.dir, '用户报障点：放电时能量必须是 电池 → 逆变器').toBe('in')
  })
})
