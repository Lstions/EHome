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
