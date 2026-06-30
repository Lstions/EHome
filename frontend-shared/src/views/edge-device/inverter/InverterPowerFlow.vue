<template>
  <div class="power-flow-container">
    <svg class="power-flow-svg" viewBox="0 0 600 200" xmlns="http://www.w3.org/2000/svg">
      <!-- Nodes -->
      <!-- PV (top-left) -->
      <g class="node pv">
        <circle cx="80" cy="50" r="28" :class="{ active: pvPower > 0 }" />
        <text x="80" y="55" text-anchor="middle" class="node-icon">☀️</text>
        <text x="80" y="95" text-anchor="middle" class="node-label">PV输入</text>
        <text x="80" y="110" text-anchor="middle" class="node-value">{{ formatPower(pvPower) }}</text>
      </g>

      <!-- Inverter (center) -->
      <g class="node inverter">
        <circle cx="300" cy="50" r="32" class="active" />
        <text x="300" y="55" text-anchor="middle" class="node-icon">⚡</text>
        <text x="300" y="100" text-anchor="middle" class="node-label">逆变器</text>
      </g>

      <!-- Load (right) -->
      <g class="node load">
        <circle cx="520" cy="50" r="28" :class="{ active: loadPower > 0 }" />
        <text x="520" y="55" text-anchor="middle" class="node-icon">🏠</text>
        <text x="520" y="95" text-anchor="middle" class="node-label">负载</text>
        <text x="520" y="110" text-anchor="middle" class="node-value">{{ formatPower(loadPower) }}</text>
      </g>

      <!-- Battery (bottom) -->
      <g class="node battery">
        <circle cx="300" cy="160" r="28" :class="{ charging: batteryCurrent < 0, discharging: batteryCurrent > 0 }" />
        <text x="300" y="165" text-anchor="middle" class="node-icon">🔋</text>
        <text x="300" y="145" text-anchor="middle" class="node-label">{{ batteryLabel }}</text>
      </g>

      <!-- Flow lines -->
      <!-- PV → Inverter -->
      <g class="flow-line">
        <line x1="108" y1="50" x2="268" y2="50" :class="{ active: pvPower > 0 }" />
        <polygon v-if="pvPower > 0" points="260,45 268,50 260,55" class="arrow" />
      </g>

      <!-- Inverter → Load -->
      <g class="flow-line">
        <line x1="332" y1="50" x2="492" y2="50" :class="{ active: loadPower > 0 }" />
        <polygon v-if="loadPower > 0" points="484,45 492,50 484,55" class="arrow" />
      </g>

      <!-- Inverter ↔ Battery -->
      <g class="flow-line">
        <line x1="300" y1="82" x2="300" y2="132" :class="{ charging: batteryCurrent < 0, discharging: batteryCurrent > 0 }" />
        <polygon v-if="batteryCurrent < 0" points="295,124 300,132 305,124" class="arrow charging" />
        <polygon v-if="batteryCurrent > 0" points="295,90 300,82 305,90" class="arrow discharging" />
      </g>
    </svg>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatPower } from '@/utils/format'

const props = defineProps<{
  pvPower: number
  loadPower: number
  batteryVoltage: number
  batteryCurrent: number  // positive = discharging, negative = charging
}>()

// click-node event removed — no parent listens to it

const batteryLabel = computed(() => {
  if (props.batteryCurrent < 0) return `充电 ${Math.abs(props.batteryCurrent).toFixed(1)}A`
  if (props.batteryCurrent > 0) return `放电 ${props.batteryCurrent.toFixed(1)}A`
  return `${props.batteryVoltage.toFixed(1)}V`
})

</script>

<style scoped>
.power-flow-container {
  display: flex;
  justify-content: center;
  padding: 12px 0;
}
.power-flow-svg {
  width: 100%;
  max-width: 600px;
  height: 200px;
}
.node circle {
  fill: var(--el-fill-color-light);
  stroke: var(--el-border-color);
  stroke-width: 2;
  transition: all 0.3s;
}
.node circle.active {
  fill: var(--el-color-success-light-7);
  stroke: var(--el-color-success);
}
.node circle.charging {
  fill: var(--el-color-primary-light-7);
  stroke: var(--el-color-primary);
}
.node circle.discharging {
  fill: var(--el-color-warning-light-7);
  stroke: var(--el-color-warning);
}
.node-icon {
  font-size: 20px;
}
.node-label {
  font-size: 11px;
  fill: var(--el-text-color-secondary);
}
.node-value {
  font-size: 12px;
  font-weight: 600;
  fill: var(--el-text-color-primary);
}
.flow-line line {
  stroke: var(--el-border-color);
  stroke-width: 2;
  stroke-dasharray: 4 4;
}
.flow-line line.active {
  stroke: var(--el-color-success);
  stroke-dasharray: none;
}
.flow-line line.charging {
  stroke: var(--el-color-primary);
  stroke-dasharray: none;
}
.flow-line line.discharging {
  stroke: var(--el-color-warning);
  stroke-dasharray: none;
}
.arrow {
  fill: var(--el-color-success);
}
.arrow.charging {
  fill: var(--el-color-primary);
}
.arrow.discharging {
  fill: var(--el-color-warning);
}
</style>
