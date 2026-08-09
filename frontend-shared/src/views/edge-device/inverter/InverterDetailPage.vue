<template>
  <div class="inverter-detail">
    <DeviceHeader
      :device="device"
      :ws-connected="wsConnected"
      :syncing-h-a="syncingHA"
      :refreshing="refreshing"
      :title="device?.name || '逆变器详情'"
      @back="goBack"
      @sync-to-h-a="handleSyncToHA"
      @refresh="handleRefresh"
      @updated="fetchDeviceDetail"
    />

    <el-card v-if="loading && !device" shadow="hover">
      <el-skeleton :rows="4" animated />
    </el-card>
    <template v-else-if="device">
      <DeviceInfoCard :device="device" />

      <!-- Core metrics：统一共享组件 MetricStatCard（透明底彩色图标 + 辅助槽恒占位等高 + 充放电方向语义色） -->
      <el-row :gutter="20" style="margin-top: 20px;">
        <el-col :xs="12" :sm="12" :md="6">
          <MetricStatCard label="PV输入" :value="formatPower(totalPvPower)" tone="warning">
            <template #icon><el-icon :size="20"><Sunrise /></el-icon></template>
          </MetricStatCard>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <MetricStatCard
            label="电池"
            :value="formatNum(latestData?.battery_voltage ?? latestData?.voltage, 1)"
            unit="V"
            tone="success"
            :direction="(latestData?.battery_current ?? latestData?.current ?? 0) < 0 ? 'charge' : (latestData?.battery_current ?? latestData?.current ?? 0) > 0 ? 'discharge' : 'idle'"
          >
            <template #icon><el-icon :size="20"><Lightning /></el-icon></template>
          </MetricStatCard>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <MetricStatCard
            label="电网"
            :value="formatNum(latestData?.grid_voltage, 0)"
            unit="V"
            tone="primary"
            :sub-text="`${formatNum(latestData?.grid_frequency ?? latestData?.frequency, 1)}Hz`"
          >
            <template #icon><el-icon :size="20"><Connection /></el-icon></template>
          </MetricStatCard>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <MetricStatCard label="负载" :value="formatPower(latestData?.load_power ?? latestData?.power ?? 0)" tone="info">
            <template #icon><el-icon :size="20"><HomeFilled /></el-icon></template>
          </MetricStatCard>
        </el-col>
      </el-row>

      <!-- Power flow diagram -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header><span>功率流向</span></template>
        <InverterPowerFlow
          :pv-power="totalPvPower"
          :load-power="latestData?.load_power ?? latestData?.power ?? 0"
          :battery-voltage="latestData?.battery_voltage ?? latestData?.voltage ?? 0"
          :battery-current="latestData?.battery_current ?? latestData?.current ?? 0"
        />
      </el-card>

      <!-- Status & alarms -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header>
          <div style="display: flex; justify-content: space-between; align-items: center;">
            <span>状态与告警</span>
            <el-tag v-if="hasAlarms" type="danger" size="small">{{ alarmCount }}条告警</el-tag>
          </div>
        </template>
        <InverterStatusGrid :latest-data="latestData" />
      </el-card>

      <!-- MPPT details -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header><span>MPPT通道</span></template>
        <InverterMpptCard :data="latestData" />
      </el-card>

      <!-- Temperature & fan -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header><span>温度与风扇</span></template>
        <InverterTempCard :latest-data="latestData" />
      </el-card>

      <!-- Energy generation -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header><span>发电量统计</span></template>
        <InverterEnergyCard :latest-data="latestData" />
      </el-card>

      <!-- Realtime data stream -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header>
          <div style="display: flex; justify-content: space-between; align-items: center;">
            <span>实时数据流</span>
            <el-tag v-if="device?.status === 'online' || device?.status === 'active'" type="success" size="small">实时</el-tag>
          </div>
        </template>
        <RealtimeDataList
          :items="realtimeDataItems"
          :auto-scroll="true"
          :device-type="device?.device_type"
          @clear="clearRealtimeData"
        />
      </el-card>

      <!-- History chart -->
      <HistoryChartSection
        ref="historyChartRef"
        :device-id="deviceId"
        :device-type="device.device_type"
        :device-type-text="deviceTypeText"
      />

      <!-- Command frequency -->
      <CommandFrequencySection :device-id="deviceId" />

      <!-- Operations -->
      <DeviceControlPanel :device-id="deviceId" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Sunrise, Lightning, Connection, HomeFilled } from '@element-plus/icons-vue'
import DeviceHeader from '../shared/DeviceHeader.vue'
import DeviceInfoCard from '../shared/DeviceInfoCard.vue'
import HistoryChartSection from '../shared/HistoryChartSection.vue'
import CommandFrequencySection from '../shared/CommandFrequencySection.vue'
import DeviceControlPanel from '../shared/DeviceControlPanel.vue'
import InverterPowerFlow from './InverterPowerFlow.vue'
import InverterMpptCard from './InverterMpptCard.vue'
import InverterStatusGrid from './InverterStatusGrid.vue'
import InverterTempCard from './InverterTempCard.vue'
import InverterEnergyCard from './InverterEnergyCard.vue'
import RealtimeDataList from '@/components/data/RealtimeDataList.vue'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
import { useDeviceData } from '@/composables/useDeviceData'
import { getDeviceTypeLabel } from '@/utils/deviceType'
import { formatPower } from '@/utils/format'

const router = useRouter()
const route = useRoute()
const deviceId = ref(Number(route.params.id) || null)

const historyChartRef = ref<InstanceType<typeof HistoryChartSection> | null>(null)

const {
  device, loading, refreshing, syncingHA, wsConnected,
  latestData, realtimeDataItems, clearRealtimeData,
  fetchDeviceDetail, fetchLatestData,
  handleRefresh: composableHandleRefresh, handleSyncToHA,
} = useDeviceData(deviceId, { maxItems: 100, errorPrefix: '获取逆变器详情失败' })

const handleRefresh = () => composableHandleRefresh(() => historyChartRef.value?.fetchHistoryData())

const deviceTypeText = computed(() => device.value ? getDeviceTypeLabel(device.value.device_type) : '逆变器')

// Calculate total PV power from multiple channels
const totalPvPower = computed(() => {
  if (!latestData.value) return 0
  let total = 0
  for (let i = 1; i <= 4; i++) {
    const p = latestData.value[`pv${i}_power`]
    if (typeof p === 'number') total += p
  }
  if (total === 0) {
    total = latestData.value.pv_power ?? latestData.value.solar_power ?? 0
  }
  return total
})

// Alarm detection — count only, detailed display is in InverterStatusGrid
const alarmCount = computed(() => {
  if (!latestData.value) return 0
  let count = 0
  for (const [key, val] of Object.entries(latestData.value)) {
    if (key.startsWith('alarm_') && (val === 1 || val > 0)) {
      count++
    }
  }
  const faultCode = latestData.value.fault_code ?? latestData.value.error_code
  if (faultCode && faultCode > 0) {
    count++
  }
  return count
})

const hasAlarms = computed(() => alarmCount.value > 0)

function formatNum(v: number | undefined | null, digits: number = 1): string {
  if (v === undefined || v === null || isNaN(v)) return '--'
  return v.toFixed(digits)
}

const goBack = () => router.back()

onMounted(() => {
  fetchDeviceDetail()
  fetchLatestData()
})
</script>

<style scoped>
.inverter-detail { padding: 0; }

.alarm-list { display: flex; flex-wrap: wrap; gap: 4px; }
</style>
