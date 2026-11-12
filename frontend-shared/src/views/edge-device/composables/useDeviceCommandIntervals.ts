/**
 * EDGE-WIZ-004/005：创建向导的**逐指令轮询间隔**状态。
 *
 * 由 CreateWizardCommandIntervals 子组件（仅创建模式）加载驱动 schedulable 指令并维护
 * 每条间隔；提交时经 freezeCommandIntervals 冻结快照参与 payload。加载失败/进行中
 * 一律给 null（不携带任何 intervals，更不会带旧驱动的数据）。
 *
 * 模板在子组件里，所以 ref 用 function-ref（setCommandIntervalsRef）回写。
 */
import { computed, ref } from 'vue'
import { feedback } from '@/utils/feedback'

export function useDeviceCommandIntervals() {
  // ---- EDGE-WIZ-004/005: 创建向导逐指令轮询间隔 ----
  // 由 CreateWizardCommandIntervals 子组件 (仅创建模式) 加载驱动 schedulable
  // 指令并维护每条的间隔; 提交时经 freezeCommandIntervals 冻结到快照参与 payload。
  // commandIntervalsError: 驱动指令列表加载失败时的错误信息 (用于拦截静默提交)。
  const commandIntervalsRef = ref()
  const commandIntervalsReady = ref(false)
  const commandIntervalsSnapshot = ref<Record<string, number> | null>(null)
  const commandIntervalsError = ref('')

  // 当前型号驱动是否声明了 schedulable 轮询指令 — 有则隐藏全局 interval_ms
  // (避免误导), 无则保留原有单个采集间隔。
  const hasSchedulableCommands = computed(() => {
    const el = commandIntervalsRef.value as any
    if (!el || typeof el.schedulableCommands === 'undefined') return false
    return (el?.schedulableCommands || []).length > 0
  })

  const prepareCommandIntervals = () => {
    commandIntervalsReady.value = false
    commandIntervalsSnapshot.value = null
  }

  const handleDriverCommandsLoadError = (message: string) => {
    commandIntervalsError.value = message
    commandIntervalsReady.value = false
    commandIntervalsSnapshot.value = null
    feedback.error(message)
  }

  // 提交时一致快照。加载失败/进行中 → null (不携带任何 intervals, 更不会带
  // 旧驱动的数据); 就绪 → 全部 schedulable 指令的当前间隔。
  const freezeCommandIntervals = (): Record<string, number> | null => {
    const el = commandIntervalsRef.value as any
    if (!el || typeof el.getIntervals !== 'function') return commandIntervalsSnapshot.value
    const intervals = el.getIntervals()
    if (intervals === null) return commandIntervalsSnapshot.value
    commandIntervalsSnapshot.value = intervals
    commandIntervalsReady.value = true
    return intervals
  }

  const setCommandIntervalsRef = (el: unknown) => { commandIntervalsRef.value = el }

  return {
    commandIntervalsRef, commandIntervalsReady, commandIntervalsSnapshot, commandIntervalsError,
    hasSchedulableCommands, prepareCommandIntervals, handleDriverCommandsLoadError, freezeCommandIntervals,
    setCommandIntervalsRef,
  }
}
