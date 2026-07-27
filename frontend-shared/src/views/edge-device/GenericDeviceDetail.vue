<template>
  <div class="device-detail">
    <DeviceHeader
      :device="device"
      :ws-connected="wsConnected"
      :syncing-h-a="syncingHA"
      :refreshing="refreshing"
      :title="deviceTypeText || '边缘设备详情'"
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
            <el-tag v-if="device?.status === 'online' || device?.status === 'active'" type="success" size="small">实时</el-tag>
          </div>
        </template>
        <RealtimeDataList
          :items="realtimeDataItems"
          :max-items="200"
          :auto-scroll="true"
          :device-type="device?.device_type"
          @clear="clearRealtimeData"
        />
      </el-card>

      <!-- Command frequency -->
      <CommandFrequencySection :device-id="deviceId" :device-type="device.device_type" />

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

const goBack = () => router.back()

onMounted(() => {
  fetchDeviceDetail()
  fetchLatestData()
})
</script>

<style scoped>
.device-detail { padding: 0; }
</style>
