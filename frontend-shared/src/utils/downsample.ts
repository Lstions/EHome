/**
 * Downsample time-series data to prevent chart lag.
 *
 * Single-series mode (downsampleData):
 * 1. Remove consecutive duplicate values — keep only transition points.
 * 2. If still > maxPoints, uniform-sample to cap the total.
 *
 * Multi-series aligned mode (downsampleMultiSeries):
 * For synchronized sensor arrays (e.g. 16 BMS cells sampled at the same
 * timestamps), downsampling each series independently produces misaligned
 * time points — the tooltip at a given time only finds data for series
 * that happened to retain a point at that exact timestamp.
 * Instead, we determine which timestamps to keep by checking if ANY series
 * changed value at that point, then keep those timestamps across ALL series.
 * This guarantees all series share the same set of timestamps after downsampling.
 */

export interface TimeSeriesPoint {
  time: string
  value: number
}

export function downsampleData<T extends TimeSeriesPoint>(
  data: T[],
  maxPoints = 500,
  epsilon = 0.001
): T[] {
  if (data.length <= 2) return data

  // Step 1: Remove consecutive duplicate values
  const deduped: T[] = [data[0]]
  for (let i = 1; i < data.length - 1; i++) {
    const prev = deduped[deduped.length - 1]
    const curr = data[i]
    const next = data[i + 1]
    if (
      Math.abs(curr.value - prev.value) > epsilon ||
      Math.abs(next.value - curr.value) > epsilon
    ) {
      deduped.push(curr)
    }
  }
  deduped.push(data[data.length - 1])

  // Step 2: If still too many points, uniform-sample
  if (deduped.length <= maxPoints) return deduped

  const step = Math.ceil(deduped.length / maxPoints)
  const sampled: T[] = [deduped[0]]
  for (let i = step; i < deduped.length - 1; i += step) {
    sampled.push(deduped[i])
  }
  sampled.push(deduped[deduped.length - 1])
  return sampled
}

/**
 * Downsample multiple synchronized series so they share the same timestamps.
 *
 * Assumes all series have the same length and are sampled at the same times
 * (which is true for BMS cell voltages — all 16 cells are read in one poll).
 *
 * Algorithm:
 * 1. Walk through all series simultaneously. For each index, check if ANY
 *    series has a value change (vs previous kept index) exceeding epsilon.
 * 2. If any series changed, keep that index across ALL series.
 * 3. If the kept indices still exceed maxPoints, uniform-sample the index set.
 * 4. Always keep first and last indices.
 */
export function downsampleMultiSeries<T extends TimeSeriesPoint>(
  series: T[][],
  maxPoints = 500,
  epsilon = 0.001
): T[][] {
  if (series.length === 0) return []
  const len = series[0].length
  if (len <= 2) return series.map(s => [...s])

  // Verify all series have the same length
  for (const s of series) {
    if (s.length !== len) {
      // Lengths differ — fall back to independent downsampling
      return series.map(s => downsampleData(s, maxPoints, epsilon))
    }
  }

  // Step 1: Find indices to keep — any index where any series changes value
  const keepIndices = new Set<number>()
  keepIndices.add(0)
  keepIndices.add(len - 1)

  for (let i = 1; i < len - 1; i++) {
    let anyChanged = false
    for (const s of series) {
      if (Math.abs(s[i].value - s[i - 1].value) > epsilon ||
          Math.abs(s[i + 1].value - s[i].value) > epsilon) {
        anyChanged = true
        break
      }
    }
    if (anyChanged) keepIndices.add(i)
  }

  // Step 2: If still too many, uniform-sample the kept indices
  let finalIndices = Array.from(keepIndices).sort((a, b) => a - b)

  if (finalIndices.length > maxPoints) {
    const step = Math.ceil(finalIndices.length / maxPoints)
    const sampled: number[] = [finalIndices[0]]
    for (let i = step; i < finalIndices.length - 1; i += step) {
      sampled.push(finalIndices[i])
    }
    sampled.push(finalIndices[finalIndices.length - 1])
    finalIndices = sampled
  }

  // Step 3: Build output — each series gets the same set of timestamps
  return series.map(s => finalIndices.map(idx => s[idx]))
}

/**
 * Downsample multiple series that may have DIFFERENT timestamps.
 *
 * Unlike downsampleMultiSeries (which requires all series to have identical
 * length and timestamps), this function handles series whose timestamps are
 * slightly misaligned (e.g. BMS rsoc and remaining_capacity are polled at
 * slightly different times, resulting in 92008 vs 92009 points).
 *
 * Algorithm:
 * 1. Collect the union of all timestamps across all series, sorted.
 * 2. For each timestamp, interpolate each series' value (nearest-neighbor).
 * 3. Run the multi-series dedup on the aligned data (keep indices where ANY
 *    series changes value).
 * 4. If still > maxPoints, uniform-sample the index set.
 *
 * This guarantees all output series share the exact same timestamps, so
 * ECharts tooltip at any time point shows all series.
 */
export function downsampleMultiSeriesAligned<T extends TimeSeriesPoint>(
  series: T[][],
  maxPoints = 500,
  epsilon = 0.001
): T[][] {
  if (series.length === 0) return []
  if (series.length === 1) return [downsampleData(series[0], maxPoints, epsilon)]

  // If all series happen to have the same length AND same timestamps,
  // use the original (faster) algorithm.
  const len = series[0].length
  let allSameLen = true
  let allSameTime = true
  for (const s of series) {
    if (s.length !== len) { allSameLen = false; break }
  }
  if (allSameLen) {
    for (let i = 0; i < len; i++) {
      const t = series[0][i].time
      for (const s of series) {
        if (s[i].time !== t) { allSameTime = false; break }
      }
      if (!allSameTime) break
    }
  }
  if (allSameLen && allSameTime) {
    return downsampleMultiSeries(series, maxPoints, epsilon)
  }

  // Step 1: Collect union of all timestamps (as ms numbers for sorting)
  const allTsSet = new Set<number>()
  for (const s of series) {
    for (const p of s) {
      allTsSet.add(new Date(p.time).getTime())
    }
  }
  const allTs = Array.from(allTsSet).sort((a, b) => a - b)

  if (allTs.length <= 2) {
    // Not enough data — return as-is
    return series.map(s => s.length > 0 ? [...s] : [])
  }

  // Step 2: Build aligned series via nearest-neighbor interpolation
  // For each series, pre-sort by timestamp and use binary search
  const aligned: T[][] = series.map(s => {
    const sorted = [...s].sort((a, b) =>
      new Date(a.time).getTime() - new Date(b.time).getTime()
    )
    return allTs.map(ts => {
      // Binary search for nearest timestamp in this series
      let lo = 0, hi = sorted.length - 1
      while (lo < hi) {
        const mid = (lo + hi) >> 1
        const midTs = new Date(sorted[mid].time).getTime()
        if (midTs < ts) lo = mid + 1
        else hi = mid
      }
      // Check if lo-1 is closer
      if (lo > 0) {
        const d1 = Math.abs(new Date(sorted[lo].time).getTime() - ts)
        const d2 = Math.abs(new Date(sorted[lo - 1].time).getTime() - ts)
        if (d2 < d1) lo--
      }
      return { time: new Date(ts).toISOString(), value: sorted[lo].value }
    })
  })

  // Step 3: Multi-series dedup — keep indices where any series changes value
  const totalLen = allTs.length
  const keepIndices = new Set<number>()
  keepIndices.add(0)
  keepIndices.add(totalLen - 1)

  for (let i = 1; i < totalLen - 1; i++) {
    let anyChanged = false
    for (const s of aligned) {
      if (Math.abs(s[i].value - s[i - 1].value) > epsilon ||
          Math.abs(s[i + 1].value - s[i].value) > epsilon) {
        anyChanged = true
        break
      }
    }
    if (anyChanged) keepIndices.add(i)
  }

  // Step 4: If still too many, uniform-sample
  let finalIndices = Array.from(keepIndices).sort((a, b) => a - b)
  if (finalIndices.length > maxPoints) {
    const step = Math.ceil(finalIndices.length / maxPoints)
    const sampled: number[] = [finalIndices[0]]
    for (let i = step; i < finalIndices.length - 1; i += step) {
      sampled.push(finalIndices[i])
    }
    sampled.push(finalIndices[finalIndices.length - 1])
    finalIndices = sampled
  }

  // Step 5: Build output — all series share the same timestamps
  return aligned.map(s => finalIndices.map(idx => s[idx]))
}
