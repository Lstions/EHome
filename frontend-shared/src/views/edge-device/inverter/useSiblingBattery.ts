/**
 * useSiblingBattery —— 同节点 BMS 的电池电流（功率流向电池支路的**兜底判向源**）。
 *
 * ## 为什么需要它（2026-11-19 用户报障）
 *
 * 「BMS 都在放电了，INV 流向图还显示电池静止，不对！」
 *
 * 现场数据（生产库，同一分钟级窗口 16:03–16:09）：
 *   · 逆变器 device 15：battery_voltage=53.1V、battery_capacity=89%、
 *     battery_charge_current=0A、battery_discharge_current=0A；
 *   · 同节点 BMS device 14：total_voltage=53.15V、rsoc=99%、current=-1.96A。
 * 逆变器连续数分钟把电池电流读成 0（泰琪丰 HBAT 的充/放电是两路独立寄存器、
 * 整安培分辨率，且这台机器没有接 BMS 通讯、89% 是它自己的电压估算）。
 * 页面只消费逆变器自己的读数 ⇒ 把「没测到」显示成「静止」。
 *
 * BMS 是电池的**直接测量**（分流器 + 库仑计），同一节点上它就是那一块电池的
 * 权威电流源；因此逆变器两路都为 0 时，方向改由 BMS 判（见 powerFlow.batteryFlow）。
 *
 * ## 边界（这些都不做，避免"用道听途说替代测量"）
 *
 *   · **只认同节点**：node_id 来自逆变器设备自身的 node_id；跨节点不猜"是不是同一块电池"；
 *   · **只认同类设备**：类型必须在 BMS_DEVICE_TYPES 白名单里（与 backend driver DeviceType 对齐）；
 *   · **只认新鲜读数**：超过 BMS_FRESH_MS 没有更新 ⇒ 返回 null —— 离线 BMS 不得点亮箭头；
 *   · **不伪造逆变器数据**：本 composable 只对外给一个"BMS 电流"，逆变器电压/容量/电流
 *     仍按原样展示，判定与标注由 powerFlow / 组件负责。
 */
import { computed, onUnmounted, ref, watch, type Ref } from 'vue'
import { edgeDeviceApi } from '@/api/edgeDevice'
import { useRealtimeData } from '@/composables/useRealtimeData'
import { logger } from '@/utils/logger'

/**
 * 视为「电池 BMS」的设备类型白名单。
 *
 * ⚠ 与 backend/internal/drivers 下各驱动的 `DeviceType()` 对齐；新增 BMS 驱动时
 * 必须同步这里，否则新设备在功率流向上等于不存在（且不会有任何报错）。
 */
export const BMS_DEVICE_TYPES: readonly string[] = ['jiabaida_bms']

/**
 * 读数新鲜度闸门（ms）。
 *
 * 取 120s：BMS 轮询周期是秒级，120s 足以跨越「WS 短暂断线 + 页面重连」；
 * 再长就会把「电池两小时前在放电」当成现在。
 */
export const BMS_FRESH_MS = 120_000

export interface SiblingBattery {
  /** BMS 有符号电流（A，正=充电 / 负=放电）；无设备 / 无读数 / 读数过期 ⇒ null */
  current: Ref<number | null>
  /** 实际用于判向的采样时刻（epoch ms）；无读数时为 0 */
  seenAt: Ref<number>
  /** 找到的同节点 BMS 设备 id；没找到为 null */
  deviceId: Ref<number | null>
}

/**
 * @param nodeId          逆变器所属节点（node_id 字符串）；null/undefined 时不解析
 * @param excludeDeviceId 逆变器自身 id（防御：同节点上若把自己当 BMS 就不会选中）
 */
export function useSiblingBattery(
  nodeId: Ref<string | number | null | undefined>,
  excludeDeviceId: Ref<number | null>,
): SiblingBattery {
  const deviceId = ref<number | null>(null)
  /** 首屏快照（/latest-values）里的 BMS 电流；WS 到达后由实时值接管 */
  const snapshot = ref<{ current: number | null; at: number }>({ current: null, at: 0 })

  const { latestData, fieldAt } = useRealtimeData({ deviceId, maxItems: 30 })

  /**
   * 「现在」必须自己走。
   *
   * 若 freshness 只在数据到达时算一次，BMS 掉线后 computed 不会被触发，
   * 页面会一直拿最后一条陈旧电流判向 —— 正是"离线设备也能点亮箭头"。
   * 15s 一次的重算足够（BMS 周期是秒级，闸门是 120s）。
   */
  const now = ref(Date.now())
  const ticker = setInterval(() => { now.value = Date.now() }, 15_000)
  onUnmounted(() => clearInterval(ticker))

  /** 解析同节点 BMS 设备并拉一次首屏快照（WS 到达前也能立刻正确判向） */
  let resolveSeq = 0
  const resolveBms = async () => {
    const nid = nodeId.value
    const exclude = excludeDeviceId.value
    const seq = ++resolveSeq
    deviceId.value = null
    snapshot.value = { current: null, at: 0 }
    if (nid === null || nid === undefined || nid === '') return

    try {
      const page = await edgeDeviceApi.getList({ node_id: nid, page: 1, page_size: 50 })
      if (seq !== resolveSeq) return
      const bms = page.items.find(d => d.id !== exclude && BMS_DEVICE_TYPES.includes(d.device_type))
      if (!bms) return
      deviceId.value = bms.id

      const values = await edgeDeviceApi.getLatestValues(bms.id)
      if (seq !== resolveSeq) return
      const hit = values.find(v => v.sensor_name === 'current')
      if (!hit) return
      const n = Number(hit.value)
      const at = Date.parse(hit.timestamp)
      snapshot.value = {
        current: Number.isFinite(n) ? n : null,
        at: Number.isFinite(at) ? at : 0,
      }
    } catch (error: any) {
      // 兄弟设备解析失败不能打断逆变器页：只记日志，判向退回逆变器自身的读数。
      logger.error('解析同节点 BMS 失败', { error: String(error), nodeId: String(nid) })
    }
  }

  watch([nodeId, excludeDeviceId], resolveBms, { immediate: true })

  const picks = computed(() => {
    const liveAt = fieldAt.value.current ?? 0
    const liveRaw = latestData.value?.current
    const liveVal = liveRaw === undefined || liveRaw === null ? NaN : Number(liveRaw)
    const liveOk = Number.isFinite(liveVal) && liveAt > 0

    if (liveOk && liveAt >= snapshot.value.at) return { value: liveVal, at: liveAt }
    if (snapshot.value.current !== null) return { value: snapshot.value.current, at: snapshot.value.at }
    return null
  })

  const current = computed<number | null>(() => {
    const pick = picks.value
    if (!pick) return null
    if (now.value - pick.at > BMS_FRESH_MS) return null
    return pick.value
  })

  const seenAt = computed(() => {
    const pick = picks.value
    return pick ? pick.at : 0
  })

  return { current, seenAt, deviceId }
}
