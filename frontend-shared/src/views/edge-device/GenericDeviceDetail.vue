<template>
  <div class="device-detail">
    <!-- 统一页头内容（与 BMS / 逆变器同维度）：状态+类型标签、节点/通道/设备号/固件副标题 -->
    <DeviceHeader
      :device="device"
      :ws-connected="wsConnected"
      :syncing-h-a="syncingHA"
      :refreshing="refreshing"
      :title="pageTitle"
      :tags="headerTags"
      :subtitle="headerSubtitle"
      @back="goBack"
      @sync-to-h-a="handleSyncToHA"
      @refresh="handleRefresh"
      @updated="fetchDeviceDetail"
    />

    <el-card v-if="loading && !device">
      <el-skeleton :rows="4" animated />
    </el-card>
    <template v-else-if="device">
      <DeviceInfoCard :device="device" />

      <!-- Realtime data -->
      <el-card style="margin-top: 20px;">
        <template #header>
          <div style="display: flex; justify-content: space-between; align-items: center;">
            <span>实时数据</span>
            <el-tag v-if="device?.status === 'online' || device?.status === 'active'" type="success" effect="plain" size="small">实时推送</el-tag>
          </div>
        </template>
        <RealtimeDataList
          :items="displayRealtimeItems"
          :auto-scroll="true"
          :device-type="device?.device_type"
          @clear="clearRealtimeData"
        />
      </el-card>

      <!-- Command frequency -->
      <CommandFrequencySection
        :device-id="deviceId"
        :node-id="deviceNodeId"
        :max-polling-commands="device?.max_polling_commands"
      />

      <!-- Operations -->
      <DeviceControlPanel :device-id="deviceId" />

      <!-- History chart -->
      <HistoryChartSection
        ref="historyChartRef"
        :device-id="deviceId"
        :device-type="device.device_type"
        :device-type-text="deviceTypeText"
        :online="device ? ((device.status === 'online' || device.status === 'active') ? 'online' : 'offline') : 'unknown'"
      />
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import DeviceHeader from './shared/DeviceHeader.vue'
import DeviceInfoCard from './shared/DeviceInfoCard.vue'
import HistoryChartSection from './shared/HistoryChartSection.vue'
import CommandFrequencySection from './shared/CommandFrequencySection.vue'
import DeviceControlPanel from './shared/DeviceControlPanel.vue'
import RealtimeDataList from '@/components/data/RealtimeDataList.vue'
import { useDeviceData } from '@/composables/useDeviceData'
import { getDeviceTypeLabel } from '@/utils/deviceType'
import { useBreadcrumbStore } from '@/stores/breadcrumb'

const router = useRouter()
const route = useRoute()
const deviceId = ref(Number(route.params.id) || 0)

const historyChartRef = ref<InstanceType<typeof HistoryChartSection> | null>(null)

const {
  device, loading, refreshing, syncingHA, wsConnected,
  realtimeDataItems, clearRealtimeData,
  fetchDeviceDetail, fetchLatestData,
  handleRefresh: composableHandleRefresh, handleSyncToHA,
} = useDeviceData(deviceId)

const handleRefresh = () => composableHandleRefresh(() => historyChartRef.value?.fetchHistoryData())

const deviceTypeText = computed(() => device.value ? getDeviceTypeLabel(device.value.device_type) : '')
/**
 * 所属节点数值 id —— 透传给指令频率面板，用于保存后确认节点是否真的应用了新配置
 * （2026-10-15 报障：改前提示永久停在「正在同步到节点」，因为根本没有确认信号）。
 * 优先 device.node.id（后端 Node 关联），回退 node_id 序列号。
 */
const deviceNodeId = computed(() => device.value?.node?.id ?? device.value?.node_id)

// 标题优先展示设备名称，无名称时兜底设备类型
const pageTitle = computed(() => device.value?.name || deviceTypeText.value || '边缘设备详情')

/* 顶部框架面包屑末段显示设备名称（2026-10-21 用户要求），卸载清空避免串页 */
const breadcrumbStore = useBreadcrumbStore()
watch(
  () => device.value?.name,
  (name) => breadcrumbStore.setLeafLabel(name || ''),
  { immediate: true },
)
onBeforeUnmount(() => breadcrumbStore.clearLeafLabel())

/**
 * 页头标签（三设备页统一口径）：状态 + 类型。
 * 状态用在线/离线/未知三档，与 BMS 页一致；不自己造第四种文案。
 */
const headerTags = computed<Array<{ text: string; type: 'success' | 'info' | 'warning' | 'danger'; effect: 'plain' }>>(() => {
  const d = device.value
  if (!d) return []
  const online = d.status === 'online' || d.status === 'active'
  return [
    { text: online ? '在线' : d.status === 'offline' ? '离线' : '状态未知', type: online ? 'success' : 'info', effect: 'plain' },
    { text: deviceTypeText.value, type: 'info', effect: 'plain' },
  ]
})

/** 页头副标题（三设备页统一维度）：节点 · 通道 · 设备号 · 固件；缺失项跳过不占位 */
const headerSubtitle = computed(() => {
  const d = device.value
  if (!d) return ''
  const parts: string[] = []
  const node = d.node?.name || d.node_id
  if (node) parts.push('节点 ' + node)
  if (d.channel?.hardware_id) parts.push('通道 ' + d.channel.hardware_id)
  if (d.id) parts.push('设备 #' + d.id)
  const fw = d.node?.firmware_version
  if (fw) parts.push('节点固件 ' + fw)
  return parts.join(' · ')
})


// 过滤无有效载荷的数据项（如后端 latest 记录为空对象/全 null 字段），
// 保证“共 N 条数据”计数与实际可读内容一致；全部无效时由列表组件展示空状态
const displayRealtimeItems = computed(() =>
  realtimeDataItems.value.filter((item) => {
    const data = item.data
    if (data && typeof data === 'object') {
      if (Object.values(data).some((v) => v !== null && v !== undefined && v !== '')) return true
    } else if (typeof data === 'number' || (typeof data === 'string' && data.trim() !== '')) {
      return true
    }
    return Array.isArray(item.rawData) && item.rawData.length > 0
  })
)

const goBack = () => router.back()

onMounted(() => {
  fetchDeviceDetail()
  fetchLatestData()
})
</script>

<style scoped>
.device-detail { padding: 0; }

/* 移动端：实时数据卡工具栏分行排列，分段控件独占一行避免"16进制"被挤折行、边框折断 */
@media (max-width: 768px) {
  .device-detail :deep(.realtime-data-list .list-header) {
    flex-direction: column;
    align-items: stretch;
    gap: 8px;
  }
  .device-detail :deep(.realtime-data-list .display-mode .el-radio-group) {
    flex-shrink: 0;
  }
  .device-detail :deep(.realtime-data-list .list-stats) {
    justify-content: flex-end;
  }
}
</style>
