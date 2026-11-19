import { describe, expect, it } from 'vitest'
import {
  batteryFlow,
  gridFlow,
  loadFlow,
  pvFlow,
  BMS_CURRENT_DEADBAND_A,
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

describe('功率流向：电网（仅有电压/频率 ⇒ 必须同时看工作模式）', () => {
  it('电池模式（B=3）：市电接入但未使用 ⇒ none、不画箭头、标「未使用」', () => {
    const f = gridFlow(231.5, 3)
    expect(f.connected, '234.8V 证明电气上已接入').toBe(true)
    expect(f.dir, '电池模式下市电不参与供能，方向必须是 none').toBe('none')
    expect(f.active).toBe(false)
    expect(f.status).toBe('未使用')
    expect(f.text).toContain('不参与供能')
  })

  it('市电模式（L=2）：电网 → 逆变器，标「供电」', () => {
    const f = gridFlow(231.5, 2)
    expect(f.connected).toBe(true)
    expect(f.dir).toBe('in')
    expect(f.active).toBe(true)
    expect(f.status).toBe('供电')
    expect(f.text).toContain('供电')
  })

  it('待机（S=1）：已接入但方向不可判定 ⇒ none、标「待机」', () => {
    const f = gridFlow(231.5, 1)
    expect(f.connected).toBe(true)
    expect(f.dir).toBe('none')
    expect(f.status).toBe('待机')
  })

  it('未知 / 故障 / 关机 / 测试模式：不猜方向，标「不可判定」', () => {
    for (const mode of [0, 4, 5, 6, null, undefined]) {
      const f = gridFlow(231.5, mode as number | null | undefined)
      expect(f.connected, 'mode=' + String(mode)).toBe(true)
      expect(f.dir, 'mode=' + String(mode)).toBe('none')
      expect(f.status, 'mode=' + String(mode)).toBe('不可判定')
    }
  })

  it('无电压 / 坏电压 ⇒ 未接入，不画箭头', () => {
    for (const v of [0, undefined, 1e12]) {
      const f = gridFlow(v, 2)
      expect(f.connected, 'v=' + String(v)).toBe(false)
      expect(f.dir, 'v=' + String(v)).toBe('none')
      expect(f.status, 'v=' + String(v)).toBe('未接入')
    }
  })
})

describe('功率流向：与用户截图一致的 2026-11-18 工况（电池模式 / PV 750W / 负载 175W / 市电 234.8V / 充电 5.0A）', () => {
  it('电网虽接入但不参与供能：PV/电池/负载方向自洽，电网无箭头', () => {
    const battery = batteryFlow(5, 0)
    const grid = gridFlow(234.8, 3)
    const load = loadFlow(175)
    expect(pvFlow(750).dir).toBe('in')
    expect(grid.dir, '截图工况是 B=电池：市电不供能，不能再画流入').toBe('none')
    expect(grid.active).toBe(false)
    expect(grid.status).toBe('未使用')
    expect(load.dir).toBe('out')
    expect(battery.dir, '充电 5.0A：能量由逆变器流向电池').toBe('out')
  })
})

describe('功率流向：逆变器电流读 0 时由同节点 BMS 判向（2026-11-19 用户报障）', () => {
  /**
   * 现场（生产库，同一分钟级窗口 16:03–16:09）：
   *   逆变器：53.1V / 89% / 充电 0A / 放电 0A
   *   同节点 BMS：53.15V / 99% / current=-1.96A
   * 改前 batteryFlow(0, 0) ⇒ 「静止」，与 BMS 页的 -1.96A 直接矛盾。
   */
  it('两路都是 0、BMS 报 -1.96A ⇒ 电池 → 逆变器「放电 2.0A」，且标注来源 BMS', () => {
    const f = batteryFlow(0, 0, -1.96)
    expect(f.dir, 'BMS 负电流 = 放电，能量由电池流向逆变器').toBe('in')
    expect(f.active).toBe(true)
    expect(f.cls).toBe('discharging')
    expect(f.label).toBe('放电 2.0A')
    expect(f.amps).toBeCloseTo(1.96, 2)
    expect(f.source).toBe('bms')
    expect(f.text).toContain('BMS')
  })

  it('两路都是 0、BMS 报 +1.5A ⇒ 逆变器 → 电池「充电 1.5A」', () => {
    const f = batteryFlow(0, 0, 1.5)
    expect(f.dir).toBe('out')
    expect(f.cls).toBe('charging')
    expect(f.label).toBe('充电 1.5A')
    expect(f.source).toBe('bms')
  })

  it('BMS 读数落在死区内 ⇒ 仍算静止（不因采样噪声画箭头）', () => {
    const f = batteryFlow(0, 0, -(BMS_CURRENT_DEADBAND_A / 2))
    expect(f.dir).toBe('none')
    expect(f.label).toBe('静止')
  })

  it('逆变器有读数时用逆变器自己的测量（BMS 只做兜底，不抢权威）', () => {
    const f = batteryFlow(0, 3, -3.97)
    expect(f.dir).toBe('in')
    expect(f.label, '显示逆变器自己的整安培读数').toBe('放电 3.0A')
    expect(f.source).toBe('inverter')
  })

  it('两源方向相反 ⇒ 不判方向，两个数都摆出来', () => {
    const chargeVsBms = batteryFlow(2, 0, -1.5)
    expect(chargeVsBms.dir, '逆变器说充电、BMS 说放电：不猜').toBe('none')
    expect(chargeVsBms.source).toBe('mixed')
    expect(chargeVsBms.label).toContain('充电 2.0A')
    expect(chargeVsBms.label).toContain('BMS 放电 1.5A')

    const dischargeVsBms = batteryFlow(0, 2, 1.5)
    expect(dischargeVsBms.dir).toBe('none')
    expect(dischargeVsBms.label).toContain('放电 2.0A')
    expect(dischargeVsBms.label).toContain('BMS 充电 1.5A')
  })

  it('脏 BMS 读数（NaN / 越界）不得产生方向', () => {
    for (const bad of [NaN, Infinity, MAX_CURRENT_A + 1, -MAX_CURRENT_A - 1]) {
      const f = batteryFlow(0, 0, bad)
      expect(f.dir, 'bad bms ' + String(bad)).toBe('none')
      expect(f.label).toBe('静止')
    }
  })

  it('两路都为 0 且没有 BMS 数据 ⇒ 静止（无 BMS 的安装不改变行为）', () => {
    for (const bms of [undefined, null]) {
      const f = batteryFlow(0, 0, bms as number | null | undefined)
      expect(f.dir).toBe('none')
      expect(f.active).toBe(false)
      expect(f.label).toBe('静止')
      expect(f.source).toBe('inverter')
    }
  })
})
