<template>
  <div class="device-detail">
    <DeviceHeader
      :device="device"
      :ws-connected="wsConnected"
      :syncing-h-a="syncingHA"
      :refreshing="refreshing"
      :title="pageTitle"
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
      <CommandFrequencySection :device-id="deviceId" />

      <!-- Operations -->
      <DeviceControlPanel :device-id="deviceId" />

      <!-- History chart -->
      <HistoryChartSection
        ref="historyChartRef"
        :device-id="deviceId"
        :device-type="device.device_type"
        :device-type-text="deviceTypeText"
      />
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import DeviceHeader from './shared/DeviceHeader.vue'
import DeviceInfoCard from './shared/DeviceInfoCard.vue'
import HistoryChartSection from './shared/HistoryChartSection.vue'
import CommandFrequencySection from './shared/CommandFrequencySection.vue'
import DeviceControlPanel from './shared/DeviceControlPanel.vue'
import RealtimeDataList from '@/components/data/RealtimeDataList.vue'
import { useDeviceData } from '@/composables/useDeviceData'
import { getDeviceTypeLabel } from '@/utils/deviceType'

const router = useRouter()
const route = useRoute()
const deviceId = ref(Number(route.params.id) || null)

const historyChartRef = ref<InstanceType<typeof HistoryChartSection> | null>(null)

const {
  device, loading, refreshing, syncingHA, wsConnected,
  realtimeDataItems, clearRealtimeData,
  fetchDeviceDetail, fetchLatestData,
  handleRefresh: composableHandleRefresh, handleSyncToHA,
} = useDeviceData(deviceId)

const handleRefresh = () => composableHandleRefresh(() => historyChartRef.value?.fetchHistoryData())

const deviceTypeText = computed(() => device.value ? getDeviceTypeLabel(device.value.device_type) : '')

// 标题优先展示设备名称，无名称时兜底设备类型
const pageTitle = computed(() => device.value?.name || deviceTypeText.value || '边缘设备详情')

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
