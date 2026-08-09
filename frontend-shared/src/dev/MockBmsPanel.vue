<template>
  <!-- 离线 UI 验证工具：注入模拟数据渲染 BMS 指标区，无需运行后端 -->
  <!-- 仅用于开发/测试环境 CDP 量化验收，不进入生产路由 -->
  <div class="mock-bms" style="padding: 16px; background: #f0f2f5; min-height: 100vh;">
    <h3 style="margin: 0 0 16px;">BMS 指标区 Mock 渲染（离线验证）</h3>

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
  </div>
</template>

<script setup lang="ts">
// Mock 数据：与生产截图一致的典型值，用于离线 UI 验证（无需后端）
import { reactive } from 'vue'
import { PieChart, Odometer, DataLine, Lightning } from '@element-plus/icons-vue'
import BmsCellVoltageChart from '@/views/edge-device/bms/BmsCellVoltageChart.vue'
import MetricStatCard from '@/components/common/MetricStatCard.vue'

const mock = reactive({
  rsoc: 97.0,
  remaining: 97.93,
  nominal: 101,
  totalVoltage: 53.150,
  current: -4.450,
  cells: [3.322, 3.322, 3.325, 3.322, 3.322, 3.323, 3.322, 3.322, 3.322, 3.321, 3.320, 3.320, 3.322, 3.323, 3.324, 3.322],
})
</script>

<style scoped>
/* 指标卡样式由共享组件 MetricStatCard 内建，此处仅保留页面级布局（若有） */
</style>
