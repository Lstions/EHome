<template>
  <div class="bms-protection-grid">
    <template v-if="protectionItems.length > 0">
      <div
        v-for="item in protectionItems"
        :key="item.key"
        class="protection-item"
        :class="{ active: item.active }"
      >
        <el-icon :size="20" :color="item.active ? THEME_COLORS.danger : THEME_COLORS.success">
          <component :is="item.active ? WarningFilled : CircleCheck" />
        </el-icon>
        <span class="protection-label">{{ item.label }}</span>
        <el-tag size="small" :type="item.active ? 'danger' : 'success'" effect="plain">
          {{ item.active ? '触发' : '正常' }}
        </el-tag>
      </div>
    </template>
    <div v-if="protectionItems.length === 0 && hasProtectionData" class="protection-summary">
      <el-tag type="success" size="small" effect="dark">保护状态正常 (bitmask=0)</el-tag>
    </div>
    <div v-if="!hasProtectionData" class="no-protection">
      <el-empty description="无保护状态数据" :image-size="60" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { WarningFilled, CircleCheck } from '@element-plus/icons-vue'
import { THEME_COLORS } from '@/utils/theme'

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

const protectionItems = computed(() => {
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
.bms-protection-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 12px;
  padding: 8px 0;
}
.protection-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
  border: 1px solid var(--el-border-color-lighter);
}
.protection-item.active {
  background: var(--el-color-danger-light-9);
  border-color: var(--el-color-danger-light-5);
}
.protection-label {
  flex: 1;
  font-size: 13px;
}
.protection-summary {
  grid-column: 1 / -1;
  text-align: center;
  padding: 12px;
}
.no-protection {
  grid-column: 1 / -1;
}

@media (max-width: 768px) {
  .bms-protection-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
    padding: 2px 0;
  }

  .protection-item {
    min-width: 0;
    gap: 6px;
    padding: 8px;
  }

  .protection-label {
    min-width: 0;
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .protection-item :deep(.el-tag) {
    flex-shrink: 0;
  }
}
</style>
