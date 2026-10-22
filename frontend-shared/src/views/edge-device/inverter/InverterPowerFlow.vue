<template>
  <div class="power-flow-container">
    <!--
      窄屏（≤768px）：**不用桌面那张 640 固有宽的 SVG**。
      2026-10-20 生产实测：345px 卡片里塞 640×260 的图，节点坐标 x=80/320/560
      ⇒ 右侧「负载 / 电池」两个节点直接出屏，必须横向拖动才能看到一半内容；
      而等比缩放又会把 12/13px 的标签压到 6px 读不出来（这个死结已试过三次，见下方 CSS 注释）。
      改用 DOM 版：上排 PV / 电网，中间逆变器，下排负载 / 电池，
      文字是真实 DOM 文本（12~15px），宽度自适应，零横滚。

      2026-10-23 用户报障（「明明电池放电，方向却指向电池」）：
      改前这里上下各只有一个写死的「↓」，与电池的实际方向无关 —— 放电时画面
      依然显示「能量从上往下流」，这正是用户看到的错误。现在每个连接位都是
      **各列独立**的箭头（topArrow / bottomArrow 按该支路真实方向取值）：
      电池放电 = 电池 → 逆变器 = 下排右列箭头朝上「↑」。
    -->
    <div v-if="isMobileViewport" class="pf-mobile" data-testid="power-flow-mobile">
      <div class="pfm-row">
        <div class="pfm-node" :class="{ on: pv.active }" data-branch="pv">
          <span class="pfm-icon">☀</span>
          <span class="pfm-label">光伏 PV</span>
          <span class="pfm-value">{{ fmtPower(pvPower) }}</span>
        </div>
        <div class="pfm-node" :class="{ on: grid.active }" data-branch="grid">
          <span class="pfm-icon">🏭</span>
          <span class="pfm-label">电网</span>
          <span class="pfm-value">{{ fmtVolt(gridVoltage) }}</span>
        </div>
      </div>
      <div class="pfm-arrow-row" role="img" :aria-label="topArrowLabel">
        <span class="pfm-arrow" :class="arrowTone(pv)" aria-hidden="true">{{ topArrow(pv) }}</span>
        <span class="pfm-arrow" :class="arrowTone(grid)" aria-hidden="true">{{ topArrow(grid) }}</span>
      </div>
      <div class="pfm-hub">
        <span class="pfm-hub-icon">⚡</span>
        <span class="pfm-hub-label">逆变器</span>
        <span class="pfm-hub-value">{{ workModeLabel }}</span>
      </div>
      <div class="pfm-arrow-row" role="img" :aria-label="bottomArrowLabel">
        <span class="pfm-arrow" :class="arrowTone(load)" aria-hidden="true">{{ bottomArrow(load) }}</span>
        <span class="pfm-arrow" :class="arrowTone(battery)" aria-hidden="true">{{ bottomArrow(battery) }}</span>
      </div>
      <div class="pfm-row">
        <div class="pfm-node" :class="{ on: load.active }" data-branch="load">
          <span class="pfm-icon">🏠</span>
          <span class="pfm-label">负载</span>
          <span class="pfm-value">{{ fmtPower(loadPower) }}</span>
        </div>
        <div class="pfm-node" :class="batteryClass" data-branch="battery">
          <span class="pfm-icon">🔋</span>
          <span class="pfm-label">{{ battery.label }}</span>
          <span class="pfm-value">{{ fmtVolt(batteryVoltage) }} · {{ capacityPct }}%</span>
        </div>
      </div>
    </div>

    <svg v-else class="power-flow-svg" viewBox="0 0 640 260" xmlns="http://www.w3.org/2000/svg"
         role="img" :aria-label="svgAriaLabel">
      <!--
        箭头头部按支路语义分色（2026-10-23）：
        改前只有一个灰色 marker，无论连线是绿/蓝/橙，箭头都是灰的 —— 方向信息里最关键的
        「箭头」反而是最弱的一环。现在每个方向语义一个 marker，与连线同色，
        保证「箭头指向 + 颜色」双重一致（不靠颜色单通道，方向文字在 <title> 与 aria-label 里）。
      -->
      <defs>
        <marker id="pf-arrow" markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto">
          <path d="M0,0 L6,3 L0,6 z" class="marker-fill" />
        </marker>
        <marker id="pf-arrow-flow" markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto">
          <path d="M0,0 L6,3 L0,6 z" class="marker-fill-flow" />
        </marker>
        <marker id="pf-arrow-charge" markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto">
          <path d="M0,0 L6,3 L0,6 z" class="marker-fill-charging" />
        </marker>
        <marker id="pf-arrow-discharge" markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto">
          <path d="M0,0 L6,3 L0,6 z" class="marker-fill-discharging" />
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
        <circle cx="80" cy="60" r="30" :class="{ on: pv.active }" />
        <text x="80" y="66" text-anchor="middle" class="pf-icon">☀</text>
        <text x="80" y="112" text-anchor="middle" class="pf-label">光伏 PV</text>
        <text x="80" y="130" text-anchor="middle" class="pf-value">{{ fmtPower(pvPower) }}</text>
      </g>

      <!-- 电网（圆落在 y170..230）：数值在圆上方，标签在圆下方 -->
      <g class="pf-node grid">
        <circle cx="80" cy="200" r="30" :class="{ on: grid.active }" />
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
        <circle cx="560" cy="60" r="30" :class="{ on: load.active }" />
        <text x="560" y="66" text-anchor="middle" class="pf-icon">🏠</text>
        <text x="560" y="112" text-anchor="middle" class="pf-label">负载</text>
        <text x="560" y="130" text-anchor="middle" class="pf-value">{{ fmtPower(loadPower) }}</text>
      </g>

      <!-- 电池（圆落在 y170..230）：数值在圆上方，状态标签在圆下方 -->
      <g class="pf-node battery">
        <circle cx="560" cy="200" r="30" :class="batteryClass" />
        <text x="560" y="206" text-anchor="middle" class="pf-icon">🔋</text>
        <text x="560" y="164" text-anchor="middle" class="pf-value">{{ fmtVolt(batteryVoltage) }} · {{ capacityPct }}%</text>
        <text x="560" y="252" text-anchor="middle" class="pf-label">{{ battery.label }}</text>
      </g>

      <!--
        连线：**方向由数据决定**，不再写死。
        · PV / 电网 → 逆变器：purely 单向（这两条支路的能量只可能流向逆变器）；
        · 逆变器 → 负载：负载只消耗，单向；
        · 电池 ⇄ 逆变器：随充放电改**起点与终点**，箭头（marker-end）自然跟着反转。
          放电时 d 从电池端出发、指向逆变器 —— 这是本轮修的核心缺陷。
        每条连线带 <title>，悬停可读方向说明；role/aria 交给外层 svg 的 aria-label。
      -->
      <path d="M110,60 C200,60 240,110 282,120" class="pf-link" :class="{ on: pv.active }"
            :marker-end="arrowMarker(pv)"><title>{{ pv.text }}</title></path>
      <path d="M110,200 C200,200 240,150 282,140" class="pf-link" :class="{ on: grid.active }"
            :marker-end="arrowMarker(grid)"><title>{{ grid.text }}</title></path>
      <path d="M358,120 C400,110 440,60 530,60" class="pf-link" :class="{ on: load.active }"
            :marker-end="arrowMarker(load)"><title>{{ load.text }}</title></path>
      <path :d="batteryPath" class="pf-link" :class="batteryClass"
            :marker-end="arrowMarker(battery)"><title>{{ battery.text }}</title></path>
    </svg>

    <div class="pf-legend">
      <span><i class="dot on" /> 工作中</span>
      <span><i class="dot idle" /> 空闲</span>
      <span class="pf-legend-note">箭头方向 = 能量流向；逐支路按实测读数判定</span>
      <!-- 电网没有功率传感器：方向是推定的，必须与实测支路区分开 -->
      <span v-if="grid.inferred && grid.active" class="pf-legend-note is-inferred">
        电网方向为推定（协议只提供电压/频率，无市电功率）
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { formatPower } from '@/utils/format'
import { batteryFlow, gridFlow, loadFlow, pvFlow, type BranchFlow } from './powerFlow'

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
 * ⚠ 数值↔字母对应来自驱动（inverter_techfine.go:424 workModeMap）：
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
 * 每条支路的能量方向 —— 全部来自 powerFlow.ts 的**纯函数单一事实源**。
 *
 * 为什么不在组件里各判一次（改前的形态）：
 *   电池方向在标签里判了一次（读两个电流字段），箭头却在模板里写死成「逆变器 → 电池」。
 *   两套判定之间没有任何约束，于是出现「文字说放电、箭头指电池」的自相矛盾（用户报障）。
 *   现在方向只有一处结论，标签、节点配色、SVG 箭头、窄屏箭头全部由它派生。
 */
const pv = computed(() => pvFlow(props.pvPower))
const grid = computed(() => gridFlow(props.gridVoltage))
const load = computed(() => loadFlow(props.loadPower))
const battery = computed(() => batteryFlow(props.batteryChargeCurrent, props.batteryDischargeCurrent))

/** 电池节点/连线配色（既有测试与 CSS 类名契约保持不变）。 */
const batteryClass = computed(() => {
  const cls = battery.value.cls
  if (cls === 'charging') return { charging: true }
  if (cls === 'discharging') return { discharging: true }
  return {}
})

/**
 * 电池连线的 path：方向不同 ⇒ **起点/终点互换**（否则 marker-end 永远落在电池端，
 * 正是用户看到的"放电却指向电池"）。弧线控制点同步镜像，保证曲线形状不突变。
 */
const batteryPath = computed(() => {
  switch (battery.value.dir) {
    case 'in': return 'M530,200 C440,200 400,150 358,140' // 电池 → 逆变器（放电）
    case 'out': return 'M358,140 C400,150 440,200 530,200' // 逆变器 → 电池（充电）
    default: return 'M358,140 C400,150 440,200 528,199' // 静止：无箭头，仅示意连接
  }
})

/**
 * 只有「有可判定流动」的支路才画箭头；静止/矛盾读数不画，绝不猜方向。
 * 箭头头部与连线同色（充=primary、放=warning、其余流动=success），
 * 这样"箭头指向"和"颜色语义"不会互相矛盾。
 */
function arrowMarker(flow: BranchFlow): string | undefined {
  if (flow.dir === 'none') return undefined
  const b = battery.value
  if (flow === b) {
    if (b.cls === 'charging') return 'url(#pf-arrow-charge)'
    if (b.cls === 'discharging') return 'url(#pf-arrow-discharge)'
  }
  return 'url(#pf-arrow-flow)'
}

/** 窄屏：连接位箭头（上排 = 节点在逆变器上方；下排 = 节点在逆变器下方）。 */
function topArrow(flow: BranchFlow): string {
  if (flow.dir === 'in') return '↓'
  if (flow.dir === 'out') return '↑'
  return '·'
}
function bottomArrow(flow: BranchFlow): string {
  if (flow.dir === 'out') return '↓'
  if (flow.dir === 'in') return '↑'
  return '·'
}

/**
 * 窄屏箭头色调：方向是本组件要传达的头号信息（用户报障点），
 * 所以有流动的箭头必须高对比 —— 不能用 placeholder 灰（实测 #a8abb2 on #fff = 2.3:1，
 * 12px 正文要求 4.5:1）；电池支路沿用充/放电语义色，其余流入支路用 success。
 */
function arrowTone(flow: BranchFlow): string {
  if (flow.dir === 'none') return 'is-idle'
  const b = battery.value
  if (flow === b) {
    if (b.cls === 'charging') return 'is-charging'
    if (b.cls === 'discharging') return 'is-discharging'
  }
  return 'is-flow'
}

/** 读屏器的方向摘要（窄屏两行箭头各自有标签，不能只念「↓ ↓」）。 */
const topArrowLabel = computed(() =>
  `上排连接方向：光伏${dirWord(pv.value)}，电网${dirWord(grid.value)}`)
const bottomArrowLabel = computed(() =>
  `下排连接方向：负载${dirWord(load.value)}，电池${dirWord(battery.value)}`)
function dirWord(flow: BranchFlow): string {
  if (flow.dir === 'in') return '流入逆变器'
  if (flow.dir === 'out') return '流出逆变器'
  return '无流动'
}

const svgAriaLabel = computed(() =>
  `功率流向：光伏${dirWord(pv.value)}，电网${dirWord(grid.value)}${grid.value.inferred && grid.value.active ? '（推定）' : ''}，`
  + `负载${dirWord(load.value)}，电池${battery.value.label}（${dirWord(battery.value)}）`)

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
  /* 复核实测：secondary #909399 on success-light-9 #e8f2eb = 2.7:1，12px 正文需 ≥4.5:1 */
  color: var(--el-text-color-regular);
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
/*
  连接位箭头：**每列一个**（2026-10-23）。改前是整行一个写死的「↓」，
  电池放电时也显示向下 —— 正是用户报障的「方向指向电池」的窄屏版本。
  现在列宽与上面的节点对齐，方向由该支路真实流向决定（↑/↓/·）。
*/
.pfm-arrow-row {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
/*
  箭头是本组件要传达的头号信息（用户报障「方向不对」），因此：
  · 18px / 700（改前 14px / 400 + placeholder 灰 2.3:1，方向几乎看不出来）；
  · 有流动的箭头用语义色，实测对比度 flow 3.45:1 / charging 4.20:1 / discharging 3.35:1；
  · 「无流动」用 secondary 灰（3.9:1），而不是 placeholder（2.3:1）。

  ⚠ 判据用 WCAG 1.4.11「非文本对比度 ≥3:1」，不是 1.4.3 的 4.5:1：
  这些 ↑/↓ 是**图形指示符**（方向本身另有「放电 6.0A」文字与节点配色承担），
  按图形对象取 3:1。暗色主题下语义色自动反向（success-dark-2 在 #141414 上 5.35:1），无需另写规则。
*/
.pfm-arrow {
  text-align: center;
  font-size: 18px;
  font-weight: 700;
  line-height: 1;
  color: var(--el-text-color-secondary);
}
.pfm-arrow.is-flow { color: var(--el-color-success-dark-2); }
.pfm-arrow.is-charging { color: var(--el-color-primary-dark-2); }
.pfm-arrow.is-discharging { color: var(--el-color-warning-dark-2); }
.pfm-arrow.is-idle { color: var(--el-text-color-secondary); }
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
  /* 同一成功色底上的灰副文本：改 regular（5.7:1） */
  color: var(--el-text-color-regular);
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
/* 箭头头部与连线同色：方向是本组件的头号信息，不能让箭头成为最弱的一环 */
.marker-fill-flow { fill: var(--el-color-success); }
.marker-fill-charging { fill: var(--el-color-primary); }
.marker-fill-discharging { fill: var(--el-color-warning); }

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
/* 复核实测：placeholder #a8abb2 on #fff = 2.3:1（12px 正文要求 4.5:1）。
   这行是「箭头方向 = 能量流向；数据来自各传感器最新值」的解释性文字，属有效信息，不允许低对比。 */
.pf-legend-note { color: var(--el-text-color-regular); }
/* 「电网方向为推定」是**限定条件**，比普通说明更强：用 warning 文字色 + 前置标记，
   让用户一眼看出它不是实测值。 */
.pf-legend-note.is-inferred {
  color: var(--el-color-warning-dark-2);
  border-left: 2px solid var(--el-color-warning);
  padding-left: 6px;
}
</style>
