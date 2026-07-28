<template>
  <div class="collector-detail">
    <el-breadcrumb separator="/" class="node-breadcrumb">
      <el-breadcrumb-item :to="{ path: '/dashboard' }">首页</el-breadcrumb-item>
      <el-breadcrumb-item :to="{ path: '/node' }">节点管理</el-breadcrumb-item>
      <el-breadcrumb-item>{{ nodeDisplayName }}</el-breadcrumb-item>
    </el-breadcrumb>
    <PageHeader :title="pageTitle" :show-back="true" @back="goBack">
      <template #extra>
        <el-tooltip content="设备离线，无法操作" placement="top" :disabled="!collectorOffline">
          <span>
            <el-button
              :icon="RefreshRight"
              :size="isMobile ? 'small' : 'default'"
              @click="handleSyncConfig"
              :loading="syncingConfig"
              :disabled="collectorOffline"
            >
              同步配置
            </el-button>
          </span>
        </el-tooltip>
        <el-tooltip content="设备离线，无法操作" placement="top" :disabled="!collectorOffline">
          <span>
            <el-button
              type="primary"
              :icon="Upload"
              :size="isMobile ? 'small' : 'default'"
              @click="showOTADialog = true"
              :disabled="collectorOffline"
            >
              OTA 升级
            </el-button>
          </span>
        </el-tooltip>
        <el-tooltip content="设备离线，无法操作" placement="top" :disabled="!collectorOffline">
          <span>
            <el-button
              type="primary"
              :icon="Connection"
              :size="isMobile ? 'small' : 'default'"
              :loading="pinging"
              :disabled="collectorOffline"
              @click="handlePing"
            >
              测延迟
            </el-button>
          </span>
        </el-tooltip>
      </template>
    </PageHeader>

    <el-card v-if="loading && !collector" shadow="hover">
      <el-skeleton :rows="5" animated />
    </el-card>
    <el-card v-else-if="collector" shadow="hover">
      <template #header>
        <span>基本信息</span>
      </template>

      <el-descriptions :column="descColumns" border>
        <el-descriptions-item label="节点名称">
          <div class="editable-name">
            <span v-if="!editingName">{{ collector.name || '(未命名)' }}</span>
            <el-input
              v-else
              v-model="editNameValue"
              size="small"
              style="width: 180px;"
              @keyup.enter="saveName"
              @keyup.escape="cancelEditName"
            />
            <el-icon v-if="!editingName" class="edit-icon" @click="startEditName"><Edit /></el-icon>
            <el-button v-else type="primary" size="small" text @click="saveName">保存</el-button>
            <el-button v-if="editingName" size="small" text @click="cancelEditName">取消</el-button>
          </div>
        </el-descriptions-item>
        <el-descriptions-item label="设备ID">
          {{ collector.node_id }}
        </el-descriptions-item>
        <el-descriptions-item label="型号">
          {{ collector.model }}
        </el-descriptions-item>
        <el-descriptions-item label="固件版本">
          <el-tag type="info" size="small">{{ collector.firmware_version || '-' }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="状态">
          <StatusBadge :status="collector.status" />
        </el-descriptions-item>
        <el-descriptions-item label="连接质量">
          <el-tooltip v-if="collector.status !== 'online'" content="设备离线，暂无数据" placement="top">
            <span class="field-na">-</span>
          </el-tooltip>
          <el-progress
            v-else-if="collector.connection_quality !== undefined"
            :percentage="collector.connection_quality"
            :color="getQualityColor(collector.connection_quality)"
            :stroke-width="6"
            style="width: 120px;"
          />
          <span v-else>-</span>
        </el-descriptions-item>
        <el-descriptions-item label="延迟">
          <span v-if="collector.status === 'online' && collector.ping_latency_ms !== undefined && collector.ping_latency_ms > 0"
                :style="{ color: getLatencyColor(collector.ping_latency_ms) }">
            {{ collector.ping_latency_ms }} ms
          </span>
          <el-tooltip v-else-if="collector.status === 'offline'" content="设备离线，暂无数据" placement="top">
            <span class="field-na">-</span>
          </el-tooltip>
          <span v-else>-</span>
        </el-descriptions-item>
        <el-descriptions-item label="上线时间">
          {{ formatTime(collector.last_online_time) }}
        </el-descriptions-item>
        <el-descriptions-item label="在线时长">
          <el-tooltip content="设备离线，暂无数据" placement="top" :disabled="collector.status === 'online' || sessionDuration !== '-'">
            <span :class="{ 'field-na': sessionDuration === '-' && collector.status !== 'online' }">{{ sessionDuration }}</span>
          </el-tooltip>
        </el-descriptions-item>
      </el-descriptions>
    </el-card>

    <!-- 配置同步状态 -->
    <el-card v-if="collector" class="config-sync-state" shadow="hover" style="margin-top: 20px;">
      <template #header>
        <span>配置同步状态 <el-tag :type="syncStateTagType" size="small">{{ syncStateLabel }}</el-tag></span>
      </template>
      <el-descriptions :column="descColumns" border>
        <el-descriptions-item label="协议版本">
          {{ collector.protocol_version || '2.0' }}
        </el-descriptions-item>
        <el-descriptions-item label="Last Manifest">
          <code>{{ collector.last_manifest_id || '—' }}</code>
        </el-descriptions-item>
        <el-descriptions-item label="Last Sync">
          {{ formatTime(collector.last_sync_at) }}
        </el-descriptions-item>
        <el-descriptions-item v-if="collector.last_sync_id" label="Last Sync ID" :span="descColumns">
          <code>{{ collector.last_sync_id }}</code>
        </el-descriptions-item>
      </el-descriptions>
    </el-card>

    <el-card v-if="loading && !collector" shadow="hover" style="margin-top: 20px;">
      <template #header>
        <span>总线配置</span>
      </template>
      <el-skeleton :rows="6" animated />
    </el-card>
    <el-card v-else shadow="hover" style="margin-top: 20px;">
      <template #header>
        <span>总线配置</span>
      </template>

      <!-- 总线配置面板 -->
      <ChannelPanel ref="busConfigPanelRef" :collector-id="collectorId" :node-device-id="collector?.node_id" :collector-status="collector?.status" :dma-channels="dmaChannels" />
    </el-card>

    <!-- DMA 通道 -->
    <el-card v-if="collector" shadow="hover" style="margin-top: 20px;">
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
            <!-- DMA 绑定只能通过 ChannelPanel 的硬件开关操作，此处仅展示状态 -->
            <el-tag
              v-if="dma.bound_to"
              type="success"
              size="small"
              effect="plain"
            >
              绑定: {{ dma.bound_to }}
            </el-tag>
            <el-tag
              v-else-if="dma.state === DmaState.ALLOCATED"
              type="warning"
              size="small"
              effect="plain"
            >
              未绑定
            </el-tag>
            <el-tag
              v-else-if="dma.state === DmaState.FREE"
              type="info"
              size="small"
              effect="plain"
            >
              未使用
            </el-tag>
            <el-tag
              v-else-if="dma.state === DmaState.DISABLED"
              type="danger"
              size="small"
              effect="plain"
            >
              已禁用
            </el-tag>
          </div>
        </div>
      </div>
    </el-card>

    <el-card shadow="hover" style="margin-top: 20px;">
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
        <div v-else class="mobile-table-wrapper">
          <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
          <el-table :data="devices" stripe @row-click="handleDeviceClick" style="cursor: pointer;">
          <el-table-column prop="name" label="设备名称" min-width="140" show-overflow-tooltip />
          <el-table-column label="类型" width="130">
            <template #default="{ row }">
              {{ getDeviceTypeText(row.device_type) }}
            </template>
          </el-table-column>
          <el-table-column label="地址" width="70">
            <template #default="{ row }">
              <code>{{ row.hardware_id || '-' }}</code>
            </template>
          </el-table-column>
          <el-table-column label="通道" width="140">
            <template #default="{ row }">
              <div v-if="getChannelForDevice(row)" class="device-channel-cell" @click="handleEditChannel(getChannelForDevice(row))">
                <el-tag size="small" type="primary" effect="light">
                  {{ getChannelForDevice(row)?.hardware_type?.toUpperCase() }} {{ getChannelForDevice(row)?.hardware_id }}
                </el-tag>
              </div>
              <span v-else class="text-muted">无</span>
            </template>
          </el-table-column>
          <el-table-column label="最新数据" min-width="160">
            <template #default="{ row }">
              <span v-if="row.last_data && Object.keys(row.last_data).length > 0">
                {{ formatLastData(row.last_data) }}
              </span>
              <span v-else class="text-muted">-</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <StatusBadge :status="row.status" />
            </template>
          </el-table-column>
          <el-table-column label="最后数据时间" width="160">
            <template #default="{ row }">
              <span class="text-muted">{{ formatTime(row.last_data_at) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="80" fixed="right">
            <template #default="{ row }">
              <el-button type="primary" text size="small" @click.stop="handleViewDevice(row)">
                查看
              </el-button>
            </template>
          </el-table-column>
          </el-table>
        </div>
      </template>
    </el-card>

    <!-- OTA 历史记录 -->
    <el-card shadow="hover" style="margin-top: 20px;">
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
        <div v-else class="mobile-table-wrapper">
          <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
          <el-table :data="otaHistory" stripe>
          <el-table-column label="升级版本" width="120">
            <template #default="{ row }">
              {{ row.from_version }} → {{ row.to_version }}
            </template>
          </el-table-column>
          <el-table-column label="状态" width="120">
            <template #default="{ row }">
              <el-tag :type="getOTAStatusType(row.status)" size="small">
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
                text
                size="small"
                @click="handleCancelOTA(row)"
              >
                取消
              </el-button>
              <span v-else>-</span>
            </template>
          </el-table-column>
          </el-table>
        </div>
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

    <!-- 系统日志 -->
    <el-card v-if="collector" shadow="hover" style="margin-top: 20px;">
      <template #header>
        <span>系统日志</span>
      </template>
      <LogPanel :collector-id="collectorId" :node-device-id="collector?.node_id" />
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useResponsive } from '@/composables/useResponsive'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Upload, Refresh, RefreshRight, Link, Connection, Warning, Edit } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import OTAForm from '@/components/forms/OTAForm.vue'
import ChannelPanel from '@/components/node/ChannelPanel.vue'
import LogPanel from '@/components/node/LogPanel.vue'
import { nodeApi, type OTARecord } from '@/api/node'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { useNodeStore } from '@/stores/node'
import { channelApi } from '@/api/channel'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { useDmaStore } from '@/stores/dma'
import { useUserStore } from '@/stores/user'
import { WS_EVENT } from '@/events/events'
import { logger } from '@/utils/logger'
import { getDeviceTypeLabel } from '@/utils/deviceType'
import { getQualityColor, getLatencyColor } from '@/utils/theme'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { DmaState, dmaStateText, dmaStateClass, dmaStateTagType } from '@/utils/dmaState'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'

const router = useRouter()
const route = useRoute()
const wsStore = useWebSocketStore()
const dmaStore = useDmaStore()
const edgeDeviceStore = useEdgeDeviceStore()
const nodeStore = useNodeStore()
const userStore = useUserStore()

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

// 节点离线（或未加载）时顶部操作按钮不可用
const collectorOffline = computed(() => !collector.value || collector.value.status === 'offline')

// 移动端：描述列表单列，桌面双列
const { isMobile } = useResponsive()
const descColumns = computed(() => (isMobile.value ? 1 : 2))

// 节点名称编辑
const editingName = ref(false)
const editNameValue = ref('')
const savingName = ref(false)

const startEditName = () => {
  editNameValue.value = collector.value?.name || ''
  editingName.value = true
}

const cancelEditName = () => {
  editingName.value = false
  editNameValue.value = ''
}

const saveName = async () => {
  if (!collector.value) return
  const targetId = collector.value.id
  const targetRouteId = route.params.id as string
  const targetName = editNameValue.value
  const sequence = ++nameSaveSequence
  savingName.value = true
  try {
    await nodeStore.updateNode(targetId, { name: targetName })
    if (sequence !== nameSaveSequence || route.params.id !== targetRouteId || collector.value?.id !== targetId) return
    collector.value.name = targetName
    editingName.value = false
    ElMessage.success('节点名称已更新')
  } catch (error: any) {
    if (sequence !== nameSaveSequence || route.params.id !== targetRouteId || collector.value?.id !== targetId) return
    ElMessage.error('保存失败: ' + (error.message || '未知错误'))
  } finally {
    if (sequence === nameSaveSequence && route.params.id === targetRouteId && collector.value?.id === targetId) savingName.value = false
  }
}
const pendingPingTimeout = ref<ReturnType<typeof setTimeout> | null>(null)
let collectorDetailSequence = 0
let devicesRequestSequence = 0
let otaRequestSequence = 0
let syncRequestSequence = 0
let nameSaveSequence = 0
let componentOperationGeneration = 0

// DMA 通道 — v2.5: 统一由 dmaStore 管理（只读展示，绑定操作在 ChannelPanel 中）
const dmaChannels = computed(() => dmaStore.mergedChannels)
const dmaLoading = computed(() => dmaStore.loading)

let unsubscribe: (() => void) | null = null

// 会话在线时长：从 last_online_time 到现在的差值，每秒刷新
const nowTick = ref(Date.now())
let sessionTimer: ReturnType<typeof setInterval> | null = null

const sessionDuration = computed(() => {
  const t = collector.value?.last_online_time
  if (!t || collector.value?.status !== 'online') return '-'
  const start = new Date(t).getTime()
  if (isNaN(start)) return '-'
  const diff = Math.floor((nowTick.value - start) / 1000)
  if (diff < 0) return '-'
  const days = Math.floor(diff / 86400)
  const hours = Math.floor((diff % 86400) / 3600)
  const minutes = Math.floor((diff % 3600) / 60)
  const seconds = Math.floor(diff % 60)
  const parts: string[] = []
  if (days > 0) parts.push(`${days}天`)
  if (hours > 0) parts.push(`${hours}小时`)
  if (minutes > 0) parts.push(`${minutes}分钟`)
  if (seconds > 0 && parts.length === 0) parts.push(`${seconds}秒`)
  return parts.join(' ') || '-'
})

const collectorId = computed(() => route.params.id as string)

// 面包屑当前节点：优先名称，未命名时显示设备ID短码
const nodeDisplayName = computed(() => {
  if (collector.value?.name) return collector.value.name
  const id = collector.value?.node_id || collectorId.value || ''
  if (!id) return '节点详情'
  return id.length > 12 ? `${id.slice(0, 8)}…` : id
})

// 页面标题：优先节点名称，未命名时与面包屑一致显示设备ID短码，"节点详情"仅作最终兜底
const pageTitle = computed(() => collector.value?.name || nodeDisplayName.value)

// 配置同步状态
const syncStateLabel = computed(() => {
  // 离线设备不可能正在同步：覆盖后端快照中可能残留的"同步中"等状态
  if (collector.value?.status === 'offline') return '离线'
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
  if (collector.value?.status === 'offline') return 'info'
  return {
    in_sync: 'success',
    syncing: 'warning',
    lag: 'danger',
    error: 'danger',
    unknown: 'info',
  }[collector.value?.config_sync_state as string] || 'info'
})

const getDeviceTypeText = (type: string) => {
  return getDeviceTypeLabel(type)
}

// 根据设备的 channel_id 查找对应的 Channel
const getChannelForDevice = (device: any) => {
  return channels.value.find(ch => ch.id === device.channel_id)
}

// 点击通道标签 → 通过 ChannelPanel 打开通道编辑对话框
const handleEditChannel = (channel: any) => {
  if (collector.value?.status !== 'online') {
    ElMessage.warning('节点离线，无法编辑通道')
    return
  }
  busConfigPanelRef.value?.handleOpenChannelManager(channel)
}

const handlePing = async () => {
  if (!collector.value) return
  const id = route.params.id as string
  const nodeId = collector.value.node_id
  const sessionGeneration = getSessionGeneration()
  const operation = componentOperationGeneration
  pinging.value = true
  try {
    await nodeApi.ping(nodeId)
    assertSessionGeneration(sessionGeneration)
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    const timeout = setTimeout(() => {
      pinging.value = false
      ElMessage.warning('延迟测量超时，采集器可能离线')
    }, 5000)
    pendingPingTimeout.value = timeout
  } catch (err: any) {
    if (operation !== componentOperationGeneration || route.params.id !== id) return
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
  const sequence = ++collectorDetailSequence
  try {
    const result = await nodeStore.fetchDetail(id, true)
    if (sequence !== collectorDetailSequence || route.params.id !== id) return
    collector.value = result
    // 自动测量延迟（如果在线且无延迟数据）
    if (collector.value?.status === 'online' && (!collector.value.ping_latency_ms || collector.value.ping_latency_ms <= 0)) {
      handlePing()
    }
  } catch (error: any) {
    if (sequence === collectorDetailSequence) ElMessage.error('获取节点详情失败')
  } finally {
    if (sequence === collectorDetailSequence) loading.value = false
  }
}

const fetchDevices = async () => {
  const id = route.params.id as string
  if (!id) return

  const sequence = ++devicesRequestSequence
  devicesLoading.value = true
  try {
    const params = { node_id: id, page: 1, page_size: 100 }
    const [, channelRes] = await Promise.all([
      edgeDeviceStore.fetchList(params, true),
      channelApi.getList(id)
    ])
    if (sequence !== devicesRequestSequence || route.params.id !== id) return
    devices.value = edgeDeviceStore.getCachedList(params)?.items || []
    // 统一为数组
    channels.value = Array.isArray(channelRes)
      ? channelRes
      : (channelRes?.items || [])
  } catch (error: any) {
    if (sequence === devicesRequestSequence) {
      logger.error('获取设备列表失败', { error: String(error) })
      ElMessage.error('获取设备列表失败')
    }
  } finally {
    if (sequence === devicesRequestSequence) devicesLoading.value = false
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

  const sequence = ++otaRequestSequence
  otaHistoryLoading.value = true
  try {
    const history = (await nodeApi.getOTAHistory(id)) || []
    if (sequence !== otaRequestSequence || route.params.id !== id) return
    otaHistory.value = history
  } catch (error: any) {
    if (sequence === otaRequestSequence) logger.error('获取OTA历史失败', { error: String(error) })
  } finally {
    if (sequence === otaRequestSequence) otaHistoryLoading.value = false
  }
}

const handleSyncConfig = async () => {
  const id = route.params.id as string
  if (!id) return

  syncingConfig.value = true
  const sequence = ++syncRequestSequence
  const sessionGeneration = getSessionGeneration()
  try {
    await nodeApi.syncConfig(id)
    assertSessionGeneration(sessionGeneration)
    if (sequence !== syncRequestSequence || route.params.id !== id) return
    ElMessage.success('配置同步成功')
  } catch (error: any) {
    if (sequence !== syncRequestSequence || route.params.id !== id) return
    ElMessage.error('配置同步失败: ' + (error.message || '未知错误'))
  } finally {
    if (sequence === syncRequestSequence && route.params.id === id) syncingConfig.value = false
  }
}

const handleCancelOTA = async (record: OTARecord) => {
  const id = collectorId.value
  const operation = componentOperationGeneration
  const sessionGeneration = getSessionGeneration()
  try {
    await ElMessageBox.confirm('确定要取消此OTA升级吗？', '提示', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning'
    })

    if (operation !== componentOperationGeneration || collectorId.value !== id) return
    await nodeApi.cancelOTA(id, record.id)
    assertSessionGeneration(sessionGeneration)
    if (operation !== componentOperationGeneration || collectorId.value !== id) return
    ElMessage.success('已取消OTA升级')
    fetchOTAHistory()
  } catch (error: any) {
    if (operation !== componentOperationGeneration || collectorId.value !== id) return
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

const formatLastData = (data: Record<string, any>): string => {
  if (!data) return '-'
  const entries = Object.entries(data).filter(([k]) => k !== 'error_code' && k !== 'raw_data')
  if (entries.length === 0) return '-'
  // Show up to 3 key=value pairs
  return entries.slice(0, 3).map(([k, v]) => {
    const unit = sensorUnitMap[k] || ''
    const name = sensorNameMap[k] || k
    return `${name}: ${typeof v === 'number' ? v.toFixed(v < 10 ? 2 : 0) : v}${unit ? unit : ''}`
  }).join('  ')
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
// DMA 辅助函数 — 使用共享枚举 (dmaState.ts)
// ============================================================

// dmaStateText, dmaStateClass, dmaStateTagType 已从 @/utils/dmaState 导入

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
  await dmaStore.fetch(collectorId.value)
}

// 采集器上线时刷新通道列表
watch(() => collector.value?.status, (newStatus, oldStatus) => {
  if (oldStatus === 'offline' && newStatus === 'online') {
    // 采集器上线，刷新总线配置和通道列表
    busConfigPanelRef.value?.refreshChannels?.()
    busConfigPanelRef.value?.refreshBuses?.()
  }
})

watch(() => route.params.id, (newId, oldId) => {
  if (newId === oldId) return
  editingName.value = false
  editNameValue.value = ''
  showOTADialog.value = false
  savingName.value = false
  nameSaveSequence++
  syncingConfig.value = false
  syncRequestSequence++
  pinging.value = false
  if (pendingPingTimeout.value) {
    clearTimeout(pendingPingTimeout.value)
    pendingPingTimeout.value = null
  }
  collector.value = null
  devices.value = []
  channels.value = []
  void fetchCollectorDetail()
  void fetchDevices()
  void fetchOTAHistory()
  void loadDmaChannels()
})

onMounted(() => {
  fetchCollectorDetail()
  fetchDevices()
  fetchOTAHistory()
  loadDmaChannels()

  // 会话时长每秒刷新
  sessionTimer = setInterval(() => { nowTick.value = Date.now() }, 1000)

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
  componentOperationGeneration++
  collectorDetailSequence++
  devicesRequestSequence++
  otaRequestSequence++
  syncRequestSequence++
  nameSaveSequence++
  if (unsubscribe) {
    unsubscribe()
  }
  if (pendingPingTimeout.value) {
    clearTimeout(pendingPingTimeout.value)
  }
  if (sessionTimer) {
    clearInterval(sessionTimer)
  }
})
</script>

<style scoped>
.collector-detail {
  padding: 0;
}

.node-breadcrumb {
  margin-bottom: 12px;
}

/* 离线导致的占位符：统一灰色，提示无数据 */
.field-na {
  color: var(--el-text-color-placeholder);
}

/* 移动端：标题保持横排，空间不足时整体换行压缩按钮区而非挤压标题 */
@media (max-width: 768px) {
  .collector-detail :deep(.page-header) {
    flex-wrap: wrap;
  }

  .collector-detail :deep(.page-header-left) {
    flex: 1;
    min-width: 0;
  }

  .collector-detail :deep(.page-header-left > div) {
    min-width: 0;
  }

  .collector-detail :deep(.page-header-left h2) {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .collector-detail :deep(.page-header-right) {
    flex-shrink: 0;
    gap: 6px;
  }
}

/* 离线禁用按钮：三种 type 统一为灰色禁用态。
   Element Plus 对 primary 类型会把 --el-button-disabled-* 变量重定义为浅蓝色，
   且 .el-button--primary.is-disabled 选择器特异性更高，
   因此这里必须显式使用灰色变量并追加 --primary 覆盖才能生效 */
.collector-detail :deep(.el-button.is-disabled),
.collector-detail :deep(.el-button.is-disabled:hover),
.collector-detail :deep(.el-button.is-disabled:focus),
.collector-detail :deep(.el-button--primary.is-disabled),
.collector-detail :deep(.el-button--primary.is-disabled:hover),
.collector-detail :deep(.el-button--primary.is-disabled:focus) {
  background-color: var(--el-fill-color-light);
  border-color: var(--el-border-color);
  color: var(--el-text-color-placeholder);
  opacity: 0.6;
}

.editable-name {
  display: flex;
  align-items: center;
  gap: 6px;
}

.edit-icon {
  cursor: pointer;
  color: var(--el-text-color-secondary);
  transition: color 0.2s;
}

.edit-icon:hover {
  color: var(--el-color-primary);
}

.text-muted {
  color: var(--el-text-color-placeholder);
  font-size: 12px;
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
  color: var(--el-text-color-regular);
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
  background: var(--el-fill-color-light);
  border-radius: 6px;
  border: 1px solid var(--el-border-color);
  transition: all 0.2s;
}

.peripheral-item:hover {
  border-color: var(--el-color-primary);
}

.peripheral-item.assigned {
  background: var(--el-color-success-light-9);
  border-color: var(--el-color-success-light-5);
}

.peripheral-info {
  display: flex;
  align-items: center;
  gap: 8px;
}

.peripheral-id {
  font-weight: 500;
  color: var(--el-text-color-primary);
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
  color: var(--el-color-success);
  font-size: 13px;
}

.capability-item {
  padding: 8px 0;
  display: flex;
  align-items: center;
}

.capability-item:not(:last-child) {
  border-bottom: 1px solid var(--el-border-color-light);
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
  border-color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
  padding: 5px 10px;
  cursor: pointer;
  transition: background 0.2s;
  text-align: center;
  min-width: 130px;
}

.device-channel-tag:hover {
  background: var(--el-color-primary-light-8);
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
  code {
    background: var(--el-fill-color-light);
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
  background: var(--el-fill-color-light);
  border-radius: 8px;
  border: 1px solid var(--el-border-color);
  transition: border-color 0.2s;
}

.dma-card:hover {
  border-color: var(--el-color-primary);
}

.dma-card.dma-state-allocated {
  background: var(--el-color-success-light-9);
  border-color: var(--el-color-success-light-5);
}

.dma-card.dma-state-disabled {
  background: var(--el-color-danger-light-9);
  border-color: var(--el-color-danger-light-5);
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
  color: var(--el-text-color-primary);
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
  color: var(--el-text-color-regular);
}

.dma-label {
  width: 70px;
  flex-shrink: 0;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.dma-bound {
  color: var(--el-color-success);
  font-weight: 500;
}

.dma-controls {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  padding-top: 8px;
  border-top: 1px solid var(--el-border-color-light);
}
</style>
