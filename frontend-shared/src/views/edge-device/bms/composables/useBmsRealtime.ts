import { computed, ref } from 'vue'
import type { Ref } from 'vue'
import { edgeDeviceApi } from '@/api/edgeDevice'
import { extractErrorMessage } from '@/utils/feedback'
import { useRealtimeData } from '@/composables/useRealtimeData'
import { useWebSocketStore } from '@/stores/websocket'
import {
  REALTIME_FIRST_DATA_GRACE_MS,
  resolveRealtimeViewState,
  type RealtimeViewState,
} from '../realtimeViewState'
import { DASH, fmt, num } from '../bmsFormat'

/**
 * BMS 详情页「实时数据」状态域：最新快照兜底 + 首屏四态 + 按字段口径派生的展示值。
 *
 * 为什么单独拆出来（2026-11-12 纯搬移式拆分）：
 *   这一段是**一个数据域**（同一份 latestData 的取数与派生），与页面的路由/页头/弹窗
 *   没有耦合；页面里保留的只是「什么时候调用」（onMounted / 切换设备 / 刷新 / 重试）。
 *
 * 边界说明：
 *   · WS 连接本身仍由页面建立（本页是独立 DEV 路由，页面负责 connect），这里只在
 *     retryRealtime 里做一次幂等重连（与拆分前一致）；
 *   · 保护位派生不在这里（见 bmsProtection.ts）：它属于保护状态卡的口径，
 *     并且要同时供 MOS 卡判断「可能被硬件保护关断」。
 */

/** 实时数据流缓存上限（本页只消费 latestData，保留该项是为了与拆分前逐字一致） */
const REALTIME_MAX_ITEMS = 200

export function useBmsRealtime(deviceId: Ref<number>) {
  // 本页是独立 DEV 路由（不在 MainLayout 之下，而 WS 连接由 MainLayout 建立）。
  // 不在这里补一次 connect()，useRealtimeData 就永远收不到推送 —— 实时流会只剩快照兜底。
  const wsStore = useWebSocketStore()
  /**
   * 页头「实时连接」标签：与逆变器页/通用设备页同一口径（WS 已连接才显示）。
   * 改前自建页头没有这个状态位 —— 统一页头后补上，用户一眼能看出数据是否在推。
   */
  const wsConnected = computed(() => wsStore.isConnected)
  // 只消费 latestData（按 sensor_name 取值的字典）。dataItems（实时流列表）本页
  // 已于 2026-10-21 按用户要求移除渲染，不再解构 —— 留着会是一个永不被读的 ref。
  const { latestData } = useRealtimeData({ deviceId, maxItems: REALTIME_MAX_ITEMS })

  const snapshotError = ref('')
  /** 最新快照请求在途（首屏骨架的判定信号之一；改前没有这个状态位） */
  const snapshotLoading = ref(false)

  /**
   * 挂载时用最新快照兜底（WS 尚无推送时不至于整页空白），口径同 useDeviceData。
   *
   * 2026-11-11 原始数据退役：原 `/latest-data`（device_data 最后一条原始记录）
   * 已随表删除，改读 `/latest-values`（每个传感器各自的最新值，跨命令聚合）。
   *
   * 这不是"换个地址拿同一份数据"，展示口径**变好了**：/latest-data 只含最后
   * 执行的那条命令的物理量，对 ASCII 一问一答设备（逆变器，9 条命令/周期）
   * 表现为"页面大部分是空的"；/latest-values 是全集。本页各指标卡本来就把
   * latestData 当「按 sensor_name 取值的字典」用（latestData.value?.cell_voltage_1
   * 之类），形状完全兼容。
   */
  async function loadLatestSnapshot() {
    snapshotError.value = ''
    snapshotLoading.value = true
    try {
      const values = await edgeDeviceApi.getLatestValues(deviceId.value)
      if (values.length === 0) return
      // 只取数值：/latest-values 的契约是 {sensor_name, value, unit, timestamp}。
      //
      // ⚠ 能力损失（方案 §3.1 已裁决，非本次引入）：字符串型读数
      // （hardware_version="V19" / serial_number）原来只存在
      // device_data.data_json.str_values 里，随表删除后无落库位置 ——
      // 生产实测该字段全库 0 行，这条路径从未被走过，故不丢真实数据。
      const parsed: Record<string, any> = {}
      for (const v of values) parsed[v.sensor_name] = v.value
      latestData.value = { ...(latestData.value ?? {}), ...parsed }
      // 实时数据流只由 WebSocket 填充：快照是"现在各字段是多少"的点，
      // 不是"刚收到了一帧"的事件。把快照包装成一条历史 item 塞进流里，
      // 会凭空造出一条用户从未见过的"收到记录"（时间戳还是快照时刻）。
      // 该列表已于 2026-10-21 按用户要求从页面移除，这里顺带删掉死写入。
    } catch (e) {
      // 快照只是兜底：失败不弹 toast，但原因常驻页面（WS 仍会带来数据）。
      snapshotError.value = extractErrorMessage(e, '最新快照加载失败')
    } finally {
      snapshotLoading.value = false
    }
  }

  /**
   * 快照是**兜底**请求（WS 才是主通道），它的失败不该把 axios 原文怼到用户脸上。
   * 生产实测：历史查询并发时 latest-data 会 10s 超时，页面就常驻一行
   * 「最新快照加载失败：timeout of 10000ms exceeded」—— 纯技术噪声。
   * 这里只保留「发生了什么 + 还能做什么」，原始信息不再展示（日志里仍有）。
   */
  const snapshotErrorText = computed(() => {
    const raw = snapshotError.value
    if (!raw) return ''
    // 2026-10-21：「实时数据流」列表已按用户要求移除，但 WS 实时通道仍在 ——
    // 文案不能再承诺「数据流仍会更新」（页面已无该列表），改为「通过实时连接更新数据」。
    if (/timeout/i.test(raw)) return '最新快照请求超时（设备数据较多时偶发；页面仍会通过实时连接更新数据）'
    if (/network|Network Error/i.test(raw)) return '最新快照请求失败：网络不可达（页面仍会通过实时连接更新数据）'
    return '最新快照加载失败：' + raw
  })

  // ════════════ 实时数据首屏四态（2026-10-21 修复「加载中伪装成空数据」） ════════════
  /**
   * 用户真机反馈：刚进 BMS 页面时顶栏指标卡显示「—」、保护状态卡显示「暂无数据」+ 12 个
   * 「正常」、MOS 卡显示「尚无 fet_status 数据」，而真实状态是**还在加载**。
   * 把「未知」渲染成「空数据 / 正常 / 关断」是信息错误 —— 保护位尤其危险：
   * 「未知」被当成「正常」会让用户错过真实故障。
   *
   * 修法：用四态（与其它列表页同款、互斥）驱动三块卡片的渲染：
   *   loading → 骨架（不渲染「—」/「暂无数据」/「正常」徽标）
   *   error   → 独立错误文案 + 重试（绝不与空态共用文案）
   *   data    → 真实数据（保护位未知时显示「未知」而不是「正常」）
   *   empty   → 明确「设备未上报」，不是加载中
   */
  const hasRealtimeData = computed(() => !!latestData.value && Object.keys(latestData.value).length > 0)
  const firstDataGraceOver = ref(false)
  let firstDataGraceTimer: ReturnType<typeof setTimeout> | null = null
  /** 重新开始首帧宽限计时（首挂载 / 切换设备 / 手动重试时调用） */
  function startFirstDataGrace() {
    firstDataGraceOver.value = false
    if (firstDataGraceTimer) clearTimeout(firstDataGraceTimer)
    firstDataGraceTimer = setTimeout(() => { firstDataGraceOver.value = true }, REALTIME_FIRST_DATA_GRACE_MS)
  }
  const realtimeState = computed<RealtimeViewState>(() => resolveRealtimeViewState({
    hasData: hasRealtimeData.value,
    snapshotLoading: snapshotLoading.value,
    snapshotError: snapshotError.value,
    graceOver: firstDataGraceOver.value,
  }))
  /** 加载中：三块卡片渲染骨架，**不渲染「—」/「暂无数据」/「正常」徽标** */
  const realtimeLoading = computed(() => realtimeState.value === 'loading')
  /** 失败：独立错误态 + 重试（与空态文案彻底分开） */
  const realtimeError = computed(() => realtimeState.value === 'error')
  /** 空态：宽限期已过、请求成功但设备确实没上报 —— 与「加载中」分开表达 */
  const realtimeEmpty = computed(() => realtimeState.value === 'empty')
  /** 重试：清掉错误、重新请求快照并重新给首帧宽限（WS 断线时顺带重连） */
  function retryRealtime() {
    snapshotError.value = ''
    startFirstDataGrace()
    wsStore.connect()
    void loadLatestSnapshot()
  }
  /** 设备切换时复位（首帧宽限重新计时；旧设备的错误不得留在新设备页面上） */
  function resetRealtime() {
    snapshotError.value = ''
    snapshotLoading.value = false
    startFirstDataGrace()
  }
  /** 组件卸载时必须清掉计时器（否则路由离开后还会写已卸载组件的状态） */
  function disposeFirstDataGrace() {
    if (firstDataGraceTimer) clearTimeout(firstDataGraceTimer)
    firstDataGraceTimer = null
  }

  // ── 字段口径：单体电压 cell_voltage_N / 温度 temp_N（或 temperature_N） ──
  const cellCount = computed(() => {
    const declared = num(latestData.value?.cell_count)
    if (declared && declared > 0) return Math.min(32, Math.floor(declared))
    let highest = 0
    for (let i = 1; i <= 32; i++) {
      if (num(latestData.value?.['cell_voltage_' + i] ?? latestData.value?.['cell_v_' + i]) !== null) highest = i
    }
    return highest
  })
  const cellCountLabel = computed(() => (cellCount.value > 0 ? cellCount.value + 'S' : DASH))

  interface CellVoltage { index: number; voltage: number }
  const cells = computed<CellVoltage[]>(() => {
    const out: CellVoltage[] = []
    for (let i = 1; i <= cellCount.value; i++) {
      const v = num(latestData.value?.['cell_voltage_' + i] ?? latestData.value?.['cell_v_' + i])
      if (v !== null) out.push({ index: i, voltage: v })
    }
    return out
  })
  const cellSum = computed(() => cells.value.reduce((a, b) => a + b.voltage, 0))
  const totalVoltage = computed(() => {
    const declared = num(latestData.value?.total_voltage ?? latestData.value?.voltage)
    if (declared !== null) return declared
    return cells.value.length ? cellSum.value : null
  })
  const avgCell = computed(() => (totalVoltage.value !== null && cells.value.length ? totalVoltage.value / cells.value.length : null))
  const maxCell = computed<CellVoltage>(() =>
    cells.value.length ? cells.value.reduce((a, b) => (b.voltage > a.voltage ? b : a)) : { index: 0, voltage: 0 })
  const minCell = computed<CellVoltage>(() =>
    cells.value.length ? cells.value.reduce((a, b) => (b.voltage < a.voltage ? b : a)) : { index: 0, voltage: 0 })
  const diff = computed(() => maxCell.value.voltage - minCell.value.voltage)
  /** 供旧页 BmsCellVoltageChart 使用的纯数值序列 */
  const cellVoltages = computed(() => cells.value.map((c) => c.voltage))
  const soc = computed(() => num(latestData.value?.rsoc ?? latestData.value?.soc))
  const socProgress = computed(() => (soc.value === null ? undefined : Math.max(0, Math.min(100, Math.round(soc.value)))))
  const nominalCapacity = computed(() => num(latestData.value?.nominal_capacity))
  const remainingCapacity = computed(() => {
    const declared = num(latestData.value?.remaining_capacity)
    if (declared !== null) return declared
    if (soc.value !== null && nominalCapacity.value !== null) return (soc.value / 100) * nominalCapacity.value
    return null
  })
  const currentDisplay = computed(() => num(latestData.value?.current))
  const currentHint = computed(() => {
    const c = currentDisplay.value
    if (c === null) return '暂无电流数据'
    if (c < 0) return '放电中（电流为负）'
    if (c > 0) return '充电中（电流为正）'
    return '无电流'
  })
  const voltageSubText = computed(() => {
    if (cells.value.length) return cells.value.length + ' 串 · 平均 ' + fmt(avgCell.value, 3) + 'V'
    if (cellCount.value) return cellCount.value + ' 串 · 单体电压未上报'
    return DASH
  })
  const capacitySubText = computed(() => '/ ' + fmt(nominalCapacity.value, 0) + ' Ah 标称')

  // ── 温度探头 ──
  interface TempProbe { index: number; value: number; level: 'success' | 'warning' | 'danger'; text: string }
  function tempLevel(v: number): 'success' | 'warning' | 'danger' {
    return v > 60 ? 'danger' : v > 45 ? 'warning' : 'success'
  }
  function tempText(v: number): string {
    return v > 60 ? '过高' : v > 45 ? '偏高' : '正常'
  }
  const temps = computed<TempProbe[]>(() => {
    const out: TempProbe[] = []
    for (let i = 1; i <= 8; i++) {
      const v = num(latestData.value?.['temp_' + i] ?? latestData.value?.['temperature_' + i])
      if (v !== null) out.push({ index: i, value: v, level: tempLevel(v), text: tempText(v) })
    }
    return out
  })

  /** 页面挂载时建立一次 WS 连接（幂等：已连接/连接中直接返回，不打断 MainLayout 建立的连接） */
  function connectRealtime() {
    wsStore.connect()
  }

  return {
    wsConnected,
    connectRealtime,
    latestData,
    snapshotError,
    snapshotLoading,
    snapshotErrorText,
    realtimeState,
    realtimeLoading,
    realtimeError,
    realtimeEmpty,
    loadLatestSnapshot,
    retryRealtime,
    startFirstDataGrace,
    resetRealtime,
    disposeFirstDataGrace,
    cellCount,
    cellCountLabel,
    cells,
    totalVoltage,
    maxCell,
    minCell,
    diff,
    cellVoltages,
    soc,
    socProgress,
    remainingCapacity,
    currentDisplay,
    currentHint,
    voltageSubText,
    capacitySubText,
    temps,
  }
}
