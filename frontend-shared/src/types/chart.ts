/**
 * Shared chart data types.
 *
 * HistoryDataPoint is compatible with TimeSeriesPoint from downsample.ts
 * (both have { time: string; value: number }), so downsample functions
 * accept HistoryDataPoint[] without conversion.
 *
 * SeriesData is the common shape used by LineChart.vue, BmsCellVoltageHistoryChart,
 * HistoryChartSection, and other chart components.
 */

export interface HistoryDataPoint {
  time: string
  value: number
}

export interface SeriesData {
  name: string
  data: HistoryDataPoint[]
  unit?: string
  category?: string
  /** Extension point for cellNumber and other domain-specific fields */
  [key: string]: any
}
