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
  const getTimeRange = (): [Date, Date] => {
    const now = new Date()
    let startTime: Date
    switch (timeRange.value) {
      case '1h':
        startTime = new Date(now.getTime() - 3600000)
        break
      case '24h':
        startTime = new Date(now.getTime() - 86400000)
        break
      case '7d':
        startTime = new Date(now.getTime() - 7 * 86400000)
        break
      case 'custom':
        if (customTimeRange.value) {
          return [new Date(customTimeRange.value[0]), new Date(customTimeRange.value[1])]
        }
        startTime = new Date(now.getTime() - 86400000)
        break
      default:
        startTime = new Date(now.getTime() - 86400000)
    }
    return [startTime, now]
  }

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
