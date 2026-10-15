<template>
  <!-- UI-4（2026-10-15）：改前"无保护"时渲染一个**居中大绿 tag**（effect=dark）
       「保护状态正常 (bitmask=0)」，上下各留一大片空白 —— 一张卡为了说一句
       "一切正常"占掉整屏（用户截图复现）。
       现在：正常时用**紧凑一行**（图标 + 文字 + 位掩码作为辅助信息），
       有保护触发时才展开明细网格。信息量决定占位，不是反过来。 -->
  <!-- 分支顺序即语义优先级：有明细（位掩码命中**或**回退字段命中）→ 展开网格；
       有位掩码且为 0 → 一行结论；连位掩码都没有 → 空态。
       ⚠ 不能把 hasProtectionData 放在最前：那样"只有 protection_* 回退字段、
       没有 protection_status"的数据（旧固件/其它厂商）会被判成空态，
       把**已上报的保护**显示成"无数据"。 -->
  <StatusItemGrid
    v-if="protectionItems.length > 0"
    :items="protectionItems"
    active-text="触发"
    inactive-text="正常"
    empty-text="无保护状态数据"
    :icon-size="20"
  />
  <div v-else-if="hasProtectionData" class="protection-summary protection-ok" role="status">
    <el-icon :size="18" color="var(--color-success)"><CircleCheck /></el-icon>
    <span class="protection-ok-text">保护状态正常</span>
    <!-- 位掩码是**辅助信息**：给需要核对原始值的运维看，不与结论抢视觉（规范 §4.2.4）。 -->
    <span class="protection-ok-meta">bitmask=0 · 12 项保护均未触发</span>
  </div>
  <div v-else class="protection-empty">
    <el-empty description="无保护状态数据" :image-size="60" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CircleCheck } from '@element-plus/icons-vue'
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
/* 正常态：紧凑一行，不撑高卡片。
   （保留 .protection-summary 类名：既有 spec 用它断言"正常态摘要存在"。） */
.protection-ok {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 4px 0;
}
.protection-ok-text { font-size: 13px; font-weight: 500; color: var(--el-text-color-primary); }
/* 辅助信息降级：小字、次级色，与结论留出间距（规范 §4.2.4）。 */
.protection-ok-meta { font-size: 12px; color: var(--el-text-color-secondary); }
.protection-empty { padding: 4px 0; }
</style>
