/**
 * 逆变器功率流向：**方向判定的单一事实源**（纯函数，无 Vue 依赖）。
 *
 * 2026-10-23 用户报障（真机截图，电池方框橙色「放电 6.0A」但箭头指向电池）：
 *   「逆变器功率流向的方向不对，明明电池放电，方向却指向电池。」
 *
 * 根因不是判定表写错，而是**箭头方向被写死**：电池连线是一条静态 <path>，
 * 起点固定在逆变器、终点固定在电池，marker-end 永远落在电池端 —— 于是放电时
 * 画面仍然是「逆变器 → 电池」，与同一节点上的「放电 6.0A」文字自相矛盾。
 * 同类问题在窄屏 DOM 版更明显：两行之间只有一个固定的「↓」，无论电池在充还是在放。
 *
 * 因此把「每条支路的能量方向」抽成这里的一组纯函数，桌面 SVG 与窄屏 DOM 都只消费它：
 *   dir = 'in'  → 能量由该支路节点**流入逆变器**
 *   dir = 'out' → 能量由逆变器**流出到该支路节点**
 *   dir = 'none'→ 没有可判定的能量流动（不画箭头，绝不猜方向）
 *
 * 各支路的知识边界（决定能不能画箭头、要不要标「推定」）：
 *   · 光伏 PV：pv1_power + pv2_power 实测功率 ⇒ 可判定；
 *   · 负载：output_active_power 实测有功 ⇒ 可判定（负载只消耗）；
 *   · 电池：HBAT 的充电电流 / 放电电流是**两个独立寄存器**（不是带符号的单量），
 *     谁 > 0 就说明能量往哪走 ⇒ 可判定；
 *   · 电网：协议只给 HGRID 电压/频率，**没有任何市电功率传感器** ⇒ 方向无法实测。
 *     仍然画「电网 → 逆变器」，但标 inferred=true（界面注明「按市电供电推定」），
 *     而不是像改前那样把它和实测支路画成一样、让人以为也是测出来的。
 */

/** 支路方向：相对**逆变器**而言（in = 流入逆变器，out = 流出到该节点）。 */
export type BranchDir = 'in' | 'out' | 'none'

export interface BranchFlow {
  /** 相对逆变器的能量方向 */
  dir: BranchDir
  /** 该支路是否有能量在动（用于节点高亮 / 连线着色） */
  active: boolean
  /** 方向是否**推定**（该支路没有功率传感器）而非实测 */
  inferred: boolean
  /** 人类可读的方向说明（<title> / aria-label，读屏器与悬停都能拿到） */
  text: string
}

/** 功率上限（W）：与驱动 checkRange 的 maxPower 同口径，超出视为坏数据、不参与方向判定。 */
export const MAX_POWER_W = 50000
/** 电流上限（A）：同上，HBAT 电流字段的物理边界。 */
export const MAX_CURRENT_A = 1000
/** 交流电压区间（V）：与驱动 minVoltAC/maxVoltAC 一致。 */
const MIN_VOLT_AC = 50
const MAX_VOLT_AC = 600

/** 归一化功率：非有限、越界、负值 ⇒ 0（负功率在这台机器上没有物理含义，不据此画箭头）。 */
function normalizePower(value: number | null | undefined): number {
  const n = Number(value)
  if (!Number.isFinite(n) || n <= 0 || n > MAX_POWER_W) return 0
  return n
}

/** 归一化电流：同上（负数/越界 ⇒ 0）。 */
function normalizeCurrent(value: number | null | undefined): number {
  const n = Number(value)
  if (!Number.isFinite(n) || n <= 0 || n > MAX_CURRENT_A) return 0
  return n
}

/** 光伏：有实测功率就向逆变器送电。 */
export function pvFlow(pvPower: number | null | undefined): BranchFlow {
  const p = normalizePower(pvPower)
  if (p <= 0) return { dir: 'none', active: false, inferred: false, text: '光伏无输出' }
  return { dir: 'in', active: true, inferred: false, text: '能量由光伏流向逆变器' }
}

/** 负载：消耗侧，永远只能「逆变器 → 负载」。 */
export function loadFlow(loadPower: number | null | undefined): BranchFlow {
  const p = normalizePower(loadPower)
  if (p <= 0) return { dir: 'none', active: false, inferred: false, text: '负载无用电' }
  return { dir: 'out', active: true, inferred: false, text: '能量由逆变器流向负载' }
}

/**
 * 电网：只有电压/频率可测，**没有功率传感器**。
 * 因此「市电在电」可判定（电压在合法区间），但「功率往哪走、走多大」不可测 ——
 * 方向按市电供电推定（inferred=true），并在界面上明确标注。
 */
export function gridFlow(gridVoltage: number | null | undefined): BranchFlow {
  const v = Number(gridVoltage)
  const present = Number.isFinite(v) && v >= MIN_VOLT_AC && v <= MAX_VOLT_AC
  if (!present) return { dir: 'none', active: false, inferred: false, text: '市电未接入（无电压）' }
  return {
    dir: 'in',
    active: true,
    inferred: true,
    text: '市电在电（协议只提供电压与频率，功率方向按市电供电推定）',
  }
}

export interface BatteryFlow extends BranchFlow {
  /** 电池节点状态标签：放电 6.0A / 充电 5.5A / 静止 / 两路同时非零时不判定 */
  label: string
  /** 节点/连线配色类（'' = 静止） */
  cls: '' | 'charging' | 'discharging'
}

/**
 * 电池：HBAT 的充电电流与放电电流是**两个独立寄存器**。
 *   · 只有充电 > 0  ⇒ 逆变器 → 电池（out）
 *   · 只有放电 > 0  ⇒ 电池 → 逆变器（in）
 *   · 两者都 0      ⇒ 无流动（none）
 *   · 两者同时 > 0  ⇒ 读数自相矛盾，**不猜方向**（none），标签两值并列摆出来
 */
export function batteryFlow(
  chargeCurrent: number | null | undefined,
  dischargeCurrent: number | null | undefined,
): BatteryFlow {
  const charge = normalizeCurrent(chargeCurrent)
  const discharge = normalizeCurrent(dischargeCurrent)

  if (charge > 0 && discharge > 0) {
    return {
      dir: 'none',
      active: true,
      inferred: false,
      label: `充电 ${charge.toFixed(1)}A / 放电 ${discharge.toFixed(1)}A`,
      cls: '',
      text: '充电电流与放电电流同时非零，读数矛盾，不判定方向',
    }
  }
  if (charge > 0) {
    return {
      dir: 'out',
      active: true,
      inferred: false,
      label: `充电 ${charge.toFixed(1)}A`,
      cls: 'charging',
      text: `电池充电 ${charge.toFixed(1)}A：能量由逆变器流向电池`,
    }
  }
  if (discharge > 0) {
    return {
      dir: 'in',
      active: true,
      inferred: false,
      label: `放电 ${discharge.toFixed(1)}A`,
      cls: 'discharging',
      text: `电池放电 ${discharge.toFixed(1)}A：能量由电池流向逆变器`,
    }
  }
  return { dir: 'none', active: false, inferred: false, label: '静止', cls: '', text: '电池无充放电' }
}
