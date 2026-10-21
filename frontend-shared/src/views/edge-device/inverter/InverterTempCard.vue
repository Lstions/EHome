<template>
  <div class="temp-card-container">
    <!-- Temperature grid -->
    <div class="temp-section">
      <p class="section-title">温度监控</p>
      <div class="temp-grid">
        <div
          v-for="temp in tempItems"
          :key="temp.key"
          class="temp-item"
          :class="tempClass(temp.value)"
        >
          <span class="temp-label">{{ temp.label }}</span>
          <span class="temp-value">{{ formatTemp(temp.value) }}</span>
        </div>
      </div>
    </div>

    <!-- Fan status -->
    <div class="fan-section" v-if="hasFanData">
      <p class="section-title">风扇状态</p>
      <div class="fan-grid">
        <div v-for="fan in fanItems" :key="fan.key" class="fan-item">
          <!--
            风扇百分比从进度条里**搬出来**放进 header 右侧（2026-10-21 用户真机反馈）：
            旧实现 text-inside + stroke-width 14 把 30% 塞在 14px 高的条里，
            文字正好压在「填充/灰底」交界处，几乎看不清、位置也不对。
            现在：标签在左；数值 + 状态 tag 在右（数值 18px/600 等宽数字，按档位着色）；
            进度条只表达比例，不再承载文字。
          -->
          <div class="fan-header">
            <span class="fan-label">{{ fan.label }}</span>
            <span class="fan-header-right">
              <span
                v-if="fan.speed !== null"
                class="fan-speed-value"
                :class="'fan-speed-' + fanSpeedLevel(fan.speed)"
              >{{ Math.round(fan.speed) }}%</span>
              <el-tag :type="fan.running ? 'success' : 'info'" effect="plain" size="small">
                {{ fan.running ? '运行' : '停止' }}
              </el-tag>
            </span>
          </div>
          <div class="fan-speed-bar" v-if="fan.speed !== null">
            <el-progress
              :percentage="fan.speed"
              :color="fanSpeedColor(fan.speed)"
              :stroke-width="8"
              :show-text="false"
            />
          </div>
        </div>
      </div>
    </div>

    <!--
      无数据时给出**原因**，而不是一句"无数据"。
      2026-10-18 实测：泰琪丰 HTEMP 的真机应答只有 6 个字段
      （"(000.0 00.0 00000 0 380.0 0…"），而驱动的 parseHTEMP 要求 >= 11 个，
      于是整条命令被判定为"帧形状不完整"并拒绝 —— 温度**在设备侧就没采到**，
      不是本页面的问题。写"无数据"会让运维去查前端/链路，方向错了。
    -->
    <el-empty
      v-if="!hasTempData && !hasFanData"
      description="设备未返回温度数据（HTEMP 应答格式与本驱动的解析器不一致，属固件/驱动侧待修项）"
      :image-size="60"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  // 后端实时数据可能尚未到达（此时为 null），组件内部已按 `!props.latestData` 兜底；
  // 类型上放开 null 以与实际运行时形态一致。
  latestData: Record<string, any> | null
}>()

/*
 * 温度字段（2026-10-21 收敛）：**不含 pv2_temp / dc_rectifier_temp**。
 * 生产数据实测（近 3 小时 362 点）：这两路只有 30 / 50 / 100 三个取值且互相完全同步，
 * 而其余 5 路是连续区间（31~45℃）—— 判据见 HistoryChartSection 温度组注释。
 * 展示一个恒定 30℃ / 跳变 100℃ 的「温度」比不展示更糟：用户会去查散热。
 * 驱动侧 HTEMP 位序需厂商文档确认后再恢复。
 */
const TEMP_FIELDS: Array<{ key: string; label: string }> = [
  { key: 'pv_temp', label: 'PV温度' },
  { key: 'inverter_temp', label: '逆变温度' },
  { key: 'boost_temp', label: '升压温度' },
  { key: 'transformer_temp', label: '变压器温度' },
  { key: 'max_temp', label: '最高温度' },
]

const hasTempData = computed(() => {
  const data = props.latestData
  if (!data) return false
  return TEMP_FIELDS.some(f => data[f.key] !== undefined)
})

const hasFanData = computed(() => {
  const data = props.latestData
  if (!data) return false
  return data.fan1_speed !== undefined || data.fan2_speed !== undefined ||
    data.fan1_status !== undefined || data.fan2_status !== undefined
})

const tempItems = computed(() => {
  const data = props.latestData
  if (!data) return []
  return TEMP_FIELDS
    .filter(f => data[f.key] !== undefined)
    .map(f => ({
      key: f.key,
      label: f.label,
      value: data[f.key],
    }))
})

const fanItems = computed(() => {
  if (!props.latestData) return []
  const fans: Array<{ key: string; label: string; speed: number | null; running: boolean }> = []
  for (let i = 1; i <= 2; i++) {
    const speed = props.latestData[`fan${i}_speed`]
    const status = props.latestData[`fan${i}_status`]
    if (speed !== undefined || status !== undefined) {
      fans.push({
        key: `fan${i}`,
        label: `风扇${i}`,
        speed: speed !== undefined ? Number(speed) : null,
        running: status !== undefined ? status === 1 || status > 0 : (speed !== undefined && speed > 0),
      })
    }
  }
  return fans
})

/**
 * 风扇转速档位（数值颜色与进度条填充**共用同一判据**，避免两处阈值漂移）：
 *   >80 danger / >50 warning / else success（> 判定，非 >=；50/80 走低一档）。
 */
function fanSpeedLevel(speed: number): 'danger' | 'warning' | 'success' {
  if (speed > 80) return 'danger'
  if (speed > 50) return 'warning'
  return 'success'
}
/** 进度条填充色：沿用本仓语义 token（明暗跟随主题），不写死十六进制 */
function fanSpeedColor(speed: number): string {
  return 'var(--color-' + fanSpeedLevel(speed) + ')'
}

/*
 * 温度分级（2026-10-21 修正）：原实现是 `>40℃ 就告警`，而逆变器正常工作时
 * PV/逆变/升压温度本来就在 40~55℃ 区间 —— 生产实测 PV温度 41℃、逆变 45℃
 * 全部被涂成黄色警告，用户截图的第一反应就是「温度有问题」。
 * 按逆变器行业惯例（过温保护通常 ≥75~80℃）重新分档：
 *   ≤60℃ 正常 / 60~75℃ 偏高（黄）/ >75℃ 危险（红）。
 * 说明：这里只影响**配色**，不参与任何告警判定；真正的过温告警由后端阈值规则产生。
 */
const TEMP_WARN_C = 60
const TEMP_DANGER_C = 75
function tempClass(v: any): string {
  const n = Number(v)
  if (isNaN(n)) return ''
  if (n > TEMP_DANGER_C) return 'temp-danger'
  if (n > TEMP_WARN_C) return 'temp-warning'
  return 'temp-normal'
}

function formatTemp(v: any): string {
  if (v === undefined || v === null || isNaN(v)) return '—'
  return `${Number(v).toFixed(1)}°C`
}
</script>

<style scoped>
.temp-card-container {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.section-title {
  margin: 0 0 12px;
  font-size: 14px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.temp-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 12px;
}

.temp-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  padding: 12px 8px;
  border-radius: 8px;
  border: 1px solid var(--el-border-color-lighter);
  background: var(--el-fill-color-lighter);
}

.temp-item.temp-normal {
  border-color: var(--el-color-success-light-5);
  background: var(--el-color-success-light-9);
}

.temp-item.temp-warning {
  border-color: var(--el-color-warning-light-5);
  background: var(--el-color-warning-light-9);
}

.temp-item.temp-danger {
  border-color: var(--el-color-danger-light-5);
  background: var(--el-color-danger-light-9);
}

.temp-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.temp-value {
  font-size: 18px;
  font-weight: 600;
}

.temp-normal .temp-value {
  color: var(--el-color-success);
}

.temp-warning .temp-value {
  color: var(--el-color-warning);
}

.temp-danger .temp-value {
  color: var(--el-color-danger);
}

.fan-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px;
}

.fan-item {
  padding: 12px;
  border-radius: 8px;
  border: 1px solid var(--el-border-color-lighter);
  background: var(--el-fill-color-lighter);
}

.fan-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}

/* header 右侧：转速百分比 + 运行状态 tag（tag 紧随数值之后，不再放条里） */
.fan-header-right {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 auto;
}

/* 转速百分比：18px/600 等宽数字，按档位着色（语义 token，明暗跟随主题） */
.fan-speed-value {
  font-size: 18px;
  font-weight: 600;
  line-height: 1;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  flex: 0 0 auto;
}

.fan-speed-success { color: var(--color-success); }
.fan-speed-warning { color: var(--color-warning); }
.fan-speed-danger { color: var(--color-danger); }

.fan-label {
  font-size: 13px;
  font-weight: 500;
}

.fan-speed-bar {
  margin-top: 4px;
}

/* 进度条只表达比例（8px 细条、全圆角），文字已移到 header 右侧 */
.fan-speed-bar :deep(.el-progress-bar__outer),
.fan-speed-bar :deep(.el-progress-bar__inner) {
  border-radius: 8px;
}

/*
  窄屏（≤768px）移动端压缩布局（2026-10-20，生产实测缺陷）。
  缺陷：旧 @media(max-width:480px) 把温度网格压成 **1 列**，7 个温度各占一整行，
  393px 下实测卡高 788px（组件根 688px），几乎全是竖排留白，温度一屏看不全。
  修法：
    · 温度 → 2 列网格（每格「标签 / 数值°C」上下排，min-height 48px），7 个值 4 行放下；
    · 风扇 → 仍是**一行一个风扇**（标签+状态在左、转速条占右侧剩余宽度），只收间距/内边距，
      行高 ≥44px；不隐藏任何数据（状态 tag 与转速条都保留）；
    · 温度级别配色由 .temp-normal/warning/danger 承载，本块**不重复定义颜色**，
      桌面与移动端共用同一套主题 token（明暗都跟随主题）。
  桌面（>768px）规则一律不动 —— 本块之外改前改后完全相同。
*/
@media (max-width: 768px) {
  .temp-card-container { gap: 6px; }

  .section-title { margin: 0 0 4px; font-size: 13px; line-height: 18px; }

  /* 温度：2 列 × 4 行（7 个值），每格 min-height 48px */
  .temp-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 2px; }
  .temp-item {
    min-height: 48px;
    padding: 4px 8px;
    gap: 2px;
    justify-content: center;
  }
  .temp-label { font-size: 12px; line-height: 16px; }
  .temp-value {
    font-size: 16px;
    line-height: 20px;
    /* 等宽数字：不同温度值宽度一致，2 列网格不会因数字抖动而重排 */
    font-variant-numeric: tabular-nums;
  }

  /* 风扇：保持行式布局（一行一个风扇，标签+状态在左、转速条占右侧），只压缩高度 */
  .fan-grid { grid-template-columns: 1fr; gap: 4px; }
  .fan-item {
    display: flex;
    flex-direction: row;
    align-items: center;
    gap: 8px;
    min-height: 38px;
    padding: 6px 10px;
  }
  /* header 仍是「标签左 / 数值+tag 右」：nowrap + 数值不可收缩 ⇒ 窄屏也不会换行或被挤掉 */
  .fan-header { flex: 0 0 auto; flex-wrap: nowrap; gap: 6px; margin-bottom: 0; }
  .fan-header-right { gap: 6px; }
  .fan-speed-value { font-size: 18px; line-height: 18px; }
  .fan-label { font-size: 12px; }
  .fan-speed-bar { flex: 1 1 auto; min-width: 0; margin-top: 0; }
}
</style>
