import { onMounted, onUnmounted } from 'vue'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import { deviceOperationApi, type DeviceOperation } from '@/api/deviceOperation'
import { WS_EVENT } from '@/events/events'

/**
 * 受控操作回执的**唯一入口**（WS 主通道 + 有界 REST 兜底）。
 *
 * 2026-11-17 真机缺陷（用户报障）：
 *   BMS 详情页把 MOS 开关置为「导通/关断」后，卡片上的「已发送…」**永不复位**，
 *   必须手点页头「刷新数据」才消失；开关本身随后自己跳到正确位置（说明设备侧已生效）。
 *
 * 根因：状态推进只有两条来路 ——
 *   ① WS `device_operation_update`（后端在 queued/dispatched/.../succeeded 每次变化都广播）；
 *   ② REST `GET /edge-devices/:id/operations`（页面只在挂载/刷新时拉一次）。
 * 全仓只有 DeviceControlPanel 订阅了 ①，BMS 页两个 composable（useBmsOperations /
 * useBmsMosControl）都只读 `store.histories` —— 而 store 里那条记录**只有 POST 的返回值**
 * （QUEUED）。于是：终端状态永远到不了页面 ⇒ 文案停在「已发送」、权威回读（verified_result）
 * 也永远不生效（开关只能等下一次周期轮询帧自己跟上，于是出现"先跳回旧值再跳回新值"的抖动）。
 *
 * 为什么做成 composable 而不是在两处各写一遍 subscribe：
 *   同一个事件名写两遍就会出现"一处改了另一处没改"的漂移（本仓已有先例：WS 事件名裸字符串）。
 *
 * 为什么还要 REST 兜底：WS 断线/事件丢失时不能把用户钉在「已发送」上。
 *   兜底是**有界**的（1.5s × 20 = 30s），到点即止，不留下长期定时器。
 */

/** 兜底轮询间隔：WS 是主通道，这里只在事件缺失/断线时保底 */
export const OPERATION_FOLLOW_INTERVAL_MS = 1500
/** 兜底轮询上限（20 次 ≈ 30s）：写 + 回读实测 1~2s，30s 足够；到点即止 */
export const OPERATION_FOLLOW_MAX_ATTEMPTS = 20

const TERMINAL_STATUS = new Set<DeviceOperation['status']>(['SUCCEEDED', 'FAILED', 'UNKNOWN', 'CANCELLED'])

/**
 * 把一条**任意来源**的回执规整成 DeviceOperation。
 *
 * 不信任入参：WS payload 是 any（后端 struct 变更时前端不能静默写坏 store），
 * 缺 command_id / edge_device_id / status 的一律丢弃并返回 false（调用方可据此断言）。
 */
export function toDeviceOperation(raw: unknown): DeviceOperation | null {
  if (!raw || typeof raw !== 'object') return null
  // 兼容两种外壳：直接是 execution，或 { execution: {...} }（POST 响应同款）
  const candidate = (raw as { execution?: unknown }).execution ?? raw
  if (!candidate || typeof candidate !== 'object') return null
  const op = candidate as Partial<DeviceOperation>
  const deviceId = Number(op.edge_device_id)
  if (typeof op.command_id !== 'string' || op.command_id === '') return null
  if (!Number.isFinite(deviceId) || deviceId <= 0) return null
  if (typeof op.status !== 'string' || !op.status) return null
  // edge_device_id 归一成 number：store.histories 以 number 为键，字符串会静默落到另一个桶里
  return { ...(op as DeviceOperation), edge_device_id: deviceId }
}

export interface DeviceOperationFeed {
  /** 把一条回执写入 store（WS handler / 轮询 / 测试都用它，保证只有一条解析口径） */
  ingest: (raw: unknown) => boolean
  /** 开始跟踪某条命令：直到终态或超时为止，用 REST 兜底推进状态 */
  startFollow: (commandId: string) => void
  /** 停止跟踪（设备切换 / 页面卸载 / 已到终态） */
  stopFollow: () => void
}

export function useDeviceOperationFeed(): DeviceOperationFeed {
  const wsStore = useWebSocketStore()
  const opStore = useDeviceOperationStore()

  let unsubscribe: (() => void) | null = null
  let followTimer: ReturnType<typeof setTimeout> | null = null
  /** 正在跟踪的 command_id；'' = 未跟踪。stopFollow 会清空它，await 之后用它判「这轮是否已被取代」 */
  let following = ''
  let attempt = 0

  function clearTimer() {
    if (followTimer !== null) {
      clearTimeout(followTimer)
      followTimer = null
    }
  }

  function stopFollow() {
    clearTimer()
    following = ''
    attempt = 0
  }

  function ingest(raw: unknown): boolean {
    const operation = toDeviceOperation(raw)
    if (!operation) return false
    opStore.apply(operation)
    if (operation.command_id === following && TERMINAL_STATUS.has(operation.status)) stopFollow()
    return true
  }

  function schedule() {
    clearTimer()
    followTimer = setTimeout(() => {
      followTimer = null
      void step()
    }, OPERATION_FOLLOW_INTERVAL_MS)
  }

  /**
   * 一次兜底轮询。
   *
   * 用 `GET /device-operations/:id`（按 command_id 精确取）而不是列表接口：
   * 列表是"该设备最近 N 条"，并发操作时会拿错行，而这里只关心自己这一条。
   */
  async function step() {
    const commandId = following
    if (!commandId) return
    attempt++
    try {
      const operation = await deviceOperationApi.get(commandId)
      // 期间可能已经 stopFollow（WS 推到终态 / 设备切换）—— 不能把旧结果写回去
      if (following !== commandId) return
      ingest(operation)
    } catch {
      // 404（记录已过保留期）/ 网络失败都不在这里报错：页面已有「已发送」在途态，
      // 兜底只负责"尽力把终态拿回来"，拿不到由看门狗（useBmsMosControl）兜住。
    }
    if (following !== commandId) return
    if (attempt >= OPERATION_FOLLOW_MAX_ATTEMPTS) {
      stopFollow()
      return
    }
    schedule()
  }

  function startFollow(commandId: string) {
    stopFollow()
    if (!commandId) return
    following = commandId
    schedule()
  }

  function onMessage(message: WebSocketMessage) {
    // 后端广播的是 Event{type,payload}；payload 直接就是 execution。
    // 同时兼容扁平格式（message.data / message 本身），与既有订阅方保持一致。
    ingest(message.payload ?? (message as unknown as { data?: unknown }).data)
  }

  onMounted(() => {
    unsubscribe = wsStore.subscribe(WS_EVENT.DEVICE_OPERATION_UPDATE, onMessage)
  })

  onUnmounted(() => {
    unsubscribe?.()
    unsubscribe = null
    stopFollow()
  })

  return { ingest, startFollow, stopFollow }
}
