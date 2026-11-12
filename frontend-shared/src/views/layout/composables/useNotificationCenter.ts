/**
 * 顶栏通知中心的数据面：REST 拉取 + WS 实时推送落列表 + 已读同步。
 *
 * 为什么从 MainLayout.vue 搬出来（纯搬移，零行为变化）：
 *   这一段是「通知实体 → 列表/未读数」的全部状态机，与布局外壳（侧栏、顶栏、
 *   抽屉、面包屑）没有任何耦合 —— 它只消费 notification API、WS store 与 router。
 *   搬成 composable 后，页面只剩三个调用点：模板绑定 ＋ onMounted/onUnmounted
 *   各一行生命周期；订阅时机、去重、上限、visibilitychange 刷新全部原样保留。
 *
 * 边界依据：**模板**里的通知面板被拆成 NotificationPanel.vue（展示层），
 * 铃铛按钮与 el-popover 的引用槽留在页面 —— 那段 DOM 由页面 scoped 样式
 * `.main-header .el-button.is-circle`（TouchTargets.spec 锁 44px 触控盒）
 * 与 `.notification-badge :deep(.el-badge__content)` 直接覆盖，搬进子组件会
 * 丢掉父组件的 data-v 作用域。
 */
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  getNotifications,
  getUnreadCount,
  markAsRead,
  markAllAsRead,
  type Notification as ApiNotification,
} from '@/api/notification'
import { useWebSocketStore } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { ElNotification } from 'element-plus'
import feedback from '@/utils/feedback'
import { logger } from '@/utils/logger'

/** 通知列表保留上限：与 REST 默认 limit 一致（api/notification.ts getNotifications(20)），
 * 避免实时推送插入导致列表无限增长。 */
const NOTIFICATION_LIMIT = 20

export function useNotificationCenter() {
  const router = useRouter()
  const wsStore = useWebSocketStore()

  // 通知 - 从 API 获取
  const notifications = ref<ApiNotification[]>([])
  const notificationCount = ref(0)

  const fetchNotifications = async () => {
    try {
      const [notifs, count] = await Promise.all([
        getNotifications(NOTIFICATION_LIMIT),
        getUnreadCount()
      ])
      notifications.value = notifs.slice(0, NOTIFICATION_LIMIT)
      notificationCount.value = count
    } catch (error) {
      // API 失败时显示空列表，不报错
      notifications.value = []
      notificationCount.value = 0
    }
  }

  // insertNotification 实时推送落列表：按 id 去重（重复推送 / 与 REST 拉取重叠）
  // + 头部插入 + 未读数 +1（read=false）+ 上限截断。
  // 返回 true 表示确实新增了一条（用于测试断言与调用方判断）。
  const insertNotification = (raw: Partial<ApiNotification> | undefined | null): boolean => {
    if (!raw || raw.id == null) return false
    const existing = notifications.value.find(item => item.id === raw.id)
    if (existing) {
      // 同 id 已存在：只做就地更新（例如推送与 REST 竞态时字段更全），不重复计数。
      Object.assign(existing, raw)
      return false
    }
    // 后端广播载荷与 models.Notification 同形；这里兜底默认值，避免缺字段导致渲染异常。
    const item: ApiNotification = {
      id: raw.id,
      type: raw.type ?? 'info',
      title: raw.title ?? '',
      description: raw.description ?? '',
      source: raw.source ?? '',
      source_id: raw.source_id,
      read: raw.read ?? false,
      created_at: raw.created_at ?? '',
    }
    notifications.value = [item, ...notifications.value].slice(0, NOTIFICATION_LIMIT)
    if (!item.read) notificationCount.value += 1
    return true
  }

  // 初始化获取通知
  fetchNotifications()

  // ── 实时通知：订阅 WS 推送（负债 D-3）──
  // 后端 events.Notification 的载荷是通知实体本身（与 REST 列表项同形），收到即插入。
  // 订阅 automation 的 3 个自定义事件：它们同样携带通知实体（顶层）与自动化领域详情
  // （detail），前端按语义给出差异化用户反馈。
  const handleNotificationPush = (message: { payload?: any; data?: any }) => {
    // 兼容两种 WS 消息形状：{type, payload} 与扁平 {type, ...fields}
    const body = (message?.payload ?? message?.data) as Partial<ApiNotification> | undefined
    const inserted = insertNotification(body)
    if (inserted) {
      logger.debug('[MainLayout] 收到实时通知', { id: body?.id })
    }
  }

  const handleAutomationDailyLimit = (message: { payload?: any; data?: any }) => {
    handleNotificationPush(message)
    const body = (message?.payload ?? message?.data) as any
    const ruleName = body?.detail?.rule_name ?? body?.title ?? ''
    feedback.warning(`策略「${ruleName}」已达日执行上限，今日后续触发将被抑制`)
  }

  const handleAutomationSystemActorUnavailable = (message: { payload?: any; data?: any }) => {
    handleNotificationPush(message)
    feedback.error('自动化执行已禁用：系统主体用户不可用，请检查初始化状态')
  }

  const handleAutomationPendingConfirm = (message: { payload?: any; data?: any }) => {
    handleNotificationPush(message)
    const body = (message?.payload ?? message?.data) as any
    const ruleName = body?.detail?.rule_name ?? body?.title ?? ''
    // 待确认事件 → 用户可见提示 + 跳转自动化策略页处理。
    // ElNotification 支持 onClick（ElMessage 不支持），点击提示即前往确认。
    ElNotification({
      type: 'warning',
      duration: 6000,
      title: '策略等待确认',
      message: `策略「${ruleName}」等待人工确认，点击前往处理`,
      onClick: () => { router.push('/automation') },
    })
  }

  let unsubscribeNotification: (() => void) | null = null
  let unsubscribeDailyLimit: (() => void) | null = null
  let unsubscribeSystemActor: (() => void) | null = null
  let unsubscribePendingConfirm: (() => void) | null = null
  // removeConnectionRefresh 解绑 wsStore.onConnected 注册的"重连成功后刷新"处理器。
  let removeConnectionRefresh: (() => void) | null = null

  // 页面重新可见时刷新（长时间挂后台的页面计数会过期）。
  // 刻意不用定时轮询：只在 visibilitychange → visible 这一个真实事件上拉取。
  const handleVisibilityChange = () => {
    if (document.visibilityState === 'visible') {
      void fetchNotifications()
    }
  }

  const setupRealtimeNotifications = () => {
    unsubscribeNotification = wsStore.subscribe(WS_EVENT.NOTIFICATION, handleNotificationPush)
    unsubscribeDailyLimit = wsStore.subscribe('automation_daily_limit', handleAutomationDailyLimit)
    unsubscribeSystemActor = wsStore.subscribe('automation_system_actor_unavailable', handleAutomationSystemActorUnavailable)
    unsubscribePendingConfirm = wsStore.subscribe('automation_pending_confirm', handleAutomationPendingConfirm)
    // WS 重连成功 → 重新拉取，补齐断线期间错过的推送（不依赖轮询）。
    removeConnectionRefresh = wsStore.onConnected(() => { void fetchNotifications() })
    document.addEventListener('visibilitychange', handleVisibilityChange)
  }

  const teardownRealtimeNotifications = () => {
    unsubscribeNotification?.(); unsubscribeNotification = null
    unsubscribeDailyLimit?.(); unsubscribeDailyLimit = null
    unsubscribeSystemActor?.(); unsubscribeSystemActor = null
    unsubscribePendingConfirm?.(); unsubscribePendingConfirm = null
    removeConnectionRefresh?.(); removeConnectionRefresh = null
    document.removeEventListener('visibilitychange', handleVisibilityChange)
  }

  // 通知处理
  const clearNotifications = async () => {
    try {
      await markAllAsRead()
      // 本地列表与计数同步收敛（不依赖随后的网络往返，避免"点了没反应"的窗口期）。
      notifications.value.forEach(item => { item.read = true })
      notificationCount.value = 0
      await fetchNotifications()
      feedback.success('已全部标记为已读')
    } catch (error) {
      feedback.handleError(error, '标记已读失败')
    }
  }

  const handleNotificationClick = async (item: ApiNotification) => {
    try {
      await markAsRead(item.id)
      // 已读同步：仅当此前确实未读才递减，避免重复点击把计数减成负数。
      if (!item.read) {
        item.read = true
        notificationCount.value = Math.max(0, notificationCount.value - 1)
      }
    } catch (error) {
      // 静默失败
    }
    // 通知跳转 (方案 v3.3 §六 D-2): 优先按 Notification.source 结构化路由,
    // 替代脆弱的 title 字符串匹配; 离线通知保留原 title 匹配行为。
    //
    // 2026-10-23（逻辑设备移除）：原 merge_failed / retention_expiring 两个分支
    // 都深链到已删除的 /logical-device 页面（合并任务管理 + 逻辑保留期编辑）。
    // 合并任务与逻辑身份整体下线，两个 source 类别不会再产生 ⇒ 分支删除，
    // 落回下方的离线通知规则；历史遗留通知不会跳到 404。
    if (item.title.includes('离线')) {
      router.push('/edge-device')
    }
  }

  return {
    notifications,
    notificationCount,
    fetchNotifications,
    clearNotifications,
    handleNotificationClick,
    setupRealtimeNotifications,
    teardownRealtimeNotifications,
  }
}
