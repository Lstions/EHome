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

/*
  窄屏（≤768px）移动端压缩布局（2026-10-20，生产实测缺陷）。
  缺陷：393px 下 1 个 MPPT 通道占 170px —— 名称/状态一行 + 电压/电流/功率 **各占一整行**，
  卡高 230px（组件根 130px），与桌面信息密度差 3 倍。
  修法：名称+状态一行；电压/电流/功率 **三列一行**（每列标签在上、数值+单位在下）。
  本组件内层 el-card 是自己的子元素，可用 :deep(.el-card__body) 收紧内距（8px）。
  桌面（>768px）规则一律不动 —— 本块之外改前改后完全相同。
*/
@media (max-width: 768px) {
  .mppt-cards { gap: 8px; }
  .mppt-card { min-width: 0; flex-basis: 100%; }
  .mppt-card :deep(.el-card__body) { padding: 8px 10px; }

  .mppt-title { font-size: 13px; line-height: 18px; }
  .mppt-header { margin-bottom: 6px; }

  /* 三个电气量 → 一行三列（不再竖排 3 行） */
  .mppt-body { flex-direction: row; gap: 6px; }
  .mppt-metric { flex: 1 1 0; min-width: 0; flex-direction: column; align-items: flex-start; gap: 2px; }
  .mppt-label { font-size: 11px; line-height: 15px; }
  .mppt-value { font-size: 14px; line-height: 19px; font-variant-numeric: tabular-nums; }
  .mppt-value.power { font-size: 15px; line-height: 19px; }
}
</style>
