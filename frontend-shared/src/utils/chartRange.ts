/**
 * Adaptive Y-axis range computation for charts.
 *
 * Extracted from BmsCellVoltageHistoryChart.vue and BmsCellVoltageChart.vue
 * where the same algorithm was duplicated.
 *
 * Algorithm:
 * 1. Compute data min/max.
 * 2. Add padding: if span < flatPadding use flatPadding, else span * paddingRatio.
 * 3. Clamp to [minBound, maxBound].
 * 4. Enforce minimum visible range (minSpan).
 * 5. Floor min / ceil max to the specified decimal places.
 */

export interface AdaptiveYAxisRangeOptions {
  /** Lower bound to clamp min to (default: -Infinity) */
  minBound?: number
  /** Upper bound to clamp max to (default: Infinity) */
  maxBound?: number
  /** Minimum visible range; if max-min < this, expand symmetrically (default: 0.1) */
  minSpan?: number
  /** Padding ratio relative to data span (default: 0.3) */
  paddingRatio?: number
  /** Flat padding used when data span is very small (default: 0.05) */
  flatPadding?: number
  /** Decimal places for floor/ceil rounding (default: 2) */
  decimals?: number
}

export function computeAdaptiveYAxisRange(
  values: number[],
  options?: AdaptiveYAxisRangeOptions,
): { min: number; max: number } {
  if (values.length === 0) return { min: 0, max: 1 }

  const {
    minBound = -Infinity,
    maxBound = Infinity,
    minSpan = 0.1,
    paddingRatio = 0.3,
    flatPadding = 0.05,
    decimals = 2,
  } = options ?? {}

  const dataMin = values.reduce((a, b) => (a < b ? a : b), Infinity)
  const dataMax = values.reduce((a, b) => (a > b ? a : b), -Infinity)
  const span = dataMax - dataMin

  // If all values are identical (or nearly so), use flat padding
  const padding = span < flatPadding ? flatPadding : span * paddingRatio
  let min = dataMin - padding
  let max = dataMax + padding

  // Clamp to safe bounds
  if (min < minBound) min = minBound
  if (max > maxBound) max = maxBound

  // Ensure minimum visible range
  if (max - min < minSpan) {
    const center = (min + max) / 2
    min = center - minSpan / 2
    max = center + minSpan / 2
    // Re-clamp after expanding
    if (min < minBound) min = minBound
    if (max > maxBound) max = maxBound
  }

  // Round to nice values: floor min, ceil max
  const factor = Math.pow(10, decimals)
  min = Math.floor(min * factor) / factor
  max = Math.ceil(max * factor) / factor

  return { min, max }
}
