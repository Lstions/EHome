import { ref } from 'vue'

/**
 * Time range selection composable.
 *
 * Extracted from BmsCellVoltageHistoryChart.vue and HistoryChartSection.vue
 * where the same timeRange/customTimeRange/getTimeRange logic was duplicated.
 *
 * @param onChange - Callback fired when a preset (non-custom) range is selected.
 *                   For 'custom', the parent component handles the date-picker
 *                   change event separately (typically its own fetch function).
 */
/**
 * 把「时间档位 + 自定义区间」换算成 [start, end]。
 *
 * 抽成独立函数（2026-11-12）：移动端时间窗上移到**页面级**后，卡片不再持有 ref，
 * 只接收 props（timeRange / customRange），需要同一份换算逻辑；重复实现会漂移。
 */
export function resolveTimeRange(
  timeRange: string,
  customTimeRange: [string, string] | null,
): [Date, Date] {
  const now = new Date()
  let startTime: Date
  switch (timeRange) {
    case '1h':
      startTime = new Date(now.getTime() - 3600000)
      break
    case '7d':
      startTime = new Date(now.getTime() - 7 * 86400000)
      break
    case 'custom':
      if (customTimeRange) {
        return [new Date(customTimeRange[0]), new Date(customTimeRange[1])]
      }
      startTime = new Date(now.getTime() - 86400000)
      break
    case '24h':
    default:
      startTime = new Date(now.getTime() - 86400000)
  }
  return [startTime, now]
}

export function useTimeRange(onChange?: () => void) {
  const timeRange = ref('24h')
  const customTimeRange = ref<[string, string] | null>(null)

  /**
   * Compute the [startTime, endTime] pair for the current selection.
   *
   * - '1h':     now - 1 hour
   * - '24h':    now - 24 hours
   * - '7d':     now - 7 days
   * - 'custom': uses customTimeRange values; falls back to 24h if unset
   */
  const getTimeRange = (): [Date, Date] => resolveTimeRange(timeRange.value, customTimeRange.value)

  /**
   * Handler for radio-group @change.
   * Only fires onChange for preset ranges — 'custom' waits for the
   * date-picker to emit its own change.
   */
  const handleTimeRangeChange = () => {
    if (timeRange.value !== 'custom' && onChange) {
      onChange()
    }
  }

  return {
    timeRange,
    customTimeRange,
    getTimeRange,
    handleTimeRangeChange,
  }
}
