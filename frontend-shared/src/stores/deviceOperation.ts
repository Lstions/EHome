import { defineStore } from 'pinia'
import { ref } from 'vue'
import { deviceOperationApi, type ConfirmationGrant, type DeviceOperation, type EffectiveAction, type ManualResolutionOutcome } from '@/api/deviceOperation'
import { edgeDeviceApi } from '@/api/edgeDevice'
import { nodeApi } from '@/api/node'
import { assertSessionGeneration, getSessionGeneration, registerSessionCacheClearer } from '@/utils/sessionCache'

const terminalStatuses = new Set(['SUCCEEDED', 'FAILED', 'UNKNOWN', 'CANCELLED'])
const resourceReportWaitMs = 2000
const statusRank: Record<DeviceOperation['status'], number> = {
  QUEUED: 1,
  DISPATCHED: 2,
  DEVICE_ACCEPTED: 3,
  VERIFYING: 4,
  SUCCEEDED: 5,
  FAILED: 5,
  UNKNOWN: 5,
  CANCELLED: 5,
}

function mayReplace(current: DeviceOperation, next: DeviceOperation): boolean {
  // A REST response can arrive after an early WebSocket acceptance/final. Do
  // not let it regress the timeline, and never let any later pending event
  // overwrite an observed terminal result.
  if (terminalStatuses.has(current.status)) {
    return current.status === 'UNKNOWN' && next.status === 'UNKNOWN' && !current.manual_resolution && !!next.manual_resolution
  }
  return statusRank[next.status] >= statusRank[current.status]
}

/**
 * 单次目录刷新后的「新鲜度证据」。
 *
 * 为什么需要它（P0-b，2026-09-21 生产缺陷）：
 * capability_stale 的用户可见后果是「暂不可用操作（9）」+ 一句
 * 「ChannelCmdV2 capability is unavailable or stale」，用户据此**无法行动**：
 * 既不知道这是「等一下就好」还是「坏了」，也没有任何可点的自愈入口。
 * 修复后的语义是两段式：
 *   1. 进入页面（或设备切换）时自动触发一次自愈（现有行为，保留）；
 *   2. 自愈之后**仍然**不可用 ⇒ 说明不是「等待窗口」问题，而是节点没回应，
 *      此时必须给出可行动文案 + 用户可主动点击的「重试」入口。
 * 点击这条路径是本修复的关键：只在页面加载时自愈的话，一个开着页面不动的
 * 用户永远等不到第二次机会。
 */
export interface ResourceRefreshAttempt {
  /** 本轮是否真的下发过 QueryResources（节点存在、会话未变更时为 true）。 */
  attempted: boolean
  /** 目录是否在刷新后变为可用（读操作恢复）。 */
  recovered: boolean
  /** 失败时的原因文本（用于用户可行动文案里说明「为什么没成功」）。 */
  error: string
  /** 该设备对应的节点 ID；缺失时无法自愈，文案必须说明这一点。 */
  nodeId: string
}

export const useDeviceOperationStore = defineStore('deviceOperation', () => {
  const catalogs = ref(new Map<number, EffectiveAction[]>())
  const histories = ref(new Map<number, DeviceOperation[]>())
  const lastRefresh = ref(new Map<number, ResourceRefreshAttempt>())
  const refreshing = ref(new Map<number, boolean>())

  /**
   * refreshResourceSnapshot 下发一次 QueryResources 并等待上报落地。
   *
   * 返回的 ResourceRefreshAttempt 必须能区分三种结果：
   *   - attempted=false：根本没发出请求（节点 ID 缺失 / 会话变更），
   *     用户需要知道「等」是没用的；
   *   - attempted=true, error!=''：请求发出但失败（离线 / 超时），
   *     用户需要知道节点没回应；
   *   - attempted=true, error=''：请求成功，等待资源上报后再取目录。
   *
   * 为什么把「是否尝试过」也返回：调用方不能把「没尝试」当成「尝试失败」，
   * 也不能在没尝试的情况下把目录当成已刷新（那正是静默失败）。
   */
  async function refreshResourceSnapshot(id: number, session: number): Promise<{ catalog: EffectiveAction[]; history: DeviceOperation[]; attempt: ResourceRefreshAttempt }> {
    const attempt: ResourceRefreshAttempt = { attempted: false, recovered: false, error: '', nodeId: '' }
    let [catalog, history] = await Promise.all([deviceOperationApi.actions(id), deviceOperationApi.list(id)])
    if (!catalog.some(item => item.reason_code === 'capability_stale')) {
      // 目录已经新鲜（例如别的标签页刚刷新过）：不要无谓地打扰节点。
      return { catalog, history, attempt }
    }
    try {
      const edge = await edgeDeviceApi.getDetail(id)
      assertSessionGeneration(session)
      attempt.nodeId = edge.node_id === undefined || edge.node_id === null ? '' : String(edge.node_id)
      if (!attempt.nodeId) {
        attempt.error = '该边缘设备未关联节点'
        return { catalog, history, attempt }
      }
      attempt.attempted = true
      await nodeApi.queryResources(attempt.nodeId)
      await new Promise(resolve => setTimeout(resolve, resourceReportWaitMs))
      assertSessionGeneration(session)
      ;[catalog, history] = await Promise.all([deviceOperationApi.actions(id), deviceOperationApi.list(id)])
      attempt.recovered = catalog.some(item => item.available)
    } catch (error) {
      attempt.attempted = true
      attempt.error = error instanceof Error && error.message ? error.message : '资源上报请求失败'
      // The original unavailable catalog remains authoritative when the
      // refresh request cannot be delivered or the session changed.
    }
    return { catalog, history, attempt }
  }

  async function refresh(id: number) {
    const session = getSessionGeneration()
    const { catalog, history, attempt } = await refreshResourceSnapshot(id, session)
    if (session !== getSessionGeneration()) return
    lastRefresh.value.set(id, attempt)
    catalogs.value.set(id, catalog)
    // REST is authoritative, but must merge rather than replace to retain an
    // ACK/final event that raced ahead of this refresh response.
    for (const operation of history) apply(operation)
  }

  /**
   * retryCapabilityRefresh 是**用户点击时**触发的自愈入口（P0-b 的关键）。
   *
   * 与 refresh 的区别：refresh 是「进入页面/切换设备」的自动尝试，
   * retryCapabilityRefresh 是用户在看到可行动指引之后主动点的重试。
   * 两者共用同一条实现，避免出现「自动路径修好了、手动路径还是旧的」这类
   * 双实现漂移。
   *
   * 进行中的请求不会并发下发：同一设备已有刷新在飞时直接返回，
   * 防止用户连点把节点刷屏。
   */
  async function retryCapabilityRefresh(id: number) {
    if (refreshing.value.get(id)) return
    refreshing.value.set(id, true)
    try {
      await refresh(id)
    } finally {
      refreshing.value.set(id, false)
    }
  }

  async function create(id: number, actionId: string, params: Record<string, unknown> = {}, confirmationToken = '', reason = '', idempotencyKey?: string) {
    const session = getSessionGeneration()
    const execution = idempotencyKey
      ? await deviceOperationApi.create(id, actionId, params, confirmationToken, reason, idempotencyKey)
      : await deviceOperationApi.create(id, actionId, params, confirmationToken, reason)
    assertSessionGeneration(session)
    apply(execution)
    return execution
  }
  async function confirm(id: number, actionId: string, params: Record<string, unknown>, reason: string): Promise<ConfirmationGrant> {
    const session = getSessionGeneration()
    const grant = await deviceOperationApi.confirm(id, actionId, params, reason)
    assertSessionGeneration(session)
    return grant
  }
  async function resolve(commandId: string, outcome: ManualResolutionOutcome, reason: string) {
    const session = getSessionGeneration()
    const execution = await deviceOperationApi.resolve(commandId, outcome, reason)
    assertSessionGeneration(session)
    apply(execution)
    return execution
  }
  function apply(operation: DeviceOperation) {
    const history = histories.value.get(operation.edge_device_id) ?? []
    const index = history.findIndex(item => item.command_id === operation.command_id)
    if (index >= 0) {
      if (!mayReplace(history[index], operation)) return
      history.splice(index, 1, operation)
    } else {
      history.unshift(operation)
    }
    histories.value.set(operation.edge_device_id, [...history].slice(0, 100))
  }
  function clear() { catalogs.value.clear(); histories.value.clear(); lastRefresh.value.clear(); refreshing.value.clear() }
  return { catalogs, histories, lastRefresh, refreshing, refresh, retryCapabilityRefresh, create, confirm, resolve, apply, clear }
})
registerSessionCacheClearer(() => useDeviceOperationStore().clear())
