<template>
  <!--
    离线 UI 验证工具：逆变器功率流向的**四种工况**并排渲染，无需后端/登录。
    仅用于开发环境 CDP 量化验收（路由由 import.meta.env.DEV 门禁，生产构建不含本 chunk）。

    为什么必须离线做：本组件只在设备详情页里出现，而详情页需要登录 + 真设备数据；
    而方向判定是纯函数（powerFlow.ts），四种工况（放电 / 充电 / 静止 / 读数矛盾）
    在一个页面里就能全部覆盖，还能同时量桌面 SVG 与窄屏 DOM 两套布局的箭头。
  -->
  <div class="mock-pf" style="padding: 16px; background: #f0f2f5; min-height: 100vh;">
    <h3 style="margin: 0 0 16px;">逆变器功率流向 四工况（离线验证）</h3>
    <el-card v-for="c in CASES" :key="c.id" :style="{ marginBottom: '16px' }" shadow="hover">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap;">
          <span>{{ c.title }}</span>
          <span style="font-size: 12px; color: #909399;">{{ c.note }}</span>
        </div>
      </template>
      <div class="pf-case" :data-case="c.id">
        <InverterPowerFlow
          :pv-power="c.pvPower"
          :load-power="c.loadPower"
          :battery-voltage="c.batteryVoltage"
          :battery-charge-current="c.batteryChargeCurrent"
          :battery-discharge-current="c.batteryDischargeCurrent"
          :battery-capacity="c.batteryCapacity"
          :grid-voltage="c.gridVoltage"
          :work-mode="c.workMode"
        />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import InverterPowerFlow from '@/views/edge-device/inverter/InverterPowerFlow.vue'

/**
 * 四个工况的场景定义。
 * 第 1 个直接复刻用户截图（PV 1W / 市电 231.5V / 负载 219W / 电池 52.3V·81% 放电 6.0A），
 * 也就是「明明电池放电，方向却指向电池」的那一帧 —— 修复后的期望是电池箭头指向逆变器。
 */
const CASES = [
  {
    id: 'discharge',
    title: '① 电池放电（用户报障的工况）',
    note: 'PV 1W · 市电 231.5V · 负载 219W · 电池 52.3V/81% 放电 6.0A ⇒ 电池 → 逆变器',
    pvPower: 1, loadPower: 219, batteryVoltage: 52.3,
    batteryChargeCurrent: 0, batteryDischargeCurrent: 6,
    batteryCapacity: 81, gridVoltage: 231.5, workMode: 3,
  },
  {
    id: 'charge',
    title: '② PV 富余，电池充电',
    note: 'PV 3200W · 负载 900W · 电池 51.8V/64% 充电 22.0A ⇒ 逆变器 → 电池',
    pvPower: 3200, loadPower: 900, batteryVoltage: 51.8,
    batteryChargeCurrent: 22, batteryDischargeCurrent: 0,
    batteryCapacity: 64, gridVoltage: 228.4, workMode: 2,
  },
  {
    id: 'idle',
    title: '③ 待机：无充放电',
    note: 'PV 0W · 负载 0W · 电池 51.5V/58% 静止 ⇒ 电池支路不画箭头（不猜方向）',
    pvPower: 0, loadPower: 0, batteryVoltage: 51.5,
    batteryChargeCurrent: 0, batteryDischargeCurrent: 0,
    batteryCapacity: 58, gridVoltage: 230.2, workMode: 1,
  },
  {
    id: 'conflict',
    title: '④ 读数矛盾：充放电流同时非零',
    note: '充电 3.0A 且放电 2.0A ⇒ 不判定方向、不画箭头，两个数都列出来',
    pvPower: 1500, loadPower: 800, batteryVoltage: 52.0,
    batteryChargeCurrent: 3, batteryDischargeCurrent: 2,
    batteryCapacity: 70, gridVoltage: 229.0, workMode: 3,
  },
]
</script>

<style scoped>
.mock-pf :deep(.el-card) {
  max-width: 720px;
}
</style>
