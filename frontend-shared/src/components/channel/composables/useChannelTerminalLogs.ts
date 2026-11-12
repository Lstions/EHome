import { computed, nextTick, ref, type Ref } from 'vue'
import { getByteCount } from '../terminalCodec'

/**
 * 终端日志（TX/RX 双面板）状态组合式：追加 / 上限截断 / 暂停 / 展开 / 导出。
 *
 * 边界依据（2026-11-12 纯搬移拆分，行为零变化）：
 *   · 搬入的是**只依赖日志状态**的工作：追加后的自动滚底（滚底需要模板 ref，故由调用方
 *     注入）、1000 条上限并保留末 800 条、暂停门闩、展开集合、导出文本。
 *   · 没搬：
 *       - DOM 结构（.panel-log / .panel-entry 两个面板仍由 ChannelTerminal.vue 渲染）；
 *       - scoped CSS（.terminal-actions 等由原组件持有，ChannelTerminal.spec.ts 直接编译
 *         原文件样式做级联断言）；
 *       - displayMode（HEX/ASCII 展示档）留在原组件：它是展示偏好，且日志文本格式化
 *         是纯函数（terminalCodec.ts），本组合式不必感知。
 *   · exportLog 需要「当前所选通道 id」，由调用方以参数传入，避免本组合式反向依赖
 *     会话状态机（日志与会话互不影响，便于独立推理）。
 */
export interface LogEntry {
  type: 'send' | 'recv' | 'error' | 'info'
  direction: 'TX' | 'RX'
  time: string
  data: string
  source: 'scheduled' | 'interactive' | 'manual'
  channelId?: number
  errorCode?: number
}

export interface TerminalLogContainers {
  txLogContainer: Ref<HTMLDivElement | undefined>
  rxLogContainer: Ref<HTMLDivElement | undefined>
}

export function useChannelTerminalLogs(containers: TerminalLogContainers) {
  const { txLogContainer, rxLogContainer } = containers

  const logEntries = ref<LogEntry[]>([])
  const isPaused = ref(false)
  const expandedEntries = ref(new Set<string>())

  const txEntries = computed(() => logEntries.value.filter(e => e.direction === 'TX'))
  const rxEntries = computed(() => logEntries.value.filter(e => e.direction === 'RX'))

  let entryCounter = 0
  const addLog = (entry: LogEntry) => {
    if (isPaused.value) return
    entryCounter++
    logEntries.value.push(entry)
    if (logEntries.value.length > 1000) logEntries.value = logEntries.value.slice(-800)
    nextTick(() => {
      if (entry.direction === 'TX' && txLogContainer.value) txLogContainer.value.scrollTop = txLogContainer.value.scrollHeight
      if (entry.direction === 'RX' && rxLogContainer.value) rxLogContainer.value.scrollTop = rxLogContainer.value.scrollHeight
    })
  }

  const clearLog = () => {
    logEntries.value = []
    expandedEntries.value.clear()
  }

  const togglePause = () => { isPaused.value = !isPaused.value }

  const isExpanded = (panel: string, index: number): boolean => expandedEntries.value.has(`${panel}-${index}`)

  const toggleExpand = (panel: string, index: number) => {
    const key = `${panel}-${index}`
    if (expandedEntries.value.has(key)) expandedEntries.value.delete(key)
    else expandedEntries.value.add(key)
    expandedEntries.value = new Set(expandedEntries.value)
  }

  const exportLog = (channelId?: number) => {
    const lines = logEntries.value.map(e => {
      const dir = e.direction === 'TX' ? 'TX▼' : 'RX▲'
      const src = e.source === 'interactive' ? '[交互]' : e.source === 'manual' ? '[手动]' : ''
      const err = e.errorCode ? `[ERR:${e.errorCode}]` : ''
      const hex = e.data.replace(/(.{2})/g, '$1 ').trim()
      return `[${e.time}] ${dir} [${getByteCount(e.data)}B] ${hex} ${src}${err}`
    })
    const content = lines.join('\n')
    const blob = new Blob([content], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    const ts = new Date().toISOString().replace(/[:.]/g, '-').substring(0, 19)
    a.download = `terminal_${channelId || 'all'}_${ts}.txt`
    a.click()
    URL.revokeObjectURL(url)
  }

  return { logEntries, txEntries, rxEntries, isPaused, addLog, clearLog, togglePause, isExpanded, toggleExpand, exportLog }
}
