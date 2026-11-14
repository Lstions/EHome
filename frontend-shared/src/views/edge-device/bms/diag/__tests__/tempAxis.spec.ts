import { describe, expect, it } from 'vitest'
import {
  resolveTempYMin,
  resolveTempYTickCount,
  TEMP_Y_BASE_MIN,
  TEMP_Y_MAX,
} from '@/views/edge-device/bms/diag/tempAxis'

/**
 * 温度 Y 轴量程（2026-11-14「图表显示不全」）。
 *
 * 线上数据实测：7 天窗口三路探头最低 13.95℃，其中 15%~24% 的点低于 20℃；
 * 而卡片把 Y 轴写死 [20, 50] ⇒ 这些点被裁到画布外，曲线在底边消失又出现。
 */
describe('resolveTempYMin（自适应 Y 下限）', () => {
  it('数据全部 ≥20℃ ⇒ 保持基准 20（跨设备可比、45℃ 线位置稳定）', () => {
    expect(resolveTempYMin([22.1, 31.0, 25.5])).toBe(TEMP_Y_BASE_MIN)
    expect(resolveTempYMin([20])).toBe(TEMP_Y_BASE_MIN)
    expect(resolveTempYMin([])).toBe(TEMP_Y_BASE_MIN)
  })

  it('夜间低温（13.95℃）⇒ 下限降到 10，且留 2℃ 余量', () => {
    expect(resolveTempYMin([13.95, 30, 25])).toBe(10)
    // 19.9 只差 0.1℃ 也要落到 10 档 —— 宁可多留空间，不可裁点。
    expect(resolveTempYMin([19.9])).toBe(10)
  })

  it('更低的温度（9℃）⇒ 下限 0，不会出现负值', () => {
    expect(resolveTempYMin([9])).toBe(0)
    expect(resolveTempYMin([-3])).toBe(0)
  })
})

describe('resolveTempYTickCount（刻度间隔恒 10℃）', () => {
  it('(上限-下限)/段数 必须整除 10 —— 否则标签会变成 23.3 这种非整数', () => {
    for (const lo of [0, 10, 20]) {
      const ticks = resolveTempYTickCount(lo, TEMP_Y_MAX)
      expect((TEMP_Y_MAX - lo) % ticks).toBe(0)
      expect((TEMP_Y_MAX - lo) / ticks).toBe(10)
    }
  })

  it('基准量程仍是 3 段（20/30/40/50），与改前逐像素一致', () => {
    expect(resolveTempYTickCount(TEMP_Y_BASE_MIN, TEMP_Y_MAX)).toBe(3)
  })
})
