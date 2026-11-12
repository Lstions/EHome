/**
 * 仪表盘「概览三态 / 状态历史 / 实时刷新 / 告警摘要派生值」
 * （原 Dashboard.vue 的概览 state、告警摘要 computed、状态历史取数、WS 防抖刷新、
 *  `fetchOverview` 整块搬出，逻辑与注释一字未改；唯一的形式变化是把状态事件的
 *  内联对象类型提成具名 `NodeStatusEvent`，字段集合与可选性完全一致）。
 *
 * 边界依据（为什么整块搬）：
 *   · 这些 ref/computed 描述的是**同一个** overview 的三态语义（未加载 / 失败 / 已加载）：
 *     overviewLoaded → metricsReady → hasAlerts → 摘要标签与图标，再流向 KPI 的 metric()。
 *     任何一段留在页面、另一段搬走，都会让「接口失败不得报运行正常」这条判据出现两个版本；
 *   · 取数（fetchOverview / fetchStatusHistory）与 WS 防抖刷新（scheduleOverviewRefresh）
 *     共用同一个 1s 定时器与同一组退订句柄，必须与状态同生共死；
 *   · 输入只有两个 API 端点，输出只有状态 +「重取一次」的入口，不含渲染职责。
 *
 * 刻意留在页面的（都属页面级编排或门禁钉住）：
 *   · metric() —— 只有 3 行，且语义是「三态 → 展示占位符」的渲染契约，跟着模板走；
 *   · retryLoad / handleRefresh —— 一次动作要同时驱动概览、趋势、状态历史，顺序即契约
 *     （先概览后趋势：趋势的设备列表来自 overview.latest_data）；
 *   · 趋势图刷新（2s 防抖）—— 趋势「默认类别自愈」watch 被
 *     src/styles/__tests__/UiAuditSystemicFixes.spec.ts 以源码文本钉在 Dashboard.vue，
 *     故趋势状态整块留在页面，本 composable 只通过 onDataUpdate 回灌一次通知。
 */
import { computed, ref } from 'vue'
import { CircleCheck, CloseBold, WarningFilled } from '@element-plus/icons-vue'
import { feedback } from '@/utils/feedback'
import { dataApi, type Overview } from '@/api/data'
import client, { type ApiEnvelope } from '@/api/client'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { logger } from '@/utils/logger'

/** 节点状态变化事件（GET /api/v1/nodes/status-history 的一行）。 */
export interface NodeStatusEvent {
  id: number
  node_id: string
  node_name?: string
  new_status: string
  created_at: string
}

export interface UseDashboardOverviewOptions {
  /**
   * WS「数据更新」时的额外通知：页面用它触发趋势图 2s 防抖刷新。
   * 只通知、不等待 —— 与改前 handleDataUpdate 内同步调用 scheduleTrendRefresh() 等价。
   */
  onDataUpdate?: () => void
}

export function useDashboardOverview(options: UseDashboardOverviewOptions = {}) {
  const overview = ref<Overview>({
    nodes: { total: 0, online: 0, offline: 0 },
    edge_devices: { total: 0, online: 0, offline: 0 },
    latest_data: []
  })

  const loading = ref(true)
  // 概览接口是否已成功加载过。区分「尚未加载 / 加载失败 / 已加载」三态：
  // 失败时 KPI 与摘要必须显示未知，而不是把缺失数据渲染成 0 或"运行正常"。
  const overviewLoaded = ref(false)
  const overviewError = ref('')
  const wsStore = useWebSocketStore()

  const statusHistory = ref<NodeStatusEvent[]>([])
  // 状态历史接口自身失败（而非"确实没有记录"）的标记，用于区分两种空列表。
  const statusHistoryUnknown = ref(false)

  // 告警摘要计算属性
  const offlineCollectors = computed(() => Math.max(0, (overview.value.nodes?.total || 0) - (overview.value.nodes?.online || 0)))
  // 离线设备数：必须减去 pending（2026-10-03 审查发现）。
  //
  // 原写法是 `total - online`，把「已创建但还没采到数据」的设备也算成离线。
  // 此前设备默认 status=active，所以看不出问题；改成 pending 后，新建设备会在
  // ~60s 的 pending 窗口内被仪表盘报成「离线设备」，触发无谓的告警摘要
  // （hasAlerts 会因此为真）。pending 既不是在线也不是故障，必须从离线里排除。
  const offlineDevices = computed(() =>
    Math.max(
      0,
      (overview.value.edge_devices?.total || 0)
        - (overview.value.edge_devices?.online || 0)
        - (overview.value.edge_devices?.pending || 0),
    ),
  )
  const dataErrorCount = computed(() => {
    return (overview.value.latest_data || []).filter(d => (d.error_code ?? 0) > 0).length
  })
  // 仅在概览成功加载后，"无告警"才等价于"运行正常"。
  // 失败时三者都不得断言，否则会把接口故障伪装成健康状态。
  const metricsReady = computed(() => overviewLoaded.value && !overviewError.value)
  const hasAlerts = computed(() => metricsReady.value && (offlineCollectors.value > 0 || offlineDevices.value > 0 || dataErrorCount.value > 0))

  const summaryTagText = computed(() => {
    if (!metricsReady.value) return '状态未知'
    return hasAlerts.value ? '需关注' : '运行正常'
  })
  const summaryTagType = computed<'warning' | 'success' | 'info'>(() => {
    if (!metricsReady.value) return 'info'
    return hasAlerts.value ? 'warning' : 'success'
  })
  const summaryIcon = computed(() => {
    if (!metricsReady.value) return CloseBold
    return hasAlerts.value ? WarningFilled : CircleCheck
  })
  const summaryIconColor = computed(() => {
    if (!metricsReady.value) return 'var(--el-color-info)'
    return hasAlerts.value ? 'var(--el-color-warning)' : 'var(--el-color-success)'
  })

  /** 状态事件数量在接口失败时是未知的，不能报成 "最近 0 条"。 */
  const statusHistoryHint = computed(() => {
    if (statusHistoryUnknown.value) return '状态变化记录暂时不可用'
    return `最近 ${statusHistory.value.length} 条真实状态事件`
  })

  let unsubscribeStatus: (() => void) | null = null
  let unsubscribeData: (() => void) | null = null
  let overviewDebounceTimer: ReturnType<typeof setTimeout> | null = null

  const fetchStatusHistory = async () => {
    try {
      const response = await client.get<unknown, ApiEnvelope<NodeStatusEvent[]>>('/api/v1/nodes/status-history', { params: { limit: 20 } })
      const events = response.data ?? []
      statusHistory.value = Array.isArray(events) ? events : []
    } catch (error) {
      logger.warn('获取节点状态历史失败', { error: String(error) })
      statusHistory.value = []
      // 概览不可用时，空数组不代表"没有状态变化"，标记未知以免误导。
      if (!metricsReady.value) statusHistoryUnknown.value = true
    }
  }

  // 处理状态更新 — 防抖：多个状态变更在1秒内只触发一次fetchOverview
  let overviewDirty = false
  const scheduleOverviewRefresh = () => {
    overviewDirty = true
    if (overviewDebounceTimer) clearTimeout(overviewDebounceTimer)
    overviewDebounceTimer = setTimeout(() => {
      if (overviewDirty) {
        overviewDirty = false
        fetchOverview(true)
      }
    }, 1000)
  }

  const handleStatusUpdate = (message: WebSocketMessage) => {
    logger.debug('状态更新', { payload: message.payload })

    if (message.payload?.node_id || message.payload?.edge_device_id) {
      scheduleOverviewRefresh()
      fetchStatusHistory()
    }
  }

  // 处理数据更新 — 不直接修改 latest_data，触发防抖刷新避免表格跳动
  const handleDataUpdate = (message: WebSocketMessage) => {
    logger.debug('数据更新', { payload: message.payload })
    scheduleOverviewRefresh()
    // 有数据更新时也刷新趋势图(复用防抖) —— 趋势防抖留在页面，这里只回灌通知
    options.onDataUpdate?.()
  }

  const fetchOverview = async (silent = false) => {
    if (!silent) loading.value = true
    try {
      const data = await dataApi.getOverview()
      overview.value = data
      overviewLoaded.value = true
      overviewError.value = ''
    } catch (error: any) {
      // 失败必须留下常驻痕迹：KPI 与摘要据此显示未知态并给出重试入口。
      // 静默刷新（WebSocket 触发）失败与首次加载失败同等对待——接口一旦不可用，
      // 就不能再断言"运行正常"；保留上一份数据继续显示绿色对勾正是本缺陷本身。
      // 唯一差别是不弹 toast：后台刷新不该刷屏，常驻横幅已经足够。
      overviewLoaded.value = false
      overview.value = {
        nodes: { total: 0, online: 0, offline: 0 },
        edge_devices: { total: 0, online: 0, offline: 0 },
        latest_data: []
      }
      overviewError.value = error?.message || '网络请求失败'
      if (!silent) feedback.handleError(error, '获取概览数据失败')
    } finally {
      if (!silent) loading.value = false
    }
  }

  /**
   * 订阅 WS 状态 / 数据事件。页面 onMounted 末尾调用，时机与改前一致：
   * 先取首屏数据、再订阅，避免首帧被 WS 触发的静默刷新打断。
   */
  const subscribeWebSocket = () => {
    unsubscribeStatus = wsStore.subscribe(WS_EVENT.NODE_STATUS, handleStatusUpdate)
    unsubscribeData = wsStore.subscribe(WS_EVENT.DATA_UPDATE, handleDataUpdate)
  }

  /** 页面 onUnmounted 调用：退订 + 清掉概览 1s 防抖定时器（趋势定时器由页面自己管）。 */
  const disposeOverview = () => {
    if (unsubscribeStatus) unsubscribeStatus()
    if (unsubscribeData) unsubscribeData()
    if (overviewDebounceTimer) {
      clearTimeout(overviewDebounceTimer)
      overviewDebounceTimer = null
    }
  }

  return {
    overview,
    loading,
    overviewLoaded,
    overviewError,
    statusHistory,
    statusHistoryUnknown,
    offlineCollectors,
    offlineDevices,
    dataErrorCount,
    metricsReady,
    hasAlerts,
    summaryTagText,
    summaryTagType,
    summaryIcon,
    summaryIconColor,
    statusHistoryHint,
    fetchOverview,
    fetchStatusHistory,
    subscribeWebSocket,
    disposeOverview,
  }
}
