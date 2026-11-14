import { describe, expect, it } from 'vitest'
import { computeSeriesSpanDays, formatXAxisTick } from '@/components/charts/LineChart.vue'

/**
 * X 轴时间标签的回归守卫（2026-11-14）。
 *
 * 线上症状：BMS 页 7 天温度趋势的最右侧两个刻度只剩 `00:00`（11/13、11/14 的日期
 * 整段消失），配合曲线在底边被裁切，用户读成「图表显示不全」。
 *
 * 根因：旧实现按「该刻度距现在多少天」决定格式（>2 天带日期），于是 7 天图里
 * **离现在最近的两天**恰好丢掉日期。判据必须是**整图跨度**。
 */
describe('formatXAxisTick（X 轴时间标签）', () => {
  const day = (m: number, d: number, h = 0, min = 0) => new Date(2026, m - 1, d, h, min).getTime()

  it('跨度 > 2 天（7 天图）⇒ 每个刻度都带日期，包括离现在最近的刻度', () => {
    // 这就是旧判据翻车的那个点：11/13、11/14 距 now 不到 2 天。
    expect(formatXAxisTick(day(11, 13), 7, true)).toBe('11/13')
    expect(formatXAxisTick(day(11, 14), 7, true)).toBe('11/14')
    // 非出血档（宽屏）保留时分。
    expect(formatXAxisTick(day(11, 13), 7, false)).toBe('11/13 00:00')
  })

  it('跨度 ≤ 2 天（1h / 24h）⇒ 只显示时刻', () => {
    expect(formatXAxisTick(day(11, 14, 14, 5), 1, true)).toBe('14:05')
    expect(formatXAxisTick(day(11, 14, 9, 7), 2, false)).toBe('09:07')
  })

  it('小一大时仍可读（补零）', () => {
    expect(formatXAxisTick(day(1, 3, 4, 5), 7, false)).toBe('01/03 04:05')
  })
})

describe('computeSeriesSpanDays（由数据算跨图跨度）', () => {
  it('多序列取并集的首末时间', () => {
    const span = computeSeriesSpanDays([
      { data: [{ time: '2026-11-07T14:56:10Z' }, { time: '2026-11-12T00:00:00Z' }] },
      { data: [{ time: '2026-11-10T00:00:00Z' }, { time: '2026-11-14T14:19:10Z' }] },
    ])
    expect(span).toBeCloseTo(7, 1)
  })

  it('单序列 / 单点 / 空数据 ⇒ 0（走时刻格式，不误判成 7 天）', () => {
    expect(computeSeriesSpanDays([{ data: [{ time: '2026-11-14T00:00:00Z' }] }])).toBe(0)
    expect(computeSeriesSpanDays([])).toBe(0)
    expect(computeSeriesSpanDays(undefined, [{ time: '2026-11-14T00:00:00Z' }])).toBe(0)
  })

  it('无 series 时回落到 props.data', () => {
    const span = computeSeriesSpanDays(undefined, [
      { time: '2026-11-08T00:00:00Z' },
      { time: '2026-11-09T00:00:00Z' },
    ])
    expect(span).toBeCloseTo(1, 5)
  })
})
