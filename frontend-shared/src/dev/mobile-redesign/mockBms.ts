/**
 * BMS 移动端改版 demo —— 静态 mock 数据（仅 DEV demo 使用，不接任何后端 API）。
 *
 * 数值口径来自生产实测（2026-10-20 生产截图 / .logs/ui-fix/review-shots）：
 *   SOC 7.0% · 剩余 7.13Ah/99Ah 标称 · 总电压 51.76V · 电流 2.60A（充电）
 *   16 串单体电压 3.231~3.242V（总和 = 51.76V，与总电压自洽）
 *   3 路温度 28.1 / 26.5 / 27.8 ℃ · fet_status 0x03（充/放 MOS 均导通）
 *   protection_status 0x00（12 项保护全未触发）· 轮询已启用 1/3 条
 *
 * 为什么单独成文件：A/B/C 三个方案必须展示**同一份事实**，否则评审时
 * "哪个看起来数更少"会混进设计差异里。数据只有一个源，方案只决定表达方式。
 *
 * ── 数据源核实（2026-10-20，Lead 查生产 unified_data）────────────────────────
 * 设备 14（BMS）24h 内每个分类各有 **504 个点位**（≈ 每 171s 一个）：
 *   total_voltage / current / rsoc / remaining_capacity / protection_status /
 *   fet_status / temperature_1..3 / cell_voltage_max / cell_voltage_min
 * ⇒ 本文件里所有趋势图/统计图**画的是真实存在的历史数据维度**，不会出现空图。
 *   取数接口：GET /api/v1/unified-data/historical-batch?device_pk=14&categories=...
 *   （生产封装见 src/composables/useHistoryData.ts）
 *
 * 24h 实测范围：SOC 7~12% · 总电压 50.9~51.8V · 电流 -4.7~1.5A ·
 *               单体 3.17~3.24V · 温度 26~30℃
 * 本 demo 的"当前时刻"取用户截图那一帧：SOC 7.0% · 51.76V · 2.60A（充电中）·
 * 单体 3.231~3.242V · 温度 26.5~28.1℃。趋势序列被约束在上述 24h 范围内，
 * 只有末尾的瞬时电流 2.6A 与当前帧对齐（充电流高于 24h 均值属正常工况）。
 */

export const DEVICE = {
  name: 'bms',
  online: true,
  model: 'jiabaida_bms',
  cellCount: 16,
  nodeSn: 'F0F5BD02DD84',
  channel: 'UART0',
  address: '#14',
  firmware: '2.8.0',
  updatedAt: '07:58:10',
} as const

export const METRICS = {
  /** 剩余电量 SOC（%） */
  soc: 7.0,
  /** 健康区间（%），用于副文本与进度条底色 */
  socHealthy: [20, 90] as const,
  /** 剩余容量（Ah） */
  remaining: 7.13,
  /** 标称容量（Ah） */
  nominal: 99,
  /** 总电压（V） */
  totalVoltage: 51.76,
  /** 电流（A），正 = 充电 */
  current: 2.6,
  /** 当前时间窗内的充放电状态文案 */
  currentDirection: '充电中' as const,
} as const

/** 16 串单体电压（V），总和 51.76V */
export const CELL_VOLTAGES: number[] = [
  3.236, 3.242, 3.234, 3.233, 3.235, 3.234, 3.237, 3.239,
  3.232, 3.233, 3.231, 3.234, 3.235, 3.233, 3.238, 3.234,
]

export const CELL_LABELS: string[] = CELL_VOLTAGES.map((_, i) => `#${i + 1}`)

/** 由数值推导的极值/压差（避免在模板里写死，防止与 CELL_VOLTAGES 脱节） */
export const CELL_STATS = (() => {
  const max = Math.max(...CELL_VOLTAGES)
  const min = Math.min(...CELL_VOLTAGES)
  const maxIndex = CELL_VOLTAGES.findIndex((v) => v === max)
  const minIndex = CELL_VOLTAGES.findIndex((v) => v === min)
  return {
    max,
    min,
    maxIndex,
    minIndex,
    diffMv: Math.round((max - min) * 1000),
    avg: CELL_VOLTAGES.reduce((a, b) => a + b, 0) / CELL_VOLTAGES.length,
  }
})()

/** 单体电压判定阈值（与项目既有口径一致：<3.0V 偏低 / >3.6V 偏高） */
export const CELL_THRESHOLDS = { low: 3.0, high: 3.6 } as const

export interface TempProbe {
  label: string
  value: number
}

export const TEMPS: TempProbe[] = [
  { label: '探头 1', value: 28.1 },
  { label: '探头 2', value: 26.5 },
  { label: '探头 3', value: 27.8 },
]

/** 温度刻度条量程（℃）与告警阈值 */
export const TEMP_SCALE = { min: 0, max: 60, warn: 45, danger: 60 } as const

export interface MosSwitch {
  key: 'charge' | 'discharge'
  name: string
  desc: string
  bit: string
  on: boolean
}

export const MOS_SWITCHES: MosSwitch[] = [
  { key: 'charge', name: '充电 MOS', desc: '允许充电', bit: 'bit0', on: true },
  { key: 'discharge', name: '放电 MOS', desc: '允许放电', bit: 'bit1', on: true },
]

/** fet_status 由 MOS 开关派生，避免与开关状态打架 */
export const FET_STATUS_HEX = '0x' + MOS_SWITCHES
  .reduce((mask, m, i) => (m.on ? mask | (1 << i) : mask), 0)
  .toString(16).padStart(2, '0')

export interface ProtectionBit {
  bit: number
  label: string
  active: boolean
}

/** 12 项保护（jiabaida 协议），全部未触发 */
export const PROTECTION_BITS: ProtectionBit[] = [
  { bit: 0, label: '单体过压', active: false },
  { bit: 1, label: '单体欠压', active: false },
  { bit: 2, label: '整组过压', active: false },
  { bit: 3, label: '整组欠压', active: false },
  { bit: 4, label: '充电过温', active: false },
  { bit: 5, label: '充电低温', active: false },
  { bit: 6, label: '放电过温', active: false },
  { bit: 7, label: '放电低温', active: false },
  { bit: 8, label: '充电过流', active: false },
  { bit: 9, label: '放电过流', active: false },
  { bit: 10, label: '短路保护', active: false },
  { bit: 11, label: '前端 IC 错误', active: false },
]

export const PROTECTION_STATUS_HEX = '0x' + PROTECTION_BITS
  .reduce((mask, p) => (p.active ? mask | (1 << p.bit) : mask), 0)
  .toString(16).padStart(2, '0')

export interface PollCommand {
  id: string
  name: string
  hint: string
  /** 启用时的模板默认间隔（ms） */
  defaultMs: number
  /** 当前生效间隔（ms），0 = 禁用 */
  intervalMs: number
}

/** 同时可轮询的指令条数上限（后端 MaxCommandsPerEdgeDevice 口径） */
export const POLL_LIMIT = 3

export const POLL_COMMANDS: PollCommand[] = [
  { id: 'read_basic_info', name: '读取基本信息', hint: '总压 / 电流 / 容量 / SOC', defaultMs: 5000, intervalMs: 0 },
  { id: 'read_cell_voltage', name: '读取单体电压', hint: '每串电压与极值', defaultMs: 5000, intervalMs: 0 },
  { id: 'read_hardware_version', name: '读取硬件版本', hint: '版本字符串', defaultMs: 60000, intervalMs: 0 },
  { id: 'read_comprehensive', name: '读取综合信息', hint: '综合帧（0x03 超集）', defaultMs: 1000, intervalMs: 1000 },
  { id: 'read_protection_count', name: '读取保护历史次数', hint: '12 类保护触发统计', defaultMs: 10000, intervalMs: 0 },
]

export interface OpItem {
  id: string
  name: string
  risky: boolean
}

export interface OpGroup {
  id: string
  name: string
  hint: string
  items: OpItem[]
}

/**
 * 受控操作目录（26 项 / 5 组）。分组口径与生产一致，
 * 但**展示方式不是本 demo 的重点**——重点是它在手机上不该是 26 行平铺。
 */
export const OP_GROUPS: OpGroup[] = [
  {
    id: 'read',
    name: '读取信息',
    hint: '一次性读取，不改配置',
    items: [
      { id: 'read_basic_info', name: '读取基本信息', risky: false },
      { id: 'read_cell_voltage', name: '读取单体电压', risky: false },
      { id: 'read_hardware_version', name: '读取硬件版本', risky: false },
      { id: 'read_comprehensive', name: '读取综合信息', risky: false },
      { id: 'read_protection_count', name: '读取保护历史次数', risky: false },
      { id: 'read_custom_attributes', name: '读取自定义属性', risky: false },
      { id: 'read_test_mos_status', name: '读取 MOS 测试状态', risky: false },
    ],
  },
  {
    id: 'diag',
    name: '诊断与测试',
    hint: '不写入参数，现场排障',
    items: [
      { id: 'find_car', name: '寻车（蜂鸣器）', risky: false },
      { id: 'test_charge_mos', name: '测试充电 MOS', risky: true },
      { id: 'test_discharge_mos', name: '测试放电 MOS', risky: true },
    ],
  },
  {
    id: 'runtime',
    name: '运行控制',
    hint: '直接影响当前运行状态',
    items: [
      { id: 'force_balance', name: '强制均衡', risky: true },
      { id: 'clear_alarm', name: '清除告警状态', risky: true },
      { id: 'auto_test_edv', name: '自动测试 EDV', risky: true },
      { id: 'bms_restart', name: '重启 BMS', risky: true },
      { id: 'set_mos_policy', name: '设置 MOS 策略', risky: true },
    ],
  },
  {
    id: 'param',
    name: '参数与标定',
    hint: '读写厂内参数',
    items: [
      { id: 'read_protection_parameters', name: '读取保护参数', risky: true },
      { id: 'read_system_parameters', name: '读取系统参数', risky: true },
      { id: 'write_protection_parameters', name: '写入保护参数', risky: true },
      { id: 'write_system_parameters', name: '写入系统参数', risky: true },
      { id: 'write_custom_attributes', name: '写入自定义属性', risky: true },
      { id: 'write_internal_resistance', name: '写入电芯内阻', risky: true },
      { id: 'write_sn', name: '写入 SN 码', risky: true },
    ],
  },
  {
    id: 'timer',
    name: '时间与上报',
    hint: '改变设备行为参数',
    items: [
      { id: 'set_static_correction_time', name: '设置静态修正时间', risky: true },
      { id: 'set_report_interval', name: '设置上报间隔', risky: true },
      { id: 'set_charge_time_window', name: '设置充电时间窗', risky: true },
      { id: 'set_discharge_time_limit', name: '设置放电时限', risky: true },
    ],
  },
]

export const OP_TOTAL = OP_GROUPS.reduce((n, g) => n + g.items.length, 0)

/** 每个方案都要用到的本地可变副本（demo 内可点，但不落任何后端） */
export function clonePollCommands(): PollCommand[] {
  return POLL_COMMANDS.map((c) => ({ ...c }))
}

export function cloneMos(): MosSwitch[] {
  return MOS_SWITCHES.map((m) => ({ ...m }))
}

/** 间隔 → 人类可读文案（单位只在这里出现一次，行内不再重复） */
export function formatInterval(ms: number): string {
  if (ms <= 0) return '已禁用'
  if (ms < 1000) return `${ms} ms`
  const s = ms / 1000
  return `${Number.isInteger(s) ? s : s.toFixed(1)} 秒`
}
/* ══════════════════════════════════════════════════════════════════════════
   统计类图表用的历史 mock（新增信息，不是现有截面的换皮）

   数据形态对齐后端真实字段：
     · temp_1..3   温度探头读数（℃）
     · rsoc        剩余电量（%）
     · current     电流（A，正 = 充电）
   时间窗 08:00–20:00 每 2 小时一个采样点，共 7 点 —— 手机上 7 个刻度
   正好是「能读出趋势、又不挤」的密度（12 点以上在 340px 里必定重叠）。
   ══════════════════════════════════════════════════════════════════════════ */

export const TREND_TIMES: string[] = ['08:00', '10:00', '12:00', '14:00', '16:00', '18:00', '20:00']

/** 3 路温度：探头 2 一直比另两路低 1.6℃，探头 1 在 14 点后单独爬升（局部发热的典型形态） */
export const TREND_TEMPS: number[][] = [
  [26.0, 26.9, 27.4, 29.6, 30.0, 29.3, 28.1],
  [26.1, 26.4, 26.8, 27.0, 27.2, 27.4, 26.5],
  [26.3, 26.9, 27.4, 28.0, 28.4, 28.2, 27.8],
]

/** SOC：从 12.4% 缓慢掉到 7.0%（放电），14 点后回升 = 开始充电 */
export const TREND_SOC: number[] = [12.4, 11.6, 10.4, 8.9, 7.6, 7.1, 7.0]

/** 电流（A）：正 = 充电。前段为小幅放电（负），18 点起转充电 */
export const TREND_CURRENT: number[] = [-1.2, -1.4, -1.8, -2.1, -1.6, 1.4, 2.6]

/** 12 类保护的历史触发次数（「读取保护历史次数」指令的真实语义）*/
export const PROTECTION_HISTORY: Array<{ label: string; count: number }> = [
  { label: '单体过压', count: 0 },
  { label: '单体欠压', count: 1 },
  { label: '整组过压', count: 0 },
  { label: '整组欠压', count: 0 },
  { label: '充电过温', count: 0 },
  { label: '充电低温', count: 0 },
  { label: '放电过温', count: 2 },
  { label: '放电低温', count: 0 },
  { label: '充电过流', count: 0 },
  { label: '放电过流', count: 3 },
  { label: '短路保护', count: 0 },
  { label: '前端 IC 错误', count: 0 },
]

export const PROTECTION_HISTORY_TOTAL = PROTECTION_HISTORY.reduce((n, i) => n + i.count, 0)
export const PROTECTION_HISTORY_WINDOW = '近 30 天'

/* 温度判定阈值（与生产页口径一致：>45℃ 偏高 / >60℃ 过高） */
export const TEMP_THRESHOLDS = { warn: 45, danger: 60 } as const

/** 轮询负载：每条指令的「每秒上报帧数」= 1000 / interval，用于采样压力判断 */
export function pollLoadPerSec(intervalMs: number): number {
  return intervalMs > 0 ? 1000 / intervalMs : 0
}
