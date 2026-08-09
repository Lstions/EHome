<template>
  <div class="bms-detail">
    <DeviceHeader
      :device="device"
      :ws-connected="wsConnected"
      :syncing-h-a="syncingHA"
      :refreshing="refreshing"
      :title="device?.name || 'BMS详情'"
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

      <!-- Core metrics：统一共享组件 MetricStatCard（透明底彩色图标 + 辅助槽恒占位等高 + 放电方向不用 danger 红） -->
      <el-row :gutter="20" style="margin-top: 20px;">
        <el-col :xs="12" :sm="12" :md="6">
          <MetricStatCard
            label="SOC"
            :value="formatNum(latestData?.rsoc ?? latestData?.soc, 1)"
            unit="%"
            tone="success"
            :progress="Math.round(latestData?.rsoc ?? latestData?.soc ?? 0)"
          >
            <template #icon><el-icon :size="20"><PieChart /></el-icon></template>
          </MetricStatCard>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <MetricStatCard
            label="剩余容量"
            :value="formatNum(latestData?.remaining_capacity, 2)"
            unit="Ah"
            tone="primary"
            :sub-text="`/ ${formatNum(latestData?.nominal_capacity, 0)}Ah`"
          >
            <template #icon><el-icon :size="20"><Odometer /></el-icon></template>
          </MetricStatCard>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <MetricStatCard
            label="总电压"
            :value="formatNum(latestData?.total_voltage ?? latestData?.voltage, 3)"
            unit="V"
            tone="warning"
          >
            <template #icon><el-icon :size="20"><DataLine /></el-icon></template>
          </MetricStatCard>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <MetricStatCard
            label="电流"
            :value="formatNum(latestData?.current, 3)"
            unit="A"
            tone="primary"
            :direction="(latestData?.current ?? 0) < 0 ? 'discharge' : (latestData?.current ?? 0) > 0 ? 'charge' : 'idle'"
          >
            <template #icon><el-icon :size="20"><Lightning /></el-icon></template>
          </MetricStatCard>
        </el-col>
      </el-row>

      <!-- Cell voltage chart -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header>
          <div class="cell-voltage-header">
            <span class="cell-voltage-title">电芯电压</span>
            <el-tag v-if="cellVoltages.length > 0" size="small" type="info" class="cell-voltage-summary">
              {{ cellVoltages.length }}节 · 最低{{ Math.min(...cellVoltages).toFixed(3) }}V · 最高{{ Math.max(...cellVoltages).toFixed(3) }}V · 压差{{ (Math.max(...cellVoltages) - Math.min(...cellVoltages)).toFixed(3) }}V
            </el-tag>
          </div>
        </template>
        <BmsCellVoltageChart :voltages="cellVoltages" :cell-count="16" height="220px" />
      </el-card>

      <!-- Cell voltage history trend -->
      <BmsCellVoltageHistoryChart ref="cellVoltageHistoryRef" :device-id="deviceId" :cell-count="16" />

      <!-- Temperature & MOS status -->
      <el-row :gutter="20" style="margin-top: 20px;">
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header><span>温度探头</span></template>
            <div class="temp-list">
              <div v-for="(temp, i) in tempProbes" :key="i" class="temp-item">
                <span class="temp-label">探头{{ i + 1 }}</span>
                <el-tag :type="temp > 60 ? 'danger' : temp > 45 ? 'warning' : 'success'" size="small">
                  {{ temp.toFixed(1) }}°C
                </el-tag>
              </div>
              <el-empty v-if="tempProbes.length === 0" description="无温度数据" :image-size="60" />
            </div>
          </el-card>
        </el-col>
        <el-col :xs="24" :sm="12">
          <el-card shadow="hover">
            <template #header><span>MOS状态</span></template>
            <BmsMosStatus :data="latestData" />
          </el-card>
        </el-col>
      </el-row>

      <!-- Protection status -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header>
          <div class="protection-header">
            <span class="protection-title">保护状态</span>
            <el-tag v-if="hasActiveProtection" type="danger" size="small" class="protection-summary">有保护触发</el-tag>
            <el-tag v-else-if="latestData" type="success" size="small" class="protection-summary">全部正常</el-tag>
          </div>
        </template>
        <BmsProtectionGrid :data="latestData" />
      </el-card>

      <!-- Realtime data stream (collapsible) -->
      <el-collapse v-model="activeCollapses" style="margin-top: 20px;">
        <el-collapse-item name="realtime">
          <template #title>
            <span>实时数据流</span>
            <el-tag size="small" type="success" style="margin-left: 8px;">{{ realtimeCount }} 条</el-tag>
          </template>
          <RealtimeDataList
            :items="realtimeDataItems"
            :auto-scroll="true"
            :device-type="device?.device_type"
            @clear="clearRealtimeData"
          />
        </el-collapse-item>
      </el-collapse>

      <!-- Command frequency & operations (collapsible) -->
      <el-collapse v-model="activeCollapses" style="margin-top: 20px;">
        <el-collapse-item name="config">
          <template #title><span>指令频率配置</span></template>
          <CommandFrequencySection :device-id="deviceId" embedded />
          <DeviceControlPanel :device-id="deviceId" />
        </el-collapse-item>
      </el-collapse>

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
import { Lightning, Odometer, PieChart, DataLine } from '@element-plus/icons-vue'
import DeviceHeader from '../shared/DeviceHeader.vue'
import DeviceInfoCard from '../shared/DeviceInfoCard.vue'
import HistoryChartSection from '../shared/HistoryChartSection.vue'
import CommandFrequencySection from '../shared/CommandFrequencySection.vue'
import DeviceControlPanel from '../shared/DeviceControlPanel.vue'
import BmsCellVoltageChart from './BmsCellVoltageChart.vue'
import BmsCellVoltageHistoryChart from './BmsCellVoltageHistoryChart.vue'
import BmsProtectionGrid from './BmsProtectionGrid.vue'
import BmsMosStatus from './BmsMosStatus.vue'
import RealtimeDataList from '@/components/data/RealtimeDataList.vue'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
import { useDeviceData } from '@/composables/useDeviceData'
import { getDeviceTypeLabel } from '@/utils/deviceType'

const router = useRouter()
const route = useRoute()
const deviceId = ref(Number(route.params.id) || null)

const historyChartRef = ref<InstanceType<typeof HistoryChartSection> | null>(null)
const cellVoltageHistoryRef = ref<InstanceType<typeof BmsCellVoltageHistoryChart> | null>(null)

const {
  device, loading, refreshing, syncingHA, wsConnected,
  latestData, realtimeDataItems, clearRealtimeData,
  fetchDeviceDetail, fetchLatestData,
  handleRefresh: composableHandleRefresh, handleSyncToHA,
} = useDeviceData(deviceId, { maxItems: 100, errorPrefix: '获取BMS详情失败' })

const handleRefresh = () => composableHandleRefresh(() => {
  historyChartRef.value?.fetchHistoryData()
  cellVoltageHistoryRef.value?.fetchCellVoltageHistory()
})

const deviceTypeText = computed(() => device.value ? getDeviceTypeLabel(device.value.device_type) : 'BMS')

// 折叠面板状态（默认全部折叠）
const activeCollapses = ref<string[]>([])

// 实时数据条数
const realtimeCount = computed(() => realtimeDataItems.value.length)

// Extract cell voltages from last_data: cell_voltage_1..16 or cell_v_1..16
const cellVoltages = computed<number[]>(() => {
  if (!latestData.value) return []
  const result: number[] = []
  for (let i = 1; i <= 16; i++) {
    const v = latestData.value[`cell_voltage_${i}`] ?? latestData.value[`cell_v_${i}`]
    if (typeof v === 'number' && v > 0) result.push(v)
  }
  return result
})

// Extract temperature probes: temp_1..4 or temperature_1..4
const tempProbes = computed<number[]>(() => {
  if (!latestData.value) return []
  const result: number[] = []
  for (let i = 1; i <= 8; i++) {
    const v = latestData.value[`temp_${i}`] ?? latestData.value[`temperature_${i}`]
    if (typeof v === 'number' && v !== 0) result.push(v)
  }
  return result
})

const hasActiveProtection = computed(() => {
  if (!latestData.value) return false
  const ps = latestData.value.protection_status
  if (ps !== undefined && ps > 0) return true
  return Object.entries(latestData.value)
    .filter(([key]) => key.startsWith('protection_') && key !== 'protection_status')
    .some(([, val]) => val === 1 || val > 0)
})

function formatNum(v: number | undefined | null, digits: number = 2): string {
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
.bms-detail { padding: 0; }

/* 卡片头部：标题单行不收缩；摘要（长 el-tag）空间不足时换到第二行，防移动端逐字断行 */
.cell-voltage-header,
.protection-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.cell-voltage-title,
.protection-title {
  white-space: nowrap;
  flex-shrink: 0;
}
.cell-voltage-summary,
.protection-summary {
  margin-left: auto;
}

.temp-list { display: flex; flex-direction: row; gap: 8px; }
.temp-item {
  flex: 1;
  display: flex; justify-content: space-between; align-items: center;
  padding: 8px 12px; background: var(--el-fill-color-lighter); border-radius: 6px;
}
.temp-label { font-size: 13px; color: var(--el-text-color-regular); }
</style>
