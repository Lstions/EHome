/**
 * 保护位掩码的「跃变计数」—— diag 卡片共用（生产实现，不是 demo）。
 *
 * ── 为什么必须用跃变而不是求和 ──────────────────────────────────────────────
 * 生产实测（设备 14，categories=protection_status）：
 *   近 7 天   5142 点  非零 0     唯一值 [0]
 *   近 30 天 20520 点  非零 1239  唯一值 [0, 1]
 *   近 90 天 20521 点  非零 1239  唯一值 [0, 1]
 *
 * 非零值**恒为 1** ⇒ 这是「当前是否有该保护」的**状态位序列**，不是累计计数。
 * 因此把值直接相加得到的数字是错的（0,0,1,1,0,1 求和 = 3，但只触发了 2 次）。
 * 正确口径：0→1 跃变算一次触发开始，1→0 算结束；持续时长 = 处于 1 的各采样区间时长之和。
 *
 * 本模块是纯函数、无副作用、不依赖 Vue，便于单测直接锁口径
 * （见 __tests__/BmsDiagCharts.spec.ts 的「跃变计数不能退化成求和」用例）。
 */

/** 历史点位的最小形状（与 useHistoryData 的 HistoryDataPoint 对齐） */
export interface HistoryPointLike {
  time: string | number
  value: number
}

/** 单个保护位的统计结果 */
export interface BitTransitionStat {
  /** 位号（0..11） */
  bit: number
  /** 中文名（如「单体过压」） */
  label: string
  /** 0→1 跃变次数 = 触发次数。**绝不是数值求和** */
  count: number
  /** 处于触发态的累计时长（ms）。窗口起点即已触发的那段不计入（起点未知） */
  activeMs: number
  /** 最近一次 0→1 的时间戳（ms）；从未触发为 null */
  lastTriggerAt: number | null
}

/** 取某个位上的状态（0 / 1） */
export function bitAt(value: number, bit: number): 0 | 1 {
  const v = Math.floor(Number(value) || 0)
  return ((v >> bit) & 1) === 1 ? 1 : 0
}

/** 点位按时间升序排列（后端通常已有序；排序是防御性的，避免乱序把跃变算反） */
export function sortByTime(points: HistoryPointLike[]): HistoryPointLike[] {
  return [...points].sort((a, b) => toMs(a.time) - toMs(b.time))
}

/** time 既可能是 ISO 字符串也可能是毫秒数 */
export function toMs(time: string | number): number {
  if (typeof time === 'number') return Number.isFinite(time) ? time : 0
  const n = new Date(time).getTime()
  return Number.isFinite(n) ? n : 0
}

/**
 * 统计某个位的 0→1 跃变次数。
 *
 * ⚠ 边界语义（写清楚，避免后人"优化"成求和）：
 *   · 窗口**起点即为 1** 时不算一次触发 —— 起点之前的状态不可知，
 *     把它当触发会凭空多算一次。这类跨窗口的触发由 activeMs 体现。
 *   · 连续多个 1 只算一次（1→1 不是跃变）。
 */
export function countRisingEdges(points: HistoryPointLike[], bit: number): number {
  const sorted = sortByTime(points)
  if (sorted.length < 2) return 0
  // 关键：prev 必须从**首点实际状态**起步，而不是 0。
  // 若首点就是 1，上一行「起点即为 1 不算触发」的口径才成立；
  // 固定 prev=0 会把窗口起点的 1 误算成一次触发（实测会凭空多算一次）。
  let prev: 0 | 1 = bitAt(sorted[0].value, bit)
  let count = 0
  for (let i = 1; i < sorted.length; i += 1) {
    const cur = bitAt(sorted[i].value, bit)
    if (prev === 0 && cur === 1) count += 1
    prev = cur
  }
  return count
}

/** 统计某个位处于 1 的累计时长（ms）：相邻两点间，若前一点为 1 则整段计入 */
export function measureActiveMs(points: HistoryPointLike[], bit: number): number {
  const sorted = sortByTime(points)
  let total = 0
  for (let i = 0; i < sorted.length - 1; i += 1) {
    if (bitAt(sorted[i].value, bit) !== 1) continue
    const delta = toMs(sorted[i + 1].time) - toMs(sorted[i].time)
    // 异常时间戳（倒序/相等/NaN）不贡献时长，避免把总时长算成负数或天文数字
    if (Number.isFinite(delta) && delta > 0) total += delta
  }
  return total
}

/**
 * 把 protection_status 序列按位展开，产出 12 个位的统计。
 *
 * @param points    protection_status 的历史点位（位掩码整数）
 * @param labels    12 个位的中文名，下标即位号
 */
export function decodeProtectionBits(
  points: HistoryPointLike[],
  labels: readonly string[],
): BitTransitionStat[] {
  const sorted = sortByTime(points)
  return labels.map((label, bit) => {
    let count = 0
    let activeMs = 0
    let lastTriggerAt: number | null = null
    if (sorted.length > 0) {
      // 同 countRisingEdges：prev 从首点实际状态起步，窗口起点即为 1 不算一次触发
      // （那段跨窗口的持续时长仍会计入 activeMs，见下方 prev === 1 分支）
      let prev: 0 | 1 = bitAt(sorted[0].value, bit)
      for (let i = 1; i < sorted.length; i += 1) {
        const cur = bitAt(sorted[i].value, bit)
        if (prev === 0 && cur === 1) {
          count += 1
          lastTriggerAt = toMs(sorted[i].time)
        }
        if (prev === 1) {
          const delta = toMs(sorted[i].time) - toMs(sorted[i - 1].time)
          if (Number.isFinite(delta) && delta > 0) activeMs += delta
        }
        prev = cur
      }
    }
    return { bit, label, count, activeMs, lastTriggerAt }
  })
}

/** 毫秒时长 → 人类可读（"45 分钟" / "2.3 小时" / "3 天"），用于「持续时长」文案 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0 分钟'
  const minutes = ms / 60000
  if (minutes < 1) return '不足 1 分钟'
  if (minutes < 60) return `${Math.round(minutes)} 分钟`
  const hours = minutes / 60
  if (hours < 24) return `${hours.toFixed(hours < 10 ? 1 : 0)} 小时`
  const days = hours / 24
  return `${days.toFixed(days < 10 ? 1 : 0)} 天`
}
