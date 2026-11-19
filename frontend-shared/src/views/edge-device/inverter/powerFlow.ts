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

/**
 * BMS 电流判向死区（A）。
 *
 * JBD BMS 的电流分辨率是 0.01A，静置时会在 0 附近抖动；不设死区的话箭头会随噪声
 * 每两秒翻一次向。0.1A 在 48V 系统上约 5W，低于「逆变器自身待机 + 采样噪声」的量级。
 */
export const BMS_CURRENT_DEADBAND_A = 0.1

/** 归一化**有符号**电流：非有限、越界 ⇒ null（null = 没有可用读数，与「正好 0A」区分开）。 */
function normalizeSignedCurrent(value: number | null | undefined): number | null {
  const n = Number(value)
  if (!Number.isFinite(n) || Math.abs(n) > MAX_CURRENT_A) return null
  return n
}

/** 电池电流的读数来源：逆变器 HBAT 两路寄存器 / 同节点 BMS / 两源互相矛盾 */
export type BatteryFlowSource = 'inverter' | 'bms' | 'mixed'

export interface BatteryFlow extends BranchFlow {
  /** 电池节点状态标签：放电 6.0A / 充电 5.5A / 静止 / 两路同时非零时不判定 */
  label: string
  /** 节点/连线配色类（'' = 静止） */
  cls: '' | 'charging' | 'discharging'
  /** 判定所依据的电流大小（A）；静止 / 读数矛盾时为 0 */
  amps: number
  /** 电流读数来源（决定卡片与图上要不要标注「BMS」） */
  source: BatteryFlowSource
}

/**
 * 电池方向 —— **逆变器两路电流 + 同节点 BMS 有符号电流**三路输入。
 *
 * 2026-11-19 用户报障（真机截图）：
 *   「BMS 都在放电了，INV 流向图还显示电池静止，不对！」
 * 同一分钟级窗口（16:03–16:09）的生产数据：
 *   · 逆变器 HBAT：battery_voltage=53.1V、battery_capacity=89%、充电 0A、放电 0A；
 *   · 同节点 BMS：total_voltage=53.15V、rsoc=99%、current=-1.96A（负=放电）。
 * 即：**逆变器自己没测到（或没上报）放电电流**，而电池确实在放电 2A 左右。
 *
 * 为什么逆变器的 0A 不能直接当作「静止」：
 *   泰琪丰 HBAT 的充/放电电流是两个独立寄存器、整安培分辨率，实测会连续数分钟
 *   读 0（同一时段 BMS 稳定在 -1.88 ~ -2.17A）。这台机器的 0A 首先是**能力边界**
 *   （逆变器没有 BMS 通讯，89% 是它自己的电压估算），把「没测到」说成「静止」是
 *   一次错误断言 —— 这正是用户看到的矛盾。
 *
 * 判定顺序（越靠前越权威，且**任何时候都不猜方向**）：
 *   1. 逆变器两路都 > 0              ⇒ 自相矛盾，不判方向（原有规则）；
 *   2. 逆变器报的方向与 BMS 相反     ⇒ 两源冲突，不判方向（新增，两个数都摆出来）；
 *   3. 逆变器任一路 > 0              ⇒ 用逆变器自己的读数（它的直接测量）；
 *   4. 逆变器两路都为 0、BMS 有净流动 ⇒ **用 BMS 判向**（本次修的缺陷）；
 *   5. 都没有                        ⇒ 静止。
 *
 * BMS 电流约定：**正 = 充电，负 = 放电**（JBD 0x03 帧 int16/100，与 BMS 页
 * 「放电中（电流为负）」同口径）。
 */
export function batteryFlow(
  chargeCurrent: number | null | undefined,
  dischargeCurrent: number | null | undefined,
  bmsCurrent?: number | null,
): BatteryFlow {
  const charge = normalizeCurrent(chargeCurrent)
  const discharge = normalizeCurrent(dischargeCurrent)
  const bms = normalizeSignedCurrent(bmsCurrent)
  // 死区内的 BMS 读数按「没有净流动」处理；它仍然算「有读数」（可与逆变器对账）
  const bmsCharging = bms !== null && bms >= BMS_CURRENT_DEADBAND_A
  const bmsDischarging = bms !== null && bms <= -BMS_CURRENT_DEADBAND_A
  const bmsText = bms === null ? '' : Math.abs(bms).toFixed(1)

  if (charge > 0 && discharge > 0) {
    return {
      dir: 'none',
      active: true,
      label: `充电 ${charge.toFixed(1)}A / 放电 ${discharge.toFixed(1)}A`,
      cls: '',
      amps: 0,
      source: 'inverter',
      text: '充电电流与放电电流同时非零，读数矛盾，不判定方向',
    }
  }

  // 两源冲突：逆变器说充电、BMS 说放电（或反之）—— 不挑一个相信，两个数都摆出来。
  if (charge > 0 && bmsDischarging) {
    return {
      dir: 'none',
      active: true,
      label: `充电 ${charge.toFixed(1)}A / BMS 放电 ${bmsText}A`,
      cls: '',
      amps: 0,
      source: 'mixed',
      text: '逆变器报充电、BMS 报放电，两个读数矛盾，不判定方向',
    }
  }
  if (discharge > 0 && bmsCharging) {
    return {
      dir: 'none',
      active: true,
      label: `放电 ${discharge.toFixed(1)}A / BMS 充电 ${bmsText}A`,
      cls: '',
      amps: 0,
      source: 'mixed',
      text: '逆变器报放电、BMS 报充电，两个读数矛盾，不判定方向',
    }
  }

  if (charge > 0) {
    return {
      dir: 'out',
      active: true,
      label: `充电 ${charge.toFixed(1)}A`,
      cls: 'charging',
      amps: charge,
      source: 'inverter',
      text: `电池充电 ${charge.toFixed(1)}A：能量由逆变器流向电池`,
    }
  }
  if (discharge > 0) {
    return {
      dir: 'in',
      active: true,
      label: `放电 ${discharge.toFixed(1)}A`,
      cls: 'discharging',
      amps: discharge,
      source: 'inverter',
      text: `电池放电 ${discharge.toFixed(1)}A：能量由电池流向逆变器`,
    }
  }

  // 逆变器两路都为 0：只有 BMS 能说明电池到底在不在动。
  if (bmsDischarging) {
    return {
      dir: 'in',
      active: true,
      label: `放电 ${bmsText}A`,
      cls: 'discharging',
      amps: Math.abs(bms as number),
      source: 'bms',
      text: `电池放电 ${bmsText}A：能量由电池流向逆变器（电流取自 BMS；逆变器电池电流寄存器读数为 0）`,
    }
  }
  if (bmsCharging) {
    return {
      dir: 'out',
      active: true,
      label: `充电 ${bmsText}A`,
      cls: 'charging',
      amps: bms as number,
      source: 'bms',
      text: `电池充电 ${bmsText}A：能量由逆变器流向电池（电流取自 BMS；逆变器电池电流寄存器读数为 0）`,
    }
  }

  return {
    dir: 'none', active: false, label: '静止', cls: '', amps: 0, source: 'inverter', text: '电池无充放电',
  }
}
