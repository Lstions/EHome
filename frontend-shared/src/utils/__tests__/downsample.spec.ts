import { describe, it, expect } from 'vitest'
import { downsampleData, downsampleMultiSeries, downsampleMultiSeriesAligned, type TimeSeriesPoint } from '../downsample'

// Helper: generate N points with constant value
function makeConstant(n: number, value: number, startMs = 1000000): TimeSeriesPoint[] {
  return Array.from({ length: n }, (_, i) => ({
    time: new Date(startMs + i * 1000).toISOString(),
    value,
  }))
}

// Helper: generate N points with linearly increasing value
function makeLinear(n: number, startMs = 1000000): TimeSeriesPoint[] {
  return Array.from({ length: n }, (_, i) => ({
    time: new Date(startMs + i * 1000).toISOString(),
    value: i,
  }))
}

// Helper: generate N points with a single spike at index k
function makeSpike(n: number, k: number, base: number, spike: number): TimeSeriesPoint[] {
  return Array.from({ length: n }, (_, i) => ({
    time: new Date(1000000 + i * 1000).toISOString(),
    value: i === k ? spike : base,
  }))
}

// ── downsampleData ──────────────────────────────────

describe('downsampleData', () => {
  it('returns data as-is when length <= 2', () => {
    const data = makeConstant(2, 42)
    const result = downsampleData(data)
    expect(result).toBe(data) // same reference, not copied
  })

  it('returns empty array for empty input', () => {
    const result = downsampleData([])
    expect(result).toEqual([])
  })

  it('removes consecutive duplicate values', () => {
    // 10 points all value=5 → should collapse to first + last = 2 points
    const data = makeConstant(10, 5)
    const result = downsampleData(data)
    expect(result.length).toBe(2)
    expect(result[0].value).toBe(5)
    expect(result[result.length - 1].value).toBe(5)
  })

  it('keeps transition points (step function)', () => {
    // 0,0,0,0,5,5,5,5,0,0 → should keep index 0, 3(transition), 4, 7(transition), 8, 9
    // Actually algorithm keeps points where curr differs from prev OR next differs from curr
    // So: 0(kept, first), 0(prev=0, next=0→no), 0(no), 0(prev=0,next=5→YES), 5(prev=0→YES), 5(no), 5(no), 5(prev=5,next=0→YES), 0(prev=5→YES), 0(last)
    const data: TimeSeriesPoint[] = Array.from({ length: 10 }, (_, i) => ({
      time: new Date(1000000 + i * 1000).toISOString(),
      value: (i >= 4 && i <= 7) ? 5 : 0,
    }))
    const result = downsampleData(data)
    // Should keep transition points: index 3, 4, 7, 8 + first(0) + last(9)
    expect(result.length).toBeLessThan(data.length)
    expect(result[0].value).toBe(0)
    expect(result[result.length - 1].value).toBe(0)
    // Verify spike is preserved
    const values = result.map(p => p.value)
    expect(values).toContain(5)
  })

  it('respects epsilon for near-duplicate values', () => {
    const data: TimeSeriesPoint[] = Array.from({ length: 10 }, (_, i) => ({
      time: new Date(1000000 + i * 1000).toISOString(),
      value: 10 + i * 0.0001, // changes by 0.0001 < epsilon=0.001
    }))
    const result = downsampleData(data, 500, 0.001)
    // All values within epsilon → collapse to first + last
    expect(result.length).toBe(2)
  })

  it('uniform-samples when deduped result exceeds maxPoints', () => {
    // 2000 linearly increasing points → dedup keeps all (no duplicates) → uniform sample
    const data = makeLinear(2000)
    const result = downsampleData(data, 100)
    expect(result.length).toBeLessThanOrEqual(102) // ~100 + first + last
    expect(result.length).toBeGreaterThanOrEqual(50)
    // First and last should be preserved
    expect(result[0].value).toBe(0)
    expect(result[result.length - 1].value).toBe(1999)
  })

  it('preserves first and last points always', () => {
    const data = makeSpike(100, 50, 10, 99)
    const result = downsampleData(data, 50)
    expect(result[0].value).toBe(10)
    expect(result[result.length - 1].value).toBe(10)
  })

  it('handles single spike correctly', () => {
    const data = makeSpike(20, 10, 0, 100)
    const result = downsampleData(data)
    // Spike at index 10 should be preserved
    const values = result.map(p => p.value)
    expect(values).toContain(100)
  })
})

// ── downsampleMultiSeries ──────────────────────────

describe('downsampleMultiSeries', () => {
  it('returns empty array for empty input', () => {
    const result = downsampleMultiSeries([])
    expect(result).toEqual([])
  })

  it('returns copies when length <= 2', () => {
    const series = [makeConstant(2, 1), makeConstant(2, 2)]
    const result = downsampleMultiSeries(series)
    expect(result.length).toBe(2)
    expect(result[0].length).toBe(2)
    expect(result[1].length).toBe(2)
  })

  it('falls back to independent downsampling when lengths differ', () => {
    const series = [makeConstant(10, 1), makeConstant(5, 2)]
    const result = downsampleMultiSeries(series)
    expect(result.length).toBe(2)
    // Each series is independently downsampled
    // 10 constant → 2 points, 5 constant → 2 points
    expect(result[0].length).toBe(2)
    expect(result[1].length).toBe(2)
  })

  it('all output series share the same timestamps', () => {
    // 2 series, same length, different patterns
    const n = 50
    const series = [
      makeSpike(n, 25, 0, 100),  // spike at index 25
      makeSpike(n, 10, 0, 200),  // spike at index 10
    ]
    const result = downsampleMultiSeries(series, 500)
    // All series should have the same length
    expect(result[0].length).toBe(result[1].length)
    // All timestamps should match
    for (let i = 0; i < result[0].length; i++) {
      expect(result[0][i].time).toBe(result[1][i].time)
    }
  })

  it('keeps indices where ANY series changes value', () => {
    // Series 1: constant 0
    // Series 2: spike at index 10
    const n = 20
    const series = [
      makeConstant(n, 0),
      makeSpike(n, 10, 0, 99),
    ]
    const result = downsampleMultiSeries(series, 500)
    // The spike in series 2 should cause index 10 to be kept in BOTH series
    expect(result[0].length).toBe(result[1].length)
    // Series 2 should still have the spike value
    const values2 = result[1].map(p => p.value)
    expect(values2).toContain(99)
  })

  it('uniform-samples when kept indices exceed maxPoints', () => {
    // 3 series all linearly increasing → all indices kept → need uniform sampling
    const series = [makeLinear(2000), makeLinear(2000), makeLinear(2000)]
    const result = downsampleMultiSeries(series, 100)
    expect(result[0].length).toBeLessThanOrEqual(102)
    expect(result[0].length).toBe(result[1].length)
    expect(result[1].length).toBe(result[2].length)
  })
})

// ── downsampleMultiSeriesAligned ───────────────────

describe('downsampleMultiSeriesAligned', () => {
  it('returns empty array for empty input', () => {
    const result = downsampleMultiSeriesAligned([])
    expect(result).toEqual([])
  })

  it('delegates to downsampleData for single series', () => {
    const data = makeConstant(10, 5)
    const result = downsampleMultiSeriesAligned([data])
    expect(result.length).toBe(1)
    expect(result[0].length).toBe(2) // constant → dedup to 2
  })

  it('delegates to downsampleMultiSeries when all same length+time', () => {
    const data = makeLinear(20)
    const series = [data, [...data].map(p => ({ ...p, value: p.value * 2 }))]
    const result = downsampleMultiSeriesAligned(series)
    // Should use fast path — all same timestamps
    expect(result[0].length).toBe(result[1].length)
    for (let i = 0; i < result[0].length; i++) {
      expect(result[0][i].time).toBe(result[1][i].time)
    }
  })

  it('aligns series with different timestamps via nearest-neighbor', () => {
    // Series 1: timestamps at t=0, 2, 4, 6, 8
    // Series 2: timestamps at t=1, 3, 5, 7, 9
    const series1: TimeSeriesPoint[] = [0, 2, 4, 6, 8].map(t => ({
      time: new Date(1000000 + t * 1000).toISOString(),
      value: t,
    }))
    const series2: TimeSeriesPoint[] = [1, 3, 5, 7, 9].map(t => ({
      time: new Date(1000000 + t * 1000).toISOString(),
      value: t * 10,
    }))
    const result = downsampleMultiSeriesAligned([series1, series2])
    // All output series should share the same timestamps
    expect(result[0].length).toBe(result[1].length)
    for (let i = 0; i < result[0].length; i++) {
      expect(result[0][i].time).toBe(result[1][i].time)
    }
  })

  it('preserves spike values across aligned series', () => {
    // Series 1: flat with spike at t=5
    // Series 2: linear
    const n = 20
    const series1: TimeSeriesPoint[] = Array.from({ length: n }, (_, i) => ({
      time: new Date(1000000 + i * 2000).toISOString(), // every 2s
      value: i === 5 ? 999 : 0,
    }))
    const series2: TimeSeriesPoint[] = Array.from({ length: n }, (_, i) => ({
      time: new Date(1000000 + i * 2000 + 1000).toISOString(), // offset 1s
      value: i,
    }))
    const result = downsampleMultiSeriesAligned([series1, series2], 500)
    // Series 1 spike should be preserved
    const values1 = result[0].map(p => p.value)
    expect(values1).toContain(999)
  })
})
