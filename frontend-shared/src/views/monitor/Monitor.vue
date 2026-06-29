<template>
  <div class="monitor-container">
    <!-- 顶部操作栏 -->
    <div class="toolbar">
      <h2>📊 系统监控</h2>
      <div class="toolbar-actions">
        <el-select v-model="refreshInterval" placeholder="刷新间隔" size="default">
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
        <el-col :span="6">
          <el-card shadow="hover" class="stat-card http">
            <div class="stat-icon"><el-icon><Connection /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">{{ formatNumber(metrics?.http?.requests_total || 0) }}</div>
              <div class="stat-label">HTTP 请求总数</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover" class="stat-card device">
            <div class="stat-icon"><el-icon><Monitor /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">
                <span class="online">{{ metrics?.device?.online || 0 }}</span>
                <span class="separator">/</span>
                <span>{{ (metrics?.device?.online || 0) + (metrics?.device?.offline || 0) }}</span>
              </div>
              <div class="stat-label">设备在线状态</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover" class="stat-card collector">
            <div class="stat-icon"><el-icon><Cpu /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">
                <span class="online">{{ metrics?.collector?.online || 0 }}</span>
                <span class="separator">/</span>
                <span>{{ (metrics?.collector?.online || 0) + (metrics?.collector?.offline || 0) }}</span>
              </div>
              <div class="stat-label">采集器在线状态</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
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

    <!-- 详细面板 -->
    <div class="detail-panels">
      <el-row :gutter="16">
        <!-- HTTP 监控 -->
        <el-col :span="12">
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
        <el-col :span="12">
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

      <el-row :gutter="16" style="margin-top: 16px;">
        <!-- 设备状态 -->
        <el-col :span="12">
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
                  :color="'var(--el-color-success)'"
                >
                  <span>{{ metrics?.device?.online || 0 }}</span>
                </el-progress>
              </div>
              <div class="status-item">
                <span class="status-label">离线</span>
                <el-progress 
                  :percentage="deviceOfflinePercent" 
                  :stroke-width="20"
                  :color="'var(--el-color-danger)'"
                >
                  <span>{{ metrics?.device?.offline || 0 }}</span>
                </el-progress>
              </div>
            </div>
          </el-card>
        </el-col>

        <!-- 采集器状态 -->
        <el-col :span="12">
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
                  :color="'var(--el-color-success)'"
                >
                  <span>{{ metrics?.collector?.online || 0 }}</span>
                </el-progress>
              </div>
              <div class="status-item">
                <span class="status-label">离线</span>
                <el-progress 
                  :percentage="collectorOfflinePercent" 
                  :stroke-width="20"
                  :color="'var(--el-color-danger)'"
                >
                  <span>{{ metrics?.collector?.offline || 0 }}</span>
                </el-progress>
              </div>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <el-row :gutter="16" style="margin-top: 16px;">
        <!-- 数据采集 -->
        <el-col :span="12">
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
        <el-col :span="12">
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
  Connection, Monitor, Cpu, DataLine, Promotion, Refresh 
} from '@element-plus/icons-vue'
import { getMetricsSummary, type MetricsSummary } from '@/api/monitor'

// 状态
const metrics = ref<MetricsSummary | null>(null)
const refreshInterval = ref(10000)
const lastUpdateTime = ref('--')
let timer: ReturnType<typeof setInterval> | null = null

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
  margin: 0;
  font-size: 24px;
}

.toolbar-actions {
  display: flex;
  gap: 12px;
}

.stat-cards {
  margin-bottom: 20px;
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
  color: #303133;
}

.stat-value .online {
  color: var(--el-color-success);
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
</style>
