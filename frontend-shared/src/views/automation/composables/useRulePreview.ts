import { computed, ref, watch, type Ref } from 'vue'
import { ApiError } from '@/api/client'
import { sensorFieldApi, type SensorFieldPreview, type SensorFieldPreviewFrame } from '@/api/sensorField'
import type { AutomationComparator, AutomationTriggerType } from '@/api/automation'
import { logger } from '@/utils/logger'

/**
 * 保存前试算（文档 §4.4）：用近 24h 真实数据回答"保存后会不会响"。
 *
 * 为什么单独成 composable（第二轮表单区拆分，2026-11-12）：
 *   · 试算的全部规则（请求序号、结论作废、一句话结论的门槛与省略条件）是**一个整体**，
 *     拆开就会丢"任何相关输入变化都必须丢弃旧结论"这条约束；
 *   · 它只读表单的 6 个字段与阈值的单位文案，不写表单 → 依赖面很窄，可以独立测。
 *
 * ⚠ 搬家纪律：函数体、文案、阈值常量与搬移前**逐字一致**（纯搬移）；
 * 本模块与保存校验（validate / onSave）**没有任何耦合**，试算永远只是提示。
 */

/**
 * 本 composable 需要宿主表单提供的**最小字段面**。
 *
 * 为什么收窄：试算只关心"扫哪台设备、哪个字段、什么比较符/阈值/持续时长"，
 * 收窄成结构类型可以避免「试算模块 → 表单模块」的类型环。
 */
export interface PreviewHostForm {
  trigger_type: AutomationTriggerType
  source_kind: 'edge_device' | 'source_group'
  trigger_edge_device_id?: number
  trigger_sensor_name: string
  trigger_comparator: AutomationComparator
  trigger_threshold: number
  trigger_duration_sec: number
}

/** 试算结论的一句话文案 + 呈现色（warning 只用于"试算不可用"，不是校验错误）。 */
export interface PreviewResult {
  text: string
  tone: 'info' | 'warning'
}

// ── 保存前试算（文档 §4.4）：用近 24h 真实数据回答"保存后会不会响" ──
//
// 为什么是按钮而不是输入即试算：后端要扫帧表（24h 窗口在生产是数十万帧量级），
// 每敲一位数字就打一次请求，等于把"填表单"变成"压测"；而且用户常常先填完整、
// 再回头改条件，自动请求只会产出一串互相覆盖、永远慢一步的结论。
//
// 为什么任何相关输入变化都必须丢弃旧结论：结论是**某一次具体输入**的答案，
// 留着它等于给用户看"别人的答案"（例如把 45°C 算出的"不会触发"挂到 30°C 的规则上），
// 比没有结论更危险。
//
// 为什么"不会触发"绝不阻止保存：试算回答的是"最近 24h 的观测里它响不响"，
// 不是"这条规则是否合法"。阈值先填、设备后接入是正常流程；把"暂时没数据"升级成
// "保存失败"，与后端对 unbound 只给 warning 的软校验口径相反，也会逼用户为了保存
// 而删掉一条本来正确的规则（见 TriggerFields 里 data-test="preview-result" 的注释）。
//
// 为什么算不出最长连续就省略而不是估一个数：试算的全部价值在于"结论有证据"；
// 没有可用采样间隔（帧太少 / 时间戳解析不了）时，任何近似值都会让用户以为
// 后端看到了一个连续的满足区间 —— 宁可不写，也不编。

/** 紧凑时长：45s / 4m10s / 1h2m（不写 "4.166m" 这种读不出来的数）。 */
function formatPreviewDuration(seconds: number): string {
  const total = Math.max(0, Math.round(seconds))
  if (total < 60) return `${total}s`
  if (total < 3600) {
    const minutes = Math.floor(total / 60)
    const rest = total % 60
    return rest > 0 ? `${minutes}m${rest}s` : `${minutes}m`
  }
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  return minutes > 0 ? `${hours}h${minutes}m` : `${hours}h`
}

/** 数值展示：最多 3 位小数并去掉尾零（后端 float64 会给出 42.800000000000004）。 */
function formatPreviewValue(value: number): string {
  return String(Number(value.toFixed(3)))
}

/**
 * 时间戳 → 毫秒；无法解析返回 null（宁可省略结论，也不拿 NaN 去算连续段）。
 *
 * Go 的 time.Time 走 RFC3339Nano（小数位最多 9 位），而部分浏览器的 Date.parse
 * 只稳定接受 3 位；先裁到毫秒再解析，否则 Safari 上所有帧都会被判成"时间戳无效"，
 * 最长连续满足会被无谓地省略。
 */
function frameTimeMs(frame: SensorFieldPreviewFrame): number | null {
  const normalized = frame.timestamp.replace(/(\.\d{3})\d+/, '$1')
  const value = Date.parse(normalized)
  return Number.isFinite(value) ? value : null
}

/**
 * 中位采样间隔（毫秒）。
 *
 * 用**全部帧**的相邻间隔取中位数，而不是只取满足帧的间隔 —— 后者在"只有两帧满足、
 * 且相隔 3 小时"时会把中位数抬高，断段阈值随之失效，空档被误读成连续满足。
 */
function medianFrameIntervalMs(ordered: SensorFieldPreviewFrame[]): number | null {
  const times = ordered.map(frameTimeMs).filter((value): value is number => value !== null)
  if (times.length < 2) return null
  const gaps: number[] = []
  for (let index = 1; index < times.length; index += 1) {
    const gap = times[index] - times[index - 1]
    if (gap > 0) gaps.push(gap)
  }
  if (gaps.length === 0) return null
  gaps.sort((a, b) => a - b)
  const middle = Math.floor(gaps.length / 2)
  return gaps.length % 2 === 1 ? gaps[middle] : (gaps[middle - 1] + gaps[middle]) / 2
}

/**
 * 最长连续满足时长（毫秒）；null = 算不出来（此时 UI 省略这一段，不估算）。
 *
 * 判据与后端 duration_would_fire 同语义：连续段的**首帧 → 末帧**时间差；
 * 相邻帧间隔超过"中位采样间隔 × 3"即断段 —— 设备停报/离线留下的空档不能被当成
 * "一直满足"，否则一次 24h 试算会把两段 1 分钟的满足拼成一条几小时的假连续。
 * 单帧不构成"连续"区间（返回 0 也会被当成结论），故一律省略。
 */
function longestSatisfiedRunMs(ordered: SensorFieldPreviewFrame[], medianMs: number | null): number | null {
  if (medianMs === null) return null
  const gapLimit = medianMs * 3
  let runStart: number | null = null
  let lastTs: number | null = null
  let best = 0
  for (const frame of ordered) {
    const ts = frameTimeMs(frame)
    if (ts === null) return null
    if (frame.ok && frame.satisfied) {
      // 上一段已经结束（不满足帧）或中间空档过大 ⇒ 从这里重新起一段
      if (runStart === null || (lastTs !== null && ts - lastTs > gapLimit)) runStart = ts
      best = Math.max(best, ts - runStart)
    } else {
      runStart = null
    }
    lastTs = ts
  }
  return best > 0 ? best : null
}

/** 最近一帧 trace 里有效值的区间（N=0 时说明"阈值从未达到"用）；取不到返回 null。 */
function previewTraceRange(trace: SensorFieldPreview['aggregation_trace']): { min: number; max: number } | null {
  const values = trace
    .filter(item => item.valid && typeof item.value === 'number' && Number.isFinite(item.value))
    .map(item => item.value)
  if (values.length === 0) return null
  return { min: Math.min(...values), max: Math.max(...values) }
}

/**
 * 把后端证据翻译成一句人话。
 *
 * 口径固定为：满足 N/M 帧、最长连续满足 X（可省略）、持续时长 Y ⇒ 会/不会触发；
 * 之后按需追加"阈值从未达到"与"数据被截断"两句。不堆原始字段：用户要的是
 * "这条规则保存后会不会响"，不是 frames / aggregation_trace 的转储。
 */
function buildPreviewText(res: SensorFieldPreview, thresholdUnit: string): string {
  if (res.status !== 'resolved') return '该字段当前解析不到，无法试算'
  const frames = res.frames
  if (res.samples_considered === 0 || frames.length === 0) {
    return '近 24h 没有该字段的数据（设备可能未上报）'
  }
  // 后端按时间升序返回；这里再排一次，避免把顺序当成契约（连续段依赖顺序）。
  const ordered = [...frames].sort((a, b) => (frameTimeMs(a) ?? 0) - (frameTimeMs(b) ?? 0))
  const total = frames.length
  const satisfied = ordered.filter(frame => frame.ok && frame.satisfied).length
  const longestMs = longestSatisfiedRunMs(ordered, medianFrameIntervalMs(ordered))
  // duration_sec=0（立即触发）时后端恒给 duration_would_fire=false —— 那是"没有
  // 持续时长要求"，不是"不会触发"；只要有一帧满足，运行时就一定会触发。
  const wouldFire = res.duration_sec > 0 ? res.duration_would_fire : satisfied > 0
  const head = longestMs === null
    ? `近 24h 试算：满足 ${satisfied}/${total} 帧`
    : `近 24h 试算：满足 ${satisfied}/${total} 帧，最长连续满足 ${formatPreviewDuration(longestMs / 1000)}`
  const verdict = wouldFire ? '会触发' : '不会触发（连续时间未达标）'
  let text = `${head}；持续时长 ${formatPreviewDuration(res.duration_sec)} ⇒ ${verdict}。`
  if (satisfied === 0) {
    const range = previewTraceRange(res.aggregation_trace)
    if (range) {
      const unit = thresholdUnit ? ` ${thresholdUnit}` : ''
      text += `阈值从未达到（该字段区间 ${formatPreviewValue(range.min)}~${formatPreviewValue(range.max)}${unit}）。`
    }
  }
  if (res.truncated) text += `数据量过大，仅分析了前 ${total} 帧（结论可能偏乐观）。`
  return text
}

/**
 * 试算状态与请求。
 *
 * @param form 宿主表单（只读 6 个字段，见 PreviewHostForm）
 * @param thresholdUnit 阈值单位文案（来自字段目录；空串 = 未知 ⇒ 结论里省略单位）
 */
export function useRulePreview(options: { form: PreviewHostForm; thresholdUnit: Ref<string> }) {
  const { form, thresholdUnit } = options

  /** 试算结论；null = 尚未试算，或已被输入变化作废。 */
  const previewResult = ref<PreviewResult | null>(null)
  const previewLoading = ref(false)

  /** 请求序号：输入一变就 +1，迟到的旧响应据此丢弃（不得把上一份输入的答案贴回来）。 */
  let previewSeq = 0

  /** 清空结论并让在途响应失效（设备/字段/比较符/阈值/持续时长任一变化时调用）。 */
  function clearPreviewResult() {
    previewSeq += 1
    previewResult.value = null
    previewLoading.value = false
  }

  /** 按钮缺什么：只有三项齐备才有可试算的输入（edge_device_id 是后端必填）。 */
  const previewMissing = computed(() => {
    const missing: string[] = []
    if (!form.trigger_edge_device_id) missing.push('边缘设备')
    if (!form.trigger_sensor_name.trim()) missing.push('字段选择器')
    if (typeof form.trigger_threshold !== 'number' || !Number.isFinite(form.trigger_threshold)) missing.push('阈值')
    return missing
  })

  const previewDisabled = computed(() => previewLoading.value || previewMissing.value.length > 0)

  /** 禁用时 title 必须说清"缺什么"（只给一个灰按钮，用户不知道该先填哪一项）。 */
  const previewHint = computed(() => {
    if (previewMissing.value.length > 0) return `请先选择/填写：${previewMissing.value.join('、')}`
    return '用最近 24 小时已上报的数据逐帧重放这条规则；只读预演，不改动规则，也不影响保存'
  })

  /**
   * 用户显式点了「用近 24h 数据试算」才发请求（输入变化走 watch 清结论，不发请求）。
   *
   * 失败不弹 ElMessage：用户正在填一条还没保存的规则，弹窗会打断输入；结论行里
   * 一句"试算暂不可用"足够说明，详情用 logger.warn 留痕给排障。
   */
  async function runPreview() {
    if (previewDisabled.value) return
    const deviceId = form.trigger_edge_device_id
    const selector = form.trigger_sensor_name.trim()
    if (!deviceId || !selector) return
    previewSeq += 1
    const requestSeq = previewSeq
    previewResult.value = null
    previewLoading.value = true
    try {
      const res = await sensorFieldApi.preview({
        edge_device_id: deviceId,
        selector,
        comparator: form.trigger_comparator,
        threshold: form.trigger_threshold,
        window_hours: 24,
        duration_sec: form.trigger_duration_sec,
      })
      if (requestSeq !== previewSeq) return
      previewResult.value = { text: buildPreviewText(res, thresholdUnit.value), tone: 'info' }
    } catch (error: unknown) {
      if (requestSeq !== previewSeq) return
      if (error instanceof ApiError && error.status === 413) {
        // 413 = 帧预算/时间窗超限：不是配置错误，换一个更小的窗口才可能成功。
        previewResult.value = { text: '时间窗数据量超限，试算不可用（请缩小范围）', tone: 'warning' }
      } else {
        previewResult.value = { text: '试算暂不可用', tone: 'warning' }
        logger.warn('自动化规则试算失败', { error: String(error), selector, deviceId })
      }
    } finally {
      if (requestSeq === previewSeq) previewLoading.value = false
    }
  }

  // 设备 / 字段 / 比较符 / 阈值 / 持续时长 / 来源与触发类型任一变化 ⇒ 旧结论立即作废。
  // 覆盖 source_kind 是为了"切到来源组再切回来"时不把来源组的旧结论带回边缘设备分支。
  watch(
    () => [
      form.trigger_type,
      form.source_kind,
      form.trigger_edge_device_id,
      form.trigger_sensor_name,
      form.trigger_comparator,
      form.trigger_threshold,
      form.trigger_duration_sec,
    ],
    () => clearPreviewResult(),
  )

  return {
    previewResult,
    previewLoading,
    previewDisabled,
    previewHint,
    runPreview,
    clearPreviewResult,
  }
}
