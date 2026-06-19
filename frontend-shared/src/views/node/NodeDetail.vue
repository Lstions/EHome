<template>
  <div class="collector-detail">
    <PageHeader title="节点详情" :show-back="true" @back="goBack">
      <template #extra>
        <el-button
          :icon="RefreshRight"
          @click="handleSyncConfig"
          :loading="syncingConfig"
          :disabled="!collector || collector.status === 'offline'"
        >
          同步配置
        </el-button>
        <el-button
          type="primary"
          :icon="Upload"
          @click="showOTADialog = true"
          :disabled="!collector || collector.status === 'offline'"
        >
          OTA 升级
        </el-button>
        <el-button
          type="primary"
          :icon="Connection"
          :loading="pinging"
          :disabled="!collector || collector.status === 'offline'"
          @click="handlePing"
        >
          测延迟
        </el-button>
      </template>
    </PageHeader>

    <el-card v-if="loading && !collector">
      <el-skeleton :rows="5" animated />
    </el-card>
    <el-card v-else-if="collector">
      <template #header>
        <span>基本信息</span>
      </template>

      <el-descriptions :column="2" border>
        <el-descriptions-item label="节点名称">
          {{ collector.name }}
        </el-descriptions-item>
        <el-descriptions-item label="设备ID">
          {{ collector.node_id }}
        </el-descriptions-item>
        <el-descriptions-item label="型号">
          {{ collector.model }}
        </el-descriptions-item>
        <el-descriptions-item label="固件版本">
          <el-tag type="info">{{ collector.firmware_version || '-' }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="状态">
          <StatusBadge :status="collector.status" />
        </el-descriptions-item>
        <el-descriptions-item label="连接质量">
          <span v-if="collector.status !== 'online'">-</span>
          <el-progress
            v-else-if="collector.connection_quality !== undefined"
            :percentage="collector.connection_quality"
            :color="getQualityColor(collector.connection_quality)"
            :stroke-width="8"
            style="width: 120px;"
          />
          <span v-else>-</span>
        </el-descriptions-item>
        <el-descriptions-item label="延迟">
          <span v-if="collector.status === 'online' && collector.ping_latency_ms !== undefined && collector.ping_latency_ms > 0"
                :style="{ color: getLatencyColor(collector.ping_latency_ms) }">
            {{ collector.ping_latency_ms }} ms
          </span>
          <span v-else>-</span>
        </el-descriptions-item>
        <el-descriptions-item label="最后在线时间">
          {{ formatTime(collector.last_online_time) }}
        </el-descriptions-item>
        <el-descriptions-item label="在线时长">
          {{ formatOnlineDuration(collector.online_duration) }}
        </el-descriptions-item>
      </el-descriptions>
    </el-card>

    <!-- 配置同步状态 -->
    <el-card v-if="collector" class="config-sync-state" style="margin-top: 20px;">
      <template #header>
        <span>配置同步状态 <el-tag :type="syncStateTagType" size="small">{{ syncStateLabel }}</el-tag></span>
      </template>
      <el-descriptions :column="2" border>
        <el-descriptions-item label="协议版本">
          {{ collector.protocol_version || '2.0' }}
        </el-descriptions-item>
        <el-descriptions-item label="Config Epoch">
          <span :class="{ 'epoch-lag': isEpochLagging }">
            {{ collector.config_epoch ?? 0 }}
          </span>
          <span v-if="isEpochLagging" class="lag-indicator">
            <el-icon><Warning /></el-icon> 落后
          </span>
        </el-descriptions-item>
        <el-descriptions-item label="Last Manifest">
          <code>{{ collector.last_manifest_id || '—' }}</code>
        </el-descriptions-item>
        <el-descriptions-item label="Last Sync">
          {{ formatTime(collector.last_sync_at) }}
        </el-descriptions-item>
        <el-descriptions-item v-if="collector.last_sync_id" label="Last Sync ID" :span="2">
          <code>{{ collector.last_sync_id }}</code>
        </el-descriptions-item>
      </el-descriptions>
    </el-card>

    <el-card v-if="loading && !collector" style="margin-top: 20px;">
      <template #header>
        <span>总线配置</span>
      </template>
      <el-skeleton :rows="6" animated />
    </el-card>
    <el-card v-else style="margin-top: 20px;">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>总线配置</span>
        </div>
      </template>

      <!-- 总线配置面板 -->
      <ChannelPanel ref="busConfigPanelRef" :collector-id="collectorId" :node-device-id="collector?.node_id" :collector-status="collector?.status" :dma-channels="dmaChannels" />
    </el-card>

    <!-- DMA 通道 -->
    <el-card v-if="collector" style="margin-top: 20px;">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>DMA 通道</span>
          <el-button size="small" @click="loadDmaChannels" :loading="dmaLoading">
            <el-icon><Refresh /></el-icon>
            刷新
          </el-button>
        </div>
      </template>

      <el-skeleton v-if="dmaLoading && dmaChannels.length === 0" :rows="3" animated />
      <el-empty v-else-if="dmaChannels.length === 0" description="暂无 DMA 数据" />
      <div v-else class="dma-grid">
        <div
          v-for="dma in dmaChannels"
          :key="dma.dma_id"
          class="dma-card"
          :class="dmaStateClass(dma.state)"
        >
          <div class="dma-header">
            <span class="dma-name">{{ dma.name }}</span>
            <el-tag :type="dmaStateTagType(dma.state)" size="small">
              {{ dmaStateText(dma.state) }}
            </el-tag>
          </div>
          <div class="dma-details">
            <div class="dma-detail-row">
              <span class="dma-label">类型</span>
              <span>{{ dmaTypeText(dma.dma_type) }}</span>
            </div>
            <div class="dma-detail-row">
              <span class="dma-label">能力</span>
              <span>{{ capText(dma.capabilities) }}</span>
            </div>
            <div class="dma-detail-row">
              <span class="dma-label">最大突发</span>
              <span>{{ dma.max_burst }} 字节</span>
            </div>
            <div v-if="dma.bound_to" class="dma-detail-row">
              <span class="dma-label">绑定</span>
              <span class="dma-bound">{{ dma.bound_to }}</span>
            </div>
            <div class="dma-detail-row">
              <span class="dma-label">兼容总线</span>
              <span>{{ busText(dma.compatible_bus) }}</span>
            </div>
          </div>
          <div class="dma-controls" @click.stop>
            <label class="dma-toggle">
              <el-switch
                :model-value="dma.state !== 2"
                :disabled="dma.state === 1"
                :loading="dmaTogglingMap[dma.dma_id] || false"
                @change="toggleDma(dma, $event)"
                active-text="启用"
                inactive-text="禁用"
              />
            </label>
          </div>
        </div>
      </div>
    </el-card>

    <el-card style="margin-top: 20px;">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>关联设备</span>
          <el-button size="small" @click="fetchDevices" :loading="devicesLoading">
            <el-icon><Refresh /></el-icon>
            刷新
          </el-button>
        </div>
      </template>

      <el-skeleton v-if="devicesLoading" :rows="4" animated />
      <template v-else>
        <el-empty v-if="devices.length === 0" description="暂无设备" />
        <el-table v-else :data="devices" stripe @row-click="handleDeviceClick" style="cursor: pointer;">
          <el-table-column prop="name" label="设备名称" min-width="160" />
          <el-table-column label="设备类型" width="150">
            <template #default="{ row }">
              {{ getDeviceTypeText(row.device_type) }}
            </template>
          </el-table-column>
          <el-table-column label="通信通道" width="160">
            <template #default="{ row }">
              <div v-if="getChannelForDevice(row)" class="device-channel-cell" @click="handleEditChannel(getChannelForDevice(row))">
                <el-tag size="default" type="primary" effect="light" class="device-channel-tag">
                  <div class="device-channel-name">{{ getChannelForDevice(row)?.name || '未命名' }}</div>
                  <div class="device-channel-bus">
                    {{ getChannelForDevice(row)?.hardware_type?.toUpperCase() }} {{ getChannelForDevice(row)?.hardware_id }}
                  </div>
                </el-tag>
              </div>
              <span v-else class="device-no-channel">
                <el-tag size="small" type="info" effect="plain">
                  无通道
                </el-tag>
              </span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <StatusBadge :status="row.status" />
            </template>
          </el-table-column>
          <el-table-column label="操作" width="100">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click.stop="handleViewDevice(row)">
                查看
              </el-button>
            </template>
          </el-table-column>
        </el-table>
      </template>
    </el-card>

    <!-- OTA 历史记录 -->
    <el-card style="margin-top: 20px;">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>OTA 升级历史</span>
          <el-button size="small" @click="fetchOTAHistory" :loading="otaHistoryLoading">
            <el-icon><Refresh /></el-icon>
            刷新
          </el-button>
        </div>
      </template>

      <el-skeleton v-if="otaHistoryLoading" :rows="4" animated />
      <template v-else>
        <el-empty v-if="otaHistory.length === 0" description="暂无升级记录" />
        <el-table v-else :data="otaHistory" stripe>
          <el-table-column label="升级版本" width="120">
            <template #default="{ row }">
              {{ row.from_version }} → {{ row.to_version }}
            </template>
          </el-table-column>
          <el-table-column label="状态" width="120">
            <template #default="{ row }">
              <el-tag :type="getOTAStatusType(row.status)">
                {{ getOTAStatusText(row.status) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="进度" width="150">
            <template #default="{ row }">
              <el-progress
                :percentage="row.progress"
                :status="row.status === 'success' ? 'success' : row.status === 'failed' ? 'exception' : ''"
                :stroke-width="6"
              />
            </template>
          </el-table-column>
          <el-table-column label="开始时间" width="180">
            <template #default="{ row }">
              {{ formatTime(row.created_at) }}
            </template>
          </el-table-column>
          <el-table-column label="完成时间" width="180">
            <template #default="{ row }">
              {{ row.completed_at ? formatTime(row.completed_at) : '-' }}
            </template>
          </el-table-column>
          <el-table-column label="操作" width="100">
            <template #default="{ row }">
              <el-button
                v-if="row.status === 'pending' || row.status === 'downloading'"
                type="danger"
                link
                size="small"
                @click="handleCancelOTA(row)"
              >
                取消
              </el-button>
              <span v-else>-</span>
            </template>
          </el-table-column>
        </el-table>
      </template>
    </el-card>

    <!-- OTA 升级对话框 -->
    <OTAForm
      :visible="showOTADialog"
      :collector-id="collectorId"
      :collector-model="collector?.model"
      :current-firmware-version="collector?.firmware_version"
      @success="handleOTASuccess"
      @update:visible="showOTADialog = $event"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Upload, Refresh, RefreshRight, Link, Connection, Warning } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import OTAForm from '@/components/forms/OTAForm.vue'
import ChannelPanel from '@/components/node/ChannelPanel.vue'
import { nodeApi, type OTARecord, type DmaChannelInfo } from '@/api/node'
import { edgeDeviceApi } from '@/api/edgeDevice'
import { channelApi } from '@/api/channel'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { logger } from '@/utils/logger'

const router = useRouter()
const route = useRoute()
const wsStore = useWebSocketStore()

const collector = ref<any>(null)
const devices = ref<any[]>([])
const channels = ref<any[]>([])
const otaHistory = ref<OTARecord[]>([])
const peripherals = ref<Capabilities | null>(null)
const loading = ref(false)
const devicesLoading = ref(true)
const otaHistoryLoading = ref(true)
const syncingConfig = ref(false)
const showOTADialog = ref(false)
const pinging = ref(false)
const busConfigPanelRef = ref<any>(null)
const pendingPingTimeout = ref<ReturnType<typeof setTimeout> | null>(null)

// DMA 通道
const dmaChannels = ref<DmaChannelInfo[]>([])
const dmaLoading = ref(false)

let unsubscribe: (() => void) | null = null

const collectorId = computed(() => route.params.id as string)

// 配置同步状态
const syncStateLabel = computed(() => {
  const s = collector.value?.config_sync_state
  return {
    in_sync: '已同步',
    syncing: '同步中',
    lag: '落后',
    error: '错误',
    unknown: '未知',
  }[s as string] || '未知'
})

const syncStateTagType = computed(() => {
  return {
    in_sync: 'success',
    syncing: 'warning',
    lag: 'danger',
    error: 'danger',
    unknown: 'info',
  }[collector.value?.config_sync_state as string] || 'info'
})

const isEpochLagging = computed(() => {
  // 服务端 epoch 暂未下发，预留接口
  const serverEpoch = 0 // TODO: 从全局 store 获取
  return (collector.value?.config_epoch ?? 0) < serverEpoch
})

const deviceTypeMap: Record<string, string> = {
  wind_speed: '风速传感器',
  wind_direction: '风向传感器',
  rain: '雨量传感器',
  light: '光照传感器',
  temp_humidity: '温湿度传感器',
  battery: '电池保护板',
  inverter: '光伏逆变器'
}

const getDeviceTypeText = (type: string) => {
  return deviceTypeMap[type] || type
}

// 根据设备的 hardware_type + hardware_id 查找对应的 Channel
const getChannelForDevice = (device: any) => {
  return channels.value.find(
    ch => ch.hardware_type === device.hardware_type && ch.hardware_id === device.hardware_id
  )
}

// 点击通道标签 → 通过 ChannelPanel 打开通道编辑对话框
const handleEditChannel = (channel: any) => {
  if (collector.value?.status !== 'online') {
    ElMessage.warning('节点离线，无法编辑通道')
    return
  }
  busConfigPanelRef.value?.handleOpenChannelManager(channel)
}

const getQualityColor = (quality: number) => {
  if (quality >= 80) return '#67c23a'
  if (quality >= 60) return '#e6a23c'
  return '#f56c6c'
}

const getLatencyColor = (ms: number) => {
  if (ms < 50) return '#67c23a'
  if (ms < 200) return '#e6a23c'
  return '#f56c6c'
}

const handlePing = async () => {
  if (!collector.value) return
  pinging.value = true
  try {
    await nodeApi.ping(collector.value.node_id)
    const timeout = setTimeout(() => {
      pinging.value = false
      ElMessage.warning('延迟测量超时，采集器可能离线')
    }, 5000)
    pendingPingTimeout.value = timeout
  } catch (err: any) {
    pinging.value = false
    ElMessage.error('发送 Ping 失败: ' + (err.message || '未知错误'))
  }
}

const fetchCollectorDetail = async () => {
  const id = route.params.id as string
  if (!id) {
    ElMessage.error('无效的节点ID')
    goBack()
    return
  }

  loading.value = true
  try {
    collector.value = await nodeApi.getDetail(id)
    // 自动测量延迟（如果在线且无延迟数据）
    if (collector.value?.status === 'online' && (!collector.value.ping_latency_ms || collector.value.ping_latency_ms <= 0)) {
      handlePing()
    }
  } catch (error: any) {
    ElMessage.error('获取节点详情失败')
  } finally {
    loading.value = false
  }
}

const fetchDevices = async () => {
  const id = route.params.id as string
  if (!id) return

  devicesLoading.value = true
  try {
    const [deviceRes, channelRes] = await Promise.all([
      edgeDeviceApi.getList({ collector_id: id, page: 1, page_size: 100 }),
      channelApi.getList(id)
    ])
    devices.value = deviceRes?.items || []
    // 统一为数组
    channels.value = Array.isArray(channelRes)
      ? channelRes
      : (channelRes?.items || [])
  } catch (error: any) {
    logger.error('获取设备列表失败', { error: String(error) })
    ElMessage.error('获取设备列表失败')
  } finally {
    devicesLoading.value = false
  }
}

const handleOTASuccess = () => {
  ElMessage.success('OTA 升级已完成')
  showOTADialog.value = false
  // 刷新节点详情以更新固件版本
  fetchCollectorDetail()
  fetchOTAHistory()
}

const fetchOTAHistory = async () => {
  const id = route.params.id as string
  if (!id) return

  otaHistoryLoading.value = true
  try {
    otaHistory.value = (await nodeApi.getOTAHistory(id)) || []
  } catch (error: any) {
    logger.error('获取OTA历史失败', { error: String(error) })
  } finally {
    otaHistoryLoading.value = false
  }
}

const handleSyncConfig = async () => {
  const id = route.params.id as string
  if (!id) return

  syncingConfig.value = true
  try {
    await nodeApi.syncConfig(id)
    ElMessage.success('配置同步成功')
  } catch (error: any) {
    ElMessage.error('配置同步失败: ' + (error.message || '未知错误'))
  } finally {
    syncingConfig.value = false
  }
}

const handleCancelOTA = async (record: OTARecord) => {
  try {
    await ElMessageBox.confirm('确定要取消此OTA升级吗？', '提示', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning'
    })

    await nodeApi.cancelOTA(collectorId.value, record.id)
    ElMessage.success('已取消OTA升级')
    fetchOTAHistory()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error('取消OTA失败')
    }
  }
}

const getOTAStatusType = (status: string) => {
  const types: Record<string, string> = {
    pending: 'info',
    downloading: 'warning',
    installing: 'warning',
    success: 'success',
    failed: 'danger',
    cancelled: 'info'
  }
  return types[status] || 'info'
}

const getOTAStatusText = (status: string) => {
  const texts: Record<string, string> = {
    pending: '等待中',
    downloading: '下载中',
    installing: '安装中',
    success: '成功',
    failed: '失败',
    cancelled: '已取消'
  }
  return texts[status] || status
}

const handleViewDevice = (device: any) => {
  router.push(`/edge-device/${device.id}`)
}

const handleDeviceClick = (row: any) => {
  handleViewDevice(row)
}

const formatTime = (time: string | null | undefined) => {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return '-'
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return '-'
  return date.toLocaleString('zh-CN')
}

const formatOnlineDuration = (seconds: number) => {
  if (!seconds) return '-'

  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)

  const parts = []
  if (days > 0) parts.push(`${days}天`)
  if (hours > 0) parts.push(`${hours}小时`)
  if (minutes > 0) parts.push(`${minutes}分钟`)

  return parts.length > 0 ? parts.join(' ') : '0分钟'
}

const goBack = () => {
  router.back()
}

// 检查是否有外设
const hasPeripherals = computed(() => {
  if (!peripherals.value?.hardware) return false
  const hardware = peripherals.value.hardware
  return (hardware.uart?.length || 0) > 0 ||
         (hardware.i2c?.length || 0) > 0 ||
         (hardware.spi?.length || 0) > 0
})

// ============================================================
// DMA 辅助函数
// ============================================================

const dmaStateText = (state: number): string => {
  switch (state) {
    case 0: return '空闲'
    case 1: return '已分配'
    case 2: return '已禁用'
    default: return '未知'
  }
}

const dmaStateClass = (state: number): string => {
  switch (state) {
    case 0: return 'dma-state-free'
    case 1: return 'dma-state-allocated'
    case 2: return 'dma-state-disabled'
    default: return ''
  }
}

const dmaStateTagType = (state: number): string => {
  switch (state) {
    case 0: return 'info'
    case 1: return 'success'
    case 2: return 'danger'
    default: return 'info'
  }
}

const dmaTypeText = (type: number): string => {
  return type === 0 ? 'GDMA' : `类型${type}`
}

const capText = (cap: number): string => {
  const parts: string[] = []
  if (cap & 1) parts.push('TX')
  if (cap & 2) parts.push('RX')
  if (cap & 4) parts.push('Burst')
  return parts.join(', ') || '无'
}

const busText = (bus: number): string => {
  const parts: string[] = []
  if (bus & 1) parts.push('UART')
  if (bus & 2) parts.push('I2C')
  if (bus & 4) parts.push('SPI')
  return parts.join(', ') || '无'
}

const loadDmaChannels = async () => {
  const id = collectorId.value
  if (!id) return
  dmaLoading.value = true
  try {
    dmaChannels.value = await nodeApi.getDmaChannels(id)
  } catch (error: any) {
    logger.error('获取 DMA 通道失败', { error: String(error) })
  } finally {
    dmaLoading.value = false
  }
}

// DMA 开关 loading 状态（按 dma_id 跟踪）
const dmaTogglingMap = ref<Record<number, boolean>>({})

const toggleDma = async (dma: DmaChannelInfo, enabled: boolean) => {
  dmaTogglingMap.value[dma.dma_id] = true
  try {
    await nodeApi.updateDmaConfig(collectorId.value, [{
      dma_id: dma.dma_id,
      enabled: enabled,
      bind_to: dma.bound_to
    }])
    await loadDmaChannels()
    ElMessage.success(enabled ? `已启用 ${dma.name}` : `已禁用 ${dma.name}`)
  } catch (error: any) {
    ElMessage.error('操作失败: ' + (error.message || '未知错误'))
  } finally {
    dmaTogglingMap.value[dma.dma_id] = false
  }
}

// 采集器上线时刷新通道列表
watch(() => collector.value?.status, (newStatus, oldStatus) => {
  if (oldStatus === 'offline' && newStatus === 'online') {
    // 采集器上线，刷新总线配置和通道列表
    busConfigPanelRef.value?.refreshChannels?.()
    busConfigPanelRef.value?.refreshBuses?.()
  }
})

onMounted(() => {
  fetchCollectorDetail()
  fetchDevices()
  fetchOTAHistory()
  loadDmaChannels()

  // 订阅状态更新
  unsubscribe = wsStore.subscribe(WS_EVENT.NODE_STATUS, (message: WebSocketMessage) => {
    if (message.payload?.node_id === collectorId.value) {
      if (message.payload?.latency_ms !== undefined) {
        collector.value = { ...collector.value, latency_ms: message.payload.latency_ms }
        pinging.value = false
        if (pendingPingTimeout.value) {
          clearTimeout(pendingPingTimeout.value)
          pendingPingTimeout.value = null
        }
        ElMessage.success(`延迟: ${message.payload.latency_ms} ms`)
      } else {
        fetchCollectorDetail()
      }
    }
  })
})

onUnmounted(() => {
  if (unsubscribe) {
    unsubscribe()
  }
  if (pendingPingTimeout.value) {
    clearTimeout(pendingPingTimeout.value)
  }
})
</script>

<style scoped>
.collector-detail {
  padding: 0;
}

.peripheral-section {
  margin-bottom: 24px;
}

.peripheral-section:last-child {
  margin-bottom: 0;
}

.section-title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  font-size: 14px;
  font-weight: 500;
  color: #606266;
}

.peripheral-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.peripheral-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  background: #f5f7fa;
  border-radius: 6px;
  border: 1px solid #e4e7ed;
  transition: all 0.2s;
}

.peripheral-item:hover {
  border-color: #409eff;
}

.peripheral-item.assigned {
  background: #f0f9eb;
  border-color: #c2e7b0;
}

.peripheral-info {
  display: flex;
  align-items: center;
  gap: 8px;
}

.peripheral-id {
  font-weight: 500;
  color: #303133;
}

.peripheral-status {
  display: flex;
  align-items: center;
  gap: 12px;
}

.assigned-info {
  display: flex;
  align-items: center;
  gap: 6px;
  color: #67c23a;
  font-size: 13px;
}

.capability-item {
  padding: 8px 0;
  display: flex;
  align-items: center;
}

.capability-item:not(:last-child) {
  border-bottom: 1px solid #f0f0f0;
}

:deep(.el-collapse-item__header) {
  font-weight: 500;
}

/* 关联设备表格中的通道标签 */
.device-channel-cell {
  display: inline-flex;
  cursor: pointer;
}

.device-channel-tag {
  border-radius: 8px;
  border-color: #409eff;
  background: #ecf5ff;
  color: #1d60d6;
  padding: 5px 10px;
  cursor: pointer;
  transition: background 0.2s;
  text-align: center;
  min-width: 130px;
}

.device-channel-tag:hover {
  background: #d9ecff;
}

.device-channel-name {
  font-size: 13px;
  font-weight: 600;
  line-height: 1.4;
  display: block;
}

.device-channel-bus {
  font-size: 11px;
  font-weight: 500;
  opacity: 0.75;
  font-family: 'Courier New', monospace;
  display: block;
  margin-top: 1px;
}

.device-no-channel {
  font-size: 12px;
}

/* 配置同步状态卡 */
.config-sync-state {
  .epoch-lag {
    color: #f56c6c;
    font-weight: 600;
  }

  .lag-indicator {
    color: #f56c6c;
    font-size: 13px;
    margin-left: 6px;
    display: inline-flex;
    align-items: center;
    gap: 2px;
  }

  code {
    background: #f5f7fa;
    padding: 2px 6px;
    border-radius: 3px;
    font-size: 12px;
    font-family: 'Courier New', Courier, monospace;
  }
}

/* DMA 通道卡片 */
.dma-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 16px;
}

.dma-card {
  padding: 16px;
  background: #f5f7fa;
  border-radius: 8px;
  border: 1px solid #e4e7ed;
  transition: border-color 0.2s;
}

.dma-card:hover {
  border-color: #409eff;
}

.dma-card.dma-state-allocated {
  background: #f0f9eb;
  border-color: #c2e7b0;
}

.dma-card.dma-state-disabled {
  background: #fef0f0;
  border-color: #fbc4c4;
}

.dma-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.dma-name {
  font-weight: 600;
  font-size: 15px;
  color: #303133;
  font-family: 'Courier New', Courier, monospace;
}

.dma-details {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 12px;
}

.dma-detail-row {
  display: flex;
  align-items: center;
  font-size: 13px;
  color: #606266;
}

.dma-label {
  width: 70px;
  flex-shrink: 0;
  color: #909399;
  font-size: 12px;
}

.dma-bound {
  color: #67c23a;
  font-weight: 500;
}

.dma-controls {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  padding-top: 8px;
  border-top: 1px solid #ebeef5;
}

.dma-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
</style>
