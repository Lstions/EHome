<template>
  <!-- UI-4（2026-10-15）：改前每项用 el-tag effect="dark"（深底白字）+ Open/Lock 图标，
       视觉比同页其它指标重一档，且 4 个色块并排像一组按钮（用户反馈"设计水平差"）。
       现在改为与页面其它指标一致的**轻量表达**：
         · 状态用「文字 + 语义色」双表达（规范 §4.2.2），不用深色 tag；
         · 导通用成功色文字，关断用中性色 —— 关断不是故障，**不得**用 danger（§4.2.1）；
         · 图标只作第三重辅助，尺寸与指标卡一致（18px）。 -->
  <div class="bms-mos-status">
    <div class="mos-item" :class="{ on: chargeOn }">
      <el-icon :size="18" :color="chargeOn ? 'var(--color-success)' : 'var(--color-info)'">
        <component :is="chargeOn ? Open : Lock" />
      </el-icon>
      <span class="mos-label">充电 MOS</span>
      <span class="mos-state" :class="chargeOn ? 'is-on' : 'is-off'">{{ chargeOn ? '导通' : '关断' }}</span>
    </div>
    <div class="mos-item" :class="{ on: dischargeOn }">
      <el-icon :size="18" :color="dischargeOn ? 'var(--color-success)' : 'var(--color-info)'">
        <component :is="dischargeOn ? Open : Lock" />
      </el-icon>
      <span class="mos-label">放电 MOS</span>
      <span class="mos-state" :class="dischargeOn ? 'is-on' : 'is-off'">{{ dischargeOn ? '导通' : '关断' }}</span>
    </div>
    <div v-if="balanceOn" class="mos-item" :class="{ on: true }">
      <el-icon :size="18" color="var(--color-success)"><Open /></el-icon>
      <span class="mos-label">均衡</span>
      <span class="mos-state is-on">进行中</span>
    </div>
    <div v-if="!hasFetStatus" class="mos-item is-unknown">
      <el-icon :size="18" color="var(--color-info)"><Lock /></el-icon>
      <span class="mos-label">无 FET 状态数据</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Open, Lock } from '@element-plus/icons-vue'

const props = defineProps<{
  data: Record<string, number> | null
}>()

// fet_status bitmask: bit0 = charge MOS, bit1 = discharge MOS
// value 0 = both OFF, 1 = charge ON, 2 = discharge ON, 3 = both ON
const hasFetStatus = computed(() => {
  if (!props.data) return false
  return props.data.fet_status !== undefined || props.data.mos_charge !== undefined
})

const chargeOn = computed(() => {
  if (!props.data) return false
  // Check fet_status bitmask first, then fallback to explicit fields
  if (props.data.fet_status !== undefined) return (props.data.fet_status & 1) !== 0
  return props.data.mos_charge === 1 || props.data.mos_charge_on === 1 || props.data.charge_mos === 1
})

const dischargeOn = computed(() => {
  if (!props.data) return false
  if (props.data.fet_status !== undefined) return (props.data.fet_status & 2) !== 0
  return props.data.mos_discharge === 1 || props.data.mos_discharge_on === 1 || props.data.discharge_mos === 1
})

const balanceOn = computed(() => {
  if (!props.data) return false
  return props.data.balancing === 1 || props.data.balance === 1 || props.data.mos_balance === 1
})
</script>

<style scoped>
/* UI-4（2026-10-15）：与页面其它指标一致的行式表达。
   改前是 4 个带 2px 边框 + 深色 tag 的色块并排，视觉重量远超同页任何指标；
   现在每项是「图标 + 标签 + 状态文字」一行，靠语义色而非色块传达状态。 */
.bms-mos-status {
  display: flex;
  gap: 8px 28px;
  padding: 2px 0;
  flex-wrap: wrap;
}
.mos-item {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.mos-item.is-unknown { opacity: 0.7; }
.mos-label {
  font-size: 13px;
  color: var(--el-text-color-regular);
}
/* 状态文字：文字 + 颜色双表达（规范 §4.2.2）。
   ⚠ 关断用**中性色**而非 danger —— 关断是正常工况，不是故障（规范 §4.2.1：
   LOW/OFF/停止/未选中不得使用 danger）。 */
.mos-state {
  font-size: 13px;
  font-weight: 600;
  white-space: nowrap;
}
.mos-state.is-on { color: var(--el-color-success); }
.mos-state.is-off { color: var(--el-text-color-secondary); }

@media (max-width: 768px) {
  .bms-mos-status {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px 12px;
  }

  .mos-item { min-width: 0; gap: 6px; }
  .mos-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
}
</style>
