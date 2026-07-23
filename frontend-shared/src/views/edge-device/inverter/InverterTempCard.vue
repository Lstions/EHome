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
              :color="fan.speed > 80 ? THEME_COLORS.danger : fan.speed > 50 ? THEME_COLORS.warning : THEME_COLORS.success"
              :stroke-width="14"
              :text-inside="true"
            />
          </div>
        </div>
      </div>
    </div>

    <el-empty v-if="!hasTempData && !hasFanData" description="无温度/风扇数据" :image-size="60" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { THEME_COLORS } from '@/utils/theme'

const props = defineProps<{
  latestData: Record<string, any>
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
  if (!props.latestData) return false
  return TEMP_FIELDS.some(f => props.latestData[f.key] !== undefined)
})

const hasFanData = computed(() => {
  if (!props.latestData) return false
  return props.latestData.fan1_speed !== undefined || props.latestData.fan2_speed !== undefined ||
    props.latestData.fan1_status !== undefined || props.latestData.fan2_status !== undefined
})

const tempItems = computed(() => {
  if (!props.latestData) return []
  return TEMP_FIELDS
    .filter(f => props.latestData[f.key] !== undefined)
    .map(f => ({
      key: f.key,
      label: f.label,
      value: props.latestData[f.key],
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
  if (v === undefined || v === null || isNaN(v)) return '--'
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

@media (max-width: 480px) {
  .temp-grid, .fan-grid { grid-template-columns: 1fr; gap: 8px; }
  .temp-item, .fan-item { padding: 10px; }
}
</style>
