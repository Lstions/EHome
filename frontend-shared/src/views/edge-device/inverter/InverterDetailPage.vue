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

      <!-- Core metrics -->
      <el-row :gutter="20" style="margin-top: 20px;">
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="metric-card">
            <div class="metric-content">
              <div class="metric-icon pv">
                <el-icon :size="20"><Sunrise /></el-icon>
              </div>
              <div class="metric-info">
                <p class="metric-label">PV输入</p>
                <p class="metric-value">{{ formatPower(totalPvPower) }}</p>
              </div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="metric-card">
            <div class="metric-content">
              <div class="metric-icon battery">
                <el-icon :size="20"><Lightning /></el-icon>
              </div>
              <div class="metric-info">
                <p class="metric-label">电池</p>
                <p class="metric-value">{{ formatNum(latestData?.battery_voltage ?? latestData?.voltage, 1) }}<span class="metric-unit">V</span></p>
                <p class="metric-sub" :class="{ negative: (latestData?.battery_current ?? latestData?.current ?? 0) < 0 }">
                  {{ (latestData?.battery_current ?? latestData?.current ?? 0) < 0 ? '充电' : '放电' }}
                  {{ Math.abs(latestData?.battery_current ?? latestData?.current ?? 0).toFixed(1) }}A
                </p>
              </div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="metric-card">
            <div class="metric-content">
              <div class="metric-icon grid">
                <el-icon :size="20"><Connection /></el-icon>
              </div>
              <div class="metric-info">
                <p class="metric-label">电网</p>
                <p class="metric-value">{{ formatNum(latestData?.grid_voltage, 0) }}<span class="metric-unit">V</span></p>
                <p class="metric-sub">{{ formatNum(latestData?.grid_frequency ?? latestData?.frequency, 1) }}Hz</p>
              </div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="metric-card">
            <div class="metric-content">
              <div class="metric-icon load">
                <el-icon :size="20"><HomeFilled /></el-icon>
              </div>
              <div class="metric-info">
                <p class="metric-label">负载</p>
                <p class="metric-value">{{ formatPower(latestData?.load_power ?? latestData?.power ?? 0) }}</p>
              </div>
            </div>
          </el-card>
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

      <!-- MPPT details -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header><span>MPPT通道</span></template>
        <InverterMpptCard :data="latestData" />
      </el-card>

      <!-- Alarm status -->
      <el-card style="margin-top: 20px;" shadow="hover" v-if="hasAlarms">
        <template #header>
          <div style="display: flex; justify-content: space-between; align-items: center;">
            <span>告警状态</span>
            <el-tag type="danger" size="small">{{ alarmCount }}条告警</el-tag>
          </div>
        </template>
        <div class="alarm-list">
          <el-tag v-for="alarm in alarmItems" :key="alarm" type="danger" size="small" style="margin: 4px;">
            {{ alarm }}
          </el-tag>
        </div>
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
          :max-items="100"
          :auto-scroll="true"
          :device-type="device?.device_type"
          @clear="clearRealtimeData"
        />
      </el-card>

      <!-- Command frequency -->
      <CommandFrequencySection :device-id="deviceId" :device-type="device.device_type" />

      <!-- Operations -->
      <OperationButtons :device="device" :device-id="deviceId" @operation-executed="fetchDeviceDetail" />

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
import { Sunrise, Lightning, Connection, HomeFilled } from '@element-plus/icons-vue'
import DeviceHeader from '../shared/DeviceHeader.vue'
import DeviceInfoCard from '../shared/DeviceInfoCard.vue'
import HistoryChartSection from '../shared/HistoryChartSection.vue'
import CommandFrequencySection from '../shared/CommandFrequencySection.vue'
import OperationButtons from '../shared/OperationButtons.vue'
import InverterPowerFlow from './InverterPowerFlow.vue'
import InverterMpptCard from './InverterMpptCard.vue'
import RealtimeDataList from '@/components/data/RealtimeDataList.vue'
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

// Alarm detection
const alarmItems = computed<string[]>(() => {
  if (!latestData.value) return []
  const alarms: string[] = []
  for (const [key, val] of Object.entries(latestData.value)) {
    if (key.startsWith('alarm_') && (val === 1 || val > 0)) {
      alarms.push(key.replace('alarm_', '').replace(/_/g, ' '))
    }
  }
  const faultCode = latestData.value.fault_code ?? latestData.value.error_code
  if (faultCode && faultCode > 0) {
    alarms.push(`故障码: ${faultCode}`)
  }
  return alarms
})

const hasAlarms = computed(() => alarmItems.value.length > 0)
const alarmCount = computed(() => alarmItems.value.length)

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

.metric-card { cursor: default; transition: transform 0.3s, box-shadow 0.3s; }
.metric-card:hover { transform: translateY(-2px); box-shadow: var(--el-box-shadow-light); }
.metric-content { display: flex; align-items: center; gap: 12px; }
.metric-icon {
  width: 48px; height: 48px; border-radius: 10px;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
  color: #fff;
}
.metric-icon.pv { background: linear-gradient(135deg, var(--el-color-warning), var(--el-color-warning-light-3)); }
.metric-icon.battery { background: linear-gradient(135deg, var(--el-color-success), var(--el-color-success-light-3)); }
.metric-icon.grid { background: linear-gradient(135deg, var(--el-color-primary), var(--el-color-primary-light-3)); }
.metric-icon.load { background: linear-gradient(135deg, var(--el-color-info), var(--el-color-info-light-3)); }
.metric-info { flex: 1; min-width: 0; display: flex; flex-direction: column; justify-content: flex-start; }
.metric-label { margin: 0 0 4px; font-size: 12px; color: var(--el-text-color-secondary); }
.metric-value { margin: 0; font-size: 22px; font-weight: 600; color: var(--el-text-color-primary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; line-height: 1.2; }
.metric-unit { font-size: 15px; font-weight: 400; color: var(--el-text-color-secondary); margin-left: 2px; }
.metric-sub { margin: 2px 0 0; font-size: 12px; color: var(--el-text-color-secondary); }
.metric-sub.negative { color: var(--el-color-danger); }

.alarm-list { display: flex; flex-wrap: wrap; gap: 4px; }
</style>
