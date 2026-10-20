/**
 * 逆变器「运行标志」—— 从协议状态串的 12 个标志位里挑出**不是告警**的那 2 位。
 *
 * ⚠ 为什么必须单独拎出来（2026-10-20 生产实测）：
 *   泰琪丰状态串 `S[1:13]` 的 12 位在协议里都叫 alarm flags，但 pos0/pos1 的语义是
 *   「PV 正在馈能负载」和「逆变器当前有输出」，设备带载工作时恒为 1。
 *   前端若把它们当告警：只要逆变器在正常工作，页面就常驻「1 条告警」+ 红色「有输出/异常」行。
 *   实测数据（设备 15，work_mode=2 市电、fault_code=0）：alarm_output=1、其余 10 位全 0。
 *   假告警比没有告警更糟 —— 它会让真告警失去可信度。
 *
 * 本模块是**唯一真源**：状态展示（InverterStatusGrid）与告警计数（InverterDetailPage）都读它，
 * 避免两处各写一份 `alarm_` 前缀判断而再次漂移。
 */

export interface RunningFlagDef {
  /** 后端字段名 */
  key: string
  /** 展示名称 */
  label: string
  /** 位为 1 时的文案（功能性为真，不是故障） */
  onText: string
  /** 位为 0 时的文案 */
  offText: string
}

export const INVERTER_RUNNING_FLAGS: RunningFlagDef[] = [
  { key: 'alarm_output', label: '交流输出', onText: '有输出', offText: '无输出' },
  { key: 'alarm_pv_to_load', label: 'PV 馈能负载', onText: '馈能中', offText: '未馈能' },
]

/** 供告警计数使用：这些 key 即使为 1 也不算告警 */
export const RUNNING_FLAG_KEYS: ReadonlySet<string> = new Set(INVERTER_RUNNING_FLAGS.map((f) => f.key))