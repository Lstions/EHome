/**
 * 边缘设备实时状态层（1Hz）
 * ============================================================================
 * 职责边界 —— 改这个文件之前先读这一段：
 *
 *   · 本 store 是边缘设备「实时状态 + 实时读数」的**唯一聚合点**。列表、看板这类
 *     「一张卡片上有一组读数」的视图都从这里取，**不要**再各自 `wsStore.subscribe`：
 *     重复订阅会把合并/节流逻辑复制到每个页面，必然漂移。
 *   · HTTP 快照（stores/edgeDevice.ts 的 list/detail 缓存）仍然负责**成员 / 配置 /
 *     分页 / 排序**；实时层只覆盖「会变的那三个字段」：status / lastData / lastDataAt。
 *     两者在**视图层**合成（见 EdgeDeviceList 的 displayDevices），互不写回 ——
 *     否则「谁覆盖谁」会变成每个页面各猜一次的隐式契约。
 *   · **渲染速率与消息速率解耦**：WS 消息只进缓冲（pending / pendingSnapshot），
 *     1Hz flush 才写响应式状态。data_update 是按**帧**推送的（一台设备可能开了多条
 *     轮询指令 ⇒ 每秒数条），每条都写响应式状态会让 24 张卡片每秒重绘数十次。
 *
 * 三个数据来源与合并优先级（见 applyPending）：
 *   ① edge_devices_live（后端 1Hz 状态快照）—— **基底**，决定"现在有哪些设备"，
 *      缺席即删除。它的速率与设备数/指令数无关（永远 1 条/秒）。
 *   ② data_update（逐帧）—— 叠在快照**上面**（更新鲜）；也是没有快照通道时的唯一来源。
 *   ③ edge_device_status（状态迁移）—— 与快照同义，早到早生效。
 *
 * 为什么是 1Hz（产品契约，页面不要自己再定一个节拍）：
 *   卡片读数是给人看的，1Hz 已到「看起来实时」的上限；再快的唯一效果是把 CPU 花在重绘上。
 *
 * 为什么状态也在这里推导（而不是只信 edge_device_status 事件）：
 *   后端「成功采到数据 ⇒ 状态 active」是同一语义（consumers_heavy.go 的判定），
 *   但那条事件只在**状态迁移**时发。若重连窗口里正好丢掉它，卡片会卡在「离线」而数据
 *   仍在流动，直到下一次对账才自愈。收到 data_update 即视为在线，与后端同义且与事件
 *   到达顺序无关。
 */
import { defineStore } from 'pinia'
import { computed, onMounted, onUnmounted, ref, shallowRef } from 'vue'
import { useWebSocketStore, type WebSocketMessage } from './websocket'
import { WS_EVENT } from '@/events/events'
// 直接引子模块而不是 '@/api/edgeDevice' barrel：状态层只需要一个纯映射，
// 不该把 list/detail/deleted/history/commands 与 axios client 全部拉进来。
import { mapStatus } from '@/api/edgeDevice/status'
import type { DeviceStatus } from '@/api/edgeDevice/types'
import { registerSessionCacheClearer } from '@/utils/sessionCache'

/** 渲染节拍：1Hz。这是**契约**，不是性能调参项（见文件头）。 */
export const LIVE_RENDER_INTERVAL_MS = 1000

/**
 * 单设备实时条目的保留期：超过这么久没有任何新消息就在 flush 时丢弃。
 *
 * 删除一台设备后它的补丁不该永久占着内存；而丢弃是**安全**的 —— 视图层会回落到
 * HTTP 快照里的 last_data，真源永远还在。10 分钟取得足够宽容，不会打断慢速轮询设备。
 */
export const LIVE_ENTRY_TTL_MS = 10 * 60_000

/** 单设备的实时状态。字段名刻意与 EdgeDevice 对齐，便于视图层浅合成。 */
export interface DeviceLiveState {
  /** 后端状态词汇表映射后的值；从未收到过状态事件时为 null（不要伪装成 offline）。 */
  status: DeviceStatus | null
  /** 跨帧累积的最新读数（键 = 后端 sensor_name，与 HTTP 快照的 last_data 同源）。 */
  lastData: Record<string, number> | null
  /** 最近一帧的服务端采集时刻（ISO 字符串，直接来自 collected_at，不做本地时钟改写）。 */
  lastDataAt: string | null
  /** 本地收到该设备最新消息的时刻（epoch ms），仅用于 TTL 清理。 */
  updatedAt: number
}

/** WS 事件的两种载荷形状：结构化事件是 { payload }，历史/扁平事件直接铺在顶层。 */
type LivePayload = {
  edge_device_id?: unknown
  device_id?: unknown
  status?: unknown
  collected_at?: unknown
  timestamp?: unknown
  data?: unknown
  /** edge_devices_live 快照载荷：items 里每项是 {id, status, last_data_at, data}。 */
  items?: unknown
}

interface PendingPatch {
  status?: DeviceStatus
  data?: Record<string, number>
  at?: string
}

/** 从消息里取载荷：与 composables/useRealtimeData.ts 的历史约定保持一致。 */
function payloadOf(message: WebSocketMessage): LivePayload {
  const nested = (message as { payload?: unknown }).payload
  if (nested && typeof nested === 'object') return nested as LivePayload
  return message as unknown as LivePayload
}

/** 设备 ID 收敛：NaN / 0 / 负数一律丢弃 —— 否则会写进一个永远匹配不上的键。 */
function toDeviceId(value: unknown): number | null {
  const id = Number(value)
  return Number.isInteger(id) && id > 0 ? id : null
}

/**
 * 读数收敛：只保留有限数值。
 *
 * 契约上 last_data 就是 Record<string, number>（后端 map[string]float64），
 * 所以非数值字段**不属于本通道**（BMS 的字符串量 hardware_version 从来不在 last_data 里）。
 */
function toReadings(value: unknown): Record<string, number> | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const out: Record<string, number> = {}
  for (const [key, raw] of Object.entries(value as Record<string, unknown>)) {
    const num = Number(raw)
    if (Number.isFinite(num)) out[key] = num
  }
  return Object.keys(out).length > 0 ? out : null
}

/**
 * 采集时刻：优先 collected_at（ISO）；兼容扁平事件里的 timestamp（epoch 秒）。
 * 两者都没有时返回 undefined，**不要**用本地时间兜底 —— 那会把「服务端没给时间」
 * 伪装成「刚刚采集」，正是要消除的那种谎报。
 */
function collectedAtOf(payload: LivePayload): string | undefined {
  if (typeof payload.collected_at === 'string' && payload.collected_at) return payload.collected_at
  const seconds = Number(payload.timestamp)
  if (Number.isFinite(seconds) && seconds > 0) return new Date(seconds * 1000).toISOString()
  return undefined
}

export const useEdgeDeviceLiveStore = defineStore('edgeDeviceLive', () => {
  const ws = useWebSocketStore()

  /**
   * 每设备实时状态快照。
   *
   * 用 shallowRef **整体替换**而不是深改对象：一次 flush 只产生一次依赖失效，
   * 视图层（computed）只重算一次。深改会让每个 set 各触发一轮调度。
   */
  const live = shallowRef<Record<number, DeviceLiveState>>({})

  /** 1Hz 时基。模板里的「更新于 N 秒前」依赖它，避免为相对时间再开第二个定时器。 */
  const now = ref(Date.now())

  /** 实时通道是否连通（页面据此决定是否需要 HTTP 降级轮询）。 */
  const connected = computed(() => ws.connected)

  /** 未 flush 的补丁。每设备只留最后一条 ⇒ 内存与消息速率无关，只与设备数有关。 */
  const pending = new Map<number, PendingPatch>()
  /**
   * 最近一份**状态快照**（edge_devices_live）。与 pending 一样只缓冲不渲染：
   * 它表达"现在系统里有哪些设备"，因此是 applyPending 的**基底**（可删除条目）。
   */
  let pendingSnapshot: Record<number, DeviceLiveState> | null = null
  /** 1Hz 节拍订阅者（页面用它做对账 / 降级轮询，不额外开定时器）。 */
  const tickHandlers = new Set<() => void>()
  const unsubscribers: Array<() => void> = []
  let consumers = 0
  let timer: ReturnType<typeof setInterval> | null = null
  let visibilityBound = false

  /** 把一条补丁并入缓冲。同一帧内的多条消息在这里合并（后到覆盖先到）。 */
  function bufferPatch(id: number, patch: PendingPatch) {
    const prev = pending.get(id)
    pending.set(id, {
      status: patch.status ?? prev?.status,
      data: patch.data ? { ...(prev?.data ?? {}), ...patch.data } : prev?.data,
      at: patch.at ?? prev?.at,
    })
  }

  /** 把快照载荷收敛为「设备 ID → 状态」。非法条目丢弃，绝不写入匹配不上的键。 */
  function readSnapshot(payload: LivePayload): Record<number, DeviceLiveState> {
    const items = Array.isArray(payload.items) ? payload.items : []
    const stamp = Date.now()
    const next: Record<number, DeviceLiveState> = {}
    for (const raw of items) {
      if (!raw || typeof raw !== 'object') continue
      const item = raw as { id?: unknown; status?: unknown; last_data_at?: unknown; data?: unknown }
      const id = toDeviceId(item.id)
      if (id === null) continue
      next[id] = {
        status: typeof item.status === 'string' && item.status ? mapStatus(item.status) : null,
        lastData: toReadings(item.data),
        lastDataAt: typeof item.last_data_at === 'string' && item.last_data_at ? item.last_data_at : null,
        updatedAt: stamp,
      }
    }
    return next
  }

  /**
   * 实时通道的**唯一入口**。订阅回调走这里，单测也走这里。
   *
   * 三个来源：
   *   · EDGE_DEVICES_LIVE（后端 1Hz 快照）—— 列表/看板的主来源，整体替换；
   *   · DATA_UPDATE（逐帧）—— 快照之间提供更新的读数，且兼容没有快照通道的旧后端；
   *   · EDGE_DEVICE_STATUS（状态迁移）—— 与快照同义，早到早生效。
   */
  function ingest(type: string, message: WebSocketMessage): void {
    const payload = payloadOf(message)

    if (type === WS_EVENT.EDGE_DEVICES_LIVE) {
      pendingSnapshot = readSnapshot(payload)
      return
    }

    const id = toDeviceId(payload.edge_device_id ?? payload.device_id)
    if (id === null) return

    if (type === WS_EVENT.DATA_UPDATE) {
      const data = toReadings(payload.data)
      if (!data) return // 没有读数的帧不改变任何状态（错误帧/空帧）
      bufferPatch(id, {
        // 采到数据 = 在线。理由见文件头「为什么状态也在这里推导」。
        status: 'active',
        data,
        at: collectedAtOf(payload),
      })
      return
    }

    if (type === WS_EVENT.EDGE_DEVICE_STATUS) {
      const raw = typeof payload.status === 'string' ? payload.status : ''
      if (!raw) return
      bufferPatch(id, { status: mapStatus(raw) })
    }
  }

  /**
   * 应用缓冲并推进时基。调用者只有 1Hz 定时器与「回到前台」两处。
   *
   * 后台标签页直接返回：消息照收（缓冲每设备只留最后一条，内存有界），但不产生任何
   * 渲染。回到前台由 visibilitychange 立刻补一拍 —— 用户不会看到「1 秒前的旧画面」。
   */
  function flush(): void {
    if (typeof document !== 'undefined' && document.hidden) return
    now.value = Date.now()
    applyPending()
    for (const handler of tickHandlers) handler()
  }

  function applyPending(): void {
    // 两个来源都空：这一拍没有状态变化（仍会推进 now / 触发节拍订阅者）。
    if (!pendingSnapshot && pending.size === 0) return
    const stamp = now.value

    // 合并优先级（长期约定，改动前先读）：
    //   ① 快照是**基底** —— 它表达"现在系统里有哪些设备"，缺席即删除；
    //   ② 逐帧补丁叠在**上面** —— 同一拍内它通常比快照更新鲜；
    //   ③ 没有快照（旧后端 / 尚未收到第一拍）时退回"上次结果上合并"，
    //      行为与引入快照通道之前完全一致。
    const next: Record<number, DeviceLiveState> = pendingSnapshot ?? { ...live.value }
    pendingSnapshot = null

    for (const [id, patch] of pending) {
      const prev = next[id]
      next[id] = {
        status: patch.status ?? prev?.status ?? null,
        // 跨帧累积：一条 data_update 只带本帧的物理量，读数全集要靠合并。
        lastData: patch.data ? { ...(prev?.lastData ?? {}), ...patch.data } : (prev?.lastData ?? null),
        lastDataAt: patch.at ?? prev?.lastDataAt ?? null,
        updatedAt: stamp,
      }
    }
    pending.clear()

    // TTL 清理（只在有更新时跑，避免每拍都全表遍历）。丢弃是安全的：视图层会回落快照。
    for (const key of Object.keys(next)) {
      const entry = next[Number(key)]
      if (entry && stamp - entry.updatedAt > LIVE_ENTRY_TTL_MS) delete next[Number(key)]
    }

    live.value = next
  }

  function onVisibilityChange(): void {
    if (typeof document !== 'undefined' && !document.hidden) flush()
  }

  function start(): void {
    consumers++
    if (consumers > 1) return // 已经被别的消费者启动

    unsubscribers.push(ws.subscribe(WS_EVENT.EDGE_DEVICES_LIVE, (m) => ingest(WS_EVENT.EDGE_DEVICES_LIVE, m)))
    unsubscribers.push(ws.subscribe(WS_EVENT.DATA_UPDATE, (m) => ingest(WS_EVENT.DATA_UPDATE, m)))
    unsubscribers.push(ws.subscribe(WS_EVENT.EDGE_DEVICE_STATUS, (m) => ingest(WS_EVENT.EDGE_DEVICE_STATUS, m)))
    timer = setInterval(flush, LIVE_RENDER_INTERVAL_MS)
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', onVisibilityChange)
      visibilityBound = true
    }
    flush() // 立刻补一拍：切回页面时不必等满 1 秒
  }

  function stop(): void {
    if (consumers === 0) return
    consumers--
    if (consumers > 0) return

    for (const unsub of unsubscribers.splice(0)) unsub()
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
    if (visibilityBound && typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', onVisibilityChange)
      visibilityBound = false
    }
    pending.clear()
    pendingSnapshot = null
    // live 刻意保留：短时间内返回同一页面可以直接出图；成员/配置仍以 HTTP 对账为准。
  }

  /** 注册 1Hz 节拍回调（仅页面可见时触发）。返回取消函数。 */
  function onTick(handler: () => void): () => void {
    tickHandlers.add(handler)
    return () => {
      tickHandlers.delete(handler)
    }
  }

  /** 会话失效/登出时清空（与 stores/edgeDevice.ts 的 clearCache 同一时机）。 */
  function clear(): void {
    live.value = {}
    pending.clear()
    now.value = Date.now()
  }

  /** 取单设备实时状态（没有则 undefined，调用方回落到 HTTP 快照）。 */
  function stateOf(id: number): DeviceLiveState | undefined {
    return live.value[id]
  }

  return {
    // state
    live,
    now,
    // getters
    connected,
    // actions
    start,
    stop,
    ingest,
    onTick,
    clear,
    stateOf,
  }
})

/**
 * 页面接入用组合式：挂载启动、卸载停止（引用计数）。
 *
 * 不要直接 `store.start()` 而不 stop —— 那样 WS 订阅与 1Hz 定时器会在路由切换后
 * 继续跑，且下一个页面 start 时 consumers 永远大于 1，永远停不下来。
 */
export function useEdgeDeviceLive() {
  const store = useEdgeDeviceLiveStore()
  onMounted(() => store.start())
  onUnmounted(() => store.stop())
  return store
}

registerSessionCacheClearer(() => useEdgeDeviceLiveStore().clear())
