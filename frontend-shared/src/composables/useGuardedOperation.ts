import { onUnmounted, watch, isRef, type Ref } from 'vue'
import { ElMessage } from 'element-plus'

export interface GuardedRow {
  busy: boolean
  feedback: string
}

export interface GuardedOperationOptions {
  /** Reactive nodeId — operation is invalidated when it changes */
  nodeId: Ref<string> | (() => string)
  /** Whether the device is offline (blocks new operations) */
  offline: Ref<boolean> | (() => boolean)
  /** Error message prefix, e.g. 'GPIO 操作失败' */
  errorPrefix: string
}

/**
 * Encapsulates the busy-guard + generation-snapshot + disposed-check + rollback
 * pattern used across GPIOResourceList, PWMResourceList, LogPanel, LogHistoryPanel.
 *
 * Usage:
 *   const { run, invalidate } = useGuardedOperation({ nodeId: () => props.nodeId, offline: () => props.offline, errorPrefix: 'GPIO 操作失败' })
 *   await run(row, '正在写入…', async () => { ... }, { rollback: () => { row.level = prev } })
 */
export function useGuardedOperation(options: GuardedOperationOptions) {
  let generation = 0
  let disposed = false

  const getNodeId = typeof options.nodeId === 'function'
    ? options.nodeId
    : () => (options.nodeId as Ref<string>).value
  const getOffline = typeof options.offline === 'function'
    ? options.offline
    : () => (options.offline as Ref<boolean>).value

  // Invalidate in-flight operations when nodeId changes
  if (isRef(options.nodeId)) {
    watch(options.nodeId, () => { generation++ })
  }

  onUnmounted(() => { disposed = true; generation++ })

  function isStale(gen: number, nodeId: string): boolean {
    return disposed || gen !== generation || getNodeId() !== nodeId
  }

  interface RunOptions {
    /** Skip the busy/offline entry guard (e.g. for debounced scheduleDuty) */
    skipEntryGuard?: boolean
    /** Called on error to roll back optimistic state */
    rollback?: () => void
    /** Called on success with the API result */
    onSuccess?: (result: unknown) => void
  }

  async function run<R>(
    row: GuardedRow,
    feedbackStart: string,
    fn: () => Promise<R>,
    opts: RunOptions = {},
  ): Promise<R | undefined> {
    if (!opts.skipEntryGuard) {
      if (row.busy || getOffline()) return undefined
    }
    const nodeId = getNodeId()
    const gen = generation
    row.busy = true
    row.feedback = feedbackStart
    try {
      const result = await fn()
      if (isStale(gen, nodeId)) return undefined
      opts.onSuccess?.(result)
      return result
    } catch (error: unknown) {
      if (isStale(gen, nodeId)) return undefined
      opts.rollback?.()
      row.feedback = '操作失败 · 重试'
      ElMessage.error(`${options.errorPrefix}: ${error instanceof Error ? error.message : '未知错误'}`)
      return undefined
    } finally {
      if (!isStale(gen, nodeId)) row.busy = false
    }
  }

  return { run, invalidate: () => { generation++ } }
}
