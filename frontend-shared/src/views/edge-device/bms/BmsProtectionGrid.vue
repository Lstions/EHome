<template>
  <StatusItemGrid
    :items="protectionItems"
    active-text="触发"
    inactive-text="正常"
    empty-text="无保护状态数据"
    :icon-size="20"
  >
    <template #summary>
      <div v-if="hasProtectionData" class="protection-summary">
        <el-tag type="success" size="small" effect="dark">保护状态正常 (bitmask=0)</el-tag>
      </div>
      <el-empty v-else description="无保护状态数据" :image-size="60" />
    </template>
  </StatusItemGrid>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import StatusItemGrid from '@/components/common/StatusItemGrid.vue'
import type { StatusItem } from '@/components/common/StatusItemGrid.vue'

const props = defineProps<{
  data: Record<string, number> | null
}>()

// Jiabaida protection_status is a 16-bit bitmask.
// Bit mapping (per jiabaida protocol):
// bit0: 单体过压, bit1: 单体欠压, bit2: 整组过压, bit3: 整组欠压
// bit4: 充电过温, bit5: 充电低温, bit6: 放电过温, bit7: 放电低温
// bit8: 充电过流, bit9: 放电过流, bit10: 短路, bit11: 前端IC错误
const PROTECTION_BITS: Array<{ bit: number; label: string }> = [
  { bit: 0, label: '单体过压' },
  { bit: 1, label: '单体欠压' },
  { bit: 2, label: '整组过压' },
  { bit: 3, label: '整组欠压' },
  { bit: 4, label: '充电过温' },
  { bit: 5, label: '充电低温' },
  { bit: 6, label: '放电过温' },
  { bit: 7, label: '放电低温' },
  { bit: 8, label: '充电过流' },
  { bit: 9, label: '放电过流' },
  { bit: 10, label: '短路保护' },
  { bit: 11, label: '前端IC错误' },
]

const hasProtectionData = computed(() => {
  if (!props.data) return false
  return props.data.protection_status !== undefined
})

const protectionItems = computed<StatusItem[]>(() => {
  if (!props.data) return []

  // If we have the bitmask field, decode it
  if (props.data.protection_status !== undefined) {
    const bitmask = Math.floor(props.data.protection_status)
    if (bitmask === 0) return []  // All clear — show summary tag
    return PROTECTION_BITS
      .filter(p => (bitmask & (1 << p.bit)) !== 0)
      .map(p => ({ key: `bit${p.bit}`, label: p.label, active: true }))
  }

  // Fallback: check individual protection_* fields
  const PROTECTION_FIELDS: Array<{ key: string; label: string }> = [
    { key: 'protection_overvoltage', label: '过压保护' },
    { key: 'protection_undervoltage', label: '欠压保护' },
    { key: 'protection_overcurrent_charge', label: '充电过流' },
    { key: 'protection_overcurrent_discharge', label: '放电过流' },
    { key: 'protection_overtemp_charge', label: '充电过温' },
    { key: 'protection_overtemp_discharge', label: '放电过温' },
    { key: 'protection_short_circuit', label: '短路保护' },
  ]
  return PROTECTION_FIELDS
    .filter(f => props.data![f.key] !== undefined)
    .map(f => ({
      key: f.key,
      label: f.label,
      active: props.data![f.key] === 1 || props.data![f.key] > 0
    }))
})
</script>

<style scoped>
.protection-summary {
  grid-column: 1 / -1;
  text-align: center;
  padding: 12px;
}
</style>
