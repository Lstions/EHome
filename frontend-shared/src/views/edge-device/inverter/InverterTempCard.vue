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
          <div class="fan-header">
            <span class="fan-label">{{ fan.label }}</span>
            <el-tag :type="fan.running ? 'success' : 'info'" effect="plain" size="small">
              {{ fan.running ? '运行' : '停止' }}
            </el-tag>
          </div>
          <div class="fan-speed-bar" v-if="fan.speed !== null">
            <el-progress
              :percentage="fan.speed"
              :color="fan.speed > 80 ? 'var(--color-danger)' : fan.speed > 50 ? 'var(--color-warning)' : 'var(--color-success)'"
              :stroke-width="14"
              :text-inside="true"
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

const TEMP_FIELDS: Array<{ key: string; label: string }> = [
  { key: 'pv_temp', label: 'PV温度' },
  { key: 'inverter_temp', label: '逆变温度' },
  { key: 'boost_temp', label: '升压温度' },
  { key: 'transformer_temp', label: '变压器温度' },
  { key: 'max_temp', label: '最高温度' },
  { key: 'pv2_temp', label: 'PV2温度' },
  { key: 'dc_rectifier_temp', label: 'DC整流温度' },
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

function tempClass(v: any): string {
  const n = Number(v)
  if (isNaN(n)) return ''
  if (n > 60) return 'temp-danger'
  if (n > 40) return 'temp-warning'
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

.fan-label {
  font-size: 13px;
  font-weight: 500;
}

.fan-speed-bar {
  margin-top: 4px;
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
  .fan-header { flex: 0 0 auto; gap: 6px; margin-bottom: 0; }
  .fan-label { font-size: 12px; }
  .fan-speed-bar { flex: 1 1 auto; min-width: 0; margin-top: 0; }
}
</style>
