<template>
  <div class="power-flow-container">
    <!--
      窄屏（≤768px）：**不用桌面那张 640 固有宽的 SVG**。
      2026-10-20 生产实测：345px 卡片里塞 640×260 的图，节点坐标 x=80/320/560
      ⇒ 右侧「负载 / 电池」两个节点直接出屏，必须横向拖动才能看到一半内容；
      而等比缩放又会把 12/13px 的标签压到 6px 读不出来（这个死结已试过三次，见下方 CSS 注释）。
      改用 DOM 版：上排 PV / 电网，中间逆变器，下排负载 / 电池，
      文字是真实 DOM 文本（12~15px），宽度自适应，零横滚。
    -->
    <div v-if="isMobileViewport" class="pf-mobile">
      <div class="pfm-row">
        <div class="pfm-node" :class="{ on: pvPower > 0 }">
          <span class="pfm-icon">☀</span>
          <span class="pfm-label">光伏 PV</span>
          <span class="pfm-value">{{ fmtPower(pvPower) }}</span>
        </div>
        <div class="pfm-node" :class="{ on: gridActive }">
          <span class="pfm-icon">🏭</span>
          <span class="pfm-label">电网</span>
          <span class="pfm-value">{{ fmtVolt(gridVoltage) }}</span>
        </div>
      </div>
      <div class="pfm-arrow" aria-hidden="true">↓</div>
      <div class="pfm-hub">
        <span class="pfm-hub-icon">⚡</span>
        <span class="pfm-hub-label">逆变器</span>
        <span class="pfm-hub-value">{{ workModeLabel }}</span>
      </div>
      <div class="pfm-arrow" aria-hidden="true">↓</div>
      <div class="pfm-row">
        <div class="pfm-node" :class="{ on: loadPower > 0 }">
          <span class="pfm-icon">🏠</span>
          <span class="pfm-label">负载</span>
          <span class="pfm-value">{{ fmtPower(loadPower) }}</span>
        </div>
        <div class="pfm-node" :class="batteryClass">
          <span class="pfm-icon">🔋</span>
          <span class="pfm-label">{{ batteryLabel }}</span>
          <span class="pfm-value">{{ fmtVolt(batteryVoltage) }} · {{ capacityPct }}%</span>
        </div>
      </div>
    </div>

    <svg v-else class="power-flow-svg" viewBox="0 0 640 260" xmlns="http://www.w3.org/2000/svg">
      <defs>
        <marker id="pf-arrow" markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto">
          <path d="M0,0 L6,3 L0,6 z" class="marker-fill" />
        </marker>
      </defs>

      <!--
        ⚠ 文本 y 坐标必须落在圆**外**：圆半径 30，圆心 y=60 ⇒ 圆占据 y 30..90。
        2026-10-20 生产实测：电网节点把 pf-value 放在 y=176（圆心 200，圆占 170..230）
        正好压在圆上；电池节点 y=176 同样压住。现统一为：
          · 圆上方节点（PV/负载）：标签 y=110、数值 y=128（圆外下方）
          · 圆下方节点（电网/电池）：数值 y=166、标签 y=252（圆外上方/下方）
        且每组都留出 ≥6px 间隙，缩放后也不会贴合。
      -->
      <!-- 光伏（圆落在 y30..90） -->
      <g class="pf-node pv">
        <circle cx="80" cy="60" r="30" :class="{ on: pvPower > 0 }" />
        <text x="80" y="66" text-anchor="middle" class="pf-icon">☀</text>
        <text x="80" y="112" text-anchor="middle" class="pf-label">光伏 PV</text>
        <text x="80" y="130" text-anchor="middle" class="pf-value">{{ fmtPower(pvPower) }}</text>
      </g>

      <!-- 电网（圆落在 y170..230）：数值在圆上方，标签在圆下方 -->
      <g class="pf-node grid">
        <circle cx="80" cy="200" r="30" :class="{ on: gridActive }" />
        <text x="80" y="206" text-anchor="middle" class="pf-icon">🏭</text>
        <text x="80" y="164" text-anchor="middle" class="pf-value">{{ fmtVolt(gridVoltage) }}</text>
        <text x="80" y="252" text-anchor="middle" class="pf-label">电网</text>
      </g>

      <!-- 逆变器 -->
      <g class="pf-node inverter">
        <circle cx="320" cy="130" r="38" class="on" />
        <text x="320" y="138" text-anchor="middle" class="pf-icon">⚡</text>
        <text x="320" y="188" text-anchor="middle" class="pf-label">逆变器</text>
        <text x="320" y="204" text-anchor="middle" class="pf-value">{{ workModeLabel }}</text>
      </g>

      <!-- 负载（圆落在 y30..90） -->
      <g class="pf-node load">
        <circle cx="560" cy="60" r="30" :class="{ on: loadPower > 0 }" />
        <text x="560" y="66" text-anchor="middle" class="pf-icon">🏠</text>
        <text x="560" y="112" text-anchor="middle" class="pf-label">负载</text>
        <text x="560" y="130" text-anchor="middle" class="pf-value">{{ fmtPower(loadPower) }}</text>
      </g>

      <!-- 电池（圆落在 y170..230）：数值在圆上方，状态标签在圆下方 -->
      <g class="pf-node battery">
        <circle cx="560" cy="200" r="30" :class="batteryClass" />
        <text x="560" y="206" text-anchor="middle" class="pf-icon">🔋</text>
        <text x="560" y="164" text-anchor="middle" class="pf-value">{{ fmtVolt(batteryVoltage) }} · {{ capacityPct }}%</text>
        <text x="560" y="252" text-anchor="middle" class="pf-label">{{ batteryLabel }}</text>
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
import { computed, onUnmounted, ref } from 'vue'
import { formatPower } from '@/utils/format'

/**
 * 窄屏用 DOM 版布局（结构变化，CSS 媒体查询只能隐藏不能换结构）。
 * SSR/测试环境没有 matchMedia 时按桌面渲染，保证既有契约测试稳定。
 */
const MOBILE_MAX = 768
const isMobileViewport = ref(
  typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia(`(max-width: ${MOBILE_MAX}px)`).matches
    : false,
)
let mobileMql: MediaQueryList | null = null
const onMobileChange = (e: MediaQueryListEvent) => { isMobileViewport.value = e.matches }
if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
  mobileMql = window.matchMedia(`(max-width: ${MOBILE_MAX}px)`)
  mobileMql.addEventListener('change', onMobileChange)
}
onUnmounted(() => {
  mobileMql?.removeEventListener('change', onMobileChange)
  mobileMql = null
})

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
  /* 不再 overflow-x:auto —— 窄屏靠 viewBox 等比缩放，而不是让用户横向拖动找内容 */
  overflow: hidden;
}
/*
  2026-10-20 修（生产实测）：
    旧规则 min-width:560px 让 390px 视口下的 SVG 保持 560px 宽，
    结果是容器要横向滚动、右侧「负载/电池」被切在屏幕外，
    且固定坐标的数值文本（225.6V / 51.6V·70%）压在节点圆上。
  改为：
    · 去掉 min-width，SVG 按 viewBox="0 0 640 260" 等比缩放到容器宽度；
    · 窄屏用 aspect-ratio 保持比例，不再写死 height:260px（否则会竖向压扁变形）。
*/
.power-flow-svg {
  width: 100%;
  max-width: 640px;
  height: auto;
  aspect-ratio: 640 / 260;
  min-width: 0;
  display: block;
}

/*
  窄屏可读性（2026-10-20 二次复审 N4）：
  640 宽的 viewBox 等比缩到 300px 时 scale=0.469，.pf-label 12px 实显 **5.63px**、
  .pf-value 13px 实显 **6.09px** —— 溢出治好了，但字也看不清了。
  等比缩小在这里是错的解法：图表的意义就是读数。
  改用「小屏换布局」：
    · 允许横向滚动并给一个下限宽度（360px），保证 scale ≥ 0.56、正文 ≥ 6.7px，
      再配合下面按比例放大 SVG 内字号（font-size 用 /0.56 后的值），实显回到 11~12px；
    · 图例本就是 DOM 文本，不受 scale 影响，正常显示。
  宁可让用户轻微横滑看功率流，也不给一张读不出数字的图。
*/
/*
  窄屏（≤1100px）：**不再等比缩小整张图**，改为「保持 640 固有宽度 + 容器横向滚动」。
  为什么阈值取 1100 而不是 560：实测 769px 时容器约 479px ⇒ scale=0.748 ⇒ 标签实显
  8.98px 仍偏小；1024px 时容器 734px ⇒ 可 1:1 显示。1100 以下统一固定宽度最稳。

  演进史（三次都被实测打回，别再试第四次）：
    v1  min-width:560px + overflow-x:auto —— 溢出治不了（容器被撑破，整页横滚）；
    v2  去掉 min-width，纯 width:100% 等比缩放 —— 溢出治好了，但 390px 下
        scale=300/640=0.469，.pf-label 12px 实显 **5.63px**、.pf-value 实显 6.09px，
        读不出数字（复审 N4）；
    v3  缩到 360px 并把 SVG 内字号放大到 19/21px —— 实显仍只有 6.75px，且
        360 > 可用宽度时又产生溢出。
  根因：只要 scale < 1，放大 SVG 内 font-size 就要按 1/scale 反算，而 scale 又随
  容器宽度连续变化 ⇒ 没有一组静态字号能覆盖所有宽度。等比缩放这条路走不通。

  定稿：固定 640px 固有尺寸（scale 恒为 1，字号 12/13px 是设计值，永远可读），
  容器自己横向滚动。代价是要滑一下，收益是**每个数字都读得出来**。
*/
@media (max-width: 1100px) {
  /* 769~1100px 仍用 SVG（卡片还有 700px+，640 固有宽放得下），只需要不溢出 */
  .power-flow-container {
    display: block;
    overflow-x: auto;
    overflow-y: hidden;
    -webkit-overflow-scrolling: touch;
    padding-bottom: 8px;
  }
  .power-flow-svg {
    width: 640px;
    max-width: none;
    height: 260px;
    margin: 0;
    flex-shrink: 0;
  }
  .pf-legend { font-size: 11px; gap: 10px 14px; }
}

/* ── 窄屏 DOM 版布局（≤768px）──
   卡片内容宽实测 295~321px，两列各约 150px，图标+标签+数值三行竖排也不挤。
   节点配色与 SVG 版共用同一套语义：primary=充电、warning=放电、success=工作。 */
.pf-mobile {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 2px;
  width: 100%;
}
.pfm-row {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
.pfm-node {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  min-width: 0;
  padding: 10px 8px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--radius-md);
  background: var(--el-fill-color-lighter);
  text-align: center;
}
.pfm-node.on {
  border-color: var(--el-color-success-light-5);
  background: var(--el-color-success-light-9);
}
.pfm-node.charging {
  border-color: var(--el-color-primary-light-5);
  background: var(--el-color-primary-light-9);
}
.pfm-node.discharging {
  border-color: var(--el-color-warning-light-5);
  background: var(--el-color-warning-light-9);
}
.pfm-icon { font-size: 20px; line-height: 1.2; }
.pfm-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  /* 中文不逐字竖排；放不下就换行，不截断 */
  word-break: keep-all;
  overflow-wrap: anywhere;
}
.pfm-value {
  font-size: 15px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  overflow-wrap: anywhere;
}
.pfm-arrow {
  text-align: center;
  font-size: 14px;
  line-height: 1;
  color: var(--el-text-color-placeholder);
}
.pfm-hub {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 10px 8px;
  border: 1px solid var(--el-color-success-light-5);
  border-radius: var(--radius-md);
  background: var(--el-color-success-light-9);
}
.pfm-hub-icon { font-size: 20px; }
.pfm-hub-label {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.pfm-hub-value {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  word-break: keep-all;
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
