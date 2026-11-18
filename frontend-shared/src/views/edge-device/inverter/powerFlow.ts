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
 * 各支路的知识边界（决定能不能画箭头、要不要标状态）：
 *   · 光伏 PV：pv1_power + pv2_power 实测功率 ⇒ 可判定；
 *   · 负载：output_active_power 实测有功 ⇒ 可判定（负载只消耗）；
 *   · 电池：HBAT 的充电电流 / 放电电流是**两个独立寄存器**（不是带符号的单量），
 *     谁 > 0 就说明能量往哪走 ⇒ 可判定；
 *   · 电网：协议只给 HGRID 电压/频率。
 *     工作模式决定方向：
 *       - 电池模式（B）：市电已接入但未使用 ⇒ dir='none'，不画箭头；
 *       - 市电模式（L）：按拓扑市电必然供电 ⇒ dir='in'，正常画出能量流；
 *       - 待机/故障/关机/模式未知：无法判定方向 ⇒ dir='none'，标「不可判定」。
 *     方向判定必须同时看 gridVoltage 与 workMode，**绝不能只看电压**。
 */

/** 支路方向：相对**逆变器**而言（in = 流入逆变器，out = 流出到该节点）。 */
export type BranchDir = 'in' | 'out' | 'none'

export interface BranchFlow {
  /** 相对逆变器的能量方向 */
  dir: BranchDir
  /** 该支路是否有能量在动（用于节点高亮 / 连线着色） */
  active: boolean
  /** 人类可读的方向说明（<title> / aria-label，读屏器与悬停都能拿到） */
  text: string
}

/** 电网节点的短徽标：让「接入」与「供能」在图上可区分。 */
export type GridStatus = '未接入' | '未使用' | '供电' | '待机' | '不可判定'

export interface GridFlow extends BranchFlow {
  /** 电气上是否接入（市电电压在合法区间） */
  connected: boolean
  /** 节点短徽标（同时用于状态展示与读屏摘要） */
  status: GridStatus
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
  if (p <= 0) return { dir: 'none', active: false, text: '光伏无输出' }
  return { dir: 'in', active: true, text: '能量由光伏流向逆变器' }
}

/** 负载：消耗侧，永远只能「逆变器 → 负载」。 */
export function loadFlow(loadPower: number | null | undefined): BranchFlow {
  const p = normalizePower(loadPower)
  if (p <= 0) return { dir: 'none', active: false, text: '负载无用电' }
  return { dir: 'out', active: true, text: '能量由逆变器流向负载' }
}

/** 工作模式数值 —— 与驱动 workModeMap / InverterStatusGrid 同表。 */
export const WORK_MODE = {
  POWER_ON: 0,
  STANDBY: 1,
  LINE: 2,
  BATTERY: 3,
  FAULT: 4,
  SHUTDOWN: 5,
  TEST: 6,
} as const

/**
 * 电网：只有电压/频率可测，**没有功率传感器**。
 *
 * 关键修正（2026-11-18 用户报障）：电压只能证明「接入」，不能证明「供能」。
 * 电池模式下市电即使有 234.8V，也没有功率流入逆变器 —— 改前只看电压就画
 * dir='in', active=true，导致电网与 PV 一样画成绿色实线箭头，用户自然读成
 * 「市电正在供电」。现在必须同时消费 workMode：
 *   · B=电池    ⇒ connected=true, dir='none'，节点标「未使用」，不画箭头；
 *   · L=市电    ⇒ connected=true, dir='in'，按市电模式正常画出能量流；
 *   · S=待机    ⇒ connected=true, dir='none'，标「待机」；
 *   · 其它/未知 ⇒ connected=true, dir='none'，标「不可判定」，绝不猜。
 */
export function gridFlow(
  gridVoltage: number | null | undefined,
  workMode: number | null | undefined,
): GridFlow {
  const v = Number(gridVoltage)
  const connected = Number.isFinite(v) && v >= MIN_VOLT_AC && v <= MAX_VOLT_AC
  if (!connected) {
    return {
      dir: 'none',
      active: false,
      connected: false,
      status: '未接入',
      text: '市电未接入（无电压）',
    }
  }

  const mode = workMode === null || workMode === undefined ? null : Number(workMode)
  const known = mode !== null && Number.isFinite(mode)

  if (known && mode === WORK_MODE.BATTERY) {
    return {
      dir: 'none',
      active: false,
      connected: true,
      status: '未使用',
      text: '市电已接入，但电池模式下不参与供能',
    }
  }
  if (known && mode === WORK_MODE.LINE) {
    return {
      dir: 'in',
      active: true,
      connected: true,
      status: '供电',
      text: '市电供电中',
    }
  }
  if (known && mode === WORK_MODE.STANDBY) {
    return {
      dir: 'none',
      active: false,
      connected: true,
      status: '待机',
      text: '市电已接入，待机状态下方向不判定',
    }
  }
  return {
    dir: 'none',
    active: false,
    connected: true,
    status: '不可判定',
    text: '市电已接入，但当前工作模式无法判定方向',
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
      label: `充电 ${charge.toFixed(1)}A / 放电 ${discharge.toFixed(1)}A`,
      cls: '',
      text: '充电电流与放电电流同时非零，读数矛盾，不判定方向',
    }
  }
  if (charge > 0) {
    return {
      dir: 'out',
      active: true,
      label: `充电 ${charge.toFixed(1)}A`,
      cls: 'charging',
      text: `电池充电 ${charge.toFixed(1)}A：能量由逆变器流向电池`,
    }
  }
  if (discharge > 0) {
    return {
      dir: 'in',
      active: true,
      label: `放电 ${discharge.toFixed(1)}A`,
      cls: 'discharging',
      text: `电池放电 ${discharge.toFixed(1)}A：能量由电池流向逆变器`,
    }
  }
  return { dir: 'none', active: false, label: '静止', cls: '', text: '电池无充放电' }
}
