<template>
  <div class="monitor-container">
    <!-- 顶部操作栏 -->
    <div class="toolbar">
      <h2><el-icon aria-hidden="true"><DataAnalysis /></el-icon> 系统监控</h2>
      <div class="toolbar-actions">
        <el-select
          v-model="refreshInterval"
          class="refresh-interval-select"
          size="default"
          aria-label="自动刷新间隔"
          @change="handleIntervalChange"
        >
          <el-option label="5秒" :value="5000" />
          <el-option label="10秒" :value="10000" />
          <el-option label="30秒" :value="30000" />
          <el-option label="1分钟" :value="60000" />
          <el-option label="关闭自动刷新" :value="0" />
        </el-select>
        <el-button type="primary" :icon="Refresh" @click="fetchMetrics">手动刷新</el-button>
      </div>
    </div>

    <!-- 接口失败：常驻错误态 + **配套**重试入口（范式同 views/dashboard/Dashboard.vue）。
         只弹一条 toast 不算错误态：自动刷新（默认 10s）会不断覆盖它，
         用户回头看页面只剩一片 0，与"系统真的空闲"逐字段相同（F28 / U-1 第 4 处）。 -->
    <el-alert
      v-if="loadError"
      class="monitor-error-alert"
      type="error"
      show-icon
      :closable="false"
      data-test="monitor-error"
    >
      <template #title>
        <span class="monitor-error-text">获取监控数据失败：{{ errorDetail }}</span>
        <el-button link type="primary" size="small" data-test="monitor-retry" @click="retryLoad">重试</el-button>
      </template>
    </el-alert>

    <!-- 统计卡片
         规范 §4.3 统计卡 MUST「统计值必须标明范围」。
         本页四个 KPI 取自两种不同的统计口径，必须逐个标注，否则用户会把
         「进程启动以来的计数器」误读成「库里累计的数据量」：
           · HTTP 请求总数 / 数据点采集总数 —— Prometheus Counter，**进程启动以来**的累计值，
             服务重启即归零（后代 handler_metrics.go:82/106 readCounterTotal）
           · 设备/节点在线状态 —— 直接 COUNT 全表，**全局**口径，与页面筛选无关
         is-stale：接口未成功时不得让"在线"绿点继续宣称健康（见 metric()）。 -->
    <div class="stat-cards" :class="{ 'is-stale': !metricsReady }">
      <el-row :gutter="16">
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card http">
            <div class="stat-icon"><el-icon><Connection /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">{{ metricText(metrics?.http?.requests_total, true) }}</div>
              <div class="stat-label">HTTP 请求总数<span class="stat-scope">{{ SCOPE_PROCESS }}</span></div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card device">
            <div class="stat-icon"><el-icon><Monitor /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">
                <span :class="onlineValueClass(metrics?.device?.online, deviceTotal)">{{ metric(metrics?.device?.online) }}</span>
                <span class="separator">/</span>
                <span>{{ deviceTotalText }}</span>
              </div>
              <div class="stat-label">设备在线状态<span class="stat-scope">{{ SCOPE_GLOBAL }}</span></div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card collector">
            <div class="stat-icon"><el-icon><Cpu /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">
                <span :class="onlineValueClass(metrics?.node?.online, nodeTotal)">{{ metric(metrics?.node?.online) }}</span>
                <span class="separator">/</span>
                <span>{{ nodeTotalText }}</span>
              </div>
              <div class="stat-label">节点在线状态<span class="stat-scope">{{ SCOPE_GLOBAL }}</span></div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card data">
            <div class="stat-icon"><el-icon><DataLine /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">{{ metricText(metrics?.data?.points_collected, true) }}</div>
              <div class="stat-label">数据点采集总数<span class="stat-scope">{{ SCOPE_PROCESS }}</span></div>
            </div>
          </el-card>
        </el-col>
      </el-row>
    </div>

    <!-- 控制面健康：与接口成败解耦，始终渲染。
         失败/未加载时整卡进入未知态（标签「状态未知」、9 个读数「—」），
         绝不显示「正常」——那正是本缺陷（把接口故障说成健康）。
         范式同 Dashboard 的「异常摘要」卡：先声明未知，再谈结论。
         位置与 .detail-panels 平级（该容器本就没有自身样式），故外观不变；
         放在 v-if 链**之前**是为了让三支分支保持连续（v-else 必须紧邻）。 -->
    <el-row class="control-health" :class="{ 'is-stale': !metricsReady }">
      <el-col :span="24">
        <el-card shadow="hover">
          <template #header>
            <div class="card-header">
              <span><el-icon><Operation /></el-icon> 控制面健康</span>
              <el-tag :type="controlHealthTag.type" size="small" data-test="monitor-control-tag">
                {{ controlHealthTag.text }}
              </el-tag>
            </div>
          </template>
          <div class="control-grid">
            <div class="control-metric"><span>操作总数</span><strong>{{ metricText(metrics?.control?.operations_total, true) }}</strong></div>
            <div class="control-metric"><span>活跃操作</span><strong>{{ metric(metrics?.control?.active) }}</strong></div>
            <div class="control-metric">
              <el-tooltip content="Outbox 待处理：已写入待发送队列、等待投递的控制消息数" placement="top">
                <span class="term">Outbox 待处理</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.outbox_pending) }}</strong>
            </div>
            <div class="control-metric">
              <el-tooltip content="Outbox 租约中：已被投递任务领取（租约锁定）、正在发送中的消息数" placement="top">
                <span class="term">Outbox 租约中</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.outbox_leased) }}</strong>
            </div>
            <div class="control-metric" :class="{ attention: (metrics?.control?.unresolved_unknown || 0) > 0 }">
              <el-tooltip content="执行结果未知（UNKNOWN）且尚未人工处置的操作数" placement="top">
                <span class="term">未处置 UNKNOWN</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.unresolved_unknown) }}</strong>
            </div>
            <div class="control-metric" :class="{ attention: (metrics?.control?.capability_stale_nodes || 0) > 0 }">
              <el-tooltip content="能力快照超过有效期未上报的节点数" placement="top">
                <span class="term">能力快照过期</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.capability_stale_nodes) }}</strong>
            </div>
            <div class="control-metric" :class="{ attention: (metrics?.control?.audit_write_failures || 0) > 0 }">
              <el-tooltip content="审计日志写入失败的次数" placement="top">
                <span class="term">审计写失败</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.audit_write_failures) }}</strong>
            </div>
            <div class="control-metric"><span>操作成功</span><strong>{{ metric(metrics?.control?.succeeded) }}</strong></div>
            <div class="control-metric" :class="{ attention: (metrics?.control?.failed || 0) > 0 }"><span>操作失败</span><strong>{{ metric(metrics?.control?.failed) }}</strong></div>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 失败时不得渲染「0 项需要关注」——那是把"未知"说成"正常" -->
    <el-alert
      v-if="controlAlertVisible"
      class="control-alert"
      type="warning"
      show-icon
      :closable="false"
      data-test="monitor-control-alert"
      :title="`控制面有 ${controlAttention} 项需要关注`"
    />

    <!-- 详细面板：接口失败时整块换成错误态，绝不留一排 0 冒充真实读数 -->
    <div v-if="loadError" class="detail-error" data-test="monitor-detail-error">
      <el-icon class="detail-error-icon" aria-hidden="true"><WarningFilled /></el-icon>
      <p class="detail-error-title">监控指标加载失败</p>
      <p class="detail-error-desc">
        HTTP / MQTT / 设备 / 节点 / 数据采集 / WebSocket / 控制面 各项读数当前均不可用，
        页面不再显示任何具体数值，以免把接口故障误读成"系统空闲"。
      </p>
      <el-button type="primary" data-test="monitor-detail-retry" @click="retryLoad">重试</el-button>
    </div>

    <div v-else-if="isLoading" class="detail-skeleton" data-test="monitor-loading" aria-busy="true">
      <el-row :gutter="16">
        <el-col v-for="i in 4" :key="i" :xs="24" :sm="12">
          <SkeletonCard variant="card" animated />
        </el-col>
      </el-row>
    </div>

    <div v-else class="detail-panels">
      <el-row :gutter="16">
        <!-- HTTP 监控 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Connection /></el-icon> HTTP 监控</span>
              </div>
            </template>
            <el-descriptions :column="isMobile ? 1 : 2" border>
              <el-descriptions-item label="请求总数">
                {{ metricText(metrics?.http?.requests_total, true) }}
              </el-descriptions-item>
              <el-descriptions-item label="处理中请求">
                {{ metric(metrics?.http?.requests_in_flight) }}
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>

        <!-- MQTT 监控 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Promotion /></el-icon> MQTT 监控</span>
              </div>
            </template>
            <el-descriptions :column="isMobile ? 1 : 2" border>
              <el-descriptions-item label="接收消息">
                {{ metricText(metrics?.mqtt?.messages_received, true) }}
              </el-descriptions-item>
              <el-descriptions-item label="发送消息">
                {{ metricText(metrics?.mqtt?.messages_sent, true) }}
              </el-descriptions-item>
              <el-descriptions-item label="连接错误">
                <span :class="{ 'error-text': (metrics?.mqtt?.connection_errors || 0) > 0 }">
                  {{ metric(metrics?.mqtt?.connection_errors, true) }}
                </span>
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>
      </el-row>

      <el-row :gutter="16" style="margin-top: 20px;">
        <!-- 设备状态 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Monitor /></el-icon> 设备状态</span>
              </div>
            </template>
            <div class="status-bars">
              <div class="status-item">
                <span class="status-label">在线</span>
                <el-progress
                  :percentage="deviceOnlinePercent"
                  :stroke-width="20"
                  :color="'var(--color-success)'"
                >
                  <span>{{ metric(metrics?.device?.online) }}</span>
                </el-progress>
              </div>
              <div class="status-item">
                <span class="status-label">离线</span>
                <el-progress
                  :percentage="deviceOfflinePercent"
                  :stroke-width="20"
                  :color="'var(--color-danger)'"
                >
                  <span>{{ metric(metrics?.device?.offline) }}</span>
                </el-progress>
              </div>
            </div>
          </el-card>
        </el-col>

        <!-- 节点状态 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Cpu /></el-icon> 节点状态</span>
              </div>
            </template>
            <div class="status-bars">
              <div class="status-item">
                <span class="status-label">在线</span>
                <el-progress
                  :percentage="nodeOnlinePercent"
                  :stroke-width="20"
                  :color="'var(--color-success)'"
                >
                  <span>{{ metric(metrics?.node?.online) }}</span>
                </el-progress>
              </div>
              <div class="status-item">
                <span class="status-label">离线</span>
                <el-progress
                  :percentage="nodeOfflinePercent"
                  :stroke-width="20"
                  :color="'var(--color-danger)'"
                >
                  <span>{{ metric(metrics?.node?.offline) }}</span>
                </el-progress>
              </div>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <el-row :gutter="16" style="margin-top: 20px;">
        <!-- 数据采集 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><DataLine /></el-icon> 数据采集</span>
              </div>
            </template>
            <el-descriptions :column="isMobile ? 1 : 2" border>
              <el-descriptions-item label="已采集">
                {{ metricText(metrics?.data?.points_collected, true) }}
              </el-descriptions-item>
              <el-descriptions-item label="已存储">
                {{ metricText(metrics?.data?.points_stored, true) }}
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>

        <!-- WebSocket -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Connection /></el-icon> WebSocket</span>
              </div>
            </template>
            <el-descriptions :column="isMobile ? 1 : 2" border>
              <el-descriptions-item label="活跃连接">
                {{ metric(metrics?.websocket?.connections_active) }}
              </el-descriptions-item>
              <el-descriptions-item label="消息总数">
                {{ metricText(metrics?.websocket?.messages_total, true) }}
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>
      </el-row>
    </div>

    <!-- 更新时间：失败时明确标注数据已陈旧，否则页脚会替接口"背书" -->
    <div class="footer-info">
      <span>最后更新: {{ lastUpdateTime }}</span>
      <span v-if="loadError" class="footer-stale" data-test="monitor-stale">
        （监控接口不可用，显示的数据已过期）
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { 
  Connection, Monitor, Cpu, DataLine, Promotion, Refresh, DataAnalysis, Operation, WarningFilled
} from '@element-plus/icons-vue'
import { getMetricsSummary, type MetricsSummary } from '@/api/monitor'
import { useResponsive } from '@/composables/useResponsive'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import { UNKNOWN, metricOrDash } from '@/utils/format'

const { isMobile } = useResponsive()

/**
 * 进度条配色（F17）：直接传 var(--color-*) 字符串。
 *
 * el-progress 的 :color 最终落到 barStyle.backgroundColor（EP progress 源码里
 * 非渐变分支即 backgroundColor），而这是**由浏览器解析**的声明值 ——
 * var() 在此处照常生效，无需 JS 解析，也无需 MutationObserver 跟随主题。
 *
 * 此前是「JS 读取主题 token + MutationObserver 跟随切主题」：结果虽正确，
 * 但按规范 §3.6.2「色值取自 token 层」应以 CSS 变量为首选；
 * JS 解析只在真正取不到 CSS 的场景（ECharts 等 Canvas 渲染）才保留。
 * （源码守卫测试断言本文件不再出现该工具函数名，故此处不写其标识符。）
 */

/**
 * 统计卡范围词（规范 §4.3 统计卡 MUST）。
 * 常量化的理由：验收要能机械地证明"每个统计值都带了范围词"，
 * 字面量散落则无法审计，也无法与 /data 的范围词保持一致。
 */
const SCOPE_PROCESS = '进程启动以来'
const SCOPE_GLOBAL = '全局'

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

/** 大数缩写（K/M）。仅接受数字：未知态在进入本函数前已被 metric() 拦下。 */
const formatNumber = (num: number): string => {
  if (num >= 1000000) {
    return (num / 1000000).toFixed(2) + 'M'
  }
  if (num >= 1000) {
    return (num / 1000).toFixed(2) + 'K'
  }
  return num.toString()
}

/**
 * 指标取值：未知一律显示 UNKNOWN「—」，绝不回落成 0。
 * 注意计数器（Counter）与仪表（Gauge）在**显示层**同构 —— 缺字段都是「接口没说」，
 * 故 isCounter 只作语义标注参数保留，不改变返回值（与 views/data-source 的 metric() 一致）。
 */
const metric = (value: number | null | undefined, isCounter = false): string | number => {
  void isCounter
  return metricOrDash(value, metricsReady.value)
}

/** 可格式化取数：未知时返回 UNKNOWN 占位符，交由 formatNumber 原样透出。 */
const metricText = (value: number | null | undefined, isCounter = false): string | number => {
  const v = metric(value, isCounter)
  return typeof v === 'number' ? formatNumber(v) : v
}

// 失败可归因到接口本身；字段缺失才是真的"接口没说"
const errorDetail = computed(() => loadError.value || '网络请求失败')

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
const deviceTotal = computed(() => {
  return (metrics.value?.device?.online || 0) + (metrics.value?.device?.offline || 0)
})

/**
 * 展示用的分母。派生值本身保留 0（数组/计数求和的空集就是 0），
 * 但**渲染**时必须跟分子一起进入未知态 —— 否则失败态会渲染出 "— / 0"，
 * 那个 0 又会被读成"总共有 0 台设备"，正是本缺陷的同一类伪装。
 */
const deviceTotalText = computed(() => (metricsReady.value ? deviceTotal.value : UNKNOWN))
const nodeTotalText = computed(() => (metricsReady.value ? nodeTotal.value : UNKNOWN))

const deviceOnlinePercent = computed(() => {
  if (deviceTotal.value === 0) return 0
  return Math.round(((metrics.value?.device?.online || 0) / deviceTotal.value) * 100)
})

const deviceOfflinePercent = computed(() => {
  if (deviceTotal.value === 0) return 0
  return Math.round(((metrics.value?.device?.offline || 0) / deviceTotal.value) * 100)
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
// 在线数为 0 且总数 > 0 时用告警色，避免与同页"离线=红"的语义冲突
const onlineValueClass = (online: number | undefined, total: number) => {
  return total > 0 && (online || 0) === 0 ? 'online-none' : 'online'
}

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
    ElMessage.error('获取监控数据失败')
  } finally {
    isLoading.value = false
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
</script>

<style scoped>
.monitor-container {
  padding: 0;
}

.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 20px;
}

.toolbar h2 {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  font-size: 24px;
}

.toolbar-actions {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}

.refresh-interval-select {
  width: 130px;
}

.stat-cards {
  margin-bottom: 20px;
}

.monitor-error-alert {
  margin-bottom: 20px;
}

.monitor-error-text {
  margin-right: 8px;
}

/* 接口未成功：不再存在"健康色"，统计卡一律降为中性色 */
.stat-cards.is-stale .stat-value,
.stat-cards.is-stale .stat-value .online,
.stat-cards.is-stale .stat-value .online-none {
  color: var(--el-text-color-secondary);
}

.detail-error {
  margin-top: 20px;
  padding: 32px 24px;
  text-align: center;
  background: var(--el-fill-color-light);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
}

.detail-error-icon {
  font-size: 40px;
  color: var(--el-color-danger);
}

.detail-error-title {
  margin: 12px 0 4px;
  font-size: 16px;
  font-weight: bold;
  color: var(--el-text-color-primary);
}

.detail-error-desc {
  max-width: 640px;
  margin: 0 auto 16px;
  font-size: 13px;
  line-height: 1.7;
  color: var(--el-text-color-secondary);
}

.detail-skeleton {
  margin-top: 20px;
}

.detail-skeleton .el-col {
  margin-bottom: 16px;
}

.control-alert {
  margin-bottom: 20px;
}

.control-health {
  margin-bottom: 20px;
}

.control-health .card-header {
  justify-content: space-between;
}

.control-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 8px;
}

.control-metric {
  min-width: 0;
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  background: var(--el-fill-color-light);
  border-radius: 8px;
}

.control-metric span {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

.control-metric .term {
  cursor: help;
  text-decoration: underline dotted var(--el-text-color-placeholder);
  text-underline-offset: 3px;
}

.control-metric strong {
  font-size: 22px;
  overflow-wrap: anywhere;
}

.control-metric.attention strong {
  color: var(--el-color-danger);
}

/* 接口未成功：整卡未知态，attention 红字同样降为中性（不能一边「—」一边报警） */
.control-health.is-stale .control-metric.attention strong {
  color: var(--el-text-color-secondary);
}

.stat-card {
  position: relative;
  overflow: hidden;
}

.stat-card .stat-icon {
  position: absolute;
  right: 20px;
  top: 50%;
  transform: translateY(-50%);
  font-size: 48px;
  opacity: 0.3;
}

.stat-card.http .stat-icon { color: var(--el-color-primary); }
.stat-card.device .stat-icon { color: var(--el-color-success); }
.stat-card.collector .stat-icon { color: var(--el-color-warning); }
.stat-card.data .stat-icon { color: var(--el-text-color-secondary); }

.stat-card :deep(.el-card__body) {
  display: flex;
  align-items: center;
  padding: 20px;
}

.stat-content {
  flex: 1;
}

.stat-value {
  font-size: 28px;
  font-weight: bold;
  color: var(--el-text-color-primary);
}

.stat-value .online {
  color: var(--el-color-success);
}

.stat-value .online-none {
  color: var(--el-color-danger);
}

.stat-value .separator {
  margin: 0 4px;
  color: var(--el-text-color-secondary);
}

.stat-label {
  font-size: 14px;
  color: var(--el-text-color-secondary);
  margin-top: 4px;
}

/**
 * 范围词（§4.3 统计卡 MUST）。
 * 移动端 2×2 栅格下卡宽仅约 165px，范围词因此另起一行并禁止逐字断行（§4.2.5）。
 */
.stat-scope {
  display: inline-block;
  margin-left: 6px;
  padding: 0 6px;
  border-radius: 8px;
  font-size: 11px;
  line-height: 16px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color);
  white-space: nowrap;
  word-break: keep-all;
  vertical-align: 1px;
}

.card-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: bold;
}

.status-bars {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.status-item {
  display: flex;
  align-items: center;
  gap: 12px;
}

.status-label {
  width: 40px;
  font-size: 14px;
  color: var(--el-text-color-regular);
}

.status-item .el-progress {
  flex: 1;
}

.error-text {
  color: var(--el-color-danger);
  font-weight: bold;
}

.footer-info {
  margin-top: 20px;
  text-align: center;
  color: var(--el-text-color-secondary);
  font-size: 14px;
}

.footer-stale {
  color: var(--el-color-danger);
}

@media (max-width: 900px) {
  .control-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 768px) {
  .toolbar {
    flex-direction: column;
    align-items: stretch;
    gap: 12px;
  }
  .toolbar-actions {
    flex-wrap: wrap;
  }
  .toolbar-actions .el-select {
    flex: 1;
  }
  .stat-card :deep(.el-card__body) {
    padding: 16px;
  }
  .stat-card .stat-icon {
    font-size: 36px;
    right: 12px;
  }
  .stat-value {
    font-size: 22px;
  }
  .stat-cards .el-col {
    margin-bottom: 12px;
  }
  /* 窄卡里范围词换行显示，避免与标签挤在同一行被压成逐字竖排 */
  .stat-scope {
    margin-left: 0;
    margin-top: 2px;
  }
}

@media (max-width: 480px) {
  .toolbar-actions {
    flex-direction: column;
    align-items: stretch;
  }
  .toolbar-actions :deep(.el-select),
  .toolbar-actions :deep(.el-button) {
    width: 100%;
  }
  .toolbar h2 {
    font-size: 20px;
  }
  .stat-card :deep(.el-card__body) {
    padding: 16px;
  }
  .stat-value {
    font-size: 24px;
  }
  .stat-icon {
    font-size: 36px;
  }
  .status-label {
    width: 36px;
    font-size: 13px;
  }
}
</style>
