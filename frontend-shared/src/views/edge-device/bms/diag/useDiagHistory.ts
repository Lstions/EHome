/**
 * 诊断图表卡的共用取数层 —— 生产实现（diag/ 目录，不是 demo）。
 *
 * 为什么需要它（而不是每张卡直接调 useHistoryData）：
 *  ① **并发上限**：BMS 页会同时挂多张诊断卡，若每张各自 fetch，一次性会打出
 *     4~6 个 historical-batch 请求。移动端弱网下这会让首屏竞争、互相拖慢。
 *     并发闸门已抽到共享模块 @/composables/useHistoryQueue（契约 §2.2），
 *     与 BmsChartsPanel 的两张主图共用同一个页面级 ≤3 上限。
 *  ② **失败隔离**：任一卡取数失败只影响它自己 —— 本模块的 fetch 永不 reject，
 *     统一返回 { series, error }，调用方据此渲染本卡空态/失败态。
 *  ③ **单点口径**：max_points 固定 500（plan §4 要求），避免各卡各写一个数字。
 *
 * 复用的是项目既有 useHistoryData（它内部已是 batch → per-category 降级），
 * 不新造 axios、不自己拼 token。
 */
import { ref } from 'vue'
import { useHistoryData, type HistorySeries } from '@/composables/useHistoryData'
import { withHistorySlot } from '@/composables/useHistoryQueue'

/**
 * 历史请求并发闸门已移到共享模块（契约 §2.2），这里继续 re-export 保持既有 import 路径可用。
 * BmsChartsPanel 与 4 张诊断卡共用同一个**页面级**上限（MAX_CONCURRENT_HISTORY = 3）。
 */
export { withHistorySlot }
/** 单次请求的最大降采样点数（plan §4：≤500） */
export const MAX_POINTS = 500

export interface DiagHistoryResult {
  series: HistorySeries[]
  /** 空串表示成功；非空为可展示的失败原因（组件内联渲染，不弹全局 toast） */
  error: string
}

export interface UseDiagHistoryOptions {
  nameMap?: Record<string, string>
  unitMap?: Record<string, string>
}

export function useDiagHistory(options: UseDiagHistoryOptions = {}) {
  const { nameMap = {}, unitMap = {} } = options

  // showError:false —— 失败由本卡内联空态表达，不弹全局 ElMessage：
  // 一张卡的取数失败不应该打断用户在别的区块的操作
  const history = useHistoryData({
    nameMap,
    unitMap,
    maxPoints: MAX_POINTS,
    showError: false,
    errorPrefix: '获取历史数据失败',
    // 诊断卡窗口更长（protection 默认 30 天）：10s 会把正常慢查询误判为失败，
    // 30s 与修复后目标（单页 < 3s、单请求远低于 10s）之间留足余量。
    timeoutMs: 30000,
    // 让失败真正上抛到 fetchRange 的 {error}，卡片才能渲染「加载失败 + 重试」；
    // 修复前 useHistoryData 吞掉全部错误 ⇒ error 恒为空 ⇒ 静默变「暂无数据」（根因 R4）。
    throwOnError: true,
  })

  /** 是否已完成过至少一次请求（区分"首屏加载中"与"确实为空"） */
  const loaded = ref(false)

  /**
   * 拉取指定时间窗的历史数据。**永不 reject**。
   *
   * useHistoryData.fetch 内部已 try/catch 并返回数组，正常不会抛；
   * 这里再包一层是最后一道兜底，保证调用方的空态逻辑有统一入口
   * （fail-open：只让本卡显示失败，不影响页面其余区块）。
   */
  async function fetchRange(
    deviceId: number | string,
    categories: string[],
    startTime: Date,
    endTime: Date,
  ): Promise<DiagHistoryResult> {
    loaded.value = false
    try {
      const series = await withHistorySlot(() =>
        history.fetch(deviceId, categories, startTime, endTime),
      )
      return { series, error: '' }
    } catch (e: unknown) {
      return { series: [], error: extractMessage(e) }
    } finally {
      loaded.value = true
    }
  }

  return {
    /** 最近一次成功返回的序列（useHistoryData 维护） */
    series: history.series,
    loading: history.loading,
    loaded,
    fetchRange,
  }
}

function extractMessage(e: unknown): string {
  if (e instanceof Error && e.message) return e.message
  if (typeof e === 'string' && e) return e
  return '历史数据加载失败'
}

/**
 * 「排除占位/坏值」的取点工具：把 HistorySeries 转成 bitTransitions 需要的点位数组。
 * 过滤 NaN / null，避免把脏数据画进图或参与跃变统计。
 */
export function pointsOf(
  series: HistorySeries[],
  category: string,
): Array<{ time: string; value: number }> {
  const hit = series.find((s) => s.category === category)
  if (!hit) return []
  return hit.data
    .filter((p) => p && typeof p.value === 'number' && Number.isFinite(p.value))
    .map((p) => ({ time: String(p.time), value: p.value }))
}

/** 取某分类的单位（用于"数字必须带单位"）；没有该分类时回退 fallback */
export function unitOf(series: HistorySeries[], category: string, fallback = ''): string {
  const hit = series.find((s) => s.category === category)
  return hit?.unit || fallback
}

/** 简单可读的时间刻度（今天只显示 HH:mm，跨天显示 MM/DD HH:mm），用于条形图的区间标签 */
export function shortTimeLabel(time: string | number): string {
  const d = new Date(typeof time === 'number' ? time : String(time))
  if (!Number.isFinite(d.getTime())) return ''
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  return `${hh}:${mm}`
}
