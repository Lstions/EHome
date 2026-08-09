<template>
  <!-- 离线 UI 验证工具：注入模拟数据渲染 BMS 指标区 + 指令频率配置 + 实时数据流，无需运行后端 -->
  <!-- 仅用于开发环境 CDP 量化验收：路由由 import.meta.env.DEV 门禁，生产构建不含本 chunk -->
  <div class="mock-bms" style="padding: 16px; background: #f0f2f5; min-height: 100vh;">
    <h3 style="margin: 0 0 16px;">BMS Mock 渲染（离线验证）</h3>

    <!-- 4 张指标卡（与 BmsDetailPage 一致，统一用共享组件 MetricStatCard，数据来自 mock） -->
    <el-row :gutter="20">
      <el-col :xs="12" :sm="12" :md="6">
        <MetricStatCard label="SOC" :value="mock.rsoc.toFixed(1)" unit="%" tone="success" :progress="Math.round(mock.rsoc)">
          <template #icon><el-icon :size="20"><PieChart /></el-icon></template>
        </MetricStatCard>
      </el-col>
      <el-col :xs="12" :sm="12" :md="6">
        <MetricStatCard label="剩余容量" :value="mock.remaining.toFixed(2)" unit="Ah" tone="primary" :sub-text="`/ ${mock.nominal.toFixed(0)}Ah`">
          <template #icon><el-icon :size="20"><Odometer /></el-icon></template>
        </MetricStatCard>
      </el-col>
      <el-col :xs="12" :sm="12" :md="6">
        <MetricStatCard label="总电压" :value="mock.totalVoltage.toFixed(3)" unit="V" tone="warning">
          <template #icon><el-icon :size="20"><DataLine /></el-icon></template>
        </MetricStatCard>
      </el-col>
      <el-col :xs="12" :sm="12" :md="6">
        <MetricStatCard label="电流" :value="mock.current.toFixed(3)" unit="A" tone="primary" :direction="mock.current < 0 ? 'discharge' : mock.current > 0 ? 'charge' : 'idle'">
          <template #icon><el-icon :size="20"><Lightning /></el-icon></template>
        </MetricStatCard>
      </el-col>
    </el-row>

    <!-- 电芯电压柱状图（mock 16 节） -->
    <el-card style="margin-top: 20px;" shadow="hover">
      <template #header>
        <div style="display:flex;justify-content:space-between;align-items:center;">
          <span>电芯电压</span>
          <el-tag size="small" type="info">{{ mock.cells.length }}节 · 最低{{ Math.min(...mock.cells).toFixed(3) }}V · 最高{{ Math.max(...mock.cells).toFixed(3) }}V · 压差{{ (Math.max(...mock.cells)-Math.min(...mock.cells)).toFixed(3) }}V</el-tag>
        </div>
      </template>
      <BmsCellVoltageChart :voltages="mock.cells" :cell-count="16" height="220px" />
    </el-card>

    <!-- 指令频率配置（独立卡片 = 桌面形态） -->
    <CommandFrequencySection :device-id="MOCK_DEVICE_ID" />

    <!-- 指令频率配置（embedded = BMS 折叠面板内形态，验证双层嵌套消除） -->
    <el-card style="margin-top: 20px;" shadow="hover">
      <template #header><span>指令频率配置（embedded 形态）</span></template>
      <CommandFrequencySection :device-id="MOCK_DEVICE_ID" embedded />
    </el-card>

    <!-- 实时数据流（mock 多行 BMS 帧 + 错误帧 + 空帧，验证行高自适应与空兜底） -->
    <el-card style="margin-top: 20px;" shadow="hover">
      <template #header><span>实时数据流</span></template>
      <RealtimeDataList :items="mockRealtimeItems" :auto-scroll="false" device-type="jiabaida_bms" />
    </el-card>
  </div>
</template>

<script setup lang="ts">
// Mock 数据：与生产截图一致的典型值，用于离线 UI 验证（无需后端）
import { reactive, onUnmounted } from 'vue'
import { PieChart, Odometer, DataLine, Lightning } from '@element-plus/icons-vue'
import BmsCellVoltageChart from '@/views/edge-device/bms/BmsCellVoltageChart.vue'
import CommandFrequencySection from '@/views/edge-device/shared/CommandFrequencySection.vue'
import RealtimeDataList from '@/components/data/RealtimeDataList.vue'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
import { edgeDeviceApi, type CommandTemplateWithInterval } from '@/api/edgeDevice'
import type { DataItem } from '@/types/realtime'

const MOCK_DEVICE_ID = 999

const mock = reactive({
  rsoc: 97.0,
  remaining: 97.93,
  nominal: 101,
  totalVoltage: 53.150,
  current: -4.450,
  cells: [3.322, 3.322, 3.325, 3.322, 3.322, 3.323, 3.322, 3.322, 3.322, 3.321, 3.320, 3.320, 3.322, 3.323, 3.324, 3.322],
})

// ── dev-only：对 edgeDeviceApi 打模块级 mock 补丁（保存原实现，卸载时恢复）──
const originalGet = edgeDeviceApi.getCommandIntervals
const originalUpdate = edgeDeviceApi.updateCommandIntervals

const MOCK_COMMANDS: CommandTemplateWithInterval[] = [
  { id: 'read-basic', name: '读取基本信息', type: 'read', cmd_byte: 0x03, write_data: '', read_length: 7, delay_ms: 0, interval_ms: 5000, schedulable: true, description: '单体电压 + 温度基础帧', current_interval_ms: 0 },
  { id: 'read-cell', name: '读取单体电压', type: 'read', cmd_byte: 0x04, write_data: '', read_length: 12, delay_ms: 0, interval_ms: 5000, schedulable: true, description: '16 节单体电压', current_interval_ms: 0 },
  { id: 'read-hw', name: '读取硬件版本', type: 'read', cmd_byte: 0x05, write_data: '', read_length: 4, delay_ms: 0, interval_ms: 60000, schedulable: true, description: '硬件/固件版本一次性确认', current_interval_ms: 0 },
  { id: 'read-combined', name: '读取综合信息', type: 'read', cmd_byte: 0x0f, write_data: '', read_length: 32, delay_ms: 0, interval_ms: 1000, schedulable: true, description: 'SOC/电压/电流/MOS/保护状态综合帧', current_interval_ms: 100 },
  { id: 'read-prot-count', name: '读取保护历史次数', type: 'read', cmd_byte: 0xaa, write_data: '', read_length: 8, delay_ms: 0, interval_ms: 10000, schedulable: true, description: '历史保护事件计数', current_interval_ms: 0 },
]

edgeDeviceApi.getCommandIntervals = async () => MOCK_COMMANDS
edgeDeviceApi.updateCommandIntervals = async (_id: number, intervals: Record<string, number>) => {
  for (const cmd of MOCK_COMMANDS) {
    if (cmd.id in intervals) cmd.current_interval_ms = intervals[cmd.id]
  }
}

onUnmounted(() => {
  edgeDeviceApi.getCommandIntervals = originalGet
  edgeDeviceApi.updateCommandIntervals = originalUpdate
})

// ── 实时数据流 mock：多行综合帧 / 原始帧 / 错误帧 / 空帧 ──
const now = Date.now()
const mockRealtimeItems: DataItem[] = [
  {
    id: 'mock-combined',
    timestamp: new Date(now).toISOString(),
    data: {
      total_voltage: 53.15, current: -4.45, rsoc: 97, remaining_capacity: 97.93,
      temperature_1: 28.5, temperature_2: 29.1, temperature_3: 27.8,
      cell_voltage_max: 3.325, cell_voltage_min: 3.32, protection_status: 0, fet_status: 3,
    },
    rawData: [0xDD, 0x5A, 0x0F, 0x20, 0x02, 0x10],
    isRealtime: true,
  },
  {
    id: 'mock-error',
    timestamp: new Date(now - 60000).toISOString(),
    data: { total_voltage: 0, current: 0, error_code: 2 },
    isRealtime: true,
  },
  {
    id: 'mock-empty',
    timestamp: new Date(now - 120000).toISOString(),
    data: {},
    isRealtime: false,
  },
]
</script>

<style scoped>
/* 指标卡样式由共享组件 MetricStatCard 内建，此处仅保留页面级布局（若有） */
</style>
