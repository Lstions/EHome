import { computed, onMounted, onUnmounted, ref } from 'vue'
import { feedback } from '@/utils/feedback'
import { getMetricsSummary, type MetricsSummary } from '@/api/monitor'
import { UNKNOWN } from '@/utils/format'
import { readyOrUnknown } from '../monitorDisplay'

/**
 * 监控页「指标取数 / 轮询 / 时间窗 / 三态与派生口径」
 * （原 Monitor.vue 的 <script setup> 主体整块搬出，函数体与注释一字未改；
 *  仅有的一处形式变化：设备/节点总数的展示值改调 monitorDisplay.readyOrUnknown，
 *  表达式与原三元完全等价）。
 *
 * 边界依据（为什么整块搬，而不是按「取数 / 派生」再切）：
 *   · metrics / isLoading / loadError / isRefreshing 是**同一台三态机**的四支：
 *     首屏骨架、常驻错误态、刷新中标记、以及 metricsReady 这个「数值是否可信」的判据；
 *     任何一支留在页面都会出现第二份判据，F28（失败不得伪装成 0）与 F31（刷新中必须可判定）
 *     两条契约就会各长一版；
 *   · 轮询（startPolling/stopPolling）与 refreshInterval、lastUpdateTime 同生共死：
 *     时间窗一改就要重起定时器，取数成功才更新「最后更新」——拆开必然要跨文件对齐时序；
 *   · 输出只有状态 + 动作，不含任何渲染职责。
 *
 * 刻意留在页面的：
 *   · 模板与页头/统计卡/三态链 —— 源码文本门禁与 390px 真实级联门禁按页面文件断言
 *     （见 Monitor.vue 文件头「刻意留在页面」清单）；
 *   · metric / metricText 的**调用点**：页面与两个子组件各自持有同一份
 *     monitorDisplay.metricOf 的薄包装（未知态口径只有一份实现）。
 */
export function useMonitor() {
  // 状态
  //
  // 三态（F28，范式同 views/dashboard/Dashboard.vue）：
  //   isLoading  — 首次加载尚未落定          → 骨架屏
  //   loadError  — 接口失败                  → 常驻错误态 + 配套重试入口，KPI 显示「—」
  //   两者皆空    — 接口成功                  → 正常渲染；此时 0 就是"确实是 0"
  // 缺陷（U-1 家族第 4 处）：接口 500 时 40 处 `|| 0` 把"未知"渲染成"0"，
  // 与"成功但系统空闲"在 DOM 上逐字段相同，用户无法察觉接口已挂。
  const metrics = ref<MetricsSummary | null>(null)
  const isLoading = ref(true)
  /**
   * 「请求在飞」状态（F31 残余）。
   *
   * 裁决：**新增 isRefreshing，不改 isLoading 语义**。理由：
   *   · isLoading 是首屏三态机的一支（骨架 / 错误 / 正常），且被 metricsReady 依赖 ——
   *     若在刷新时把它置回 true，KPI 会瞬间整片变「—」（数值凭空消失再回来），
   *     还会与 loadError 的优先级纠缠；这属于**改变视觉**，本任务明确不做。
   *   · 刷新语义与首屏语义正交：刷新时页面已是「成功态」（保留陈旧值），
   *     只是数据不再新鲜。用一个独立布尔表达，两者互不干扰，首屏骨架行为逐字节不变。
   *   · 现有 refresh 语义在其他视图（Dashboard/NodeList/NodeOverview）也叫 refreshing，
   *     命名一致，门禁侧无需为 /monitor 记特例。
   */
  const isRefreshing = ref(false)
  const loadError = ref('')
  const refreshInterval = ref(10000)
  const lastUpdateTime = ref(UNKNOWN)
  let timer: ReturnType<typeof setInterval> | null = null

  /**
   * 指标是否可信。只有「接口成功且未失败」时，数值才允许被断言。
   * 失败 / 尚未加载时，0 与「—」的区别正是本缺陷的核心：
   * 前者是「确实是 0」，后者是「接口没说」（规范 §3.2.5）。
   */
  const metricsReady = computed(() => !isLoading.value && !loadError.value)

  // 失败可归因到接口本身；字段缺失才是真的"接口没说"
  const errorDetail = computed(() => loadError.value || '网络请求失败')

  /**
   * 「需关注」计数 = **待处置条件**之和。
   * 刻意**不含** control.failed：操作失败是进程启动以来的累计计数器（重启归零、含历史已排查项），
   * 把它算进"需要关注"会让横幅长期报警；但改前模板又给 failed 单独加了 attention 红字，
   * 造成"横幅计数"与"红字读数"两个口径互相矛盾（审计 C-ops P1）。现统一为：
   * 只有下面三项计入（failed 本身改中性计数 + tooltip 说明）。
   */
  const controlAttention = computed(() =>
    (metrics.value?.control?.unresolved_unknown || 0) +
    (metrics.value?.control?.capability_stale_nodes || 0) +
    (metrics.value?.control?.audit_write_failures || 0)
  )

  // 失败时不得断言"控制面正常"，也必须给出未知态而不是"0 项需要关注"。
  // 告警条本身由 controlAlertVisible 在失败时整体隐藏，故其标题可继续用真实计数。
  const controlHealthTag = computed(() => {
    if (!metricsReady.value) return { type: 'info' as const, text: '状态未知' }
    return controlAttention.value > 0
      ? { type: 'warning' as const, text: '需关注' }
      : { type: 'success' as const, text: '正常' }
  })
  const controlAlertVisible = computed(() => metricsReady.value && controlAttention.value > 0)

  // 计算属性
  // 总数必须含 pending（2026-10-03）：刚创建、还没采到数据的设备既不在
  // online 也不在 offline 里。漏掉它会让「本页设备总数」小于实际行数，
  // 且在线/离线百分比的分母偏小、两个百分比之和虚高。
  const deviceTotal = computed(() => {
    return (metrics.value?.device?.online || 0)
      + (metrics.value?.device?.offline || 0)
      + (metrics.value?.device?.pending || 0)
  })

  /**
   * 展示用的分母。派生值本身保留 0（数组/计数求和的空集就是 0），
   * 但**渲染**时必须跟分子一起进入未知态 —— 否则失败态会渲染出 "— / 0"，
   * 那个 0 又会被读成"总共有 0 台设备"，正是本缺陷的同一类伪装。
   */
  const deviceTotalText = computed(() => readyOrUnknown(metricsReady.value, deviceTotal.value))
  const nodeTotalText = computed(() => readyOrUnknown(metricsReady.value, nodeTotal.value))

  const deviceOnlinePercent = computed(() => {
    if (deviceTotal.value === 0) return 0
    return Math.round(((metrics.value?.device?.online || 0) / deviceTotal.value) * 100)
  })

  const deviceOfflinePercent = computed(() => {
    if (deviceTotal.value === 0) return 0
    return Math.round(((metrics.value?.device?.offline || 0) / deviceTotal.value) * 100)
  })

  // 「等待数据」占比。分母与在线/离线一致（三者相加即总数），
  // 因此三条进度条之和恒为 100%，不会出现"三个百分比加起来超过 100%"的假象。
  const devicePendingPercent = computed(() => {
    if (deviceTotal.value === 0) return 0
    return Math.round(((metrics.value?.device?.pending || 0) / deviceTotal.value) * 100)
  })

  const nodeTotal = computed(() => {
    return (metrics.value?.node?.online || 0) + (metrics.value?.node?.offline || 0)
  })

  const nodeOnlinePercent = computed(() => {
    if (nodeTotal.value === 0) return 0
    return Math.round(((metrics.value?.node?.online || 0) / nodeTotal.value) * 100)
  })

  const nodeOfflinePercent = computed(() => {
    if (nodeTotal.value === 0) return 0
    return Math.round(((metrics.value?.node?.offline || 0) / nodeTotal.value) * 100)
  })

  // 方法

  /**
   * 拉取指标。
   *
   * 失败必须留下**常驻**痕迹：只弹一条 toast 就丢掉错误态，正是本缺陷本身 ——
   * 自动刷新（默认 10s）会不断覆盖，用户回头看页面只剩一片 0，无从分辨
   * 「系统空闲」与「接口挂了」。故这里同时置 loadError，由错误横幅与 KPI 的「—」承载。
   *
   * 与 Dashboard.fetchOverview 的差别：此处**保留上一份数据**而不是清空成 0 ——
   * 清空会造成比缺陷更糟的「接口抖一下整页归零」。但保留归保留，
   * **可见数值一律改由 metric() 渲染**（见模板），失败时全部显示「—」，
   * 页面不会继续声称任何具体数字；数据留着只为重试成功后无闪烁。
   */
  const fetchMetrics = async () => {
    // 进入即置真、finally 置回：覆盖手动刷新、自动轮询、首屏、重试四条入口，
    // 且失败路径同样会复位（不只在成功分支复位）。
    isRefreshing.value = true
    try {
      const res = await getMetricsSummary()
      if (res.code === 200 && res.data) {
        metrics.value = res.data
        lastUpdateTime.value = new Date().toLocaleString('zh-CN')
        loadError.value = ''
      } else {
        // 200 但 envelope 不成立（后端异常包装 / data 为空）：同样不得当成"成功且全 0"
        const message = (res as { message?: string } | undefined)?.message
        loadError.value = message || '响应格式异常'
      }
    } catch (error) {
      console.error('获取指标失败:', error)
      loadError.value = error instanceof Error ? error.message : String(error)
      feedback.handleError(error, '获取监控数据失败')
    } finally {
      isLoading.value = false
      isRefreshing.value = false
    }
  }

  /** 错误态的重试入口：清空错误 → 重新拉取（范式同 Dashboard.retryLoad）。 */
  const retryLoad = async () => {
    loadError.value = ''
    isLoading.value = true
    try {
      await fetchMetrics()
    } finally {
      isLoading.value = false
    }
  }

  const startPolling = () => {
    stopPolling()
    if (refreshInterval.value > 0) {
      timer = setInterval(fetchMetrics, refreshInterval.value)
    }
  }

  const stopPolling = () => {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  }

  // 监听刷新间隔变化
  const handleIntervalChange = () => {
    startPolling()
  }

  // 生命周期
  onMounted(() => {
    fetchMetrics()
    startPolling()
  })

  onUnmounted(() => {
    stopPolling()
  })

  return {
    // 三态与取数
    metrics,
    isLoading,
    isRefreshing,
    loadError,
    errorDetail,
    refreshInterval,
    lastUpdateTime,
    metricsReady,
    // 控制面与设备/节点的派生口径
    controlAttention,
    controlHealthTag,
    controlAlertVisible,
    deviceTotal,
    deviceTotalText,
    nodeTotal,
    nodeTotalText,
    deviceOnlinePercent,
    deviceOfflinePercent,
    devicePendingPercent,
    nodeOnlinePercent,
    nodeOfflinePercent,
    // 动作
    fetchMetrics,
    retryLoad,
    handleIntervalChange,
  }
}
