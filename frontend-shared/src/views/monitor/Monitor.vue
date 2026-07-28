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

    <!-- 统计卡片 -->
    <div class="stat-cards">
      <el-row :gutter="16">
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card http">
            <div class="stat-icon"><el-icon><Connection /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">{{ formatNumber(metrics?.http?.requests_total || 0) }}</div>
              <div class="stat-label">HTTP 请求总数</div>
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
              <div class="stat-label">设备在线状态</div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card collector">
            <div class="stat-icon"><el-icon><Cpu /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">
                <span :class="onlineValueClass(metrics?.collector?.online, collectorTotal)">{{ metrics?.collector?.online || 0 }}</span>
                <span class="separator">/</span>
                <span>{{ collectorTotal }}</span>
              </div>
              <div class="stat-label">采集器在线状态</div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card data">
            <div class="stat-icon"><el-icon><DataLine /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">{{ formatNumber(metrics?.data?.points_collected || 0) }}</div>
              <div class="stat-label">数据点采集总数</div>
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
            <el-descriptions :column="2" border>
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
            <el-descriptions :column="2" border>
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
                  :color="THEME_COLORS.success"
                >
                  <span>{{ metrics?.device?.online || 0 }}</span>
                </el-progress>
              </div>
              <div class="status-item">
                <span class="status-label">离线</span>
                <el-progress
                  :percentage="deviceOfflinePercent"
                  :stroke-width="20"
                  :color="THEME_COLORS.danger"
                >
                  <span>{{ metrics?.device?.offline || 0 }}</span>
                </el-progress>
              </div>
            </div>
          </el-card>
        </el-col>

        <!-- 采集器状态 -->
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header>
              <div class="card-header">
                <span><el-icon><Cpu /></el-icon> 采集器状态</span>
              </div>
            </template>
            <div class="status-bars">
              <div class="status-item">
                <span class="status-label">在线</span>
                <el-progress 
                  :percentage="collectorOnlinePercent" 
                  :stroke-width="20"
                  :color="THEME_COLORS.success"
                >
                  <span>{{ metrics?.collector?.online || 0 }}</span>
                </el-progress>
              </div>
              <div class="status-item">
                <span class="status-label">离线</span>
                <el-progress
                  :percentage="collectorOfflinePercent"
                  :stroke-width="20"
                  :color="THEME_COLORS.danger"
                >
                  <span>{{ metrics?.collector?.offline || 0 }}</span>
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
            <el-descriptions :column="2" border>
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
            <el-descriptions :column="2" border>
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
import { THEME_COLORS } from '@/utils/theme'

// 状态
const metrics = ref<MetricsSummary | null>(null)
const refreshInterval = ref(10000)
const lastUpdateTime = ref('--')
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

const collectorTotal = computed(() => {
  return (metrics.value?.collector?.online || 0) + (metrics.value?.collector?.offline || 0)
})

const collectorOnlinePercent = computed(() => {
  if (collectorTotal.value === 0) return 0
  return Math.round(((metrics.value?.collector?.online || 0) / collectorTotal.value) * 100)
})

const collectorOfflinePercent = computed(() => {
  if (collectorTotal.value === 0) return 0
  return Math.round(((metrics.value?.collector?.offline || 0) / collectorTotal.value) * 100)
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
}
</style>
