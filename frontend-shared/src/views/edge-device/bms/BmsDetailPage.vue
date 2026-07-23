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

      <!-- Core metrics -->
      <el-row class="core-metrics" :gutter="20" style="margin-top: 20px;">
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="metric-card">
            <div class="metric-content">
              <div class="metric-icon soc">
                <el-icon :size="20"><PieChart /></el-icon>
              </div>
              <div class="metric-info">
                <p class="metric-label">SOC</p>
                <p class="metric-value">{{ formatNum(latestData?.rsoc ?? latestData?.soc, 1) }}<span class="metric-unit">%</span></p>
                <el-progress
                  :percentage="Math.round(latestData?.rsoc ?? latestData?.soc ?? 0)"
                  :stroke-width="6"
                  :show-text="false"
                  :color="socProgressColor"
                  style="margin-top: 4px;"
                />
              </div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="metric-card">
            <div class="metric-content">
              <div class="metric-icon soh">
                <el-icon :size="20"><Odometer /></el-icon>
              </div>
              <div class="metric-info">
                <p class="metric-label">剩余容量</p>
                <p class="metric-value">{{ formatNum(latestData?.remaining_capacity, 2) }}<span class="metric-unit">Ah</span></p>
                <p class="metric-sub">/ {{ formatNum(latestData?.nominal_capacity, 0) }}Ah</p>
              </div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="metric-card">
            <div class="metric-content">
              <div class="metric-icon voltage">
                <el-icon :size="20"><DataLine /></el-icon>
              </div>
              <div class="metric-info">
                <p class="metric-label">总电压</p>
                <p class="metric-value">{{ formatNum(latestData?.total_voltage ?? latestData?.voltage, 3) }}<span class="metric-unit">V</span></p>
                <p class="metric-sub">{{ cellVoltages.length > 0 ? `${cellVoltages.length}节电芯` : '电池组端电压' }}</p>
              </div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="metric-card">
            <div class="metric-content">
              <div class="metric-icon current" :class="{ negative: (latestData?.current ?? 0) < 0 }">
                <el-icon :size="20"><Lightning /></el-icon>
              </div>
              <div class="metric-info">
                <p class="metric-label">电流</p>
                <p class="metric-value" :class="{ negative: (latestData?.current ?? 0) < 0 }">
                  {{ formatNum(latestData?.current, 3) }}<span class="metric-unit">A</span>
                </p>
                <p class="metric-sub" :class="{ negative: (latestData?.current ?? 0) < 0 }">
                  {{ (latestData?.current ?? 0) < 0 ? '放电中' : (latestData?.current ?? 0) > 0 ? '充电中' : '静止' }}
                </p>
              </div>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <!-- Cell voltage chart -->
      <el-card class="section-card" style="margin-top: 20px;" shadow="hover">
        <template #header>
          <div class="section-header">
            <span>电芯电压</span>
            <el-tag v-if="cellVoltages.length > 0" size="small" type="info">
              {{ cellVoltages.length }}节 · 最低{{ Math.min(...cellVoltages).toFixed(3) }}V · 最高{{ Math.max(...cellVoltages).toFixed(3) }}V · 压差{{ (Math.max(...cellVoltages) - Math.min(...cellVoltages)).toFixed(3) }}V
            </el-tag>
          </div>
        </template>
        <BmsCellVoltageChart :voltages="cellVoltages" :cell-count="16" height="220px" />
      </el-card>

      <!-- Cell voltage history trend -->
      <BmsCellVoltageHistoryChart ref="cellVoltageHistoryRef" :device-id="deviceId" :cell-count="16" />

      <!-- Temperature & MOS status -->
      <el-row class="status-row" :gutter="20" style="margin-top: 20px;">
        <el-col :xs="24" :sm="24" :md="12">
          <el-card class="section-card" shadow="hover">
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
        <el-col :xs="24" :sm="24" :md="12">
          <el-card class="section-card" shadow="hover">
            <template #header><span>MOS状态</span></template>
            <BmsMosStatus :data="latestData" />
          </el-card>
        </el-col>
      </el-row>

      <!-- Protection status -->
      <el-card class="section-card" style="margin-top: 20px;" shadow="hover">
        <template #header>
          <div class="section-header">
            <span>保护状态</span>
            <el-tag v-if="hasActiveProtection" type="danger" size="small">有保护触发</el-tag>
            <el-tag v-else-if="latestData" type="success" size="small">全部正常</el-tag>
          </div>
        </template>
        <BmsProtectionGrid :data="latestData" />
      </el-card>

      <!-- Desktop keeps the data stream visible; mobile uses a collapsed section to keep the page scannable. -->
      <el-card v-if="!isMobile" class="section-card" style="margin-top: 20px;" shadow="hover">
        <template #header>
          <div class="section-header">
            <span>实时数据流</span>
            <el-tag size="small" type="success">{{ realtimeCount }} 条</el-tag>
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
      <el-collapse v-else v-model="mobileSections" class="mobile-detail-collapse">
        <el-collapse-item name="realtime">
          <template #title>
            <div class="collapse-title">
              <span>实时数据流</span>
              <el-tag size="small" type="success">{{ realtimeCount }} 条</el-tag>
            </div>
          </template>
          <RealtimeDataList
            :items="realtimeDataItems"
            :max-items="100"
            :auto-scroll="true"
            :device-type="device?.device_type"
            @clear="clearRealtimeData"
          />
        </el-collapse-item>
        <el-collapse-item name="commands">
          <template #title><div class="collapse-title"><span>指令频率与设备操作</span></div></template>
          <CommandFrequencySection
            :device-id="deviceId"
            :device-type="device.device_type"
            :embedded="true"
          />
          <div class="mobile-section-divider" />
          <OperationButtons
            :device="device"
            :device-id="deviceId"
            :embedded="true"
            @operation-executed="fetchDeviceDetail"
          />
        </el-collapse-item>
      </el-collapse>

      <!-- Desktop: command frequency as its own card -->
      <CommandFrequencySection
        v-if="!isMobile"
        :device-id="deviceId"
        :device-type="device.device_type"
      />

      <!-- Operations -->
      <OperationButtons
        v-if="!isMobile"
        :device="device"
        :device-id="deviceId"
        @operation-executed="fetchDeviceDetail"
      />

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
import OperationButtons from '../shared/OperationButtons.vue'
import BmsCellVoltageChart from './BmsCellVoltageChart.vue'
import BmsCellVoltageHistoryChart from './BmsCellVoltageHistoryChart.vue'
import BmsProtectionGrid from './BmsProtectionGrid.vue'
import BmsMosStatus from './BmsMosStatus.vue'
import RealtimeDataList from '@/components/data/RealtimeDataList.vue'
import { useDeviceData } from '@/composables/useDeviceData'
import { getDeviceTypeLabel } from '@/utils/deviceType'
import { useResponsive } from '@/composables/useResponsive'

const router = useRouter()
const route = useRoute()
const deviceId = ref(Number(route.params.id) || null)
const { isMobile } = useResponsive()
const mobileSections = ref<string[]>([])

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

// 实时数据条数
const realtimeCount = computed(() => realtimeDataItems.value.length)

// SOC progress bar color — green theme to match card icon
const socProgressColor = 'var(--el-color-success)'

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
.bms-detail { padding: 0; min-width: 0; }

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 8px;
  flex-wrap: wrap;
}

.mobile-detail-collapse {
  margin-top: 12px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  background: var(--el-bg-color);
}

/* 折叠面板内两个功能块（指令频率 / 设备操作）之间的轻量分隔 */
.mobile-section-divider {
  height: 1px;
  margin: 16px 0 4px;
  background: var(--el-border-color-lighter);
}

.collapse-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  min-width: 0;
  padding-right: 4px;
}

.metric-card { cursor: default; transition: transform 0.3s, box-shadow 0.3s; min-height: 110px; border-radius: 12px; }
.metric-card:hover { transform: translateY(-2px); box-shadow: var(--shadow-md); }
.metric-card :deep(.el-card__body) { min-height: 78px; display: flex; align-items: center; }
.metric-content { display: flex; align-items: center; gap: 16px; width: 100%; min-height: 62px; }
.metric-sub-placeholder { visibility: hidden; }
/* 与节点页 stat-card 保持一致：彩色图标（无渐变底色） */
.metric-icon {
  width: 56px; height: 56px; border-radius: 12px;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
  font-size: 28px;
  background: transparent;
}
.metric-icon :deep(.el-icon) { font-size: 28px; }
.metric-icon.soc { color: var(--el-color-success); }
.metric-icon.soh { color: var(--el-color-primary); }
.metric-icon.voltage { color: var(--el-color-warning); }
.metric-icon.current { color: var(--el-color-primary); }
.metric-icon.current.negative { color: var(--el-color-danger); }
.metric-info { flex: 1; min-width: 0; display: flex; flex-direction: column; justify-content: flex-start; }
.metric-label { margin: 0 0 4px; font-size: 13px; color: var(--el-text-color-secondary); }
.metric-value { margin: 0; font-size: 28px; font-weight: 600; color: var(--el-text-color-primary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; line-height: 1.2; }
.metric-value.negative { color: var(--el-color-danger); }
.metric-unit { font-size: 16px; font-weight: 400; color: var(--el-text-color-secondary); margin-left: 2px; }
.metric-sub {
  margin: 4px 0 0; font-size: 12px; color: var(--el-text-color-secondary);
}
.metric-sub.negative { color: var(--el-color-danger); }

.temp-list { display: flex; flex-wrap: wrap; gap: 8px; }
.temp-item {
  flex: 1;
  display: flex; justify-content: space-between; align-items: center;
  padding: 8px 12px; background: var(--el-fill-color-lighter); border-radius: 6px;
}
.temp-label { font-size: 13px; color: var(--el-text-color-regular); }

@media (max-width: 768px) {
  .bms-detail :deep(.el-card__header) {
    padding: 13px 14px;
  }

  .bms-detail :deep(.el-card__body) {
    padding: 14px;
  }

  .core-metrics {
    margin-top: 12px !important;
    margin-left: -6px !important;
    margin-right: -6px !important;
  }

  .core-metrics :deep(.el-col),
  .status-row :deep(.el-col) {
    padding-left: 6px !important;
    padding-right: 6px !important;
  }

  .metric-card {
    min-height: 96px;
    border-radius: 10px;
  }

  .metric-card :deep(.el-card__body) {
    min-height: 72px;
    padding: 12px;
  }

  .metric-content {
    gap: 10px;
    min-height: 68px;
    align-items: flex-start;
  }

  /* 与节点页 MobileStatCard 保持一致：34px 图标 / 22px 图标字号 / 20px 数值 / 13px 标签 */
  .metric-icon {
    width: 34px;
    height: 34px;
    border-radius: 8px;
  }

  .metric-icon :deep(.el-icon) {
    font-size: 22px !important;
  }

  .metric-label {
    margin-bottom: 3px;
    font-size: 13px;
    line-height: 1.3;
  }

  .metric-value {
    font-size: 20px;
    line-height: 1.2;
  }

  .metric-unit {
    font-size: 13px;
  }

  .metric-sub {
    margin-top: 3px;
    font-size: 12px;
    line-height: 1.3;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .section-header :deep(.el-tag) {
    max-width: 100%;
    height: auto;
    line-height: 1.4;
    white-space: normal;
    text-align: right;
  }

  .status-row {
    margin-top: 12px !important;
    margin-left: -6px !important;
    margin-right: -6px !important;
  }

  .status-row .section-card {
    height: 100%;
  }

  .temp-list {
    gap: 6px;
  }

  .temp-item {
    flex: 1 1 calc(50% - 3px);
    min-width: 0;
    padding: 8px 10px;
  }
}
</style>
