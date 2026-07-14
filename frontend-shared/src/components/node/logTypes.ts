export interface IncomingRealtimeLogLine {
  ts: number
  level: number
  tag: string
  msg: string
}

export interface RealtimeLogLine extends IncomingRealtimeLogLine {
  id: number
}

export interface RealtimeSearchCountState {
  epoch: number
  baselineId: number
  baselineMatchIds: number[]
  matchedAfterBaseline: number
}

export type LogLevelTagType = 'danger' | 'warning' | 'info' | undefined

const LOG_LEVEL_TEXT = ['ERROR', 'WARN', 'INFO', 'DEBUG', 'VERBOSE'] as const
const LOG_LEVEL_TAG_TYPES: readonly LogLevelTagType[] = ['danger', 'warning', undefined, 'info', 'info']

export function levelText(level: number): string {
  return LOG_LEVEL_TEXT[level] ?? 'UNKNOWN'
}

export function levelTagType(level: number): LogLevelTagType {
  return LOG_LEVEL_TAG_TYPES[level]
}

export function formatUptime(tsUs: number): string {
  const totalMs = Math.max(0, Math.floor(Number(tsUs || 0) / 1000))
  const hours = Math.floor(totalMs / 3_600_000)
  const minutes = Math.floor((totalMs % 3_600_000) / 60_000)
  const seconds = Math.floor((totalMs % 60_000) / 1000)
  const millis = totalMs % 1000
  return `${String(hours).padStart(2, '0')}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}.${String(millis).padStart(3, '0')}`
}
