/**
 * 温度趋势图的 Y 轴量程（2026-11-14 修复「图表显示不全」）。
 *
 * ============ 症状与真因 ============
 *
 * BMS 页 7 天温度趋势里，曲线反复「掉到底边消失、过一阵又从底边冒出来」，
 * 每个缺口看起来都像丢数据；右侧日期刻度还会只剩 00:00。
 *
 * 真因**不在数据**：同一窗口的接口实测 499 点、无 >30min 的缺口、末点距现在 17 分钟。
 * 而是 TempTrendCard 把 Y 轴写死成 [20, 50]，夜间三路探头最低到 13.95℃
 * （7 天窗口内 15%~24% 的点低于 20℃）—— 低于下限的点被 ECharts 裁掉，
 * 线在 20℃ 处消失、再从 20℃ 冒出来，于是「看起来缺了一大段」。
 *
 * ============ 规则 ============
 *
 *   · 数据全部 ≥ 基准线（20℃）⇒ 保持 20：跨设备可比、45℃ 告警线位置稳定；
 *   · 一旦低于基准 ⇒ 下限下调到 10℃ 的整数倍（0 / 10），并在最低点下留 2℃ 余量；
 *   · (上限 - 下限) 始终能被 10 整除 ⇒ 刻度间隔恒为 10℃，标签仍是整数。
 */
export const TEMP_Y_BASE_MIN = 20
export const TEMP_Y_MAX = 50
export const TEMP_Y_STEP = 10

/**
 * 由数据点（或最低温）推出 Y 轴下限。
 *
 * @param values 所有温度采样（可只传最低温一个值）
 * @param base   基准下限（默认 20℃）
 */
export function resolveTempYMin(values: readonly number[], base: number = TEMP_Y_BASE_MIN): number {
  let lo = Infinity
  for (const v of values) {
    if (Number.isFinite(v) && v < lo) lo = v
  }
  if (!Number.isFinite(lo) || lo >= base) return base
  // floor 到 10℃ 档：13.95 → 10、9 → 0；再减 2℃ 余量保证曲线不贴底。
  return Math.max(0, Math.floor((lo - 2) / TEMP_Y_STEP) * TEMP_Y_STEP)
}

/** 与 Y 轴上限配套的刻度段数：保证 axis interval 恒为 10℃。 */
export function resolveTempYTickCount(
  yMin: number,
  yMax: number = TEMP_Y_MAX,
  step: number = TEMP_Y_STEP,
): number {
  const n = Math.round((yMax - yMin) / step)
  return n >= 2 ? n : 2
}
