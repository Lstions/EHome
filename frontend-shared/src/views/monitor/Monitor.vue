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

    <!-- 统计卡片
         规范 §4.3 统计卡 MUST「统计值必须标明范围」。
         本页四个 KPI 取自两种不同的统计口径，必须逐个标注，否则用户会把
         「进程启动以来的计数器」误读成「库里累计的数据量」：
           · HTTP 请求总数 / 数据点采集总数 —— Prometheus Counter，**进程启动以来**的累计值，
             服务重启即归零（后代 handler_metrics.go:82/106 readCounterTotal）
           · 设备/节点在线状态 —— 直接 COUNT 全表，**全局**口径，与页面筛选无关 -->
    <div class="stat-cards">
      <el-row :gutter="16">
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card http">
            <div class="stat-icon"><el-icon><Connection /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">{{ formatNumber(metrics?.http?.requests_total || 0) }}</div>
              <div class="stat-label">HTTP 请求总数<span class="stat-scope">{{ SCOPE_PROCESS }}</span></div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card device">
            <div class="stat-icon"><el-icon><Monitor /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">
                <span :class="onlineValueClass(metrics?.device?.online, deviceTotal)">{{ metrics?.device?.online || 0 }}</span>
                <span class="separator">/</span>
                <span>{{ deviceTotal }}</span>
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
                <span :class="onlineValueClass(metrics?.node?.online, nodeTotal)">{{ metrics?.node?.online || 0 }}</span>
                <span class="separator">/</span>
                <span>{{ nodeTotal }}</span>
              </div>
              <div class="stat-label">节点在线状态<span class="stat-scope">{{ SCOPE_GLOBAL }}</span></div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card data">
            <div class="stat-icon"><el-icon><DataLine /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">{{ formatNumber(metrics?.data?.points_collected || 0) }}</div>
              <div class="stat-label">数据点采集总数<span class="stat-scope">{{ SCOPE_PROCESS }}</span></div>
            </div>
          </el-card>
        </el-col>
      </el-row>
    </div>

    <el-alert
      v-if="controlAttention > 0"
      class="control-alert"
      type="warning"
      show-icon
      :closable="false"
      :title="`控制面有 ${controlAttention} 项需要关注`"
    />

    <!-- 详细面板 -->
    <div class="detail-panels">
      <el-row class="control-health">
        <el-col :span="24">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Operation /></el-icon> 控制面健康</span>
                <el-tag :type="controlAttention > 0 ? 'warning' : 'success'" size="small">
                  {{ controlAttention > 0 ? '需关注' : '正常' }}
                </el-tag>
              </div>
            </template>
            <div class="control-grid">
              <div class="control-metric"><span>操作总数</span><strong>{{ formatNumber(metrics?.control?.operations_total || 0) }}</strong></div>
              <div class="control-metric"><span>活跃操作</span><strong>{{ metrics?.control?.active || 0 }}</strong></div>
              <div class="control-metric">
                <el-tooltip content="Outbox 待处理：已写入待发送队列、等待投递的控制消息数" placement="top">
                  <span class="term">Outbox 待处理</span>
                </el-tooltip>
                <strong>{{ metrics?.control?.outbox_pending || 0 }}</strong>
              </div>
              <div class="control-metric">
                <el-tooltip content="Outbox 租约中：已被投递任务领取（租约锁定）、正在发送中的消息数" placement="top">
                  <span class="term">Outbox 租约中</span>
                </el-tooltip>
                <strong>{{ metrics?.control?.outbox_leased || 0 }}</strong>
              </div>
              <div class="control-metric" :class="{ attention: (metrics?.control?.unresolved_unknown || 0) > 0 }">
                <el-tooltip content="执行结果未知（UNKNOWN）且尚未人工处置的操作数" placement="top">
                  <span class="term">未处置 UNKNOWN</span>
                </el-tooltip>
                <strong>{{ metrics?.control?.unresolved_unknown || 0 }}</strong>
              </div>
              <div class="control-metric" :class="{ attention: (metrics?.control?.capability_stale_nodes || 0) > 0 }">
                <el-tooltip content="能力快照超过有效期未上报的节点数" placement="top">
                  <span class="term">能力快照过期</span>
                </el-tooltip>
                <strong>{{ metrics?.control?.capability_stale_nodes || 0 }}</strong>
              </div>
              <div class="control-metric" :class="{ attention: (metrics?.control?.audit_write_failures || 0) > 0 }">
                <el-tooltip content="审计日志写入失败的次数" placement="top">
                  <span class="term">审计写失败</span>
                </el-tooltip>
                <strong>{{ metrics?.control?.audit_write_failures || 0 }}</strong>
              </div>
              <div class="control-metric"><span>操作成功</span><strong>{{ metrics?.control?.succeeded || 0 }}</strong></div>
              <div class="control-metric" :class="{ attention: (metrics?.control?.failed || 0) > 0 }"><span>操作失败</span><strong>{{ metrics?.control?.failed || 0 }}</strong></div>
            </div>
          </el-card>
        </el-col>
      </el-row>

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
                {{ formatNumber(metrics?.http?.requests_total || 0) }}
              </el-descriptions-item>
              <el-descriptions-item label="处理中请求">
                {{ metrics?.http?.requests_in_flight || 0 }}
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
                {{ formatNumber(metrics?.mqtt?.messages_received || 0) }}
              </el-descriptions-item>
              <el-descriptions-item label="发送消息">
                {{ formatNumber(metrics?.mqtt?.messages_sent || 0) }}
              </el-descriptions-item>
              <el-descriptions-item label="连接错误">
                <span :class="{ 'error-text': (metrics?.mqtt?.connection_errors || 0) > 0 }">
                  {{ metrics?.mqtt?.connection_errors || 0 }}
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
                  <span>{{ metrics?.device?.online || 0 }}</span>
                </el-progress>
              </div>
              <div class="status-item">
                <span class="status-label">离线</span>
                <el-progress
                  :percentage="deviceOfflinePercent"
                  :stroke-width="20"
                  :color="'var(--color-danger)'"
                >
                  <span>{{ metrics?.device?.offline || 0 }}</span>
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
                  <span>{{ metrics?.node?.online || 0 }}</span>
                </el-progress>
              </div>
              <div class="status-item">
                <span class="status-label">离线</span>
                <el-progress
                  :percentage="nodeOfflinePercent"
                  :stroke-width="20"
                  :color="'var(--color-danger)'"
                >
                  <span>{{ metrics?.node?.offline || 0 }}</span>
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
                {{ formatNumber(metrics?.data?.points_collected || 0) }}
              </el-descriptions-item>
              <el-descriptions-item label="已存储">
                {{ formatNumber(metrics?.data?.points_stored || 0) }}
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
                {{ metrics?.websocket?.connections_active || 0 }}
              </el-descriptions-item>
              <el-descriptions-item label="消息总数">
                {{ formatNumber(metrics?.websocket?.messages_total || 0) }}
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>
      </el-row>
    </div>

    <!-- 更新时间 -->
    <div class="footer-info">
      <span>最后更新: {{ lastUpdateTime }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { 
  Connection, Monitor, Cpu, DataLine, Promotion, Refresh, DataAnalysis, Operation
} from '@element-plus/icons-vue'
import { getMetricsSummary, type MetricsSummary } from '@/api/monitor'
import { useResponsive } from '@/composables/useResponsive'
import { UNKNOWN } from '@/utils/format'

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
const metrics = ref<MetricsSummary | null>(null)
const refreshInterval = ref(10000)
const lastUpdateTime = ref(UNKNOWN)
let timer: ReturnType<typeof setInterval> | null = null

const controlAttention = computed(() =>
  (metrics.value?.control?.unresolved_unknown || 0) +
  (metrics.value?.control?.capability_stale_nodes || 0) +
  (metrics.value?.control?.audit_write_failures || 0)
)

// 计算属性
const deviceTotal = computed(() => {
  return (metrics.value?.device?.online || 0) + (metrics.value?.device?.offline || 0)
})

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

const fetchMetrics = async () => {
  try {
    const res = await getMetricsSummary()
    if (res.code === 200 && res.data) {
      metrics.value = res.data
      lastUpdateTime.value = new Date().toLocaleString('zh-CN')
    }
  } catch (error) {
    console.error('获取指标失败:', error)
    ElMessage.error('获取监控数据失败')
  }
}

const formatNumber = (num: number): string => {
  if (num >= 1000000) {
    return (num / 1000000).toFixed(2) + 'M'
  }
  if (num >= 1000) {
    return (num / 1000).toFixed(2) + 'K'
  }
  return num.toString()
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
