import { ref, type Ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { nodeApi, type Node } from '@/api/node'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { getSessionGeneration, assertSessionGeneration } from '@/utils/sessionCache'
import feedback from '@/utils/feedback'

/**
 * 页头动作：同步配置 / 测延迟 / 复制设备 ID / 重命名 / OTA 完成回调，以及弹窗开关。
 *
 * 为什么单独成 composable：这一组是**对外的写动作**（都会向节点或后端发起变更），
 * 与取数、与页签的内容无关；它们共享同一套「先记代际、await 后校验」的守卫，
 * 拆开写在页面里会让页面同时出现取数守卫与写动作守卫两套代码，容易漏掉其中一套。
 *
 * 页头 DOM 本身仍在页面文件里（门禁按页面文件检查页头结构）。
 */

interface HeaderOptions {
  node: Ref<Node | null>
  nodeSerial: Ref<string>
  nodeOffline: Ref<boolean>
  /** 节点详情代际（与 useNodeOverviewData 共用同一个普通对象）。 */
  seq: { detailSequence: number }
  /** 页面级操作代际（路由切换/卸载时自增，使在途写操作结果失效）。 */
  opGeneration: Ref<number>
  fetchDetail: () => Promise<void>
}

export function useNodeHeaderActions(options: HeaderOptions) {
  const route = useRoute()
  const wsStore = useWebSocketStore()

  // 页头操作
  const syncing = ref(false)
  const pinging = ref(false)
  const pendingPingTimeout = ref<ReturnType<typeof setTimeout> | null>(null)
  const showOTADialog = ref(false)

  // 弹窗
  const renameVisible = ref(false)
  const renameDraft = ref('')
  const renameSaving = ref(false)
  const eventsVisible = ref(false)

  let unsubscribePing: (() => void) | null = null

  async function handleSyncConfig() {
    const id = route.params.id as string
    if (!id || options.nodeOffline.value) return
    syncing.value = true
    const sequence = options.seq.detailSequence
    const sessionGeneration = getSessionGeneration()
    try {
      await nodeApi.syncConfig(id)
      assertSessionGeneration(sessionGeneration)
      if (sequence !== options.seq.detailSequence || route.params.id !== id) return
      ElMessage.success('配置同步已触发')
    } catch (err: any) {
      if (sequence !== options.seq.detailSequence || route.params.id !== id) return
      feedback.handleError(err, '配置同步失败')
    } finally {
      if (sequence === options.seq.detailSequence && route.params.id === id) syncing.value = false
    }
  }

  async function handlePing() {
    if (!options.node.value || options.nodeOffline.value) return
    const id = route.params.id as string
    const serial = options.nodeSerial.value
    const sessionGeneration = getSessionGeneration()
    const operation = options.opGeneration.value
    pinging.value = true
    try {
      await nodeApi.ping(serial)
      assertSessionGeneration(sessionGeneration)
      if (operation !== options.opGeneration.value || route.params.id !== id) return
      // 等待 ping_result WS 事件；5s 超时兜底
      pendingPingTimeout.value = setTimeout(() => {
        pinging.value = false
        ElMessage.warning('延迟测量超时，节点可能离线')
      }, 5000)
    } catch (err: any) {
      if (operation !== options.opGeneration.value || route.params.id !== id) return
      pinging.value = false
      feedback.handleError(err, '发送 Ping 失败')
    }
  }

  function copyId() {
    const id = options.nodeSerial.value
    navigator.clipboard?.writeText(id).then(() => ElMessage.success('设备 ID 已复制')).catch(() => ElMessage.info(id))
  }

  async function saveRename() {
    const v = renameDraft.value.trim()
    if (!v) { feedback.error('名称不能为空'); return }
    const id = route.params.id as string
    renameSaving.value = true
    const sessionGeneration = getSessionGeneration()
    try {
      await nodeApi.update(id, { name: v })
      assertSessionGeneration(sessionGeneration)
      if (route.params.id !== id) return
      if (options.node.value) options.node.value = { ...options.node.value, name: v }
      renameVisible.value = false
      ElMessage.success('设备名称已更新')
    } catch (err: any) {
      // 同文件上方 DMA 保存已写明对象（「DMA 配置保存失败」），此处也应写明是重命名。
      feedback.handleErrorWithContext(err, '重命名设备失败')
    } finally {
      renameSaving.value = false
    }
  }

  function handleOTASuccess() {
    ElMessage.success('OTA 升级已完成')
    showOTADialog.value = false
    void options.fetchDetail()
  }

  /**
   * 延迟结果经 ping_result 事件到达。
   * 只认当前节点的序列号：切节点后旧节点的延迟不能写进新节点的页头。
   */
  function startPingSubscription() {
    const unsubPing = wsStore.subscribe(WS_EVENT.PING_RESULT, (message: WebSocketMessage) => {
      if (message.payload?.node_id !== options.nodeSerial.value) return
      if (message.payload?.latency_ms !== undefined) {
        if (options.node.value) options.node.value = { ...options.node.value, latency_ms: message.payload.latency_ms }
        pinging.value = false
        if (pendingPingTimeout.value) { clearTimeout(pendingPingTimeout.value); pendingPingTimeout.value = null }
        ElMessage.success(`延迟: ${message.payload.latency_ms} ms`)
      }
    })
    unsubscribePing = () => { unsubPing() }
  }

  /** 路由切换：在途的测延迟/同步随旧节点作废，超时计时器必须清掉。 */
  function resetForRouteChange() {
    pinging.value = false
    syncing.value = false
    if (pendingPingTimeout.value) { clearTimeout(pendingPingTimeout.value); pendingPingTimeout.value = null }
  }

  /** 卸载：停订阅、清计时器、清选中态。 */
  function stopAll() {
    if (unsubscribePing) { unsubscribePing(); unsubscribePing = null }
    if (pendingPingTimeout.value) clearTimeout(pendingPingTimeout.value)
  }

  return {
    syncing,
    pinging,
    pendingPingTimeout,
    showOTADialog,
    renameVisible,
    renameDraft,
    renameSaving,
    eventsVisible,
    handleSyncConfig,
    handlePing,
    copyId,
    saveRename,
    handleOTASuccess,
    startPingSubscription,
    resetForRouteChange,
    stopAll,
  }
}
