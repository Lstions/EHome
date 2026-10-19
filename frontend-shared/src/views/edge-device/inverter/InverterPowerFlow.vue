<template>
  <div class="power-flow-container">
    <svg class="power-flow-svg" viewBox="0 0 640 260" xmlns="http://www.w3.org/2000/svg">
      <defs>
        <marker id="pf-arrow" markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto">
          <path d="M0,0 L6,3 L0,6 z" class="marker-fill" />
        </marker>
      </defs>

      <!-- 光伏 -->
      <g class="pf-node pv">
        <circle cx="80" cy="60" r="30" :class="{ on: pvPower > 0 }" />
        <text x="80" y="66" text-anchor="middle" class="pf-icon">☀</text>
        <text x="80" y="108" text-anchor="middle" class="pf-label">光伏 PV</text>
        <text x="80" y="124" text-anchor="middle" class="pf-value">{{ fmtPower(pvPower) }}</text>
      </g>

      <!-- 电网 -->
      <g class="pf-node grid">
        <circle cx="80" cy="200" r="30" :class="{ on: gridActive }" />
        <text x="80" y="206" text-anchor="middle" class="pf-icon">🏭</text>
        <text x="80" y="248" text-anchor="middle" class="pf-label">电网</text>
        <text x="80" y="176" text-anchor="middle" class="pf-value">{{ fmtVolt(gridVoltage) }}</text>
      </g>

      <!-- 逆变器 -->
      <g class="pf-node inverter">
        <circle cx="320" cy="130" r="38" class="on" />
        <text x="320" y="138" text-anchor="middle" class="pf-icon">⚡</text>
        <text x="320" y="188" text-anchor="middle" class="pf-label">逆变器</text>
        <text x="320" y="204" text-anchor="middle" class="pf-value">{{ workModeLabel }}</text>
      </g>

      <!-- 负载 -->
      <g class="pf-node load">
        <circle cx="560" cy="60" r="30" :class="{ on: loadPower > 0 }" />
        <text x="560" y="66" text-anchor="middle" class="pf-icon">🏠</text>
        <text x="560" y="108" text-anchor="middle" class="pf-label">负载</text>
        <text x="560" y="124" text-anchor="middle" class="pf-value">{{ fmtPower(loadPower) }}</text>
      </g>

      <!-- 电池 -->
      <g class="pf-node battery">
        <circle cx="560" cy="200" r="30" :class="batteryClass" />
        <text x="560" y="206" text-anchor="middle" class="pf-icon">🔋</text>
        <text x="560" y="248" text-anchor="middle" class="pf-label">{{ batteryLabel }}</text>
        <text x="560" y="176" text-anchor="middle" class="pf-value">{{ fmtVolt(batteryVoltage) }} · {{ capacityPct }}%</text>
      </g>

      <!-- 连线：PV -> 逆变器 -->
      <path d="M110,60 C200,60 240,110 282,120" class="pf-link" :class="{ on: pvPower > 0 }" marker-end="url(#pf-arrow)" />
      <!-- 连线：电网 -> 逆变器 -->
      <path d="M110,200 C200,200 240,150 282,140" class="pf-link" :class="{ on: gridActive }" marker-end="url(#pf-arrow)" />
      <!-- 连线：逆变器 -> 负载 -->
      <path d="M358,120 C400,110 440,60 530,60" class="pf-link" :class="{ on: loadPower > 0 }" marker-end="url(#pf-arrow)" />
      <!-- 连线：逆变器 <-> 电池 -->
      <path d="M358,140 C400,150 440,200 530,200" class="pf-link" :class="batteryClass" marker-end="url(#pf-arrow)" />
    </svg>

    <div class="pf-legend">
      <span><i class="dot on" /> 工作中</span>
      <span><i class="dot idle" /> 空闲</span>
      <span class="pf-legend-note">箭头方向 = 能量流向；数据来自各传感器最新值</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatPower } from '@/utils/format'

const props = defineProps<{
  /** 光伏总功率（pv1_power + pv2_power + …） */
  pvPower: number
  /** 负载有功功率（output_active_power） */
  loadPower: number
  /** 电池电压（battery_voltage，单位 V） */
  batteryVoltage: number
  /** 电池充电电流（battery_charge_current，A）——“充电”= 该字段 > 0 */
  batteryChargeCurrent: number
  /** 电池放电电流（battery_discharge_current，A）——“放电”= 该字段 > 0 */
  batteryDischargeCurrent: number
  /** 电池剩余容量（battery_capacity，%） */
  batteryCapacity: number
  /** 市电电压（grid_voltage，V） */
  gridVoltage: number
  /** 工作模式数值（work_mode），用于中心节点文案 */
  workMode: number | null
  /** 输出视在功率（output_apparent_power，VA），仅用于展示 */
  apparentPower?: number
}>()

/**
 * 工作模式解码 —— 与 InverterStatusGrid 同表。
 *
 * ⚠ 数值↔字母对应来自驱动（inverter_techfine.go:516 workModeMap）：
 *   P=0 S=1 L=2 B=3 F=4 D=5 X=6
 * 标签里带上字母是为了让"界面显示的模式"和"设备回的字符"能一眼对上 ——
 * 这类映射一旦漂移不会报错，只会静默说错话。
 */
const WORK_MODE_MAP: Record<number, string> = {
  0: '开机 Power-on', 1: '待机 Standby', 2: '市电 Line', 3: '电池 Battery',
  4: '故障 Fault', 5: '关机 Shutdown', 6: '测试 Test',
}

const workModeLabel = computed(() => {
  const m = props.workMode
  if (m === null || m === undefined || isNaN(Number(m))) return '—'
  return WORK_MODE_MAP[Number(m)] ?? `模式${m}`
})

/**
 * 电池状态：**充电电流与放电电流是两个独立字段**，不是同一个量的正负号。
 *
 * ⚠ 这里必须区分，不能简化成 single signed current：
 * 泰琪丰用的是 battery_charge_current / battery_discharge_current 两个寄存器，
 * 两者都可能为 0（浮充/待机），也可能同时非 0（边充边放的理论态）。
 * 早期实现读的是不存在的 battery_current，于是永远走"0"分支 —— 箭头恒为灰色。
 */
const batteryClass = computed(() => {
  if (props.batteryChargeCurrent > 0) return { charging: true }
  if (props.batteryDischargeCurrent > 0) return { discharging: true }
  return {}
})

const batteryLabel = computed(() => {
  if (props.batteryChargeCurrent > 0) return `充电 ${props.batteryChargeCurrent.toFixed(1)}A`
  if (props.batteryDischargeCurrent > 0) return `放电 ${props.batteryDischargeCurrent.toFixed(1)}A`
  return '静止'
})

/**
 * 物理闸门：把不可能的读数当作"没有数据"。
 *
 * 2026-10-19 实测：后端曾把 HBMS1 的 16 位二进制串当电池电压，写库 2087 条
 * 1e12 伏特。解析器根因已修，但历史脏数据可能仍是某些传感器的"最新值"，
 * 而功率流是整页最显眼的组件 —— 让它显示 1000000000000.0V 比显示"—"糟得多。
 */
function sane(v: number | undefined | null, min: number, max: number): number | null {
  if (v === undefined || v === null) return null
  const n = Number(v)
  if (isNaN(n) || n < min || n > max) return null
  return n
}

const capacityPct = computed(() => {
  const v = sane(props.batteryCapacity, 0, 100)
  return v === null ? '—' : v.toFixed(0)
})

/** 市电"有电"的判据同样走闸门：1e12 V 不是"有电"，是坏数据。 */
const gridActive = computed(() => sane(props.gridVoltage, 1, 1000) !== null)

function fmtPower(w: number | undefined | null): string {
  const v = sane(w, -20000, 20000)
  return v === null ? '—' : formatPower(v)
}

function fmtVolt(v: number | undefined | null): string {
  const n = sane(v, 0, 1000)
  return n === null ? '—' : `${n.toFixed(1)}V`
}
</script>

<style scoped>
.power-flow-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 12px 0;
  width: 100%;
  overflow-x: auto;
}
.power-flow-svg { width: 100%; max-width: 640px; height: 260px; min-width: 560px; }

@media (max-width: 480px) {
  .power-flow-container { align-items: flex-start; }
  .power-flow-svg { min-width: 560px; }
}

.pf-node circle {
  fill: var(--el-fill-color-light);
  stroke: var(--el-border-color);
  stroke-width: 2;
  transition: all 0.3s;
}
.pf-node circle.on { fill: var(--el-color-success-light-7); stroke: var(--el-color-success); }
.pf-node circle.charging { fill: var(--el-color-primary-light-7); stroke: var(--el-color-primary); }
.pf-node circle.discharging { fill: var(--el-color-warning-light-7); stroke: var(--el-color-warning); }
.pf-icon { font-size: 22px; }
.pf-label { font-size: 12px; fill: var(--el-text-color-secondary); }
.pf-value { font-size: 13px; font-weight: 600; fill: var(--el-text-color-primary); }

.pf-link {
  fill: none;
  stroke: var(--el-border-color);
  stroke-width: 2;
  stroke-dasharray: 5 5;
  transition: stroke 0.3s;
}
.pf-link.on { stroke: var(--el-color-success); stroke-dasharray: none; }
.pf-link.charging { stroke: var(--el-color-primary); stroke-dasharray: none; }
.pf-link.discharging { stroke: var(--el-color-warning); stroke-dasharray: none; }
.marker-fill { fill: var(--el-text-color-secondary); }

.pf-legend {
  display: flex;
  gap: 16px;
  align-items: center;
  margin-top: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  flex-wrap: wrap;
  justify-content: center;
}
.pf-legend .dot {
  display: inline-block;
  width: 8px; height: 8px;
  border-radius: 50%;
  margin-right: 4px;
}
.pf-legend .dot.on { background: var(--el-color-success); }
.pf-legend .dot.idle { background: var(--el-border-color); }
.pf-legend-note { color: var(--el-text-color-placeholder); }
</style>
