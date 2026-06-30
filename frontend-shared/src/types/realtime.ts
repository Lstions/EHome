/**
 * Shared data types for realtime data components.
 *
 * Extracted from RealtimeDataList.vue so that composables and other
 * modules can import the type without relying on Vue SFC type exports
 * (which TS resolves incorrectly via `import type from '*.vue'`).
 */

export interface DataItem {
  id: string
  timestamp: string
  data: any
  rawData?: number[]
  isRealtime: boolean
}
