<template>
  <div class="mppt-cards" v-if="mpptChannels.length > 0">
    <el-card
      v-for="ch in mpptChannels"
      :key="ch.index"
      shadow="hover"
      class="mppt-card"
      :class="{ offline: !ch.online }"
    >
      <div class="mppt-header">
        <span class="mppt-title">MPPT{{ ch.index }}</span>
        <el-tag size="small" :type="ch.online ? 'success' : 'info'">
          {{ ch.online ? '运行中' : '断开' }}
        </el-tag>
      </div>
      <div class="mppt-body">
        <div class="mppt-metric">
          <span class="mppt-label">电压</span>
          <span class="mppt-value">{{ ch.voltage.toFixed(1) }}V</span>
        </div>
        <div class="mppt-metric">
          <span class="mppt-label">电流</span>
          <span class="mppt-value">{{ ch.current.toFixed(2) }}A</span>
        </div>
        <div class="mppt-metric">
          <span class="mppt-label">功率</span>
          <span class="mppt-value power">{{ formatPower(ch.power) }}</span>
        </div>
      </div>
    </el-card>
  </div>
  <el-empty v-else description="无MPPT通道数据" :image-size="60" />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatPower } from '@/utils/format'

const props = defineProps<{
  data: Record<string, number> | null
}>()

interface MpptChannel {
  index: number
  voltage: number
  current: number
  power: number
  online: boolean
}

const mpptChannels = computed<MpptChannel[]>(() => {
  if (!props.data) return []
  const channels: MpptChannel[] = []
  // Try to find mppt_1_*, mppt_2_* etc.
  for (let i = 1; i <= 4; i++) {
    const v = props.data[`pv${i}_voltage`] ?? props.data[`mppt${i}_voltage`]
    const c = props.data[`pv${i}_current`] ?? props.data[`mppt${i}_current`]
    const p = props.data[`pv${i}_power`] ?? props.data[`mppt${i}_power`]
    if (v !== undefined || c !== undefined || p !== undefined) {
      channels.push({
        index: i,
        voltage: v ?? 0,
        current: c ?? 0,
        power: p ?? 0,
        online: (p ?? 0) > 0 || (v ?? 0) > 0
      })
    }
  }
  return channels
})
</script>

<style scoped>
.mppt-cards { display: flex; gap: 16px; flex-wrap: wrap; }
.mppt-card { min-width: 200px; flex: 1 1 200px; }
.mppt-card.offline { opacity: 0.6; }
.mppt-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.mppt-title { font-weight: 600; font-size: 14px; }
.mppt-body { display: flex; flex-direction: column; gap: 8px; }
.mppt-metric { display: flex; justify-content: space-between; align-items: center; }
.mppt-label { font-size: 12px; color: var(--el-text-color-secondary); }
.mppt-value { font-size: 14px; font-weight: 500; }
.mppt-value.power { color: var(--el-color-success); font-size: 16px; }

@media (max-width: 480px) {
  .mppt-cards { gap: 10px; }
  .mppt-card { min-width: 0; flex-basis: 100%; }
  .mppt-card :deep(.el-card__body) { padding: 14px; }
}
</style>
